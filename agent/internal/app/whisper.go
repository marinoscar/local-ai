package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
)

// WhisperUnit is /etc/systemd/system/localai-whisper.service.
func (p Paths) WhisperUnit() string { return whisper.DefaultUnitPath(p.Root) }

// WhisperEnv is /etc/localai/whisper.env.
func (p Paths) WhisperEnv() string { return p.Join(whisper.EnvPath) }

// whisperState is what the agent keeps for the speech service: the in-memory start-failure fallback
// (companion-services.md section 5) and the last resolved selection.
type whisperState struct {
	fb whisper.Fallback
	// mu serializes applyWhisper; device is the device of the last applied selection (a string).
	mu     sync.Mutex
	device atomic.Value
	// reserve is the graphics memory (MiB) the last applied selection reserves on the gpu: the model's
	// min_vram_mib, 0 on the cpu or without a model. The router's fit rules read it as W_res through
	// whisperReserveMiB.
	reserve atomic.Int64
}

// whisperReserveMiB is W_res of the router's fit rules (llama-router.md section 4.3.1, companion-services.md
// section 4.2.3): the resolved model's min_vram_mib while whisper's device is gpu and localai-whisper is not
// running yet; 0 otherwise, because once the unit runs its memory is already measured in G_others and
// counting it again would refuse loads that fit. A unit that cannot be read counts as not running: the
// reservation is the safe side of a load that is about to share the card.
func (d *Daemon) whisperReserveMiB() int64 {
	reserve := d.whisper.reserve.Load()
	if reserve <= 0 {
		return 0
	}
	if d.c != nil && d.c.Systemd != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if st, err := d.c.Systemd.State(ctx, services.UnitWhisper); err == nil && st.Active() {
			return 0
		}
	}
	return reserve
}

// writeWhisperUnit writes localai-whisper.service and reloads systemd (companion-services.md section
// 3.3). It never starts, stops or restarts the unit: whisper.env, written by applyWhisper, does.
func (d *Daemon) writeWhisperUnit(ctx context.Context) {
	if d.c.Systemd == nil {
		return
	}
	w := &whisper.UnitWriter{Path: d.c.Paths.WhisperUnit(), Systemd: d.c.Systemd}
	if d.env.Unprivileged || !d.haveLocalAI {
		w.UID, w.GID = -1, -1
	}
	res, err := w.Write(ctx)
	if err != nil {
		d.log.Error("the whisper unit could not be written", "error", err.Error())
		return
	}
	d.log.Info("the whisper unit is in place", "written", res.Written)
}

// whisperInputs gathers what whisper.Resolve decides from.
func (d *Daemon) whisperInputs() whisper.Inputs {
	cfg := d.c.Config.Get()
	cs := d.c.Catalog.Get()
	var prof *catalog.Profile
	if cs.Catalog != nil {
		prof, _ = cs.Catalog.Profile(cfg.Profile)
	}
	in := whisper.Inputs{
		Config: whisper.ConfigFromNode(cfg), Catalog: cs.Catalog, Profile: prof,
		AddModels: cfg.ProfileOverrides.AddModels, LlamaBackend: cfg.Llama.Backend,
		BuildBackend: whisper.CurrentBuildBackend(d.c.Paths.Join(whisper.Root)),
		ForceCPU:     d.whisper.fb.ForceCPU(),
		Hardware:     whisper.Hardware{FreeVRAMMiB: -1},
	}
	if d.c.ArkIndex != nil {
		in.Copies = d.c.ArkIndex
	}
	if d.c.Hardware != nil {
		snap := d.c.Hardware.Snapshot()
		backend := whisperBackend(cfg)
		if backend == "" || backend == "auto" {
			backend = snap.Backend
		}
		in.Hardware.PhysicalCores = snap.CPU.PhysicalCores
		in.Hardware.TotalVRAMMiB = snap.FitMemory(backend)
		if in.Hardware.TotalVRAMMiB > 0 && !snap.SampledAt.IsZero() {
			in.Hardware.FreeVRAMMiB = in.Hardware.TotalVRAMMiB - snap.FitUsedMiB(backend)
		}
	}
	return in
}

