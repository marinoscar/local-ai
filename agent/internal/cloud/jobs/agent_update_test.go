package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// *cloud.Claimer is the settler of the executor: SettleResult and SettleFailure of AGENT-18.
var _ UpdateSettler = (*cloud.Claimer)(nil)

// updClock never sleeps: Sleep advances the time it reports.
type updClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  int
	onSleep func(n int)
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
	c.now = c.now.Add(d)
	c.sleeps++
	n, hook := c.sleeps, c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return nil
}

// settleCall is what the fake settler saw.
type settleCall struct {
	entry  cloud.InflightEntry
	result any
	code   string
	msg    string
}

type fakeSettler struct {
	mu    sync.Mutex
	calls []settleCall
	// errs are the answers of the first calls; later calls succeed, unless always is set.
	errs   []error
	always error
}

func (s *fakeSettler) next() error {
	if len(s.errs) == 0 {
		return s.always
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

func (s *fakeSettler) SettleResult(_ context.Context, e cloud.InflightEntry, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, settleCall{entry: e, result: result})
	return s.next()
}

func (s *fakeSettler) SettleFailure(_ context.Context, e cloud.InflightEntry, code, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, settleCall{entry: e, code: code, msg: msg})
	return s.next()
}

func (s *fakeSettler) list() []settleCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]settleCall(nil), s.calls...)
}

// fakeUpdRunner is the UpdateRunner of the unit tests.
type fakeUpdRunner struct {
	mu          sync.Mutex
	updateRes   update.Result
	updateErr   error
	gotReq      update.Request
	updateCalls int
	startRes    update.StartupResult
	startErr    error
	done        chan struct{}
	last        *state.UpdateLast
}

func newFakeUpdRunner() *fakeUpdRunner { return &fakeUpdRunner{done: make(chan struct{})} }

func (f *fakeUpdRunner) Update(_ context.Context, req update.Request) (update.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	f.gotReq = req
	return f.updateRes, f.updateErr
}

func (f *fakeUpdRunner) Startup(context.Context) (update.StartupResult, error) {
	f.decide(f.startRes)
	return f.startRes, f.startErr
}

func (f *fakeUpdRunner) decide(res update.StartupResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startRes = res
	select {
	case <-f.done:
	default:
		close(f.done)
	}
}

func (f *fakeUpdRunner) StartupDone() <-chan struct{} { return f.done }

func (f *fakeUpdRunner) LastStartup() update.StartupResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startRes
}

func (f *fakeUpdRunner) LastResult() *state.UpdateLast {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

var updStart = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// updEntry is an inflight entry of an agent.update command whose lease ends 5 minutes after updStart.
func updEntry(id, token string) cloud.InflightEntry {
	return cloud.InflightEntry{
		JobID: id, Type: cloud.TypePrefix + cloud.TypeAgentUpdate, Plane: cloud.PlaneLocalai, ClaimToken: token,
		LeaseExpiresAt: updStart.Add(5 * time.Minute), StartedAt: updStart,
	}
}

type updRig struct {
	t       *testing.T
	store   cloud.InflightStore
	clock   *updClock
	settler *fakeSettler
}

func newUpdRig(t *testing.T, entries ...cloud.InflightEntry) *updRig {
	t.Helper()
	r := &updRig{
		t:       t,
		store:   cloud.InflightStore{Path: filepath.Join(t.TempDir(), "inflight.json")},
		clock:   &updClock{now: updStart},
		settler: &fakeSettler{},
	}
	if len(entries) > 0 {
		if err := r.store.Save(entries); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *updRig) exec(run UpdateRunner) *AgentUpdate {
	a := &AgentUpdate{Runner: run, Inflight: r.store, Clock: r.clock, Logger: discardLogger()}
	a.Bind(r.settler)
	return a
}

func updJob(id string) cloud.Job {
	j := job(id)
	j.Type = cloud.TypePrefix + cloud.TypeAgentUpdate
	return j
}

func TestAgentUpdateStartsTheUpdateWithTheClaimToken(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "claim-token-1"))
	run := newFakeUpdRunner()
	run.updateRes = update.Result{Status: update.StatusRestarting, From: "0.1.0", To: "0.2.0"}
	a := r.exec(run)

	out, err := a.Update(context.Background(), updJob("job-1"), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Defer || out.Result != nil {
		t.Fatalf("an installed update defers: %+v", out)
	}
	req := run.gotReq
	if req.Version != "0.2.0" || req.Job == nil || req.Job.JobID != "job-1" || req.Job.ClaimToken != "claim-token-1" || req.Actor.JobID != "job-1" {
		t.Fatalf("request %+v", req)
	}
	if len(r.settler.list()) != 0 {
		t.Fatal("nothing is settled before the restart")
	}
}

func TestAgentUpdateAlreadyCurrentSettlesAtOnce(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "tok"))
	run := newFakeUpdRunner()
	run.updateRes = update.Result{Status: update.StatusAlreadyCurrent, From: "0.2.0", To: "0.2.0", Reason: update.ReasonSameVersion}
	out, err := r.exec(run).Update(context.Background(), updJob("job-1"), "0.2.0")
	if err != nil || out.Defer {
		t.Fatalf("%+v %v", out, err)
	}
	b, _ := json.Marshal(out.Result)
	if string(b) != `{"from_version":"0.2.0","to_version":"0.2.0","status":"already_current"}` {
		t.Fatalf("result %s", b)
	}
}

