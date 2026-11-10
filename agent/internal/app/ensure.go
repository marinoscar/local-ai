package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/cloud/jobs"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/ensure/host"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// The ensure engine in the daemon (ADR-0010, rebuild-kit.md section 4.5, agent.md section 3.8.33): the
// agent keeps the node complete. internal/ensure decides and acts through ports; this file builds the real
// ones from the stack (the kit roots and the node facts of the ark side, the service controller, the audit
// log and the doctor), registers GET, POST and DELETE /ensure, and starts the worker that turns "a sync run
// ended", "the agent started" and "the node went online" into an automatic pass.

// Delays of the triggers: long enough for the thing that caused them to finish (the run slot is released
// just after sync.done; the services come up after start; the network settles after mode_online).
var ensureDelays = struct{ Start, Sync, Online, Cfg time.Duration }{
	Start: 90 * time.Second, Sync: 15 * time.Second, Online: 30 * time.Second, Cfg: 5 * time.Second,
}

// ensureStack is the engine as the daemon holds it.
type ensureStack struct {
	d    *Daemon
	svc  *ensure.Service
	host *host.Host
}

// buildEnsure builds the engine over the ark stack. It returns nil when there is no ark stack: the routes
// then answer 503 like the other ark routes.
func (d *Daemon) buildEnsure() (*ensureStack, error) {
	if d.ark == nil {
		return nil, errors.New("the sync stack is not up")
	}
	c := d.c
	s := &ensureStack{d: d}
	clock := d.env.Clock
	s.host = host.New(host.Config{
		FS: kit.OSFS{}, Runner: d.env.Runner, OptRoot: kit.DefaultOptRoot,
		Roots: d.ark.kitRoots, Node: d.ark.facts.kitNode,
		Kiwix: kiwix.Install, Piper: piper.Install, Keys: update.EmbeddedKeys(), Now: clock.Now,
		ServiceActive: func(ctx context.Context, name string) bool {
			svc, ok := services.ByName(name)
			if !ok || c.Systemd == nil {
				return false
			}
			st, err := c.Systemd.State(ctx, svc.Unit)
			return err == nil && st.ActiveState == "active"
		},
		Restart: func(ctx context.Context, name string) error {
			svc, ok := services.ByName(name)
			if !ok {
				return fmt.Errorf("unknown service %q", name)
			}
			// The same path as POST /services/kiwix/restart, so the library is regenerated afterwards.
			_, err := (&kiwixKickActions{ServiceActions: c.Controller, kick: &d.kiwixKick}).Do(ctx, svc, services.ActionRestart)
			return err
		},
	})
	journal, recovered, err := ensure.OpenJournal(config.OSFS{}, filepath.Join(c.Paths.Join(varDir), "ensure", ensure.JournalFile), clock.Now())
	if err != nil {
		d.log.Warn("the install journal could not be read; starting with an empty one", "error", err.Error())
	}
	if recovered != nil {
		d.log.Warn("an install pass was interrupted by a restart; the next pass starts from what the computer has", "run", recovered.ID, "code", doctor.KIT051)
	}
	s.svc = ensure.New(ensure.Deps{
		Settings: s.settings,
		Wants:    s.wants,
		Planner:  &ensure.Planner{Machine: s.host, Kit: s.host, Clock: clock},
		Executor: &ensure.Executor{Installer: s.host, Kit: s.host, Audit: d.audit, Clock: clock, Log: d.log.With("component", "ensure"), WhisperWant: s.whisperWant},
		Journal:  journal, Gate: s.gate, Alert: s.alert, Audit: d.audit, Clock: clock, Log: d.log,
	})
	return s, nil
}

// settings is ensure.auto of node.yaml now (the defaults for a block that does not parse: the store refused
// such a file, so the last valid one is in use).
func (s *ensureStack) settings() ensure.Settings {
	st, probs := ensure.ParseSettings(s.d.c.Config.Get().Blocks[ensure.BlockName])
	if probs.HasErrors() {
		return ensure.DefaultSettings()
	}
	return st
}

// explicitlyOff reads `enabled: false` of an owned block: a missing block or key is not off.
func explicitlyOff(cfg config.Config, name string) bool {
	raw, ok := cfg.Blocks[name]
	if !ok {
		return false
	}
	var b struct {
		Enabled *bool `yaml:"enabled"`
	}
	return raw.Decode(&b) == nil && b.Enabled != nil && !*b.Enabled
}

type whisperBlock struct {
	Commit  string `yaml:"commit"`
	Backend string `yaml:"backend"`
}

func whisperOf(cfg config.Config) whisperBlock {
	var w whisperBlock
	if raw, ok := cfg.Blocks["whisper"]; ok {
		_ = raw.Decode(&w)
	}
	return w
}

