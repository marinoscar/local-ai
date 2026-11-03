package subproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// script writes an executable shell script. The scripts are the test's own programs: the code under
// test never runs a shell, the scripts do (their shebang line is not a shell Spec.Bin).
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvironmentIsScrubbedAndFixed(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://inherited.invalid:1")
	t.Setenv("SECRET_TOKEN", "must-not-leak")
	bin := script(t, `echo "$HOME|$PATH|$LC_ALL|${HTTPS_PROXY:-none}|${SECRET_TOKEN:-none}|$#|$1"`)
	res, err := Run(context.Background(), nil, Spec{Bin: bin, Args: []string{"a b; rm -rf /"}, Home: "/var/lib/localai/sync", Timeout: 10 * time.Second})
	if err != nil || !res.OK() {
		t.Fatalf("run: %v %+v", err, res)
	}
	want := "/var/lib/localai/sync|" + execx.FixedPath + "|C|none|none|1|a b; rm -rf /"
	if got := strings.TrimSpace(res.Stdout); got != want {
		t.Fatalf("environment and argv\n got %q\nwant %q", got, want)
	}
}

func TestExtraEnvironmentIsAddedAndReservedNamesRefused(t *testing.T) {
	bin := script(t, `echo "${HTTPS_PROXY:-none}"`)
	res, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "/h", Env: []string{"HTTPS_PROXY=http://127.0.0.1:9"}, Timeout: 10 * time.Second})
	if err != nil || strings.TrimSpace(res.Stdout) != "http://127.0.0.1:9" {
		t.Fatalf("extra env: %v %q", err, res.Stdout)
	}
	for _, env := range [][]string{{"PATH=/tmp"}, {"HOME=/root"}, {"LC_ALL=x"}, {"NOEQUALS"}, {"=x"}, {"lower=x"}, {"BAD-NAME=x"}} {
		if _, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "/h", Env: env, Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
			t.Errorf("env %v: want ErrInvalid, got %v", env, err)
		}
	}
	if _, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "relative", Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
		t.Errorf("relative HOME: want ErrInvalid, got %v", err)
	}
	if _, err := Run(context.Background(), nil, Spec{Bin: "/bin/sh", Home: "/h", Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a shell must be refused: %v", err)
	}
}

func TestNonZeroExitIsAResultNotAnError(t *testing.T) {
	bin := script(t, `echo out; echo "bad thing happened" >&2; exit 3`)
	res, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "/h", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("a non-zero exit is not an error: %v", err)
	}
	if res.OK() || res.ExitCode != 3 || strings.TrimSpace(res.Stdout) != "out" || res.Tail(100) != "bad thing happened" {
		t.Fatalf("result %+v tail %q", res, res.Tail(100))
	}
}

func TestOutputIsBoundedAndTailIsSafe(t *testing.T) {
	bin := script(t, `i=0; while [ $i -lt 2000 ]; do echo "line number $i of a very chatty program"; i=$((i+1)); done; printf 'end\033[31m\001marker' >&2`)
	res, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "/h", Timeout: 20 * time.Second, MaxOut: 1024})
	if err != nil || !res.Truncated || len(res.Stdout) != 1024 {
		t.Fatalf("bounded output: %v truncated=%v len=%d", err, res.Truncated, len(res.Stdout))
	}
	if got := res.Tail(50); strings.ContainsAny(got, "\x1b\x01") || !strings.HasSuffix(got, "marker") {
		t.Fatalf("tail %q", got)
	}
	long := Result{Stderr: strings.Repeat("x", 500)}
	if got := long.Tail(20); len([]rune(got)) != 23 || !strings.HasPrefix(got, "...") {
		t.Fatalf("tail of a long message: %q", got)
	}
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	bin := script(t, `(sleep 3; touch `+marker+`) & sleep 30`)
	start := time.Now()
	_, err := Run(context.Background(), nil, Spec{Bin: bin, Home: "/h", Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the timeout took %s", time.Since(start))
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a grandchild survived the timeout: the process group was not killed")
	}
}

func TestCancelReturnsTheContextError(t *testing.T) {
	bin := script(t, `sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := Run(ctx, nil, Spec{Bin: bin, Home: "/h", Timeout: time.Minute})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("the cancel took %s", time.Since(start))
	}
}

func TestStartFailureAndTheRunnerSeam(t *testing.T) {
	_, err := Run(context.Background(), nil, Spec{Bin: "/nonexistent/dir/tool", Home: "/h", Timeout: time.Second})
	if !errors.Is(err, ErrStart) {
		t.Fatalf("want ErrStart, got %v", err)
	}
	cred := &execx.Credential{UID: 1234, GID: 5678}
	f := fake.New().Expect(fake.Key("/opt/x/tool", "verify", "/f"), execx.Result{ExitCode: 1, Stderr: []byte("nope")}, nil)
	res, err := Run(context.Background(), f, Spec{Bin: "/opt/x/tool", Args: []string{"verify", "/f"}, Home: "/h", Credential: cred, Timeout: time.Second})
	if err != nil || res.ExitCode != 1 || res.Stderr != "nope" {
		t.Fatalf("fake runner: %v %+v", err, res)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Credential == nil || *calls[0].Credential != *cred || calls[0].MaxOut != DefaultMaxOut {
		t.Fatalf("the call did not carry the credential and the default cap: %+v", calls)
	}
}
