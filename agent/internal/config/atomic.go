package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrNotDurable wraps the error of the final directory fsync. The rename already happened, so the
// new content is in place; only its survival across a power cut is not guaranteed.
var ErrNotDurable = errors.New("config: file replaced but the directory sync failed")

// tempMarker separates the target name from the random suffix of a temp file.
const tempMarker = ".tmp-"

// WriteFileAtomic replaces path with data so that a reader (or a crash) sees the old file or the
// new one, never a mix (agent.md 3.3): it writes path.tmp-<rand> in the same directory, fsyncs it,
// chowns and chmods it, renames it over path and fsyncs the directory. uid or gid of -1 leaves that
// id alone. On any failure the temp file is removed and path is untouched.
func WriteFileAtomic(path string, data []byte, mode os.FileMode, uid, gid int) error {
	return WriteFileAtomicFS(OSFS{}, path, data, mode, uid, gid)
}

// WriteFileAtomicFS is WriteFileAtomic on an injected FS.
func WriteFileAtomicFS(fsys FS, path string, data []byte, mode os.FileMode, uid, gid int) (err error) {
	f, tmp, err := createTemp(fsys, path)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if err != nil && !errors.Is(err, ErrNotDurable) {
			if !closed {
				_ = f.Close()
			}
			_ = fsys.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if uid >= 0 || gid >= 0 {
		if err = f.Chown(uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", tmp, err)
		}
	}
	if err = f.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	closed = true
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = fsys.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	if serr := fsys.SyncDir(filepath.Dir(path)); serr != nil && !errors.Is(serr, syscall.EINVAL) && !errors.Is(serr, syscall.ENOTSUP) {
		err = fmt.Errorf("%w: %v", ErrNotDurable, serr)
	}
	return err
}

// createTemp opens a fresh path.tmp-<rand> with O_EXCL, owner-only until the final chmod.
func createTemp(fsys FS, path string) (File, string, error) {
	for try := 0; try < 8; try++ {
		var b [6]byte
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return nil, "", fmt.Errorf("random temp name: %w", err)
		}
		name := path + tempMarker + hex.EncodeToString(b[:])
		f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("create temp file next to %s: %w", path, err)
		}
	}
	return nil, "", fmt.Errorf("create temp file next to %s: too many name collisions", path)
}

// SweepTemps removes leftover path.tmp-* files, the debris of a process that died between writing
// and renaming. It returns the names it removed. Call it at start, never while a write to path may
// be in flight.
func SweepTemps(fsys FS, path string) []string {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), base+tempMarker) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if fsys.Remove(full) == nil {
			removed = append(removed, full)
		}
	}
	return removed
}
