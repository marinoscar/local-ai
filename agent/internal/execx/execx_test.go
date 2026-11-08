package execx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, name string) string {
	t.Helper()
	p, err := Lookup(name)
	if err != nil {
		t.Skipf("%s not available on this machine: %v", name, err)
	}
	return p
}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	echo := must(t, "echo")
	res, err := Exec{}.Run(context.Background(), Cmd{Path: echo, Args: []string{"hello", "world"}, Timeout: 5 * time.Second, MaxOut: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "hello world\n" || res.ExitCode != 0 || res.Truncated {
		t.Errorf("unexpected result: %+v", res)
	}
	// A non-zero exit is a result, not an error.
	f := must(t, "false")
	res, err = Exec{}.Run(context.Background(), Cmd{Path: f, Timeout: 5 * time.Second, MaxOut: 16})
	if err != nil || res.ExitCode != 1 {
		t.Errorf("false: err=%v exit=%d, want nil and 1", err, res.ExitCode)
	}
}

func TestTimeoutKillsTheChild(t *testing.T) {
	sleep := must(t, "sleep")
	start := time.Now()
	res, err := Exec{}.Run(context.Background(), Cmd{Path: sleep, Args: []string{"30"}, Timeout: 200 * time.Millisecond, MaxOut: 16})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s: the child was not killed", took)
	}
	if res.Duration <= 0 {
		t.Error("Duration not set on timeout")
	}
}

func TestCancelledContextIsNotATimeout(t *testing.T) {
	sleep := must(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := Exec{}.Run(ctx, Cmd{Path: sleep, Args: []string{"30"}, Timeout: time.Minute, MaxOut: 16})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestOutputCapSetsTruncatedAndKeepsTheFirstBytes(t *testing.T) {
	head := must(t, "head")
	res, err := Exec{}.Run(context.Background(), Cmd{Path: head, Args: []string{"-c", "100000", "/dev/zero"}, Timeout: 10 * time.Second, MaxOut: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 1000 || !res.Truncated {
		t.Errorf("len=%d truncated=%v, want 1000 and true", len(res.Stdout), res.Truncated)
	}
	// Stderr is capped independently.
	res, _ = Exec{}.Run(context.Background(), Cmd{Path: head, Args: []string{"/no/such/file/" + strings.Repeat("x", 300)}, Timeout: 10 * time.Second, MaxOut: 50})
	if len(res.Stderr) != 50 || !res.Truncated {
		t.Errorf("stderr len=%d truncated=%v, want 50 and true", len(res.Stderr), res.Truncated)
	}
}

func TestDefaultEnvironmentIsTheFixedPathOnly(t *testing.T) {
	env := must(t, "env")
	t.Setenv("LOCALAI_SECRET_FOR_TEST", "must-not-leak")
	res, err := Exec{}.Run(context.Background(), Cmd{Path: env, Timeout: 5 * time.Second, MaxOut: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(res.Stdout), "PATH="+FixedPath+"\n"; got != want {
		t.Errorf("env = %q, want %q", got, want)
	}
	res, _ = Exec{}.Run(context.Background(), Cmd{Path: env, Env: []string{"A=1"}, Timeout: 5 * time.Second, MaxOut: 4096})
	if string(res.Stdout) != "A=1\n" {
		t.Errorf("explicit env = %q, want exactly A=1", res.Stdout)
	}
}

func TestStdinIsPassedThrough(t *testing.T) {
	cat := must(t, "cat")
	res, err := Exec{}.Run(context.Background(), Cmd{Path: cat, Stdin: strings.NewReader("from stdin"), Timeout: 5 * time.Second, MaxOut: 64})
	if err != nil || string(res.Stdout) != "from stdin" {
		t.Errorf("err=%v out=%q", err, res.Stdout)
	}
}

func TestInvalidCommandsAreRefusedBeforeStarting(t *testing.T) {
	echo := must(t, "echo")
	cases := map[string]Cmd{
		"empty path":    {Timeout: time.Second, MaxOut: 1},
		"relative path": {Path: "bin/echo", Timeout: time.Second, MaxOut: 1},
		"unclean path":  {Path: "/usr/bin/../bin/echo", Timeout: time.Second, MaxOut: 1},
		"a shell":       {Path: "/bin/sh", Args: []string{"-c", "id"}, Timeout: time.Second, MaxOut: 1},
		"bash":          {Path: "/usr/bin/bash", Timeout: time.Second, MaxOut: 1},
		"no timeout":    {Path: echo, MaxOut: 1},
		"no output cap": {Path: echo, Timeout: time.Second},
		"NUL in arg":    {Path: echo, Args: []string{"a\x00b"}, Timeout: time.Second, MaxOut: 1},
	}
	for name, c := range cases {
		if _, err := (Exec{}).Run(context.Background(), c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestLookupRefusesRelativeAndOutsideNames(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	noexec := filepath.Join(dir, "data")
	if err := os.WriteFile(noexec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs := []string{dir}

	if p, err := lookupIn(dirs, "tool"); err != nil || p != bin {
		t.Errorf("bare name: %q, %v", p, err)
	}
	if p, err := lookupIn(dirs, bin); err != nil || p != bin {
		t.Errorf("absolute name inside the fixed dirs: %q, %v", p, err)
	}
	for _, name := range []string{"", "./tool", "bin/tool", "../tool", "tool/../tool", dir + "/../" + filepath.Base(dir) + "/tool"} {
		if _, err := lookupIn(dirs, name); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", name, err)
		}
	}
	other := t.TempDir()
	outside := filepath.Join(other, "tool")
	_ = os.WriteFile(outside, []byte("x"), 0o755)
	for _, name := range []string{outside, "/etc/passwd", "data", "missing"} {
		if _, err := lookupIn(dirs, name); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: err = %v, want ErrNotFound", name, err)
		}
	}
	// The real Lookup is bound to the fixed PATH.
	if _, err := Lookup("./echo"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Lookup(./echo) = %v, want ErrInvalid", err)
	}
	if _, err := Lookup("/tmp/anything"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup(/tmp/anything) = %v, want ErrNotFound", err)
	}
}

// errWriter fails every write, like a progress reader that went away.
type errWriter struct{ got strings.Builder }

func (w *errWriter) Write(p []byte) (int, error) {
	w.got.Write(p)
	return 0, errors.New("gone")
}

func TestTeeSeesTheOutputAndItsErrorsAreIgnored(t *testing.T) {
	echo := must(t, "echo")
	tee := &errWriter{}
	res, err := Exec{}.Run(context.Background(), Cmd{Path: echo, Args: []string{"[1/2] one"}, Timeout: 5 * time.Second, MaxOut: 4, Tee: tee})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("err=%v exit=%d", err, res.ExitCode)
	}
	if tee.got.String() != "[1/2] one\n" {
		t.Errorf("tee got %q", tee.got.String())
	}
	if string(res.Stdout) != "[1/2" || !res.Truncated {
		t.Errorf("the cap must still apply to the result: %q truncated=%v", res.Stdout, res.Truncated)
	}
}
