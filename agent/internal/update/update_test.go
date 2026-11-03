package update

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

var job = &JobRef{JobID: "job-aaaa-1111", ClaimToken: "claim-bbbb-2222"}

func TestUpdateSuccessArmsTheGuardBeforeTheRestart(t *testing.T) {
	skipWithoutUnixModes(t)
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")

	res, err := run.Update(context.Background(), Request{Version: "0.2.0", Job: job, Actor: audit.Cloud(job.JobID), OpID: "op_test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusRestarting || res.From != "0.1.0" || res.To != "0.2.0" {
		t.Fatalf("result %+v", res)
	}

	// The new binary is installed, executable, and the old one is kept as aiagent.prev.
	if got := r.agentContent(); got != string(fakeBinary("0.2.0", testArch)) {
		t.Fatalf("aiagent holds %q", got)
	}
	prev, err := os.ReadFile(r.paths.Prev())
	if err != nil || string(prev) != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("aiagent.prev holds %q, %v", prev, err)
	}
	for _, p := range []string{r.paths.Agent(), r.paths.Prev()} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode %v", p, fi.Mode())
		}
	}
	for _, leftover := range []string{r.paths.New(), r.paths.Prev() + ".tmp"} {
		if r.exists(leftover) {
			t.Fatalf("%s was left behind", leftover)
		}
	}
	// The staging folder is gone once its content is installed.
	if r.exists(filepath.Join(r.paths.UpdateDir, "0.2.0")) {
		t.Fatal("the staging folder was not removed")
	}

	// pending.json: root 0600, deadline now+120s, no boot attempt yet, the job and its token.
	p := r.pending()
	if p == nil || p.From != "0.1.0" || p.To != "0.2.0" || p.BootAttempts != 0 || p.RolledBack ||
		!p.StartedAt.Equal(r.start) || !p.Deadline.Equal(r.start.Add(120*time.Second)) || p.Job == nil || *p.Job != *job {
		t.Fatalf("pending %+v", p)
	}
	if fi, _ := os.Stat(r.paths.Pending()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending.json mode %v", fi.Mode())
	}

	// Order: stale timer stopped, then the timer armed with the exact argv, then the restart, and nothing else:
	// llama is never touched.
	want := []string{
		"systemctl stop localai-agent-rollback.timer",
		"systemctl stop localai-agent-rollback.service",
		fmt.Sprintf(armLine, r.paths.Prev()),
		restartLine,
	}
	got := r.exec.argv()
	// The smoke test of the staged binary comes first; it is the only other call.
	if len(got) != len(want)+1 || !strings.HasSuffix(got[0], "version --json") {
		t.Fatalf("calls %q", got)
	}
	for i, w := range want {
		if got[i+1] != w {
			t.Fatalf("call %d = %q, want %q", i+1, got[i+1], w)
		}
	}
	for _, l := range got {
		if strings.Contains(l, "llama") {
			t.Fatalf("the update touched llama: %q", l)
		}
	}
	// The smoke test ran with a 5 s limit on the staged file.
	if c := r.exec.calls[0]; c.Timeout != 5*time.Second || !strings.HasPrefix(c.Path, filepath.Join(r.paths.UpdateDir, "0.2.0")) {
		t.Fatalf("smoke cmd %+v", c)
	}

	if a := r.audit.actions(); len(a) != 1 || a[0] != "agent.update.install:ok:" || r.audit.evs[0].Actor.JobID != job.JobID {
		t.Fatalf("audit %v", a)
	}
	wantEvents := []string{"update.state:checking", "update.state:downloading", "update.state:verifying", "update.state:installing", "update.state:restarting"}
	if got := r.events.list(); strings.Join(got, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("events %v", got)
	}
}

func TestUpdateNewestWithoutAVersion(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	r.release("0.3.0")
	res, err := r.runner("0.1.0").Update(context.Background(), Request{})
	if err != nil || res.To != "0.3.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := r.agentContent(); got != string(fakeBinary("0.3.0", testArch)) {
		t.Fatalf("aiagent holds %q", got)
	}
}

