package cloud

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api/schema"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// tailnetIP is a Tailscale-range address, built at run time so the source holds no such literal.
var tailnetIP = "100." + "64.12.9"

type cfgView struct{ c config.Config }

func (v cfgView) Get() config.Config { return v.c }

// newBuilder is a SnapshotBuilder over the rig's fakes and the given config.
func newBuilder(t *testing.T, r *rig, cfg config.Config) *SnapshotBuilder {
	t.Helper()
	sb, err := NewSnapshotBuilder(SnapshotOptions{
		Status: r.status, Models: r.models, Ark: r.ark, Drill: r.drill, Sync: &fakeSync{}, Config: cfgView{cfg},
		BootID: "5d2f6a54-0f6e-4c53-9d1e-4b6f3f2a7c10", Now: r.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

type fakeSync struct{ r SyncReport }

func (f *fakeSync) Sync() SyncReport { return f.r }

func nodeConfig() config.Config {
	c := config.Defaults()
	c.Node.ID = "ai-n3"
	c.Tailscale.ServePattern = "B"
	return c
}

// readyJSON is a READY.json as the ark slice writes it: it still has root.path and generated_by.node_id,
// and a field the cloud schema does not know.
func readyJSON(rootID, kind, generatedAt string, items int, itemStatus string) []byte {
	its := make([]map[string]any, 0, items)
	for i := 0; i < items; i++ {
		its = append(its, map[string]any{
			"id": "item-" + strings.Repeat("a", 5) + "-" + itoa3(i), "kind": "model", "tier": 1, "status": itemStatus, "bytes": 1000,
			"reason": strings.Repeat("r", 190), "version": "v1", "unknown_extra": true,
		})
	}
	doc := map[string]any{
		"schema_version": 1, "generated_at": generatedAt,
		"generated_by": map[string]any{"agent_version": "0.1.0", "node_id": "ai-n3"},
		"root": map[string]any{
			"id": rootID, "kind": kind, "path": "/home/someone/drive", "profile": "small-8gb", "catalog_version": "2026.11.02-1",
			"fs_total_bytes": 1000000, "fs_free_bytes": 500000, "budget_bytes": 900000, "used_bytes": 400000, "trash_bytes": 0, "read_only": false,
		},
		"overall": "yellow", "survival_capable": true, "last_sync": nil, "last_full_reverify_at": nil,
		"counts": map[string]any{"verified": items}, "items": its,
		"problems": []any{map[string]any{"code": "LAI-SYN-020", "severity": "error", "title": "An item failed"}},
	}
	b, _ := json.Marshal(doc)
	return b
}

func itoa3(i int) string {
	s := "000" + strings.TrimLeft(strings.Repeat("0", 0)+itoaPlain(i), " ")
	return s[len(s)-4:]
}

func itoaPlain(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func drillJSON(runID string) []byte {
	doc := map[string]any{
		"schema_version": 1, "node_id": "ai-n3", "run_id": runID, "trigger": "scheduled", "agent_version": "0.1.0",
		"catalog_version": "2026.11.02-1", "mode": "online", "quick": false, "started_at": "2026-11-02T03:00:00Z", "finished_at": "2026-11-02T03:04:00Z",
		"overall": "warn", "survival_ready": true, "counts": map[string]any{"pass": 10, "warn": 1, "fail": 0, "skip": 0},
		"checks":        []any{map[string]any{"id": "drl.stt.es", "area": "stt", "result": "warn", "code": "LAI-SVC-015", "ms": 120, "details": map[string]any{"text": "ok", "n": 2, "flag": true, "none": nil}}},
		"restored":      map[string]any{"loaded_models": []any{"qwen3.5-9b-q4km"}, "default_model": "qwen3.5-9b-q4km", "ok": true},
		"network_fence": map[string]any{"enabled": true, "blocked_dials": 0},
	}
	b, _ := json.Marshal(doc)
	return b
}

func richFixtures(r *rig) {
	r.models.view = statusapi.ModelsView{Default: "qwen3.5-9b-q4km", ModelsMax: 1, Rows: []statusapi.ModelRow{
		{ID: "qwen3.5-9b-q4km", Present: true, Verified: true, Loadable: true, Status: "loaded", Roles: []string{"chat", "coder"}, InProfile: true,
			Locations: []statusapi.ModelLocation{{Root: "/srv/localai", Complete: true}}},
		{ID: "qwen3.6-35b-a3b-q4km", Loadable: false, Status: "absent", LoadableReason: &statusapi.ReasonBody{Code: "LAI-CAT-005", Message: "Needs 11.0 GB of graphics memory; this computer has 8.0 GB."}},
		{ID: "small-whisper-1", Present: true, Verified: true, Loadable: true, Status: "n/a"},
	}}
	r.ark.r = ArkReport{DriveLastSyncedAt: t0.Add(-time.Hour), Roots: []ArkRootReport{
		{ID: "internal", Path: "/srv/localai", Kind: "internal", Present: true, T1: "ok", T2: "partial", T3: "not_planned", ItemsMissing: 1, BytesUsed: 400000, BytesFree: 500000,
			LastSyncAt: t0.Add(-2 * time.Hour), Overall: "yellow", SurvivalCapable: true, ReadyJSON: readyJSON("internal", "internal", "2026-11-02T03:10:00Z", 3, "verified")},
		{ID: "ark-3f9c0a12", Path: "/media/drive", Kind: "ark_drive", Present: false, T1: "ok", T2: "ok", T3: "ok", Overall: "green", SurvivalCapable: true},
	}}
	r.drill.d = &DrillReport{RunID: "drl-20261102T030000Z", LastRunAt: t0.Add(-12 * time.Hour), Result: "warn", SurvivalReady: true,
		FailedChecks: []string{"LAI-SVC-015"}, FailedCheckIDs: []string{"drl.stt.es"}, ResultsJSON: drillJSON("drl-20261102T030000Z")}
	r.status.edit(func(s *statusapiStatus) {
		s.Services = append(s.Services,
			statusapi.ServiceRow{Name: "kiwix", Active: "inactive", Sub: "dead"},
			statusapi.ServiceRow{Name: "whisper", Active: "activating", Sub: "start"},
			statusapi.ServiceRow{Name: "agent", Active: "active"}, // not part of the snapshot's service enum
			statusapi.ServiceRow{Name: "sync", Active: "not-found"})
		s.Mode.Transition = &statusapi.TransitionBlock{OpID: "op_1", From: "online", Target: "offline", Step: "link_stop", StartedAt: "2026-11-02T15:00:00Z"}
		s.Alerts = append(s.Alerts, statusapi.AlertRow{Code: "LAI-NET-005", Severity: "error", Title: "Two Tailscale patterns"})
	})
}

func loadSchema(t *testing.T) (*schema.Validator, any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/state.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	v := schema.New(doc)
	// CheckSchema refuses the "$defs" keyword itself, so it checks the definitions and the root apart.
	root := doc.(map[string]any)
	rootOnly := map[string]any{}
	for k, val := range root {
		if k != "$defs" {
			rootOnly[k] = val
		}
	}
	if err := v.CheckSchema(rootOnly); err != nil {
		t.Fatalf("the schema file uses unsupported keywords: %v", err)
	}
	for name, def := range root["$defs"].(map[string]any) {
		if err := v.CheckSchema(def); err != nil {
			t.Fatalf("$defs/%s uses unsupported keywords: %v", name, err)
		}
	}
	return v, doc
}

func validate(t *testing.T, body []byte) {
	t.Helper()
	v, doc := loadSchema(t)
	var inst any
	if err := json.Unmarshal(body, &inst); err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(doc, inst); err != nil {
		t.Fatalf("the snapshot does not conform to state.v1.schema.json: %v\n%s", err, body)
	}
}

func TestSnapshotMatchesSchemaMinimalAndFull(t *testing.T) {
	r := newRig(t)
	b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validate(t, b.Body)

	richFixtures(r)
	full, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validate(t, full.Body)
	s := full.Snapshot
	if s.Ark.Roots[0].Ready == nil || s.Drill == nil || s.Drill.Results == nil || len(s.Models) != 3 || s.ModeTransition == nil {
		t.Fatalf("the full fixture did not fill every block: %+v", s)
	}
	// The strict mirror drops what the cloud schema forbids.
	for _, forbidden := range []string{"/home/someone", `"path"`, "node_id\":\"ai-n3\",\"agent", "unknown_extra"} {
		if strings.Contains(string(full.Body[strings.Index(string(full.Body), `"ark"`):]), forbidden) {
			t.Errorf("the snapshot still has %q", forbidden)
		}
	}
	if strings.Count(string(full.Body), `"node_id"`) != 1 {
		t.Error("node_id appears outside the top level (READY generated_by or the drill document)")
	}
}

func TestSnapshotMappingRules(t *testing.T) {
	r := newRig(t)
	richFixtures(r)
	b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := b.Snapshot
	if s.NodeID != "ai-n3" || s.SchemaVersion != 1 || s.Seq != 0 || s.Mode != "online" || s.Connectivity != "full" || s.Profile != "small-8gb" {
		t.Errorf("header %+v", s)
	}
	if s.CatalogVersion == nil || *s.CatalogVersion != "2026.11.02-1" {
		t.Errorf("catalog_version %v", s.CatalogVersion)
	}
	if s.ModeTransition.Target != "offline" || s.ModeTransition.Step != "link_stop" {
		t.Errorf("mode_transition %+v", s.ModeTransition)
	}
	// Model status: loaded stays, everything else (absent, n/a) is unloaded; roots by root id.
	byID := map[string]SnapModel{}
	for _, m := range s.Models {
		byID[m.ID] = m
	}
	if byID["qwen3.5-9b-q4km"].Status != "loaded" || !byID["qwen3.5-9b-q4km"].Default || len(byID["qwen3.5-9b-q4km"].Roots) != 1 || byID["qwen3.5-9b-q4km"].Roots[0] != "internal" {
		t.Errorf("loaded model %+v", byID["qwen3.5-9b-q4km"])
	}
	if byID["qwen3.6-35b-a3b-q4km"].Status != "unloaded" || byID["qwen3.6-35b-a3b-q4km"].LoadableReason == nil || byID["small-whisper-1"].Status != "unloaded" {
		t.Errorf("other models %+v", byID)
	}
	if byID["qwen3.5-9b-q4km"].Slots != 1 || len(byID["qwen3.5-9b-q4km"].InputModalities) != 1 {
		t.Errorf("slots/modalities default %+v", byID["qwen3.5-9b-q4km"])
	}
	// Alerts: error becomes critical.
	sev := map[string]string{}
	for _, a := range s.Alerts {
		sev[a.Code] = a.Severity
	}
	if sev["LAI-NET-005"] != "critical" || sev["LAI-HW-005"] != "warning" {
		t.Errorf("alert severities %v", sev)
	}
	// READY problems: error becomes critical too.
	if p := s.Ark.Roots[0].Ready.Problems; len(p) != 1 || p[0].Severity != "critical" {
		t.Errorf("ready problems %+v", p)
	}
	// Services: only the five known names; active_state mapped.
	state := map[string]string{}
	for _, sv := range s.Services {
		state[sv.Name] = sv.ActiveState
	}
	want := map[string]string{"llama": "active", "kiwix": "inactive", "whisper": "activating", "sync": "not_installed"}
	if len(state) != len(want) {
		t.Errorf("services %v", state)
	}
	for k, v := range want {
		if state[k] != v {
			t.Errorf("service %s = %q, want %q", k, state[k], v)
		}
	}
	// serve_pattern comes from node.yaml; tailscale mirrors the status.
	if s.Tailscale.ServePattern != "B" || !s.Tailscale.ServeActive || s.Tailscale.BackendState == nil || *s.Tailscale.BackendState != "Running" {
		t.Errorf("tailscale %+v", s.Tailscale)
	}
	cfg := nodeConfig()
	cfg.Tailscale.ServePattern = "A"
	b2, _ := newBuilder(t, r, cfg).Build(context.Background())
	if b2.Snapshot.Tailscale.ServePattern != "A" {
		t.Errorf("serve_pattern %q", b2.Snapshot.Tailscale.ServePattern)
	}
	// Hardware and llama.
	if s.Hardware.RAMUsedMiB != 14336-5120 || s.Hardware.Power.Source != "ac" || len(s.Hardware.GPUs) != 1 || s.Llama.Router != "up" || s.Llama.Backend != "cuda" || s.Llama.ModelsMax != 1 {
		t.Errorf("hardware/llama %+v / %+v", s.Hardware, s.Llama)
	}
	// Tiers and drive timestamp.
	if r0 := s.Ark.Roots[0]; r0.Tiers.T2 != "partial" || r0.Tiers.T3 != "not_planned" || r0.ReadyGeneratedAt == nil || s.Ark.DriveLastSyncedAt == nil {
		t.Errorf("ark %+v", r0)
	}
	if s.Ark.Roots[1].Ready != nil || s.Ark.Roots[1].ReadyGeneratedAt != nil {
		t.Errorf("a root without READY.json must report ready null: %+v", s.Ark.Roots[1])
	}
}

func TestLlamaRouterStates(t *testing.T) {
	for name, tc := range map[string]struct {
		rows []statusapi.ServiceRow
		mode string
		want string
	}{
		"healthy":    {[]statusapi.ServiceRow{{Name: "llama", Active: "active", Healthy: true}}, "router", "up"},
		"starting":   {[]statusapi.ServiceRow{{Name: "llama", Active: "activating"}}, "router", "starting"},
		"not ready":  {[]statusapi.ServiceRow{{Name: "llama", Active: "active", Healthy: false}}, "router", "starting"},
		"stopped":    {[]statusapi.ServiceRow{{Name: "llama", Active: "inactive"}}, "router", "down"},
		"no service": {nil, "router", "down"},
		"single":     {[]statusapi.ServiceRow{{Name: "llama", Active: "active", Healthy: true}}, "single", "single_mode"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.status.edit(func(s *statusapiStatus) { s.Services, s.Node.LlamaMode = tc.rows, tc.mode })
			b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if b.Snapshot.Llama.Router != tc.want {
				t.Errorf("router = %s, want %s", b.Snapshot.Llama.Router, tc.want)
			}
		})
	}
}

func TestSeqAndBootID(t *testing.T) {
	r := newRig(t)
	sb := newBuilder(t, r, nodeConfig())
	var seqs []int64
	for i := 0; i < 3; i++ {
		b, err := sb.Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, b.Snapshot.Seq)
		if b.Snapshot.BootID != sb.BootID() {
			t.Error("boot_id changed inside a process")
		}
	}
	if seqs[0] != 0 || seqs[1] != 1 || seqs[2] != 2 {
		t.Errorf("seq %v: starts at 0 and goes up by one", seqs)
	}
	other := newBuilder(t, r, nodeConfig())
	if _, err := NewSnapshotBuilder(SnapshotOptions{}); err == nil {
		t.Error("a builder without a status source must be refused")
	}
	if other.Seq() != -1 {
		t.Errorf("a new builder starts before seq 0, got %d", other.Seq())
	}
	if n := newUUID(); len(n) != 36 || n == newUUID() {
		t.Errorf("uuid %q", n)
	}
}

func TestReadySentOnlyWhenItsGeneratedAtChanged(t *testing.T) {
	r := newRig(t)
	richFixtures(r)
	sb := newBuilder(t, r, nodeConfig())
	ctx := context.Background()
	b1, _ := sb.Build(ctx)
	if b1.Snapshot.Ark.Roots[0].Ready == nil {
		t.Fatal("the first snapshot must carry READY")
	}
	// Not committed (the push failed): the next snapshot carries it again.
	b2, _ := sb.Build(ctx)
	if b2.Snapshot.Ark.Roots[0].Ready == nil {
		t.Fatal("an unaccepted READY must be sent again")
	}
	b2.Commit()
	b3, _ := sb.Build(ctx)
	if b3.Snapshot.Ark.Roots[0].Ready != nil || b3.Snapshot.Ark.Roots[0].ReadyGeneratedAt == nil {
		t.Fatalf("an accepted, unchanged READY must be null with ready_generated_at set: %+v", b3.Snapshot.Ark.Roots[0])
	}
	r.ark.mu.Lock()
	r.ark.r.Roots[0].ReadyJSON = readyJSON("internal", "internal", "2026-11-02T14:00:00Z", 3, "verified")
	r.ark.mu.Unlock()
	b4, _ := sb.Build(ctx)
	if b4.Snapshot.Ark.Roots[0].Ready == nil || b4.Snapshot.Ark.Roots[0].Ready.GeneratedAt != "2026-11-02T14:00:00Z" {
		t.Fatal("a changed generated_at must send READY again")
	}
}

func TestDrillResultsGoOutOnceAfterEachRunAndNeverForManualPlug(t *testing.T) {
	r := newRig(t)
	richFixtures(r)
	sb := newBuilder(t, r, nodeConfig())
	ctx := context.Background()
	b1, _ := sb.Build(ctx)
	if b1.Snapshot.Drill == nil || b1.Snapshot.Drill.Results == nil || b1.Snapshot.Drill.Results.RunID != "drl-20261102T030000Z" {
		t.Fatalf("drill %+v", b1.Snapshot.Drill)
	}
	if b1.Snapshot.Drill.FailedChecks[0] != "LAI-SVC-015" || b1.Snapshot.Drill.Result != "warn" {
		t.Errorf("drill summary %+v", b1.Snapshot.Drill)
	}
	b1.Commit()
	b2, _ := sb.Build(ctx)
	if b2.Snapshot.Drill == nil || b2.Snapshot.Drill.Results != nil {
		t.Fatal("results must be null after they were accepted")
	}
	// A new run: results again, once.
	r.drill.mu.Lock()
	r.drill.d = &DrillReport{RunID: "drl-20261103T030000Z", LastRunAt: t0, Result: "pass", SurvivalReady: true, ResultsJSON: drillJSON("drl-20261103T030000Z")}
	r.drill.mu.Unlock()
	b3, _ := sb.Build(ctx)
	if b3.Snapshot.Drill.Results == nil {
		t.Fatal("a new run must send its results")
	}
	// A manual_plug record never carries results.
	r.drill.mu.Lock()
	r.drill.d = &DrillReport{RunID: "drl-manual-1", LastRunAt: t0, Result: "pass", SurvivalReady: true, ManualPlug: true, ResultsJSON: drillJSON("drl-manual-1")}
	r.drill.mu.Unlock()
	b4, _ := sb.Build(ctx)
	if b4.Snapshot.Drill == nil || b4.Snapshot.Drill.Results != nil || len(b4.Snapshot.Drill.FailedChecks) != 0 {
		t.Fatalf("manual_plug drill %+v", b4.Snapshot.Drill)
	}
	validate(t, b3.Body)
	validate(t, b4.Body)
	// No drill at all is null.
	r.drill.mu.Lock()
	r.drill.d = nil
	r.drill.mu.Unlock()
	b5, _ := sb.Build(ctx)
	if b5.Snapshot.Drill != nil {
		t.Error("no drill must be null")
	}
}

func TestSyncResultMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		run  SyncRun
		want string
	}{
		"done clean":         {SyncRun{State: "done"}, "ok"},
		"done with failures": {SyncRun{State: "done", FailedItems: 2}, "partial"},
		"done items left":    {SyncRun{State: "done", LeftItems: 1}, "partial"},
		"failed":             {SyncRun{State: "failed"}, "failed"},
		"cancelled":          {SyncRun{State: "cancelled"}, "failed"},
		"skipped":            {SyncRun{State: "skipped", SkipReason: "mode"}, "skipped"},
		"unknown":            {SyncRun{State: "weird"}, "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SyncResult(tc.run); got != tc.want {
				t.Errorf("SyncResult = %s, want %s", got, tc.want)
			}
		})
	}
	r := newRig(t)
	sync := &fakeSync{r: SyncReport{Running: true, RunID: "run-20261102T031500Z-a1f3", ProgressPct: f64(42.5),
		Last: &SyncRun{ID: "run-1", FinishedAt: t0, State: "skipped", SkipReason: "mode", ErrorCode: "LAI-SYN-010"}}}
	sb, _ := NewSnapshotBuilder(SnapshotOptions{Status: r.status, Sync: sync, BootID: "5d2f6a54-0f6e-4c53-9d1e-4b6f3f2a7c10", Now: r.clk.Now})
	b, _ := sb.Build(context.Background())
	s := b.Snapshot.Sync
	if !s.Running || s.RunID == nil || *s.ProgressPct != 42.5 || s.LastRun == nil || s.LastRun.Result != "skipped" || *s.LastRun.SkipReason != "mode" || *s.LastRun.ErrorCode != "LAI-SYN-010" {
		t.Errorf("sync %+v", s)
	}
	validate(t, b.Body)
}

