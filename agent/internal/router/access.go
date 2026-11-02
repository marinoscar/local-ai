package router

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Identity is the numeric user and group of the unit's user (localai). A negative id never
// matches an owner.
type Identity struct{ UID, GID int }

// Reasons CheckReadable reports; they are stable words for the details of LAI-LLM-008.
const (
	AccessMissing    = "missing"
	AccessNotFile    = "not_a_file"
	AccessNoTraverse = "directory_not_searchable"
	AccessNoRead     = "file_not_readable"
	AccessError      = "stat_failed"
)

// AccessProblem is why a model file cannot be used by the router.
type AccessProblem struct {
	Path   string
	Reason string
}

// Error implements error.
func (e *AccessProblem) Error() string { return e.Reason + ": " + e.Path }

// CheckReadable verifies that the unit's user can open the file (llama-router.md section 4.2 step
// 3). Go cannot do faccessat with another user's credentials, so it reads the mode bits and the
// owner along the path: every directory from the root down must have o+x, or be owned by or
// grouped to the user with the matching bit; the file must exist, be a regular file and have o+r,
// or be owned by or grouped to the user with the matching bit. The user's other groups are not
// known, so a directory or file reachable only through one of them is reported as not readable
// (the safe error: the model is left out and the doctor says why).
func CheckReadable(fsys config.FS, id Identity, file string) error {
	if !strings.HasPrefix(file, "/") {
		return &AccessProblem{Path: file, Reason: AccessMissing}
	}
	// "/" first, then every directory prefix down to the file's directory.
	dirs := []string{"/"}
	parts := strings.Split(strings.Trim(file, "/"), "/")
	for i, dir := 0, ""; i < len(parts)-1; i++ {
		dir += "/" + parts[i]
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		info, err := fsys.Stat(dir)
		if err != nil {
			return accessErr(dir, err)
		}
		if !info.IsDir() {
			return &AccessProblem{Path: dir, Reason: AccessNotFile}
		}
		if !permits(info, id, 0o001, 0o100, 0o010) {
			return &AccessProblem{Path: dir, Reason: AccessNoTraverse}
		}
	}
	info, err := fsys.Stat(file)
	if err != nil {
		return accessErr(file, err)
	}
	if !info.Mode().IsRegular() {
		return &AccessProblem{Path: file, Reason: AccessNotFile}
	}
	if !permits(info, id, 0o004, 0o400, 0o040) {
		return &AccessProblem{Path: file, Reason: AccessNoRead}
	}
	return nil
}

func accessErr(p string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return &AccessProblem{Path: p, Reason: AccessMissing}
	}
	return &AccessProblem{Path: p, Reason: AccessError}
}

// permits reports whether the other bit is set, or the user owns the file and the owner bit is
// set, or the file's group is the user's group and the group bit is set.
func permits(info os.FileInfo, id Identity, other, owner, group os.FileMode) bool {
	mode := info.Mode().Perm()
	if mode&other != 0 {
		return true
	}
	uid, gid, ok := ownerOf(info)
	if !ok {
		return false
	}
	if id.UID >= 0 && uid == id.UID && mode&owner != 0 {
		return true
	}
	if id.GID >= 0 && gid == id.GID && mode&group != 0 {
		return true
	}
	return false
}

// String renders an identity for logs.
func (i Identity) String() string { return fmt.Sprintf("uid=%d gid=%d", i.UID, i.GID) }
