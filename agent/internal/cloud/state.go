package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// MaxSnapshotBytes is the largest state body the cloud accepts (agent.md section 4.8.4).
const MaxSnapshotBytes = 256 << 10

// MaxReducedBytes is the largest reduced snapshot of the capabilities fallback.
const MaxReducedBytes = 32 << 10

// The seams of the snapshot builder besides statusapi's. Every one is optional: a missing one renders
// the empty state of its block, so a node without an ark slice or drill still reports.

// StatusSource is statusapi's builder (*statusapi.StatusBuilder): the snapshot reuses the status view
// instead of reading the subsystems a second time.
type StatusSource interface {
	Build(ctx context.Context, forCloud bool, o statusapi.BuildOptions) statusapi.Status
}

// CatalogSource is the catalog the agent runs with (*statusapi.CatalogStore).
type CatalogSource interface {
	Get() statusapi.CatalogState
}

// ArkSource says what the content roots hold, from their READY.json files (ark-sync.md section 4.12).
// The ark slice implements it.
type ArkSource interface {
	Ark() ArkReport
}

// ArkReport is what an ArkSource answers. A zero time is JSON null.
type ArkReport struct {
	Roots []ArkRootReport
	// DriveLastSyncedAt is the last successful sync to an ark drive (it survives unplugging).
	DriveLastSyncedAt time.Time
}

// ArkRootReport is one content root.
type ArkRootReport struct {
	// ID is the root id: internal or ark-<8 hex>.
	ID string
	// Path is the root's directory. It maps a model's locations to root ids and is never sent.
	Path string
	// Kind is internal or ark_drive.
	Kind    string
	Present bool
	// Tiers are ok, partial or missing for t1 and t2, and also not_planned for t3 (ark-sync.md
	// section 4.16).
	T1, T2, T3                                    string
	ItemsMissing, ItemsFailed, ItemsSkippedBudget int
	BytesUsed, BytesFree                          int64
	LastSyncAt, LastVerifyAt                      time.Time
	// Overall is green, yellow or red; SurvivalCapable is READY.json's survival_capable.
	Overall         string
	SurvivalCapable bool
	// ReadyJSON is the root's READY.json, nil while it has none yet.
	ReadyJSON []byte
}

// SyncSource is the sync engine's picture (ark-sync.md section 3.5); the ark slice implements it.
type SyncSource interface {
	Sync() SyncReport
}

// SyncReport is what a SyncSource answers.
type SyncReport struct {
	Running     bool
	RunID       string
	ProgressPct *float64
	// Last is the last finished run of any trigger, nil when none.
	Last *SyncRun
}

// SyncRun is a finished sync, verify or import run.
type SyncRun struct {
	ID         string
	FinishedAt time.Time
	// State is the run's final state: done, failed, cancelled or skipped.
	State string
	// FailedItems and LeftItems count the items that failed and the ones left for time or budget.
	FailedItems, LeftItems int
	SkipReason, ErrorCode  string
}

// SyncResult maps a run's final state to the snapshot's sync.last_run.result (cloud-app.md section
// 3.6): done with no failed item and none left is ok, done otherwise partial, failed and cancelled
// are failed, skipped is skipped. An unknown state counts as failed.
func SyncResult(r SyncRun) string {
	switch r.State {
	case "done":
		if r.FailedItems == 0 && r.LeftItems == 0 {
			return "ok"
		}
		return "partial"
	case "skipped":
		return "skipped"
	}
	return "failed"
}

// DrillSource is the drill's last run (drill.md); the drill slice implements it.
type DrillSource interface {
	Drill() *DrillReport
}

// DrillReport is what a DrillSource answers; nil when no drill ever ran.
type DrillReport struct {
	RunID          string
	LastRunAt      time.Time
	Result         string // pass, warn or fail
	SurvivalReady  bool
	FailedChecks   []string
	FailedCheckIDs []string
	// ResultsJSON is the full results document of the last run (drill.md section 3.4); nil when the
	// run was a manual_plug record.
	ResultsJSON []byte
	// ManualPlug marks a record written by `aictl drill record-manual`: it has no checks and its
	// results are never sent.
	ManualPlug bool
}

