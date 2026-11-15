// Package untar unpacks a .tar.gz of a program tree (the music runtime's python-build-standalone
// interpreter, #511) in-process, without the tar program.
//
// Why not tar: GNU tar 1.35 (Ubuntu 26.04) opens every path with openat2(RESOLVE_BENEATH) and has no
// fallback, and the agent's unit has RestrictSUIDSGID=yes, whose seccomp filter answers openat2 with
// ENOSYS. Every `tar -x` run by the agent then fails ("Cannot open: Function not implemented").
//
// Unlike the sync engine's extractor (sync/resolve, which keeps staged trees to regular files), a
// program tree needs its symbolic links (bin/python3 -> python3.12). The rules:
//   - an absolute name, a `..` element, a backslash or a control character refuses the archive;
//   - directories, regular files and symbolic links are written; a hard link, a device, a fifo or any
//     other entry refuses the archive;
//   - a symbolic link must be relative and, resolved from its own directory, stay inside dest;
//   - nothing is ever written through a symbolic link: every directory on the way must be a real one;
//   - an entry that already exists refuses the archive (dest should be fresh);
//   - modes keep only the permission bits (never setuid, setgid or sticky);
//   - entries, one file's size and the total size are bounded.
package untar

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsafe is wrapped by every refusal.
var ErrUnsafe = errors.New("untar: unsafe archive")

// Limits bound one extraction; a zero field takes its default.
type Limits struct {
	MaxEntries                  int
	MaxFileBytes, MaxTotalBytes int64
}

const (
	defaultEntries = 100_000
	defaultFile    = 2 << 30
	defaultTotal   = 8 << 30
)

func (l Limits) withDefaults() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = defaultEntries
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = defaultFile
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = defaultTotal
	}
	return l
}

// Stats says what an extraction wrote.
type Stats struct {
	Dirs, Files, Symlinks int
	Bytes                 int64
}

// ExtractTarGz unpacks the gzip-compressed tar at src into the existing directory dest. On an error,
// what was written stays in dest for the caller to remove.
func ExtractTarGz(ctx context.Context, src, dest string, lim Limits) (Stats, error) {
	f, err := os.Open(src)
	if err != nil {
		return Stats{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrUnsafe, err)
	}
	defer gz.Close()
	return Extract(ctx, gz, dest, lim)
}

// Extract unpacks an uncompressed tar stream into the existing directory dest.
func Extract(ctx context.Context, r io.Reader, dest string, lim Limits) (Stats, error) {
	lim = lim.withDefaults()
	var st Stats
	fi, err := os.Lstat(dest)
	if err != nil {
		return st, err
	}
	if !fi.IsDir() {
		return st, fmt.Errorf("untar: %s is not a directory", dest)
	}
	tr := tar.NewReader(r)
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			return st, fmt.Errorf("%w: %v", ErrUnsafe, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if entries++; entries > lim.MaxEntries {
			return st, fmt.Errorf("%w: more than %d entries", ErrUnsafe, lim.MaxEntries)
		}
		rel, err := entryPath(h.Name)
		if err != nil {
			return st, err
		}
		if rel == "" {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return st, fmt.Errorf("%w: an entry with no name", ErrUnsafe)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := mkdirs(dest, rel); err != nil {
				return st, err
			}
			st.Dirs++
		case tar.TypeReg:
			if h.Size < 0 || h.Size > lim.MaxFileBytes {
				return st, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsafe, rel, lim.MaxFileBytes)
			}
			if st.Bytes+h.Size > lim.MaxTotalBytes {
				return st, fmt.Errorf("%w: more than %d bytes in all", ErrUnsafe, lim.MaxTotalBytes)
			}
			if err := writeFile(dest, rel, tr, h.Size, os.FileMode(h.Mode)&0o777); err != nil {
				return st, err
			}
			st.Files++
			st.Bytes += h.Size
		case tar.TypeSymlink:
			if err := symlink(dest, rel, h.Linkname); err != nil {
				return st, err
			}
			st.Symlinks++
		case tar.TypeLink:
			return st, fmt.Errorf("%w: %s is a hard link", ErrUnsafe, rel)
		default:
			return st, fmt.Errorf("%w: %s is a device, a fifo or an unsupported entry", ErrUnsafe, rel)
		}
	}
}

// entryPath is the clean slash path of an entry below the root ("" for the root itself).
func entryPath(name string) (string, error) {
	if strings.ContainsAny(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: %q is not a relative slash path", ErrUnsafe, clip(name))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %q has a control character", ErrUnsafe, clip(name))
		}
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	for _, el := range strings.Split(trimmed, "/") {
		if el == ".." {
			return "", fmt.Errorf("%w: %q leaves the folder", ErrUnsafe, clip(name))
		}
	}
	return path.Clean(trimmed), nil
}

// mkdirs makes every directory of rel below dest, refusing to pass through anything but a real
// directory.
func mkdirs(dest, rel string) error {
	cur := dest
	for _, el := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, el)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			continue
		case err == nil:
			return fmt.Errorf("%w: %s passes through a link or a file", ErrUnsafe, rel)
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// parent makes the directories above rel and returns the entry's own absolute path, which must not exist.
func parent(dest, rel string) (string, error) {
	if dir := path.Dir(rel); dir != "." {
		if err := mkdirs(dest, dir); err != nil {
			return "", err
		}
	}
	p := filepath.Join(dest, filepath.FromSlash(rel))
	if _, err := os.Lstat(p); err == nil {
		return "", fmt.Errorf("%w: %s is in the archive twice or already on disk", ErrUnsafe, rel)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return p, nil
}

func writeFile(dest, rel string, src io.Reader, size int64, mode os.FileMode) error {
	p, err := parent(dest, rel)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(src, size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("%w: %s is cut short", ErrUnsafe, rel)
	}
	if err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

func symlink(dest, rel, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\") {
		return fmt.Errorf("%w: the link %s points to %q, not a relative path", ErrUnsafe, rel, clip(target))
	}
	// Resolved lexically from the link's own folder, the target must stay inside dest.
	resolved := path.Clean(path.Join(path.Dir(rel), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%w: the link %s points outside the folder", ErrUnsafe, rel)
	}
	p, err := parent(dest, rel)
	if err != nil {
		return err
	}
	return os.Symlink(target, p)
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