// Every failure leaves the node exactly as it was.
func TestUpdateFailsClosed(t *testing.T) {
	skipWithoutUnixModes(t)
	other := updatetest.NewSigner()
	cases := []struct {
		name     string
		code     string
		step     string
		setup    func(r *rig, rel *updatetest.Release, run *Runner)
		discards bool // the staging folder is deleted (verification failures)
	}{
		{"signature from an unknown key", doctor.AGT010, "signature", func(r *rig, rel *updatetest.Release, run *Runner) {
			rel.Assets[SigName] = other.Sign(rel.Assets[SumsName], updatetest.SignOptions{})
		}, true},
		{"no trusted key is embedded", doctor.AGT010, "signature", func(r *rig, rel *updatetest.Release, run *Runner) {
			run.Keys = []PublicKey{}
		}, true},
		{"SHA256SUMS edited after signing", doctor.AGT010, "signature", func(r *rig, rel *updatetest.Release, run *Runner) {
			rel.Assets[SumsName] = append([]byte(nil), rel.Assets[SumsName]...)
			rel.Assets[SumsName][0] ^= 1
		}, true},
		{"signature file is garbage", doctor.AGT010, "signature", func(r *rig, rel *updatetest.Release, run *Runner) {
			rel.Assets[SigName] = []byte("garbage")
		}, true},
		{"binary tampered after signing", doctor.AGT010, "checksum", func(r *rig, rel *updatetest.Release, run *Runner) {
			rel.Assets[BinaryName(testArch)] = fakeBinary("0.2.0-evil", testArch)
		}, true},
		{"binary not listed in SHA256SUMS", doctor.AGT010, "checksum", func(r *rig, rel *updatetest.Release, run *Runner) {
			sums := updatetest.Sums(map[string][]byte{BinaryName("arm64"): fakeBinary("0.2.0", "arm64")})
			rel.Assets[SumsName] = sums
			rel.Assets[SigName] = r.signer.Sign(sums, updatetest.SignOptions{})
		}, true},
		{"binary asset missing", doctor.AGT010, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			delete(rel.Assets, BinaryName(testArch))
		}, false},
		{"signature asset missing", doctor.AGT010, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			delete(rel.Assets, SigName)
		}, false},
		{"SHA256SUMS asset missing", doctor.AGT010, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			delete(rel.Assets, SumsName)
		}, false},
		{"asset listed but not served", doctor.AGT010, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
				if strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
					http.NotFound(w, req)
					return true
				}
				return false
			}
		}, false},
		{"broken binary: smoke test fails", doctor.AGT009, "smoke", func(r *rig, rel *updatetest.Release, run *Runner) {
			bin := []byte("BROKEN\n")
			rel.Assets[BinaryName(testArch)] = bin
			sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): bin})
			rel.Assets[SumsName] = sums
			rel.Assets[SigName] = r.signer.Sign(sums, updatetest.SignOptions{})
		}, true},
		{"binary reports another version", doctor.AGT009, "smoke", func(r *rig, rel *updatetest.Release, run *Runner) {
			bin := fakeBinary("0.2.1", testArch)
			rel.Assets[BinaryName(testArch)] = bin
			sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): bin})
			rel.Assets[SumsName] = sums
			rel.Assets[SigName] = r.signer.Sign(sums, updatetest.SignOptions{})
		}, true},
		{"binary reports another architecture", doctor.AGT009, "smoke", func(r *rig, rel *updatetest.Release, run *Runner) {
			bin := fakeBinary("0.2.0", "arm64")
			rel.Assets[BinaryName(testArch)] = bin
			sums := updatetest.Sums(map[string][]byte{BinaryName(testArch): bin, BinaryName("arm64"): bin})
			rel.Assets[SumsName] = sums
			rel.Assets[SigName] = r.signer.Sign(sums, updatetest.SignOptions{})
		}, true},
		{"truncated download", doctor.AGT009, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
				if !strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
					return false
				}
				// Promise 1000 bytes, send 5 and hang up.
				w.Header().Set("Content-Length", "1000")
				_, _ = w.Write([]byte("short"))
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, _ := hj.Hijack()
					_ = conn.Close()
				}
				return true
			}
		}, false},
		{"binary larger than the cap", doctor.AGT009, "download", func(r *rig, rel *updatetest.Release, run *Runner) {
			r.srv.Hook = func(w http.ResponseWriter, req *http.Request) bool {
				if !strings.HasSuffix(req.URL.Path, "/"+BinaryName(testArch)) {
					return false
				}
				w.Header().Set("Content-Length", fmt.Sprint(MaxBinaryBytes+1))
				w.WriteHeader(http.StatusOK)
				return true
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, "0.1.0")
			rel := r.release("0.2.0")
			run := r.runner("0.1.0")
			c.setup(r, rel, run)

			_, err := run.Update(context.Background(), Request{Version: "0.2.0", Job: job})
			e := errCode(t, err, c.code)
			if e.Step != c.step {
				t.Fatalf("step %q, want %q (%v)", e.Step, c.step, err)
			}
			if e.Message == "" || strings.Contains(e.Message, r.root) {
				t.Fatalf("the message must be plain and name no path: %q", e.Message)
			}
			// Nothing was replaced, armed or restarted.
			if got := r.agentContent(); got != string(fakeBinary("0.1.0", testArch)) {
				t.Fatalf("aiagent changed: %q", got)
			}
			for _, p := range []string{r.paths.Prev(), r.paths.New(), r.paths.Pending()} {
				if r.exists(p) {
					t.Fatalf("%s must not exist after a failed update", p)
				}
			}
			if n := r.exec.count("systemd-run") + r.exec.count("systemctl"); n != 0 {
				t.Fatalf("systemd was used %d times: %v", n, r.exec.argv())
			}
			if c.discards && r.exists(filepath.Join(r.paths.UpdateDir, "0.2.0")) {
				t.Fatal("the staging folder of a failed verification must be deleted")
			}
			// A refused update is audited, announced and recorded as failed.
			if a := r.audit.actions(); len(a) != 1 || a[0] != "agent.update.fail:fail:"+c.code {
				t.Fatalf("audit %v", a)
			}
			if ev := r.events.list(); ev[len(ev)-1] != "update.state:failed" {
				t.Fatalf("events %v", ev)
			}
			if last := r.state.Snapshot().Update.Last; last == nil || last.Result != state.UpdateFailed || last.To != "0.2.0" {
				t.Fatalf("state.update.last = %+v", last)
			}
		})
	}
}

