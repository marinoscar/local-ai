package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// Tests of `aictl rebuild-agent` (KIT-05, aictl.md section 4.23b). The engine is replaced by a fake that
// records its options and requests (its own behaviour is covered in internal/router/build); the node and
// its kit are temporary directories.

type fakeAgentEngine struct {
	opt      build.AgentOptions
	report   build.AgentReport
	checkErr error
	res      build.AgentResult
	err      error
	progress []build.Progress
	reqs     []build.AgentRequest
	checks   []build.AgentRequest
}

func (e *fakeAgentEngine) Check(_ context.Context, req build.AgentRequest) (build.AgentReport, error) {
	e.checks = append(e.checks, req)
	return e.report, e.checkErr
}

func (e *fakeAgentEngine) Build(_ context.Context, req build.AgentRequest) (build.AgentResult, error) {
	e.reqs = append(e.reqs, req)
	for _, p := range e.progress {
		if req.Progress != nil {
			req.Progress(p)
		}
	}
	return e.res, e.err
}

type agentFix struct {
	*kitFix
	eng  *fakeAgentEngine
	made int
}

func goodReport() build.AgentReport {
	return build.AgentReport{Tag: "node-v0.4.0", Version: "0.4.0", GoVersion: "1.24.7", Checks: []build.Check{
		{ID: "source", Title: "The agent source in the kit", OK: true, Detail: "local-ai-node-v0.4.0-src.tar.gz (21 MB)"},
		{ID: "toolchain", Title: "The Go toolchain in the kit", OK: true, Detail: "go1.24.7.linux-amd64.tar.gz (75 MB)"},
		{ID: "vendor", Title: "The source is complete and vendored", OK: true, Detail: "agent/go.mod, cmd/aiagent and 41 vendored packages"},
		{ID: "go_version", Title: "The Go toolchain is new enough", OK: true, Detail: "Go 1.24.7"},
		{ID: "compile", Title: "The compile can run as an unprivileged user", OK: true, Detail: "as localai"},
		{ID: "disk", Title: "Free disk space for the build", OK: true, Detail: "12.0 GB free in /var/lib/localai/build"},
	}}
}

func newAgentFix(t *testing.T) *agentFix {
	t.Helper()
	f := &agentFix{kitFix: newKitFix(t)}
	f.eng = &fakeAgentEngine{
		report: goodReport(),
		res:    build.AgentResult{Tag: "node-v0.4.0", Version: "0.4.0", Switched: true, Restarted: true, Previous: "0.3.0"},
	}
	oldNew, oldUser := agentNewEngine, agentCompileUser
	agentNewEngine = func(opt build.AgentOptions) agentEngine {
		f.made++
		f.eng.opt = opt
		return f.eng
	}
	agentCompileUser = build.CompileAsCaller
	t.Cleanup(func() { agentNewEngine, agentCompileUser = oldNew, oldUser })
	return f
}

// ---- the happy path -----------------------------------------------------------------------------

func TestRebuildAgentSuccessLine(t *testing.T) {
	f := newAgentFix(t)
	f.eng.progress = []build.Progress{{Stage: build.StageBuild, Message: "Compiling the agent (no internet is used)"}}
	r := f.run(t, "rebuild-agent", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-agent.en", r)
	if !strings.Contains(unwrap(r.out), "✓ Built aiagent v0.4.0 from the ark kit (no internet used). The agent restarted and is healthy.") {
		t.Errorf("stdout = %q", r.out)
	}
	es := f.run(t, "--lang", "es", "rebuild-agent", "--yes")
	if es.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", es.code, es.out, es.errOut)
	}
	checkGolden(t, "rebuild-agent.es", es)
	if len(f.eng.reqs) != 2 || f.eng.reqs[0].DryRun {
		t.Errorf("requests = %+v", f.eng.reqs)
	}
}

func TestRebuildAgentWiresTheKitTheNodeAndTheAgentsHealth(t *testing.T) {
	f := newAgentFix(t)
	if r := f.run(t, "rebuild-agent", "--yes", "--from-kit"); r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	opt := f.eng.opt
	if got := opt.KitRoots(); len(got) != 1 || got[0] != f.env.Paths.InternalRoot {
		t.Errorf("kit roots = %v", got)
	}
	if opt.OptRoot != f.opt || opt.Arch != "amd64" || opt.CompileUser != build.CompileAsCaller || opt.Audit == nil || opt.Health == nil {
		t.Errorf("options = %+v", opt)
	}
	// There is no agent behind the socket of this test: the health read fails and does not panic.
	if _, err := opt.Health(context.Background()); err == nil {
		t.Error("a health read of a missing agent must fail")
	}
	if f.made != 1 {
		t.Errorf("engines made = %d", f.made)
	}
}