func (s *ensureStack) facts() ensure.Facts {
	cfg := s.d.c.Config.Get()
	f := ensure.Facts{
		Backend: cfg.Llama.Backend, KiwixOff: explicitlyOff(cfg, "kiwix"), PiperOff: explicitlyOff(cfg, "piper"), WhisperOff: explicitlyOff(cfg, "whisper"),
	}
	f.Codename, f.Arch = s.d.ark.facts.release()
	w := whisperOf(cfg)
	f.WhisperCommit, f.WhisperBackend = w.Commit, w.Backend
	return f
}

func (s *ensureStack) whisperWant() ensure.WhisperWant {
	return ensure.WantsFor([]string{"kit-whisper-cpp"}, s.facts()).WhisperWant
}

// wants is what the profile and node.yaml want: the kit items of the profile (the same list the index of the
// kit is written from) turned into tools, packages and the speech program.
func (s *ensureStack) wants(ctx context.Context) (ensure.Wants, error) {
	roots := s.d.ark.roots.List()
	if len(roots) == 0 {
		return ensure.Wants{}, errors.New("there is no content root")
	}
	items, err := s.d.ark.kitItems(ctx, roots[0])
	if err != nil {
		return ensure.Wants{}, fmt.Errorf("the profile could not be read: %w", err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ensure.WantsFor(ids, s.facts()), nil
}

// gate says whether a pass may start: not while a sync, verify or import run holds the run slot (the kit may
// be changing) and not while a mode change is in flight.
func (s *ensureStack) gate() error {
	if s.d.ark != nil && s.d.ark.engine != nil && s.d.ark.engine.SyncState() != "" {
		return ensure.ErrSyncRunning
	}
	if s.d.mode != nil && s.d.mode.Info().Transition != nil {
		return ensure.ErrModeChanging
	}
	return nil
}

// alert raises or clears LAI-KIT-051 and LAI-KIT-052.
func (s *ensureStack) alert(code string, raise bool, details map[string]any) {
	if raise {
		s.d.raise(code, details)
		return
	}
	s.d.clearAlert(code)
}

// ---- api.EnsureEngine ---------------------------------------------------------------------------

// ensureEngine adapts the service to the routes and maps its refusals to documented errors.
type ensureEngine struct{ s *ensureStack }

var _ api.EnsureEngine = ensureEngine{}

func (e ensureEngine) EnsureView(ctx context.Context, lang string) (any, error) {
	return e.s.svc.View(ctx, lang), nil
}

func (e ensureEngine) EnsureStart(ctx context.Context, req api.EnsureStartRequest) (any, bool, error) {
	v, started, err := e.s.svc.Start(ctx, ensure.StartRequest{DryRun: req.DryRun, Confirm: req.Confirm, Actor: req.Actor, Lang: req.Lang})
	if err != nil {
		return nil, false, ensureAPIError(err)
	}
	return v, started || v.State == ensure.StateInstalling && !v.DryRun, nil
}

func (e ensureEngine) EnsureCancel(ctx context.Context, lang string) (any, bool, error) {
	was := e.s.svc.View(ctx, lang).State == ensure.StateInstalling
	return e.s.svc.Cancel(ctx, lang), was, nil
}

// ensureAPIError maps the engine's refusals.
func ensureAPIError(err error) error {
	var ce *ensure.ConfirmError
	switch {
	case errors.As(err, &ce):
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "confirm", "reason": ce.Reason, "step": ce.ID})
	case errors.Is(err, ensure.ErrSyncRunning):
		return api.NewError(doctor.SYN005, http.StatusConflict, nil)
	case errors.Is(err, ensure.ErrModeChanging):
		return api.NewError(api.CodeBusy, http.StatusConflict, map[string]any{"reason": "mode_change"})
	}
	return err
}

// ensureJobs is the cloud command `ensure.run`: the same start as POST /ensure with the safe default (the
// automatic steps, no confirmation, no dry run), so the cloud can never confirm compiling or restarting a
// running service. The engine's refusals are the route's (409 LAI-SYN-005, 409 LAI-AGT-022).
type ensureJobs struct{ s *ensureStack }

var _ jobs.Ensure = ensureJobs{}

func (e ensureJobs) StartEnsure(ctx context.Context, actor audit.Actor) (jobs.EnsureStarted, error) {
	v, started, err := e.s.svc.Start(ctx, ensure.StartRequest{Actor: actor, Lang: "en"})
	if err != nil {
		return jobs.EnsureStarted{}, ensureAPIError(err)
	}
	running := started || v.State == ensure.StateInstalling && !v.DryRun
	return jobs.EnsureStarted{Running: running, State: string(v.State), Pending: pendingSteps(v)}, nil
}

