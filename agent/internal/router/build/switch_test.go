package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// faultFS is the real filesystem with a rename or a symlink that can be made to fail.
type faultFS struct {
	OSFS
	symlink func(target, link string) error
	rename  func(oldpath, newpath string) error
}

func (f faultFS) Symlink(target, link string) error {
	if f.symlink != nil {
		if err := f.symlink(target, link); err != nil {
			return err
		}
	}
	return f.OSFS.Symlink(target, link)
}

func (f faultFS) Rename(oldpath, newpath string) error {
	if f.rename != nil {
		if err := f.rename(oldpath, newpath); err != nil {
			return err
		}
	}
	return f.OSFS.Rename(oldpath, newpath)
}

func TestActivateThroughTheAgent(t *testing.T) {
	h := newHarness(t)
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || res.BuildID != testID || res.PreviousBuild != oldID || res.DefaultModel != testModel {
		t.Fatalf("result = %+v", res)
	}
	if h.link() != "builds/"+testID {
		t.Errorf("current = %q", h.link())
	}
	// current was switched before the agent was asked to restart, and only once.
	if len(h.linkAtRestart) != 1 || h.linkAtRestart[0] != "builds/"+testID {
		t.Errorf("the link at restart time: %v", h.linkAtRestart)
	}
	if h.exec.ran("systemctl") {
		t.Error("systemctl was used although the agent is up")
	}
	if len(h.probe.completions) != 1 || h.probe.completions[0] != 16 {
		t.Errorf("completions = %v, want one of 16 tokens", h.probe.completions)
	}
	if len(h.probe.loads) != 0 {
		t.Errorf("the default model was already loaded, yet LoadModel ran: %v", h.probe.loads)
	}
	if len(h.rec.ids) != 0 {
		t.Errorf("the agent records the build itself; the recorder was called with %v", h.rec.ids)
	}
	if len(h.audit.events) != 1 {
		t.Fatalf("audit events: %+v", h.audit.events)
	}
	ev := h.audit.events[0]
	if ev.Action != "llama.build.switch" || ev.Result != audit.ResultOK || ev.Code != "" || ev.Actor.Kind != audit.KindCLIRoot ||
		ev.Target["build"] != testID || ev.Target["previous"] != oldID {
		t.Errorf("audit event %+v", ev)
	}
	if h.exists("current.tmp") {
		t.Error("current.tmp was left behind")
	}
}

func TestActivateWithoutTheAgentUsesSystemctl(t *testing.T) {
	h := newHarness(t, withoutAgent())
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || h.link() != "builds/"+testID {
		t.Fatalf("%+v %q", res, h.link())
	}
	if h.agent.restarts != 0 {
		t.Error("the agent was asked to restart although it is down")
	}
	var found bool
	for _, c := range h.exec.all() {
		if filepath.Base(c.Path) == "systemctl" {
			found = true
			if strings.Join(c.Args, " ") != "restart localai-llama.service" || c.Path != "/usr/bin/systemctl" {
				t.Errorf("systemctl argv: %s %v", c.Path, c.Args)
			}
		}
	}
	if !found {
		t.Fatal("systemctl was not run")
	}
	if fmt.Sprint(h.rec.ids) != "["+testID+"]" {
		t.Errorf("the recorder got %v", h.rec.ids)
	}
}

func TestNoAgentAtAllIsTreatedAsDown(t *testing.T) {
	h := newHarness(t)
	h.opts.Agent = nil
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if !h.exec.ran("systemctl restart localai-llama.service") {
		t.Error("systemctl was not used")
	}
}

func TestAgentThatStopsAnsweringFallsBackToSystemctl(t *testing.T) {
	h := newHarness(t)
	h.agent.errs = []error{fmt.Errorf("dial unix: %w", ErrAgentUnavailable)}
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if !h.exec.ran("systemctl restart localai-llama.service") {
		t.Error("no fallback to systemctl")
	}
}

func TestRecorderErrorIsAWarning(t *testing.T) {
	h := newHarness(t, withoutAgent())
	h.rec.err = errors.New("read-only file system")
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "read-only file system") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

