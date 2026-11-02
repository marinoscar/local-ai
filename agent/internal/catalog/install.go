package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FS is the filesystem seam of Install and Recover (agent.md 3.2: every external dependency sits
// behind an interface). OSFS is the real one; the tests wrap it with fault injection to prove that
// a crash at any step leaves a usable catalog.
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	// WriteFile creates or truncates name, writes data and fsyncs it before returning.
	WriteFile(name string, data []byte, perm os.FileMode) error
	Rename(oldpath, newpath string) error
	RemoveAll(path string) error
	// SyncDir fsyncs a directory so a rename inside it is durable.
	SyncDir(dir string) error
	Stat(name string) (os.FileInfo, error)
}

// OSFS is the real filesystem.
type OSFS struct{}

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// WriteFile implements FS.
func (OSFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// RemoveAll implements FS.
func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

// SyncDir implements FS.
func (OSFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}

// Stat implements FS.
func (OSFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// ErrRejected is what errors.Is matches on a *RejectedError (doctor code LAI-CAT-002).
var ErrRejected = errors.New("new catalog failed validation; the previous one is kept")

// RejectedError is returned by Install when the new catalog does not pass lint. Nothing under the
// live directory was touched.
type RejectedError struct{ Report Report }

func (e *RejectedError) Error() string {
	msg := CodeRejected + ": " + ErrRejected.Error()
	if n := len(e.Report.Problems); n > 0 {
		p := e.Report.Problems[0]
		msg += fmt.Sprintf(" (%d problem(s); first: %s  %s  %s)", n, p.Location(), p.Code, p.Message)
	}
	return msg
}

// Code is the doctor code of the error.
func (e *RejectedError) Code() string { return CodeRejected }

// Is lets errors.Is(err, ErrRejected) match.
func (e *RejectedError) Is(target error) bool { return target == ErrRejected }

// InstallOptions configures Install.
type InstallOptions struct {
	// FS is the filesystem (default OSFS).
	FS FS
	// Test marks the installed catalog as a draft: a TEST file is written next to the catalog files
	// (`aictl catalog use --test`, AICTL-07). The loader reports such a catalog as source "test".
	Test bool
}

// InstallResult says what changed.
type InstallResult struct {
	Version  string
	Previous string // version of the catalog that was replaced, "" when there was none or it was unreadable
}

// installMu serialises installs inside one process; across processes the agent is the single writer
// and the root-only CLI commands run when it is stopped or idle (agent.md 3.3).
var installMu sync.Mutex

// Sibling directory names of the live catalog directory.
const (
	newSuffix  = ".new"
	prevSuffix = ".prev"
)

// Install replaces dir (/etc/localai/catalog) with the catalog in newDir, atomically:
//
//  1. lint newDir; a catalog that fails is rejected (*RejectedError, LAI-CAT-002) and nothing is touched;
//  2. copy it to <dir>.new, fsync every file, lint the copy;
//  3. remove <dir>.prev, rename <dir> to <dir>.prev, rename <dir>.new to <dir>.
//
// A crash after step 3's first rename leaves no <dir>; Recover (called by Install and by the
// agent's startup) puts <dir>.prev back, so a node is never left without its last good catalog.
func Install(dir, newDir string, opts InstallOptions) (*InstallResult, error) {
	installMu.Lock()
	defer installMu.Unlock()
	fsys := opts.FS
	if fsys == nil {
		fsys = OSFS{}
	}
	dir = filepath.Clean(dir)
	tmp, prev := dir+newSuffix, dir+prevSuffix
	parent := filepath.Dir(dir)

	if _, err := recoverLocked(fsys, dir); err != nil {
		return nil, fmt.Errorf("catalog install: recovering an interrupted install: %w", err)
	}
	if rep := Lint(newDir, LintOptions{}); !rep.Valid() {
		return nil, &RejectedError{Report: rep}
	}

	if err := fsys.RemoveAll(tmp); err != nil {
		return nil, fmt.Errorf("catalog install: %w", err)
	}
	if err := copyCatalog(fsys, newDir, tmp, opts.Test); err != nil {
		_ = fsys.RemoveAll(tmp)
		return nil, fmt.Errorf("catalog install: copying to %s: %w", tmp, err)
	}
	// Lint what will actually be installed, not what was meant to be.
	if rep := Lint(tmp, LintOptions{}); !rep.Valid() {
		_ = fsys.RemoveAll(tmp)
		return nil, &RejectedError{Report: rep}
	}

	res := &InstallResult{Version: readVersionFile(newDir), Previous: readVersionFile(dir)}
	_, statErr := fsys.Stat(dir)
	hadOld := statErr == nil
	if hadOld {
		if err := fsys.RemoveAll(prev); err != nil {
			_ = fsys.RemoveAll(tmp)
			return nil, fmt.Errorf("catalog install: %w", err)
		}
		if err := fsys.Rename(dir, prev); err != nil {
			_ = fsys.RemoveAll(tmp)
			return nil, fmt.Errorf("catalog install: keeping the previous catalog: %w", err)
		}
	}
	if err := fsys.Rename(tmp, dir); err != nil {
		if hadOld {
			_ = fsys.Rename(prev, dir) // best effort; Recover finishes the job after a crash
		}
		_ = fsys.RemoveAll(tmp)
		return nil, fmt.Errorf("catalog install: installing the new catalog: %w", err)
	}
	if err := fsys.SyncDir(parent); err != nil {
		return res, fmt.Errorf("catalog install: installed, but syncing %s failed: %w", parent, err)
	}
	return res, nil
}

// Recover repairs an install that was interrupted between its two renames: when <dir> is missing
// and <dir>.prev exists it moves the previous catalog back. It also removes a stale <dir>.new.
// It reports whether it restored the previous catalog.
func Recover(fsys FS, dir string) (bool, error) {
	installMu.Lock()
	defer installMu.Unlock()
	if fsys == nil {
		fsys = OSFS{}
	}
	return recoverLocked(fsys, filepath.Clean(dir))
}

func recoverLocked(fsys FS, dir string) (bool, error) {
	tmp, prev := dir+newSuffix, dir+prevSuffix
	restored := false
	if _, err := fsys.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		if _, perr := fsys.Stat(prev); perr == nil {
			if err := fsys.Rename(prev, dir); err != nil {
				return false, err
			}
			restored = true
		}
	}
	if _, err := fsys.Stat(tmp); err == nil {
		if err := fsys.RemoveAll(tmp); err != nil {
			return restored, err
		}
	}
	if restored {
		if err := fsys.SyncDir(filepath.Dir(dir)); err != nil {
			return restored, err
		}
	}
	return restored, nil
}

// IsTestCatalog reports whether dir carries the TEST marker of `aictl catalog use --test`.
func IsTestCatalog(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, FileTestMarker))
	return err == nil
}

// readVersionFile returns the trimmed CATALOG_VERSION of dir, "" when unreadable.
func readVersionFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, FileVersion))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// copyCatalog copies the regular files of src into dst (fsynced) and the directories between them.
// A symlink or other special file is an error, and a TEST marker in src is never copied: whether the
// result is a test catalog is decided by the caller.
func copyCatalog(fsys FS, src, dst string, test bool) error {
	if err := fsys.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return fsys.MkdirAll(target, 0o755)
		case rel == FileTestMarker:
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFileBytes {
			return fmt.Errorf("%s is larger than %d bytes", rel, MaxFileBytes)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return fsys.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return err
	}
	if test {
		if err := fsys.WriteFile(filepath.Join(dst, FileTestMarker), []byte("draft catalog installed with aictl catalog use --test\n"), 0o644); err != nil {
			return err
		}
	}
	return syncTree(fsys, dst)
}

// syncTree fsyncs dst and its subdirectories so the copied names are durable before the rename.
func syncTree(fsys FS, dst string) error {
	return filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return fsys.SyncDir(p)
		}
		return nil
	})
}
