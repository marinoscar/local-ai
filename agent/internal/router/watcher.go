package router

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Defaults of the watcher (llama-router.md section 4.14, agent.md section 3.1).
const (
	// DefaultPollInterval is the cadence of GET /models.
	DefaultPollInterval = 5 * time.Second
	// DefaultMaxBackoff caps the poll interval while the router does not answer, and the SSE
	// reconnect delay.
	DefaultMaxBackoff = 30 * time.Second
	// DefaultDownAfter is the number of consecutive failed polls (the router not answering) before
	// LAI-LLM-004 is raised: a llama restart takes a few seconds and is not an alert.
	DefaultDownAfter = 3
	// DefaultUnauthorizedAfter is the number of consecutive 401 answers before LAI-LLM-005 is
	// raised (the client has already re-read the key file once per call).
	DefaultUnauthorizedAfter = 2
	// minKickGap is the shortest time between two polls triggered by SSE events, so a stream of
	// download_progress events cannot turn into a stream of polls.
	minKickGap = 500 * time.Millisecond
	// sseGoodRun is how long a stream must live to count as healthy and reset the reconnect backoff.
	sseGoodRun = 30 * time.Second
	// sseMinBackoff is the first reconnect delay of the stream.
	sseMinBackoff = time.Second
)

// Publisher receives events; *events.Bus implements it. The watcher publishes model.loaded and
// model.unloaded (agent.md section 3.10) with the typed payloads of internal/events.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// AlertSink is the alert manager hook (AGENT-11): Raise reports a doctor code, with the model id it
// concerns as subject (empty for a node-wide code), and Clear withdraws it. Raise is called once
// per problem, not on every poll. It has the same shape as the service watcher's sink; ManagerAlerts
// connects it to the doctor alert manager.
type AlertSink interface {
	Raise(code, subject string, details map[string]any)
	Clear(code, subject string)
}

// WatcherOptions configures NewWatcher. Client is required.
type WatcherOptions struct {
	Client Router
	// Publisher and Alerts are optional. When the Publisher is also an events subscriber (the bus
	// is), the watcher listens for key.rotated: it makes the client re-read the key and polls at once.
	Publisher Publisher
	Alerts    AlertSink
	// Clock defaults to the wall clock.
	Clock  config.Clock
	Logger *slog.Logger
	// ModelsMax returns models_max of the profile for LAI-LLM-028; nil, or a value of 0 or less,
	// skips the check.
	ModelsMax func() int
	// PresetIDs returns the ids of the effective model set (the sections of models.ini) for
	// LAI-LLM-024. nil skips the id comparison; a model whose source is "cache" counts either way.
	PresetIDs func() []string
	// OnProgress receives every SSE event. The orchestrator uses it to forward model.loading
	// progress with its own op_id. It is called on the stream goroutine and must not block.
	OnProgress func(Progress)
	// Interval, MaxBackoff, DownAfter and UnauthorizedAfter override the defaults (tests).
	Interval          time.Duration
	MaxBackoff        time.Duration
	DownAfter         int
	UnauthorizedAfter int
}

// View is the watcher's picture of the router, for /status and /models. Reading it never blocks on
// the router.
type View struct {
	// Models is the last successful GET /models, in the router's order. nil before the first success.
	Models []ModelStatus
	// At is when Models was read; zero before the first success.
	At time.Time
	// Stale is true when the last poll failed (or none has succeeded yet): Models is then the last
	// good picture, not the current one.
	Stale bool
	// Err is the text of the last failure while Stale; it never holds key material.
	Err string
}

// Finding is an active doctor code of the watcher.
type Finding struct {
	Code Code
	// Subject is the model id, or empty for a node-wide code.
	Subject string
	Since   time.Time
	Details map[string]any
}

type findingKey struct {
	code    Code
	subject string
}

