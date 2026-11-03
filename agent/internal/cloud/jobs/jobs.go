// Package jobs holds the executors of the cloud command plane (AGENT-18): one strict decoder and one
// executor for each of the 14 job types of agent.md section 4.8.6, behind the cloud.Handler interface
// the claim loop of internal/cloud runs.
//
// Every executor calls the same internal method as the REST route of the same operation, with the
// principal audit.Cloud(jobID): the router orchestrator for load, unload and default, modelsapi.Service
// for download, delete and the key, api.ServicesDeps.RunAction for a service restart,
// statusapi.Deps.RefreshCatalog for a catalog refresh, the journal reader for logs and the mode engine
// for mode.offline. So a cloud command gets the same checks, the same audit lines and the same typed
// errors as a local request, and no code in this package decides anything about a model, a file or a
// unit: it maps a validated params object to one call and the call's outcome to the result schema of
// cloud-app.md section 3.7.
//
// The cloud is untrusted (threat T4): the decoders accept the closed set of fields and patterns of
// params.go and nothing else; no params object can name a command, a path or a unit file. A node
// declines a job type either through cloud.allow (the claim loop) or by not providing the dependency
// of the type (Handlers leaves the type out, so it is refused with LAI-CLD-057 like a type missing from
// cloud.allow).
//
// Every dependency is a small interface with a fake in the tests; nothing here touches the network,
// systemd or the router directly. Nothing registers at init time: the daemon wiring (AGENT-20) builds
// Deps and passes Handlers(Deps) to cloud.NewRegistry.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// Router is the load orchestrator: *router.Orchestrator implements it. The three calls take the
// cloud's LockWait (router.DefaultLockWait, 60 s) for modelOpsMu instead of failing at once
// (agent.md section 4.9).
type Router interface {
	LoadWith(ctx context.Context, req router.LoadRequest) (router.LoadResult, error)
	UnloadWith(ctx context.Context, req router.UnloadRequest) (router.UnloadResult, error)
	SetDefault(ctx context.Context, id string, actor audit.Actor) (router.DefaultResult, error)
}

// Models is the model and key service of the routes: *modelsapi.Service implements it.
type Models interface {
	Get(ctx context.Context, id string) (modelsapi.ModelDetail, error)
	Download(ctx context.Context, req modelsapi.DownloadRequest) (modelsapi.DownloadAccepted, error)
	Delete(ctx context.Context, req modelsapi.DeleteRequest) (modelsapi.DeleteResult, error)
	RotateKey(ctx context.Context, req modelsapi.KeyRotateRequest) (modelsapi.KeyRotated, error)
}

// ArkRequest is the part of an ark sync or verify request a cloud command sets; the trigger is
// "cloud" (ark-sync.md section 3.5) for an Actor of kind cloud.
type ArkRequest struct {
	Roots []string
	Items []string
	// DryRun is for sync only: plan, write nothing.
	DryRun bool
	Actor  audit.Actor
}

// ArkSyncStarted is what starting a sync reports.
type ArkSyncStarted struct {
	// RunID is the run's id; empty for a dry run (no run exists).
	RunID string
	// Plan is the plan of the run (or of the dry run).
	ItemsToDownload, BytesToDownload, ItemsSkippedBudget int64
}

// ArkVerifyStarted is what starting a verify reports.
type ArkVerifyStarted struct {
	RunID        string
	ItemsPlanned int64
}

// Ark is the sync engine's start calls (ark-sync.md section 3.5). The engine (SYNC-07 and later)
// implements it, or an adapter over it does. Both return at the start of the run, never at its end.
// An engine with a run already active returns *modelsapi.RunActiveError; the executors answer
// started: false with that run's id.
type Ark interface {
	StartSync(ctx context.Context, req ArkRequest) (ArkSyncStarted, error)
	StartVerify(ctx context.Context, req ArkRequest) (ArkVerifyStarted, error)
}

// Modes is the mode engine: *modes.Engine implements it.
type Modes interface {
	Request(ctx context.Context, target modes.Mode, o modes.RequestOptions) (modes.Result, error)
	Info() modes.Info
}

