package local

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
)

// FileError is a file operation that failed. The command shows it as LAI-CLI-012 with the path and
// Reason in plain words, never the Go error chain.
type FileError struct {
	Op   string // "read", "write", "copy", "remove", "list"
	Path string
	Err  error
}

func (e *FileError) Error() string { return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err) }

// Unwrap exposes the cause.
func (e *FileError) Unwrap() error { return e.Err }

// Reason says why in everyday words ("the disk is full", "permission denied").
func (e *FileError) Reason() string { return Reason(e.Err) }

// Reason describes an I/O error in plain words.
func Reason(err error) string {
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "the disk is full"
	case errors.Is(err, syscall.EROFS):
		return "the disk is read-only"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "it does not exist"
	case errors.Is(err, syscall.ENOTDIR):
		return "part of the path is not a folder"
	case errors.Is(err, fs.ErrExist):
		return "it already exists"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func fileErr(op, path string, err error) error {
	if err == nil {
		return nil
	}
	var fe *FileError
	if errors.As(err, &fe) {
		return err
	}
	return &FileError{Op: op, Path: path, Err: err}
}

// GroupError is a group the commands need (localai-operators, localai) that does not exist on this
// machine.
type GroupError struct {
	Name string
	Err  error
}

func (e *GroupError) Error() string { return fmt.Sprintf("group %q: %v", e.Name, e.Err) }

// Unwrap exposes the cause.
func (e *GroupError) Unwrap() error { return e.Err }

// ErrNoKit means no content root holds a catalog in its kit (internal disk and mounted AI-ARK
// drives were all searched). The command exits 5.
var ErrNoKit = errors.New("no ark kit with a catalog was found")

// ErrNoDrive means --to-ark found no mounted, initialised AI-ARK drive. The command exits 5.
var ErrNoDrive = errors.New("no AI-ARK drive is mounted")

// BackupError is a backup that cannot be used (failed integrity check, an entry that must not be
// restored, a file that is not a backup). Nothing on the node was changed. Exit 1 (LAI-CLI-012 is
// for I/O; this one is shown with the same code and the reason as the message).
type BackupError struct{ Reason string }

func (e *BackupError) Error() string { return "backup: " + e.Reason }
