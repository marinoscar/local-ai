package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// installed runs a successful Update on r (the old process), leaving the node exactly as it is the moment the
// restart is asked for: the new binary installed, pending.json written, the timer armed.
func installed(t *testing.T, r *rig, from, to string) {
	t.Helper()
	r.release(to)
	if _, err := r.runner(from).Update(context.Background(), Request{Version: to, Job: job}); err != nil {
		t.Fatal(err)
	}
	r.exec.reset()
	r.audit.evs = nil
	r.events.states = nil
}

func TestStartupWithoutAMarkerDoesNothing(t *testing.T) {
	r := newRig(t, "0.1.0")
	run := r.runner("0.1.0")
	res, err := run.Startup(context.Background())
	if err != nil || res.Kind != StartupNone || res.Job != nil {
		t.Fatalf("%+v %v", res, err)
	}
	select {
	case <-run.StartupDone():
	default:
		t.Fatal("StartupDone must be closed once Startup decided, also when there was nothing to decide")
	}
	if len(r.exec.argv()) != 0 {
		t.Fatal("no marker, no systemctl")
	}
}

// Restart simulated, the new process is healthy: the update is confirmed.
func TestStartupNewAgentHealthyConfirmsTheUpdate(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")

	newProc := r.runner("0.2.0")
	probes := 0
	newProc.Probe = func(context.Context) error { probes++; return nil }
	res, err := newProc.Startup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != StartupUpdated || res.From != "0.1.0" || res.To != "0.2.0" || res.Job == nil || *res.Job != *job {
		t.Fatalf("%+v", res)
	}
	if probes != 1 {
		t.Fatalf("probes: %d", probes)
	}
	if r.exists(r.paths.Pending()) {
		t.Fatal("the marker must be deleted")
	}
	if got := r.exec.argv(); len(got) != 2 || got[0] != "systemctl stop localai-agent-rollback.timer" {
		t.Fatalf("the timer must be stopped, calls: %v", got)
	}
	if last := r.state.Snapshot().Update.Last; last == nil || last.Result != state.UpdateOK || last.From != "0.1.0" || last.To != "0.2.0" || !last.At.Equal(r.start) {
		t.Fatalf("state.update.last = %+v", last)
	}
	if a := r.audit.actions(); len(a) != 1 || a[0] != "agent.update.ok:ok:" {
		t.Fatalf("audit %v", a)
	}
	if ev := r.events.list(); len(ev) != 1 || ev[0] != "update.state:ok" {
		t.Fatalf("events %v", ev)
	}
	if len(r.alerts.codes) != 0 {
		t.Fatalf("a good update raises no alert: %v", r.alerts.codes)
	}
	// aiagent.prev is kept even after a success (ADR-0001).
	if prev, _ := os.ReadFile(r.paths.Prev()); string(prev) != string(fakeBinary("0.1.0", testArch)) {
		t.Fatal("aiagent.prev must be kept")
	}
	// The decision is published for the code that settles the cloud command.
	if got := newProc.LastStartup(); got.Kind != StartupUpdated || got.Job == nil {
		t.Fatalf("LastStartup %+v", got)
	}
	// The aiagent that is installed is the new one and was never touched by the check.
	if r.agentContent() != string(fakeBinary("0.2.0", testArch)) {
		t.Fatal("the new agent must stay installed")
	}
}

// The health check waits for the probe on the injected clock: three failures, then it passes.
func TestStartupWaitsForTheProbeOnTheClock(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")
	run := r.runner("0.2.0")
	n := 0
	run.Probe = func(context.Context) error {
		n++
		if n < 4 {
			return errors.New("listener not up yet")
		}
		return nil
	}
	res, err := run.Startup(context.Background())
	if err != nil || res.Kind != StartupUpdated {
		t.Fatalf("%+v %v", res, err)
	}
	if n != 4 || r.clock.Slept() != 3*2*time.Second {
		t.Fatalf("probes %d, waited %s", n, r.clock.Slept())
	}
}

