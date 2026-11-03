package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// Tests of `aictl update` (AICTL-09, aictl.md section 4.27). The library (internal/update) is the real one,
// pointed at a fake GitHub over TLS with a test signing key made at run time; no process is started
// (the exec is a fake that answers the staged binary's `version --json`, systemctl and systemd-run), no
// clock sleeps (a manual clock advances when the wait sleeps) and /health of the restarted agent is a
// script. The agent's own half (steps 9 and 10) is internal/update's and the daemon's tests.

const updArch = "amd64"

// updClock never sleeps: Sleep advances the time it reports.
type updClock struct {
	mu    sync.Mutex
	now   time.Time
	slept time.Duration
}

func (c *updClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *updClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.slept += d
	return nil
}

func (c *updClock) Slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

func updBinary(version string) []byte { return []byte("FAKE " + version + " " + updArch + "\n") }

// updExec answers like the machine would: systemctl and systemd-run succeed, any other path is a staged
// agent whose first line says what `version --json` reports.
type updExec struct {
	mu        sync.Mutex
	calls     []string
	onRestart func()
	smokeFail bool
	restart   error
}

func (e *updExec) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	e.mu.Lock()
	e.calls = append(e.calls, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	hook, rerr, fail := e.onRestart, e.restart, e.smokeFail
	e.mu.Unlock()
	switch filepath.Base(c.Path) {
	case "systemctl":
		if len(c.Args) == 3 && c.Args[1] == "restart" {
			if rerr != nil {
				return execx.Result{ExitCode: 1, Stderr: []byte("Failed to restart localai-agent.service")}, nil
			}
			if hook != nil {
				hook()
			}
		}
		return execx.Result{}, nil
	case "systemd-run":
		return execx.Result{}, nil
	}
	if fail {
		return execx.Result{ExitCode: 3, Stderr: []byte("broken")}, nil
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return execx.Result{}, err
	}
	var v, arch string
	if n, _ := fmt.Sscanf(strings.SplitN(string(data), "\n", 2)[0], "FAKE %s %s", &v, &arch); n != 2 {
		return execx.Result{ExitCode: 1, Stderr: []byte("broken")}, nil
	}
	out, _ := json.Marshal(map[string]string{"version": v, "commit": "test", "os": "linux", "arch": arch, "built_at": "unknown"})
	return execx.Result{Stdout: out}, nil
}

