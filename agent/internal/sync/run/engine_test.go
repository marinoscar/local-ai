package run

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
)

// holdUntil makes the fake machine's items block until the test lets them go (or the run is cancelled).
type holdUntil struct {
	mu    sync.Mutex
	chans map[string]chan struct{}
}

func newHold() *holdUntil { return &holdUntil{chans: map[string]chan struct{}{}} }

func (h *holdUntil) ch(key string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.chans[key]
	if !ok {
		c = make(chan struct{})
		h.chans[key] = c
	}
	return c
}

func (h *holdUntil) release(key string) {
	c := h.ch(key)
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-c:
	default:
		close(c)
	}
}

// waitOrCancel blocks until the item is released or the context ends.
func (h *holdUntil) waitOrCancel(ctx context.Context, key string) bool {
	select {
	case <-h.ch(key):
		return true
	case <-ctx.Done():
		return false
	}
}

func cancelled(job item.Job) item.Result {
	return item.Result{Root: job.Root.ID, Item: job.Ref.ID, State: journal.StateDownloading, Cancelled: true}
}

func TestPreflightTable(t *testing.T) {
	internal := ark.Root{ID: "internal", Kind: ark.KindInternal, Path: "/srv/localai", State: ark.StateReady}
	drive := ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: "/media/x/localai", Mount: "/media/x", State: ark.StateReady}
	roRoot := ark.Root{ID: driveB, Kind: ark.KindArkDrive, Path: "/media/y/localai", State: ark.StateReadOnly, ReadOnly: true}
	online := NetState{Mode: plan.ModeOnline, Online: true}
	def := DefaultSettings()
	disabled := def
	disabled.Enabled = false
	battOK := def
	battOK.AllowOnBattery = true
	noDrives := def
	noDrives.ArkDrive.Enabled = false

	cases := []struct {
		name      string
		req       StartRequest
		set       Settings
		net       NetState
		battery   bool
		mounted   []ark.Root
		skip      string // reason of a skip
		code      string // doctor code of the skip or the refusal
		refuse    bool
		offline   bool
		warnings  int
		wantRoots []string
	}{
		{name: "scheduled, all good", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: online, mounted: []ark.Root{internal, drive}, wantRoots: []string{"internal", driveID}},
		{name: "scheduled, disabled", req: StartRequest{Trigger: TriggerScheduled}, set: disabled, net: online, mounted: []ark.Root{internal}, skip: SkipDisabled},
		{name: "manual, disabled still goes", req: StartRequest{Trigger: TriggerManual}, set: disabled, net: online, mounted: []ark.Root{internal}, wantRoots: []string{"internal"}},
		{name: "scheduled, survival", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: NetState{Mode: plan.ModeSurvival}, mounted: []ark.Root{internal}, skip: SkipMode, code: doctor.SYN002},
		{name: "manual, survival is refused", req: StartRequest{Trigger: TriggerManual}, set: def, net: NetState{Mode: plan.ModeSurvival}, mounted: []ark.Root{internal}, refuse: true, code: doctor.SYN010},
		{name: "manual, survival is refused even with allow_offline_mode", req: StartRequest{Trigger: TriggerManual, AllowOfflineMode: true}, set: def, net: NetState{Mode: plan.ModeSurvival}, mounted: []ark.Root{internal}, refuse: true, code: doctor.SYN010},
		{name: "cloud, survival is refused", req: StartRequest{Trigger: TriggerCloud}, set: def, net: NetState{Mode: plan.ModeSurvival}, mounted: []ark.Root{internal}, refuse: true, code: doctor.SYN010},
		{name: "scheduled, offline mode", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: NetState{Mode: plan.ModeOffline, Online: true}, mounted: []ark.Root{internal}, skip: SkipMode, code: doctor.SYN002},
		{name: "mode_online catch-up in offline mode skips", req: StartRequest{Trigger: TriggerModeOnline}, set: def, net: NetState{Mode: plan.ModeOffline, Online: true}, mounted: []ark.Root{internal}, skip: SkipMode, code: doctor.SYN002},
		{name: "manual, offline mode needs confirmation", req: StartRequest{Trigger: TriggerManual}, set: def, net: NetState{Mode: plan.ModeOffline, Online: true}, mounted: []ark.Root{internal}, refuse: true, code: doctor.SYN011},
		{name: "manual, offline mode with allow_offline_mode uses the network", req: StartRequest{Trigger: TriggerManual, AllowOfflineMode: true}, set: def, net: NetState{Mode: plan.ModeOffline, Online: true}, mounted: []ark.Root{internal}, wantRoots: []string{"internal"}},
		{name: "scheduled, on battery", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: online, battery: true, mounted: []ark.Root{internal}, skip: SkipBattery, code: doctor.SYN003},
		{name: "scheduled, on battery but allowed", req: StartRequest{Trigger: TriggerScheduled}, set: battOK, net: online, battery: true, mounted: []ark.Root{internal}, wantRoots: []string{"internal"}},
		{name: "manual, on battery goes with a warning", req: StartRequest{Trigger: TriggerManual}, set: def, net: online, battery: true, mounted: []ark.Root{internal}, warnings: 1, wantRoots: []string{"internal"}},
		{name: "scheduled, no connectivity", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: NetState{Mode: plan.ModeOnline, Online: false}, mounted: []ark.Root{internal}, skip: SkipOffline, code: doctor.SYN001},
		{name: "manual, no connectivity goes, resolvers fail fast", req: StartRequest{Trigger: TriggerManual}, set: def, net: NetState{Mode: plan.ModeOnline, Online: false}, mounted: []ark.Root{internal}, offline: true, wantRoots: []string{"internal"}},
		{name: "scheduled, no writable roots", req: StartRequest{Trigger: TriggerScheduled}, set: def, net: online, mounted: []ark.Root{roRoot}, skip: SkipNoRoots, code: doctor.ARK001},
		{name: "manual, no writable roots", req: StartRequest{Trigger: TriggerManual}, set: def, net: online, mounted: []ark.Root{roRoot}, refuse: true, code: doctor.ARK001},
		{name: "drives disabled in the settings are not written", req: StartRequest{Trigger: TriggerManual}, set: noDrives, net: online, mounted: []ark.Root{internal, drive}, wantRoots: []string{"internal"}},
		{name: "a named root", req: StartRequest{Trigger: TriggerManual, Roots: []string{driveID}}, set: def, net: online, mounted: []ark.Root{internal, drive}, wantRoots: []string{driveID}},
		{name: "a root named by its path and by its mount point", req: StartRequest{Trigger: TriggerManual, Roots: []string{"/srv/localai/", "/media/x"}}, set: def, net: online, mounted: []ark.Root{internal, drive}, wantRoots: []string{"internal", driveID}},
		{name: "a named read-only root is left out", req: StartRequest{Trigger: TriggerManual, Roots: []string{"internal", driveB}}, set: def, net: online, mounted: []ark.Root{internal, roRoot}, wantRoots: []string{"internal"}},
		{name: "verify is allowed in survival: it needs no network", req: StartRequest{Trigger: TriggerVerify}, set: def, net: NetState{Mode: plan.ModeSurvival}, mounted: []ark.Root{internal}, offline: true, wantRoots: []string{"internal"}},
		{name: "import is allowed in offline mode", req: StartRequest{Trigger: TriggerImport}, set: def, net: NetState{Mode: plan.ModeOffline}, mounted: []ark.Root{internal}, offline: true, wantRoots: []string{"internal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := preflight(tc.req, tc.set, tc.net, tc.battery, tc.mounted)
			switch {
			case tc.refuse:
				var m *ModeError
				var none NoRootsError
				switch {
				case errors.As(v.Err, &m):
					if m.Code != tc.code {
						t.Fatalf("code = %s, want %s", m.Code, tc.code)
					}
				case errors.As(v.Err, &none):
					if tc.code != doctor.ARK001 {
						t.Fatalf("refused with no roots, want %s", tc.code)
					}
				default:
					t.Fatalf("verdict = %+v", v)
				}
			case tc.skip != "":
				if !v.Skip || v.Reason != tc.skip || v.Code != tc.code || v.Err != nil {
					t.Fatalf("verdict = %+v", v)
				}
			default:
				if v.Skip || v.Err != nil {
					t.Fatalf("verdict = %+v", v)
				}
				if !reflect.DeepEqual(v.Roots, tc.wantRoots) || v.Offline != tc.offline || len(v.Warnings) != tc.warnings {
					t.Fatalf("verdict = %+v", v)
				}
			}
		})
	}
	t.Run("an unknown root is refused whatever the trigger", func(t *testing.T) {
		v := preflight(StartRequest{Trigger: TriggerScheduled, Roots: []string{"ark-ffffffff"}}, def, online, false, []ark.Root{internal})
		var u *UnknownRootError
		if !errors.As(v.Err, &u) || u.Root != "ark-ffffffff" {
			t.Fatalf("verdict = %+v", v)
		}
	})
}

