package statusapi

import (
	"context"
	"os"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// StatusSources are the views StatusBuilder composes GET /status from. Every field is optional (see
// the package views): a missing one renders its empty state.
type StatusSources struct {
	Config    ConfigView
	State     StateView
	Hardware  HardwareView
	Mode      ModeView
	Services  ServiceView
	Models    ModelView
	Ark       ArkView
	Cloud     CloudView
	Tailscale TailscaleView
	Alerts    AlertView
	Counters  CounterView
	Catalog   *CatalogStore

	// CheckedAt is when the connectivity prober last finished a pass (ConnectivityProber.LastCheckedAt).
	CheckedAt func() time.Time
	// Version is the agent version (node.agent_version).
	Version string
	// Hostname defaults to os.Hostname.
	Hostname func() (string, error)
	// StartedAt is when the agent started, for node.uptime_s. Zero gives an uptime of 0.
	StartedAt time.Time
	// Now defaults to time.Now.
	Now func() time.Time
}

// BuildOptions are the per-request choices of Build.
type BuildOptions struct {
	// Lang is the language of alert titles and fixes: "es", anything else is English.
	Lang string
	// Counters adds node.counters (?counters=1).
	Counters bool
}

// StatusBuilder composes the GET /status body from the views. It is also the source of the state
// snapshot the cloud link pushes (Build with forCloud).
type StatusBuilder struct{ src StatusSources }

// NewStatusBuilder returns a StatusBuilder over src.
func NewStatusBuilder(src StatusSources) *StatusBuilder {
	if src.Now == nil {
		src.Now = time.Now
	}
	if src.Hostname == nil {
		src.Hostname = os.Hostname
	}
	return &StatusBuilder{src: src}
}

// Build returns the status body. With forCloud the body is the one pushed to the cloud (agent.md
// section 4.8.4): the same, minus alerts[].details and hardware.gpus[].processes, in English, and
// without counters.
func (b *StatusBuilder) Build(ctx context.Context, forCloud bool, o BuildOptions) Status {
	s := b.src
	now := s.Now()
	lang := o.Lang
	if forCloud {
		lang, o.Counters = doctor.LangEN, false
	}
	if lang != doctor.LangES {
		lang = doctor.LangEN
	}

	cfg := config.Defaults()
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	var st state.State
	haveState := s.State != nil
	if haveState {
		st = s.State.Snapshot()
	}
	var snap hardware.Snapshot
	if s.Hardware != nil {
		snap = s.Hardware.Snapshot()
	}
	var models ModelsView
	if s.Models != nil {
		models = s.Models.Models(lang)
	}
	catalogVersion := ""
	if s.Catalog != nil {
		catalogVersion = s.Catalog.Get().Catalog.Version
	}

	out := Status{
		Node:         b.node(cfg, st, snap, catalogVersion, now),
		Mode:         b.mode(st, haveState),
		Connectivity: b.connectivity(st),
		Hardware:     hardwareBlock(snap, forCloud),
		Services:     b.services(ctx),
		Models:       modelsBlock(models),
		Ark:          b.ark(),
		Cloud:        b.cloud(),
		Tailscale:    b.tailscale(cfg),
		Alerts:       b.alerts(lang, forCloud),
	}
	if o.Counters {
		c := map[string]any{}
		if s.Counters != nil {
			for k, v := range s.Counters.Counters() {
				c[k] = v
			}
		}
		out.Node.Counters = c
	}
	return out
}

func (b *StatusBuilder) node(cfg config.Config, st state.State, snap hardware.Snapshot, catalogVersion string, now time.Time) NodeBlock {
	s := b.src
	host, _ := s.Hostname()
	backend := cfg.Llama.Backend
	if backend == "" {
		backend = snap.Backend
	}
	if backend == "" {
		backend = hardware.BackendCPU
	}
	mode := cfg.Llama.Mode
	if mode == "" {
		mode = "router"
	}
	// llama_build is the build id state.json records (f3586e66b-cuda), not node.yaml llama.build, which
	// is the directory of the current build.
	build := st.Llama.Build
	var uptime int64
	if !s.StartedAt.IsZero() && now.After(s.StartedAt) {
		uptime = int64(now.Sub(s.StartedAt) / time.Second)
	}
	return NodeBlock{
		ID: cfg.Node.ID, DisplayName: cfg.Node.DisplayName, Hostname: host, Profile: strPtr(cfg.Profile),
		AgentVersion: s.Version, Backend: backend, LlamaMode: mode, LlamaBuild: strPtr(build),
		CatalogVersion: strPtr(catalogVersion), UptimeS: uptime, Time: rfc3339(now),
	}
}

// mode is the mode block: the engine's picture, else state.json's, else offline (the safe value: a
// node that cannot tell exposes nothing and pulls nothing).
func (b *StatusBuilder) mode(st state.State, haveState bool) ModeBlock {
	if b.src.Mode != nil {
		info := b.src.Mode.Info()
		out := ModeBlock{Value: string(info.Mode)}
		if info.Since != nil {
			out.Since = timePtr(*info.Since)
		}
		if t := info.Transition; t != nil {
			ws := make([]WarningBlock, 0, len(t.Warnings))
			for _, w := range t.Warnings {
				ws = append(ws, WarningBlock{Code: w.Code, Message: w.Message})
			}
			out.Transition = &TransitionBlock{
				OpID: t.OpID, From: string(t.From), Target: string(t.Target), Step: t.Step,
				StartedAt: rfc3339(t.StartedAt), Warnings: ws,
			}
		}
		return out
	}
	if haveState {
		out := ModeBlock{Value: string(st.Mode)}
		if st.ModeChangedAt != nil {
			out.Since = timePtr(*st.ModeChangedAt)
		}
		return out
	}
	return ModeBlock{Value: string(modes.Offline)}
}

func (b *StatusBuilder) connectivity(st state.State) ConnectivityBlock {
	out := ConnectivityBlock{Value: st.Connectivity.Value}
	if out.Value == "" {
		out.Value = state.ConnNotChecked
	}
	if st.Connectivity.NoneSince != nil {
		out.NoneSince = timePtr(*st.Connectivity.NoneSince)
	}
	if b.src.CheckedAt != nil {
		out.CheckedAt = timePtr(b.src.CheckedAt())
	}
	return out
}

func hardwareBlock(snap hardware.Snapshot, forCloud bool) HardwareBlock {
	h := HardwareBlock{
		RAMTotalMiB: snap.RAM.TotalMiB, RAMAvailableMiB: snap.RAM.AvailableMiB,
		CPUTempC: snap.Thermal.CPUTempC, GPUs: []GPUBlock{},
		Power: PowerBlock{
			Source: snap.Power.Source, BatteryPercent: snap.Power.BatteryPercent, TimeRemainingS: snap.Power.TimeRemainingS,
			Profile: snap.Power.Profile, Stale: snap.Power.Stale,
		},
	}
	if h.Power.Source == "" {
		h.Power.Source = hardware.SourceUnknown
	}
	if snap.Power.HasBattery() {
		charging := snap.Power.Charging
		h.Power.Charging = &charging
	}
	if len(snap.GPUs) > 0 {
		name := snap.GPUs[0].Name
		var total, used int64
		for _, g := range snap.GPUs {
			total += g.VRAMTotalMiB
			used += g.VRAMUsedMiB
			row := GPUBlock{
				Index: g.Index, Vendor: g.Vendor, Name: g.Name, VRAMTotalMiB: g.VRAMTotalMiB, VRAMUsedMiB: g.VRAMUsedMiB,
				TempC: g.TempC, UtilPct: g.UtilPct, Throttling: append([]string{}, g.Throttling...),
			}
			if !forCloud {
				for _, p := range g.Processes {
					row.Processes = append(row.Processes, GPUProcess{PID: p.PID, Name: p.Name, VRAMMiB: p.VRAMMiB})
				}
			}
			h.GPUs = append(h.GPUs, row)
		}
		h.GPU, h.VRAMTotalMiB, h.VRAMUsedMiB = &name, &total, &used
		if t, ok := snap.HottestGPUTempC(); ok {
			h.GPUTempC = &t
		}
	}
	return h
}

func (b *StatusBuilder) services(ctx context.Context) []ServiceRow {
	rows := []ServiceRow{}
	if b.src.Services == nil {
		return rows
	}
	for _, s := range b.src.Services.Snapshot(ctx) {
		rows = append(rows, ServiceRow{
			Name: s.Name, Unit: s.Unit, Active: s.Active, Sub: s.Sub, Since: timePtr(s.Since),
			Restarts: s.Restarts, Healthy: s.Healthy, State: s.Health,
		})
	}
	return rows
}

func modelsBlock(v ModelsView) ModelsBlock {
	out := ModelsBlock{Default: strPtr(v.Default), Loaded: []string{}, Loading: []string{}, ModelsMax: v.ModelsMax}
	for _, r := range v.Rows {
		if r.Present {
			out.Present++
		}
		if r.Loadable {
			out.Loadable++
		}
		switch r.Status {
		case ModelLoaded:
			out.Loaded = append(out.Loaded, r.ID)
		case ModelLoading:
			out.Loading = append(out.Loading, r.ID)
		}
	}
	return out
}

// ark is the ark block; without a view it is the empty state: no roots, no drive, an idle sync.
func (b *StatusBuilder) ark() ArkBlock {
	out := ArkBlock{Roots: []ArkRootRow{}, Sync: ArkSyncRow{State: "idle"}}
	if b.src.Ark == nil {
		return out
	}
	info := b.src.Ark.Ark()
	for _, r := range info.Roots {
		out.Roots = append(out.Roots, ArkRootRow{Path: r.Path, Kind: r.Kind, Status: r.Status, ReadyPct: r.ReadyPct, UpdatedAt: timePtr(r.UpdatedAt)})
	}
	out.Drive.Mounted = info.DriveMounted
	if info.SyncState != "" {
		out.Sync.State = info.SyncState
	}
	out.Sync.LastSuccessAt = timePtr(info.LastSuccessAt)
	return out
}

// cloud is the cloud block; without a view the node counts as not enrolled.
func (b *StatusBuilder) cloud() CloudBlock {
	out := CloudBlock{State: "unenrolled"}
	if b.src.Cloud == nil {
		return out
	}
	c := b.src.Cloud.Cloud()
	out.Enrolled, out.URL, out.LastHeartbeatAt = c.Enrolled, strPtr(c.URL), timePtr(c.LastHeartbeatAt)
	if c.State != "" {
		out.State = c.State
	}
	return out
}

// tailscale is the tailscale block; without a manager it reports the configuration and an unapplied,
// unread serve state.
func (b *StatusBuilder) tailscale(cfg config.Config) TailscaleBlock {
	if b.src.Tailscale != nil {
		t := b.src.Tailscale.Status()
		return TailscaleBlock{Manage: t.Manage, Pattern: normalPattern(t.Pattern), Applied: t.Applied, BackendState: strPtr(t.BackendState)}
	}
	return TailscaleBlock{Manage: cfg.Tailscale.Manage, Pattern: normalPattern(cfg.Tailscale.ServePattern)}
}

func normalPattern(p string) string {
	switch p {
	case modes.PatternA, modes.PatternB:
		return p
	}
	return modes.PatternNone
}

func (b *StatusBuilder) alerts(lang string, forCloud bool) []AlertRow {
	rows := []AlertRow{}
	if b.src.Alerts == nil {
		return rows
	}
	for _, a := range b.src.Alerts.Active() {
		a = a.In(lang)
		row := AlertRow{Code: a.Code, Severity: string(a.Severity), Title: a.Title, Fix: a.Fix, Since: timePtr(a.Since)}
		if !forCloud && len(a.Details) > 0 {
			row.Details = a.Details
		}
		rows = append(rows, row)
	}
	return rows
}