// Services runs a unit action: api.ServicesDeps implements it (RunAction).
type Services interface {
	RunAction(ctx context.Context, req api.ServiceActionRequest) (api.ServiceActionResult, error)
}

// Catalog refreshes the catalog: statusapi.Deps implements it (RefreshCatalog).
type Catalog interface {
	RefreshCatalog(ctx context.Context, actor audit.Actor) (statusapi.CatalogRefreshBody, error)
}

// Link is the cloud link: *cloud.Link implements it.
type Link interface {
	// PushNow sends a state snapshot now (status.refresh).
	PushNow(ctx context.Context) (cloud.StateRefresh, error)
	// Push sends the llama key now; status.refresh uses it when the cloud has not got the key yet.
	Push(ctx context.Context) error
}

// Status is the status builder: *statusapi.StatusBuilder implements it. The executors read the
// installed catalog version, the default model and the GPU memory in use from it.
type Status interface {
	Build(ctx context.Context, forCloud bool, o statusapi.BuildOptions) statusapi.Status
}

// StateView is the state store: *state.Store implements it.
type StateView interface {
	Snapshot() state.State
}

// Hardware samples the hardware again: *hardware.Service implements it. model.load asks for a fresh
// sample before it reads vram_used_mib.
type Hardware interface {
	Refresh(ctx context.Context) hardware.Snapshot
}

// AgentUpdater is the seam of AGENT-19: the self-update behind agent.update. The executor of this
// package decodes {version} and hands over; the updater runs agent.md section 4.10 and returns the
// outcome. A successful update replaces the process, so it returns an Outcome with Defer set (the
// job stays in inflight.json and the new process settles it after its health check); an update that
// does not go ahead ("already_current") returns the result at once. Without an updater the type
// fails with LAI-AGT-023.
type AgentUpdater interface {
	Update(ctx context.Context, job cloud.Job, version string) (cloud.Outcome, error)
}

// Deps are what the executors need. A job type whose dependency is nil is left out of Handlers.
type Deps struct {
	Router   Router
	Models   Models
	Ark      Ark
	Modes    Modes
	Services Services
	// Logs reads the journal for logs.tail: *services.Journal implements api.LogTailer.
	Logs     api.LogTailer
	Catalog  Catalog
	Link     Link
	Status   Status
	State    StateView
	Hardware Hardware
	// AgentUpdate is AGENT-19's updater; nil makes agent.update fail with LAI-AGT-023.
	AgentUpdate AgentUpdater

	// Clock times the wait for modelOpsMu; nil is the wall clock.
	Clock  cloud.Clock
	Logger *slog.Logger
}

func (d Deps) clock() cloud.Clock {
	if d.Clock == nil {
		return cloud.RealClock{}
	}
	return d.Clock
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.Default().With("component", "cloud")
	}
	return d.Logger.With("component", "cloud")
}

// Handlers returns the executors the dependencies allow, ready for cloud.NewRegistry. The types, in
// the order of the cloud.allow default: status.refresh (needs Link, Status and State), model.load
// (Router, Status), model.unload and model.set_default (Router), model.download and model.delete
// (Models), ark.sync and ark.verify (Ark), mode.offline (Modes), key.rotate (Models),
// service.restart (Services), logs.tail (Logs), catalog.refresh (Catalog, Status); agent.update is
// always there.
func Handlers(d Deps) []cloud.Handler {
	var hs []cloud.Handler
	add := func(ok bool, h cloud.Handler) {
		if ok {
			hs = append(hs, h)
		}
	}
	add(d.Link != nil && d.State != nil, newStatusRefresh(d))
	add(d.Router != nil && d.Status != nil, newModelLoad(d))
	add(d.Router != nil, newModelUnload(d))
	add(d.Router != nil, newModelSetDefault(d))
	add(d.Models != nil, newModelDownload(d))
	add(d.Models != nil, newModelDelete(d))
	add(d.Ark != nil, newArkSync(d))
	add(d.Ark != nil, newArkVerify(d))
	add(d.Modes != nil, newModeOffline(d))
	add(d.Models != nil, newKeyRotate(d))
	add(d.Services != nil, newServiceRestart(d))
	add(d.Logs != nil, newLogsTail(d))
	add(d.Catalog != nil && d.Status != nil, newCatalogRefresh(d))
	add(true, newAgentUpdate(d))
	return hs
}