// The new process never becomes healthy: the 60 s window ends, the previous binary is restored, the marker
// says rolled_back and the agent is restarted. Then the restored agent reports it (LAI-AGT-009).
func TestStartupHealthFailureRollsBackThenTheOldAgentReportsIt(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")

	newProc := r.runner("0.2.0")
	probes := 0
	newProc.Probe = func(context.Context) error { probes++; return errors.New("the router is not reachable") }
	res, err := newProc.Startup(context.Background())
	if err != nil {
		t.Fatalf("a rollback that went through is not an error: %v", err)
	}
	if res.Kind != StartupRollingBack || res.Reason != ReasonHealth || res.Err == nil || res.Job == nil {
		t.Fatalf("%+v", res)
	}
	if slept := r.clock.Slept(); slept < 58*time.Second || slept > HealthWindow {
		t.Fatalf("the check must use the 60 s window on the injected clock, waited %s", slept)
	}
	if probes < 20 {
		t.Fatalf("only %d probes in the window", probes)
	}
	// The previous binary is back, the marker says why, and the restart was asked for once.
	if r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("aiagent holds %q", r.agentContent())
	}
	p := r.pending()
	if p == nil || !p.RolledBack || p.Reason != ReasonHealth || p.BootAttempts != 1 || p.Job == nil {
		t.Fatalf("marker %+v", p)
	}
	if got := r.exec.argv(); len(got) != 1 || got[0] != restartLine {
		t.Fatalf("calls %v", got)
	}
	if prev, _ := os.ReadFile(r.paths.Prev()); string(prev) != string(fakeBinary("0.1.0", testArch)) {
		t.Fatal("aiagent.prev must stay")
	}
	// The failing process does not settle or report: the restored agent does.
	if len(r.alerts.codes) != 0 || r.state.Snapshot().Update.Last != nil {
		t.Fatal("the new process must leave the report to the restored agent")
	}

	// The old binary starts.
	r.exec.reset()
	old := r.runner("0.1.0")
	old.Probe = func(context.Context) error { t.Fatal("the restored agent does not run the health probe"); return nil }
	res, err = old.Startup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != StartupRolledBack || res.Reason != ReasonHealth || res.From != "0.1.0" || res.To != "0.2.0" || res.Job == nil || *res.Job != *job {
		t.Fatalf("%+v", res)
	}
	if r.exists(r.paths.Pending()) {
		t.Fatal("the restored agent deletes the marker")
	}
	if last := r.state.Snapshot().Update.Last; last == nil || last.Result != state.UpdateRolledBack || last.To != "0.2.0" {
		t.Fatalf("state.update.last = %+v", last)
	}
	if len(r.alerts.codes) != 1 || r.alerts.codes[0] != doctor.AGT009 || r.alerts.last["to_version"] != "0.2.0" || r.alerts.last["from_version"] != "0.1.0" {
		t.Fatalf("alerts %v %v", r.alerts.codes, r.alerts.last)
	}
	if a := r.audit.actions(); len(a) != 1 || a[0] != "agent.update.rolled_back:fail:"+doctor.AGT009 {
		t.Fatalf("audit %v", a)
	}
	if ev := r.events.list(); len(ev) != 1 || ev[0] != "update.state:rolled_back" {
		t.Fatalf("events %v", ev)
	}
	if got := r.exec.argv(); len(got) < 1 || got[0] != "systemctl stop localai-agent-rollback.timer" {
		t.Fatalf("the timer must be stopped: %v", got)
	}
	if r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
		t.Fatal("the old agent stays")
	}
}

// Three starts of the new binary without a confirmation: rolled back without another health check.
func TestStartupBootAttemptsRollBack(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")
	run := r.runner("0.2.0")
	run.Probe = func(context.Context) error { t.Fatal("the third start must not probe"); return nil }

	// Two earlier starts crashed before they decided.
	p := r.pending()
	p.BootAttempts = 2
	if err := run.writePending(*p); err != nil {
		t.Fatal(err)
	}
	res, err := run.Startup(context.Background())
	if err != nil || res.Kind != StartupRollingBack || res.Reason != ReasonBootAttempts {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.pending(); got == nil || !got.RolledBack || got.Reason != ReasonBootAttempts || got.BootAttempts != 3 {
		t.Fatalf("marker %+v", got)
	}
	if r.agentContent() != string(fakeBinary("0.1.0", testArch)) || r.exec.count(restartLine) != 1 {
		t.Fatal("the previous agent must be restored and started")
	}
}

func TestStartupCountsBootAttempts(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")
	run := r.runner("0.2.0")
	var seen int
	run.Probe = func(context.Context) error {
		// The attempt is recorded before the probe runs, so a crash inside it still counts.
		seen = r.pending().BootAttempts
		return nil
	}
	if _, err := run.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("boot_attempts during the check = %d, want 1", seen)
	}
}

// A shutdown during the check decides nothing: the marker stays, the timer or the next start decides.
func TestStartupInterruptedKeepsTheMarker(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")
	run := r.runner("0.2.0")
	ctx, cancel := context.WithCancel(context.Background())
	run.Probe = func(context.Context) error { cancel(); return errors.New("shutting down") }
	res, err := run.Startup(ctx)
	if err != nil || res.Kind != StartupInterrupted {
		t.Fatalf("%+v %v", res, err)
	}
	if p := r.pending(); p == nil || p.RolledBack || p.BootAttempts != 1 {
		t.Fatalf("marker %+v", p)
	}
	if r.agentContent() != string(fakeBinary("0.2.0", testArch)) || len(r.exec.argv()) != 0 {
		t.Fatal("an interrupted check must not roll back")
	}
	select {
	case <-run.StartupDone():
		t.Fatal("an interrupted check has not decided")
	default:
	}
}