func (e *updExec) has(prefix string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, l := range e.calls {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func (e *updExec) argv() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

type updFix struct {
	*localFixture
	host   *hostFix
	signer *updatetest.Signer
	srv    *updatetest.Server
	exec   *updExec
	clock  *updClock
	cur    string

	// restarted is set when the agent was asked to restart; health answers from it.
	restarted bool
	// healthFn, when set, is the script of /health; the default is "the old version until the restart,
	// then the new one after healthyAfter polls".
	healthFn     func(ctx context.Context) (update.Health, error)
	healthPolls  int
	healthyAfter int
	newVersion   string
	probeErr     error
	probed       int
}

func newUpdFix(t *testing.T, cur string) *updFix {
	t.Helper()
	f := &updFix{
		localFixture: newLocalFixture(t), signer: updatetest.NewSigner(), exec: &updExec{}, cur: cur,
		clock: &updClock{now: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)},
	}
	f.host = setKitHost(t, "noble", updArch)
	if err := os.MkdirAll(filepath.Join(f.host.opt, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.agentPath(), updBinary(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	f.srv = updatetest.NewServer("owner-a/repo-a")
	t.Cleanup(f.srv.Close)
	f.setMode(t, "online")
	f.exec.onRestart = func() { f.restarted = true }

	pub, err := update.ParsePublicKey(f.signer.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	oldTweak, oldHealth, oldProbe := updateTweak, updateHealthFn, updateConnProbe
	updateTweak = func(r *update.Runner) {
		r.Current, r.Arch, r.Keys = f.cur, updArch, []update.PublicKey{pub}
		r.Source = update.Source{
			Repo: "owner-a/repo-a", Channel: update.ChannelStable, Client: f.srv.Client(),
			APIBase: f.srv.URL, DownloadBase: f.srv.URL, AllowHost: func(h string) bool { return h == "127.0.0.1" },
		}
		r.Exec, r.Clock = f.exec, f.clock
		r.Systemctl, r.SystemdRun = "/usr/bin/systemctl", "/usr/bin/systemd-run"
	}
	updateHealthFn = func(*Call) func(context.Context) (update.Health, error) {
		return func(ctx context.Context) (update.Health, error) {
			if f.healthFn != nil {
				return f.healthFn(ctx)
			}
			if !f.restarted {
				return update.Health{Version: f.cur}, nil
			}
			f.healthPolls++
			if f.healthPolls <= f.healthyAfter {
				return update.Health{}, errors.New("the agent is restarting")
			}
			return update.Health{Version: f.newVersion}, nil
		}
	}
	updateConnProbe = func(context.Context) error { f.probed++; return f.probeErr }
	t.Cleanup(func() { updateTweak, updateHealthFn, updateConnProbe = oldTweak, oldHealth, oldProbe })
	return f
}

func (f *updFix) agentPath() string { return filepath.Join(f.host.opt, "bin", "aiagent") }
func (f *updFix) prevPath() string  { return filepath.Join(f.host.opt, "bin", "aiagent.prev") }
func (f *updFix) pendingPath() string {
	return filepath.Join(f.env.Paths.StateDir, "update", "pending.json")
}

func (f *updFix) setMode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(f.env.Paths.StateFile(), []byte(`{"mode":"`+mode+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *updFix) release(version string) *updatetest.Release {
	f.newVersion = version
	rel := updatetest.NewRelease(f.signer, version, map[string][]byte{
		update.BinaryName("amd64"): updBinary(version),
		update.BinaryName("arm64"): []byte("FAKE " + version + " arm64\n"),
	})
	f.srv.Add(rel)
	return rel
}

func (f *updFix) installed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.agentPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *updFix) requests() int { return len(f.srv.Requests()) }

func (f *updFix) run(t *testing.T, term Terminal, args ...string) runResult {
	t.Helper()
	return runCLI(t, DefaultRegistry, localOpts(term), args...)
}

func (f *updFix) untouchedInstall(t *testing.T) {
	t.Helper()
	if got := f.installed(t); got != string(updBinary(f.cur)) {
		t.Errorf("the installed agent was replaced: %q", got)
	}
	if _, err := os.Stat(f.prevPath()); err == nil {
		t.Error("aiagent.prev exists: something was installed")
	}
	if _, err := os.Stat(f.pendingPath()); err == nil {
		t.Error("an update was left pending")
	}
	if f.exec.has("systemctl") || f.exec.has("systemd-run") {
		t.Errorf("the agent was touched: %v", f.exec.argv())
	}
}

// ---- the happy path -----------------------------------------------------------------------------

func TestUpdateInstallsAndWaitsForTheNewVersion(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.healthyAfter = 3 // the restarted agent answers a few polls with an error before it is up
	r := f.run(t, fake.Typed(true, "y"), "update")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGoldenFree(t, "update.en", r)
	if got := f.installed(t); got != string(updBinary("0.2.0")) {
		t.Errorf("installed = %q", got)
	}
	if prev, err := os.ReadFile(f.prevPath()); err != nil || string(prev) != string(updBinary("0.1.0")) {
		t.Errorf("aiagent.prev = %q, %v: the old agent is always kept", prev, err)
	}
	if _, err := os.Stat(f.pendingPath()); err != nil {
		t.Errorf("the rollback marker is armed for the new process to settle: %v", err)
	}
	argv := f.exec.argv()
	wantArm := "systemd-run --unit=localai-agent-rollback --on-active=120s --timer-property=AccuracySec=1s " + filepath.Join(f.host.opt, "bin", "aiagent.prev") + " update-rollback-check"
	var armed, restarted bool
	for _, l := range argv {
		armed = armed || l == wantArm
		restarted = restarted || l == "systemctl --no-block restart localai-agent.service"
	}
	if !armed || !restarted {
		t.Errorf("exec = %v\nwant the rollback armed (%q) and a non-blocking restart", argv, wantArm)
	}
	if f.healthPolls < 4 {
		t.Errorf("polls = %d: it must wait until /health reports the new version", f.healthPolls)
	}
	if f.clock.Slept() < 3*time.Second {
		t.Errorf("slept %v on the injected clock", f.clock.Slept())
	}

	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "agent.update.install" || lines[0]["result"] != "ok" {
		t.Fatalf("audit = %v", lines)
	}
	if actor, _ := lines[0]["actor"].(map[string]any); actor["kind"] != "cli-root" {
		t.Errorf("actor = %v", lines[0]["actor"])
	}
}

func TestUpdateInSpanish(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	r := f.run(t, fake.Typed(true, "sí"), "--lang", "es", "update")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGoldenFree(t, "update.es", r)
}

func TestUpdateJSONIsTheOutcome(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	r := f.run(t, nil, "--json", "update", "--yes")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc updateDoc
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil || doc != (updateDoc{Status: "updated", From: "0.1.0", To: "0.2.0"}) {
		t.Fatalf("doc = %+v, %v: %q", doc, err, r.out)
	}
	// Without --yes there is nobody to ask.
	if r := f.run(t, nil, "--json", "update"); r.code != ExitUsage || !strings.Contains(r.out, CodeNeedConfirm) {
		t.Errorf("exit %d\n%s", r.code, r.out)
	}
}

func TestUpdateTheVersionShownIsTheVersionInstalled(t *testing.T) {
	// A newer release that appears between the question and the answer is not installed by surprise.
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	term := fake.Typed(true, "y")
	var added bool
	f.srv.Hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/releases/tags/") && !added {
			added = true
			f.release("0.3.0") // appears between the question and the install
			f.newVersion = "0.2.0"
		}
		return false
	}
	r := f.run(t, term, "update")
	if r.code != 0 || f.installed(t) != string(updBinary("0.2.0")) {
		t.Fatalf("exit %d installed %q\n%s\n%s", r.code, f.installed(t), r.out, r.errOut)
	}
}

func TestUpdateWithAVersionInstallsThatOne(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.release("0.3.0")
	f.newVersion = "0.2.0"
	r := f.run(t, nil, "update", "--version", "0.2.0", "--yes")
	if r.code != 0 || f.installed(t) != string(updBinary("0.2.0")) {
		t.Fatalf("exit %d installed %q\n%s\n%s", r.code, f.installed(t), r.out, r.errOut)
	}
	if !strings.Contains(unwrap(r.out), "Version 0.2.0 is available (you have 0.1.0).") {
		t.Errorf("stdout = %q", r.out)
	}
}

// ---- --check ------------------------------------------------------------------------------------

func TestUpdateCheck(t *testing.T) {
	t.Run("a newer release is available: the line, its code and its fix; nothing is installed", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		term := fake.Typed(true, "y")
		r := f.run(t, term, "update", "--check")
		if r.code != 0 || term.Reads() != 0 {
			t.Fatalf("exit %d reads %d\n%s\n%s", r.code, term.Reads(), r.out, r.errOut)
		}
		checkGolden(t, "update.check.en", r)
		f.untouchedInstall(t)
		if f.downloads() != 0 {
			t.Errorf("--check downloaded %d files", f.downloads())
		}
		es := f.run(t, nil, "--lang", "es", "update", "--check")
		checkGolden(t, "update.check.es", es)
	})
	t.Run("already up to date: LAI-AGT-015 and exit 0", func(t *testing.T) {
		f := newUpdFix(t, "0.2.0")
		f.release("0.2.0")
		r := f.run(t, nil, "update", "--check")
		if r.code != 0 || !strings.Contains(r.out, "["+doctor.AGT015+"]") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		checkGolden(t, "update.uptodate.en", r)
		// Without --check it is the same answer, and nobody is asked anything.
		term := fake.Typed(true, "y")
		if r := f.run(t, term, "update"); r.code != 0 || term.Reads() != 0 || !strings.Contains(unwrap(r.out), "You are up to date") {
			t.Errorf("exit %d reads %d\n%s", r.code, term.Reads(), r.out)
		}
		f.cur = "0.2.0"
		f.untouchedInstall(t)
	})
	t.Run("an older newest release is not installed unless allow_downgrade", func(t *testing.T) {
		f := newUpdFix(t, "0.3.0")
		f.release("0.2.0")
		r := f.run(t, nil, "update", "--check")
		if r.code != 0 || !strings.Contains(unwrap(r.out), "is older than the version you have (0.3.0), so nothing was installed.") ||
			!strings.Contains(unwrap(r.out), "update.allow_downgrade") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		f.cur = "0.3.0"
		f.untouchedInstall(t)
	})
	t.Run("allow_downgrade in node.yaml lets an older release be chosen", func(t *testing.T) {
		f := newUpdFix(t, "0.3.0")
		f.release("0.2.0")
		if err := os.WriteFile(f.env.Paths.NodeYAML(), []byte("schema_version: 1\nupdate:\n  repo: owner-a/repo-a\n  channel: stable\n  allow_downgrade: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := f.run(t, nil, "update", "--check", "--version", "0.2.0")
		if r.code != 0 || !strings.Contains(unwrap(r.out), "Version 0.2.0 is available (you have 0.3.0).") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
	})
	t.Run("--json", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		var doc updateDoc
		r := f.run(t, nil, "--json", "update", "--check")
		if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil || doc != (updateDoc{Status: "available", From: "0.1.0", To: "0.2.0"}) {
			t.Fatalf("exit %d: %q", r.code, r.out)
		}
		f2 := newUpdFix(t, "0.2.0")
		f2.release("0.2.0")
		r = f2.run(t, nil, "--json", "update", "--check")
		if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil || doc != (updateDoc{Status: "already_current", From: "0.2.0", To: "0.2.0", Reason: "same_version"}) {
			t.Fatalf("exit %d: %q", r.code, r.out)
		}
	})
}

func (f *updFix) downloads() int {
	n := 0
	for _, q := range f.srv.Requests() {
		if strings.Contains(q.Path, "/releases/download/") {
			n++
		}
	}
	return n
}

// ---- the mode -----------------------------------------------------------------------------------

func TestUpdateIsRefusedWhenTheNodeIsNotOnline(t *testing.T) {
	for _, mode := range []string{"offline", "survival"} {
		t.Run(mode, func(t *testing.T) {
			f := newUpdFix(t, "0.1.0")
			f.release("0.2.0")
			f.setMode(t, mode)
			r := f.run(t, nil, "update", "--yes")
			if r.code != ExitRefused || !strings.Contains(r.errOut, "["+doctor.AGT023+"]") {
				t.Fatalf("exit %d\n%s", r.code, r.errOut)
			}
			if f.requests() != 0 {
				t.Errorf("an offline node reached the network: %d requests", f.requests())
			}
			f.untouchedInstall(t)
			if mode == "survival" {
				checkGolden(t, "update.offline.en", r)
				es := f.run(t, nil, "--lang", "es", "update", "--yes")
				checkGolden(t, "update.offline.es", es)
			}
			// --check is the same: it needs the internet as well.
			if r := f.run(t, nil, "update", "--check"); r.code != ExitRefused {
				t.Errorf("--check: exit %d\n%s", r.code, r.errOut)
			}
		})
	}
	t.Run("the agent's own answer wins over state.json", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		rig := newMgRig(t, fake.Options{})
		rig.agent().Respond(http.MethodGet, "/mode", 200, `{"mode":"survival"}`)
		got := rig.run(mgArgs("update", "--yes"))
		if got.code != ExitRefused || f.requests() != 0 {
			t.Fatalf("exit %d requests %d\n%s", got.code, f.requests(), got.errOut)
		}
	})
}

func TestUpdateWithNoModeAtAllNeedsGitHubToAnswer(t *testing.T) {
	t.Run("GitHub does not answer: refused like offline", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		_ = os.Remove(f.env.Paths.StateFile())
		f.probeErr = errors.New("no route to host")
		r := f.run(t, nil, "update", "--yes")
		if r.code != ExitRefused || !strings.Contains(r.errOut, "["+doctor.AGT023+"]") || f.probed != 1 || f.requests() != 0 {
			t.Fatalf("exit %d probed %d\n%s", r.code, f.probed, r.errOut)
		}
	})
	t.Run("GitHub answers: it goes ahead", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		_ = os.Remove(f.env.Paths.StateFile())
		r := f.run(t, nil, "update", "--yes")
		if r.code != 0 || f.probed != 1 {
			t.Fatalf("exit %d probed %d\n%s\n%s", r.code, f.probed, r.out, r.errOut)
		}
	})
	t.Run("a mode that is known is never probed", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		if r := f.run(t, nil, "update", "--yes"); r.code != 0 || f.probed != 0 {
			t.Fatalf("exit %d probed %d", r.code, f.probed)
		}
	})
}

