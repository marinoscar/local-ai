package fake_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestClockTickersFireOnlyWhenTimeAdvances(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClock(start)
	tk := c.NewTicker(10 * time.Second)
	select {
	case <-tk.C():
		t.Fatal("fired before any time passed")
	default:
	}
	c.Advance(9 * time.Second)
	select {
	case <-tk.C():
		t.Fatal("fired early")
	default:
	}
	c.Advance(time.Second)
	select {
	case at := <-tk.C():
		if !at.Equal(start.Add(10 * time.Second)) {
			t.Errorf("tick at %v", at)
		}
	default:
		t.Fatal("did not fire")
	}
	if !c.Now().Equal(start.Add(10 * time.Second)) {
		t.Errorf("now = %v", c.Now())
	}
	tk.Stop()
	c.Advance(time.Minute)
	select {
	case <-tk.C():
		t.Fatal("a stopped ticker must not fire")
	default:
	}
	if c.Tickers() != 0 {
		t.Errorf("tickers = %d", c.Tickers())
	}
}

func TestFaultFSFailsTheNamedOperationOnly(t *testing.T) {
	dir := t.TempDir()
	ffs := fake.New(config.OSFS{}).FailNth("rename", 2)
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	if err := os.WriteFile(a, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ffs.Rename(a, b); err != nil {
		t.Fatalf("first rename must work: %v", err)
	}
	if err := ffs.Rename(b, c); !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("second rename must fail: %v", err)
	}
	if err := ffs.Rename(b, c); err != nil {
		t.Fatalf("third rename must work again: %v", err)
	}
	ffs.FailOn("remove", -1)
	if err := ffs.Remove(c); !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("remove must fail: %v", err)
	}
	ffs.Reset()
	if err := ffs.Remove(c); err != nil {
		t.Fatalf("Reset clears the failures: %v", err)
	}
}

func TestCrashMakesCleanupFailToo(t *testing.T) {
	dir := t.TempDir()
	ffs := fake.New(config.OSFS{})
	ffs.Crash = true
	ffs.FailOn("rename", 1)
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ffs.Rename(f, f+"2"); err == nil {
		t.Fatal("rename must fail")
	}
	if err := ffs.Remove(f); !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("after a crash nothing cleans up: %v", err)
	}
}
