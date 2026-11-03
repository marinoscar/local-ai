//go:build linux

package run

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
)

// The worker priority is real: a scheduled worker's thread ends up in the idle I/O class at nice 10, a
// manual worker's in best-effort level 7. The check reads it back from the kernel; a sandbox that
// refuses ioprio_set or setpriority skips the test rather than failing it (the engine treats a refusal
// as non-fatal too).
func TestLowPriorityIsAppliedToTheWorkerThread(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scheduled bool
		class     int
		level     int
		nice      int
	}{
		{"scheduled: idle I/O class and nice 10", true, ioprioClassIdle, 0, ScheduledNice},
		{"manual: best-effort level 7", false, ioprioClassBE, ManualIOLevel, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type got struct {
				class, level, nice int
				setErr, readErr    error
			}
			out := make(chan got, 1)
			go func() {
				// The goroutine locks its thread and never unlocks it: the thread dies with it.
				var g got
				g.setErr = LowPriority(tc.scheduled)
				if runtime.GOOS == "linux" {
					g.class, g.level, g.nice, g.readErr = ThreadPriority()
				}
				out <- g
			}()
			g := <-out
			if g.setErr != nil && (errors.Is(g.setErr, syscall.EPERM) || errors.Is(g.setErr, syscall.ENOSYS) || errors.Is(g.setErr, syscall.EINVAL)) {
				t.Skipf("the kernel refuses a priority change here: %v", g.setErr)
			}
			if g.setErr != nil || g.readErr != nil {
				t.Fatalf("set: %v, read: %v", g.setErr, g.readErr)
			}
			if g.class != tc.class || g.level != tc.level {
				t.Fatalf("ioprio = class %d level %d, want class %d level %d", g.class, g.level, tc.class, tc.level)
			}
			if tc.scheduled && g.nice != tc.nice {
				t.Fatalf("nice = %d, want %d", g.nice, tc.nice)
			}
		})
	}
}
