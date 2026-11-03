package ark_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// overviewLayout is overview.md section 3.3, directories only, in the order it lists them.
var overviewLayout = []string{
	"models", "speech", "speech/whisper", "speech/piper", "kiwix", "maps", "maps/assets", "kit", "docs",
	".trash", ".staging",
}

func TestLayoutDirsMatchTheOverview(t *testing.T) {
	if !reflect.DeepEqual(ark.LayoutDirs, overviewLayout) {
		t.Fatalf("LayoutDirs = %v, want %v", ark.LayoutDirs, overviewLayout)
	}
	if len(ark.PrivateDirs) != 2 || !ark.PrivateDirs[".trash"] || !ark.PrivateDirs[".staging"] {
		t.Fatalf("PrivateDirs = %v, want exactly .trash and .staging", ark.PrivateDirs)
	}
}

func TestCreateLayoutModes(t *testing.T) {
	old := syscall.Umask(0o077) // a hostile umask must not change the result
	defer syscall.Umask(old)

	root := filepath.Join(t.TempDir(), "srv", "localai") // the parent does not exist either
	fsys := newFS()
	created, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{})
	if err != nil || !created {
		t.Fatalf("CreateLayout = %v, %v", created, err)
	}
	if m := mode(t, root); m != 0o755 {
		t.Errorf("root mode = %o, want 755", m)
	}
	for _, rel := range overviewLayout {
		want := os.FileMode(0o755)
		if rel == ".trash" || rel == ".staging" {
			want = 0o700
		}
		if got := mode(t, filepath.Join(root, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s mode = %o, want %o", rel, got, want)
		}
	}
	// Only directories are created: ROOT.json and READY.json belong to their owners.
	for _, n := range []string{ark.RootJSONName, "READY.json"} {
		if exists(filepath.Join(root, n)) {
			t.Errorf("CreateLayout wrote %s", n)
		}
	}
}

func TestCreateLayoutChownsOnlyTheTopDirectoryAndOnlyWhenItCreatedIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localai")
	fsys := newFS()
	owner := &ark.Owner{UID: 981, GID: 982}
	if _, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	if want := []chownCall{{root, 981, 982}}; !reflect.DeepEqual(fsys.chowns, want) {
		t.Fatalf("chowns = %v, want %v", fsys.chowns, want)
	}
	// A second run finds the root and changes no ownership.
	fsys.chowns = nil
	created, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{Owner: owner})
	if err != nil || created || len(fsys.chowns) != 0 {
		t.Fatalf("second run: created=%v err=%v chowns=%v", created, err, fsys.chowns)
	}
	// No owner: no chown at all.
	fsys2 := newFS()
	if _, err := ark.CreateLayout(fsys2, filepath.Join(t.TempDir(), "x"), ark.LayoutOptions{}); err != nil || len(fsys2.chowns) != 0 {
		t.Fatalf("no owner: err=%v chowns=%v", err, fsys2.chowns)
	}
}

func TestCreateLayoutIsIdempotentAndKeepsContent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "models", "m1", "m1.gguf"), "weights")
	if err := os.Chmod(filepath.Join(root, "models"), 0o750); err != nil {
		t.Fatal(err)
	}
	fsys := newFS()
	created, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{})
	if err != nil || created {
		t.Fatalf("CreateLayout = %v, %v", created, err)
	}
	if readFile(t, filepath.Join(root, "models", "m1", "m1.gguf")) != "weights" {
		t.Fatal("content changed")
	}
	if m := mode(t, filepath.Join(root, "models")); m != 0o750 {
		t.Errorf("an existing public directory's mode was changed to %o", m)
	}
	for _, rel := range overviewLayout {
		if !exists(filepath.Join(root, filepath.FromSlash(rel))) {
			t.Errorf("%s was not created", rel)
		}
	}
}

