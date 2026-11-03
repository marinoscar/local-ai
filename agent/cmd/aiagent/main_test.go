package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

func runCmd(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	// No test starts the real daemon: it is root-only and listens on the machine's ports.
	old := serve
	serve = func(context.Context, io.Writer) int { return 0 }
	t.Cleanup(func() { serve = old })
	var out, errb bytes.Buffer
	code := run(ctx, args, &out, &errb)
	return code, out.String(), errb.String()
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// One case per row of the dispatch table in agent.md section 3.1.
func TestDispatch(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode int
		outHas   string
		errHas   string
	}{
		{"aictl --help", []string{"/usr/local/bin/aictl", "--help"}, 0, "aictl: command line", ""},
		{"aictl help", []string{"aictl", "help"}, 0, "aictl: command line", ""},
		{"aictl anything", []string{"aictl", "bogus"}, 2, "", "Unknown command"},
		{"aictl no args", []string{"aictl"}, 2, "", "aictl: command line"},
		{"aiagent ctl --help", []string{"aiagent", "ctl", "--help"}, 0, "aictl: command line", ""},
		{"aiagent serve runs the daemon", []string{"aiagent", "serve"}, 0, "", ""},
		{"aiagent version", []string{"aiagent", "version"}, 0, "aiagent ", ""},
		{"release file name is the agent, not the cli", []string{"./aiagent-linux-amd64", "version"}, 0, "aiagent ", ""},
		{"migrate llama-qwen is a stub", []string{"aiagent", "migrate", "llama-qwen", "--dry-run"}, 1, "", "not implemented"},
		{"migrate without target", []string{"aiagent", "migrate"}, 2, "", "usage:"},
		{"update-rollback-check takes no argument", []string{"aiagent", "update-rollback-check", "now"}, 2, "", "usage:"},
		{"no argument prints usage", []string{"aiagent"}, 2, "", "usage:"},
		{"unknown subcommand prints usage", []string{"aiagent", "bogus"}, 2, "", "usage:"},
		{"version with a bad flag", []string{"aiagent", "version", "--yaml"}, 2, "", "usage:"},
		{"empty argv", []string{}, 2, "", "usage:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs := runCmd(t, cancelled(), c.args...)
			if code != c.wantCode {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, c.wantCode, out, errs)
			}
			if !strings.Contains(out, c.outHas) {
				t.Errorf("stdout %q lacks %q", out, c.outHas)
			}
			if !strings.Contains(errs, c.errHas) {
				t.Errorf("stderr %q lacks %q", errs, c.errHas)
			}
		})
	}
}

func TestVersionFormat(t *testing.T) {
	_, out, _ := runCmd(t, cancelled(), "aiagent", "version")
	re := regexp.MustCompile(`^aiagent \S+ \(\S+, ` + runtime.GOOS + `/` + runtime.GOARCH + `, built \S+\)\n$`)
	if !re.MatchString(out) {
		t.Errorf("version line %q does not match the agent.md format", out)
	}
}

func TestVersionJSONHasExactlyFiveKeys(t *testing.T) {
	code, out, _ := runCmd(t, cancelled(), "aiagent", "version", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not one JSON document: %v (%q)", err, out)
	}
	for _, k := range []string{"version", "commit", "os", "arch", "built_at"} {
		if m[k] == "" {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
	if len(m) != 5 || m["os"] != runtime.GOOS || m["arch"] != runtime.GOARCH {
		t.Errorf("unexpected document: %v", m)
	}
}

type recordExec struct{ calls []string }

func (r *recordExec) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.calls = append(r.calls, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	return execx.Result{}, nil
}

// useRollbackRunner points update-rollback-check at a temp node for the duration of the test.
func useRollbackRunner(t *testing.T) (update.Paths, *recordExec) {
	t.Helper()
	root := t.TempDir()
	paths := update.Paths{UpdateDir: filepath.Join(root, "update"), BinDir: filepath.Join(root, "bin")}
	for _, d := range []string{paths.UpdateDir, paths.BinDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recordExec{}
	old := rollbackRunner
	rollbackRunner = func(stderr io.Writer) *update.Runner {
		return &update.Runner{
			Paths: paths, Current: "0.1.0", Exec: rec, Systemctl: "/usr/bin/systemctl",
			Log: slog.New(slog.NewJSONHandler(stderr, nil)),
		}
	}
	t.Cleanup(func() { rollbackRunner = old })
	return paths, rec
}

func TestUpdateRollbackCheck(t *testing.T) {
	writeMarker := func(t *testing.T, paths update.Paths) {
		t.Helper()
		now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
		b, _ := json.Marshal(update.Pending{From: "0.1.0", To: "0.2.0", StartedAt: now, Deadline: now.Add(2 * time.Minute)})
		if err := os.WriteFile(paths.Pending(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("the update was confirmed: nothing to do", func(t *testing.T) {
		_, rec := useRollbackRunner(t)
		code, _, errs := runCmd(t, context.Background(), "aiagent", "update-rollback-check")
		if code != 0 || !strings.Contains(errs, "none") || len(rec.calls) != 0 {
			t.Fatalf("exit %d, stderr %q, calls %v", code, errs, rec.calls)
		}
	})
	t.Run("the update was not confirmed: roll back and restart", func(t *testing.T) {
		paths, rec := useRollbackRunner(t)
		writeMarker(t, paths)
		if err := os.WriteFile(paths.Agent(), []byte("new"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.Prev(), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		code, _, errs := runCmd(t, context.Background(), "aiagent", "update-rollback-check")
		if code != 0 || !strings.Contains(errs, "rolled_back") {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
		if got, _ := os.ReadFile(paths.Agent()); string(got) != "old" {
			t.Fatalf("aiagent holds %q", got)
		}
		if len(rec.calls) != 1 || rec.calls[0] != "systemctl --no-block restart localai-agent.service" {
			t.Fatalf("calls %v", rec.calls)
		}
	})
	t.Run("no previous binary: exit 1 with the doctor code", func(t *testing.T) {
		paths, _ := useRollbackRunner(t)
		writeMarker(t, paths)
		if err := os.WriteFile(paths.Agent(), []byte("new"), 0o755); err != nil {
			t.Fatal(err)
		}
		code, _, errs := runCmd(t, context.Background(), "aiagent", "update-rollback-check")
		if code != 1 || !strings.Contains(errs, "LAI-AGT-009") {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
	})
}
