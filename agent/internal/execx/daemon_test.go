package execx

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHelperDaemonize is not a test: Exec.Run re-executes the test binary into it to play a program
// that forks into the background and keeps its stdout open (xclip, wl-copy do that).
func TestHelperDaemonize(t *testing.T) {
	switch os.Getenv("EXECX_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperDaemonize$")
		child.Env = []string{"EXECX_HELPER=child"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr // the child holds the pipes open
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("forked\n")
		os.Exit(0)
	case "child":
		time.Sleep(4 * time.Second)
		os.Exit(0)
	}
}

// A program that exits 0 but leaves a child holding its pipes is a success, with what it printed.
func TestBackgroundChildHoldingThePipesIsNotAnError(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	start := time.Now()
	res, err := Exec{}.Run(context.Background(), Cmd{
		Path: self, Args: []string{"-test.run=^TestHelperDaemonize$"}, Env: []string{"EXECX_HELPER=parent"},
		Timeout: 10 * time.Second, MaxOut: 1024,
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("err = %v, exit %d, want nil and 0", err, res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "forked") {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if took := time.Since(start); took > 3900*time.Millisecond {
		t.Errorf("took %s: Run waited for the background child", took)
	}
}

// Credential runs the program as another user. Only root may switch, so the test needs root.
func TestCredentialRunsAsAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to switch users")
	}
	id := must(t, "id")
	res, err := Exec{}.Run(context.Background(), Cmd{
		Path: id, Args: []string{"-u"}, Timeout: 5 * time.Second, MaxOut: 64,
		Credential: &Credential{UID: 65534, GID: 65534},
	})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "65534" {
		t.Errorf("err = %v, stdout %q, want 65534", err, res.Stdout)
	}
}