func TestAgentUpdateErrorsKeepTheirCode(t *testing.T) {
	for code, mk := range map[string]error{
		doctor.AGT023: &update.Error{Code: doctor.AGT023, Message: "Not online."},
		doctor.AGT010: &update.Error{Code: doctor.AGT010, Message: "Bad signature."},
		doctor.AGT009: &update.Error{Code: doctor.AGT009, Message: "Smoke test failed."},
		doctor.AGT099: errors.New("something with /secret/path in it"),
	} {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		run := newFakeUpdRunner()
		run.updateErr = mk
		_, err := r.exec(run).Update(context.Background(), updJob("job-1"), "0.2.0")
		wantCode(t, err, code)
		if _, msg := cloud.CodeAndMessage(err); strings.Contains(msg, "/secret/path") {
			t.Errorf("%s: the message leaks the cause: %q", code, msg)
		}
	}
}

func TestAgentUpdateNeedsItsInflightEntry(t *testing.T) {
	r := newUpdRig(t) // no entry written
	run := newFakeUpdRunner()
	_, err := r.exec(run).Update(context.Background(), updJob("job-1"), "0.2.0")
	wantCode(t, err, doctor.AGT099)
	if run.updateCalls != 0 {
		t.Fatal("without the entry nothing may be touched: the new process could not settle the command")
	}
}

func TestPostStartSettlesTheCommandAfterTheHealthCheck(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "claim-token-1"))
	run := newFakeUpdRunner()
	run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1", ClaimToken: "claim-token-1"}}
	a := r.exec(run)

	if err := a.PostStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The claim loop hands the same entry to Recover: the command is still settled once.
	if !a.Recover(context.Background(), updEntry("job-1", "claim-token-1")) {
		t.Fatal("agent.update entries are taken over")
	}
	a.Wait()

	calls := r.settler.list()
	if len(calls) != 1 {
		t.Fatalf("settled %d times", len(calls))
	}
	c := calls[0]
	b, _ := json.Marshal(c.result)
	if c.entry.JobID != "job-1" || c.entry.ClaimToken != "claim-token-1" || string(b) != `{"from_version":"0.1.0","to_version":"0.2.0","status":"updated"}` {
		t.Fatalf("call %+v %s", c, b)
	}
}

func TestPostStartRolledBackSettlesAFailure(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "tok"))
	run := newFakeUpdRunner()
	run.startRes = update.StartupResult{Kind: update.StartupRolledBack, From: "0.1.0", To: "0.2.0", Reason: update.ReasonHealth, Job: &update.JobRef{JobID: "job-1", ClaimToken: "tok"}}
	a := r.exec(run)
	if err := a.PostStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	calls := r.settler.list()
	if len(calls) != 1 || calls[0].result != nil || calls[0].code != doctor.AGT009 {
		t.Fatalf("calls %+v", calls)
	}
	msg := calls[0].msg
	if !strings.Contains(msg, `{"from_version":"0.1.0","to_version":"0.2.0","status":"rolled_back"}`) || !strings.Contains(msg, "0.1.0 was restored") {
		t.Fatalf("message %q", msg)
	}
}