func TestStartSkipsAScheduledRunAndRefusesAManualOne(t *testing.T) {
	r := newRig(t)
	r.net = NetState{Mode: plan.ModeSurvival}
	eng := r.start()

	got, err := eng.Start(bg, StartRequest{Trigger: TriggerScheduled})
	if err != nil || got.State != StateSkipped || got.Reason != SkipMode || got.Code != doctor.SYN002 || !journal.ValidRunID(got.RunID) {
		t.Fatalf("scheduled: %+v, %v", got, err)
	}
	if eng.RunActive() {
		t.Fatal("a skipped run holds the slot")
	}
	st, ok := eng.Status()
	if !ok || st.RunID != got.RunID || st.State != StateSkipped || st.Reason != SkipMode || st.Result != ResultSkipped || st.FinishedAt == nil {
		t.Fatalf("status = %+v", st)
	}
	if d := r.evs.done(); len(d) != 1 || d[0].State != StateSkipped {
		t.Fatalf("sync.done = %+v", d)
	}
	_, err = eng.Start(bg, StartRequest{Trigger: TriggerManual})
	var me *ModeError
	if !errors.As(err, &me) || me.Code != doctor.SYN010 {
		t.Fatalf("manual in survival: %v", err)
	}
	// Nothing was planned and nothing ran.
	if len(r.plnr.reqs) != 0 || len(r.mach.order()) != 0 {
		t.Fatal("a refused run planned or ran something")
	}
}

func TestOneRunAtATime(t *testing.T) {
	r := newRig(t)
	hold := newHold()
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		if !hold.waitOrCancel(ctx, job.Ref.ID) {
			return cancelled(job)
		}
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 100)}})
	eng := r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)

	_, err := eng.Start(bg, StartRequest{Trigger: TriggerManual})
	var active *RunActiveError
	if !errors.As(err, &active) || active.RunID != id {
		t.Fatalf("second Start: %v", err)
	}
	if !eng.RunActive() || !eng.Running() {
		t.Fatal("RunActive is false during a run")
	}
	hold.release("m1")
	st := r.waitEnd(id)
	if st.State != StateDone || st.Result != ResultOK || st.Totals.Done != 1 {
		t.Fatalf("status = %+v", st)
	}
	if eng.RunActive() {
		t.Fatal("RunActive is true after the run")
	}
	// The slot is free again.
	hold.release("m1")
	id2 := r.startRun(StartRequest{Trigger: TriggerManual})
	if id2 == id {
		t.Fatal("same run id twice")
	}
	r.waitEnd(id2)
}