// ---- verification: nothing is replaced ----------------------------------------------------------

func TestUpdateWithABadSignatureReplacesNothing(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	other := updatetest.NewSigner() // not the key the agent trusts
	rel := updatetest.NewRelease(other, "0.2.0", map[string][]byte{
		update.BinaryName("amd64"): updBinary("0.2.0"),
		update.BinaryName("arm64"): []byte("FAKE 0.2.0 arm64\n"),
	})
	f.srv.Add(rel)
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT010+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "update.badsig.en", r)
	es := f.run(t, nil, "--lang", "es", "update", "--yes")
	checkGolden(t, "update.badsig.es", es)
	f.untouchedInstall(t)
	if entries, _ := os.ReadDir(filepath.Join(f.env.Paths.StateDir, "update")); len(entries) != 0 {
		t.Errorf("the staging folder was not removed: %v", entries)
	}
	lines := f.auditLines()
	if len(lines) == 0 || lines[0]["action"] != "agent.update.fail" || lines[0]["code"] != doctor.AGT010 {
		t.Errorf("audit = %v", lines)
	}
}

func TestUpdateWithAChangedBinaryReplacesNothing(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	rel := f.release("0.2.0")
	rel.Assets[update.BinaryName("amd64")] = []byte("FAKE 0.2.0 amd64\nand something else\n") // signed sums name another digest
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT010+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	f.untouchedInstall(t)
}