func TestPostStartLeavesTheSettleToTheRestoredAgent(t *testing.T) {
	for _, kind := range []update.StartupKind{update.StartupRollingBack, update.StartupInterrupted} {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		run := newFakeUpdRunner()
		run.startRes = update.StartupResult{Kind: kind, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1", ClaimToken: "tok"}}
		a := r.exec(run)
		_ = a.PostStart(context.Background())
		a.Wait()
		if n := len(r.settler.list()); n != 0 {
			t.Fatalf("%s: a process that is being replaced must not settle (%d calls)", kind, n)
		}
	}
}

func TestPostStartWithoutAJobSettlesNothing(t *testing.T) {
	r := newUpdRig(t, updEntry("job-9", "tok"))
	run := newFakeUpdRunner()
	run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0"} // started by `aictl update`
	a := r.exec(run)
	if err := a.PostStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if len(r.settler.list()) != 0 {
		t.Fatal("an update started from the CLI has no command to settle")
	}
}

func TestSettleWaitsForTheLinkAndGivesUpAfterTheLease(t *testing.T) {
	t.Run("retries while the link is down", func(t *testing.T) {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		r.settler.errs = []error{cloud.ErrNotLinked, cloud.ErrNotLinked, cloud.ErrNotLinked}
		run := newFakeUpdRunner()
		run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1"}}
		a := r.exec(run)
		_ = a.PostStart(context.Background())
		a.Wait()
		if n := len(r.settler.list()); n != 4 {
			t.Fatalf("%d attempts, want 3 refused and one accepted", n)
		}
		if r.clock.sleeps != 3 || r.clock.Now().Sub(updStart) != 3*defaultSettleRetry {
			t.Fatalf("waited %d times, %s", r.clock.sleeps, r.clock.Now().Sub(updStart))
		}
	})
	t.Run("gives up once the lease is long over, leaving the entry", func(t *testing.T) {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		r.settler.always = cloud.ErrNotLinked
		run := newFakeUpdRunner()
		run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1"}}
		a := r.exec(run)
		_ = a.PostStart(context.Background())
		a.Wait()
		// Lease 5 min + 1 min slack at 15 s a try: tries at 0, 15, ... 375 s, then it stops.
		if n := len(r.settler.list()); n != 26 {
			t.Fatalf("%d attempts", n)
		}
		if entries, _ := r.store.Load(); len(entries) != 1 {
			t.Fatal("the entry stays for the next start")
		}
	})
	t.Run("a refusal other than not linked ends the attempts", func(t *testing.T) {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		r.settler.errs = []error{errors.New("disk full")}
		run := newFakeUpdRunner()
		run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1"}}
		a := r.exec(run)
		_ = a.PostStart(context.Background())
		a.Wait()
		if n := len(r.settler.list()); n != 1 {
			t.Fatalf("%d attempts", n)
		}
	})
	t.Run("a settler that is bound late", func(t *testing.T) {
		r := newUpdRig(t, updEntry("job-1", "tok"))
		run := newFakeUpdRunner()
		run.startRes = update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1"}}
		a := &AgentUpdate{Runner: run, Inflight: r.store, Clock: r.clock, Logger: discardLogger()}
		r.clock.onSleep = func(n int) {
			if n == 2 {
				a.Bind(r.settler)
			}
		}
		_ = a.PostStart(context.Background())
		a.Wait()
		if n := len(r.settler.list()); n != 1 {
			t.Fatalf("%d settles", n)
		}
	})
}

func TestRecoverTakesOnlyAgentUpdateEntries(t *testing.T) {
	r := newUpdRig(t)
	a := r.exec(newFakeUpdRunner())
	other := updEntry("job-1", "tok")
	other.Type = cloud.TypePrefix + cloud.TypeModelLoad
	if a.Recover(context.Background(), other) {
		t.Fatal("other types are left to the claim loop (LAI-AGT-017)")
	}
}

// The claim loop starts before the health check has finished: Recover waits for the decision.
func TestRecoverWaitsForTheHealthCheck(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "tok"))
	run := newFakeUpdRunner()
	a := r.exec(run)
	r.clock.onSleep = func(n int) {
		if n == 5 {
			run.decide(update.StartupResult{Kind: update.StartupUpdated, From: "0.1.0", To: "0.2.0", Job: &update.JobRef{JobID: "job-1"}})
		}
	}
	if !a.Recover(context.Background(), updEntry("job-1", "tok")) {
		t.Fatal("not taken")
	}
	a.Wait()
	calls := r.settler.list()
	if len(calls) != 1 || calls[0].result == nil {
		t.Fatalf("calls %+v", calls)
	}
	if r.clock.sleeps != 5 {
		t.Fatalf("waited %d times on the clock", r.clock.sleeps)
	}
}

