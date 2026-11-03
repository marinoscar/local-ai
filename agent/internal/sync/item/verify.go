package item

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hashBufSize is the read buffer of a verification: 1 MiB, the downloader's block size, so a cancel
// lands at the same granularity.
const hashBufSize = 1 << 20

// maxTreeDepth bounds the walk of a directory item.
const maxTreeDepth = 32

// syncFile makes a file's data durable (fsync of a file opened read-only is enough on Linux).
func syncFile(fsys FS, name string) error {
	f, err := fsys.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	serr := f.Sync()
	if cerr := f.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// hashFile reads the whole file from disk and returns its sha256 and size. progress, when set, is
// called with the bytes read so far after every block. It stops with the context's error at the next
// block boundary.
func hashFile(ctx context.Context, fsys FS, name string, progress func(n int64)) (sum string, size int64, err error) {
	r, err := fsys.OpenRead(name)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	buf := make([]byte, hashBufSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			size += int64(n)
			if progress != nil {
				progress(size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", size, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// verifyFile is the verifying step of one file: fsync, drop the cache, and (when reread is on) read
// the whole file back from disk and hash it. It returns the digest of what is on disk. With reread
// off the digest is empty and the caller keeps the downloader's.
func verifyFile(ctx context.Context, fsys FS, name string, reread bool, progress func(n int64)) (sum string, size int64, err error) {
	if err := syncFile(fsys, name); err != nil {
		return "", 0, fmt.Errorf("fsync %s: %w", filepath.Base(name), err)
	}
	if !reread {
		info, err := fsys.Stat(name)
		if err != nil {
			return "", 0, err
		}
		return "", info.Size(), nil
	}
	_ = fsys.DropCache(name) // best effort: a failed fadvise only means the re-read may be cached
	return hashFile(ctx, fsys, name, progress)
}

// TreeFile is one file of a staged directory tree, hashed from disk.
type TreeFile struct {
	// Path is relative to the tree, slash separated.
	Path   string
	SHA256 string
	Size   int64
}

// ErrNotRegular: a directory item's tree holds a symlink, a device or another special file. Sync
// never stages one (the archive rules of ark-sync.md section 6).
var ErrNotRegular = errors.New("item: the tree holds a file that is not a regular file")

// walkTree lists the regular files below dir (relative slash paths, sorted), refusing anything else.
// The SHA256SUMS file of the tree itself is skipped.
func walkTree(fsys FS, dir string) ([]string, error) {
	var out []string
	var walk func(rel string, depth int) error
	walk = func(rel string, depth int) error {
		if depth > maxTreeDepth {
			return fmt.Errorf("%w: %s is nested too deep", ErrNotRegular, rel)
		}
		ents, err := fsys.ReadDir(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, e := range ents {
			name := e.Name()
			r := name
			if rel != "" {
				r = rel + "/" + name
			}
			info, err := fsys.Lstat(filepath.Join(dir, filepath.FromSlash(r)))
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				if err := walk(r, depth+1); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if r == "SHA256SUMS" || strings.HasPrefix(name, "SHA256SUMS.tmp-") || (strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-")) {
					continue
				}
				out = append(out, r)
			default:
				return fmt.Errorf("%w: %s", ErrNotRegular, r)
			}
		}
		return nil
	}
	if err := walk("", 0); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// hashTree hashes every file of a staged directory tree from disk, in path order.
func hashTree(ctx context.Context, fsys FS, dir string, progress func(n int64)) ([]TreeFile, error) {
	paths, err := walkTree(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []TreeFile
	var total int64
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := syncFile(fsys, full); err != nil {
			return nil, fmt.Errorf("fsync %s: %w", p, err)
		}
		_ = fsys.DropCache(full)
		base := total
		sum, size, err := hashFile(ctx, fsys, full, func(n int64) {
			if progress != nil {
				progress(base + n)
			}
		})
		if err != nil {
			return nil, err
		}
		total += size
		out = append(out, TreeFile{Path: p, SHA256: sum, Size: size})
	}
	return out, nil
}

// exists reports whether a path exists (any kind), following nothing.
func exists(fsys FS, name string) bool {
	_, err := fsys.Lstat(name)
	return err == nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
