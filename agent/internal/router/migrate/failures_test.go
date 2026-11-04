package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func assertRolledBackError(t *testing.T, err error, want string) {
	t.Helper()
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("err = %v", err)
	}
	if !me.RolledBack || me.Code != doctor.LLM029 {
		t.Fatalf("a failure after the old service stopped must roll back by itself: %+v", me)
	}
	if !strings.Contains(me.Error(), want) && !strings.Contains(fmtDetails(me), want) {
		t.Fatalf("the error does not say %q: %v / %v", want, me, me.Details)
	}
}

func fmtDetails(e *Error) string {
	b, _ := json.Marshal(e.Details)
	return string(b)
}

// assertRolledBack checks that the old service serves again, exactly as before.
func assertRolledBack(t *testing.T, w *world, where string) {
	t.Helper()
	f := func(format string, a ...any) { t.Helper(); t.Fatalf(where+": "+format, a...) }
	ctx := context.Background()
	st, _ := w.sd.State(ctx, LegacyUnitName)
	if st.ActiveState != "active" || !st.Enabled() || st.UnitFileState == "masked" {
		f("the old service: %+v", st)
	}
	if got, ok := w.fs.content("/etc/systemd/system/llama-qwen.service"); !ok || string(got) != w.legacyText {
		f("the old unit file is missing or changed")
	}
	if ns, _ := w.sd.State(ctx, NewUnitName); ns.Active() || ns.Enabled() {
		f("the new service: %+v", ns)
	}
	if fi, err := w.fs.Lstat("/etc/llama-qwen/api-key"); err != nil || fi.Mode()&0o170000 != 0 && !fi.Mode().IsRegular() {
		f("the old key path is not a regular file: %v %v", fi, err)
	}
	cur, _ := w.fs.content("/etc/llama-qwen/api-key")
	nk, _ := w.fs.content("/etc/localai/api-key")
	if string(cur) != string(nk) && len(nk) != 0 {
		f("the old key file does not hold the current key")
	}
	if string(cur) != w.key {
		f("the old key file holds another key")
	}
	if w.portOwner() != "legacy" {
		f("port owner %q", w.portOwner())
	}
	if status, _, _ := w.probe.V1Models(ctx, true); status != 200 {
		f("/v1/models with the key answers %d", status)
	}
	if status, _, _ := w.probe.V1Models(ctx, false); status != 401 {
		f("/v1/models without the key answers %d", status)
	}
	if got, ok := w.fs.content(oldModel); !ok || string(got) != string(w.model) {
		f("the old model path does not resolve to the model")
	}
	if !w.st.has || w.st.entry.State != StateRolledBack {
		f("state.migration = %+v", w.st.entry)
	}
	var pr Progress
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/progress.json")), &pr); err != nil || pr.Rollback != "R6" {
		f("progress.json: %+v %v", pr, err)
	}
	if !w.agent.active || pr.AgentStopped {
		f("the agent was not started again: active=%v flag=%v", w.agent.active, pr.AgentStopped)
	}
	assertNoKey(t, w, where)
}

// assertNoKey scans everything the migration wrote or said for the key.
func assertNoKey(t *testing.T, w *world, where string) {
	t.Helper()
	if strings.Contains(w.out.String(), w.key) || strings.Contains(strings.Join(w.audits, "\n"), w.key) || strings.Contains(strings.Join(w.execCalls, "\n"), w.key) {
		t.Fatalf("%s: the key was printed, audited or passed to a program", where)
	}
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	for p, n := range w.fs.nodes {
		if strings.HasPrefix(p, "/var/lib/localai/migration/") && strings.Contains(string(n.data), w.key) {
			t.Fatalf("%s: %s contains the key", where, p)
		}
	}
}

func lastProgress(t *testing.T, w *world) Progress {
	t.Helper()
	var pr Progress
	if err := json.Unmarshal([]byte(mustRead(t, w, "/var/lib/localai/migration/llama-qwen/progress.json")), &pr); err != nil {
		t.Fatal(err)
	}
	return pr
}

