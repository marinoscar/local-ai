package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
	"github.com/marinoscar/local-ai/agent/internal/drill/media"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The drill's model checks and state restore (DRILL-03, drill.md sections 4.2 and 4.3), wired to the
// daemon. The checks and the restorer see small interfaces; this file is the one place that connects
// them to the router orchestrator, the router client, the state store and systemd.

// modelOrchestrator is the part of *router.Orchestrator the drill uses.
type modelOrchestrator interface {
	LoadWith(ctx context.Context, req router.LoadRequest) (router.LoadResult, error)
	UnloadWith(ctx context.Context, req router.UnloadRequest) (router.UnloadResult, error)
	SetDefault(ctx context.Context, id string, actor audit.Actor) (router.DefaultResult, error)
}

// routerModels is the part of the router client that lists the models.
type routerModels interface {
	Models(ctx context.Context) ([]router.ModelStatus, error)
}

// modelStateView is the part of the state store that knows the default and the load order.
type modelStateView interface {
	Snapshot() state.State
}

// drillModels is drill.ModelOps over the orchestrator: every load and unload takes the one path users
// use (llama-router.md sections 4.4 and 4.5), with its model-operations lock, its audit lines and its
// rollback. The actor is the agent itself.
type drillModels struct {
	orch   modelOrchestrator
	router routerModels
	state  modelStateView
}

var _ drill.ModelOps = drillModels{}

// Resident implements drill.ModelOps: the loaded models in restore order. The order is the node's
// state.models.desired_loaded (most recently used first, as the boot restore uses it) for the models
// it lists, then any other loaded model by id.
func (m drillModels) Resident(ctx context.Context) ([]string, error) {
	statuses, err := m.router.Models(ctx)
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, s := range statuses {
		if s.Status.Resident() {
			loaded = append(loaded, s.ID)
		}
	}
	desired := m.state.Snapshot().Models.DesiredLoaded
	rank := func(id string) int {
		if i := slices.Index(desired, id); i >= 0 {
			return i
		}
		return len(desired)
	}
	slices.SortStableFunc(loaded, func(a, b string) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	if loaded == nil {
		loaded = []string{}
	}
	return loaded, nil
}

// Default implements drill.ModelOps: the default model the node recorded, "" when none.
func (m drillModels) Default() string {
	if d := m.state.Snapshot().Models.Default; d != nil {
		return *d
	}
	return ""
}

// Load implements drill.ModelOps. It waits for the model to be ready (the orchestrator's load timeout,
// 300 s by default) and waits for the lock like a survival load does, so a user's operation that is in
// progress is not refused.
func (m drillModels) Load(ctx context.Context, id string, evict bool) (drill.LoadInfo, error) {
	res, err := m.orch.LoadWith(ctx, router.LoadRequest{
		ID: id, Wait: true, UnloadOthers: evict, Actor: audit.System(), LockWait: router.DefaultLockWait,
	})
	if err != nil {
		return drill.LoadInfo{}, modelError(err)
	}
	return drill.LoadInfo{LoadMS: res.LoadMS, AlreadyLoaded: res.AlreadyLoaded}, nil
}

// Unload implements drill.ModelOps. A model that is not loaded is not an error.
func (m drillModels) Unload(ctx context.Context, id string) error {
	_, err := m.orch.UnloadWith(ctx, router.UnloadRequest{ID: id, Actor: audit.System(), LockWait: router.DefaultLockWait})
	return modelError(err)
}

// SetDefault implements drill.ModelOps.
func (m drillModels) SetDefault(ctx context.Context, id string) error {
	_, err := m.orch.SetDefault(ctx, id, audit.System())
	return modelError(err)
}

// modelError keeps the doctor code of an orchestrator error, so a check can show why a load failed.
func modelError(err error) error {
	if err == nil {
		return nil
	}
	var oe *router.OpError
	if errors.As(err, &oe) {
		return &drill.ModelError{Code: string(oe.Code), Message: oe.Message, Err: oe.Err}
	}
	return err
}

// drillWhisper is drill.WhisperUnit over the guarded systemd manager.
type drillWhisper struct{ m services.Manager }

var _ drill.WhisperUnit = drillWhisper{}

// Running implements drill.WhisperUnit: active, or on its way up.
func (w drillWhisper) Running(ctx context.Context) (bool, error) {
	st, err := w.m.State(ctx, services.UnitWhisper)
	if err != nil {
		return false, err
	}
	switch st.ActiveState {
	case services.ActiveActive, services.ActiveActivating, services.ActiveReloading:
		return true, nil
	}
	return false, nil
}

// Start implements drill.WhisperUnit.
func (w drillWhisper) Start(ctx context.Context) error { return w.m.Start(ctx, services.UnitWhisper) }

// drillRestorer builds the state restorer the drill engine is created with (drill.md section 4.3).
func (d *Daemon) drillRestorer() (drill.Restorer, error) {
	c := d.c
	var whisper drill.WhisperUnit
	if c.Systemd != nil {
		whisper = drillWhisper{m: c.Systemd}
	}
	r, err := drill.NewStateRestorer(d.drillModels(), whisper, d.log)
	if err != nil {
		return nil, fmt.Errorf("drill restorer: %w", err)
	}
	return r, nil
}

func (d *Daemon) drillModels() drillModels {
	return drillModels{orch: d.c.Orchestrator, router: d.c.Router, state: d.c.State}
}

// registerDrillModelChecks adds checks 5, 6, 7 and 22 to the drill registry.
func (d *Daemon) registerDrillModelChecks(reg *drill.Registry) error {
	err := checks.Register(reg, checks.ModelDeps{
		Models: d.drillModels(), Chat: d.c.Router, Inputs: d.routerInputs,
		Presence: router.ManifestPresence{FS: config.OSFS{}}, Photo: media.SevenPNG(),
	})
	if err != nil {
		return fmt.Errorf("drill model checks: %w", err)
	}
	return nil
}
