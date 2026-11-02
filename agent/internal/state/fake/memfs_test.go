package fake

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestRebootDropsUnsyncedBytes(t *testing.T) {
	m := New()
	f, err := m.OpenFile("/d/a", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("def")); err != nil {
		t.Fatal(err)
	}
	m.Reboot()
	if data, _ := m.Contents("/d/a"); string(data) != "abc" {
		t.Fatalf("after reboot = %q, want only the fsynced bytes", data)
	}
}

func TestCrashAfterFreezesEverything(t *testing.T) {
	m := New()
	m.Seed("/d/a", []byte("x"))
	m.CrashAfter(1)
	if err := m.Remove("/d/missing"); err == nil || errors.Is(err, ErrCrashed) {
		t.Fatalf("first mutating op should run (and fail as not-exist), got %v", err)
	}
	if err := m.Remove("/d/a"); !errors.Is(err, ErrCrashed) {
		t.Fatalf("second op err = %v, want ErrCrashed", err)
	}
	if _, err := m.ReadFile("/d/a"); !errors.Is(err, ErrCrashed) {
		t.Fatalf("read after the kill err = %v, want ErrCrashed", err)
	}
	m.Reboot()
	if _, ok := m.Contents("/d/a"); !ok {
		t.Fatal("the killed remove must have no effect")
	}
}

func TestHookErrorIsAnErrnoPathError(t *testing.T) {
	m := New()
	m.Hook = func(op Op) error {
		if op.Kind == KindOpen {
			return syscall.ENOSPC
		}
		return nil
	}
	_, err := m.OpenFile("/d/a", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinkSharesTheInode(t *testing.T) {
	m := New()
	m.Seed("/d/a", []byte("x"))
	if err := m.Link("/d/a", "/d/b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Link("/d/a", "/d/b"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second link err = %v, want exist", err)
	}
	if err := m.Rename("/d/a", "/d/c"); err != nil {
		t.Fatal(err)
	}
	if data, _ := m.Contents("/d/b"); string(data) != "x" {
		t.Fatalf("link content = %q", data)
	}
	m.NoLink = true
	if err := m.Link("/d/b", "/d/e"); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("NoLink err = %v", err)
	}
}