// TestAFailureBeforeTheOldServiceStopsLeavesItServingAndTheRerunCompletes injects a failure at each of
// M1 to M5: the old service keeps serving, the progress stays at the last completed step, and once the
// cause is gone running the migration again finishes it.
func TestAFailureBeforeTheOldServiceStopsLeavesItServingAndTheRerunCompletes(t *testing.T) {
	cases := []struct {
		name   string
		opts   worldOpts
		break_ func(*world)
		fix    func(*world)
		last   string // the last step that completed
	}{
		{"M1 the build fails", worldOpts{}, func(w *world) { w.builder.buildErr = errors.New("compiler crashed") }, func(w *world) { w.builder.buildErr = nil }, M0},
		{"M1 the new build has no GPU", worldOpts{}, func(w *world) { w.noGPU = true; w.cfgNode.cfg.Llama.Backend = "vulkan" }, func(w *world) { w.noGPU = false; w.cfgNode.cfg.Llama.Backend = "cuda" }, M0},
		{"M2 the key cannot be written", worldOpts{hasCurrent: true}, func(w *world) { w.fs.failWrite = "/etc/localai/api-key" }, func(w *world) { w.fs.failWrite = "" }, M1},
		{"M3 the disk is full while copying", worldOpts{hasCurrent: true, crossFS: true}, func(w *world) { w.fs.failWrite = ".gguf.part" }, func(w *world) { w.fs.failWrite = "" }, M2},
		{"M4 node.yaml cannot be written", worldOpts{hasCurrent: true}, func(w *world) { w.cfgNode.err = errors.New("read-only file system") }, func(w *world) { w.cfgNode.err = nil }, M3},
		{"M4 the generator fails", worldOpts{hasCurrent: true}, func(w *world) { w.rend.err = errors.New("LAI-LLM-007") }, func(w *world) { w.rend.err = nil }, M3},
		{"M4 the model has no preset", worldOpts{hasCurrent: true}, func(w *world) { w.rend.omitModel = true }, func(w *world) { w.rend.omitModel = false }, M3},
		{"M4 the preset lacks load-on-startup", worldOpts{hasCurrent: true}, func(w *world) { w.rend.noLoadFlag = true }, func(w *world) { w.rend.noLoadFlag = false }, M3},
		{"M5 localai cannot read a model file", worldOpts{hasCurrent: true}, func(w *world) { w.unreadable[newModel] = true }, func(w *world) { w.unreadable = map[string]bool{} }, M4},
		{"M5 localai cannot run the new llama-server", worldOpts{hasCurrent: true}, func(w *world) { w.newBinaryBroken = true }, func(w *world) { w.newBinaryBroken = false }, M4},
		{"M5 the new llama-server has no router memory margin", worldOpts{hasCurrent: true}, func(w *world) { w.oldRouterFlags = true }, func(w *world) { w.oldRouterFlags = false }, M4},
		{"M5 the port is not the old service's", worldOpts{hasCurrent: true}, func(w *world) { w.ssPID = 4242 }, func(w *world) { w.ssPID = 0 }, M4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, c.opts)
			c.break_(w)
			_, err := w.run(w.engine())
			var me *Error
			if !errors.As(err, &me) || me.RolledBack || me.Code == "" {
				t.Fatalf("err = %v", err)
			}
			assertLegacyServing(t, w, c.name)
			pr := lastProgress(t, w)
			if pr.Step != c.last || pr.T0 != nil || pr.Rollback != "" {
				t.Fatalf("progress %+v, want last step %s", pr, c.last)
			}
			if w.st.entry.State != StateInProgress {
				t.Fatalf("state %+v", w.st.entry)
			}
			// The old model path still resolves to the model, whatever M3 had done.
			if got, ok := w.fs.content(oldModel); !ok || string(got) != string(w.model) {
				t.Fatalf("the old model path does not resolve to the model")
			}
			if !w.agent.active {
				t.Fatal("the agent was not started again after the failure")
			}
			if strings.Contains(w.out.String(), "Rolling back") {
				t.Fatal("a failure before the downtime must not roll back")
			}
			// Fix the cause, run again: it continues and completes.
			c.fix(w)
			if _, err := w.run(w.engine()); err != nil {
				t.Fatalf("re-run: %v\n%s", err, w.out.String())
			}
			assertMigrated(t, w, c.name)
		})
	}
}

