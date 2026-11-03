package resolve

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Bounds of the safe extractor (ark-sync.md section 6: archive extraction rejects path traversal,
// absolute paths, symlinks out of the tree, hard links and device nodes). The numbers are far above
// a docs and catalog tree and far below anything that could fill a disk by accident.
const (
	maxTarEntries = 50000
	maxTarFile    = 32 << 20  // one extracted file
	maxTarTotal   = 256 << 20 // all extracted bytes
	maxTarRaw     = 1 << 30   // all bytes read, extracted or skipped (a gzip bomb stops here)
)

// ErrUnsafeArchive is wrapped by every refusal of the extractor.
var ErrUnsafeArchive = errors.New("resolve: the archive is not safe to unpack")

// tarRule says which entries are wanted: a slash path below the archive's top directory (a prefix
// "docs/" or an exact file "inventory.example.yaml"), where it goes, and a filter on the part after
// the prefix.
type tarRule struct {
	// Prefix is "docs/" (everything below) or an exact path such as "inventory.example.yaml".
	Prefix string
	// Dest is the directory (for a "dir/" prefix) or the file path (for an exact one) under the
	// destination root.
	Dest string
	// Allow, when set, is asked for the part after the prefix and may refuse an entry (the entry is
	// then skipped, not an error).
	Allow func(rel string) bool
}

func (r tarRule) dir() bool { return strings.HasSuffix(r.Prefix, "/") }

// extractTar unpacks the wanted entries of a (gzipped) tar archive into root. It reads the archive as
// one stream: every entry's path must be clean, relative and below one common top directory, whether
// the entry is wanted or not (a hostile name anywhere refuses the whole archive); a symlink, hard
// link, device or fifo inside a wanted path refuses it too, and outside one is ignored; sizes,
// counts and the raw byte total are bounded. Wanted regular files are written 0644 with O_TRUNC
// (so a retry after a crash overwrites its own leftovers), never through a symlink.
//
// It returns the names (below root, slash separated) of the files written.
func extractTar(r io.Reader, root string, rules []tarRule) ([]string, error) {
	br := bufio.NewReader(io.LimitReader(r, maxTarRaw))
	var src io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%w: not a readable gzip stream", ErrUnsafeArchive)
		}
		defer zr.Close()
		src = io.LimitReader(zr, maxTarRaw)
	}
	tr := tar.NewReader(src)
	var (
		top     string
		entries int
		total   int64
		written []string
	)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnsafeArchive, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if entries++; entries > maxTarEntries {
			return nil, fmt.Errorf("%w: more than %d entries", ErrUnsafeArchive, maxTarEntries)
		}
		rel, err := entryPath(h.Name, &top)
		if err != nil {
			return nil, err
		}
		if rel == "" { // the top directory itself
			continue
		}
		rule, sub, wanted := matchRule(rules, rel, h.Typeflag == tar.TypeDir)
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			if wanted {
				return nil, fmt.Errorf("%w: %s is a link or a special file", ErrUnsafeArchive, rel)
			}
			continue
		default:
			if wanted {
				return nil, fmt.Errorf("%w: %s has an unsupported entry type", ErrUnsafeArchive, rel)
			}
			continue
		}
		if !wanted {
			continue
		}
		if rule.Allow != nil && !rule.Allow(sub) {
			continue
		}
		if h.Size < 0 || h.Size > maxTarFile {
			return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsafeArchive, rel, maxTarFile)
		}
		if total += h.Size; total > maxTarTotal {
			return nil, fmt.Errorf("%w: more than %d bytes to extract", ErrUnsafeArchive, maxTarTotal)
		}
		dest := rule.Dest
		if rule.dir() {
			dest = path.Join(rule.Dest, sub)
		}
		if !inside(dest) {
			return nil, fmt.Errorf("%w: %s maps outside the destination", ErrUnsafeArchive, rel)
		}
		if err := writeExtracted(root, dest, tr, h.Size); err != nil {
			return nil, err
		}
		written = append(written, dest)
	}
	return written, nil
}

// entryPath validates one entry name and returns it relative to the archive's single top directory
// ("" for the top directory itself).
func entryPath(name string, top *string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %q %s", ErrUnsafeArchive, Clip([]byte(name), 80), why)
	}
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return bad("is not a usable name")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return bad("has a control character")
		}
	}
	if strings.HasPrefix(name, "/") {
		return bad("is an absolute path")
	}
	for _, el := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if el == ".." {
			return bad("climbs out of the archive")
		}
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	first, rest, hasRest := strings.Cut(clean, "/")
	if *top == "" {
		*top = first
	}
	if first != *top {
		return bad("is not below the archive's one top directory")
	}
	if !hasRest {
		return "", nil
	}
	return rest, nil
}

// matchRule finds the rule that wants rel and the part below its prefix.
func matchRule(rules []tarRule, rel string, isDir bool) (tarRule, string, bool) {
	for _, r := range rules {
		if r.dir() {
			if strings.HasPrefix(rel, r.Prefix) && len(rel) > len(r.Prefix) {
				return r, strings.TrimPrefix(rel, r.Prefix), true
			}
			continue
		}
		if rel == r.Prefix && !isDir {
			return r, "", true
		}
	}
	return tarRule{}, "", false
}

// writeExtracted writes one file below root, creating its directories, never following a symlink.
func writeExtracted(root, rel string, src io.Reader, size int64) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %s: %v", ErrUnsafeArchive, rel, err)
	}
	// Nothing below root may be a symlink: it would redirect a later write.
	for p := dir; len(p) > len(root); p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%w: %s sits behind a link or a file", ErrUnsafeArchive, rel)
		}
	}
	if fi, err := os.Lstat(full); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s exists and is not a regular file", ErrUnsafeArchive, rel)
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("%w: cannot write %s: %v", ErrUnsafeArchive, rel, err)
	}
	n, err := io.Copy(f, io.LimitReader(src, size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w: writing %s: %v", ErrUnsafeArchive, rel, err)
	}
	if n != size {
		return fmt.Errorf("%w: %s has an inconsistent size", ErrUnsafeArchive, rel)
	}
	return nil
}