func TestSecondProcessFailsTheLock(t *testing.T) {
	r1 := newRig(t)
	hold := newHold()
	r1.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		hold.waitOrCancel(ctx, job.Ref.ID)
		return verified(job, "v1")
	}
	r1.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	e1 := r1.start()
	id := r1.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r1.mach.startedCh, 1)

	// Another agent (its own journal, its own memory) on the same lock file.
	r2 := newRig(t)
	r2.deps.Lock = FileLock{Path: r1.dir + "/lock"}
	e2 := r2.start()
	_, err := e2.Start(bg, StartRequest{Trigger: TriggerManual})
	var active *RunActiveError
	if !errors.As(err, &active) || active.RunID != id {
		t.Fatalf("second process: %v (want run %s)", err, id)
	}
	hold.release("m1")
	r1.waitEnd(id)
	_ = e1
	// With the first run over, the second process gets the lock.
	r2.mach.beh = nil
	r2.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	id2 := r2.startRun(StartRequest{Trigger: TriggerManual})
	r2.waitEnd(id2)
}

func TestCancelStopsCleanlyAndFreesTheSlot(t *testing.T) {
	r := newRig(t)
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		<-ctx.Done()
		return cancelled(job)
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 100), row("m2", 100)}})
	eng := r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)

	if gotID, active := eng.Cancel(); gotID != id || !active {
		t.Fatalf("Cancel = %s, %v", gotID, active)
	}
	if st, _ := eng.Status(); st.State != StateCancelling && st.FinishedAt == nil {
		t.Fatalf("state during cancel = %s", st.State)
	}
	eng.Cancel() // idempotent
	st := r.waitEnd(id)
	if st.State != StateCancelled || st.Result != ResultCancelled {
		t.Fatalf("status = %+v", st)
	}
	// The cancelled item is not reported, the second never started: both resume next run.
	if len(r.evs.itemDone()) != 0 || len(r.mach.order()) != 1 {
		t.Fatalf("items done %v, started %v", r.evs.itemDone(), r.mach.order())
	}
	if d := r.evs.done(); len(d) != 1 || d[0].State != StateCancelled {
		t.Fatalf("sync.done = %+v", d)
	}
	// With nothing running, a cancel does nothing and says which run was the last.
	if gotID, active := eng.Cancel(); gotID != id || active {
		t.Fatalf("Cancel with no run = %s, %v", gotID, active)
	}
	// The slot is free.
	r.mach.beh = nil
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
}

func TestCancelWithNoRunEver(t *testing.T) {
	r := newRig(t)
	eng := r.start()
	if id, active := eng.Cancel(); id != "" || active {
		t.Fatalf("Cancel = %q, %v", id, active)
	}
	if _, ok := eng.Status(); ok {
		t.Fatal("a status before any run")
	}
	if err := eng.CancelGraceful(bg); err != nil {
		t.Fatal(err)
	}
}

func TestModeSwitchCancelsTheRunWithLAISYN002(t *testing.T) {
	r := newRig(t)
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		<-ctx.Done()
		return cancelled(job)
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 100)}})
	eng := r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)

	// The mode engine's step: cancel and wait for the run to stop.
	if err := eng.CancelGraceful(bg); err != nil {
		t.Fatal(err)
	}
	st, _ := eng.Status()
	if st.RunID != id || st.State != StateCancelled || st.Code != doctor.SYN002 || st.Reason != SkipMode || st.FinishedAt == nil {
		t.Fatalf("status = %+v", st)
	}
	if eng.RunActive() {
		t.Fatal("the run is still active after CancelGraceful")
	}
}

func TestCancelGracefulHonoursItsContext(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		<-release // a machine that ignores the cancel for a while
		return cancelled(job)
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	eng := r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if err := eng.CancelGraceful(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CancelGraceful = %v", err)
	}
	close(release)
	r.waitEnd(id)
}