func TestRebuildAgentConfirmation(t *testing.T) {
	t.Run("a person who says no changes nothing", func(t *testing.T) {
		f := newAgentFix(t)
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "n")), "rebuild-agent")
		if r.code != ExitRefused || !strings.Contains(r.out, "Nothing was changed.") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s\n%s builds=%d", r.code, r.out, r.errOut, len(f.eng.reqs))
		}
		if !strings.Contains(unwrap(r.errOut), "Build the agent 0.4.0 from the kit and switch to it? The agent restarts; this takes a few minutes.") {
			t.Errorf("prompt = %q", r.errOut)
		}
	})
	t.Run("a person who says yes builds", func(t *testing.T) {
		f := newAgentFix(t)
		if r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-agent"); r.code != 0 || len(f.eng.reqs) != 1 {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("no terminal and no --yes asks nothing and builds nothing", func(t *testing.T) {
		f := newAgentFix(t)
		r := f.run(t, "rebuild-agent")
		if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeNeedConfirm+"]") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
	t.Run("a failed check is told before anyone is asked", func(t *testing.T) {
		f := newAgentFix(t)
		f.eng.report.Checks[2] = build.Check{ID: "vendor", Title: "The source is complete and vendored", Code: doctor.KIT030, Detail: "missing: vendor/github.com/example/dep"}
		r := runCLI(t, DefaultRegistry, localOpts(fake.Typed(true, "y")), "rebuild-agent")
		if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.KIT030+"]") || strings.Contains(r.errOut, "Build the agent") || len(f.eng.reqs) != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
	})
}