// TestAFailureAfterTheOldServiceStopsRollsBackAutomatically injects a failure at M6, M7 and each check
// of M8: the migration rolls back by itself and the old service serves again.
func TestAFailureAfterTheOldServiceStopsRollsBackAutomatically(t *testing.T) {
	cases := []struct {
		name   string
		opts   worldOpts
		break_ func(*world)
		want   string
	}{
		{"M6 the old service will not stop", worldOpts{hasCurrent: true}, func(w *world) { w.sd.fail["stop "+LegacyUnitName] = errors.New("Job for llama-qwen.service canceled") }, "cannot stop"},
		{"M6 something else holds the port", worldOpts{hasCurrent: true}, func(w *world) { w.strangerOnPort = true }, "port 8080 is still held"},
		{"M7 the new service cannot start", worldOpts{hasCurrent: true}, func(w *world) { w.sd.fail["start "+NewUnitName] = errors.New("Job failed") }, "cannot start"},
		{"M7 /health never answers", worldOpts{hasCurrent: true}, func(w *world) { w.newBroken = true }, "/health"},
		{"M7 the model fails to load", worldOpts{hasCurrent: true}, func(w *world) { w.loadFails = true }, "failed to load"},
		{"M7 the model never finishes loading", worldOpts{hasCurrent: true}, func(w *world) { w.loadPolls = 1 << 30 }, "was not loaded within"},
		{"M8a the new service answers without a key", worldOpts{hasCurrent: true}, func(w *world) { w.noAuth = true }, "a) GET /v1/models without a key answered 200"},
		{"M8b the alias is missing", worldOpts{hasCurrent: true}, func(w *world) { w.missingAlias = true }, "does not list the alias legacy-name"},
		{"M8b /v1/models fails with the key", worldOpts{hasCurrent: true}, func(w *world) { w.v1Status = 500 }, "b) GET /v1/models with the key answered 500"},
		{"M8c the legacy name does not answer", worldOpts{hasCurrent: true}, func(w *world) { w.chatStatus = 500 }, "c) a chat completion"},
		{"M8d Tailscale Serve changed", worldOpts{hasCurrent: true}, func(w *world) { w.serveAfter = `{"TCP":{"8443":{}}}` }, "d) the Tailscale Serve"},
		{"M8e the public URL stopped answering", worldOpts{hasCurrent: true}, func(w *world) {
			w.cfgNode.cfg.Node.PublicHost = ptr("node.example.test")
			w.st.conn = "internet"
			w.publicBefore, w.publicAfter = 200, 502
		}, "e) https://node.example.test/v1/models answered 502"},
		{"M8f the context is not the old one", worldOpts{hasCurrent: true}, func(w *world) { w.nctx = 2048 }, "f) n_ctx is 2048"},
		{"cross fs, M8 fails", worldOpts{hasCurrent: true, crossFS: true, withDraft: true, dropIn: true}, func(w *world) { w.chatStatus = 500 }, "c) a chat completion"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, c.opts)
			c.break_(w)
			_, err := w.run(w.engine())
			assertRolledBackError(t, err, c.want)
			assertRolledBack(t, w, c.name)
			if !strings.Contains(w.out.String(), "Rolling back") || !strings.Contains(w.out.String(), "Rolled back:") {
				t.Fatalf("the output does not say it rolled back:\n%s", w.out.String())
			}
			// The migration can be tried again later: with the cause gone it completes.
			w2 := newWorldAfter(w)
			if _, err := w2.run(w2.engine()); err != nil {
				t.Fatalf("again: %v\n%s", err, w2.out.String())
			}
			assertMigrated(t, w2, c.name+", second attempt")
		})
	}
}

// newWorldAfter clears every failure switch of a world that was rolled back, keeping its disk state, so
// the migration can be run on the node again (rollback, then forward again).
func newWorldAfter(w *world) *world {
	w.newBroken, w.loadFails, w.noAuth, w.missingAlias = false, false, false, false
	w.chatStatus, w.v1Status, w.serveAfter, w.publicAfter = 0, 0, "", 0
	w.nctx, w.loadPolls, w.strangerOnPort = 4096, 2, false
	w.sd.fail = map[string]error{}
	w.out = &lockedBuf{}
	w.probe.polls = 0
	return w
}

