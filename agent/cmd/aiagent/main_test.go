package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func runCmd(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
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
		{"aictl anything", []string{"aictl", "bogus"}, 2, "", "unknown command"},
		{"aictl no args", []string{"aictl"}, 2, "", "aictl: command line"},
		{"aiagent ctl --help", []string{"aiagent", "ctl", "--help"}, 0, "aictl: command line", ""},
		{"aiagent serve stops on cancel", []string{"aiagent", "serve"}, 0, "", ""},
		{"aiagent version", []string{"aiagent", "version"}, 0, "aiagent ", ""},
		{"release file name is the agent, not the cli", []string{"./aiagent-linux-amd64", "version"}, 0, "aiagent ", ""},
		{"migrate llama-qwen is a stub", []string{"aiagent", "migrate", "llama-qwen", "--dry-run"}, 1, "", "not implemented"},
		{"migrate without target", []string{"aiagent", "migrate"}, 2, "", "usage:"},
		{"update-rollback-check is a stub", []string{"aiagent", "update-rollback-check"}, 1, "", "not implemented"},
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