// typed is a Handler over a params type P: the decoder returns a P and the executor receives it.
type typed[P any] struct {
	name        string
	cancellable bool
	inflight    bool
	decode      func(raw json.RawMessage) (P, error)
	run         func(ctx context.Context, job cloud.Job, p P) (cloud.Outcome, error)
}

var _ cloud.Handler = typed[noParams]{}

func (h typed[P]) Type() string      { return h.name }
func (h typed[P]) Cancellable() bool { return h.cancellable }

// NeedsInflight says that the claim loop must refuse the command when inflight.json cannot be
// written (agent.update: the new process settles it from that file).
func (h typed[P]) NeedsInflight() bool { return h.inflight }

func (h typed[P]) Decode(raw json.RawMessage) (any, error) {
	p, err := h.decode(raw)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (h typed[P]) Run(ctx context.Context, job cloud.Job, params any) (cloud.Outcome, error) {
	p, ok := params.(P)
	if !ok {
		return cloud.Outcome{}, &cloud.JobError{Code: doctor.AGT099, Message: "The command was decoded for a different type."}
	}
	out, err := h.run(ctx, job, p)
	return out, mapError(err)
}

// mapError gives an error of an operation the form the claim loop reports: the orchestrator's
// *router.OpError becomes a *cloud.JobError with its code and message; an *api.Error, a deadline and
// every other error pass through (cloud.CodeAndMessage handles them).
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var oe *router.OpError
	var busy *router.BusyError
	var je *cloud.JobError
	var ae *api.Error
	switch {
	case errors.As(err, &je), errors.As(err, &ae):
		return err
	case errors.As(err, &oe):
		return &cloud.JobError{Code: string(oe.Code), Message: oe.Text("en")}
	case errors.As(err, &busy):
		return &cloud.JobError{Code: doctor.AGT022, Message: "Another model operation is running."}
	case errors.Is(err, router.ErrInterrupted):
		return &cloud.JobError{Code: doctor.AGT024, Message: "The agent is shutting down."}
	}
	return err
}

// isBusy reports whether err says that another model operation holds modelOpsMu (and nothing has been
// touched): LAI-AGT-022 without the "loaded" reason of a delete, which no waiting would change.
func isBusy(err error) bool {
	var oe *router.OpError
	var busy *router.BusyError
	var ae *api.Error
	switch {
	case errors.As(err, &busy):
		return true
	case errors.As(err, &oe):
		return oe.Code == router.CodeBusy
	case errors.As(err, &ae):
		if ae.Code != doctor.AGT022 {
			return false
		}
		reason, _ := ae.Details["reason"].(string)
		return reason != "loaded"
	}
	return false
}

// Waiting for modelOpsMu: operations the orchestrator cannot wait for itself (a delete, a key
// rotation, a service restart) try again every second for up to a minute, then fail with the busy
// error (agent.md section 4.9: cloud jobs wait up to 60 s).
const (
	busyPoll = time.Second
	busyWait = router.DefaultLockWait
)

// retryBusy calls fn until it stops answering "busy" or the wait has run out; the last error comes
// back when it has.
func retryBusy[T any](ctx context.Context, d Deps, fn func() (T, error)) (T, error) {
	clk := d.clock()
	deadline := clk.Now().Add(busyWait)
	for {
		res, err := fn()
		if err == nil || !isBusy(err) || !clk.Now().Before(deadline) {
			return res, err
		}
		t := clk.NewTimer(busyPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return res, err
		case <-t.C():
		}
	}
}

func statusOptions() statusapi.BuildOptions { return statusapi.BuildOptions{Lang: "en"} }

// jobErr builds a *cloud.JobError.
func jobErr(code, format string, a ...any) error {
	return &cloud.JobError{Code: code, Message: fmt.Sprintf(format, a...)}
}