func TestSchedulerRespectsConcurrencyAndWritersPerRoot(t *testing.T) {
	t.Run("one writer per root, one slot per root at a time", func(t *testing.T) {
		r := newRig(t)
		r.set.Concurrency, r.set.PerRootWriters = 4, 1
		r.addRoot(ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: r.dir + "/drive", State: ark.StateReady})
		hold := newHold()
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			hold.waitOrCancel(ctx, job.Root.ID+"/"+job.Ref.ID)
			return verified(job, "v1")
		}
		r.setPlans(map[string][]plan.ItemPlan{
			"internal": {row("i1", 1), row("i2", 1), row("i3", 1)},
			driveID:    {row("d1", 1), row("d2", 1)},
		})
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})

		// Two roots, one writer each: exactly one item per root is running.
		first := recvN(t, r.mach.startedCh, 2)
		sort.Strings(first)
		if !reflect.DeepEqual(first, []string{driveID + "/d1", "internal/i1"}) {
			t.Fatalf("first started = %v", first)
		}
		expectNone(t, r.mach.startedCh)
		hold.release("internal/i1")
		if got := recvN(t, r.mach.startedCh, 1); got[0] != "internal/i2" {
			t.Fatalf("after i1: %v", got)
		}
		hold.release(driveID + "/d1")
		if got := recvN(t, r.mach.startedCh, 1); got[0] != driveID+"/d2" {
			t.Fatalf("after d1: %v", got)
		}
		hold.release("internal/i2")
		if got := recvN(t, r.mach.startedCh, 1); got[0] != "internal/i3" {
			t.Fatalf("after i2: %v", got)
		}
		hold.release(driveID + "/d2")
		hold.release("internal/i3")
		st := r.waitEnd(id)
		if st.Totals.Done != 5 || st.Totals.Items != 5 {
			t.Fatalf("totals = %+v", st.Totals)
		}
		if r.mach.maxRoot["internal"] != 1 || r.mach.maxRoot[driveID] != 1 || r.mach.maxCur != 2 {
			t.Fatalf("max per root %v, max overall %d", r.mach.maxRoot, r.mach.maxCur)
		}
	})
	t.Run("two writers per root", func(t *testing.T) {
		r := newRig(t)
		r.set.Concurrency, r.set.PerRootWriters = 4, 2
		hold := newHold()
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			hold.waitOrCancel(ctx, job.Ref.ID)
			return verified(job, "v1")
		}
		r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1), row("c", 1)}})
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})
		recvN(t, r.mach.startedCh, 2)
		expectNone(t, r.mach.startedCh)
		hold.release("a")
		recvN(t, r.mach.startedCh, 1)
		hold.release("b")
		hold.release("c")
		r.waitEnd(id)
		if r.mach.maxRoot["internal"] != 2 {
			t.Fatalf("max per root = %d", r.mach.maxRoot["internal"])
		}
	})
	t.Run("network downloads: sync.concurrency slots over all roots", func(t *testing.T) {
		r := newRig(t)
		r.set.Concurrency, r.set.PerRootWriters = 2, 4
		var mu sync.Mutex
		net, maxNet := 0, 0
		hold := newHold()
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			release, err := job.Gate.Network(ctx)
			if err != nil {
				return cancelled(job)
			}
			mu.Lock()
			net++
			maxNet = max(maxNet, net)
			mu.Unlock()
			hold.waitOrCancel(ctx, job.Ref.ID)
			mu.Lock()
			net--
			mu.Unlock()
			release()
			return verified(job, "v1")
		}
		r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1), row("c", 1), row("d", 1)}})
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})
		recvN(t, r.mach.startedCh, 4) // all four are in flight (four writers), only two hold a slot
		hold.release("a")
		hold.release("b")
		hold.release("c")
		hold.release("d")
		r.waitEnd(id)
		if maxNet != 2 {
			t.Fatalf("max network downloads = %d, want 2", maxNet)
		}
	})
	t.Run("an exclusive resource runs one at a time", func(t *testing.T) {
		r := newRig(t)
		r.set.PerRootWriters = 4
		var mu sync.Mutex
		cur, maxCur := 0, 0
		hold := newHold()
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			release, err := job.Gate.Exclusive(ctx, "pmtiles")
			if err != nil {
				return cancelled(job)
			}
			mu.Lock()
			cur++
			maxCur = max(maxCur, cur)
			mu.Unlock()
			hold.waitOrCancel(ctx, job.Ref.ID)
			mu.Lock()
			cur--
			mu.Unlock()
			release()
			return verified(job, "v1")
		}
		r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1), row("c", 1)}})
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})
		recvN(t, r.mach.startedCh, 3)
		hold.release("a")
		hold.release("b")
		hold.release("c")
		r.waitEnd(id)
		if maxCur != 1 {
			t.Fatalf("max exclusive = %d", maxCur)
		}
	})
}

func TestInterleaveIsRoundRobinBetweenRoots(t *testing.T) {
	mk := func(root, id string) *task { return &task{root: ark.Root{ID: root}, row: plan.ItemPlan{ID: id}} }
	got := interleave([][]*task{
		{mk("internal", "i1"), mk("internal", "i2"), mk("internal", "i3")},
		{mk("ark-a", "d1")},
		{mk("ark-b", "e1"), mk("ark-b", "e2")},
	})
	var keys []string
	for _, tk := range got {
		keys = append(keys, tk.key())
	}
	want := []string{"internal/i1", "ark-a/d1", "ark-b/e1", "internal/i2", "ark-b/e2", "internal/i3"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("order = %v, want %v", keys, want)
	}
}

func TestSingleRootRunsInPlanOrder(t *testing.T) {
	r := newRig(t)
	r.set.PerRootWriters = 1
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("first", 1), row("second", 1), row("third", 1)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	r.waitEnd(id)
	if got := r.mach.order(); !reflect.DeepEqual(got, []string{"internal/first", "internal/second", "internal/third"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestLocalCopyWaitsForTheFirstCopyToVerify(t *testing.T) {
	setup := func(t *testing.T) (*rig, *holdUntil) {
		r := newRig(t)
		r.set.Concurrency, r.set.PerRootWriters = 4, 1
		r.addRoot(ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: r.dir + "/drive", State: ark.StateReady})
		hold := newHold()
		local := row("m1", 100)
		local.Source, local.FromRoot, local.After = plan.SourceLocalCopy, "internal", "internal"
		r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 100)}, driveID: {local}})
		_ = hold
		return r, hold
	}
	t.Run("the copy starts only after the first copy verified", func(t *testing.T) {
		r, hold := setup(t)
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			hold.waitOrCancel(ctx, job.Root.ID+"/"+job.Ref.ID)
			return verified(job, "v1")
		}
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})
		if got := recvN(t, r.mach.startedCh, 1); got[0] != "internal/m1" {
			t.Fatalf("started %v", got)
		}
		expectNone(t, r.mach.startedCh) // the drive's copy waits for it
		hold.release("internal/m1")
		if got := recvN(t, r.mach.startedCh, 1); got[0] != driveID+"/m1" {
			t.Fatalf("then started %v", got)
		}
		hold.release(driveID + "/m1")
		if st := r.waitEnd(id); st.Totals.Done != 2 {
			t.Fatalf("totals = %+v", st.Totals)
		}
	})
	t.Run("when the first copy fails there is nothing to copy", func(t *testing.T) {
		r, _ := setup(t)
		r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
			return item.Result{Root: job.Root.ID, Item: job.Ref.ID, State: journal.StateFailed, Code: doctor.SYN031, Reason: "exhausted", Retryable: true}
		}
		r.start()
		id := r.startRun(StartRequest{Trigger: TriggerManual})
		st := r.waitEnd(id)
		if got := r.mach.order(); !reflect.DeepEqual(got, []string{"internal/m1"}) {
			t.Fatalf("started %v: the copy must not start", got)
		}
		if st.Totals.Failed != 1 || st.Totals.Skipped != 1 || st.Totals.Items != 2 {
			t.Fatalf("totals = %+v", st.Totals)
		}
		var skipped *ItemResult
		for i := range st.Items {
			if st.Items[i].Result == "skipped" {
				skipped = &st.Items[i]
			}
		}
		if skipped == nil || skipped.Root != driveID || skipped.Reason != string(plan.ReasonUnresolved) {
			t.Fatalf("items = %+v", st.Items)
		}
	})
}