func TestRebuildAgentNotRestartedIsAWarning(t *testing.T) {
	f := newAgentFix(t)
	f.eng.res = build.AgentResult{Tag: "node-v0.4.0", Version: "0.4.0", Switched: true, RestartNote: "the agent was not running"}
	r := f.run(t, "rebuild-agent", "--yes")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Built aiagent v0.4.0 from the ark kit (no internet used) and installed it.") ||
		!strings.Contains(unwrap(r.errOut), "! The agent was not running or could not be restarted from here. Start it with `sudo systemctl restart localai-agent`.") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func TestRebuildAgentJSONIsTheResultDocument(t *testing.T) {
	f := newAgentFix(t)
	r := f.run(t, "--json", "rebuild-agent", "--yes")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		Tag       string `json:"tag"`
		Version   string `json:"version"`
		Switched  bool   `json:"switched"`
		Restarted bool   `json:"restarted"`
		Previous  string `json:"previous"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || doc.Tag != "node-v0.4.0" || doc.Version != "0.4.0" || !doc.Switched || !doc.Restarted || doc.Previous != "0.3.0" {
		t.Fatalf("%v: %q", err, r.out)
	}
}

// ---- --dry-run ------------------------------------------------------------------------------------

func TestRebuildAgentDryRunPrintsEveryCheck(t *testing.T) {
	f := newAgentFix(t)
	r := f.run(t, "rebuild-agent", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-agent.dry-run.en", r)
	es := f.run(t, "--lang", "es", "rebuild-agent", "--dry-run")
	checkGolden(t, "rebuild-agent.dry-run.es", es)
	if len(f.eng.reqs) != 0 || len(f.eng.checks) != 2 || !f.eng.checks[0].DryRun {
		t.Errorf("a dry run only checks: builds %d, checks %+v", len(f.eng.reqs), f.eng.checks)
	}
}

func TestRebuildAgentDryRunWithMissingPartsExitsNonZero(t *testing.T) {
	f := newAgentFix(t)
	f.eng.report.Checks[1] = build.Check{ID: "toolchain", Title: "The Go toolchain in the kit", Code: doctor.KIT040, Detail: "no go<version>.linux-amd64.tar.gz on any root"}
	f.eng.report.Checks[2] = build.Check{ID: "vendor", Title: "The source is complete and vendored", Code: doctor.KIT030, Detail: "missing: vendor/github.com/example/dep"}
	r := f.run(t, "rebuild-agent", "--dry-run")
	if r.code != ExitFailed {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "rebuild-agent.dry-run.fail.en", r)
	js := f.run(t, "--json", "rebuild-agent", "--dry-run")
	var doc struct{ Error struct{ Code string } }
	if js.code != ExitFailed || json.Unmarshal([]byte(js.out), &doc) != nil || doc.Error.Code == "" {
		t.Fatalf("exit %d out %q err %q", js.code, js.out, js.errOut)
	}
}

func TestRebuildAgentDryRunJSONIsTheReport(t *testing.T) {
	f := newAgentFix(t)
	r := f.run(t, "--json", "rebuild-agent", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		Tag    string `json:"tag"`
		Checks []struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || doc.Tag != "node-v0.4.0" || len(doc.Checks) != 6 || !doc.Checks[0].OK {
		t.Fatalf("%v: %q", err, r.out)
	}
}

// ---- errors -------------------------------------------------------------------------------------------

func TestRebuildAgentErrorsKeepTheirCodeAndWords(t *testing.T) {
	tests := []struct {
		name    string
		err     *build.Error
		wantExp int
		want    []string
	}{
		{"the compile failed", &build.Error{Code: doctor.KIT020, Stage: build.StageBuild, Message: "Building the agent failed: go exited with status 1.",
			Details: map[string]any{"cause": "exit", "log_path": "/var/lib/localai/build/agent-build.log"}}, ExitFailed,
			[]string{"[LAI-KIT-020]", "Building the agent failed: go exited with status 1.", "The log is in /var/lib/localai/build/agent-build.log.", "run `sudo aictl rebuild-agent --dry-run`"}},
		{"it was rolled back", &build.Error{Code: doctor.KIT021, Stage: build.StageCheck, Message: "The new agent did not become healthy, so the previous agent was put back.",
			Details: map[string]any{"cause": "health", "rolled_back": true, "previous_version": "0.3.0"}}, ExitFailed,
			[]string{"[LAI-KIT-021]", "so the previous agent was put back.", "The previous agent is running again; nothing is lost."}},
		{"it did not work and nothing was replaced", &build.Error{Code: doctor.KIT021, Stage: build.StageSmoke, Message: "The new agent did not pass its start-up test (it reports version \"0.0.1\", not \"0.4.0\"), so nothing was replaced.",
			Details: map[string]any{"cause": "smoke"}}, ExitFailed, []string{"[LAI-KIT-021]", "so nothing was replaced."}},
		{"not vendored", &build.Error{Code: doctor.KIT030, Stage: build.StageBuild, Message: "The agent source in the kit is not fully vendored, so it cannot be built without internet.",
			Details: map[string]any{"cause": "not_vendored"}}, ExitFailed, []string{"[LAI-KIT-030]", "not fully vendored", "Sync the kit with internet."}},
		{"the kit lacks a part", &build.Error{Code: doctor.KIT040, Stage: build.StagePreflight, Message: "The Go toolchain in the kit: no go<version>.linux-amd64.tar.gz on any root",
			Details: map[string]any{"cause": "check_toolchain"}}, ExitNotFound, []string{"[LAI-KIT-040]", "The Go toolchain in the kit"}},
		{"a file of the kit is damaged", &build.Error{Code: doctor.KIT001, Stage: build.StageSource, Message: "The kit's file for the agent source changed while it was read. Run `aictl kit verify --full`.",
			Details: map[string]any{"cause": "digest"}}, ExitFailed, []string{"[LAI-KIT-001]", "aictl kit verify --full"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFix(t)
			f.eng.err = tc.err
			r := f.run(t, "rebuild-agent", "--yes")
			if r.code != tc.wantExp {
				t.Fatalf("exit %d, want %d\n%s", r.code, tc.wantExp, r.errOut)
			}
			for _, w := range tc.want {
				if !strings.Contains(unwrap(r.errOut), w) {
					t.Errorf("stderr lacks %q:\n%s", w, r.errOut)
				}
			}
			js := f.run(t, "--json", "rebuild-agent", "--yes")
			var doc struct {
				Error struct {
					Code    string
					Details map[string]any
				}
			}
			if json.Unmarshal([]byte(js.out), &doc) != nil || doc.Error.Code != tc.err.Code || doc.Error.Details["cause"] != tc.err.Details["cause"] {
				t.Errorf("--json: %q", js.out)
			}
			if es := f.run(t, "--lang", "es", "rebuild-agent", "--yes"); !strings.Contains(es.errOut, "["+tc.err.Code+"]") || strings.Contains(es.errOut, tc.err.Message) {
				t.Errorf("a Spanish run shows the code's Spanish title, not the English sentence:\n%s", es.errOut)
			}
		})
	}
}

func TestRebuildAgentChecksFailingAtTheEngine(t *testing.T) {
	f := newAgentFix(t)
	f.eng.checkErr = context.Canceled
	r := f.run(t, "rebuild-agent", "--yes")
	if r.code == 0 || len(f.eng.reqs) != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildAgentWithNoKit(t *testing.T) {
	f := newAgentFix(t)
	if err := os.RemoveAll(f.kitDir); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, "rebuild-agent", "--yes")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "["+CodeKitMissing+"]") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestRebuildAgentNeedsRootAndTakesNoArguments(t *testing.T) {
	f := newAgentFix(t)
	if r := f.runAs(t, 1000, "rebuild-agent", "--yes"); r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-006]") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if r := f.run(t, "rebuild-agent", "now"); r.code != ExitUsage || !strings.Contains(r.errOut, "Unexpected argument") || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if r := f.run(t, "rebuild-agent", "--no-such-flag"); r.code != ExitUsage || f.made != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}