func TestUpdateWithAStagedBinaryThatDoesNotRunReplacesNothing(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.exec.smokeFail = true
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT009+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	f.untouchedInstall(t)
}

func TestUpdateWhoseRestartIsRefusedPutsTheOldAgentBack(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.exec.restart = errors.New("refused")
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT009+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if got := f.installed(t); got != string(updBinary("0.1.0")) {
		t.Errorf("installed = %q: a new binary never runs without its guard", got)
	}
	if _, err := os.Stat(f.pendingPath()); err == nil {
		t.Error("the marker was left behind")
	}
}

// ---- the rollback -------------------------------------------------------------------------------

func TestUpdateWhoseNewAgentFailsItsHealthCheckIsRolledBack(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	// The new process fails its health check and puts the previous binary back: /health answers with
	// the old version again, and the marker is gone.
	f.healthFn = func(context.Context) (update.Health, error) {
		if !f.restarted {
			return update.Health{Version: "0.1.0"}, nil
		}
		_ = os.Remove(f.pendingPath())
		return update.Health{Version: "0.1.0"}, nil
	}
	r := f.run(t, fake.Typed(true, "y"), "update")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT009+"]") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGoldenFree(t, "update.rolledback.en", r)
	if strings.Contains(r.out, "Updated to") {
		t.Error("a rolled back update printed the success line")
	}
}