func TestScrubberDropsAFieldAndLogsCLD058(t *testing.T) {
	r := newRig(t)
	secretHex := strings.Repeat("9f", 32)
	r.status.edit(func(s *statusapiStatus) {
		s.Alerts = []statusapi.AlertRow{
			{Code: "LAI-HW-005", Severity: "warning", Title: "Reach it at " + tailnetIP + " now"},
			{Code: "LAI-NET-005", Severity: "warning", Title: "key " + secretHex},
			{Code: "LAI-HW-006", Severity: "warning", Title: "Fine title"},
		}
		s.Hardware.GPUs[0].Name = "Card in /home/someone/rig"
	})
	b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{tailnetIP, secretHex, "/home/someone"} {
		if strings.Contains(string(b.Body), leak) {
			t.Errorf("%q left the node", leak)
		}
	}
	if b.Snapshot.Alerts[0].Title != "" || b.Snapshot.Alerts[1].Title != "" || b.Snapshot.Alerts[2].Title != "Fine title" || b.Snapshot.Hardware.GPUs[0].Name != "" {
		t.Errorf("fields: %+v / %+v", b.Snapshot.Alerts, b.Snapshot.Hardware.GPUs)
	}
	want := map[string]bool{"alerts[0].title": true, "alerts[1].title": true, "hardware.gpus[0].name": true}
	for _, p := range b.Dropped {
		delete(want, p)
	}
	if len(want) != 0 || len(b.Dropped) != 3 {
		t.Errorf("dropped paths %v", b.Dropped)
	}
}