func TestOneItemFailsTheRestContinue(t *testing.T) {
	r := newRig(t)
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		if job.Ref.ID == "bad" {
			_, _ = r.jr.Items.Update(job.Root.ID, job.Ref.ID, func(i *journal.Item) { i.State, i.Version = journal.StateFailed, "v1" })
			return item.Result{Root: job.Root.ID, Item: job.Ref.ID, Version: "v1", State: journal.StateFailed, Code: doctor.SYN031, Reason: "exhausted", Retryable: true}
		}
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 100), row("bad", 200), row("c", 300)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	st := r.waitEnd(id)
	if st.State != StateDone || st.Result != ResultPartial || st.Totals.Done != 2 || st.Totals.Failed != 1 || st.Totals.Items != 3 {
		t.Fatalf("status = %+v", st)
	}
	results := map[string]string{}
	for _, d := range r.evs.itemDone() {
		results[d.Item] = d.Result
	}
	if !reflect.DeepEqual(results, map[string]string{"a": "verified", "bad": "failed", "c": "verified"}) {
		t.Fatalf("item_done = %v", results)
	}
	if st.Totals.BytesTotal != 600 || st.Totals.BytesDone != 400 {
		t.Fatalf("bytes = %+v", st.Totals)
	}
	rec, _ := r.jr.Items.Get("internal", "bad")
	if rec.ConsecutiveFailedRuns != 1 {
		t.Fatalf("consecutive failed runs = %d", rec.ConsecutiveFailedRuns)
	}
}

func TestThreeFailedRunsInARowRaiseLAISYN035(t *testing.T) {
	r := newRig(t)
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		_, _ = r.jr.Items.Update(job.Root.ID, job.Ref.ID, func(i *journal.Item) { i.State, i.Version = journal.StateFailed, "v1" })
		return item.Result{Root: job.Root.ID, Item: job.Ref.ID, Version: "v1", State: journal.StateFailed, Code: doctor.SYN031, Reason: "exhausted"}
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("flaky", 1)}})
	r.start()
	var codes []string
	for i := 0; i < RepeatFailureRuns; i++ {
		r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	}
	for _, d := range r.evs.itemDone() {
		codes = append(codes, d.Code)
	}
	want := []string{doctor.SYN031, doctor.SYN031, doctor.SYN035}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}
	// A success resets the count (the machine does it; here the record says so).
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		_, _ = r.jr.Items.Update(job.Root.ID, job.Ref.ID, func(i *journal.Item) { i.State, i.ConsecutiveFailedRuns = journal.StateVerified, 0 })
		return verified(job, "v1")
	}
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if rec, _ := r.jr.Items.Get("internal", "flaky"); rec.ConsecutiveFailedRuns != 0 {
		t.Fatalf("counter = %d", rec.ConsecutiveFailedRuns)
	}
}

func TestRootFaultStopsWritesToThatRootOnly(t *testing.T) {
	r := newRig(t)
	r.set.PerRootWriters = 1
	r.addRoot(ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: r.dir + "/drive", State: ark.StateReady})
	var mu sync.Mutex
	var faults []string
	r.deps.RootFault = func(rootID string, err *download.Error) {
		mu.Lock()
		faults = append(faults, rootID+":"+err.Code)
		mu.Unlock()
	}
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		if job.Root.ID == driveID && job.Ref.ID == "d1" {
			return item.Result{Root: job.Root.ID, Item: job.Ref.ID, State: journal.StateFailed, Code: download.CodeIO, Reason: "root_fault",
				RootFault: &download.Error{Kind: download.KindRootFault, Code: download.CodeIO, Err: errors.New("EIO")}}
		}
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("i1", 1), row("i2", 1)}, driveID: {row("d1", 1), row("d2", 1), row("d3", 1)}})
	r.start()
	st := r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if !reflect.DeepEqual(faults, []string{driveID + ":" + download.CodeIO}) {
		t.Fatalf("faults = %v", faults)
	}
	started := map[string]bool{}
	for _, k := range r.mach.order() {
		started[k] = true
	}
	if started[driveID+"/d2"] || started[driveID+"/d3"] || !started["internal/i2"] {
		t.Fatalf("started = %v", r.mach.order())
	}
	if st.Totals.Done != 2 || st.Totals.Failed != 1 || st.Totals.Skipped != 2 || st.Totals.Items != 5 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	for _, it := range st.Items {
		if it.Result == "skipped" && (it.Reason != string(plan.ReasonReadOnly) || it.Code != doctor.ARK003) {
			t.Fatalf("skipped item = %+v", it)
		}
	}
}

func TestScheduledRunStopsAtMaxRunHours(t *testing.T) {
	r := newRig(t)
	r.set.MaxRunHours = 2
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		<-ctx.Done()
		return cancelled(job)
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1), row("c", 1)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerScheduled})
	recvN(t, r.mach.startedCh, 1)
	if r.clk.pending() != 1 {
		t.Fatalf("pending timers = %d, want the time limit", r.clk.pending())
	}
	r.clk.Advance(90 * time.Minute)
	if st, _ := r.eng.Status(); st.FinishedAt != nil && st.State != StateRunning {
		// not yet: 90 minutes is under the limit
		t.Fatalf("stopped early: %+v", st)
	}
	r.clk.Advance(30 * time.Minute)
	st := r.waitEnd(id)
	// The run ends done, with every item it did not finish skipped for time (they resume next night).
	if st.State != StateDone || st.Reason != string(plan.ReasonTimeLimit) || st.Result != ResultPartial {
		t.Fatalf("status = %+v", st)
	}
	if st.Totals.Skipped != 3 || st.Totals.Items != 3 || st.Totals.Done != 0 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	for _, it := range st.Items {
		if it.Result != "skipped" || it.Reason != string(plan.ReasonTimeLimit) {
			t.Fatalf("item = %+v", it)
		}
	}
	if got := r.mach.order(); len(got) != 1 {
		t.Fatalf("started = %v: nothing starts after the limit", got)
	}
}