func TestUpdateRolledBackInSpanishAndJSON(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.healthFn = func(context.Context) (update.Health, error) {
		if f.restarted {
			_ = os.Remove(f.pendingPath())
		}
		return update.Health{Version: "0.1.0"}, nil
	}
	r := f.run(t, nil, "--lang", "es", "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.errOut), "no arrancó bien, así que se volvió automáticamente a la versión anterior 0.1.0") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "update.rolledback.es", r)

	f2 := newUpdFix(t, "0.1.0")
	f2.release("0.2.0")
	f2.healthFn = func(context.Context) (update.Health, error) {
		if f2.restarted {
			_ = os.Remove(f2.pendingPath())
		}
		return update.Health{Version: "0.1.0"}, nil
	}
	j := f2.run(t, nil, "--json", "update", "--yes")
	var doc struct {
		Error struct {
			Code    string
			Details map[string]any
		}
	}
	if j.code != ExitFailed || json.Unmarshal([]byte(j.out), &doc) != nil || doc.Error.Code != doctor.AGT009 ||
		doc.Error.Details["status"] != "rolled_back" || doc.Error.Details["from"] != "0.1.0" || doc.Error.Details["to"] != "0.2.0" {
		t.Fatalf("exit %d: %q", j.code, j.out)
	}
}

func TestUpdateWhoseNewAgentNeverReportsTimesOut(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	f.healthFn = func(context.Context) (update.Health, error) {
		if !f.restarted {
			return update.Health{Version: "0.1.0"}, nil
		}
		return update.Health{}, errors.New("the agent is restarting")
	}
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "["+doctor.AGT009+"]") || !strings.Contains(unwrap(r.errOut), "did not report the new version in time") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if got := f.clock.Slept(); got < 115*time.Second || got > 125*time.Second {
		t.Errorf("waited %v on the injected clock, want about 120 s", got)
	}
	if _, err := os.Stat(f.pendingPath()); err != nil {
		t.Error("the marker (and the timer) must stay: the rollback still protects the node")
	}
}