// TestAWarningDoesNotRollBack: a public URL that was already not answering before is a warning.
func TestAPublicURLThatDidNotAnswerBeforeIsAWarning(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	w.cfgNode.cfg.Node.PublicHost = ptr("node.example.test")
	w.st.conn = "full"
	w.publicBefore, w.publicAfter = 502, 502
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if !strings.Contains(w.out.String(), "it did not answer 200 before the migration either") {
		t.Fatalf("no warning:\n%s", w.out.String())
	}
	assertMigrated(t, w, "public warning")
	// Offline: the public URL is not asked at all.
	w = newWorld(t, worldOpts{hasCurrent: true})
	w.cfgNode.cfg.Node.PublicHost = ptr("node.example.test")
	w.st.conn = "none"
	w.publicErr = true
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if !strings.Contains(w.out.String(), "offline: the public URL was not checked") {
		t.Fatalf("no offline warning:\n%s", w.out.String())
	}
}

// TestAFailureAtTheCommitStepDoesNotRollBack: M9 happens after the new service is verified, so it stays
// and a re-run finishes.
func TestAFailureAtTheCommitStepKeepsTheNewServiceAndTheRerunFinishes(t *testing.T) {
	for name, key := range map[string]string{
		"enable fails":  "enable " + NewUnitName,
		"disable fails": "disable " + LegacyUnitName,
		"reload fails":  "daemon-reload ",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, worldOpts{hasCurrent: true, crossFS: true})
			w.sd.failAlways[key] = errors.New("Failed to connect to bus")
			_, err := w.run(w.engine())
			var me *Error
			if !errors.As(err, &me) || me.RolledBack || me.Step != M9 {
				t.Fatalf("err = %v", err)
			}
			if w.portOwner() != "new" {
				t.Fatalf("port owner %q: the verified new service must keep serving", w.portOwner())
			}
			if pr := lastProgress(t, w); pr.Step != M8 {
				t.Fatalf("progress %+v", pr)
			}
			delete(w.sd.failAlways, key)
			if _, err := w.run(w.engine()); err != nil {
				t.Fatalf("re-run: %v", err)
			}
			assertMigrated(t, w, name)
		})
	}
}

// TestResumeFromEveryRecordedStep kills the run right after each step has been recorded and runs the
// migration again: it continues from that step and completes.
func TestResumeFromEveryRecordedStep(t *testing.T) {
	for name, o := range map[string]worldOpts{
		"same fs":  {hasCurrent: true},
		"cross fs": {hasCurrent: true, crossFS: true, withDraft: true, dropIn: true},
		"build":    {},
	} {
		t.Run(name, func(t *testing.T) {
			// A clean run finds where each step is recorded.
			at := map[string]int{}
			ref := newWorld(t, o)
			e := ref.engine()
			e.afterSave = func(p Progress) {
				if p.Step != "" && p.Rollback == "" {
					at[p.Step] = ref.k.count()
				}
			}
			if _, err := ref.run(e); err != nil {
				t.Fatal(err)
			}
			for _, step := range forwardSteps[:len(forwardSteps)-1] { // M0 to M9
				t.Run(step, func(t *testing.T) {
					w := newWorld(t, o)
					w.k.at, w.k.after = at[step], true
					if _, err := w.run(w.engine()); err == nil {
						t.Fatal("the run was not killed")
					}
					if pr := lastProgress(t, w); pr.Step != step {
						t.Fatalf("recorded step %q, want %q", pr.Step, step)
					}
					assertSafeAfterKill(t, w, "after "+step)
					reboot(w)
					res, err := w.run(w.engine())
					if err != nil || res.State != StateDone {
						t.Fatalf("resume: %+v %v\n%s", res, err, w.out.String())
					}
					assertMigrated(t, w, "resumed after "+step)
					if !strings.Contains(w.out.String(), "Resuming from step") {
						t.Errorf("the resumed run does not say so:\n%s", w.out.String())
					}
				})
			}
		})
	}
}