func TestManualRunHasNoTimeLimit(t *testing.T) {
	r := newRig(t)
	r.set.MaxRunHours = 1
	hold := newHold()
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		hold.waitOrCancel(ctx, job.Ref.ID)
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)
	if r.clk.pending() != 0 {
		t.Fatal("a manual run armed the time limit")
	}
	r.clk.Advance(48 * time.Hour)
	hold.release("a")
	if st := r.waitEnd(id); st.State != StateDone || st.Result != ResultOK {
		t.Fatalf("status = %+v", st)
	}
}

func TestProgressEventsAreThrottledAndStatusHasRateAndETA(t *testing.T) {
	r := newRig(t)
	step := make(chan struct{})
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		report := func(state journal.ItemState, done int64) {
			job.Progress(item.Progress{State: state, Done: done, Total: 10_000})
		}
		report(journal.StateDownloading, 100)
		report(journal.StateDownloading, 200) // same second: no event
		report(journal.StateDownloading, 300)
		step <- struct{}{}
		<-step
		report(journal.StateDownloading, 2100) // two seconds later
		report(journal.StateVerifying, 2100)   // a state change goes out at once
		step <- struct{}{}
		<-step
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 10_000)}})
	eng := r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})

	<-step
	if got := r.evs.progress(); len(got) != 1 || got[0].BytesDone != 100 || got[0].State != "downloading" || got[0].Item != "m1" {
		t.Fatalf("progress after three reports in one second: %+v", got)
	}
	r.clk.Advance(2 * time.Second)
	step <- struct{}{}
	<-step
	got := r.evs.progress()
	if len(got) != 3 || got[1].BytesDone != 2100 || got[1].State != "downloading" || got[2].State != "verifying" {
		t.Fatalf("progress = %+v", got)
	}
	st, _ := eng.Status()
	if st.State != StateRunning || len(st.Active) != 1 || st.Active[0].Item != "m1" || st.Active[0].State != "verifying" || st.Active[0].BytesDone != 2100 || st.Active[0].Attempt != 1 {
		t.Fatalf("active = %+v", st.Active)
	}
	// 2000 bytes in the two seconds between the samples.
	if st.Totals.RateBPS != 1000 || st.Totals.BytesDone != 2100 || st.Totals.BytesTotal != 10_000 || st.Totals.ETAS != 7 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	step <- struct{}{}
	end := r.waitEnd(id)
	if end.Totals.BytesDone != 10_000 || end.Totals.RateBPS != 0 || end.Totals.ETAS != 0 || len(end.Active) != 0 {
		t.Fatalf("final totals = %+v", end.Totals)
	}
}

func TestEndOfRunPassesRunInOrderAndAHookCannotBreakTheRun(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	ret := &fakeRetention{note: note}
	r.deps.Retention = ret
	r.deps.Unknown = func(ark.Root) ([]string, error) { return nil, nil }
	ready := &fakeReady{note: note}
	r.deps.Ready = ready
	r.deps.Hooks = Hooks{
		Reverify: func(ctx context.Context, rc RunContext, budget time.Duration) { note("reverify"); ret.budget = budget },
		Extras:   func(ctx context.Context, rc RunContext) { note("extras"); panic("a hook that panics") },
		Report:   func(ctx context.Context, rc RunContext, st RunStatus) { note("report:" + st.State) },
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	st := r.waitEnd(id)
	if st.State != StateDone || st.Ready["internal"] != "green" {
		t.Fatalf("status = %+v", st)
	}
	want := []string{"ready:internal", "retention:internal:14:7", "reverify", "ready:internal", "extras", "report:done"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if ret.budget != 90*time.Minute {
		t.Fatalf("reverify budget = %v", ret.budget)
	}
	if d := r.evs.done(); len(d) != 1 || d[0].Ready["internal"] != "green" {
		t.Fatalf("sync.done = %+v", d)
	}
	// A cancelled run skips the re-verification and the extras, but still writes READY and reports.
	order = nil
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result { <-ctx.Done(); return cancelled(job) }
	id = r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 1)
	r.eng.Cancel()
	r.waitEnd(id)
	for _, s := range order {
		if s == "reverify" || s == "extras" {
			t.Fatalf("a cancelled run ran %s: %v", s, order)
		}
	}
}

type fakeRetention struct {
	note   func(string)
	budget time.Duration
}

func (f *fakeRetention) PurgeExpired(ctx context.Context, rootID string, trashDays, quarantineDays int) (retention.Expired, error) {
	f.note("retention:" + rootID + ":" + itoa(trashDays) + ":" + itoa(quarantineDays))
	return retention.Expired{}, nil
}

func (f *fakeRetention) PurgeUnknownStaging(unknown func(root ark.Root) ([]string, error), rootID string) (int, error) {
	return 0, nil
}

type fakeReady struct{ note func(string) }

func (f *fakeReady) Refresh(ctx context.Context, rootIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range rootIDs {
		f.note("ready:" + id)
		out[id] = "green"
	}
	return out, nil
}

func TestPlanFailureFailsTheRunWithItsCode(t *testing.T) {
	r := newRig(t)
	r.plnr.err = &plan.ErrUnknownProfile{Profile: "ghost", Root: "internal"}
	r.start()
	st := r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if st.State != StateFailed || st.Code != plan.CodeUnknownProfile || st.Result != ResultFailed {
		t.Fatalf("status = %+v", st)
	}
	r.plnr.err = errors.New("disk on fire")
	st = r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if st.State != StateFailed || st.Code != doctor.AGT099 {
		t.Fatalf("status = %+v", st)
	}
}

func TestCatalogRefreshFailureIsNotFatal(t *testing.T) {
	r := newRig(t)
	r.deps.Catalog = refresherFunc(func(context.Context) error { return errors.New("no source") })
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	r.start()
	st := r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if st.State != StateDone || st.Totals.Done != 1 || len(st.Warnings) != 1 {
		t.Fatalf("status = %+v", st)
	}
	// Offline: the refresh is not even tried.
	called := false
	r.deps.Catalog = refresherFunc(func(context.Context) error { called = true; return nil })
	r.net = NetState{Mode: plan.ModeOnline, Online: false}
	r.start()
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if called {
		t.Fatal("the catalog was refreshed without the network")
	}
	if !r.plnr.lastRequest().Offline {
		t.Fatal("the plan was not made offline")
	}
}

type refresherFunc func(context.Context) error

func (f refresherFunc) Refresh(ctx context.Context) error { return f(ctx) }

func TestUnplaceableAndUnknownItemsAreSkippedAsUnresolved(t *testing.T) {
	r := newRig(t)
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("ok", 1), row("kit-item", 1), row("ghost", 1)}})
	r.plnr.items["kit-item"] = plan.CatalogItem{ID: "kit-item", Kind: "kit"}
	delete(r.plnr.items, "ghost")
	r.start()
	st := r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if st.Totals.Done != 1 || st.Totals.Skipped != 2 || st.Totals.Items != 3 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	for _, it := range st.Items {
		if it.Result == "skipped" && it.Reason != string(plan.ReasonUnresolved) {
			t.Fatalf("item = %+v", it)
		}
	}
}