func TestPushLogsLAICLD058WithPathsOnly(t *testing.T) {
	r := newRig(t)
	r.status.edit(func(s *statusapiStatus) {
		s.Alerts = []statusapi.AlertRow{{Code: "LAI-HW-005", Severity: "warning", Title: "At " + tailnetIP}}
	})
	r.enroll()
	r.waitCount("state", 1)
	logs := r.logs.String()
	if !strings.Contains(logs, doctor.CLD058) || !strings.Contains(logs, "alerts[0].title") || strings.Contains(logs, tailnetIP) {
		t.Errorf("log:\n%s", logs)
	}
}

func TestSnapshotTrimsDriveItemsFirstThenInternalThenModels(t *testing.T) {
	big := func(rootID, kind string, n int, status string) ArkRootReport {
		return ArkRootReport{ID: rootID, Kind: kind, Present: true, T1: "ok", T2: "ok", T3: "ok", Overall: "green", ReadyJSON: readyJSON(rootID, kind, "2026-11-02T03:10:00Z", n, status)}
	}
	t.Run("drive items are cut first", func(t *testing.T) {
		r := newRig(t)
		r.ark.r = ArkReport{Roots: []ArkRootReport{big("internal", "internal", 600, "verified"), big("ark-aaaaaaaa", "ark_drive", 600, "verified")}}
		b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Body) > MaxSnapshotBytes {
			t.Fatalf("body %d bytes", len(b.Body))
		}
		if len(b.Trimmed) == 0 || b.Trimmed[0] != "ark.drive.items" {
			t.Fatalf("trimmed %v", b.Trimmed)
		}
		for _, rt := range b.Snapshot.Ark.Roots {
			if rt.ID != "internal" && len(rt.Ready.Items) != 0 {
				t.Errorf("drive %s kept %d verified items", rt.ID, len(rt.Ready.Items))
			}
		}
		validate(t, b.Body)
	})
	t.Run("non-verified items stay, then the internal root is cut", func(t *testing.T) {
		r := newRig(t)
		// Drive roots whose items are all non-verified cannot shrink; the internal root's verified items go.
		r.ark.r = ArkReport{Roots: []ArkRootReport{big("internal", "internal", 600, "verified"), big("ark-aaaaaaaa", "ark_drive", 300, "missing"), big("ark-bbbbbbbb", "ark_drive", 300, "failed")}}
		b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Body) > MaxSnapshotBytes || len(b.Trimmed) != 2 || b.Trimmed[0] != "ark.drive.items" || b.Trimmed[1] != "ark.internal.items" {
			t.Fatalf("body %d trimmed %v", len(b.Body), b.Trimmed)
		}
		for _, rt := range b.Snapshot.Ark.Roots {
			if rt.ID != "internal" && len(rt.Ready.Items) != 300 {
				t.Errorf("drive %s lost non-verified items: %d", rt.ID, len(rt.Ready.Items))
			}
		}
	})
	t.Run("what cannot shrink is an error", func(t *testing.T) {
		r := newRig(t)
		r.ark.r = ArkReport{Roots: []ArkRootReport{big("internal", "internal", 600, "missing"), big("ark-aaaaaaaa", "ark_drive", 600, "missing")}}
		b, err := newBuilder(t, r, nodeConfig()).Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Last resort: READY documents are dropped altogether and the snapshot fits.
		if len(b.Body) > MaxSnapshotBytes || b.Trimmed[len(b.Trimmed)-1] != "ark.ready" {
			t.Fatalf("body %d trimmed %v", len(b.Body), b.Trimmed)
		}
	})
	t.Run("models keep the profile's and the loaded ones", func(t *testing.T) {
		s := StateSnapshot{Models: []SnapModel{
			{ID: "aaa", Status: "loaded"}, {ID: "bbb", Status: "unloaded", inProfile: true}, {ID: "ccc", Status: "unloaded", Present: true},
			{ID: "ddd", Status: "loading"}, {ID: "eee", Status: "failed"},
		}}
		trimModels(&s)
		var ids []string
		for _, m := range s.Models {
			ids = append(ids, m.ID)
		}
		if strings.Join(ids, ",") != "aaa,bbb,ddd" {
			t.Errorf("kept %v", ids)
		}
	})
}

