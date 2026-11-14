package app

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/svc/acestep"
)

// The music engine's wiring (E11, #508/#511): the acestep adapter the orchestrator loads music models
// with, its unit file, the runtime doctor checks and the workload resolution. The song routes are
// internal/music's.

// musicRuntime is the runtime check for this node's backend.
func (d *Daemon) musicRuntime() acestep.Runtime {
	return acestep.Runtime{Root: d.c.Paths.Join(acestep.RuntimeRoot), Backend: func() string { return d.c.Config.Get().Llama.Backend }}
}

// newMusicAdapter builds the acestep adapter over the guarded systemd manager.
func (d *Daemon) newMusicAdapter() (*acestep.Adapter, error) {
	env := acestep.EnvFile{Path: d.c.Paths.Join(acestep.EnvPath), UID: -1, GID: -1}
	if !d.env.Unprivileged && d.haveLocalAI {
		env.UID, env.GID = 0, d.localaiGID
	}
	return acestep.NewAdapter(acestep.Options{
		Units:     d.c.Systemd,
		Health:    acestep.HTTPHealth{Port: func() int { return d.c.Config.Get().Ports.Music }},
		Env:       env.Write,
		EnvBase:   func() acestep.Env { return acestep.Env{Port: d.c.Config.Get().Ports.Music} },
		Installed: d.musicRuntime().Installed,
		// GPUHeld is not wired yet: the stop waits for the unit only (TODO(#505): per-unit VRAM from
		// the hardware sampler's process list).
		Logger: d.log,
	})
}

// musicWanted reports whether the node wants music: an acestep model is in the profile's or
// node.yaml's model list, or in a workload.
func (d *Daemon) musicWanted() bool {
	in := d.routerInputs()
	if in.Catalog == nil {
		return false
	}
	isMusic := func(id string) bool {
		m, ok := in.Catalog.Model(id)
		return ok && m.Engine == catalog.EngineAcestep
	}
	for id := range router.DesiredModels(in.Profile, in.Overrides) {
		if isMusic(id) {
			return true
		}
	}
	for _, w := range in.Workloads {
		for _, id := range w.Models {
			if isMusic(id) {
				return true
			}
		}
	}
	return false
}

// gpuDriver is the NVIDIA driver version of the first GPU that reports one.
func (d *Daemon) gpuDriver() string {
	for _, g := range d.c.Hardware.Snapshot().GPUs {
		if g.Vendor == hardware.VendorNVIDIA && g.Driver != nil {
			return *g.Driver
		}
	}
	return ""
}

// registerMusicChecks adds LAI-SVC-150, LAI-SVC-159 and the workload check (LAI-CAT-020).
func (d *Daemon) registerMusicChecks() error {
	for _, c := range acestep.DoctorChecks(acestep.CheckOptions{Wanted: d.musicWanted, Runtime: d.musicRuntime(), Driver: d.gpuDriver}) {
		if err := d.c.Checks.Add(c); err != nil {
			return err
		}
	}
	return d.c.Checks.Add(doctor.Check{ID: "catalog.workloads", Area: "CAT", Fast: true, Run: func(context.Context) []doctor.Result {
		var out []doctor.Result
		for _, dw := range d.droppedWorkloads() {
			out = append(out, doctor.Result{Code: doctor.CAT020, Details: map[string]any{"workload": dw.Name, "models": dw.Unknown}})
		}
		return out
	}})
}

// resolveWorkloads is the node's workloads: the profile's merged with node.yaml's.
func (d *Daemon) resolveWorkloads(cat *catalog.Catalog, prof *catalog.Profile) []router.Workload {
	ws, _ := router.ResolveWorkloads(cat, prof, d.c.Config.Get().Workloads)
	return ws
}

func (d *Daemon) droppedWorkloads() []router.DroppedWorkload {
	cfg := d.c.Config.Get()
	cs := d.c.Catalog.Get()
	prof, _ := cs.Catalog.Profile(cfg.Profile)
	_, dropped := router.ResolveWorkloads(cs.Catalog, prof, cfg.Workloads)
	return dropped
}

// writeMusicUnit writes localai-music.service (never enabled, never started here) when music is wanted.
func (d *Daemon) writeMusicUnit(ctx context.Context) {
	if d.c.Systemd == nil || !d.musicWanted() {
		return
	}
	w := &acestep.UnitWriter{Path: d.c.Paths.Join(acestep.DefaultUnitPath("")), Systemd: d.c.Systemd}
	if d.env.Unprivileged || !d.haveLocalAI {
		w.UID, w.GID = -1, -1
	}
	written, err := w.Write(ctx)
	if err != nil {
		d.log.Error("the music unit could not be written", "error", err.Error())
		return
	}
	d.log.Info("the music unit is in place", "written", written)
}

// musicEngineModels is the cached picture of the music engine for GET /models.
func (d *Daemon) musicEngineModels() []router.EngineModel {
	if d.acestep == nil {
		return nil
	}
	return d.acestep.Cached()
}