// SnapshotOptions configure a SnapshotBuilder.
type SnapshotOptions struct {
	// Status is required.
	Status StatusSource
	// Models gives the per-model rows (statusapi.ModelView); nil sends an empty list.
	Models  statusapi.ModelView
	Catalog CatalogSource
	Config  interface{ Get() config.Config }
	Ark     ArkSource
	Sync    SyncSource
	Drill   DrillSource
	// BootID is a fresh UUID per agent process; required.
	BootID string
	Now    func() time.Time
}

// SnapshotBuilder builds LocalaiStateSnapshotV1 from the views. It is safe for concurrent use.
type SnapshotBuilder struct {
	o SnapshotOptions

	mu  sync.Mutex
	seq int64
	// committed holds what the cloud last accepted: the READY generated_at of each root and the drill
	// run whose results were sent.
	readyAccepted map[string]string
	drillAccepted string
}

// NewSnapshotBuilder returns a builder; Status and BootID are required.
func NewSnapshotBuilder(o SnapshotOptions) (*SnapshotBuilder, error) {
	if o.Status == nil || o.BootID == "" {
		return nil, errors.New("cloud: the snapshot builder needs a status source and a boot id")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &SnapshotBuilder{o: o, seq: -1, readyAccepted: map[string]string{}}, nil
}

// Built is one snapshot ready to send.
type Built struct {
	Snapshot StateSnapshot
	// Body is the JSON of the snapshot, at most MaxSnapshotBytes.
	Body []byte
	// Trimmed lists what was cut to fit (never values), Dropped the paths of the fields the scrubber
	// removed (LAI-CLD-058).
	Trimmed []string
	Dropped []string

	commit func()
}

// Commit records that the cloud accepted this snapshot: its READY documents and drill results are
// not sent again until they change. Call it on a 200.
func (b *Built) Commit() {
	if b.commit != nil {
		b.commit()
	}
}

// Build takes a snapshot now: seq goes up by one, the status view is read once, every string is
// scrubbed and the body is trimmed to MaxSnapshotBytes.
func (sb *SnapshotBuilder) Build(ctx context.Context) (*Built, error) {
	st := sb.o.Status.Build(ctx, true, statusBuildOptions)
	now := sb.o.Now()
	cfg := config.Defaults()
	if sb.o.Config != nil {
		cfg = sb.o.Config.Get()
	}

	sb.mu.Lock()
	sb.seq++
	seq := sb.seq
	accepted := map[string]string{}
	for k, v := range sb.readyAccepted {
		accepted[k] = v
	}
	drillAccepted := sb.drillAccepted
	sb.mu.Unlock()

	var ark ArkReport
	if sb.o.Ark != nil {
		ark = sb.o.Ark.Ark()
	}
	snap := StateSnapshot{
		SchemaVersion: 1, NodeID: st.Node.ID, BootID: sb.o.BootID, Seq: seq, ReportedAt: rfc3339(now),
		AgentVersion: cut(st.Node.AgentVersion, 64), Mode: oneOf(st.Mode.Value, "offline", "online", "survival"),
		Connectivity: oneOf(st.Connectivity.Value, "not_checked", "full", "internet", "tailnet", "none", "not_checked"),
		Profile:      cut(deref(st.Node.Profile), 64),
		Llama:        snapLlama(st, cfg),
		Hardware:     snapHardware(st),
		Services:     snapServices(st),
		Tailscale: SnapTailscale{
			ServePattern: oneOf(cfg.Tailscale.ServePattern, "none", "A", "B", "none"),
			ServeActive:  st.Tailscale.Applied, BackendState: capPtr(st.Tailscale.BackendState, 32),
		},
		Alerts: snapAlerts(st),
	}
	if t := st.Mode.Transition; t != nil {
		snap.ModeTransition = &SnapTransition{Target: oneOf(t.Target, "offline", "online", "survival"), Step: cut(t.Step, 64), StartedAt: t.StartedAt}
	}
	if st.Node.CatalogVersion != nil && ValidCatalogVersion(*st.Node.CatalogVersion) {
		cv := *st.Node.CatalogVersion
		snap.CatalogVersion = &cv
	}
	snap.Models = sb.snapModels(st, ark)

	pendingReady := map[string]string{}
	snap.Ark, pendingReady = snapArk(ark, accepted)
	snap.Sync = sb.snapSync()
	var pendingDrill string
	snap.Drill, pendingDrill = sb.snapDrill(drillAccepted)

	dropped := scrubValue(reflect.ValueOf(&snap), "")
	body, trimmed, err := fitSnapshot(&snap)
	if err != nil {
		return nil, err
	}
	built := &Built{Snapshot: snap, Body: body, Trimmed: trimmed, Dropped: dropped}
	built.commit = func() {
		sb.mu.Lock()
		defer sb.mu.Unlock()
		for k, v := range pendingReady {
			// A trimmed or dropped READY document was not delivered: do not mark it sent.
			if r := readyOf(&snap, k); r != nil {
				sb.readyAccepted[k] = v
			}
		}
		if pendingDrill != "" && snap.Drill != nil && snap.Drill.Results != nil {
			sb.drillAccepted = pendingDrill
		}
	}
	return built, nil
}

// Seq is the sequence number of the last snapshot built, -1 before the first.
func (sb *SnapshotBuilder) Seq() int64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.seq
}

