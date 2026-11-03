package run

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

func TestFileLockIsExclusiveAndSaysWhoHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync", "lock")
	l := FileLock{Path: path}
	id, _ := journal.NewRunID(epoch, nil)

	first, err := l.Acquire("starting")
	if err != nil {
		t.Fatal(err)
	}
	first.SetHolder(id)

	// A second holder (another process or a chroot has its own open file description) is refused and
	// told who has it.
	_, err = l.Acquire("starting")
	var busy *LockBusyError
	if !errors.Is(err, ErrLockBusy) || !errors.As(err, &busy) || busy.RunID != id || busy.PID != os.Getpid() {
		t.Fatalf("second Acquire: %v (%+v)", err, busy)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), strconv.Itoa(os.Getpid())+" "+id) {
		t.Fatalf("lock file = %q", data)
	}

	first.Release()
	first.Release() // idempotent
	second, err := l.Acquire("starting")
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	second.Release()
}

func TestAStaleLockFileIsNotALock(t *testing.T) {
	// An agent that was killed left its lock file with a holder in it. The kernel dropped the flock when
	// the process died, so the next Acquire just takes it (the stale-lock recovery of the spec).
	path := filepath.Join(t.TempDir(), "lock")
	id, _ := journal.NewRunID(epoch, nil)
	if err := os.WriteFile(path, []byte("999999 "+id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ls, err := FileLock{Path: path}.Acquire("starting")
	if err != nil {
		t.Fatalf("a stale lock file blocked the lock: %v", err)
	}
	defer ls.Release()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "999999") {
		t.Fatalf("the stale holder was not replaced: %q", data)
	}
}

func TestLockFileGarbageIsHarmless(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	l := FileLock{Path: path}
	ls, err := l.Acquire("starting")
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Release()
	if err := os.WriteFile(path, []byte("\x00\x01 not-a-run-id"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = l.Acquire("x")
	var busy *LockBusyError
	if !errors.As(err, &busy) || busy.RunID != "" {
		t.Fatalf("err = %v, busy = %+v", err, busy)
	}
}

func TestLockDirectoryIsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "lock")
	ls, err := FileLock{Path: path}.Acquire("starting")
	if err != nil {
		t.Fatal(err)
	}
	ls.Release()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