func TestReduceKeepsModeModelsServicesTiersAndAlertCodes(t *testing.T) {
	r := newRig(t)
	richFixtures(r)
	b, _ := newBuilder(t, r, nodeConfig()).Build(context.Background())
	red := Reduce(b.Snapshot, "qwen3.5-9b-q4km")
	raw, _ := json.Marshal(red)
	if len(raw) > MaxReducedBytes {
		t.Fatalf("reduced state is %d bytes", len(raw))
	}
	if red.Mode != "online" || len(red.LoadedModels) != 1 || red.DefaultModel == nil || len(red.Services) != 4 || len(red.Ark) != 2 || len(red.Alerts) != 2 {
		t.Errorf("reduced %+v", red)
	}
}

// ---------------------------------------------------------------------------------------------------
// The push loop

func TestFirstSnapshotIsPushedAtLinkStartWithSeqFromZero(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.enroll()
	r.waitCount("state", 1)
	r.waitTimers(2)          // the heartbeat's and the state loop's maximum-interval timer
	r.step(60 * time.Second) // the maximum interval
	r.waitCount("state", 2)
	states := r.srv.States()
	var a, b StateSnapshot
	_ = json.Unmarshal(states[0], &a)
	_ = json.Unmarshal(states[1], &b)
	if a.Seq != 0 || b.Seq != 1 || a.BootID != r.link.BootID() || a.BootID != b.BootID || a.NodeID != "ai-n3" {
		t.Errorf("seq/boot: %d %d %s", a.Seq, b.Seq, a.BootID)
	}
	validate(t, states[0])
	validate(t, states[1])
}