func TestPlannerSkipRowsAndTheItemsFilter(t *testing.T) {
	r := newRig(t)
	budget := plan.ItemPlan{ID: "big", Kind: "model", Action: plan.ActionSkip, Reason: plan.ReasonBudget, ToVersion: "v1"}
	disk := plan.ItemPlan{ID: "huge", Kind: "model", Action: plan.ActionSkip, Reason: plan.ReasonDisk}
	keep := plan.ItemPlan{ID: "have", Kind: "model", Action: plan.ActionKeep}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1), budget, disk, keep}})
	r.start()
	st := r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if st.Totals.Items != 4 || st.Totals.Done != 2 || st.Totals.Skipped != 2 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	codes := map[string]string{}
	for _, d := range r.evs.itemDone() {
		codes[d.Item] = d.Reason + "/" + d.Code
	}
	if codes["big"] != "budget/"+doctor.SYN041 || codes["huge"] != "disk/"+doctor.SYN040 {
		t.Fatalf("skip codes = %v", codes)
	}

	// With an items filter only those rows count, and only they run.
	r.mach.mu.Lock()
	r.mach.started = nil
	r.mach.mu.Unlock()
	st = r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual, Items: []string{"b"}}))
	if got := r.mach.order(); !reflect.DeepEqual(got, []string{"internal/b"}) || st.Totals.Items != 1 {
		t.Fatalf("ran %v, totals %+v", got, st.Totals)
	}
}

func TestLastRunSurvivesARestart(t *testing.T) {
	r := newRig(t)
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 5)}})
	r.start()
	id := r.startRun(StartRequest{Trigger: TriggerCloud})
	st := r.waitEnd(id)

	e2, err := New(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := e2.Status()
	if !ok || got.RunID != id || got.Trigger != TriggerCloud || got.State != StateDone || got.Totals.Done != 1 || got.FinishedAt == nil {
		t.Fatalf("after a restart: %+v", got)
	}
	if !got.StartedAt.Equal(st.StartedAt) {
		t.Fatalf("started_at %v != %v", got.StartedAt, st.StartedAt)
	}
}

func TestStatusOfARunThatWasRunningWhenTheAgentDied(t *testing.T) {
	r := newRig(t)
	eng := r.start()
	id, _ := journal.NewRunID(epoch, nil)
	data := `{"run_id":"` + id + `","trigger":"manual","state":"running","started_at":"2026-11-02T18:04:11Z","finished_at":null,"roots":["internal"],"totals":{},"active":[]}`
	if err := writeFile(r.deps.JournalDir+"/"+LastRunName, data); err != nil {
		t.Fatal(err)
	}
	e2, _ := New(r.deps)
	got, ok := e2.Status()
	if !ok || got.State != StateFailed || got.Result != ResultFailed {
		t.Fatalf("status = %+v", got)
	}
	_ = eng
}

func TestResumeFinishesInterruptedSwaps(t *testing.T) {
	r := newRig(t)
	ready := &fakeReady{note: func(string) {}}
	r.deps.Ready = ready
	r.mach.recoverFn = func(in journal.Intent) (item.Result, bool) {
		return item.Result{Root: in.RootID, Item: in.ItemID, Version: in.Version, State: journal.StateVerified}, true
	}
	eng := r.start()
	rec := journal.Recovery{Intents: map[string]journal.Intent{
		"internal/m1":      {RootID: "internal", ItemID: "m1", Version: "v1", Swap: &journal.SwapPlan{}},
		"internal/no-swap": {RootID: "internal", ItemID: "no-swap", Version: "v1", State: journal.StateDownloading},
	}}
	got := eng.Resume(bg, rec)
	if len(got) != 1 || got[0].Item != "m1" || got[0].State != journal.StateVerified {
		t.Fatalf("results = %+v", got)
	}
	if len(r.mach.recovered) != 1 || r.mach.recovered[0].ItemID != "m1" {
		t.Fatalf("recovered = %+v", r.mach.recovered)
	}
	if d := r.evs.itemDone(); len(d) != 1 || d[0].Item != "m1" || d[0].Result != "verified" {
		t.Fatalf("item_done = %+v", d)
	}
	if eng.Resume(bg, journal.Recovery{}) != nil {
		t.Fatal("resume of nothing returned something")
	}
}