// assertRolledBack checks what every failed activation must leave: current on the previous build, a
// second restart, LAI-LLM-019 and an audit line that says so.
func assertRolledBack(t *testing.T, h *harness, err error, wantCause string) *Error {
	t.Helper()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 {
		t.Fatalf("code = %s, want LAI-LLM-019 (%v)", e.Code, e)
	}
	if e.Details["cause"] != wantCause {
		t.Errorf("cause = %v, want %s", e.Details["cause"], wantCause)
	}
	if e.Details["rolled_back"] != true || e.Details["previous_build"] != oldID {
		t.Errorf("details = %v", e.Details)
	}
	if h.link() != "builds/"+oldID {
		t.Errorf("current = %q, want the previous build", h.link())
	}
	if n := len(h.linkAtRestart); n != 2 || h.linkAtRestart[0] != "builds/"+testID || h.linkAtRestart[1] != "builds/"+oldID {
		t.Errorf("links at the two restarts: %v", h.linkAtRestart)
	}
	if !h.exists("builds/" + testID + "/BUILD.json") {
		t.Error("the rejected build must stay on disk for inspection")
	}
	if len(h.audit.events) != 1 || h.audit.events[0].Result != audit.ResultFail || h.audit.events[0].Code != doctor.LLM019 ||
		h.audit.events[0].Target["rolled_back"] != "true" {
		t.Errorf("audit: %+v", h.audit.events)
	}
	if tail, _ := e.Details["log_tail"].(string); !strings.Contains(tail, "cmake") {
		t.Errorf("the build log tail is not in the details: %q", tail)
	}
	return e
}

func TestRestartFailureSwitchesBack(t *testing.T) {
	h := newHarness(t)
	h.agent.errs = []error{errors.New("500 LAI-LLM-001: the unit did not restart")}
	_, err := h.build()
	assertRolledBack(t, h, err, "restart_failed")
	if h.agent.restarts != 2 {
		t.Errorf("restarts = %d, want 2 (the new build, then the old one)", h.agent.restarts)
	}
	if len(h.probe.completions) != 0 {
		t.Error("the check ran after a failed restart")
	}
}

func TestSystemctlRestartFailureSwitchesBack(t *testing.T) {
	h := newHarness(t, withoutAgent())
	calls := 0
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) != "systemctl" {
			return execx.Result{}, nil, false
		}
		calls++
		if calls == 1 {
			return exitWith(1, "Job for localai-llama.service failed"), nil, true
		}
		return execx.Result{}, nil, true
	})
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 || e.Details["rolled_back"] != true || h.link() != "builds/"+oldID || calls != 2 {
		t.Fatalf("%+v link %q calls %d", e, h.link(), calls)
	}
	if !strings.Contains(e.Details["restart_error"].(string), "Job for localai-llama.service failed") {
		t.Errorf("restart_error = %v", e.Details["restart_error"])
	}
	if len(h.rec.ids) != 0 {
		t.Errorf("a rejected build was recorded: %v", h.rec.ids)
	}
}

func TestPostSwitchCheckFailures(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(h *harness)
		wantCause string
		minSlept  time.Duration
		maxSlept  time.Duration
	}{
		{
			name:      "health never answers within 60 s",
			setup:     func(h *harness) { h.probe.healthFn = func(int) error { return errors.New("connection refused") } },
			wantCause: "health_timeout", minSlept: 60 * time.Second, maxSlept: 62 * time.Second,
		},
		{
			name:      "default model lookup fails",
			setup:     func(h *harness) { h.probe.defErr = errors.New("agent: 500") },
			wantCause: "default_lookup",
		},
		{
			name:      "default model fails to start",
			setup:     func(h *harness) { h.probe.statusFn = func(int) (string, error) { return StatusFailed, nil } },
			wantCause: "model_failed",
		},
		{
			name: "loading the default model fails",
			setup: func(h *harness) {
				h.probe.statusFn = func(int) (string, error) { return "unloaded", nil }
				h.probe.loadErr = errors.New("500 LAI-LLM-009")
			},
			wantCause: "load_failed",
		},
		{
			name:      "default model never loads within 180 s",
			setup:     func(h *harness) { h.probe.statusFn = func(int) (string, error) { return StatusLoading, nil } },
			wantCause: "load_timeout", minSlept: 180 * time.Second, maxSlept: 182 * time.Second,
		},
		{
			name: "the model list never answers within 180 s",
			setup: func(h *harness) {
				h.probe.statusFn = func(int) (string, error) { return "", errors.New("502") }
			},
			wantCause: "load_timeout", minSlept: 180 * time.Second, maxSlept: 182 * time.Second,
		},
		{
			name:      "the 16-token completion fails",
			setup:     func(h *harness) { h.probe.completeErr = errors.New("503 loading model") },
			wantCause: "completion_failed",
		},
		{
			name: "the model loads too late for a completion",
			setup: func(h *harness) {
				// Loaded on the 91st poll, 180 s after the restart: no time is left.
				h.probe.statusFn = func(n int) (string, error) {
					if n < 91 {
						return StatusLoading, nil
					}
					return StatusLoaded, nil
				}
			},
			wantCause: "completion_timeout", minSlept: 180 * time.Second, maxSlept: 182 * time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			_, err := h.build()
			assertRolledBack(t, h, err, tc.wantCause)
			if tc.maxSlept > 0 && (h.clk.slep < tc.minSlept || h.clk.slep > tc.maxSlept) {
				t.Errorf("the check waited %s, want between %s and %s", h.clk.slep, tc.minSlept, tc.maxSlept)
			}
		})
	}
}