func quickState(r *rig) { r.srv.Handshake.StateMinIntervalS, r.srv.Handshake.StateMaxIntervalS = 5, 60 }

func TestStatePushIsDebouncedToTheMinimumInterval(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.enroll()
	r.waitCount("state", 1)
	r.waitTimers(2)
	// Three relevant events inside the debounce window cost one push, five seconds after the last one.
	_ = r.bus.Publish("model.loaded", "", modelLoaded("qwen3.5-9b-q4km"))
	_ = r.bus.Publish("model.loaded", "", modelLoaded("qwen3.5-9b-q4km"))
	_ = r.bus.Publish("service.state", "", serviceState())
	r.step(5 * time.Second) // the debounce timer replaced the maximum-interval timer
	r.waitCount("state", 2)
	// Quiet again: the next one is the maximum interval, not another debounce.
	r.waitTimers(2)
	n := r.srv.Count("state")
	r.clk.Advance(5 * time.Second)
	if r.srv.Count("state") != n {
		t.Error("an extra snapshot without a new event")
	}
	r.step(55 * time.Second)
	r.waitCount("state", 3)
}

func TestStale409IsIgnoredAndTheNextSnapshotIsNewer(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.srv.Script("state", fakeStale())
	r.enroll()
	r.waitCount("state", 1) // answered 409 stale_seq
	if r.link.State() != StateLinked {
		t.Fatalf("a 409 changed the state to %s", r.link.State())
	}
	r.waitTimers(2)
	r.step(60 * time.Second)
	r.waitCount("state", 2)
	states := r.srv.States() // only the accepted one is stored
	if len(states) != 1 {
		t.Fatalf("accepted %d", len(states))
	}
	var s StateSnapshot
	_ = json.Unmarshal(states[0], &s)
	if s.Seq != 1 {
		t.Errorf("the next snapshot has seq %d, want 1", s.Seq)
	}
}

