package adapt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/importer"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/retention"
	"github.com/marinoscar/local-ai/agent/internal/sync/run"
	"github.com/marinoscar/local-ai/agent/internal/sync/verify"
)

// These tests run the real engine, item machine, downloader, journal, best-copy index, retention,
// planner and re-verification over temp directories. The HTTP client of every downloader dials through
// importer.RefuseDial(true): a code path that reaches for the network panics the test.

type e2eBus struct {
	mu  sync.Mutex
	evs []events.SyncItemDone
}

func (b *e2eBus) Publish(typ, _ string, data any) error {
	if d, ok := data.(events.SyncItemDone); ok {
		b.mu.Lock()
		b.evs = append(b.evs, d)
		b.mu.Unlock()
	}
	return nil
}

func (b *e2eBus) itemDone() []events.SyncItemDone {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]events.SyncItemDone(nil), b.evs...)
}

type e2eProfile struct {
	models, content []string
}

func (p *e2eProfile) AddItems(models, content []string) error {
	p.models, p.content = append(p.models, models...), append(p.content, content...)
	return nil
}

type e2eCatalog struct{ installed []string }

func (c *e2eCatalog) Version() string { return "2026.11.02-1" }
func (c *e2eCatalog) Install(_ context.Context, dir string) (string, error) {
	c.installed = append(c.installed, dir)
	return "2026.12.01-1", nil
}

type e2e struct {
	t        *testing.T
	base     string
	internal ark.Root
	drive    ark.Root
	roots    []ark.Root
	idx      *ark.Index
	jr       *journal.Journal
	ret      *retention.Retention
	eng      *run.Engine
	bus      *e2eBus
	profile  *e2eProfile
	cat      *e2eCatalog
	imp      Importer
	rv       *verify.Reverifier
	data     map[string][]byte
	mode     string
	faults   []string
	catalog  *catalog.Catalog
	planner  *importer.Planner
	gatherer *plan.Gatherer
}

