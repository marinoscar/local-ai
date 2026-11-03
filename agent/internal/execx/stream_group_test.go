package execx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperParentOfSleeper is not a test: Start re-executes the test binary into it to play a
// program that starts a child of its own and prints the child's pid.
func TestHelperParentOfSleeper(t *testing.T) {
	switch os.Getenv("EXECX_STREAM_HELPER") {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperParentOfSleeper$")
		child.Env = []string{"EXECX_STREAM_HELPER=child"}
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Println(child.Process.Pid)
		_ = child.Wait()
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

// alive reports whether pid is a running process (a zombie waiting for its parent is not).
func alive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": the state follows the last closing parenthesis.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

// The timeout kills the whole process group: a child of the program does not outlive it, so no
// orphan is left behind.
func TestStartTimeoutKillsTheGrandchildToo(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	p, err := Exec{}.Start(context.Background(), StreamCmd{
		Path: self, Args: []string{"-test.run=^TestHelperParentOfSleeper$"}, Env: []string{"EXECX_STREAM_HELPER=parent"},
		Timeout: 3 * time.Second, MaxStderr: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(p.Stdout())
	line, err := r.ReadString('\n') // the child exists once its pid is printed
	if err != nil {
		t.Fatalf("no pid from the helper: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid %q: %v", line, err)
	}
	if !alive(pid) {
		t.Fatal("the grandchild is not running before the timeout")
	}
	_, _ = io.Copy(io.Discard, r)
	if _, err := p.Wait(); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Errorf("the grandchild %d survived the timeout: an orphan was left", pid)
	}
}

// Kill takes the group down too.
func TestStartKillTakesTheGrandchildToo(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	p, err := Exec{}.Start(context.Background(), StreamCmd{
		Path: self, Args: []string{"-test.run=^TestHelperParentOfSleeper$"}, Env: []string{"EXECX_STREAM_HELPER=parent"},
		Timeout: time.Minute, MaxStderr: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(p.Stdout())
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(line))
	p.Kill()
	_, _ = io.Copy(io.Discard, r)
	if _, err := p.Wait(); err != nil {
		t.Errorf("a plain Kill is not an error: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Errorf("the grandchild %d survived Kill", pid)
	}
}