// The old binary runs while the marker is live and not rolled back (a manual `cp aiagent.prev aiagent`, or an
// update that never took): the update did not take.
func TestStartupOldVersionWithLiveMarkerCountsAsRolledBack(t *testing.T) {
	r := newRig(t, "0.1.0")
	installed(t, r, "0.1.0", "0.2.0")
	// Someone restored the old binary by hand.
	r.writeAgent(fakeBinary("0.1.0", testArch))
	res, err := r.runner("0.1.0").Startup(context.Background())
	if err != nil || res.Kind != StartupRolledBack {
		t.Fatalf("%+v %v", res, err)
	}
	if r.exists(r.paths.Pending()) || len(r.alerts.codes) != 1 {
		t.Fatal("the stale marker must be consumed and reported")
	}
}

func TestStartupCorruptMarkerIsRemoved(t *testing.T) {
	r := newRig(t, "0.1.0")
	if err := os.WriteFile(r.paths.Pending(), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := r.runner("0.2.0").Startup(context.Background())
	if err != nil || res.Kind != StartupNone || r.exists(r.paths.Pending()) {
		t.Fatalf("%+v %v", res, err)
	}
}

// Crash between install and restart: the daemon did not restart (the CLI died, or the restart never came), so
// the old binary still runs and the transient timer is the only thing left. When it fires the previous binary
// is restored and the agent restarted, and the restored agent reports the rollback.
func TestCrashBetweenInstallAndRestartIsRolledBackByTheTimer(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	crashed := make(chan struct{})
	r.exec.onRestart = func() error {
		runtime.Goexit() // the process dies as the restart is asked for
		return nil
	}
	go func() {
		defer close(crashed)
		_, _ = r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0", Job: job})
	}()
	<-crashed

	// What the crash left: the new binary, the marker, the armed timer.
	if r.agentContent() != string(fakeBinary("0.2.0", testArch)) {
		t.Fatal("the new binary is installed")
	}
	if p := r.pending(); p == nil || p.RolledBack || p.To != "0.2.0" {
		t.Fatalf("marker %+v", p)
	}
	if r.exec.count("systemd-run --unit=localai-agent-rollback") != 1 {
		t.Fatalf("the timer must be armed before the restart: %v", r.exec.argv())
	}
	r.exec.onRestart = nil
	r.exec.reset()

	// The timer fires 120 s later and runs aiagent.prev update-rollback-check.
	r.clock.Advance(120 * time.Second)
	timer := r.runner("0.1.0")
	out, err := timer.RollbackCheck(context.Background())
	if err != nil || out != RollbackDone {
		t.Fatalf("%v %v", out, err)
	}
	if r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("aiagent holds %q", r.agentContent())
	}
	if p := r.pending(); p == nil || !p.RolledBack || p.Reason != ReasonTimer || p.Job == nil {
		t.Fatalf("marker %+v", p)
	}
	if got := r.exec.argv(); len(got) != 1 || got[0] != restartLine {
		t.Fatalf("calls %v", got)
	}

	// The restored agent starts, reports LAI-AGT-009 and consumes the marker.
	res, err := r.runner("0.1.0").Startup(context.Background())
	if err != nil || res.Kind != StartupRolledBack || res.Reason != ReasonTimer || res.Job == nil {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.alerts.codes) != 1 || r.alerts.codes[0] != doctor.AGT009 || r.exists(r.paths.Pending()) {
		t.Fatalf("alerts %v", r.alerts.codes)
	}
}

