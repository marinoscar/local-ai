package state

import (
	"os"
	"sort"
)

// File is the part of *os.File the atomic writer needs.
type File interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
	Chmod(mode os.FileMode) error
	Chown(uid, gid int) error
}

// FS is the filesystem seam of the state store (agent.md section 3.2: every external dependency sits
// behind an interface with a fake). The real implementation is OSFS; tests use state/fake.MemFS, which
// can inject ENOSPC/EROFS/EACCES at any operation and simulate a kill at any point of a write.
type FS interface {
	ReadFile(name string) ([]byte, error)
	// OpenFile is only used with O_WRONLY|O_CREATE|O_EXCL for temp files.
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	// Link creates newname as a hard link of oldname and fails if newname exists (like os.Link).
	Link(oldname, newname string) error
	Remove(name string) error
	// ReadDirNames returns the sorted names (not paths) of the entries of dir.
	ReadDirNames(dir string) ([]string, error)
	// SyncDir fsyncs a directory so a rename or unlink inside it is durable.
	SyncDir(dir string) error
	Exists(name string) (bool, error)
}

// OSFS is the real filesystem.
type OSFS struct{}

var _ FS = OSFS{}

// ReadFile implements FS.
func (OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// OpenFile implements FS.
func (OSFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Link implements FS.
func (OSFS) Link(oldname, newname string) error { return os.Link(oldname, newname) }

// Remove implements FS.
func (OSFS) Remove(name string) error { return os.Remove(name) }

// ReadDirNames implements FS.
func (OSFS) ReadDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// SyncDir implements FS.
func (OSFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// Exists implements FS.
func (OSFS) Exists(name string) (bool, error) {
	_, err := os.Lstat(name)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