// Watcher is the router watcher worker (an app.Worker): every 5 s, and when the router's SSE stream
// says something changed, it reads GET /models, diffs the result with the last view and
//
//   - publishes model.loaded and model.unloaded (reason crash, or preset_changed for a model that
//     disappeared from the list) for changes that happen outside an agent operation;
//   - raises LAI-LLM-009 (a model failed), LAI-LLM-028 (more loaded than models_max), LAI-LLM-024
//     (models that are not in the preset), LAI-LLM-004 (the router does not answer) and LAI-LLM-005
//     (it rejects our key) through the alert sink, and clears them when they stop being true.
//
// Events are edge-triggered; findings are level-triggered (computed from the current view).
type Watcher struct {
	opt   WatcherOptions
	log   *slog.Logger
	clock config.Clock

	kick chan struct{}

	mu           sync.Mutex
	view         View
	haveBase     bool
	status       map[string]Status
	loadingSince map[string]time.Time
	suppress     map[string]int
	epoch        uint64
	active       map[findingKey]Finding
	downStreak   int
	unauthStreak int
	failEpisode  bool
	progress     map[string]Progress

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWatcher returns a Watcher.
func NewWatcher(opt WatcherOptions) *Watcher {
	if opt.Interval <= 0 {
		opt.Interval = DefaultPollInterval
	}
	if opt.MaxBackoff < opt.Interval {
		opt.MaxBackoff = DefaultMaxBackoff
	}
	if opt.DownAfter <= 0 {
		opt.DownAfter = DefaultDownAfter
	}
	if opt.UnauthorizedAfter <= 0 {
		opt.UnauthorizedAfter = DefaultUnauthorizedAfter
	}
	w := &Watcher{
		opt: opt, clock: opt.Clock, kick: make(chan struct{}, 1),
		status: map[string]Status{}, loadingSince: map[string]time.Time{},
		suppress: map[string]int{}, active: map[findingKey]Finding{}, progress: map[string]Progress{},
		view: View{Stale: true},
	}
	if w.clock == nil {
		w.clock = config.RealClock{}
	}
	l := opt.Logger
	if l == nil {
		l = slog.Default()
	}
	w.log = l.With("component", "router")
	return w
}

// Name implements app.Worker.
func (w *Watcher) Name() string { return "router-watcher" }

// Start implements app.Worker. It returns at once; the first poll runs on the poll goroutine, so a
// router that is not up yet does not delay the agent's start.
func (w *Watcher) Start(ctx context.Context) error {
	if w.opt.Client == nil {
		return fmt.Errorf("router: the watcher needs a client")
	}
	ctx, w.cancel = context.WithCancel(ctx)
	loops := []func(context.Context){w.pollLoop, w.sseLoop}
	if sub, ok := w.opt.Publisher.(interface {
		Subscribe(context.Context, events.Filter, uint64) (<-chan events.Event, error)
	}); ok {
		f, err := events.NewFilter(events.TypeKeyRotated)
		if err == nil {
			if ch, err := sub.Subscribe(ctx, f, 0); err == nil {
				loops = append(loops, func(ctx context.Context) { w.rotationLoop(ctx, ch) })
			}
		}
	}
	for _, loop := range loops {
		w.wg.Add(1)
		go func(loop func(context.Context)) {
			defer w.wg.Done()
			loop(ctx)
		}(loop)
	}
	return nil
}

// Stop implements app.Worker: it ends the loops and waits for them within ctx.
func (w *Watcher) Stop(ctx context.Context) error {
	if w.cancel != nil {
		w.cancel()
	}
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// View returns a copy of the last view. It never blocks on the router.
func (w *Watcher) View() View {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.view
	v.Models = append([]ModelStatus(nil), v.Models...)
	return v
}

// Findings returns the active doctor codes of the watcher, ordered by code then subject.
func (w *Watcher) Findings() []Finding {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Finding, 0, len(w.active))
	for _, f := range w.active {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// LatestProgress returns the last SSE progress seen for a model that is loading.
func (w *Watcher) LatestProgress(id string) (Progress, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.progress[id]
	return p, ok
}

// Suppress tells the watcher that an agent operation (load, unload, key rotation, llama restart)
// is running: the router's changes until release is called are the operation's own, so no event is
// published and the failure and count findings are not re-evaluated for them. The watcher keeps
// reading and takes the result as its new baseline, so nothing the operation did is reported later.
// The orchestrator calls it with its op_id while it holds modelOpsMu. release is idempotent.
func (w *Watcher) Suppress(opID string) (release func()) {
	w.mu.Lock()
	w.suppress[opID]++
	w.epoch++
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			if w.suppress[opID]--; w.suppress[opID] <= 0 {
				delete(w.suppress, opID)
			}
			w.epoch++
			w.mu.Unlock()
			w.Kick()
		})
	}
}