func TestRollbackCheckPaths(t *testing.T) {
	t.Run("update was confirmed: no marker, nothing to do", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		out, err := r.runner("0.1.0").RollbackCheck(context.Background())
		if err != nil || out != RollbackNone || len(r.exec.argv()) != 0 {
			t.Fatalf("%v %v %v", out, err, r.exec.argv())
		}
	})
	t.Run("a rollback is recorded but not reported: restart again", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		p := r.pending()
		p.RolledBack = true
		if err := r.runner("0.1.0").writePending(*p); err != nil {
			t.Fatal(err)
		}
		r.writeAgent(fakeBinary("0.1.0", testArch))
		out, err := r.runner("0.1.0").RollbackCheck(context.Background())
		if err != nil || out != RollbackRetried || r.exec.count(restartLine) != 1 {
			t.Fatalf("%v %v %v", out, err, r.exec.argv())
		}
		if r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
			t.Fatal("the agent binary must not be touched again")
		}
	})
	t.Run("the previous binary is gone: the rollback fails loudly", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		_ = os.Remove(r.paths.Prev())
		_, err := r.runner("0.1.0").RollbackCheck(context.Background())
		errCode(t, err, doctor.AGT009)
		if r.agentContent() != string(fakeBinary("0.2.0", testArch)) {
			t.Fatal("without a previous binary nothing is replaced")
		}
		if p := r.pending(); p == nil || p.RolledBack {
			t.Fatalf("the marker must stay as it was: %+v", p)
		}
	})
	t.Run("a corrupt marker restores the previous binary", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		if err := os.WriteFile(r.paths.Pending(), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := r.runner("0.1.0").RollbackCheck(context.Background())
		if err != nil || out != RollbackDone || r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
			t.Fatalf("%v %v", out, err)
		}
		if r.exists(r.paths.Pending()) {
			t.Fatal("a marker nobody can read is removed")
		}
	})
	t.Run("systemctl refuses the restart", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		r.exec.onRestart = func() error { return fmt.Errorf("no bus") }
		_, err := r.runner("0.1.0").RollbackCheck(context.Background())
		errCode(t, err, doctor.AGT009)
		// The binary is restored and the marker records it, so the next attempt (or the next boot) finishes.
		if r.agentContent() != string(fakeBinary("0.1.0", testArch)) {
			t.Fatal("the binary is restored even when the restart failed")
		}
		if p := r.pending(); p == nil || !p.RolledBack {
			t.Fatalf("marker %+v", p)
		}
	})
}

func TestWaitForUpdate(t *testing.T) {
	healthSeq := func(hs ...Health) func(context.Context) (Health, error) {
		i := 0
		return func(context.Context) (Health, error) {
			if i >= len(hs) {
				return Health{}, errors.New("connection refused")
			}
			h := hs[i]
			i++
			if h.Version == "down" {
				return Health{}, errors.New("connection refused")
			}
			return h, nil
		}
	}
	t.Run("restart then the new health", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		run := r.runner("0.1.0")
		out, err := run.WaitForUpdate(context.Background(), healthSeq(
			Health{Version: "0.1.0"}, // the old process answers until systemd stops it
			Health{Version: "down"}, Health{Version: "down"},
			Health{Version: "0.2.0", Starting: true}, // answering but still starting
			Health{Version: "0.2.0"},
		), "0.1.0", "0.2.0", time.Minute)
		if err != nil || out != WaitUpdated {
			t.Fatalf("%v %v", out, err)
		}
		if r.clock.Slept() != 4*time.Second {
			t.Fatalf("waited %s on the injected clock", r.clock.Slept())
		}
	})
	t.Run("restart then the old version is back: rolled back", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		run := r.runner("0.1.0")
		// The old process still answers while the marker is live, which is not a rollback.
		n := 0
		fetch := func(context.Context) (Health, error) {
			n++
			if n == 3 {
				// The restored agent consumed the marker.
				_ = os.Remove(r.paths.Pending())
			}
			return Health{Version: "0.1.0"}, nil
		}
		out, err := run.WaitForUpdate(context.Background(), fetch, "0.1.0", "0.2.0", time.Minute)
		if err != nil || out != WaitRolledBack || n != 3 {
			t.Fatalf("%v %v after %d reads", out, err, n)
		}
	})
	t.Run("never answers with the new version: error with LAI-AGT-009", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		installed(t, r, "0.1.0", "0.2.0")
		run := r.runner("0.1.0")
		_, err := run.WaitForUpdate(context.Background(), healthSeq(), "0.1.0", "0.2.0", 10*time.Second)
		errCode(t, err, doctor.AGT009)
		if r.clock.Slept() > 10*time.Second {
			t.Fatalf("waited %s past the timeout", r.clock.Slept())
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		r := newRig(t, "0.1.0")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.runner("0.1.0").WaitForUpdate(ctx, healthSeq(), "0.1.0", "0.2.0", time.Minute); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	})
}

func TestFetchHealth(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.srv.Hook = nil
	// The helper reads {"version","starting"} from <base>/api/v1/health.
	hs := newHealthServer(t, `{"ok":true,"version":"0.2.0","mode":"online","starting":false}`)
	h, err := FetchHealth(hs.Client(), hs.URL)(context.Background())
	if err != nil || h.Version != "0.2.0" || h.Starting {
		t.Fatalf("%+v %v", h, err)
	}
	if !strings.HasSuffix(hs.lastPath(), "/api/v1/health") {
		t.Fatalf("path %s", hs.lastPath())
	}
	bad := newHealthServer(t, `not json`)
	if _, err := FetchHealth(bad.Client(), bad.URL)(context.Background()); err == nil {
		t.Fatal("an unreadable answer is an error")
	}
}
