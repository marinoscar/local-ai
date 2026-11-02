package docsrender

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func readTreeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func siblings(t *testing.T, out string) []string {
	t.Helper()
	m, _ := filepath.Glob(out + ".*")
	sort.Strings(m)
	return m
}

func TestCheckOutRefusesGitCheckouts(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "wt")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "deep", "er"), worktree, filepath.Join(root, "plain")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A linked worktree has a .git FILE.
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-into-repo")
	if err := os.Symlink(filepath.Join(repo, "deep"), link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, out string
		refused   bool
	}{
		{"inside a checkout", filepath.Join(repo, "out"), true},
		{"deep inside a checkout, not created yet", filepath.Join(repo, "deep", "er", "new", "docs"), true},
		{"the checkout itself", repo, true},
		{"inside a linked worktree", filepath.Join(worktree, "docs"), true},
		{"through a symlink into a checkout", filepath.Join(link, "out"), true},
		{"a dot-dot path that ends in a checkout", filepath.Join(root, "plain", "..", "repo", "x"), true},
		{"a folder with no .git above it", filepath.Join(root, "plain", "out"), false},
		{"a folder that does not exist at all", filepath.Join(root, "plain", "a", "b", "c"), false},
	}
	for _, c := range cases {
		err := CheckOut(c.out)
		var e *Error
		switch {
		case c.refused && (!errors.As(err, &e) || e.Kind != KindUsage):
			t.Errorf("%s: err = %v, want a usage refusal", c.name, err)
		case !c.refused && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		}
	}
}

func TestWriteTreeCreatesFilesAndModes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a", "docs")
	files := map[string][]byte{"index.html": []byte("<i>"), "sub/x.txt": []byte("x")}
	if err := WriteTree(out, files, nil); err != nil {
		t.Fatal(err)
	}
	got := readTreeFiles(t, out)
	if got["index.html"] != "<i>" || got["sub/x.txt"] != "x" || len(got) != 2 {
		t.Errorf("tree = %v", got)
	}
	for p, want := range map[string]os.FileMode{out: 0o755, filepath.Join(out, "sub"): 0o755, filepath.Join(out, "index.html"): 0o644} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", p, fi.Mode().Perm(), want, err)
		}
	}
	if s := siblings(t, out); len(s) != 0 {
		t.Errorf("leftovers: %v", s)
	}
}

func TestWriteTreeReplacesTheOldFolderWholesale(t *testing.T) {
	out := filepath.Join(t.TempDir(), "docs")
	if err := WriteTree(out, map[string][]byte{"old.html": []byte("old"), "keep.txt": []byte("1")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := WriteTree(out, map[string][]byte{"new.html": []byte("new")}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readTreeFiles(t, out); len(got) != 1 || got["new.html"] != "new" {
		t.Errorf("tree = %v", got)
	}
	if s := siblings(t, out); len(s) != 0 {
		t.Errorf("leftovers: %v", s)
	}
}

// failRename makes the n-th call to rename fail, and records the calls.
func failRename(t *testing.T, failOn ...int) *[]string {
	t.Helper()
	orig := rename
	var calls []string
	bad := map[int]bool{}
	for _, n := range failOn {
		bad[n] = true
	}
	rename = func(a, b string) error {
		calls = append(calls, filepath.Base(a)+" -> "+filepath.Base(b))
		if bad[len(calls)] {
			return errors.New("injected failure")
		}
		return orig(a, b)
	}
	t.Cleanup(func() { rename = orig })
	return &calls
}

func TestWriteTreeSwapFailureKeepsTheOldFolder(t *testing.T) {
	cases := []struct {
		name   string
		failOn []int
	}{
		{"the old folder cannot be moved aside", []int{1}},
		{"the new folder cannot be moved into place", []int{2}},
	}
	for _, c := range cases {
		out := filepath.Join(t.TempDir(), "docs")
		if err := WriteTree(out, map[string][]byte{"old.html": []byte("old")}, nil); err != nil {
			t.Fatal(err)
		}
		failRename(t, c.failOn...)
		err := WriteTree(out, map[string][]byte{"new.html": []byte("new")}, nil)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindIO {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if got := readTreeFiles(t, out); len(got) != 1 || got["old.html"] != "old" {
			t.Errorf("%s: the old folder is not intact: %v", c.name, got)
		}
		if s := siblings(t, out); len(s) != 0 {
			t.Errorf("%s: leftovers: %v", c.name, s)
		}
	}
}

func TestWriteTreeFirstWriteFailureLeavesNothing(t *testing.T) {
	out := filepath.Join(t.TempDir(), "docs")
	failRename(t, 1) // no old folder, so the first rename is tmp -> out
	if err := WriteTree(out, map[string][]byte{"a.html": []byte("a")}, nil); err == nil {
		t.Fatal("expected an error")
	}
	if exists(out) || len(siblings(t, out)) != 0 {
		t.Errorf("leftovers after a failed first write: %v", siblings(t, out))
	}
}

func TestWriteTreeRecoversAfterACrashBetweenRenames(t *testing.T) {
	out := filepath.Join(t.TempDir(), "docs")
	// State left by a run that died after moving out to out.prev: there is no out.
	if err := os.MkdirAll(out+".prev", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out+".prev", "old.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	failRename(t, 2) // restore prev -> out works, then tmp -> out ... is call 3; fail the swap of the old aside
	err := WriteTree(out, map[string][]byte{"new.html": []byte("new")}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := readTreeFiles(t, out); got["old.html"] != "old" {
		t.Errorf("the previous folder was not put back: %v", got)
	}
}

func TestWriteTreeUnwritableAndInvalid(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		out   string
		files map[string][]byte
	}{
		{"the parent is a file", filepath.Join(blocker, "docs"), map[string][]byte{"a": nil}},
		{"a name that escapes the folder", filepath.Join(base, "docs"), map[string][]byte{"../escape.txt": []byte("x")}},
		{"an absolute name", filepath.Join(base, "docs2"), map[string][]byte{"/etc/x": []byte("x")}},
	}
	for _, c := range cases {
		err := WriteTree(c.out, c.files, nil)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindIO {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if exists(c.out) {
			t.Errorf("%s: output exists after a failure", c.name)
		}
		if s := siblings(t, c.out); len(s) != 0 {
			t.Errorf("%s: leftovers %v", c.name, s)
		}
	}
	if exists(filepath.Join(base, "escape.txt")) {
		t.Error("a file was written outside the output folder")
	}
	if !strings.Contains((&Error{Msg: "cannot write", Path: "/x"}).Error(), "/x") {
		t.Error("the error does not name the path")
	}
}