func TestRecoverWithoutAnyDecisionFailsAsInterrupted(t *testing.T) {
	r := newUpdRig(t, updEntry("job-1", "tok"))
	a := r.exec(newFakeUpdRunner()) // Startup never runs: nothing wires it
	a.AwaitStartup = 10 * time.Second
	a.Recover(context.Background(), updEntry("job-1", "tok"))
	a.Wait()
	calls := r.settler.list()
	if len(calls) != 1 || calls[0].code != doctor.AGT017 {
		t.Fatalf("calls %+v", calls)
	}
	if r.clock.Now().Sub(updStart) > 12*time.Second {
		t.Fatalf("waited %s, more than the limit", r.clock.Now().Sub(updStart))
	}
}

// A crash between recording the outcome and settling: the next start finds no marker, only state.update.last.
func TestRecoverUsesTheRecordedOutcomeAfterACrash(t *testing.T) {
	cases := []struct {
		name   string
		last   *state.UpdateLast
		result bool
		code   string
	}{
		{"updated", &state.UpdateLast{Result: state.UpdateOK, From: "0.1.0", To: "0.2.0", At: updStart.Add(time.Minute)}, true, ""},
		{"rolled back", &state.UpdateLast{Result: state.UpdateRolledBack, From: "0.1.0", To: "0.2.0", At: updStart.Add(time.Minute)}, false, doctor.AGT009},
		{"failed", &state.UpdateLast{Result: state.UpdateFailed, From: "0.1.0", To: "0.2.0", At: updStart.Add(time.Minute)}, false, doctor.AGT009},
		{"an older update is not this one", &state.UpdateLast{Result: state.UpdateOK, From: "0.0.9", To: "0.1.0", At: updStart.Add(-time.Hour)}, false, doctor.AGT017},
		{"no record", nil, false, doctor.AGT017},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newUpdRig(t, updEntry("job-1", "tok"))
			run := newFakeUpdRunner()
			run.startRes = update.StartupResult{Kind: update.StartupNone}
			run.last = c.last
			run.decide(run.startRes)
			a := r.exec(run)
			a.Recover(context.Background(), updEntry("job-1", "tok"))
			a.Wait()
			calls := r.settler.list()
			if len(calls) != 1 || (calls[0].result != nil) != c.result || calls[0].code != c.code {
				t.Fatalf("calls %+v", calls)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------
// Integration scenario 5 (agent.md section 9.2): the real Runner against a fake GitHub signed with a test key.

type scenarioExec struct {
	mu       sync.Mutex
	calls    []string
	restarts int
}

// Run answers systemctl and systemd-run and runs a staged "binary" by reading its first line.
func (e *scenarioExec) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	switch filepath.Base(c.Path) {
	case "systemctl":
		if len(c.Args) == 3 && c.Args[1] == "restart" {
			e.restarts++
		}
		return execx.Result{}, nil
	case "systemd-run":
		return execx.Result{}, nil
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return execx.Result{}, err
	}
	var v, arch string
	if n, _ := fmt.Sscanf(strings.SplitN(string(data), "\n", 2)[0], "FAKE %s %s", &v, &arch); n != 2 {
		return execx.Result{ExitCode: 1}, nil
	}
	out, _ := json.Marshal(map[string]string{"version": v, "commit": "test", "os": "linux", "arch": arch, "built_at": "unknown"})
	return execx.Result{Stdout: out}, nil
}

type scenario struct {
	t       *testing.T
	root    string
	paths   update.Paths
	signer  *updatetest.Signer
	srv     *updatetest.Server
	exec    *scenarioExec
	clock   *updClock
	store   cloud.InflightStore
	settler *fakeSettler
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	root := t.TempDir()
	s := &scenario{
		t: t, root: root,
		paths:   update.Paths{UpdateDir: filepath.Join(root, "update"), BinDir: filepath.Join(root, "bin")},
		signer:  updatetest.NewSigner(),
		exec:    &scenarioExec{},
		clock:   &updClock{now: updStart},
		store:   cloud.InflightStore{Path: filepath.Join(root, "inflight.json"), FS: config.OSFS{}},
		settler: &fakeSettler{},
	}
	for _, d := range []string{s.paths.UpdateDir, s.paths.BinDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.writeAgent("0.1.0")
	s.srv = updatetest.NewServer("owner-a/repo-a")
	t.Cleanup(s.srv.Close)
	return s
}

func fakeBin(version string) []byte { return []byte("FAKE " + version + " amd64\n") }

func (s *scenario) writeAgent(version string) {
	if err := os.WriteFile(s.paths.Agent(), fakeBin(version), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scenario) agent() string {
	b, err := os.ReadFile(s.paths.Agent())
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func (s *scenario) release(version string, bin []byte) {
	s.srv.Add(updatetest.NewRelease(s.signer, version, map[string][]byte{
		update.BinaryName("amd64"): bin, update.BinaryName("arm64"): bin,
	}))
}

// process is one run of the agent: its Runner, its executor, and the same disk.
func (s *scenario) process(current string, probe func(context.Context) error) (*update.Runner, *AgentUpdate) {
	pub, err := update.ParsePublicKey(s.signer.PublicKeyFile())
	if err != nil {
		s.t.Fatal(err)
	}
	run := &update.Runner{
		Paths: s.paths, Current: current, Arch: "amd64", Keys: []update.PublicKey{pub},
		Source: update.Source{
			Repo: "owner-a/repo-a", Client: s.srv.Client(), APIBase: s.srv.URL, DownloadBase: s.srv.URL,
			AllowHost: func(h string) bool { return h == "127.0.0.1" },
		},
		Exec: s.exec, Clock: s.clock, Mode: func() string { return "online" },
		State:     &memState{},
		Systemctl: "/usr/bin/systemctl", SystemdRun: "/usr/bin/systemd-run",
		Probe: probe, Log: discardLogger(),
	}
	a := &AgentUpdate{Runner: run, Inflight: s.store, Clock: s.clock, Logger: discardLogger()}
	a.Bind(s.settler)
	return run, a
}

// memState is an in-memory state store shared by the processes of one scenario through the same pointer.
type memState struct {
	mu sync.Mutex
	st state.State
}

func (m *memState) Snapshot() state.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *memState) MutateSync(fn func(*state.State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := m.st
	if err := fn(&cp); err != nil {
		return err
	}
	m.st = cp
	return nil
}

func (s *scenario) startJob(a *AgentUpdate, id, token, version string) (cloud.Outcome, error) {
	if err := s.store.Save([]cloud.InflightEntry{updEntry(id, token)}); err != nil {
		s.t.Fatal(err)
	}
	return a.Update(context.Background(), updJob(id), version)
}

func TestScenarioSelfUpdateFromTheCloud(t *testing.T) {
	s := newScenario(t)
	s.release("0.2.0", fakeBin("0.2.0"))

	// The old process runs the job: steps 1 to 8, then it defers.
	_, oldProc := s.process("0.1.0", nil)
	out, err := s.startJob(oldProc, "job-1", "claim-secret-1", "0.2.0")
	if err != nil || !out.Defer {
		t.Fatalf("%+v %v", out, err)
	}
	if s.agent() != "FAKE 0.2.0 amd64" || s.exec.restarts != 1 {
		t.Fatalf("agent %q, restarts %d", s.agent(), s.exec.restarts)
	}
	if len(s.settler.list()) != 0 {
		t.Fatal("nothing is settled before the new process has checked its health")
	}
	entries, _ := s.store.Load()
	if len(entries) != 1 || entries[0].ClaimToken != "claim-secret-1" {
		t.Fatalf("the inflight entry must stay: %+v", entries)
	}

	// The restart is simulated: the new process starts, the claim loop offers it the entry.
	_, newProc := s.process("0.2.0", func(context.Context) error { return nil })
	if err := newProc.PostStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !newProc.Recover(context.Background(), entries[0]) {
		t.Fatal("not taken over")
	}
	newProc.Wait()

	calls := s.settler.list()
	if len(calls) != 1 || calls[0].entry.ClaimToken != "claim-secret-1" || calls[0].entry.JobID != "job-1" {
		t.Fatalf("calls %+v", calls)
	}
	b, _ := json.Marshal(calls[0].result)
	if string(b) != `{"from_version":"0.1.0","to_version":"0.2.0","status":"updated"}` {
		t.Fatalf("result %s", b)
	}
	if _, err := os.Stat(s.paths.Pending()); !os.IsNotExist(err) {
		t.Fatal("the marker must be gone")
	}
	if prev, _ := os.ReadFile(s.paths.Prev()); strings.TrimSpace(string(prev)) != "FAKE 0.1.0 amd64" {
		t.Fatal("aiagent.prev is kept")
	}
}

func TestScenarioBrokenReleaseIsRolledBack(t *testing.T) {
	s := newScenario(t)
	s.release("0.3.0", fakeBin("0.3.0")) // passes the smoke test, fails its health check

	_, oldProc := s.process("0.1.0", nil)
	if out, err := s.startJob(oldProc, "job-2", "claim-secret-2", "0.3.0"); err != nil || !out.Defer {
		t.Fatalf("%+v %v", out, err)
	}
	entries, _ := s.store.Load()

	// Restart: the new binary never becomes healthy.
	_, badProc := s.process("0.3.0", func(context.Context) error { return errors.New("the llama unit changed state") })
	if err := badProc.PostStart(context.Background()); err != nil {
		t.Fatalf("a rollback that went through is not an error: %v", err)
	}
	badProc.Recover(context.Background(), entries[0])
	badProc.Wait()
	if len(s.settler.list()) != 0 {
		t.Fatal("the failing process must not settle: the restored agent does")
	}
	if s.agent() != "FAKE 0.1.0 amd64" || s.exec.restarts != 2 {
		t.Fatalf("agent %q, restarts %d", s.agent(), s.exec.restarts)
	}

	// Restart again: the old binary reports the rollback and settles the command as a failure.
	_, restored := s.process("0.1.0", nil)
	if err := restored.PostStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored.Recover(context.Background(), entries[0])
	restored.Wait()
	calls := s.settler.list()
	if len(calls) != 1 || calls[0].code != doctor.AGT009 || calls[0].entry.ClaimToken != "claim-secret-2" {
		t.Fatalf("calls %+v", calls)
	}
	if !strings.Contains(calls[0].msg, `"status":"rolled_back"`) || !strings.Contains(calls[0].msg, `"to_version":"0.3.0"`) {
		t.Fatalf("message %q", calls[0].msg)
	}
}

func TestScenarioBrokenBinaryNeverReplacesTheAgent(t *testing.T) {
	s := newScenario(t)
	s.release("0.2.0", []byte("BROKEN\n"))
	_, oldProc := s.process("0.1.0", nil)
	out, err := s.startJob(oldProc, "job-3", "claim-secret-3", "0.2.0")
	wantCode(t, err, doctor.AGT009)
	if out.Defer {
		t.Fatal("a failed update does not defer: the claim loop settles the failure")
	}
	if s.agent() != "FAKE 0.1.0 amd64" || s.exec.restarts != 0 {
		t.Fatalf("agent %q, restarts %d", s.agent(), s.exec.restarts)
	}
	if _, err := os.Stat(s.paths.Pending()); !os.IsNotExist(err) {
		t.Fatal("no marker for an update that did not start")
	}
}

func TestScenarioOfflineJobFailsWithAGT023(t *testing.T) {
	s := newScenario(t)
	s.release("0.2.0", fakeBin("0.2.0"))
	_, oldProc := s.process("0.1.0", nil)
	oldProc.Runner.(*update.Runner).Mode = func() string { return "survival" }
	_, err := s.startJob(oldProc, "job-4", "tok", "0.2.0")
	wantCode(t, err, doctor.AGT023)
	if len(s.srv.Requests()) != 0 {
		t.Fatal("no request may leave a node that is not online")
	}
}

func TestScenarioNewestAlreadyInstalled(t *testing.T) {
	s := newScenario(t)
	s.release("0.1.0", fakeBin("0.1.0"))
	_, oldProc := s.process("0.1.0", nil)
	out, err := s.startJob(oldProc, "job-5", "tok", "0.1.0")
	if err != nil || out.Defer {
		t.Fatalf("%+v %v", out, err)
	}
	b, _ := json.Marshal(out.Result)
	if string(b) != `{"from_version":"0.1.0","to_version":"0.1.0","status":"already_current"}` {
		t.Fatalf("result %s", b)
	}
}

// The claim loop offers the entry before the health check has finished: the executor waits for the decision.
func TestScenarioRecoverBeforeTheHealthCheck(t *testing.T) {
	s := newScenario(t)
	s.release("0.2.0", fakeBin("0.2.0"))
	_, oldProc := s.process("0.1.0", nil)
	if _, err := s.startJob(oldProc, "job-6", "claim-secret-6", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.store.Load()

	_, newProc := s.process("0.2.0", func(context.Context) error { return nil })
	// The health check runs when the executor first waits on the clock: Recover came first.
	s.clock.onSleep = func(n int) {
		if n == 1 {
			if err := newProc.PostStart(context.Background()); err != nil {
				t.Error(err)
			}
		}
	}
	newProc.Recover(context.Background(), entries[0])
	newProc.Wait()
	calls := s.settler.list()
	if len(calls) != 1 || calls[0].result == nil || calls[0].entry.ClaimToken != "claim-secret-6" {
		t.Fatalf("calls %+v", calls)
	}
}