// reboot is the process coming back: nothing is dead any more, and a new Engine will read the journal.
func reboot(w *world) {
	w.k.mu.Lock()
	w.k.dead, w.k.at = false, 0
	w.k.mu.Unlock()
	w.out = &lockedBuf{}
	w.probe.polls = 0
}

// assertSafeAfterKill is what must hold at ANY point a run can die: no data is lost, the old unit and the
// key can still be recovered, and until the old service was stopped it is the one serving.
func assertSafeAfterKill(t *testing.T, w *world, where string) {
	t.Helper()
	f := func(format string, a ...any) { t.Helper(); t.Fatalf(where+": "+format, a...) }
	if got, ok := w.fs.content(oldModel); !(ok && string(got) == string(w.model)) {
		if got, ok := w.fs.content(newModel); !(ok && string(got) == string(w.model)) {
			f("the model is reachable neither at %s nor at %s", oldModel, newModel)
		}
	}
	_, haveUnit := w.fs.content("/etc/systemd/system/llama-qwen.service")
	_, haveBackup := w.fs.content("/var/lib/localai/migration/llama-qwen/llama-qwen.service")
	linkUnit, _ := w.fs.Readlink("/etc/systemd/system/llama-qwen.service")
	if !haveUnit && !haveBackup && linkUnit == "" {
		f("the old unit is neither in /etc/systemd/system nor in the backup")
	}
	if got, ok := w.fs.content("/etc/llama-qwen/api-key"); !(ok && string(got) == w.key) {
		if got, ok := w.fs.content("/etc/localai/api-key"); !(ok && string(got) == w.key) {
			f("the key is lost")
		}
	}
	if !w.fs.has("/var/lib/localai/migration/llama-qwen/progress.json") {
		return
	}
	var pr Progress
	data, _ := w.fs.content("/var/lib/localai/migration/llama-qwen/progress.json")
	if json.Unmarshal(data, &pr) != nil {
		f("progress.json is damaged")
	}
	if pr.T0 == nil && pr.Rollback == "" && pr.Step != M9 && pr.Step != M10 {
		if st, _ := w.sd.State(context.Background(), LegacyUnitName); st.ActiveState != "active" {
			f("the old service is not running although M6 has not started (step %q)", pr.Step)
		}
	}
	if pending, _, _ := w.engine().Pending(context.Background()); !pending && pr.Step != M10 {
		f("an interrupted migration is not reported as pending")
	}
}

// survive is the kill-at-every-step test: it runs the whole migration, kills the process before and after
// every mutating operation (every file write, rename, link, unit call, state and node.yaml write), and
// demands that a new process always ends in the same consistent state.
func survive(t *testing.T, o worldOpts, setup func(*world), final func(*testing.T, *world, string, *Result, error)) {
	t.Helper()
	clean := newWorld(t, o)
	if setup != nil {
		setup(clean)
	}
	cres, cerr := clean.run(clean.engine())
	total := clean.k.count()
	final(t, clean, "clean run", cres, cerr)
	if total < 40 {
		t.Fatalf("only %d operations: the counter is not counting", total)
	}
	t.Logf("%d mutating operations, %d kills", total, 2*total)
	for k := 1; k <= total; k++ {
		for _, after := range []bool{false, true} {
			where := "kill " + map[bool]string{false: "before", true: "after"}[after] + " operation " + strconv.Itoa(k) + "/" + strconv.Itoa(total)
			w := newWorld(t, o)
			if setup != nil {
				setup(w)
			}
			w.k.at, w.k.after = k, after
			res, err := w.run(w.engine())
			if !w.k.dead {
				// Dying "after" the very last operation is a run that completed.
				if !(after && k == total) {
					t.Fatalf("%s: the process did not die (err %v)", where, err)
				}
			} else {
				assertSafeAfterKill(t, w, where)
				reboot(w)
				res, err = w.run(w.engine())
			}
			final(t, w, where, res, err)
			// Recovery is idempotent: running again changes nothing.
			if err == nil && res.State == StateDone {
				snap := w.fs.snapshot()
				reboot(w)
				if _, err2 := w.run(w.engine()); err2 != nil {
					t.Fatalf("%s: the run after recovery failed: %v", where, err2)
				}
				if w.fs.snapshot() != snap {
					t.Fatalf("%s: a run after recovery changed the disk", where)
				}
			}
		}
	}
}

