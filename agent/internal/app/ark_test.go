package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The ark wiring (#266): the sync engine, the readiness service, the drive events, the kit report, the
// node.yaml block validators and the whisper check are built by the real daemon over the rig's fakes.
// Nothing here sleeps: a test waits on the engine's own Wait, on the callback the daemon calls after it
// handled an event, or calls the synchronous entry point itself.

// syncRun starts a manual run of one item that is already on the node (so there is nothing to
// download and the offline downloader is never asked) and waits until the engine has ended it.
func syncRun(t *testing.T, r *rig, item string) string {
	t.Helper()
	got := r.do("POST", "/ark/sync", map[string]any{"items": []string{item}})
	if got.status != http.StatusAccepted {
		t.Fatalf("POST /ark/sync = %d %s", got.status, got.body)
	}
	id, _ := got.json()["run_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.d.Components().SyncEngine.Wait(ctx, id); err != nil {
		t.Fatalf("the run %s did not end: %v\n%s", id, err, r.logText())
	}
	return id
}

// A started agent serves the ark routes with the real engine behind them: GET /ark/sync is not 503, it
// is the spec's 404 before the first run and the run once there was one; GET /ark answers the report
// of the internal root.
func TestStartedAgentServesTheArkRoutes(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	c := d.Components()
	if c.SyncEngine == nil || c.Ready == nil || c.ArkIndex == nil {
		t.Fatalf("the ark stack was not built: engine %v, ready %v, index %v\n%s", c.SyncEngine, c.Ready, c.ArkIndex, r.logText())
	}

	// No run yet: 404 LAI-AGT-021 (agent.md section 3.8.18), which is an answer, not the 503 of a stub.
	if got := r.do("GET", "/ark/sync", nil); got.status != http.StatusNotFound || errCode(got) != doctor.AGT021 {
		t.Errorf("GET /ark/sync before a run = %d %s, want 404 %s", got.status, got.body, doctor.AGT021)
	}
	if got := r.do("DELETE", "/ark/sync", nil); got.status != http.StatusAccepted || got.json()["state"] != "idle" {
		t.Errorf("DELETE /ark/sync with nothing running = %d %s, want 202 idle", got.status, got.body)
	}

	id := syncRun(t, r, modelGamma)
	got := r.do("GET", "/ark/sync", nil)
	body := got.json()
	if got.status != http.StatusOK || body["run_id"] != id || body["state"] != "done" || body["trigger"] != "manual" {
		t.Errorf("GET /ark/sync after a run = %d %s, want 200 with the run %s done", got.status, got.body, id)
	}
	if body["finished_at"] == nil {
		t.Errorf("the finished run has no finished_at: %s", got.body)
	}

	// GET /ark: one report per mounted root, regenerated now (the worker's own first pass may or may
	// not have run: Regenerate is serialized, so after it returns the report is there).
	if err := c.Ready.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	ark := r.do("GET", "/ark", nil)
	roots, _ := ark.json()["roots"].([]any)
	if ark.status != http.StatusOK || len(roots) != 1 {
		t.Fatalf("GET /ark = %d %s, want the report of the internal root", ark.status, ark.body)
	}
	if root, _ := roots[0].(map[string]any)["root"].(map[string]any); root["id"] != "internal" {
		t.Errorf("the report is of %v, want the internal root", root)
	}
	if _, err := os.Stat(r.path("srv/localai/READY.json")); err != nil {
		t.Errorf("READY.json was not written on the internal root: %v", err)
	}
	// /status shows the engine's state and the root through the same service.
	if st := r.do("GET", "/status", nil).json(); st["ark"] == nil {
		t.Error("GET /status has no ark block")
	}
	// The speech routes are served (no voice is installed on the rig).
	if got := r.do("GET", "/speech/voices", nil); got.status != http.StatusOK {
		t.Errorf("GET /speech/voices = %d %s", got.status, got.body)
	}
}

// The sync block of node.yaml is validated by the engine's validator: concurrency 9 is out of range, so
// the file is refused (LAI-AGT-002) and the agent runs on the safe defaults; a valid block is accepted.
func TestSyncBlockIsValidatedByTheEngine(t *testing.T) {
	bad := newRig(t, func(r *rig) { r.writeNodeYAMLText(r.nodeYAML() + "sync:\n  concurrency: 9\n") })
	bad.start()
	bad.waitStarted()
	if st := bad.d.Components().Config.Status(); st.Code != doctor.AGT002 {
		t.Errorf("node.yaml with sync.concurrency 9: status %q %+v, want %s", st.Code, st.Problems, doctor.AGT002)
	} else if !strings.Contains(joinProblems(st), "sync") {
		t.Errorf("the problem does not name the sync block: %+v", st.Problems)
	}

	good := newRig(t, func(r *rig) {
		r.writeNodeYAMLText(r.nodeYAML() + "sync:\n  concurrency: 3\n  bandwidth_limit: 20MiB/s\n")
	})
	good.start()
	good.waitStarted()
	if st := good.d.Components().Config.Status(); st.Code != "" {
		t.Errorf("a valid sync block was refused: %q %+v", st.Code, st.Problems)
	}
	if got := good.d.syncSettings(); got.Concurrency != 3 || got.BandwidthLimit != "20MiB/s" {
		t.Errorf("the engine reads %+v", got)
	}
	// The shared bandwidth bucket follows node.yaml.
	if rate := good.d.ark.bucket.Rate(); rate != 20<<20 {
		t.Errorf("the bucket rate = %d, want 20 MiB/s", rate)
	}
}

func joinProblems(st config.Status) string {
	var parts []string
	for _, p := range st.Problems {
		parts = append(parts, p.Path+" "+p.Message)
	}
	return strings.Join(parts, "; ")
}

// An unmount event reaches the sync engine's RootGone and the readiness service, a mount event reads
// the drive into the index; the daemon tells the test after it handled each, so nothing sleeps.
func TestDriveEventsReachTheEngineAndTheReadinessService(t *testing.T) {
	handled := make(chan string, 4)
	r := newRig(t, withEnv(func(e *Env) {
		e.OnDriveEvent = func(typ, root string) { handled <- typ + " " + root }
	}))
	d := r.start()
	r.waitStarted()

	for _, tc := range []struct{ typ, want string }{
		{events.TypeDriveMounted, "drive.mounted ark-0a1b2c3d"},
		{events.TypeDriveUnmounted, "drive.unmounted ark-0a1b2c3d"},
	} {
		publishDriveEvent(t, d, tc.typ)
		select {
		case got := <-handled:
			if got != tc.want {
				t.Fatalf("handled %q, want %q", got, tc.want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("the daemon did not handle %s\n%s", tc.typ, r.logText())
		}
	}
	// The unmount was applied by the real wiring: the engine was asked (no run was using the drive)
	// and the log says which root is gone.
	if !r.logs.has("a content root is gone", map[string]any{"root": "ark-0a1b2c3d", "code": doctor.ARK007, "run_was_using_it": false}) {
		t.Errorf("the unmount is not logged:\n%s", r.logText())
	}
}

func publishDriveEvent(t *testing.T, d *Daemon, typ string) {
	t.Helper()
	var payload any
	switch typ {
	case events.TypeDriveMounted:
		payload = events.DriveMounted{RootID: "ark-0a1b2c3d", Mount: "/media/x", State: "ready"}
	case events.TypeDriveUnmounted:
		payload = events.DriveUnmounted{RootID: "ark-0a1b2c3d"}
	}
	if err := d.Components().Bus.Publish(typ, "", payload); err != nil {
		t.Fatal(err)
	}
}

type recGoner struct {
	mu    sync.Mutex
	roots []string
	ret   bool
}

func (g *recGoner) RootGone(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.roots = append(g.roots, id)
	return g.ret
}

type recReady struct{ mounted, unmounted []string }

func (r *recReady) Mounted(_ context.Context, id string) error {
	r.mounted = append(r.mounted, id)
	return nil
}
func (r *recReady) Unmounted(id string) { r.unmounted = append(r.unmounted, id) }

type recIndex struct{ calls [][]string }

func (x *recIndex) Rebuild(_ context.Context, roots []ark.Root) ([]ark.Problem, error) {
	var ids []string
	for _, r := range roots {
		ids = append(ids, r.ID)
	}
	x.calls = append(x.calls, ids)
	return nil, nil
}

// The handler in isolation: RootGone is called for the unmounted root before the index is rebuilt
// without it, and the readiness service forgets the report; a mount rebuilds the index first and then
// asks for the report; an event of another type does nothing.
func TestDriveEventsHandler(t *testing.T) {
	gone, rdy, idx := &recGoner{ret: true}, &recReady{}, &recIndex{}
	var seen []string
	h := driveEvents{
		engine: gone, ready: rdy, index: idx, roots: func() []ark.Root { return []ark.Root{{ID: ark.InternalID}} },
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		observe: func(typ, id string) { seen = append(seen, typ+" "+id) },
	}
	h.handle(context.Background(), events.TypeDriveUnmounted, "ark-aaaa0001")
	h.handle(context.Background(), events.TypeDriveMounted, "ark-aaaa0002")
	h.handle(context.Background(), events.TypeSyncDone, "ignored")
	if len(gone.roots) != 1 || gone.roots[0] != "ark-aaaa0001" {
		t.Errorf("RootGone calls = %v", gone.roots)
	}
	if len(rdy.unmounted) != 1 || rdy.unmounted[0] != "ark-aaaa0001" || len(rdy.mounted) != 1 || rdy.mounted[0] != "ark-aaaa0002" {
		t.Errorf("readiness: unmounted %v, mounted %v", rdy.unmounted, rdy.mounted)
	}
	if len(idx.calls) != 2 {
		t.Errorf("the index was rebuilt %d times, want once per drive event", len(idx.calls))
	}
	if len(seen) != 2 {
		t.Errorf("observed %v, want the two drive events only", seen)
	}
}

// The whisper doctor check is registered next to the kiwix one and raises LAI-SVC-012 when whisper is
// enabled and no build is installed; with whisper off it says nothing.
func TestWhisperDoctorCheckIsRegistered(t *testing.T) {
	r := newRig(t, func(r *rig) { r.writeNodeYAMLText(r.nodeYAML() + "whisper:\n  enabled: true\n") })
	d := r.start()
	r.waitStarted()
	found := false
	for _, ch := range d.Components().Checks.Checks() {
		if ch.ID == whisper.CheckBuilt {
			found = true
		}
	}
	if !found {
		t.Fatalf("the check %s is not registered", whisper.CheckBuilt)
	}
	d.Components().Doctor.Tick(context.Background())
	if !hasAlert(d, doctor.SVC012) {
		t.Errorf("whisper is enabled and not built, but %s is not raised", doctor.SVC012)
	}

	off := newRig(t)
	od := off.start()
	off.waitStarted()
	od.Components().Doctor.Tick(context.Background())
	if hasAlert(od, doctor.SVC012) {
		t.Errorf("%s is raised while whisper is off", doctor.SVC012)
	}
}

// A swap that was in flight when the agent died is finished at start: the run log of the dead process
// names the swap, the journal replay hands it to the engine, and Engine.Resume (which runs after
// journal.Open and the index) takes the item to verified. Here the swap had committed its manifest but
// not logged swap_done.
func TestCrashedSwapIsResumedAfterTheJournalOpens(t *testing.T) {
	const itemID, version = "model-crashed-1b-q4", "v0123456789a"
	body := []byte("weights of a model that was being swapped in")
	sum := sha256.Sum256(body)
	r := newRig(t, func(r *rig) {
		itemRel, err := ark.ItemDir(ark.ItemRef{Kind: "model", Engine: "llama", ID: itemID})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(r.path("srv/localai"), filepath.FromSlash(itemRel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.gguf"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
		m := manifest.Manifest{
			SchemaVersion: 1, ItemID: itemID, ItemKind: "model", Engine: "llama", CatalogVersion: "2026.11.02-1", Version: version,
			SortKey: version, State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
			Files:      []manifest.File{{Kind: "weights", Path: "a.gguf", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), SHA256Source: "catalog"}},
			TotalBytes: int64(len(body)), DownloadedAt: at, VerifiedAt: at, LastReverifiedAt: at, Trust: manifest.TrustPinned,
			SyncedBy: manifest.SyncedBy{NodeID: rigNode, AgentVersion: "0.1.0-test", RunID: "run-20261102T180000Z-0001"},
		}
		if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
			t.Fatal(err)
		}
		// The log of the process that died: the swap was planned (its last rename, the manifest, had
		// happened) and swap_done was never written.
		staging := filepath.Join(r.path("srv/localai"), ".staging", itemID, version)
		runs := journal.NewRuns(journal.Options{Dir: r.path("var/lib/localai/sync"), Now: func() time.Time { return at }})
		run, err := runs.Start()
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range []journal.Event{
			{Ev: journal.EvState, Root: ark.InternalID, Item: itemID, Version: version, From: journal.StateStaged, To: journal.StateSwapping},
			{Ev: journal.EvSwapPlan, Root: ark.InternalID, Item: itemID, Version: version, Renames: [][2]string{
				{filepath.Join(staging, "a.gguf"), filepath.Join(dir, "a.gguf")},
				{filepath.Join(staging, manifest.FileName), filepath.Join(dir, manifest.FileName)},
			}},
		} {
			if err := run.Append(ev); err != nil {
				t.Fatal(err)
			}
		}
		if err := run.Close(); err != nil {
			t.Fatal(err)
		}
	})
	d := r.start()
	r.waitStarted()
	if !r.logs.has("a swap interrupted by a restart was finished", map[string]any{"root": ark.InternalID, "item": itemID, "version": version}) {
		t.Fatalf("the interrupted swap was not resumed:\n%s", r.logText())
	}
	rec, ok := d.ark.jr.Items.Get(ark.InternalID, itemID)
	if !ok || rec.State != journal.StateVerified || rec.Version != version {
		t.Errorf("the item record after the resume = %+v (found %v), want verified at %s", rec, ok, version)
	}
	if copies := d.Components().ArkIndex.Copies(itemID); len(copies) != 1 {
		t.Errorf("the best-copy index holds %d copies of the resumed item, want 1", len(copies))
	}
}

type recRefresher struct{ calls [][]string }

func (r *recRefresher) Refresh(_ context.Context, ids []string) (map[string]string, error) {
	r.calls = append(r.calls, append([]string(nil), ids...))
	return map[string]string{"a": "green"}, nil
}

// The engine's READY hook regenerates the kit index (kit/MANIFEST.json and kit/README.txt) of a root
// that has a kit folder before READY.json is written, leaves a root with no kit folder and a read-only
// root alone, and returns what the readiness service answered.
func TestKitIndexIsRegeneratedBeforeReady(t *testing.T) {
	base := t.TempDir()
	mk := func(name string, kitDir bool) ark.Root {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if kitDir {
			if err := os.MkdirAll(filepath.Join(p, "kit"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return ark.Root{ID: name, Kind: ark.KindArkDrive, Path: p, State: ark.StateReady}
	}
	a, b, c := mk("a", true), mk("b", false), mk("c", true)
	c.ReadOnly = true
	inner := &recRefresher{}
	k := kitRefreshing{
		inner: inner, roots: func() []ark.Root { return []ark.Root{a, b, c} },
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		refresher: kit.Refresher{FS: config.OSFS{}, Now: func() time.Time { return t0 }},
	}
	got, err := k.Refresh(context.Background(), []string{"a", "b", "c"})
	if err != nil || got["a"] != "green" || len(inner.calls) != 1 || len(inner.calls[0]) != 3 {
		t.Fatalf("Refresh = %v, %v (inner calls %v)", got, err, inner.calls)
	}
	for _, f := range []string{"MANIFEST.json", "README.txt"} {
		if _, err := os.Stat(filepath.Join(a.Path, "kit", f)); err != nil {
			t.Errorf("kit/%s of the root with a kit folder was not written: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(c.Path, "kit", f)); err == nil {
			t.Errorf("kit/%s was written on a read-only root", f)
		}
	}
	if _, err := os.Stat(filepath.Join(b.Path, "kit")); err == nil {
		t.Error("a kit folder was created on a root that had none")
	}
	// A root the run did not touch is not refreshed.
	if err := os.Remove(filepath.Join(a.Path, "kit", "MANIFEST.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Refresh(context.Background(), []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Path, "kit", "MANIFEST.json")); err == nil {
		t.Error("an untouched root was refreshed")
	}
}

// This node adds itself to kit/catalog/pins.json of a root it synced (rebuild-kit.md section 3.3): the
// commits come from the BUILD.json of the builds in use (node.yaml keeps 9 characters, a pin needs 40),
// the codename and architecture from os-release; a root with no kit/catalog is left alone and a damaged
// file is never rewritten.
func TestNodeAddsItselfToThePinsOfARoot(t *testing.T) {
	llamaSHA := strings.Repeat("ab", 20) // built at run time: no 40-hex literal in the source
	r := newRig(t, func(r *rig) {
		r.write("etc/os-release", "VERSION_CODENAME=resolute\n", 0o644)
		r.write("opt/localai/llama.cpp/current/BUILD.json", `{"commit":"`+llamaSHA+`","backend":"cpu"}`, 0o644)
		r.write("opt/localai/whisper.cpp/current/BUILD.json", `{"commit":"short"}`, 0o644) // not a pin: left out
	})
	d := r.start()
	r.waitStarted()
	own, ok := d.ark.facts.nodePins()
	if !ok {
		t.Fatal("no pins to report")
	}
	if got := own.LlamaCommits(rigNode); len(got) != 1 || got[0] != llamaSHA {
		t.Errorf("llama commits = %v, want the one of BUILD.json", got)
	}
	if got := own.WhisperCommits(rigNode); len(got) != 0 {
		t.Errorf("whisper commits = %v, a short commit is not a pin", got)
	}
	if got := own.AgentTags(rigNode); len(got) != 1 || got[0] != "node-v0.1.0-test" {
		t.Errorf("agent tags = %v", got)
	}
	if len(own.Codenames) != 1 || own.Codenames[0].Codename != "resolute" {
		t.Errorf("codenames = %+v", own.Codenames)
	}

	base := t.TempDir()
	mk := func(name string, catalogDir bool) ark.Root {
		dir := filepath.Join(base, name, "kit")
		if catalogDir {
			dir = filepath.Join(dir, "catalog")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return ark.Root{ID: name, Kind: ark.KindArkDrive, Path: filepath.Join(base, name), State: ark.StateReady}
	}
	withCatalog, without, damaged := mk("a", true), mk("b", false), mk("c", true)
	badFile := filepath.Join(damaged.Path, "kit", "catalog", "pins.json")
	if err := os.WriteFile(badFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := kitRefreshing{
		inner: &recRefresher{}, roots: func() []ark.Root { return []ark.Root{withCatalog, without, damaged} },
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		refresher: kit.Refresher{FS: config.OSFS{}, Now: func() time.Time { return t0 }},
		pins:      func() (kit.Pins, bool) { return own, true },
	}
	if _, err := k.Refresh(context.Background(), []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	got, err := kit.ReadPins(config.OSFS{}, filepath.Join(withCatalog.Path, "kit", "catalog", "pins.json"))
	if err != nil || len(got.LlamaCommits(rigNode)) != 1 {
		t.Errorf("pins of the root with kit/catalog = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(without.Path, "kit", "catalog")); err == nil {
		t.Error("kit/catalog was created on a root that had none")
	}
	if b, _ := os.ReadFile(badFile); string(b) != "{not json" {
		t.Errorf("a damaged pins.json was rewritten: %q", b)
	}
}

// The facts the kit completeness rules read come from node.yaml, os-release and the architecture, the
// same as `aictl kit status` reads (kitReadFacts in internal/cli).
func TestKitNodeFacts(t *testing.T) {
	r := newRig(t, func(r *rig) {
		r.write("etc/os-release", "NAME=\"Ubuntu\"\nVERSION_CODENAME=resolute\n", 0o644)
		r.writeNodeYAMLText(r.nodeYAML() + "whisper:\n  enabled: true\n  commit: 1a2b3c4d5\n")
	})
	d := r.start()
	r.waitStarted()
	f := d.ark.facts.kitNode()
	if f == nil {
		t.Fatal("no facts")
	}
	if f.NodeID != rigNode || f.Codename != "resolute" || f.Arch == "" || f.AgentVersion != "0.1.0-test" || f.WhisperCommit != "1a2b3c4d5" {
		t.Errorf("facts = %+v", f)
	}
	if cn, _ := d.ark.facts.release(); cn != "resolute" {
		t.Errorf("release = %q", cn)
	}
}
