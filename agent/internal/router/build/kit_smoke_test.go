package build

import (
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

func TestSmokeWritesAFewWordsWithTheSmallestModel(t *testing.T) {
	h := newHarness(t)
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	var cli execx.Cmd
	for _, c := range h.exec.all() {
		if strings.HasSuffix(c.Path, "llama-cli") {
			cli = c
		}
	}
	// The new build's own llama-cli, in the directory the build is still in (builds/<id>.tmp).
	if cli.Path != h.root+"/builds/"+testID+".tmp/bin/llama-cli" {
		t.Fatalf("llama-cli ran as %q", cli.Path)
	}
	want := []string{"-m", "/models/tiny.gguf", "-n", "8", "-p", "Hello", "-ngl", "99", "--no-warmup"}
	if strings.Join(cli.Args, " ") != strings.Join(want, " ") || cli.Timeout != 120*time.Second {
		t.Errorf("argv %v timeout %v, want %v and 120 s", cli.Args, cli.Timeout, want)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "write a few words") {
			t.Errorf("unexpected warning %q", w)
		}
	}
	if !strings.Contains(readFile(t, res.LogPath), "llama-cli -m /models/tiny.gguf") {
		t.Error("the completion is not in build.log")
	}
}

func TestSmokeWithoutAModelIsSkippedWithAWarning(t *testing.T) {
	tests := []struct {
		name string
		set  func(h *harness)
	}{
		{"no function", func(h *harness) { h.opts.SmokeModel = nil }},
		{"no model on the computer", func(h *harness) { h.opts.SmokeModel = func() (string, bool) { return "", false } }},
		{"an empty path", func(h *harness) { h.opts.SmokeModel = func() (string, bool) { return "", true } }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.set(h)
			res, err := h.build()
			if err != nil {
				t.Fatal(err)
			}
			if h.exec.ran("llama-cli") {
				t.Error("llama-cli ran without a model")
			}
			if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "write a few words") || !res.Switched {
				t.Errorf("warnings %v switched %v", res.Warnings, res.Switched)
			}
		})
	}
}

func TestSmokeCompletionFailuresKeepCurrentUntouched(t *testing.T) {
	tests := []struct {
		name  string
		res   execx.Result
		err   error
		cause string
	}{
		{"llama-cli exits non-zero", exitWith(1, "error: failed to load model"), nil, "smoke_generate_exit"},
		{"llama-cli writes nothing", okOut("  \n"), nil, "smoke_generate_empty"},
		{"llama-cli does not finish in time", execx.Result{}, execx.ErrTimeout, "smoke_generate_exec"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			res := tc.res
			h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
				if strings.HasSuffix(c.Path, "llama-cli") {
					return res, tc.err, true
				}
				return execx.Result{}, nil, false
			})
			_, err := h.build()
			e := mustErr(t, err)
			if e.Code != doctor.LLM019 || e.Stage != StageSmoke || e.Details["cause"] != tc.cause {
				t.Fatalf("got %s %s %v, want LAI-LLM-019 smoke %s", e.Code, e.Stage, e.Details["cause"], tc.cause)
			}
			if h.link() != "builds/"+oldID || h.agent.restarts != 0 || h.exists("builds/"+testID) || !h.exists("builds/"+testID+".tmp.failed/build.log") {
				t.Errorf("link %q, restarts %d: the failed build must be kept for inspection and nothing switched", h.link(), h.agent.restarts)
			}
		})
	}
}

func TestPrebuiltIsAskedForAFewWordsToo(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if !h.exec.ran("llama-cli -m /models/tiny.gguf") {
		t.Errorf("no completion: %v", h.exec.lines())
	}
}