func blob(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func hexsha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const rev = "0123456789abcdef0123456789abcdef01234567"

func newE2E(t *testing.T) *e2e {
	t.Helper()
	x := &e2e{t: t, base: t.TempDir(), bus: &e2eBus{}, profile: &e2eProfile{}, cat: &e2eCatalog{}, mode: plan.ModeSurvival,
		data: map[string][]byte{"w1": blob(300_000, 1), "w2": blob(120_000, 2), "mm": blob(900, 3), "extra": blob(5_000, 4)}}
	x.internal = ark.Root{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(x.base, "srv"), State: ark.StateReady}
	x.drive = ark.Root{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(x.base, "drive", "localai"), Mount: filepath.Join(x.base, "drive"), State: ark.StateReady, MountID: 7}
	x.roots = []ark.Root{x.internal, x.drive}
	for _, r := range x.roots {
		if err := os.MkdirAll(r.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rootList := func() []ark.Root { return x.roots }

	var err error
	x.jr, _, err = journal.Open(bg, journal.Options{Dir: filepath.Join(x.base, "var"), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	x.idx = ark.NewIndex(config.OSFS{})
	x.ret, err = retention.New(retention.Deps{FS: retention.OSFS{}, Roots: rootList, Copies: x.idx, Journal: x.jr.Items, Index: x.idx, Items: x.jr.Items, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	dl := download.New(download.Options{
		HTTP:  download.NewHTTPClient(download.ClientConfig{DialContext: importer.RefuseDial(true)}),
		Space: x.ret, Logger: quiet,
	})
	machine, err := item.New(item.Deps{FS: item.OSFS{}, Journal: x.jr, Fetcher: dl, Index: x.idx, Remover: x.ret, Roots: rootList, NodeID: "ai-test", AgentVersion: "0.0.0", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}

	models := []catalog.Model{
		x.model("m-full", []string{"w1", "mm"}), x.model("m-small", []string{"w2"}), x.model("m-extra", []string{"extra"}), x.model("m-absent", []string{"w2"}),
	}
	prof := catalog.Profile{ID: "int", Storage: catalog.Storage{Target: "internal", BudgetBytes: 100_000_000, MinFreeBytes: 1000},
		Include: catalog.Include{Models: []string{"m-full", "m-small", "m-absent"}}}
	x.catalog = catalog.New("2026.11.02-1", models, nil, []catalog.Profile{prof})
	x.gatherer, err = plan.NewGatherer(plan.Deps{
		FS: config.OSFS{}, Catalog: func() *catalog.Catalog { return x.catalog },
		Settings: func() plan.NodeSettings { return plan.NodeSettings{Profile: "int"} },
		Roots:    rootList, Copies: x.idx, Journal: x.jr.Items,
		Statfs:  func(string) (int64, int64, error) { return 10_000_000_000, 5_000_000_000, nil },
		Node:    func() plan.NodeInfo { return plan.NodeInfo{Backend: "cpu", Arch: "amd64"} },
		Network: func() plan.Network { return plan.Network{Mode: x.mode, Online: false} },
		Logger:  quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	x.planner, err = importer.New(importer.Deps{G: x.gatherer, Catalog: x.cat, Profile: x.profile, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	x.rv, err = verify.New(verify.Deps{FS: verify.OSFS{}, Roots: rootList, Index: x.idx, Restore: x.ret, Journal: x.jr.Items, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	rev := Reverification{Reverifier: x.rv, Settings: run.DefaultSettings}
	x.eng, err = run.New(run.Deps{
		Journal: x.jr, Machine: machine, ImportMachine: machine, Planner: GathererPlanner{G: x.gatherer, Importer: x.planner}, Copies: x.idx,
		FS: config.OSFS{}, JournalDir: filepath.Join(x.base, "var"), Settings: run.DefaultSettings,
		Net:    func() run.NetState { return run.NetState{Mode: x.mode, Online: false} },
		Events: x.bus, Logger: quiet, Priority: func(bool) error { return nil }, Retention: x.ret,
		SourceFault: func(rootID, itemID, version string) {
			x.faults = append(x.faults, rootID+"/"+itemID+"/"+version)
			x.idx.Exclude(rootID, itemID, version)
		},
		Hooks: run.Hooks{Reverify: rev.Slice, Verify: rev.Run},
	})
	if err != nil {
		t.Fatal(err)
	}
	x.imp = Importer{Engine: x.eng, Planner: x.planner}
	return x
}

// model is a catalog model whose files are the named blobs, pinned by sha256 (a catalog pin).
func (x *e2e) model(id string, blobs []string) catalog.Model {
	m := catalog.Model{ID: id, Engine: "llama", SurvivalTier: 2, Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/" + id, Revision: rev}}
	for _, name := range blobs {
		m.Files = append(m.Files, catalog.ModelFile{Kind: "weights", Name: name + ".gguf", URL: "https://files.example.test/" + name + ".gguf", SizeBytes: int64(len(x.data[name])), SHA256: hexsha(x.data[name])})
	}
	return m
}

// put installs a verified copy of a model on a root: its files and a manifest, as a sync leaves them.
func (x *e2e) put(root ark.Root, id string, blobs []string) string {
	x.t.Helper()
	dir := filepath.Join(root.Path, "models", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		x.t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: "model", Engine: "llama", CatalogVersion: "2026.11.02-1", Version: rev[:12], SortKey: rev[:12], State: "verified",
		Source: manifest.Source{Type: "huggingface"}, Trust: manifest.TrustPinned,
		DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch,
	}
	for _, name := range blobs {
		if err := os.WriteFile(filepath.Join(dir, name+".gguf"), x.data[name], 0o644); err != nil {
			x.t.Fatal(err)
		}
		m.Files = append(m.Files, manifest.File{Kind: "weights", Path: name + ".gguf", SizeBytes: int64(len(x.data[name])), SHA256: hexsha(x.data[name]), SHA256Source: "catalog"})
		m.TotalBytes += int64(len(x.data[name]))
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		x.t.Fatal(err)
	}
	x.rebuild()
	return dir
}

func (x *e2e) rebuild() {
	if _, err := x.idx.Rebuild(bg, x.roots); err != nil {
		x.t.Fatal(err)
	}
}

func (x *e2e) wait(runID string) run.RunStatus {
	x.t.Helper()
	if err := x.eng.Wait(bg, runID); err != nil {
		x.t.Fatal(err)
	}
	st, ok := x.eng.Status()
	if !ok || st.RunID != runID || st.FinishedAt == nil {
		x.t.Fatalf("run %s did not end: %+v", runID, st)
	}
	return st
}

func (x *e2e) importRun(req api.ArkImportRequest) run.RunStatus {
	x.t.Helper()
	started, err := x.imp.StartImport(bg, req)
	if err != nil {
		x.t.Fatalf("StartImport: %v", err)
	}
	return x.wait(started.RunID)
}

func resultsByItem(st run.RunStatus) map[string]run.ItemResult {
	out := map[string]run.ItemResult{}
	for _, it := range st.Items {
		out[it.Item] = it
	}
	return out
}

// An import into an empty internal root copies what the drive holds, verified against the drive's
// MANIFEST and the catalog's pin, in survival mode, with the network refused by a transport that panics.
func TestImportIntoAnEmptyInternalRootNeedsNoNetworkInSurvivalMode(t *testing.T) {
	x := newE2E(t)
	driveDirFull := x.put(x.drive, "m-full", []string{"w1", "mm"})
	x.put(x.drive, "m-small", []string{"w2"})
	x.put(x.drive, "m-extra", []string{"extra"})
	before := dirSnapshot(t, x.drive.Path)

	st := x.importRun(api.ArkImportRequest{OpID: "op_1"})

	if st.Trigger != run.TriggerImport || st.State != run.StateDone || !reflect.DeepEqual(st.Roots, []string{"internal"}) {
		t.Fatalf("status = %+v", st)
	}
	by := resultsByItem(st)
	if by["m-full"].Result != "verified" || by["m-small"].Result != "verified" {
		t.Fatalf("items = %+v", st.Items)
	}
	// m-absent is in the profile and not on the drive: skipped as unresolved, not an error. m-extra is on
	// the drive and not in the profile, and nobody asked for it: it is not part of this import.
	if a := by["m-absent"]; a.Result != "skipped" || a.Reason != string(plan.ReasonUnresolved) {
		t.Fatalf("m-absent = %+v", a)
	}
	if _, ok := by["m-extra"]; ok {
		t.Fatal("an item the profile does not list was imported")
	}
	if st.Result != run.ResultOK && st.Result != run.ResultPartial {
		t.Fatalf("result = %s", st.Result)
	}

	// The bytes are on the internal disk, the manifest names the drive's version and checksums.
	for name, id := range map[string]string{"w1": "m-full", "mm": "m-full", "w2": "m-small"} {
		got, err := os.ReadFile(filepath.Join(x.internal.Path, "models", id, name+".gguf"))
		if err != nil || !bytes.Equal(got, x.data[name]) {
			t.Fatalf("%s/%s: %v", id, name, err)
		}
	}
	c, err := x.idx.BestCopy("m-full")
	if err != nil || c.RootID != ark.InternalID || c.Manifest.Version != rev[:12] || c.Manifest.Trust != manifest.TrustPinned || c.Manifest.SyncedBy.RunID != st.RunID {
		t.Fatalf("best copy = %+v, %v", c, err)
	}
	if rec, _ := x.jr.Items.Get(ark.InternalID, "m-full"); rec.State != journal.StateVerified {
		t.Fatalf("journal = %+v", rec)
	}
	if left, _ := os.ReadDir(filepath.Join(x.internal.Path, ".staging")); len(left) != 0 {
		t.Fatalf("staging is not clean: %v", left)
	}
	// The drive was only read.
	if after := dirSnapshot(t, x.drive.Path); !reflect.DeepEqual(before, after) {
		t.Fatal("an import wrote to the drive")
	}
	_ = driveDirFull
	// sync.item_done said so for the router and the companions.
	var done []string
	for _, e := range x.bus.itemDone() {
		done = append(done, e.Item+":"+e.Result)
	}
	if !contains(done, "m-full:verified") || !contains(done, "m-small:verified") || !contains(done, "m-absent:skipped") {
		t.Fatalf("item_done = %v", done)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func dirSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out = append(out, rel+"/")
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, rel+" "+hexsha(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A flipped byte on the drive: the copy no longer matches its own MANIFEST, so it is LAI-SYN-070, the
// item is not imported, the drive's copy is excluded in memory and nothing is written to the drive.
func TestImportOfADamagedDriveCopyIsSYN070AndNothingIsWrittenToTheDrive(t *testing.T) {
	x := newE2E(t)
	dir := x.put(x.drive, "m-small", []string{"w2"})
	b, _ := os.ReadFile(filepath.Join(dir, "w2.gguf"))
	b[777] ^= 0xFF
	if err := os.WriteFile(filepath.Join(dir, "w2.gguf"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	x.rebuild()
	before := dirSnapshot(t, x.drive.Path)

	st := x.importRun(api.ArkImportRequest{Items: []string{"m-small"}})
	it := resultsByItem(st)["m-small"]
	if it.Code != doctor.SYN070 || it.Result == "verified" {
		t.Fatalf("item = %+v", it)
	}
	if st.Totals.Failed != 1 || st.Result != run.ResultPartial {
		t.Fatalf("status = %+v", st)
	}
	if !reflect.DeepEqual(x.faults, []string{driveID + "/m-small/" + rev[:12]}) {
		t.Fatalf("faults = %v", x.faults)
	}
	if _, err := x.idx.BestCopy("m-small"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("a damaged drive copy is still offered, or something was installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(x.internal.Path, "models", "m-small", "MANIFEST.json")); err == nil {
		t.Fatal("a copy that failed its checksum was installed")
	}
	if after := dirSnapshot(t, x.drive.Path); !reflect.DeepEqual(before, after) {
		t.Fatal("an import wrote to the drive")
	}
}

func TestImportRefusesWhatTheProfileDoesNotListUnlessAddedToIt(t *testing.T) {
	x := newE2E(t)
	x.put(x.drive, "m-extra", []string{"extra"})

	st := x.importRun(api.ArkImportRequest{Items: []string{"m-extra"}})
	if it := resultsByItem(st)["m-extra"]; it.Result != "skipped" || it.Reason != string(plan.ReasonNotInProfile) {
		t.Fatalf("item = %+v", it)
	}
	if len(x.profile.models) != 0 {
		t.Fatal("node.yaml was changed")
	}
	if c, err := x.idx.BestCopy("m-extra"); err != nil || c.RootID != driveID {
		t.Fatalf("a refused item was copied to the internal root: %v", err)
	}

	st = x.importRun(api.ArkImportRequest{Items: []string{"m-extra"}, AddToProfile: true})
	if it := resultsByItem(st)["m-extra"]; it.Result != "verified" {
		t.Fatalf("item = %+v", it)
	}
	if !reflect.DeepEqual(x.profile.models, []string{"m-extra"}) {
		t.Fatalf("profile models = %v", x.profile.models)
	}
	if c, err := x.idx.BestCopy("m-extra"); err != nil || c.RootID != ark.InternalID {
		t.Fatalf("%v", err)
	}
}

func TestImportDryRunPlansFromTheDriveAndWritesNothing(t *testing.T) {
	x := newE2E(t)
	x.put(x.drive, "m-small", []string{"w2"})
	x.put(x.drive, "m-extra", []string{"extra"})
	before := dirSnapshot(t, x.base)

	got, err := x.imp.DryRunImport(bg, api.ArkImportRequest{Items: []string{"m-small", "m-extra"}, AddToProfile: true, Catalog: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	plans := got.([]plan.RootPlan)
	if len(plans) != 1 || plans[0].Root != ark.InternalID || !plans[0].Offline {
		t.Fatalf("plans = %+v", plans)
	}
	rows := map[string]plan.ItemPlan{}
	for _, r := range plans[0].Items {
		rows[r.ID] = r
	}
	if len(rows) != 2 || rows["m-small"].Source != plan.SourceLocalCopy || rows["m-small"].FromRoot != driveID || rows["m-extra"].Action != plan.ActionDownload {
		t.Fatalf("rows = %+v", rows)
	}
	if after := dirSnapshot(t, x.base); !reflect.DeepEqual(before, after) || len(x.profile.models) != 0 || len(x.cat.installed) != 0 {
		t.Fatal("a dry run wrote something")
	}
}

func TestImportWithSeveralDrivesAndNoSourceIsARK015WithTheirIds(t *testing.T) {
	x := newE2E(t)
	second := ark.Root{ID: "ark-0e0f1011", Kind: ark.KindArkDrive, Path: filepath.Join(x.base, "drive2", "localai"), State: ark.StateReady, MountID: 9}
	if err := os.MkdirAll(second.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	x.roots = append(x.roots, second)

	for name, call := range map[string]func() error{
		"dry run": func() error { _, err := x.imp.DryRunImport(bg, api.ArkImportRequest{DryRun: true}); return err },
		"start":   func() error { _, err := x.imp.StartImport(bg, api.ArkImportRequest{}); return err },
	} {
		err := call()
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != doctor.ARK015 || ae.Status != http.StatusConflict {
			t.Fatalf("%s: err = %v", name, err)
		}
		roots, _ := ae.Details["roots"].([]string)
		if !reflect.DeepEqual(roots, []string{driveID, "ark-0e0f1011"}) {
			t.Fatalf("%s: details = %v", name, ae.Details)
		}
	}
	if _, ok := x.eng.Status(); ok {
		t.Fatal("a run was started")
	}
	// Naming one is enough.
	x.put(x.drive, "m-small", []string{"w2"})
	if st := x.importRun(api.ArkImportRequest{FromRoot: driveID, Items: []string{"m-small"}}); resultsByItem(st)["m-small"].Result != "verified" {
		t.Fatalf("%+v", st.Items)
	}
}

func TestImportErrorsMapToTheRoutesAnswers(t *testing.T) {
	x := newE2E(t)
	for _, tc := range []struct {
		name   string
		req    api.ArkImportRequest
		status int
		code   string
		field  string
	}{
		{"no drive named and none mounted", api.ArkImportRequest{}, http.StatusBadRequest, api.CodeInvalidRequest, "from_root"},
		{"a drive that is not mounted", api.ArkImportRequest{FromRoot: "ark-ffffffff"}, http.StatusBadRequest, api.CodeInvalidRequest, "from_root"},
	} {
		x.roots = x.roots[:1] // internal only for the first, the drive back for the others
		if tc.name != "no drive named and none mounted" {
			x.roots = []ark.Root{x.internal, x.drive}
		}
		_, err := x.imp.StartImport(bg, tc.req)
		var ae *api.Error
		if !errors.As(err, &ae) || ae.Code != tc.code || ae.Status != tc.status || ae.Details["field"] != tc.field {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
	x.roots = []ark.Root{x.internal, x.drive}
	x.roots[1].State = ark.StateUnclean
	_, err := x.imp.StartImport(bg, api.ArkImportRequest{FromRoot: driveID})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != doctor.ARK004 || ae.Status != http.StatusConflict {
		t.Fatalf("an unclean drive: %v", err)
	}
	x.roots[1].State = ark.StateReady

	// A run is active: the slot is shared with every other run.
	x.put(x.drive, "m-small", []string{"w2"})
	first, err := x.imp.StartImport(bg, api.ArkImportRequest{Items: []string{"m-small"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.imp.StartImport(bg, api.ArkImportRequest{Items: []string{"m-small"}}); err != nil {
		if !errors.As(err, &ae) || ae.Code != doctor.SYN005 {
			t.Fatalf("a second import: %v", err)
		}
	}
	x.wait(first.RunID)
}

// A flipped byte on the internal disk: the verify run finds it (LAI-SYN-025), quarantines the copy,
// records it for READY and leaves the neighbour alone; a run is the last one that touched the root.
func TestVerifyRunQuarantinesABitFlipAndTheNextSyncHasSomethingToFetch(t *testing.T) {
	x := newE2E(t)
	dir := x.put(x.internal, "m-small", []string{"w2"})
	x.put(x.internal, "m-full", []string{"w1", "mm"})
	b, _ := os.ReadFile(filepath.Join(dir, "w2.gguf"))
	b[1000] ^= 0x01
	if err := os.WriteFile(filepath.Join(dir, "w2.gguf"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	started, err := VerifyStarter{Engine: x.eng}.StartVerify(bg, api.ArkVerifyRequest{Items: []string{"m-small", "m-full"}, OpID: "op_2"})
	if err != nil {
		t.Fatal(err)
	}
	st := x.wait(started.RunID)
	by := resultsByItem(st)
	if st.Trigger != run.TriggerVerify || by["m-small"].Result != "quarantined" || by["m-small"].Code != doctor.SYN025 || by["m-full"].Result != "verified" {
		t.Fatalf("status = %+v", st)
	}
	if st.Totals.Items != 2 || st.Totals.Done != 1 || st.Totals.Failed != 1 || st.Result != run.ResultPartial {
		t.Fatalf("totals = %+v result %s", st.Totals, st.Result)
	}
	if _, err := x.idx.BestCopy("m-small"); !errors.Is(err, ark.ErrNotPresent) {
		t.Fatalf("a quarantined copy is served: %v", err)
	}
	if rec, _ := x.jr.Items.Get(ark.InternalID, "m-small"); rec.State != journal.StateQuarantined || rec.Code != doctor.SYN025 {
		t.Fatalf("journal = %+v", rec)
	}
	if q, _ := os.ReadDir(filepath.Join(x.internal.Path, ".staging", "quarantine", "m-small")); len(q) != 1 {
		t.Fatalf("quarantine = %v", q)
	}
	// The planner now sees the item as missing: a sync would fetch it (here, offline, from the drive).
	x.put(x.drive, "m-small", []string{"w2"})
	st2 := x.importRun(api.ArkImportRequest{Items: []string{"m-small"}})
	if resultsByItem(st2)["m-small"].Result != "verified" {
		t.Fatalf("the quarantined item was not restored by the next import: %+v", st2.Items)
	}
	got, _ := os.ReadFile(filepath.Join(x.internal.Path, "models", "m-small", "w2.gguf"))
	if !bytes.Equal(got, x.data["w2"]) {
		t.Fatal("the restored file is not the good one")
	}
	if !strings.Contains(strings.Join(dirSnapshot(t, filepath.Join(x.internal.Path, ".staging", "quarantine")), "\n"), "reason.json") {
		t.Fatal("the quarantine has no reason")
	}
}