func TestUpdateVersionRules(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		want      string
		allowDown bool
		status    string
		reason    string
		to        string
	}{
		{"newer", "0.1.0", "0.2.0", false, StatusRestarting, "", "0.2.0"},
		{"same version", "0.2.0", "0.2.0", false, StatusAlreadyCurrent, ReasonSameVersion, "0.2.0"},
		{"same version even when downgrades are allowed", "0.2.0", "0.2.0", true, StatusAlreadyCurrent, ReasonSameVersion, "0.2.0"},
		{"downgrade refused", "0.3.0", "0.2.0", false, StatusAlreadyCurrent, ReasonDowngradeRefused, "0.3.0"},
		{"downgrade allowed", "0.3.0", "0.2.0", true, StatusRestarting, "", "0.2.0"},
		{"release is above its own pre-release", "0.2.0-rc.1", "0.2.0", false, StatusRestarting, "", "0.2.0"},
		{"pre-release below the release is a downgrade", "0.2.0", "0.2.0-rc.1", false, StatusAlreadyCurrent, ReasonDowngradeRefused, "0.2.0"},
		{"a development build is older than any release", "0.0.0-dev", "0.1.0", false, StatusRestarting, "", "0.1.0"},
		{"a stamp that is not a version is older than any release", "unknown", "0.1.0", false, StatusRestarting, "", "0.1.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.current)
			rel := r.release(c.want)
			rel.Prerelease = strings.Contains(c.want, "-")
			run := r.runner(c.current)
			run.AllowDowngrade = c.allowDown
			res, err := run.Update(context.Background(), Request{Version: c.want})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != c.status || res.Reason != c.reason || res.To != c.to || res.From != c.current {
				t.Fatalf("result %+v", res)
			}
			if c.status == StatusAlreadyCurrent {
				// Nothing was downloaded and nothing was touched.
				if r.downloads() != 0 || len(r.exec.argv()) != 0 || r.exists(r.paths.Pending()) || r.exists(r.paths.Prev()) {
					t.Fatal("an update that does not go ahead must change nothing")
				}
				if r.agentContent() != string(fakeBinary(c.current, testArch)) {
					t.Fatal("aiagent changed")
				}
			}
		})
	}
}