func TestCreateLayoutTightensThePrivateDirectories(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".staging", ".trash"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ark.CreateLayout(newFS(), root, ark.LayoutOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".staging", ".trash"} {
		if m := mode(t, filepath.Join(root, d)); m != 0o700 {
			t.Errorf("%s mode = %o, want 700", d, m)
		}
	}
}

func TestCreateLayoutFailures(t *testing.T) {
	t.Run("a file in the way is never replaced", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "kiwix"), "i am a file")
		_, err := ark.CreateLayout(newFS(), root, ark.LayoutOptions{})
		if !errors.Is(err, ark.ErrLayoutConflict) {
			t.Fatalf("err = %v, want ErrLayoutConflict", err)
		}
		if readFile(t, filepath.Join(root, "kiwix")) != "i am a file" {
			t.Fatal("the file was touched")
		}
	})
	t.Run("a dangling symlink where a directory belongs", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "docs")); err != nil {
			t.Fatal(err)
		}
		if _, err := ark.CreateLayout(newFS(), root, ark.LayoutOptions{}); !errors.Is(err, ark.ErrLayoutConflict) {
			t.Fatalf("err = %v, want ErrLayoutConflict", err)
		}
	})
	t.Run("a symlink to a real directory is accepted", func(t *testing.T) {
		root := t.TempDir()
		big := t.TempDir()
		if err := os.Symlink(big, filepath.Join(root, "models")); err != nil {
			t.Fatal(err)
		}
		if _, err := ark.CreateLayout(newFS(), root, ark.LayoutOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the parent of the root is a file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "srv")
		writeFile(t, parent, "file")
		if _, err := ark.CreateLayout(newFS(), filepath.Join(parent, "localai"), ark.LayoutOptions{}); !errors.Is(err, ark.ErrLayoutConflict) {
			t.Fatalf("err = %v, want ErrLayoutConflict", err)
		}
	})
	t.Run("read-only filesystem", func(t *testing.T) {
		root := t.TempDir()
		fsys := newFS()
		fsys.mkdirErr[root] = &os.PathError{Op: "mkdir", Path: root, Err: syscall.EROFS}
		_, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{})
		if !errors.Is(err, syscall.EROFS) {
			t.Fatalf("err = %v, want EROFS", err)
		}
		if len(names(t, root)) != 0 {
			t.Fatalf("something was created: %v", names(t, root))
		}
	})
	t.Run("disk full half way, then a retry completes", func(t *testing.T) {
		root := t.TempDir()
		fsys := newFS()
		fsys.mkdirErr[filepath.Join(root, "kiwix")] = &os.PathError{Op: "mkdir", Path: "kiwix", Err: syscall.ENOSPC}
		if _, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{}); !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("err = %v, want ENOSPC", err)
		}
		if !exists(filepath.Join(root, "speech", "piper")) || exists(filepath.Join(root, "kiwix")) {
			t.Fatalf("partial state wrong: %v", names(t, root))
		}
		delete(fsys.mkdirErr, filepath.Join(root, "kiwix"))
		if _, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{}); err != nil {
			t.Fatalf("retry: %v", err)
		}
		for _, rel := range overviewLayout {
			if !exists(filepath.Join(root, filepath.FromSlash(rel))) {
				t.Errorf("%s missing after the retry", rel)
			}
		}
	})
	t.Run("chown refused", func(t *testing.T) {
		// A user that is not root cannot give the directory away; the error must surface.
		root := filepath.Join(t.TempDir(), "localai")
		fsys := &chownFailFS{tfs: newFS()}
		created, err := ark.CreateLayout(fsys, root, ark.LayoutOptions{Owner: &ark.Owner{UID: 1, GID: 1}})
		if !errors.Is(err, syscall.EPERM) || !created {
			t.Fatalf("created=%v err=%v, want created and EPERM", created, err)
		}
	})
}

type chownFailFS struct{ *tfs }

func (chownFailFS) Chown(string, int, int) error { return syscall.EPERM }