// ---- Ctrl-C -------------------------------------------------------------------------------------

func TestUpdateCtrlCAfterTheRestartOnlyStopsWaiting(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	rig := newMgRig(t, fake.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The person presses Ctrl-C while the restarted agent has not answered yet.
	f.healthFn = func(context.Context) (update.Health, error) {
		if !f.restarted {
			return update.Health{Version: "0.1.0"}, nil
		}
		cancel()
		return update.Health{}, errors.New("the agent is restarting")
	}
	got := rig.run(mgRun{args: []string{"update", "--yes"}, ctx: ctx})
	if got.code != ExitInterrupted || !strings.Contains(unwrap(got.out), "Stopped waiting. The agent keeps updating") {
		t.Fatalf("exit %d\n%s\n%s", got.code, got.out, got.errOut)
	}
	if f.installed(t) != string(updBinary("0.2.0")) {
		t.Error("the update was undone by a Ctrl-C after the restart")
	}
	if _, err := os.Stat(f.pendingPath()); err != nil {
		t.Errorf("the rollback marker must stay so the timer still protects the node: %v", err)
	}
	if !f.exec.has("systemd-run --unit=localai-agent-rollback") {
		t.Error("the rollback timer was not armed")
	}
}

func TestUpdateCtrlCAfterTheRestartInJSON(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	rig := newMgRig(t, fake.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.healthFn = func(context.Context) (update.Health, error) {
		if f.restarted {
			cancel()
		}
		return update.Health{Version: "0.1.0"}, nil
	}
	got := rig.run(mgRun{args: []string{"--json", "update", "--yes"}, ctx: ctx})
	var doc struct{ Error struct{ Code string } }
	if got.code != ExitInterrupted || json.Unmarshal([]byte(got.out), &doc) != nil || doc.Error.Code != CodeInterrupted {
		t.Fatalf("exit %d: %q", got.code, got.out)
	}
}

func TestUpdateCtrlCBeforeTheRestartChangesNothing(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	rig := newMgRig(t, fake.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.srv.Hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/releases/download/") {
			cancel()             // Ctrl-C during the download
			<-r.Context().Done() // the client gives up; nothing is answered
			return true
		}
		return false
	}
	got := rig.run(mgRun{args: []string{"update", "--yes"}, ctx: ctx})
	if got.code != ExitInterrupted || !strings.Contains(unwrap(got.out), "The update was stopped before the agent was restarted. Nothing was changed.") {
		t.Fatalf("exit %d\n%s\n%s", got.code, got.out, got.errOut)
	}
	f.untouchedInstall(t)
}

// ---- the questions, the flags, root -------------------------------------------------------------

func TestUpdateConfirmation(t *testing.T) {
	t.Run("a person who says no changes nothing", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		r := f.run(t, fake.Typed(true, "n"), "update")
		if r.code != ExitRefused || !strings.Contains(r.out, "Nothing was changed.") || !strings.Contains(unwrap(r.errOut), "Update now? The agent restarts; models keep running. [y/N]") {
			t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
		}
		f.untouchedInstall(t)
		if f.downloads() != 0 {
			t.Error("something was downloaded before the answer")
		}
	})
	t.Run("no terminal and no --yes asks nothing and installs nothing", func(t *testing.T) {
		f := newUpdFix(t, "0.1.0")
		f.release("0.2.0")
		r := f.run(t, nil, "update")
		if r.code != ExitUsage || !strings.Contains(r.errOut, "["+CodeNeedConfirm+"]") {
			t.Fatalf("exit %d\n%s", r.code, r.errOut)
		}
		f.untouchedInstall(t)
	})
}

func TestUpdateUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		in   string
	}{
		{"a version with a v", []string{"--version", "v0.2.0"}, ExitUsage, "is not valid; use a version such as 1.2.3 (no leading v)"},
		{"a version that is not a version", []string{"--version", "latest"}, ExitUsage, "is not valid"},
		{"an extra argument", []string{"now"}, ExitUsage, "Unexpected argument"},
		{"an unknown flag", []string{"--force"}, ExitUsage, "force"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdFix(t, "0.1.0")
			f.release("0.2.0")
			r := f.run(t, nil, append([]string{"update", "--yes"}, tc.args...)...)
			if r.code != tc.code || !strings.Contains(unwrap(r.errOut), tc.in) || f.requests() != 0 {
				t.Fatalf("exit %d requests %d\n%s", r.code, f.requests(), r.errOut)
			}
		})
	}
}

func TestUpdateUnknownVersionIsNotFound(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	r := f.run(t, nil, "update", "--version", "9.9.9", "--yes")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "["+doctor.AGT021+"]") || !strings.Contains(unwrap(r.errOut), "That version has not been released.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	f.untouchedInstall(t)
}

func TestUpdateWithNoReleaseAtAll(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "["+doctor.AGT021+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestUpdateNeedsRoot(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	o := localOpts(nil)
	o.euid = 1000
	for _, args := range [][]string{{"update", "--yes"}, {"update", "--check"}} {
		r := runCLI(t, DefaultRegistry, o, args...)
		if r.code != ExitDenied || !strings.Contains(r.errOut, "["+CodeNeedRoot+"]") || f.requests() != 0 {
			t.Fatalf("%v: exit %d\n%s", args, r.code, r.errOut)
		}
	}
	f.untouchedInstall(t)
}

func TestUpdateRefusedWhileAnotherUpdateIsPending(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	f.release("0.2.0")
	if err := os.MkdirAll(filepath.Dir(f.pendingPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	now := f.clock.Now()
	body, _ := json.Marshal(map[string]any{"from": "0.1.0", "to": "0.2.0", "started_at": now, "deadline": now.Add(time.Minute)})
	if err := os.WriteFile(f.pendingPath(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, nil, "update", "--yes")
	if r.code != ExitRefused || !strings.Contains(r.errOut, "["+doctor.AGT022+"]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if f.exec.has("systemctl") {
		t.Error("the agent was restarted while an update was pending")
	}
}

func TestUpdateSettingsComeFromNodeYAML(t *testing.T) {
	f := newUpdFix(t, "0.1.0")
	if got := readUpdateSettings(f.env); got != (updateSettings{Repo: update.DefaultRepo, Channel: update.ChannelStable}) {
		t.Errorf("defaults = %+v", got)
	}
	if err := os.WriteFile(f.env.Paths.NodeYAML(), []byte("schema_version: 1\nupdate:\n  repo: owner-b/repo-b\n  channel: prerelease\n  allow_downgrade: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readUpdateSettings(f.env); got != (updateSettings{Repo: "owner-b/repo-b", Channel: update.ChannelPrerelease, AllowDowngrade: true}) {
		t.Errorf("settings = %+v", got)
	}
	// The runner is built from them (the test hook replaces only the transport, key and clock).
	var got *update.Runner
	prev := updateTweak
	updateTweak = func(r *update.Runner) { got = r; prev(r) }
	f.release("0.2.0")
	_ = f.run(t, nil, "update", "--check")
	if got == nil || !got.AllowDowngrade || got.Paths.UpdateDir != filepath.Join(f.env.Paths.StateDir, "update") ||
		got.Paths.BinDir != filepath.Join(f.host.opt, "bin") || got.Actor.Kind != "cli-root" || got.State != nil {
		t.Errorf("runner = %+v", got)
	}
}

func TestUpdateCommandIsRegisteredAsLocalRoot(t *testing.T) {
	for _, name := range []string{"update", "rebuild-llama"} {
		cmd, ok := DefaultRegistry.cmds[name]
		if !ok || cmd.Marks&MarkRoot == 0 || cmd.Marks&MarkLocal == 0 {
			t.Errorf("%s is not [local] [root]: %+v", name, cmd)
		}
	}
	if DefaultRegistry.cmds["update"].Timeout != 15*time.Minute || DefaultRegistry.cmds["rebuild-llama"].Timeout != 3*time.Hour {
		t.Error("the timeouts are the ones of aictl.md section 4")
	}
}
