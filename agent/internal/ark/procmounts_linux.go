//go:build linux

package ark

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultMountInfoPath is the real mount table.
const DefaultMountInfoPath = "/proc/self/mountinfo"

// pollSlice is how long one poll(2) call waits before it looks at ctx again. It only bounds how
// fast Changes stops after the context is cancelled; a mount-table change wakes the poll at once.
const pollSlice = time.Second

// ProcMounts is the real MountSource: it reads /proc/self/mountinfo and uses poll(2) with
// POLLPRI|POLLERR on it, which is how the kernel signals every change of the mount table
// (ark-sync.md section 4.1.2 step 1).
type ProcMounts struct {
	// Path is the file to read; default DefaultMountInfoPath. Tests point it elsewhere.
	Path string
}

func (p ProcMounts) path() string {
	if p.Path == "" {
		return DefaultMountInfoPath
	}
	return p.Path
}

// Read implements MountSource: it reads the whole file and parses it. The error is that of
// ParseMountInfo; the entries are valid even when it reports skipped lines.
func (p ProcMounts) Read() ([]MountEntry, error) {
	f, err := os.Open(p.path())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMountInfo(f)
}

// Changes implements MountSource. It opens the file, reads it to the end once (which arms the
// kernel's change counter for this open file), then polls it; after every wake-up it seeks to the
// start and reads it again to re-arm, and sends one signal. The channel has room for one pending
// signal, so a burst of changes coalesces. It is closed when ctx is done. If the file cannot be
// opened the channel is closed at once and the detector falls back on its timer.
func (p ProcMounts) Changes(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	f, err := os.Open(p.path())
	if err != nil {
		close(ch)
		return ch
	}
	var once sync.Once
	closeAll := func() { once.Do(func() { f.Close(); close(ch) }) }
	go func() {
		defer closeAll()
		if _, err := io.Copy(io.Discard, f); err != nil {
			return
		}
		fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLPRI | unix.POLLERR}}
		for ctx.Err() == nil {
			fds[0].Revents = 0
			n, err := unix.Poll(fds, int(pollSlice/time.Millisecond))
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return
			}
			if n == 0 || fds[0].Revents&(unix.POLLPRI|unix.POLLERR) == 0 {
				continue
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return
			}
			if _, err := io.Copy(io.Discard, f); err != nil {
				return
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch
}
