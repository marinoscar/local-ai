package docsrender

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// rename is os.Rename; tests replace it to make the swap fail.
var rename = os.Rename

// Owner is the user and group the written files get (the `localai` service user on a node).
type Owner struct{ UID, GID int }

// CheckOut refuses an output folder that is inside a git checkout: the rendered documents hold the
// real lab values, and a checkout is where they could be committed by accident (aictl.md 4.22, 6).
// It walks up from the cleaned absolute path, and again from its symlink-resolved form, looking for
// a .git entry (a folder, or the file of a worktree). It returns an *Error of KindUsage.
func CheckOut(out string) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return &Error{Kind: KindUsage, Path: out, Msg: "not a usable path", Err: err}
	}
	abs = filepath.Clean(abs)
	for _, p := range []string{abs, resolveExisting(abs)} {
		for dir := p; ; dir = filepath.Dir(dir) {
			if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
				return &Error{Kind: KindUsage, Path: abs, Msg: "refusing to write inside a git checkout (" + dir + ")"}
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return nil
}

// resolveExisting resolves symlinks in the longest existing prefix of p and appends the rest.
func resolveExisting(p string) string {
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		if filepath.Dir(cur) == cur {
			return p
		}
	}
}

func randSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func writeFile(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(p, 0o644)
}

// WriteTree writes files (relative slash path -> content) as the folder out, atomically: the files go
// to <out>.tmp-<random>, the old folder moves to <out>.prev, the new one is renamed into place, and
// only then is .prev removed. Any failure leaves the old folder as it was and removes the temporary
// one. Files are 0644, folders 0755; owner, when given, owns everything.
func WriteTree(out string, files map[string][]byte, owner *Owner) error {
	out = filepath.Clean(out)
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ioError("cannot write", parent, unwrapPath(err))
	}
	tmp := out + ".tmp-" + randSuffix()
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return ioError("cannot write", tmp, unwrapPath(err))
	}
	fail := func(p string, err error) error {
		_ = os.RemoveAll(tmp)
		return ioError("cannot write", p, unwrapPath(err))
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		clean := filepath.Clean(filepath.FromSlash(n))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fail(n, errors.New("path escapes the output folder"))
		}
		dst := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fail(dst, err)
		}
		if err := writeFile(dst, files[n]); err != nil {
			return fail(dst, err)
		}
	}
	if owner != nil {
		_ = filepath.WalkDir(tmp, func(p string, _ fs.DirEntry, err error) error {
			if err == nil {
				_ = os.Lchown(p, owner.UID, owner.GID)
			}
			return nil
		})
	}

	prev := out + ".prev"
	if !exists(out) && exists(prev) { // an earlier run died between the two renames
		if err := rename(prev, out); err != nil {
			return fail(prev, err)
		}
	}
	_ = os.RemoveAll(prev)
	hadOld := exists(out)
	if hadOld {
		if err := rename(out, prev); err != nil {
			return fail(out, err)
		}
	}
	if err := rename(tmp, out); err != nil {
		if hadOld {
			_ = rename(prev, out)
		}
		return fail(out, err)
	}
	if hadOld {
		_ = os.RemoveAll(prev)
	}
	return nil
}