// Kick asks for a poll now (after a llama restart, a key rotation, an operation). It never blocks.
func (w *Watcher) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// pollLoop polls on a ticker, backs off while the router fails, and polls at once on Kick.
func (w *Watcher) pollLoop(ctx context.Context) {
	t := w.clock.NewTicker(w.opt.Interval)
	defer t.Stop()
	fails, skip := 0, 0
	run := func() {
		if w.Poll(ctx) {
			fails, skip = 0, 0
			return
		}
		fails++
		skip = backoffSkips(fails, w.opt.Interval, w.opt.MaxBackoff)
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.kick:
			run()
		case <-t.C():
			if skip > 0 {
				skip--
				continue
			}
			run()
		}
	}
}

// backoffSkips is how many ticks the poll loop lets pass after the given number of consecutive
// failures: the wait doubles per failure and is capped (5 s, 10 s, 20 s, 30 s, 30 s ... for the
// defaults), so 1, 3, 5, 5 ... ticks are skipped.
func backoffSkips(fails int, interval, max time.Duration) int {
	wait := interval
	for i := 0; i < fails && wait < max; i++ {
		wait *= 2
	}
	if wait > max {
		wait = max
	}
	return int(wait/interval) - 1
}

// rotationLoop reacts to key.rotated: the client forgets its cached key and the watcher polls.
func (w *Watcher) rotationLoop(ctx context.Context, ch <-chan events.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if r, ok := w.opt.Client.(interface{ KeyRotated() }); ok {
				r.KeyRotated()
			}
			w.Kick()
		}
	}
}

