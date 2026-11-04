package fsperm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMkdirAllIgnoresTheUmask(t *testing.T) {
	old := syscall.Umask(0o027) // the agent's unit
	defer syscall.Umask(old)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "speech", "piper", "piper-en-medium")
	if err := MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(root, "speech"), filepath.Join(root, "speech", "piper"), deep} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("%s has mode %v, want 0755 under umask 0027", d, fi.Mode().Perm())
		}
	}
	// os.MkdirAll alone would have given 0750: the bug this package exists for.
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(plain); fi.Mode().Perm() != 0o750 {
		t.Skipf("this system's umask handling differs (mode %v)", fi.Mode().Perm())
	}
}

func TestMkdirAllLeavesExistingDirectoriesAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o711); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a")
	if err := MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(root); fi.Mode().Perm() != 0o711 {
		t.Errorf("an existing parent was changed to %v", fi.Mode().Perm())
	}
	if err := MkdirAll(sub, 0o700); err != nil { // already there: untouched
		t.Fatal(err)
	}
	if fi, _ := os.Stat(sub); fi.Mode().Perm() != 0o755 {
		t.Errorf("an existing directory was changed to %v", fi.Mode().Perm())
	}
}