// applyWhisper resolves the speech setup and makes whisper.env match: written atomically, the unit
// restarted only when the bytes changed, stopped (LAI-SVC-010) when no model is installed. It is safe
// to call from any trigger; calls are serialized.
func (d *Daemon) applyWhisper(ctx context.Context) {
	if !blockEnabled(d.c.Config.Get(), "whisper") || d.c.Systemd == nil || d.c.Catalog == nil {
		d.whisper.reserve.Store(0)
		return
	}
	d.whisper.mu.Lock()
	defer d.whisper.mu.Unlock()
	sel, probs := whisper.Resolve(d.whisperInputs())
	seen := map[string]bool{}
	for _, p := range probs {
		seen[p.Code] = true
		d.raiseSeverity(p.Code, p.Severity, p.Details)
	}
	for _, code := range []string{doctor.SVC010, doctor.SVC013} {
		if !seen[code] {
			d.clearAlert(code)
		}
	}
	d.whisper.device.Store(sel.Device)
	d.whisper.reserve.Store(sel.ReserveMiB)
	w := &whisper.EnvWriter{Path: d.c.Paths.WhisperEnv(), Units: d.c.Systemd, UID: -1, GID: -1}
	if !d.env.Unprivileged && d.haveLocalAI {
		w.UID, w.GID = 0, d.localaiGID
	}
	res, err := w.Apply(ctx, sel)
	if err != nil {
		d.log.Warn("whisper.env could not be applied", "error", err.Error())
		return
	}
	if res.Written || res.Stopped {
		d.log.Info("whisper.env applied", "model", sel.ModelID, "device", sel.Device, "threads", sel.Threads,
			"written", res.Written, "restarted", res.Restarted, "stopped", res.Stopped)
	}
}

// applyWhisperAsync runs applyWhisper in the background; it can wait for a unit restart.
func (d *Daemon) applyWhisperAsync(ctx context.Context) {
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.applyWhisper(ctx)
	}()
}

// whisperWorker regenerates whisper.env when a drive comes or goes or a model finishes syncing, and
// counts failed gpu starts of the unit (whisper.Fallback): the second one switches the device to cpu
// for this boot and rewrites the env (LAI-SVC-013).
type whisperWorker struct {
	d      *Daemon
	cancel context.CancelFunc
	done   chan struct{}
}

func (w *whisperWorker) Name() string { return "whisper-env" }

func (w *whisperWorker) Start(ctx context.Context) error {
	filter, err := events.ParseFilter(events.TypeDriveMounted + "," + events.TypeDriveUnmounted + "," + events.TypeSyncItemDone)
	if err != nil {
		return err
	}
	ctx, w.cancel = context.WithCancel(ctx)
	evs, err := w.d.c.Bus.Subscribe(ctx, filter, 0)
	if err != nil {
		w.cancel()
		return err
	}
	var states <-chan services.UnitState
	if w.d.c.Systemd != nil {
		if states, err = w.d.c.Systemd.Subscribe(ctx, []string{services.UnitWhisper}); err != nil {
			w.d.log.Warn("the whisper unit cannot be watched; the cpu fallback is off", "error", err.Error())
			states = nil
		}
	}
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-evs:
				if !ok {
					return
				}
				w.d.applyWhisper(ctx)
			case st, ok := <-states:
				if !ok {
					states = nil
					continue
				}
				dev, _ := w.d.whisper.device.Load().(string)
				if w.d.whisper.fb.Observe(st, dev, w.d.env.Clock.Now()) {
					w.d.log.Warn("speech recognition failed to start twice on the graphics card; switching to the processor for this boot", "code", doctor.SVC013)
					w.d.applyWhisper(ctx)
				}
			}
		}
	}()
	return nil
}

func (w *whisperWorker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startWhisper is the startup part: the unit file, the env, and the worker for later changes.
func (d *Daemon) startWhisper(ctx context.Context) {
	if blockEnabled(d.c.Config.Get(), "whisper") {
		d.writeWhisperUnit(ctx)
		d.applyWhisperAsync(d.rootCtx)
	}
	d.startWorker(&whisperWorker{d: d})
}
