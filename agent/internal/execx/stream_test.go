package execx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func startProc(t *testing.T, ctx context.Context, c StreamCmd) Process {
	t.Helper()
	if c.Path == "" {
		c.Path = must(t, "cat")
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	if c.MaxStderr == 0 {
		c.MaxStderr = 1024
	}
	p, err := Exec{}.Start(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartStreamsStdoutAndPassesStdin(t *testing.T) {
	p := startProc(t, context.Background(), StreamCmd{Stdin: strings.NewReader("hello\n")})
	out, err := io.ReadAll(p.Stdout())
	if err != nil || string(out) != "hello\n" {
		t.Fatalf("stdout = %q, %v", out, err)
	}
	res, err := p.Wait()
	if err != nil || res.ExitCode != 0 || res.Duration <= 0 {
		t.Errorf("Wait = %+v, %v", res, err)
	}
}

func TestStartExitStatusAndStderrAreResultsNotErrors(t *testing.T) {
	head := must(t, "head")
	p := startProc(t, context.Background(), StreamCmd{Path: head, Args: []string{"/no/such/file/" + strings.Repeat("x", 300)}, MaxStderr: 40})
	_, _ = io.Copy(io.Discard, p.Stdout())
	res, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || len(res.Stderr) != 40 || !res.StderrTruncated {
		t.Errorf("res = exit %d, stderr %d bytes, truncated %v", res.ExitCode, len(res.Stderr), res.StderrTruncated)
	}
}

func TestStartEnvironmentAndDirAreExact(t *testing.T) {
	env := must(t, "env")
	t.Setenv("LOCALAI_SECRET_FOR_TEST", "must-not-leak")
	p := startProc(t, context.Background(), StreamCmd{Path: env, Env: []string{"LD_LIBRARY_PATH=/x"}})
	out, _ := io.ReadAll(p.Stdout())
	_, _ = p.Wait()
	if string(out) != "LD_LIBRARY_PATH=/x\n" {
		t.Errorf("env = %q", out)
	}
	pwd := must(t, "pwd")
	dir := t.TempDir()
	p = startProc(t, context.Background(), StreamCmd{Path: pwd, Dir: dir})
	out, _ = io.ReadAll(p.Stdout())
	_, _ = p.Wait()
	if got := strings.TrimSpace(string(out)); got != dir {
		t.Errorf("cwd = %q, want %q", got, dir)
	}
}

func TestStartTimeoutKillsTheGroup(t *testing.T) {
	sleep := must(t, "sleep")
	p := startProc(t, context.Background(), StreamCmd{Path: sleep, Args: []string{"30"}, Timeout: 200 * time.Millisecond})
	start := time.Now()
	_, _ = io.Copy(io.Discard, p.Stdout()) // ends when the child is killed
	res, err := p.Wait()
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 5*time.Second || res.ExitCode == 0 {
		t.Errorf("not killed in time: %s, exit %d", time.Since(start), res.ExitCode)
	}
}

func TestStartKillEndsTheStreamWithoutAnError(t *testing.T) {
	sleep := must(t, "sleep")
	p := startProc(t, context.Background(), StreamCmd{Path: sleep, Args: []string{"30"}})
	p.Kill()
	p.Kill() // idempotent
	_, _ = io.Copy(io.Discard, p.Stdout())
	res, err := p.Wait()
	if err != nil {
		t.Errorf("a plain Kill is not an error: %v", err)
	}
	if res.ExitCode == 0 {
		t.Error("a killed process cannot have exit status 0")
	}
	p.Kill() // after Wait: harmless
}

func TestStartCancelledContextKillsAndIsNotATimeout(t *testing.T) {
	sleep := must(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	p := startProc(t, ctx, StreamCmd{Path: sleep, Args: []string{"30"}})
	cancel()
	_, _ = io.Copy(io.Discard, p.Stdout())
	_, err := p.Wait()
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestStartAppliesTheResourceLimits(t *testing.T) {
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skip("needs /proc")
	}
	p := startProc(t, context.Background(), StreamCmd{Rlimits: []Rlimit{{RlimitNOFILE, 64}, {RlimitCPU, 60}, {RlimitAS, 2 << 30}}})
	pid := p.(*streamProc).cmd.Process.Pid
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	p.Kill()
	_, _ = io.Copy(io.Discard, p.Stdout())
	_, _ = p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Max open files": "64", "Max cpu time": "60", "Max address space": "2147483648"}
	for _, line := range strings.Split(string(b), "\n") {
		for name, v := range want {
			if strings.HasPrefix(line, name) {
				f := strings.Fields(strings.TrimPrefix(line, name))
				if len(f) < 2 || f[0] != v || f[1] != v {
					t.Errorf("%s: %q, want soft and hard %s", name, line, v)
				}
				delete(want, name)
			}
		}
	}
	if len(want) != 0 {
		t.Errorf("limits not found in /proc: %v", want)
	}
}

func TestStartRefusesAnUnknownResourceAndNeverRunsUnlimited(t *testing.T) {
	cat := must(t, "cat")
	_, err := Exec{}.Start(context.Background(), StreamCmd{Path: cat, Timeout: time.Second, MaxStderr: 8, Rlimits: []Rlimit{{Resource: 99, Limit: 1}}})
	if !errors.Is(err, ErrRlimit) {
		t.Errorf("err = %v, want ErrRlimit", err)
	}
}

func TestStartRefusesInvalidCommands(t *testing.T) {
	cat := must(t, "cat")
	cases := map[string]StreamCmd{
		"relative":  {Path: "cat", Timeout: time.Second, MaxStderr: 8},
		"a shell":   {Path: "/bin/sh", Timeout: time.Second, MaxStderr: 8},
		"timeout":   {Path: cat, MaxStderr: 8},
		"no stderr": {Path: cat, Timeout: time.Second},
		"NUL":       {Path: cat, Args: []string{"a\x00"}, Timeout: time.Second, MaxStderr: 8},
	}
	for name, c := range cases {
		if _, err := (Exec{}).Start(context.Background(), c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := (Exec{}).Start(context.Background(), StreamCmd{Path: "/no/such/program", Timeout: time.Second, MaxStderr: 8}); err == nil {
		t.Error("a missing program must fail to start")
	}
}