func TestCheck(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	res, err := run.Check(context.Background(), "")
	if err != nil || res.Status != StatusAvailable || res.To != "0.2.0" || res.From != "0.1.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if r.downloads() != 0 || len(r.exec.argv()) != 0 {
		t.Fatal("a check must download and run nothing")
	}
	run.Current = "0.2.0"
	if res, err = run.Check(context.Background(), ""); err != nil || res.Status != StatusAlreadyCurrent {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUpdateNeedsOnlineMode(t *testing.T) {
	for _, mode := range []string{"offline", "survival", ""} {
		t.Run("mode "+mode, func(t *testing.T) {
			r := newRig(t, "0.1.0")
			r.release("0.2.0")
			r.mode = mode
			run := r.runner("0.1.0")
			_, err := run.Update(context.Background(), Request{Version: "0.2.0"})
			errCode(t, err, doctor.AGT023)
			if _, err := run.Check(context.Background(), ""); CodeOf(err) != doctor.AGT023 {
				t.Fatalf("a check is refused too: %v", err)
			}
			if r.requests() != 0 || len(r.exec.argv()) != 0 || r.exists(r.paths.Pending()) {
				t.Fatal("a refused update must not touch the network, systemd or the disk")
			}
			// A refusal is not a failed update.
			if r.state.Snapshot().Update.Last != nil {
				t.Fatal("a refusal must not be recorded as a failed update")
			}
		})
	}
}

func TestUpdateRefusesWhileAnotherIsPending(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	if _, err := run.Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	r.exec.reset()
	r.release("0.3.0")
	_, err := run.Update(context.Background(), Request{Version: "0.3.0"})
	errCode(t, err, doctor.AGT022)
	if len(r.exec.argv()) != 0 {
		t.Fatal("nothing may run while an update is pending")
	}

	// A marker nobody acted on long after its deadline does not block updates for ever.
	r.clock.Advance(120*time.Second + staleMarkerAfter + time.Second)
	run.Current = "0.2.0"
	if _, err := run.Update(context.Background(), Request{Version: "0.3.0"}); err != nil {
		t.Fatalf("a stale marker must be cleared: %v", err)
	}
	if p := r.pending(); p == nil || p.To != "0.3.0" {
		t.Fatalf("pending %+v", p)
	}
}

func TestUpdateCorruptMarkerIsCleared(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	if err := os.WriteFile(r.paths.Pending(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatalf("a corrupt marker must not block an update: %v", err)
	}
}

// The guard is armed before the restart. When it cannot be armed the update is not applied at all.
func TestUpdateArmFailureRestoresThePreviousAgent(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	r.exec.armExit = 1
	_, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0", Job: job})
	errCode(t, err, doctor.AGT009)
	if got := r.agentContent(); got != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("the previous agent must be back, aiagent holds %q", got)
	}
	if r.exists(r.paths.Pending()) {
		t.Fatal("the marker must be removed")
	}
	if r.exec.count("systemctl --no-block restart") != 0 {
		t.Fatal("the agent must not be restarted without its guard")
	}
	if strings.Contains(err.Error(), "already exists") == false {
		t.Fatalf("the cause is kept for the log: %v", err)
	}
}

func TestUpdateRestartRefusedUndoesTheInstall(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	r.exec.onRestart = func() error { return fmt.Errorf("systemd said no") }
	_, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT009)
	if got := r.agentContent(); got != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("aiagent holds %q", got)
	}
	if r.exists(r.paths.Pending()) {
		t.Fatal("the marker must be removed")
	}
	// The timer that was armed is stopped again.
	stops := r.exec.count("systemctl stop localai-agent-rollback.timer")
	if stops != 2 {
		t.Fatalf("the timer must be stopped before arming and after the undo, saw %d: %v", stops, r.exec.argv())
	}
}

func TestUpdateInstallFailureKeepsTheCurrentAgent(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	fs := &failFS{OSFS: OSFS{}, failRenameTo: r.paths.Agent()}
	run.FS = fs
	_, err := run.Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT009)
	if got := r.agentContent(); got != string(fakeBinary("0.1.0", testArch)) {
		t.Fatalf("aiagent holds %q", got)
	}
	if r.exists(r.paths.New()) {
		t.Fatal("the half-installed aiagent.new must be removed")
	}
	if r.exists(r.paths.Pending()) || r.exec.count("systemd") != 0 {
		t.Fatal("nothing may be armed after a failed install")
	}
}

