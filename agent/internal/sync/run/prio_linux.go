//go:build linux

package run

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// I/O scheduling classes and levels (linux/ioprio.h).
const (
	ioprioWhoProcess = 1
	ioprioClassBE    = 2
	ioprioClassIdle  = 3
	ioprioClassShift = 13
	// ScheduledNice is the CPU niceness of a scheduled run's workers.
	ScheduledNice = 10
	// ManualIOLevel is the best-effort I/O level (the lowest) of a manual run's workers.
	ManualIOLevel = 7
)

// LowPriority puts the calling goroutine's OS thread at low CPU and I/O priority for the whole run
// (ark-sync.md section 3.7): a scheduled run gets the idle I/O class and nice 10, a manual run
// best-effort I/O level 7 at the normal niceness. Call it first thing in a worker goroutine. The goroutine is
// locked to its thread and never unlocks it, so when the goroutine ends the Go runtime terminates
// the thread: the low priority cannot leak to another goroutine, and threads the runtime creates for
// other work do not inherit it (the runtime starts them from a template thread). A failure to set a
// priority (a kernel or sandbox that refuses) is returned and the worker simply runs at normal
// priority: it is never fatal.
func LowPriority(scheduled bool) error {
	runtime.LockOSThread()
	tid := unix.Gettid()
	class, level := ioprioClassBE, ManualIOLevel
	if scheduled {
		class, level = ioprioClassIdle, 0
	}
	value := class<<ioprioClassShift | level
	var first error
	if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), uintptr(value)); errno != 0 {
		first = errno
	}
	if scheduled {
		if err := unix.Setpriority(unix.PRIO_PROCESS, tid, ScheduledNice); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ThreadPriority reads back the calling thread's I/O class and CPU nice value (tests, and the doctor).
// The goroutine must be locked to its thread.
func ThreadPriority() (ioClass, ioLevel, nice int, err error) {
	tid := unix.Gettid()
	r, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(tid), 0)
	if errno != 0 {
		return 0, 0, 0, errno
	}
	ioClass, ioLevel = int(r)>>ioprioClassShift, int(r)&(1<<ioprioClassShift-1)
	raw, err := unix.Getpriority(unix.PRIO_PROCESS, tid)
	if err != nil {
		return ioClass, ioLevel, 0, err
	}
	return ioClass, ioLevel, 20 - raw, nil
}
