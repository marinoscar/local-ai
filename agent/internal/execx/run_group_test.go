package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperRunParent is not a test: Run starts the test binary into it to play a program (cmake) that starts a
// child of its own (ninja), writes the child's pid to $EXECX_PID_FILE and waits for it. A shell cannot play it:
// Run refuses shells by design.
func TestHelperRunParent(t *testing.T) {
	switch os.Getenv("EXECX_RUN_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperRunParent$")
		child.Env = []string{"EXECX_RUN_HELPER=child"}
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv("EXECX_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(4)
		}
		_ = child.Wait()
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// Run is what the build manager starts cmake with (a compile is cmake, then ninja, then a compiler per file):
// cancelling it (Ctrl-C of `aictl rebuild-llama`, answered "yes" to "Stop the build?") or timing it out must
// take the whole process group, not only the program it started, or a stopped build would go on compiling in
// the background (aictl.md section 4.23, issue #54 note 4). Start has the same tests in stream_group_test.go.
func TestRunKillsTheGrandchildOnCancelAndOnTimeout(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		cancel  bool
		wantErr func(error) bool
	}{
		{"cancelled", time.Minute, true, func(err error) bool { return errors.Is(err, context.Canceled) && !errors.Is(err, ErrTimeout) }},
		{"timed out", 1500 * time.Millisecond, false, func(err error) bool { return errors.Is(err, ErrTimeout) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := Exec{}.Run(ctx, Cmd{
					Path: self, Args: []string{"-test.run=^TestHelperRunParent$"},
					Env:     []string{"EXECX_RUN_HELPER=parent", "EXECX_PID_FILE=" + pidFile},
					Timeout: tc.timeout, MaxOut: 1024,
				})
				done <- err
			}()
			var pid int
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if b, err := os.ReadFile(pidFile); err == nil {
					if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
						pid = n
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("the helper never wrote the pid of its child")
			}
			if !alive(pid) {
				t.Fatal("the grandchild is not running before the cancellation")
			}
			if tc.cancel {
				cancel()
			}
			select {
			case err := <-done:
				if !tc.wantErr(err) {
					t.Fatalf("err = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return")
			}
			for end := time.Now().Add(5 * time.Second); alive(pid) && time.Now().Before(end); {
				time.Sleep(10 * time.Millisecond)
			}
			if alive(pid) {
				t.Errorf("the grandchild %d survived: the build would go on compiling after it was stopped", pid)
			}
		})
	}
}