func TestUpdateWithoutAnInstalledAgent(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	_ = os.Remove(r.paths.Agent())
	_, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT009)
}

func TestUpdateUnsupportedArch(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	run.Arch = "riscv64"
	errCode(t, func() error { _, err := run.Update(context.Background(), Request{Version: "0.2.0"}); return err }(), doctor.AGT009)
	if r.downloads() != 0 {
		t.Fatal("nothing may be downloaded for an architecture without a release")
	}
}

// Old staging folders of failed runs do not pile up.
func TestUpdateCleansOldStagingFolders(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	stale := filepath.Join(r.paths.UpdateDir, "0.1.5")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.runner("0.1.0").Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if r.exists(stale) {
		t.Fatal("a stale staging folder must be removed")
	}
}

// The directory of a staging folder is root only.
func TestStagingFolderIsPrivate(t *testing.T) {
	skipWithoutUnixModes(t)
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	// A failing smoke test keeps nothing, so look at the folder while the binary runs.
	var mode os.FileMode
	r.exec.onRestart = nil
	run.Exec = execFunc(func(ctx context.Context, c execCmd) (execResult, error) {
		if fi, err := os.Stat(filepath.Dir(c.Path)); err == nil && strings.HasSuffix(c.Path, BinaryName(testArch)) {
			mode = fi.Mode().Perm()
		}
		return r.exec.Run(ctx, c)
	})
	if _, err := run.Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if mode != 0o700 {
		t.Fatalf("staging folder mode %v, want 0700", mode)
	}
}

// A real process: the staged binary is run for its version, not a fake.
func TestSmokeRunsTheRealStagedBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /bin/sh")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs /bin/sh")
	}
	r := newRig(t, "0.1.0")
	good := updatetest.FakeAgentScript("0.2.0", testArch)
	rel := updatetest.NewRelease(r.signer, "0.2.0", map[string][]byte{BinaryName(testArch): good, BinaryName("arm64"): good})
	r.srv.Add(rel)
	run := r.runner("0.1.0")
	run.Exec = nil // the real execx.Exec; systemctl and systemd-run are replaced below
	run.init()
	real := run.Exec
	run.Exec = execFunc(func(ctx context.Context, c execCmd) (execResult, error) {
		if base := filepath.Base(c.Path); base == "systemctl" || base == "systemd-run" {
			return r.exec.Run(ctx, c)
		}
		return real.Run(ctx, c)
	})
	if _, err := run.Update(context.Background(), Request{Version: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(r.paths.Agent()); string(got) != string(good) {
		t.Fatal("the verified script was not installed")
	}

	// The same script reporting the wrong version is refused by the real smoke test.
	r2 := newRig(t, "0.1.0")
	bad := updatetest.FakeAgentScript("0.2.9", testArch)
	r2.srv.Add(updatetest.NewRelease(r2.signer, "0.2.0", map[string][]byte{BinaryName(testArch): bad, BinaryName("arm64"): bad}))
	run2 := r2.runner("0.1.0")
	run2.Exec = nil
	run2.init()
	_, err := run2.Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT009)
	if r2.agentContent() != string(fakeBinary("0.1.0", testArch)) {
		t.Fatal("aiagent changed")
	}
}

func TestReadSmallFileLimits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte(strings.Repeat("a", 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLimited(OSFS{}, p, 9); err == nil {
		t.Fatal("a file over the limit must be refused")
	}
}

// A caller that cannot say what mode the node is in does not update it.
func TestUpdateWithoutAModeSourceIsRefused(t *testing.T) {
	r := newRig(t, "0.1.0")
	r.release("0.2.0")
	run := r.runner("0.1.0")
	run.Mode = nil
	_, err := run.Update(context.Background(), Request{Version: "0.2.0"})
	errCode(t, err, doctor.AGT099)
	if r.requests() != 0 || r.exists(r.paths.Pending()) {
		t.Fatal("nothing may happen without a mode")
	}
}