// lazyEnsureJobs finds the engine when the command runs: the claim plane is built before the engine.
type lazyEnsureJobs struct{ d *Daemon }

var _ jobs.Ensure = lazyEnsureJobs{}

func (l lazyEnsureJobs) StartEnsure(ctx context.Context, actor audit.Actor) (jobs.EnsureStarted, error) {
	if l.d.ensure == nil {
		return jobs.EnsureStarted{}, ensureUnavailable{}.err()
	}
	return ensureJobs{l.d.ensure}.StartEnsure(ctx, actor)
}

// pendingSteps counts the parts that are not in place.
func pendingSteps(v ensure.View) int {
	n := 0
	for _, s := range v.Steps {
		if s.Status != ensure.StatusOK && s.Status != ensure.StatusDone {
			n++
		}
	}
	return n
}

// ensureSnapshot is the cloud snapshot's ensure block: the engine's picture in English (the cloud writes its
// own text in the viewer's language from the codes). It is nil until the engine is built, and when it is not.
type ensureSnapshot struct{ d *Daemon }

var _ cloud.EnsureSource = ensureSnapshot{}

func (e ensureSnapshot) Ensure(ctx context.Context) *cloud.EnsureReport {
	if e.d.ensure == nil {
		return nil
	}
	v := e.d.ensure.svc.View(ctx, "en")
	r := &cloud.EnsureReport{
		State: string(v.State), Auto: string(v.Auto), Message: v.Message, Done: v.Progress.Done, Total: v.Progress.Total,
		NextKind: string(v.NextAction.Kind), NextStep: v.NextAction.Step,
	}
	for _, s := range v.Steps {
		r.Steps = append(r.Steps, cloud.EnsureStepReport{ID: s.ID, Status: string(s.Status)})
	}
	return r
}

// ensureUnavailable answers 503 while the sync stack is not up.
type ensureUnavailable struct{}

var _ api.EnsureEngine = ensureUnavailable{}

func (ensureUnavailable) err() error {
	return api.NewError(api.CodeStarting, http.StatusServiceUnavailable, map[string]any{"reason": "sync_engine_not_ready"})
}
func (u ensureUnavailable) EnsureView(context.Context, string) (any, error) { return nil, u.err() }
func (u ensureUnavailable) EnsureStart(context.Context, api.EnsureStartRequest) (any, bool, error) {
	return nil, false, u.err()
}
func (u ensureUnavailable) EnsureCancel(context.Context, string) (any, bool, error) {
	return nil, false, u.err()
}

// ---- the worker ---------------------------------------------------------------------------------

// ensureWorker turns events into passes: sync.done (a run that did something), mode.changed to online, and
// the start of the agent. It also runs the service's own loop.
type ensureWorker struct {
	s      *ensureStack
	cancel context.CancelFunc
	done   chan struct{}
	loop   chan struct{}
}

// Name implements Worker.
func (w *ensureWorker) Name() string { return "ensure" }

// Start implements Worker: it subscribes before it returns, so an event published after Start is seen.
func (w *ensureWorker) Start(ctx context.Context) error {
	filter, err := events.ParseFilter(events.TypeSyncDone + "," + events.TypeModeChanged)
	if err != nil {
		return err
	}
	ctx, w.cancel = context.WithCancel(ctx)
	ch, err := w.s.d.c.Bus.Subscribe(ctx, filter, 0)
	if err != nil {
		w.cancel()
		return err
	}
	w.loop, w.done = make(chan struct{}), make(chan struct{})
	go func() { defer close(w.loop); w.s.svc.Run(ctx) }()
	go func() {
		defer close(w.done)
		for ev := range ch {
			switch ev.Type {
			case events.TypeSyncDone:
				var d events.SyncDone
				if json.Unmarshal(ev.Data, &d) == nil && d.State != "skipped" {
					w.s.svc.Trigger(ensure.TriggerSync, ensureDelays.Sync)
				}
			case events.TypeModeChanged:
				var m events.ModeChanged
				if json.Unmarshal(ev.Data, &m) == nil && m.To == "online" {
					w.s.svc.Trigger(ensure.TriggerOnline, ensureDelays.Online)
				}
			}
		}
	}()
	w.s.svc.Trigger(ensure.TriggerStart, ensureDelays.Start)
	return nil
}

// Stop implements Worker: the running pass is cancelled and waited for.
func (w *ensureWorker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	for _, ch := range []chan struct{}{w.done, w.loop} {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// onConfigChange runs a pass soon after ensure.auto changed: a person who turns it back on expects it to
// act, and one who turns it off or to ask expects the alerts to follow.
func (s *ensureStack) onConfigChange(c config.Change) {
	if hasPrefix(c.Changed, ensure.BlockName) {
		s.svc.Trigger(ensure.TriggerManual, ensureDelays.Cfg)
	}
}
