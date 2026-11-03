package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// DefaultLockPath is the lock file of a node (ark-sync.md sections 3.1 and 4.7).
const DefaultLockPath = "/var/lib/localai/sync/lock"

// ErrLockBusy: another process (another agent, for example one in a chroot) holds the sync lock. It is
// LAI-SYN-005, the same answer as a run that is active in this process.
var ErrLockBusy = errors.New("run: another process is syncing")

// Locker is the cross-process run slot: the in-memory guard of the engine covers one process, this one
// covers two. FileLock is the real one (flock); tests fake it.
type Locker interface {
	// Acquire takes the lock or returns ErrLockBusy, wrapped in a *LockBusyError that names the
	// holder's run when it can be read. holder is written into the lock file for the diagnosis of the
	// next process that fails to get it.
	Acquire(holder string) (Lease, error)
}

// Lease is a held lock.
type Lease interface {
	// SetHolder rewrites what the lock file says about its holder (the run id, once the run exists).
	SetHolder(holder string)
	// Release gives the lock up.
	Release()
}

// LockBusyError wraps ErrLockBusy with what the file says about the holder.
type LockBusyError struct {
	// RunID is the run id the holder wrote ("" when unreadable).
	RunID string
	// PID is the holder's process id (0 when unreadable).
	PID int
}

func (e *LockBusyError) Error() string {
	return fmt.Sprintf("%v (pid %d, run %q)", ErrLockBusy, e.PID, e.RunID)
}

// Is makes errors.Is(err, ErrLockBusy) true.
func (e *LockBusyError) Is(target error) bool { return target == ErrLockBusy }

// FileLock is flock(2) on a file. The kernel drops the lock when the holder dies, however it dies, so
// a lock file left behind by a killed agent is not a stale lock: the next Acquire simply gets it (the
// "stale-lock recovery" of the spec is the kernel's). The file carries "<pid> <run id>" for the
// diagnosis of a failed Acquire; it is rewritten by every holder and never deleted.
type FileLock struct {
	// Path is the lock file; DefaultLockPath when empty.
	Path string
}

func (l FileLock) path() string {
	if l.Path == "" {
		return DefaultLockPath
	}
	return l.Path
}

// Acquire implements Locker.
func (l FileLock) Acquire(holder string) (Lease, error) {
	p := l.path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("run: lock directory: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("run: open lock: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		defer f.Close()
		if isWouldBlock(err) {
			be := &LockBusyError{}
			if data, rerr := os.ReadFile(p); rerr == nil {
				be.PID, be.RunID = parseHolder(string(data))
			}
			return nil, be
		}
		return nil, fmt.Errorf("run: lock %s: %w", p, err)
	}
	ls := &fileLease{f: f}
	ls.SetHolder(holder)
	return ls, nil
}

type fileLease struct {
	mu sync.Mutex
	f  *os.File
}

// SetHolder implements Lease.
func (l *fileLease) SetHolder(holder string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	if err := l.f.Truncate(0); err == nil {
		_, _ = l.f.WriteAt([]byte(strconv.Itoa(os.Getpid())+" "+holder+"\n"), 0)
	}
}

// Release implements Lease.
func (l *fileLease) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	_ = flockRelease(l.f)
	_ = l.f.Close()
	l.f = nil
}

func parseHolder(s string) (pid int, run string) {
	fields := strings.Fields(s)
	if len(fields) > 0 {
		pid, _ = strconv.Atoi(fields[0])
	}
	if len(fields) > 1 && journal.ValidRunID(fields[1]) {
		run = fields[1]
	}
	return pid, run
}
