package local

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrBusy means another aictl command that changes the node's files is running.
var ErrBusy = errors.New("another aictl command is changing the node files")

// lockName is the lock file inside the configuration directory.
const lockName = ".aictl.lock"

// Lock takes the exclusive, non-blocking lock that every aictl command changing files under
// /etc/localai holds while it works, so two of them (two terminals, a script and a person) never
// interleave their writes. A second caller gets ErrBusy at once. The lock belongs to the open file
// description, so the kernel drops it if the process dies: no stale lock is ever left behind.
// Release it with the returned function.
func Lock(e Env) (release func(), err error) {
	path := filepath.Join(e.Paths.EtcDir, lockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fileErr("write", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fileErr("write", path, err)
	}
	return func() { _ = f.Close() }, nil
}