// BootID is the id of this agent process.
func (sb *SnapshotBuilder) BootID() string { return sb.o.BootID }

func readyOf(s *StateSnapshot, rootID string) *ReadyReport {
	for i := range s.Ark.Roots {
		if s.Ark.Roots[i].ID == rootID {
			return s.Ark.Roots[i].Ready
		}
	}
	return nil
}

// Reduce derives the reduced snapshot of the capabilities fallback from a full one.
func Reduce(s StateSnapshot, defaultModel string) ReducedState {
	r := ReducedState{
		SchemaVersion: 1, BootID: s.BootID, Seq: s.Seq, ReportedAt: s.ReportedAt, Mode: s.Mode,
		LoadedModels: []string{}, Services: []ReducedService{}, Ark: []ReducedRoot{}, Alerts: []string{},
	}
	if defaultModel != "" {
		d := defaultModel
		r.DefaultModel = &d
	}
	for _, m := range s.Models {
		if m.Status == "loaded" {
			r.LoadedModels = append(r.LoadedModels, m.ID)
		}
		if m.Default && r.DefaultModel == nil {
			d := m.ID
			r.DefaultModel = &d
		}
	}
	for _, sv := range s.Services {
		r.Services = append(r.Services, ReducedService{Name: sv.Name, ActiveState: sv.ActiveState})
	}
	for _, rt := range s.Ark.Roots {
		r.Ark = append(r.Ark, ReducedRoot{ID: rt.ID, T1: rt.Tiers.T1, T2: rt.Tiers.T2, T3: rt.Tiers.T3, Overall: rt.Overall})
	}
	for _, a := range s.Alerts {
		r.Alerts = append(r.Alerts, a.Code)
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// Blocks

func snapLlama(st statusapi.Status, cfg config.Config) SnapLlama {
	l := SnapLlama{
		Build:     cut(orStr(deref(st.Node.LlamaBuild), "unknown"), 80),
		Backend:   oneOf(st.Node.Backend, "cpu", "cuda", "vulkan", "cpu"),
		ModelsMax: clampInt(st.Models.ModelsMax, 1, 16),
	}
	llamaRow := serviceRow(st, "llama")
	switch {
	case st.Node.LlamaMode == "single" || cfg.Llama.Mode == "single":
		l.Router = "single_mode"
	case llamaRow == nil:
		l.Router = "down"
	case llamaRow.Active == "activating" || (llamaRow.Active == "active" && !llamaRow.Healthy):
		l.Router = "starting"
	case llamaRow.Active == "active" && llamaRow.Healthy:
		l.Router = "up"
	default:
		l.Router = "down"
	}
	return l
}

func serviceRow(st statusapi.Status, name string) *statusapi.ServiceRow {
	for i := range st.Services {
		if st.Services[i].Name == name {
			return &st.Services[i]
		}
	}
	return nil
}

func snapHardware(st statusapi.Status) SnapHardware {
	h := SnapHardware{
		GPUs:        []SnapGPU{},
		RAMTotalMiB: clampI(st.Hardware.RAMTotalMiB, 0, 16777216),
		CPUTempC:    clampPtr(st.Hardware.CPUTempC, -50, 150),
		Power:       SnapPower{Source: powerSource(st.Hardware.Power.Source)},
	}
	h.RAMUsedMiB = clampI(st.Hardware.RAMTotalMiB-st.Hardware.RAMAvailableMiB, 0, 16777216)
	if st.Hardware.Power.BatteryPercent != nil {
		p := clampF(float64(*st.Hardware.Power.BatteryPercent), 0, 100)
		h.Power.BatteryPercent = &p
	}
	for i, g := range st.Hardware.GPUs {
		if i == 8 {
			break
		}
		h.GPUs = append(h.GPUs, SnapGPU{
			Name: cut(g.Name, 128), VRAMTotalMiB: clampI(g.VRAMTotalMiB, 0, 16777216),
			VRAMUsedMiB: clampI(g.VRAMUsedMiB, 0, 16777216), TempC: clampPtr(g.TempC, -50, 150),
		})
	}
	return h
}

func snapServices(st statusapi.Status) []SnapService {
	out := []SnapService{}
	for _, s := range st.Services {
		switch s.Name {
		case "llama", "kiwix", "whisper", "sync", "drill":
		default:
			continue
		}
		out = append(out, SnapService{Name: s.Name, ActiveState: activeState(s.Active), Since: s.Since})
	}
	return out
}

// activeState maps systemd's ActiveState to the snapshot enum: a unit with no file is not_installed,
// anything unknown counts as inactive.
func activeState(a string) string {
	switch a {
	case "active", "reloading":
		return "active"
	case "inactive", "failed", "activating", "deactivating":
		return a
	case "", "not-found", "not_installed":
		return "not_installed"
	}
	return "inactive"
}

var alertCodeRE = regexp.MustCompile(`^LAI-[A-Z]{2,3}-\d{3}$`)

func snapAlerts(st statusapi.Status) []SnapAlert {
	out := []SnapAlert{}
	for _, a := range st.Alerts {
		if !alertCodeRE.MatchString(a.Code) {
			continue
		}
		if len(out) == 50 {
			break
		}
		out = append(out, SnapAlert{Code: a.Code, Severity: severityOut(a.Severity), Title: cut(a.Title, 200)})
	}
	return out
}

// severityOut maps the agent's severity to the cloud's: error becomes critical (the cloud enum has no
// error), info, warning and critical stay as they are, anything else is info.
func severityOut(s string) string {
	switch s {
	case "critical", "error":
		return "critical"
	case "warning":
		return "warning"
	}
	return "info"
}

var modelIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)

func (sb *SnapshotBuilder) snapModels(st statusapi.Status, ark ArkReport) []SnapModel {
	out := []SnapModel{}
	if sb.o.Models == nil {
		return out
	}
	view := sb.o.Models.Models("en")
	var cat statusapi.CatalogState
	haveCat := sb.o.Catalog != nil
	if haveCat {
		cat = sb.o.Catalog.Get()
	}
	rootByPath := map[string]string{}
	for _, r := range ark.Roots {
		if r.Path != "" {
			rootByPath[r.Path] = r.ID
		}
	}
	for _, r := range view.Rows {
		if !modelIDRE.MatchString(r.ID) {
			continue
		}
		m := SnapModel{
			ID: r.ID, Present: r.Present, Verified: r.Verified, Loadable: r.Loadable,
			Status: modelStatus(r.Status), Default: r.ID == view.Default,
			Slots: 1, Roles: capStrings(r.Roles, 8, 32), InputModalities: []string{"text"}, Roots: []string{},
			inProfile: r.InProfile,
		}
		if r.LoadableReason != nil {
			msg := cut(r.LoadableReason.Message, 200)
			m.LoadableReason = &msg
		}
		if haveCat && cat.Catalog != nil {
			if cm, ok := cat.Catalog.Model(r.ID); ok {
				if cm.Llama != nil {
					if cm.Llama.Parallel > 0 {
						m.Slots = clampInt(cm.Llama.Parallel, 1, 64)
					}
					if cm.Llama.Ctx > 0 {
						c := clampInt(cm.Llama.Ctx, 0, 4194304)
						m.Ctx = &c
					}
					if cm.Llama.MmprojFileKind != "" {
						m.InputModalities = []string{"text", "image"}
					}
				}
				if cm.Engine == "whisper" {
					m.InputModalities = []string{"audio"}
				}
			}
		}
		for _, loc := range r.Locations {
			if id, ok := rootByPath[loc.Root]; ok && loc.Complete && len(m.Roots) < 9 {
				m.Roots = append(m.Roots, id)
			} else if !ok && rootIDRE.MatchString(loc.Root) && loc.Complete && len(m.Roots) < 9 {
				m.Roots = append(m.Roots, loc.Root)
			}
		}
		out = append(out, m)
		if len(out) == 200 {
			break
		}
	}
	return out
}

var rootIDRE = regexp.MustCompile(`^(internal|ark-[0-9a-f]{8})$`)

// modelStatus maps the router status to the snapshot's: loaded, loading and failed stay, everything
// else (absent, stored, unloaded, sleeping, n/a) is unloaded.
func modelStatus(s string) string {
	switch s {
	case "loaded", "loading", "failed":
		return s
	}
	return "unloaded"
}

// snapArk builds the ark block. accepted maps a root to the READY generated_at the cloud last took:
// a root whose READY.json is unchanged since then reports ready null. The second result maps each
// root whose READY document is included to its generated_at, for Commit.
func snapArk(ark ArkReport, accepted map[string]string) (SnapArk, map[string]string) {
	out := SnapArk{Roots: []SnapRoot{}, DriveLastSyncedAt: timeOut(ark.DriveLastSyncedAt)}
	pending := map[string]string{}
	for _, r := range ark.Roots {
		if !rootIDRE.MatchString(r.ID) {
			continue
		}
		if len(out.Roots) == 9 {
			break
		}
		row := SnapRoot{
			ID: r.ID, Present: r.Present,
			Tiers: SnapTiers{
				T1: oneOf(r.T1, "missing", "ok", "partial", "missing"), T2: oneOf(r.T2, "missing", "ok", "partial", "missing"),
				T3: oneOf(r.T3, "missing", "ok", "partial", "missing", "not_planned"),
			},
			ItemsMissing: maxInt(r.ItemsMissing, 0), ItemsFailed: maxInt(r.ItemsFailed, 0), ItemsSkippedBudget: maxInt(r.ItemsSkippedBudget, 0),
			BytesUsed: clampI(r.BytesUsed, 0, 1<<53-1), BytesFree: clampI(r.BytesFree, 0, 1<<53-1),
			LastSyncAt: timeOut(r.LastSyncAt), LastVerifyAt: timeOut(r.LastVerifyAt),
			Overall: oneOf(r.Overall, "red", "green", "yellow", "red"), SurvivalCapable: r.SurvivalCapable,
		}
		if len(r.ReadyJSON) > 0 {
			if rep, ok := decodeReady(r.ReadyJSON); ok {
				g := rep.GeneratedAt
				row.ReadyGeneratedAt = &g
				if accepted[r.ID] != g {
					row.Ready = rep
					pending[r.ID] = g
				}
			}
		}
		out.Roots = append(out.Roots, row)
	}
	return out, pending
}

// decodeReady reads a READY.json into the strict mirror: root.path, generated_by.node_id and any
// field the schema does not know are dropped by the decoding, problem severity error becomes critical.
func decodeReady(raw []byte) (*ReadyReport, bool) {
	var r ReadyReport
	if err := json.Unmarshal(raw, &r); err != nil || r.GeneratedAt == "" {
		return nil, false
	}
	if r.Items == nil {
		r.Items = []ReadyItem{}
	}
	if r.Problems == nil {
		r.Problems = []ReadyProblem{}
	}
	if r.Counts == nil {
		r.Counts = map[string]int{}
	}
	for i := range r.Problems {
		r.Problems[i].Severity = severityOut(r.Problems[i].Severity)
	}
	return &r, true
}

func (sb *SnapshotBuilder) snapSync() SnapSync {
	out := SnapSync{}
	if sb.o.Sync == nil {
		return out
	}
	s := sb.o.Sync.Sync()
	out.Running = s.Running
	out.RunID = strPtrOrNil(cut(s.RunID, 64))
	if s.ProgressPct != nil {
		p := clampF(*s.ProgressPct, 0, 100)
		out.ProgressPct = &p
	}
	if l := s.Last; l != nil && !l.FinishedAt.IsZero() {
		out.LastRun = &SnapSyncRun{
			ID: cut(l.ID, 64), FinishedAt: rfc3339(l.FinishedAt), Result: SyncResult(*l),
			SkipReason: strPtrOrNil(cut(l.SkipReason, 64)), ErrorCode: codeOrNil(l.ErrorCode),
		}
	}
	return out
}

// snapDrill builds the drill block. The full results document goes out once after a run (when the
// run is not the one the cloud already took) and never for a manual_plug record.
func (sb *SnapshotBuilder) snapDrill(accepted string) (*SnapDrill, string) {
	if sb.o.Drill == nil {
		return nil, ""
	}
	d := sb.o.Drill.Drill()
	if d == nil || d.RunID == "" || d.LastRunAt.IsZero() {
		return nil, ""
	}
	out := &SnapDrill{
		RunID: cut(d.RunID, 64), LastRunAt: rfc3339(d.LastRunAt), Result: oneOf(d.Result, "fail", "pass", "warn", "fail"),
		SurvivalReady: d.SurvivalReady, FailedChecks: capCodes(d.FailedChecks, 50), FailedCheckIDs: capStrings(d.FailedCheckIDs, 50, 96),
	}
	var pending string
	if !d.ManualPlug && len(d.ResultsJSON) > 0 && accepted != d.RunID {
		var res DrillResults
		if json.Unmarshal(d.ResultsJSON, &res) == nil && res.RunID != "" {
			if res.Checks == nil {
				res.Checks = []DrillCheck{}
			}
			if res.Restored.LoadedModels == nil {
				res.Restored.LoadedModels = []string{}
			}
			out.Results = &res
			pending = d.RunID
		}
	}
	return out, pending
}

// ---------------------------------------------------------------------------------------------------
// Size cap and trimming

// fitSnapshot marshals s and, when the body is over MaxSnapshotBytes, trims it in the order of
// cloud-app.md section 3.6: items[] of drive roots to the items that are not verified, then the same
// for the internal root, then the models list down to the profile's models and the loaded ones. As a
// last resort it drops the READY documents and the drill results altogether. The schema has no
// truncation flag (every object is strict), so the cut is reported in the log only.
func fitSnapshot(s *StateSnapshot) (body []byte, trimmed []string, err error) {
	marshal := func() error {
		var e error
		body, e = json.Marshal(s)
		return e
	}
	if err = marshal(); err != nil {
		return nil, nil, fmt.Errorf("cloud: encode snapshot: %w", err)
	}
	if len(body) <= MaxSnapshotBytes {
		return body, nil, nil
	}
	steps := []struct {
		name string
		do   func()
	}{
		{"ark.drive.items", func() { trimItems(s, true) }},
		{"ark.internal.items", func() { trimItems(s, false) }},
		{"models", func() { trimModels(s) }},
		{"ark.ready", func() {
			for i := range s.Ark.Roots {
				s.Ark.Roots[i].Ready = nil
			}
		}},
		{"drill.results", func() {
			if s.Drill != nil {
				s.Drill.Results = nil
			}
		}},
	}
	for _, st := range steps {
		st.do()
		trimmed = append(trimmed, st.name)
		if err = marshal(); err != nil {
			return nil, nil, fmt.Errorf("cloud: encode snapshot: %w", err)
		}
		if len(body) <= MaxSnapshotBytes {
			return body, trimmed, nil
		}
	}
	return nil, trimmed, fmt.Errorf("cloud: the snapshot is %d bytes after trimming, over the %d byte limit", len(body), MaxSnapshotBytes)
}

// trimItems keeps only the items that are not verified in the READY documents of drive roots (drive
// true) or of the internal root (drive false).
func trimItems(s *StateSnapshot, drive bool) {
	for i := range s.Ark.Roots {
		r := &s.Ark.Roots[i]
		if r.Ready == nil || (r.ID == "internal") == drive {
			continue
		}
		kept := make([]ReadyItem, 0, len(r.Ready.Items))
		for _, it := range r.Ready.Items {
			if it.Status != "verified" {
				kept = append(kept, it)
			}
		}
		r.Ready.Items = kept
	}
}

// trimModels keeps the profile's models and the loaded and loading ones (agent.md section 4.8.4).
func trimModels(s *StateSnapshot) {
	kept := make([]SnapModel, 0, len(s.Models))
	for _, m := range s.Models {
		if m.inProfile || m.Status == "loaded" || m.Status == "loading" {
			kept = append(kept, m)
		}
	}
	s.Models = kept
}

// ---------------------------------------------------------------------------------------------------
// The scrubber pass (agent.md section 4.8.4: never keys, tokens, tailnet addresses or names, paths)

var homePathRE = regexp.MustCompile(`(^|[^A-Za-z0-9_])/home/[A-Za-z0-9._-]+`)
var lanIPRE = regexp.MustCompile(`(^|[^0-9.])(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})([^0-9.]|$)`)

// secretLike reports whether s must not leave the node: whatever logx.ScrubString changes (bearer
// credentials, credential-prefixed tokens, 64-hex keys, tailnet addresses and names), a path under
// /home and a LAN address.
func secretLike(s string) bool {
	if len(s) < 6 {
		return false
	}
	return logx.ScrubString(s) != s || homePathRE.MatchString(s) || lanIPRE.MatchString(s)
}

// scrubValue walks v and zeroes every string that is secretLike (a string becomes "", a *string nil,
// an element of a string slice is removed). It returns the paths of what it dropped, never the values.
func scrubValue(v reflect.Value, path string) []string {
	var dropped []string
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
				if name == "" || name == "-" {
					continue
				}
				f := v.Field(i)
				walkField(f, join(path, name), &dropped, walk)
			}
		case reflect.Slice:
			if v.Type().Elem().Kind() == reflect.String {
				kept := reflect.MakeSlice(v.Type(), 0, v.Len())
				for i := 0; i < v.Len(); i++ {
					if secretLike(v.Index(i).String()) {
						dropped = append(dropped, fmt.Sprintf("%s[%d]", path, i))
						continue
					}
					kept = reflect.Append(kept, v.Index(i))
				}
				if kept.Len() != v.Len() && v.CanSet() {
					v.Set(kept)
				}
				return
			}
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
			}
		case reflect.Map:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			for _, k := range keys {
				mv := v.MapIndex(k)
				if mv.Kind() == reflect.Interface && !mv.IsNil() {
					if s, ok := mv.Elem().Interface().(string); ok && secretLike(s) {
						v.SetMapIndex(k, reflect.ValueOf(""))
						dropped = append(dropped, join(path, k.String()))
					}
				}
			}
		}
	}
	walk(v, path)
	return dropped
}