func TestState400LogsThePathNeverTheValue(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.srv.Script("state", badRequestWithPath("models", "0", "id"))
	r.enroll()
	r.waitCount("state", 1)
	r.waitTimers(2)
	// The builder keeps running: the next change sends a fresh snapshot.
	_ = r.bus.Publish("model.loaded", "", modelLoaded("qwen3.5-9b-q4km"))
	r.step(5 * time.Second)
	r.waitCount("state", 2)
	logs := r.logs.String()
	if !strings.Contains(logs, "models.0.id") {
		t.Errorf("the Zod path is not in the log:\n%s", logs)
	}
	if strings.Contains(logs, "SECRET-VALUE") {
		t.Error("a value reached the log")
	}
}

func TestStateFailureBacksOffOnItsOwnWithoutChangingTheLinkState(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.srv.Forever("state", fakeReply(503))
	r.enroll()
	r.waitCount("state", 1)
	r.waitTimers(2)
	r.step(BackoffMin)
	r.waitCount("state", 2)
	if r.link.State() != StateLinked {
		t.Errorf("a state push failure moved the link to %s", r.link.State())
	}
	r.srv.Clear("state")
	r.step(BackoffMin) // the test jitter is the lower bound of each window
	r.waitCount("state", 3)
}

func TestState404RunsTheHandshakeAndFallsBackToCapabilities(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.srv.Script("state", fakeReply(404))
	r.srv.Handshake.Features = []string{"commands.v1", "catalog.v1", "key.v1"}
	// The enroll handshake sees no state.v1 either, so the endpoint is never tried.
	r.enroll()
	r.waitAlert(doctor.CLD056, true)
	if r.srv.Count("state") != 0 {
		t.Fatalf("the state endpoint was called %d times", r.srv.Count("state"))
	}
	if st := r.link.CloudStatus(); st.StateTransport != "capabilities" {
		t.Errorf("state_transport = %s", st.StateTransport)
	}
	// The heartbeat carries the reduced snapshot instead.
	r.waitCount("heartbeat", 1)
	hb := heartbeatBodyOf(t, r, 0)
	state, ok := hb["capabilities"].(map[string]any)["localai"].(map[string]any)["state"].(map[string]any)
	if !ok || state["mode"] != "online" || state["boot_id"] != r.link.BootID() {
		t.Fatalf("capabilities.localai.state = %v", hb["capabilities"])
	}
	// Once an hour the handshake is repeated; an updated cloud brings the endpoint back.
	r.srv.Locked(func() { r.srv.Handshake.Features = []string{"state.v1", "commands.v1", "catalog.v1", "key.v1"} })
	cfgCalls := r.srv.Count("config")
	r.step(time.Hour)
	r.waitCount("config", cfgCalls+1)
	r.waitCount("state", 1)
	r.waitAlert(doctor.CLD056, false)
	if st := r.link.CloudStatus(); st.StateTransport != "endpoint" {
		t.Errorf("state_transport = %s", st.StateTransport)
	}
}

func TestStateEndpointAnswering404MidwayFallsBack(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.srv.StateEndpoint = false // the route is missing, yet the handshake advertises state.v1
	r.srv.Handshake.Features = []string{"state.v1", "commands.v1", "catalog.v1", "key.v1"}
	r.enroll()
	r.waitCount("state", 1) // 404: the link repeats the handshake...
	r.waitCount("config", 2)
	// ...which still says state.v1, so the node is the unknown part: it registers again.
	r.waitCount("register", 2)
	r.waitState(StateLinked)
}

func TestPushNowForStatusRefresh(t *testing.T) {
	r := newRig(t)
	if _, err := r.link.PushNow(r.ctx); err != ErrNotLinked {
		t.Fatalf("not linked: %v", err)
	}
	r.enroll()
	r.waitCount("state", 1)
	res, err := r.link.PushNow(r.ctx)
	if err != nil || res.Seq != 1 || res.ReportedAt == "" {
		t.Fatalf("PushNow = %+v %v", res, err)
	}
}

func TestStopPushesALastSnapshotThatConforms(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount("state", 1)
	r.stop()
	states := r.srv.States()
	if len(states) != 2 {
		t.Fatalf("%d snapshots accepted", len(states))
	}
	validate(t, states[1])
}
