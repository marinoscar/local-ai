package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// tmpInfix separates a file name from the random suffix of its temp file. Open removes leftovers of
// this shape after a crash.
const tmpInfix = ".tmp-"

// WriteFileAtomic replaces path with data so that a reader (or a power cut) sees either the complete
// old file or the complete new one, never a mix (agent.md section 3.3). Steps: create
// path.tmp-<rand> in the same directory (O_EXCL, 0600), write, chown/chmod, fsync, close, rename over
// path, fsync the directory. uid or gid < 0 leaves that owner unchanged. On any failure before the
// rename the temp file is removed and path is untouched. If the final directory fsync fails the new
// file is in place but not known to be durable, and the error is returned.
func WriteFileAtomic(fsys FS, path string, data []byte, mode os.FileMode, uid, gid int) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("state: random temp suffix: %w", err)
	}
	tmp := path + tmpInfix + hex.EncodeToString(suffix)
	f, err := fsys.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("state: create temp file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = fsys.Remove(tmp)
		}
	}()
	if n, werr := f.Write(data); werr != nil || n != len(data) {
		_ = f.Close()
		if werr == nil {
			werr = fmt.Errorf("short write (%d of %d bytes)", n, len(data))
		}
		return fmt.Errorf("state: write temp file: %w", werr)
	}
	if uid >= 0 || gid >= 0 {
		if cerr := f.Chown(uid, gid); cerr != nil {
			_ = f.Close()
			return fmt.Errorf("state: chown temp file: %w", cerr)
		}
	}
	if cerr := f.Chmod(mode); cerr != nil {
		_ = f.Close()
		return fmt.Errorf("state: chmod temp file: %w", cerr)
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return fmt.Errorf("state: fsync temp file: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("state: close temp file: %w", cerr)
	}
	if rerr := fsys.Rename(tmp, path); rerr != nil {
		return fmt.Errorf("state: rename temp file: %w", rerr)
	}
	renamed = true
	if derr := fsys.SyncDir(filepath.Dir(path)); derr != nil {
		return fmt.Errorf("state: fsync directory: %w", derr)
	}
	return nil
}