func TestHealthComesLateButInTime(t *testing.T) {
	h := newHarness(t)
	h.probe.healthFn = func(n int) error {
		if n < 20 { // 19 failed polls of 2 s: up after 38 s
			return errors.New("starting")
		}
		return nil
	}
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if h.clk.slep != 38*time.Second {
		t.Errorf("waited %s", h.clk.slep)
	}
}

func TestDefaultModelIsLoadedWhenNeeded(t *testing.T) {
	h := newHarness(t)
	h.probe.statusFn = func(n int) (string, error) {
		if n == 1 {
			return "unloaded", nil
		}
		return StatusLoaded, nil
	}
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if fmt.Sprint(h.probe.loads) != "["+testModel+"]" {
		t.Errorf("loads = %v", h.probe.loads)
	}
}

func TestLoadingDefaultModelIsWaitedForNotLoadedTwice(t *testing.T) {
	h := newHarness(t)
	h.probe.statusFn = func(n int) (string, error) {
		if n < 4 {
			return StatusLoading, nil
		}
		return StatusLoaded, nil
	}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if len(h.probe.loads) != 0 {
		t.Errorf("a loading model was loaded again: %v", h.probe.loads)
	}
}

func TestNoAgentToLoadThroughKeepsWaiting(t *testing.T) {
	h := newHarness(t, withoutAgent())
	h.probe.loadErr = ErrNoAgent
	h.probe.statusFn = func(n int) (string, error) {
		if n < 3 {
			return "unloaded", nil
		}
		return StatusLoaded, nil
	}
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if len(h.probe.loads) != 1 {
		t.Errorf("LoadModel is tried once, not on every poll: %v", h.probe.loads)
	}
}