// sseLoop keeps the progress stream open: it reconnects with backoff, and every event nudges the
// poll loop. SSE never decides anything; if it does not work (an older llama-server, a refused key)
// the poll is enough, so failures here are logged once per episode and nothing else.
func (w *Watcher) sseLoop(ctx context.Context) {
	backoff := sseMinBackoff
	logged := false
	var lastKick time.Time
	for ctx.Err() == nil {
		ch, err := w.opt.Client.Progress(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !logged {
				w.log.Warn("the router progress stream is not available; polling only", "error", err)
				logged = true
			}
		} else {
			logged = false
			start := w.clock.Now()
			got := false
			for p := range ch {
				got = true
				w.onProgress(p)
				if now := w.clock.Now(); now.Sub(lastKick) >= minKickGap {
					lastKick = now
					w.Kick()
				}
			}
			if got || w.clock.Now().Sub(start) >= sseGoodRun {
				backoff = sseMinBackoff
			}
			// The stream ended (a llama restart, a crash): look at the models now.
			w.Kick()
		}
		if !w.sleep(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > w.opt.MaxBackoff {
			backoff = w.opt.MaxBackoff
		}
	}
}

func (w *Watcher) sleep(ctx context.Context, d time.Duration) bool {
	t := w.clock.NewTicker(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	}
}

func (w *Watcher) onProgress(p Progress) {
	w.mu.Lock()
	switch {
	case p.HasValue && p.Status == StatusLoading:
		w.progress[p.Model] = p
	case p.Status != "" && p.Status != StatusLoading, p.Event == EventModelRemove:
		delete(w.progress, p.Model)
	}
	w.mu.Unlock()
	if w.opt.OnProgress != nil {
		w.opt.OnProgress(p)
	}
}

// action is something to do after the lock is released.
type action func()

// Poll reads GET /models once and applies it: one watcher cycle. It reports whether the router
// answered with a usable list. The loops call it; tests call it directly.
func (w *Watcher) Poll(ctx context.Context) (ok bool) {
	w.mu.Lock()
	startEpoch, startSuppressed := w.epoch, len(w.suppress) > 0
	w.mu.Unlock()
	models, err := w.opt.Client.Models(ctx)
	if err != nil && ctx.Err() != nil {
		return false
	}
	var acts []action
	w.mu.Lock()
	if err != nil {
		acts = w.failLocked(err)
	} else {
		quiet := startSuppressed || len(w.suppress) > 0 || w.epoch != startEpoch
		acts = w.applyLocked(models, quiet)
	}
	w.mu.Unlock()
	for _, a := range acts {
		a()
	}
	return err == nil
}

// failLocked records a failed poll: the view goes stale, and the streaks decide LAI-LLM-004/005.
func (w *Watcher) failLocked(err error) []action {
	w.view.Stale, w.view.Err = true, err.Error()
	var acts []action
	switch CodeOf(err) {
	case CodeKeyRejected:
		w.downStreak = 0
		if w.unauthStreak++; w.unauthStreak >= w.opt.UnauthorizedAfter {
			acts = append(acts, w.setFindingLocked(CodeKeyRejected, "", map[string]any{"error": err.Error()})...)
			acts = append(acts, w.clearFindingLocked(CodeRouterDown, "")...)
		}
	case CodeRouterDown:
		w.unauthStreak = 0
		if w.downStreak++; w.downStreak >= w.opt.DownAfter {
			acts = append(acts, w.setFindingLocked(CodeRouterDown, "", map[string]any{"error": err.Error()})...)
			acts = append(acts, w.clearFindingLocked(CodeKeyRejected, "")...)
		}
	default:
		// Malformed answers, a missing key file: the view is stale, nothing else is claimed.
		w.downStreak, w.unauthStreak = 0, 0
	}
	if !w.failEpisode {
		w.failEpisode = true
		acts = append(acts, func() { w.log.Warn("the router poll failed; the view is stale", "error", err) })
	}
	return acts
}

// applyLocked takes a good GET /models: diff, events, findings, new baseline. When quiet is true
// (an agent operation is or was running during the poll) it publishes nothing and leaves the
// failure and count findings as they are.
func (w *Watcher) applyLocked(models []ModelStatus, quiet bool) []action {
	var acts []action
	now := w.clock.Now()
	if w.failEpisode || w.downStreak > 0 || w.unauthStreak > 0 {
		acts = append(acts, func() { w.log.Info("the router answers again") })
	}
	w.failEpisode, w.downStreak, w.unauthStreak = false, 0, 0
	acts = append(acts, w.clearFindingLocked(CodeRouterDown, "")...)
	acts = append(acts, w.clearFindingLocked(CodeKeyRejected, "")...)

	cur := make(map[string]ModelStatus, len(models))
	for _, m := range models {
		cur[m.ID] = m
		if !m.Status.Known() {
			id, st := m.ID, m.Status
			if _, seen := w.status[id]; !seen || w.status[id] != st {
				acts = append(acts, func() {
					w.log.Warn("the router reported a status this agent does not know", "model", id, "status", string(st))
				})
			}
		}
	}

	if w.haveBase && !quiet {
		acts = append(acts, w.diffLocked(cur, now)...)
	}
	// New baseline, whatever happened above.
	next := make(map[string]Status, len(cur))
	for id, m := range cur {
		next[id] = m.Status
		// load_ms of a later model.loaded is measured from the first poll that saw it loading.
		if _, ok := w.loadingSince[id]; !ok && m.Status == StatusLoading {
			w.loadingSince[id] = now
		}
	}
	for id := range w.loadingSince {
		if m, ok := cur[id]; !ok || m.Status != StatusLoading {
			delete(w.loadingSince, id)
		}
	}
	w.status, w.haveBase = next, true
	w.view = View{Models: append([]ModelStatus(nil), models...), At: now}

	acts = append(acts, w.evaluateLocked(models, quiet, now)...)
	return acts
}

// diffLocked compares the new list with the last statuses.
func (w *Watcher) diffLocked(cur map[string]ModelStatus, now time.Time) []action {
	var acts []action
	ids := make([]string, 0, len(cur)+len(w.status))
	for id := range cur {
		ids = append(ids, id)
	}
	for id := range w.status {
		if _, ok := cur[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		prev := w.status[id]
		m, present := cur[id]
		switch {
		case prev.Resident() && !present:
			acts = append(acts, w.publish(events.TypeModelUnloaded, events.ModelUnloaded{Model: id, Reason: events.UnloadPresetChanged}))
		case prev.Resident() && !m.Status.Resident():
			acts = append(acts, w.publish(events.TypeModelUnloaded, events.ModelUnloaded{Model: id, Reason: events.UnloadCrash}))
		case present && m.Status == StatusLoaded && !prev.Resident():
			var ms int64
			if since, ok := w.loadingSince[id]; ok {
				ms = now.Sub(since).Milliseconds()
			}
			acts = append(acts, w.publish(events.TypeModelLoaded, events.ModelLoaded{Model: id, LoadMS: ms}))
		}
	}
	return acts
}

// publish returns an action that sends the event; a refused event is a programming error, logged.
func (w *Watcher) publish(typ string, data any) action {
	return func() {
		w.log.Info("model state changed outside an agent operation", "event", typ, "data", fmt.Sprintf("%+v", data))
		if w.opt.Publisher == nil {
			return
		}
		if err := w.opt.Publisher.Publish(typ, "", data); err != nil {
			w.log.Error("could not publish the event", "event", typ, "error", err)
		}
	}
}

// evaluateLocked recomputes the level-triggered findings of LAI-LLM-009, 028 and 024.
func (w *Watcher) evaluateLocked(models []ModelStatus, quiet bool, now time.Time) []action {
	var acts []action
	want := map[findingKey]map[string]any{}

	// 009 and 028 depend on states an operation moves through on purpose; during one they stay as they were.
	if quiet {
		for k, f := range w.active {
			if k.code == CodeModelFailed || k.code == CodeTooManyLoaded {
				want[k] = f.Details
			}
		}
	} else {
		loaded := []string{}
		for _, m := range models {
			if m.Failed {
				want[findingKey{CodeModelFailed, m.ID}] = map[string]any{"model": m.ID, "exit_code": m.ExitCode}
			}
			if m.Status == StatusLoaded {
				loaded = append(loaded, m.ID)
			}
		}
		if w.opt.ModelsMax != nil {
			if max := w.opt.ModelsMax(); max > 0 && len(loaded) > max {
				sort.Strings(loaded)
				want[findingKey{CodeTooManyLoaded, ""}] = map[string]any{"loaded": loaded, "models_max": max}
			}
		}
	}

	var preset map[string]struct{}
	if w.opt.PresetIDs != nil {
		preset = map[string]struct{}{}
		for _, id := range w.opt.PresetIDs() {
			preset[id] = struct{}{}
		}
	}
	var foreign []string
	for _, m := range models {
		if m.Source == SourceCache {
			foreign = append(foreign, m.ID)
			continue
		}
		if preset != nil {
			if _, ok := preset[m.ID]; !ok {
				foreign = append(foreign, m.ID)
			}
		}
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		if len(foreign) > 20 {
			foreign = foreign[:20]
		}
		want[findingKey{CodeCacheModels, ""}] = map[string]any{"models": foreign}
	}

	keys := make([]findingKey, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].code != keys[j].code {
			return keys[i].code < keys[j].code
		}
		return keys[i].subject < keys[j].subject
	})
	for _, k := range keys {
		if _, on := w.active[k]; !on {
			acts = append(acts, w.setFindingLocked(k.code, k.subject, want[k])...)
		}
	}
	var gone []findingKey
	for k := range w.active {
		if _, ok := want[k]; ok {
			continue
		}
		switch k.code {
		case CodeModelFailed, CodeTooManyLoaded, CodeCacheModels:
			gone = append(gone, k)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].code != gone[j].code {
			return gone[i].code < gone[j].code
		}
		return gone[i].subject < gone[j].subject
	})
	for _, k := range gone {
		acts = append(acts, w.clearFindingLocked(k.code, k.subject)...)
	}
	_ = now
	return acts
}