func walkField(f reflect.Value, path string, dropped *[]string, walk func(reflect.Value, string)) {
	switch f.Kind() {
	case reflect.String:
		if secretLike(f.String()) {
			f.SetString("")
			*dropped = append(*dropped, path)
		}
	case reflect.Pointer:
		if f.IsNil() {
			return
		}
		if f.Elem().Kind() == reflect.String {
			if secretLike(f.Elem().String()) {
				f.Set(reflect.Zero(f.Type()))
				*dropped = append(*dropped, path)
			}
			return
		}
		walk(f.Elem(), path)
	default:
		walk(f, path)
	}
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// ---------------------------------------------------------------------------------------------------
// Small helpers

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func timeOut(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := rfc3339(t)
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func capPtr(s *string, n int) *string {
	if s == nil || *s == "" {
		return nil
	}
	c := cut(*s, n)
	return &c
}

func clampPtr(v *float64, lo, hi float64) *float64 {
	if v == nil {
		return nil
	}
	c := clampF(*v, lo, hi)
	return &c
}

func codeOrNil(s string) *string {
	if !alertCodeRE.MatchString(s) {
		return nil
	}
	return &s
}

func capStrings(in []string, maxN, maxLen int) []string {
	out := []string{}
	for _, s := range in {
		if len(out) == maxN {
			break
		}
		out = append(out, cut(s, maxLen))
	}
	return out
}

func capCodes(in []string, maxN int) []string {
	out := []string{}
	for _, s := range in {
		if len(out) == maxN {
			break
		}
		if alertCodeRE.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// oneOf returns v when it is one of allowed, else def.
func oneOf(v, def string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}