func TestScheduleCatchupStartsAModeOnlineRunWhenDue(t *testing.T) {
	r := newRig(t)
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("m1", 1)}})
	eng := r.start()
	eng.ScheduleCatchup(5 * time.Minute)
	eng.ScheduleCatchup(10 * time.Minute) // the second call replaces the first
	r.clk.Advance(6 * time.Minute)
	if eng.RunActive() || len(r.mach.order()) != 0 {
		t.Fatal("the replaced timer fired")
	}
	r.clk.Advance(5 * time.Minute)
	st, ok := eng.Status()
	if !ok || st.Trigger != TriggerModeOnline {
		t.Fatalf("status = %+v", st)
	}
	r.waitEnd(st.RunID)
	// A node that went offline in the meantime skips it (the mode check is preflight's).
	r.net = NetState{Mode: plan.ModeOffline, Online: true}
	eng.ScheduleCatchup(time.Minute)
	r.clk.Advance(2 * time.Minute)
	st, _ = eng.Status()
	if st.State != StateSkipped || st.Reason != SkipMode {
		t.Fatalf("catch-up in offline mode: %+v", st)
	}
}

func TestWorkersGetTheirPriority(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var got []bool
	r.deps.Priority = func(scheduled bool) error {
		mu.Lock()
		got = append(got, scheduled)
		mu.Unlock()
		return errors.New("refused")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 1), row("b", 1)}})
	r.start()
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerScheduled}))
	r.waitEnd(r.startRun(StartRequest{Trigger: TriggerManual}))
	if !reflect.DeepEqual(got, []bool{true, true, false, false}) {
		t.Fatalf("priority calls = %v (a failure to set it is never fatal)", got)
	}
}

func TestPlanSummary(t *testing.T) {
	r := newRig(t)
	budget := plan.ItemPlan{ID: "big", Kind: "model", Action: plan.ActionSkip, Reason: plan.ReasonBudget}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("a", 100), row("b", 250), budget}})
	eng := r.start()
	sum, err := eng.PlanSummary(bg, nil, nil)
	if err != nil || sum != (PlanSummary{ItemsToDownload: 2, BytesToDownload: 350, ItemsSkippedBudget: 1}) {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
	if sum, _ = eng.PlanSummary(bg, nil, []string{"b"}); sum.ItemsToDownload != 1 || sum.BytesToDownload != 250 {
		t.Fatalf("filtered summary = %+v", sum)
	}
	if _, err = eng.PlanSummary(bg, []string{"ark-ffffffff"}, nil); err == nil {
		t.Fatal("unknown root accepted")
	}
	// A dry run plans offline in survival mode and writes nothing.
	r.net = NetState{Mode: plan.ModeSurvival}
	if _, err = eng.PlanSummary(bg, nil, nil); err != nil || !r.plnr.lastRequest().Offline {
		t.Fatalf("survival summary: %v, offline = %v", err, r.plnr.lastRequest().Offline)
	}
	if eng.RunActive() {
		t.Fatal("a summary took the run slot")
	}
}

var _ = events.TypeSyncDone

func TestADriveThatGoesAwayCancelsOnlyItsOwnWork(t *testing.T) {
	r := newRig(t)
	r.set.Concurrency, r.set.PerRootWriters = 4, 1
	r.addRoot(ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: r.dir + "/drive", State: ark.StateReady})
	hold := newHold()
	r.mach.beh = func(ctx context.Context, job item.Job) item.Result {
		if !hold.waitOrCancel(ctx, job.Root.ID+"/"+job.Ref.ID) {
			return cancelled(job)
		}
		return verified(job, "v1")
	}
	r.setPlans(map[string][]plan.ItemPlan{"internal": {row("i1", 1), row("i2", 1)}, driveID: {row("d1", 1), row("d2", 1)}})
	eng := r.start()
	if eng.RootGone(driveID) {
		t.Fatal("RootGone without a run reported success")
	}
	id := r.startRun(StartRequest{Trigger: TriggerManual})
	recvN(t, r.mach.startedCh, 2) // i1 and d1
	if eng.RootGone("ark-ffffffff") {
		t.Fatal("RootGone of a root the run does not use reported success")
	}
	if !eng.RootGone(driveID) {
		t.Fatal("RootGone did not find the root")
	}
	// The drive's work stops and its second item never starts; internal goes on.
	hold.release("internal/i1")
	if got := recvN(t, r.mach.startedCh, 1); got[0] != "internal/i2" {
		t.Fatalf("then started %v", got)
	}
	hold.release("internal/i2")
	st := r.waitEnd(id)
	if st.State != StateDone || st.Totals.Done != 2 || st.Totals.Skipped != 2 || st.Totals.Items != 4 {
		t.Fatalf("status = %+v", st)
	}
	for _, it := range st.Items {
		if it.Root == driveID && (it.Result != "skipped" || it.Reason != SkipRootGone || it.Code != doctor.ARK007) {
			t.Fatalf("drive item = %+v", it)
		}
	}
	if got := r.mach.order(); len(got) != 3 {
		t.Fatalf("started = %v: the drive's second item must not start", got)
	}
}

func TestSettingsValidatorIsAConfigBlockValidator(t *testing.T) {
	var v SettingsValidator
	if probs := v.ValidateBlock(nil); len(probs) != 0 {
		t.Fatalf("problems = %v", probs)
	}
	if probs := v.ValidateBlock(config.RawBlock("concurrency: 9")); len(probs) != 1 || !probs.HasErrors() {
		t.Fatalf("problems = %v", probs)
	}
}