// setFindingLocked marks a finding active and returns the action that raises it, once.
func (w *Watcher) setFindingLocked(code Code, subject string, details map[string]any) []action {
	k := findingKey{code, subject}
	if _, on := w.active[k]; on {
		return nil
	}
	w.active[k] = Finding{Code: code, Subject: subject, Since: w.clock.Now(), Details: details}
	return []action{func() {
		w.log.Warn("doctor finding raised", "code", string(code), "model", subject)
		if w.opt.Alerts != nil {
			w.opt.Alerts.Raise(string(code), subject, details)
		}
	}}
}

// clearFindingLocked withdraws a finding if it is active.
func (w *Watcher) clearFindingLocked(code Code, subject string) []action {
	k := findingKey{code, subject}
	if _, on := w.active[k]; !on {
		return nil
	}
	delete(w.active, k)
	return []action{func() {
		w.log.Info("doctor finding cleared", "code", string(code), "model", subject)
		if w.opt.Alerts != nil {
			w.opt.Alerts.Clear(string(code), subject)
		}
	}}
}

// AlertManager is what ManagerAlerts needs from the doctor alert manager; *doctor.Manager
// implements it.
type AlertManager interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
	Clear(code string)
}

// ManagerAlerts adapts the doctor alert manager (AGENT-11), whose alerts are keyed by code alone, to
// the watcher's AlertSink: a code is raised when its first subject appears, re-raised with the new
// list of models (details are replaced, Since is kept) when the subjects change, and cleared when the
// last one is gone. Severity, title and fix come from the doctor code table.
func ManagerAlerts(m AlertManager) AlertSink {
	return &managerAlerts{m: m, subjects: map[string]map[string]map[string]any{}}
}

type managerAlerts struct {
	m  AlertManager
	mu sync.Mutex
	// subjects maps a code to its active subjects and their details.
	subjects map[string]map[string]map[string]any
}

func (a *managerAlerts) Raise(code, subject string, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.subjects[code]
	if set == nil {
		set = map[string]map[string]any{}
		a.subjects[code] = set
	}
	set[subject] = details
	_ = a.m.Raise(code, mergeDetails(set))
}

func (a *managerAlerts) Clear(code, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.subjects[code]
	if _, had := set[subject]; !had {
		return
	}
	delete(set, subject)
	if len(set) == 0 {
		delete(a.subjects, code)
		a.m.Clear(code)
		return
	}
	_ = a.m.Raise(code, mergeDetails(set))
}

// mergeDetails is the details of an alert for a set of subjects: the details of a node-wide
// subject as they are, or {"models": [ids]} for model subjects.
func mergeDetails(set map[string]map[string]any) map[string]any {
	out := map[string]any{}
	var models []string
	for subject, d := range set {
		if subject == "" {
			for k, v := range d {
				out[k] = v
			}
			continue
		}
		models = append(models, subject)
	}
	if len(models) > 0 {
		sort.Strings(models)
		out["models"] = models
	}
	return out
}
