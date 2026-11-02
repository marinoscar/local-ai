// Package static serves files from a content root with the path-safety rules of agent.md section
// 3.11: nothing outside the root is ever opened, whatever the request path says.
//
// The package imports no other part of the agent, so the api package can use it for /docs/ and
// the maps mount for its tiles. It reports problems as plain errors (*Error for ServeFile) and
// leaves the answer to the caller, which writes the doctor-coded envelope or page.
//
// Offline first: it reads the local disk only. Every file is opened for one response and closed
// when the response ends, so a drive that is ejected is never held by a lingering handle.
package static

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Errors SafeJoin returns. A caller answers all of them with the same 404, so a client cannot tell
// a missing file from a forbidden one; the distinction is for logs and tests.
var (
	// ErrUnsafePath: the request path is not acceptable: an absolute path, a NUL or control
	// character, a backslash, a "." or ".." segment, an encoded dot or slash, or a dotfile.
	ErrUnsafePath = errors.New("static: unsafe path")
	// ErrOutsideRoot: the path, after Clean or after following symbolic links, leaves the root.
	ErrOutsideRoot = errors.New("static: path leaves the root")
	// ErrNotFound: the root or the path does not exist (or is a symbolic link loop).
	ErrNotFound = errors.New("static: not found")
)

// SafeJoin returns the real path of reqPath under root, or an error. reqPath is the request path
// below the mount, relative and already percent-decoded ("maps/assets/a.json", never "/etc/x");
// "" and "/" mean the root itself.
//
// The rules, in order:
//   - reqPath must not be absolute, contain NUL or another control character or a backslash, or
//     carry an encoded dot, slash, backslash or NUL (%2e, %2f, %5c, %00 in any case);
//   - no segment may be "." or "..", and none may start with "." (dotfiles such as .git and
//     .env are never served, wherever they are);
//   - the joined path is filepath.Clean'ed and must stay under root;
//   - symbolic links are resolved (filepath.EvalSymlinks) and the real path must still be under the
//     real root and must not pass through a dotfile. A symbolic link whose target is inside the
//     root is followed; one that leaves it is ErrOutsideRoot. The root itself may be a link.
//
// The returned path is the resolved one, so a later symlink swap is the only gap left; the content
// roots are written by the agent and root only.
func SafeJoin(root, reqPath string) (string, error) {
	if root == "" {
		return "", ErrNotFound
	}
	rel, err := cleanRel(reqPath)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", ErrNotFound
	}
	joined := filepath.Join(realRoot, rel)
	if !within(realRoot, joined) {
		return "", ErrOutsideRoot
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", ErrNotFound // missing, no permission, a link loop, a file used as a directory
	}
	if !within(realRoot, resolved) {
		return "", ErrOutsideRoot
	}
	if r, err := filepath.Rel(realRoot, resolved); err != nil || hasDotSegment(r) {
		return "", ErrUnsafePath
	}
	return resolved, nil
}

// cleanRel validates reqPath and returns it as an OS-relative path ("." for the root).
func cleanRel(reqPath string) (string, error) {
	if reqPath == "" || reqPath == "/" {
		return ".", nil
	}
	if strings.HasPrefix(reqPath, "/") {
		return "", ErrUnsafePath
	}
	for i := 0; i < len(reqPath); i++ {
		c := reqPath[i]
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "", ErrUnsafePath
		}
	}
	lower := strings.ToLower(reqPath)
	for _, enc := range []string{"%2e", "%2f", "%5c", "%00"} {
		if strings.Contains(lower, enc) {
			return "", ErrUnsafePath
		}
	}
	segs := strings.Split(strings.TrimSuffix(reqPath, "/"), "/")
	for _, s := range segs {
		if s == "" || strings.HasPrefix(s, ".") { // "a//b", ".", "..", ".env"
			return "", ErrUnsafePath
		}
	}
	return filepath.FromSlash(strings.Join(segs, "/")), nil
}

// within reports whether p is root or below it. Both are clean absolute paths.
func within(root, p string) bool {
	sep := string(filepath.Separator)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, sep)+sep)
}

// hasDotSegment reports whether a relative path has a segment starting with "." (other than the
// path "." itself, which is the root).
func hasDotSegment(rel string) bool {
	if rel == "." {
		return false
	}
	for _, s := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(s, ".") {
			return true
		}
	}
	return false
}

// openRegular opens path for reading and returns it with its info, refusing anything that is not a
// regular file (a directory, a FIFO, a device, a socket). The path is checked before it is opened
// so that a FIFO cannot block the request, and again on the open handle.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	if fi.IsDir() {
		return nil, nil, errIsDir
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	fi, err = f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, ErrNotFound
	}
	return f, fi, nil
}

var errIsDir = errors.New("static: is a directory")

// isDir reports whether p (a SafeJoin result) is a directory.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