func finalMigrated(t *testing.T, w *world, where string, _ *Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: the migration did not complete: %v\n%s", where, err, w.out.String())
	}
	assertMigrated(t, w, where)
	if !w.agent.active {
		t.Fatalf("%s: the agent was left stopped", where)
	}
	assertNoKey(t, w, where)
}

func TestMigrationSurvivesAKillAtEveryStep(t *testing.T) {
	t.Run("same filesystem", func(t *testing.T) {
		survive(t, worldOpts{hasCurrent: true}, nil, finalMigrated)
	})
	t.Run("across filesystems with a draft, a drop-in and a build", func(t *testing.T) {
		survive(t, worldOpts{crossFS: true, withDraft: true, dropIn: true}, nil, finalMigrated)
	})
}

// TestRollbackSurvivesAKillAtEveryStep: M8 fails, so the migration rolls back by itself; the process is
// killed at every operation of the whole run, the migration is run again, and the node always ends with
// the old service serving.
func TestRollbackSurvivesAKillAtEveryStep(t *testing.T) {
	survive(t, worldOpts{hasCurrent: true, crossFS: true}, func(w *world) { w.chatStatus = 500 },
		func(t *testing.T, w *world, where string, res *Result, err error) {
			t.Helper()
			// A rollback that was killed half way is finished by the next run, which reports success.
			if err == nil && res != nil && res.State == StateRolledBack {
				assertRolledBack(t, w, where)
				return
			}
			assertRolledBackError(t, err, "c) a chat completion")
			assertRolledBack(t, w, where)
		})
}

func TestKilledBetweenStoppingTheOldServiceAndTheJournalStartsItAgainFirst(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	// Run up to M5, then kill right after the old service was stopped (the stop is the operation after
	// T0 was recorded).
	ref := newWorld(t, worldOpts{hasCurrent: true})
	stopAt := 0
	ref.sd.calls = nil
	_, _ = ref.run(ref.engine(func(o *Options) {
		o.Systemd = &countSD{fakeSD: ref.sd, onStop: func() { stopAt = ref.k.count() }}
	}))
	w.k.at, w.k.after = stopAt, true
	if _, err := w.run(w.engine()); err == nil {
		t.Fatal("not killed")
	}
	if st, _ := w.sd.State(context.Background(), LegacyUnitName); st.ActiveState != "inactive" {
		t.Fatalf("the old service should be stopped: %+v", st)
	}
	if pr := lastProgress(t, w); pr.Step != M5 || pr.T0 == nil {
		t.Fatalf("progress %+v", pr)
	}
	reboot(w)
	if _, err := w.run(w.engine()); err != nil {
		t.Fatalf("%v\n%s", err, w.out.String())
	}
	if !strings.Contains(w.out.String(), "stopped "+LegacyUnitName+" and did not finish: starting it again") {
		t.Fatalf("the old service was not started again first:\n%s", w.out.String())
	}
	assertMigrated(t, w, "killed in the downtime")
	if w.sd.called("start", LegacyUnitName) != 1 {
		t.Fatalf("calls %v", w.sd.calls)
	}
}

type countSD struct {
	*fakeSD
	onStop func()
}

func (c *countSD) Stop(ctx context.Context, unit string) error {
	err := c.fakeSD.Stop(ctx, unit)
	if unit == LegacyUnitName {
		c.onStop()
	}
	return err
}

func TestStateJSONFollowsTheSteps(t *testing.T) {
	w := newWorld(t, worldOpts{hasCurrent: true})
	if _, err := w.run(w.engine()); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, h := range w.st.history {
		steps = append(steps, h.State+"/"+h.Step)
	}
	got := strings.Join(steps, " ")
	for _, want := range []string{"in_progress/M0", "in_progress/M8", "done/M9", "done/M10"} {
		if !strings.Contains(got, want) {
			t.Errorf("state history lacks %q: %s", want, got)
		}
	}
	if last := w.st.history[len(w.st.history)-1]; last.State != StateDone || last.Step != M10 || last.At.IsZero() {
		t.Fatalf("last %+v", last)
	}
}