func TestNoDefaultModelOnlyChecksHealth(t *testing.T) {
	h := newHarness(t)
	h.probe.def = ""
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if res.DefaultModel != "" || len(h.probe.completions) != 0 || h.probe.nStatus != 0 {
		t.Errorf("a node with no default model was asked for a model: %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "No default model") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestNoProbeSkipsTheCheckWithAWarning(t *testing.T) {
	h := newHarness(t)
	h.opts.Probe = nil
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "not checked") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestBusyAgentIsRetriedThenTheSwitchIsUndone(t *testing.T) {
	h := newHarness(t)
	busy := &BusyError{OpID: "op_1"}
	for i := 0; i < 40; i++ {
		h.agent.errs = append(h.agent.errs, busy)
	}
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.AGT022 || e.Details["op_id"] != "op_1" || e.Details["rolled_back"] != true {
		t.Fatalf("%+v", e)
	}
	if h.link() != "builds/"+oldID {
		t.Errorf("current = %q, want the previous build", h.link())
	}
	if h.agent.restarts != 13 || h.clk.slep != time.Minute {
		t.Errorf("restarts %d slept %s; want 13 tries over a minute (every 5 s)", h.agent.restarts, h.clk.slep)
	}
	if h.exec.ran("systemctl") {
		t.Error("a busy agent must not be bypassed with systemctl")
	}
	if len(h.audit.events) != 1 || h.audit.events[0].Code != doctor.AGT022 || h.audit.events[0].Result != audit.ResultFail {
		t.Errorf("audit: %+v", h.audit.events)
	}
	if len(h.probe.completions) != 0 {
		t.Error("the check ran for a build that never started")
	}
}

func TestBusyAgentThatFreesUpIsFine(t *testing.T) {
	h := newHarness(t)
	h.agent.errs = []error{&BusyError{}, &BusyError{}}
	res, err := h.build()
	if err != nil || !res.Switched {
		t.Fatalf("%v %+v", err, res)
	}
	if h.agent.restarts != 3 || h.clk.slep != 10*time.Second {
		t.Errorf("restarts %d slept %s", h.agent.restarts, h.clk.slep)
	}
}

func TestFailedCheckWithNoPreviousBuildHasNothingToGoBackTo(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.probe.completeErr = errors.New("boom")
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 || e.Details["rolled_back"] != false || !strings.Contains(e.Message, "no previous build") {
		t.Fatalf("%+v", e)
	}
	if h.agent.restarts != 1 {
		t.Errorf("restarts = %d; with nothing to go back to there is no second restart", h.agent.restarts)
	}
}

func TestRollbackThatCannotRestartIsReported(t *testing.T) {
	h := newHarness(t)
	h.agent.errs = []error{errors.New("first restart fails"), errors.New("second restart fails too")}
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 || e.Details["rolled_back"] != false || !strings.Contains(e.Message, "could not be put back") {
		t.Fatalf("%+v", e)
	}
	if h.link() != "builds/"+oldID {
		t.Errorf("the link is still put back: %q", h.link())
	}
}

func TestSymlinkFailureLeavesCurrentAlone(t *testing.T) {
	h := newHarness(t)
	h.opts.FS = faultFS{symlink: func(target, link string) error {
		if strings.HasSuffix(link, "current.tmp") {
			return errors.New("read-only file system")
		}
		return nil
	}}
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 || e.Details["cause"] != "symlink" {
		t.Fatalf("%+v", e)
	}
	if h.link() != "builds/"+oldID || h.agent.restarts != 0 {
		t.Errorf("link %q restarts %d", h.link(), h.agent.restarts)
	}
}

func TestRenameOfTheLinkFailureLeavesNoTemporaryLink(t *testing.T) {
	h := newHarness(t)
	h.opts.FS = faultFS{rename: func(oldpath, newpath string) error {
		if strings.HasSuffix(oldpath, "current.tmp") {
			return errors.New("busy")
		}
		return nil
	}}
	_, err := h.build()
	if e := mustErr(t, err); e.Details["cause"] != "symlink" {
		t.Fatalf("%+v", e)
	}
	if h.exists("current.tmp") || h.link() != "builds/"+oldID {
		t.Errorf("current.tmp left or current changed: %q", h.link())
	}
}

func TestPlacingTheBuildCanFail(t *testing.T) {
	h := newHarness(t)
	final := filepath.Join(h.root, "builds", testID)
	h.opts.FS = faultFS{rename: func(oldpath, newpath string) error {
		if newpath == final {
			return errors.New("input/output error")
		}
		return nil
	}}
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Stage != StageSwitch {
		t.Fatalf("%+v", e)
	}
	if h.link() != "builds/"+oldID || h.exists("builds/"+testID) || !h.exists("builds/"+testID+".tmp.failed") {
		t.Error("a build that could not be placed must be kept as .tmp.failed and change nothing")
	}
}

func TestCancelledDuringTheCheckSwitchesBack(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.probe.healthFn = func(int) error {
		cancel()
		return errors.New("not yet")
	}
	_, err := h.manager().Build(ctx, h.req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's error", err)
	}
	if h.link() != "builds/"+oldID || h.agent.restarts != 2 {
		t.Errorf("a cancelled check must put the previous build back: link %q restarts %d", h.link(), h.agent.restarts)
	}
}

func TestStoreRecorder(t *testing.T) {
	tests := []struct {
		name      string
		start     state.Llama
		id        string
		wantBuild string
		wantPrev  string
	}{
		{"first build", state.Llama{}, "b-cuda", "b-cuda", ""},
		{"replaces and remembers", state.Llama{Build: "a-cuda"}, "b-cuda", "b-cuda", "a-cuda"},
		{"same id changes nothing", state.Llama{Build: "b-cuda", PreviousBuild: strp("a-cuda")}, "b-cuda", "b-cuda", "a-cuda"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.State{Llama: tc.start}
			err := StoreRecorder{Store: fakeMutator{st}}.RecordBuild(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			prev := ""
			if st.Llama.PreviousBuild != nil {
				prev = *st.Llama.PreviousBuild
			}
			if st.Llama.Build != tc.wantBuild || prev != tc.wantPrev {
				t.Errorf("build %q previous %q", st.Llama.Build, prev)
			}
		})
	}
}

type fakeMutator struct{ s *state.State }

func (m fakeMutator) Mutate(fn func(*state.State) error) error { return fn(m.s) }

func strp(s string) *string { return &s }

func TestBusyErrorText(t *testing.T) {
	if !strings.Contains((&BusyError{OpID: "op_9"}).Error(), "op_9") || strings.Contains((&BusyError{}).Error(), "()") {
		t.Error("BusyError text")
	}
}
