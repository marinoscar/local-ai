package cloud

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/logx"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
	"gopkg.in/yaml.v3"
)

// State is the link state of agent.md section 4.8.1, as GET /cloud and status.cloud show it.
type State string

// The seven states.
const (
	StateUnenrolled  State = "unenrolled"
	StateRegistering State = "registering"
	StateLinked      State = "linked"
	StateBackoff     State = "backoff"
	StateDisabled    State = "disabled"
	StateRevoked     State = "revoked"
	StateStopped     State = "stopped"
)

// Timings of the link (agent.md sections 4.8.2 to 4.8.8).
const (
	registerTimeout   = 10 * time.Second
	callTimeout       = 10 * time.Second
	stopCallTimeout   = 3 * time.Second
	disabledInterval  = 300 * time.Second
	hourly            = time.Hour
	keyRetryEvery     = 5 * time.Minute
	keyWarnAfter      = 30 * time.Minute
	failWarnAfter     = 5 * time.Minute
	hbFailuresToBack  = 3
	clockSkewLimit    = 5 * time.Minute
	conflictWindow    = 60 * time.Second
	capabilitiesProbe = hourly
)

// Errors of the link.
var (
	// ErrNotLinked: the call needs a registered node and the link is not (or no longer) one.
	ErrNotLinked = errors.New("cloud: the node is not linked to the cloud")
	// ErrKeyPushInsecure: the key is never sent over plain http (dev_insecure).
	ErrKeyPushInsecure = errors.New("cloud: the llama key is only sent over https")
	// ErrNoKeyPush: the cloud has no key endpoint (an older cloud app).
	ErrNoKeyPush = errors.New("cloud: the cloud has no key endpoint")
)

// Seams of the link. Every one but Config, State, Status and Online is optional.

// ConfigStore is node.yaml: reads, and the comment-preserving update enroll uses to set cloud.url.
// *config.Store implements it. A store that also has Subscribe() (<-chan config.Change, func())
// makes the link register again when cloud.allow changes.
type ConfigStore interface {
	Get() config.Config
	Update(fn func(root *yaml.Node) error) error
}

type configSubscriber interface {
	Subscribe() (<-chan config.Change, func())
}

// StateStore is the part of *state.Store the link uses (key.cloud_synced).
type StateStore interface {
	Snapshot() state.State
	Mutate(fn func(*state.State) error) error
}

// Publisher is the event bus; *events.Bus implements it.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// EventSubscriber is the event bus's subscription side; *events.Bus implements it.
type EventSubscriber interface {
	Subscribe(ctx context.Context, filter events.Filter, lastID uint64) (<-chan events.Event, error)
}

// AlertSink raises and clears doctor alerts; *doctor.Manager implements it.
type AlertSink interface {
	Raise(code string, details map[string]any, opts ...doctor.RaiseOption) error
	Clear(code string)
}

// HardwareView is the last hardware sample; *hardware.Service implements it.
type HardwareView interface {
	Snapshot() hardware.Snapshot
}

// ClaimLoop is the command plane (AGENT-18): the link starts it when it becomes linked and stops it
// first on every stop. Start must return at once; Stop waits for the loop (an in-progress long poll
// is cancelled) within ctx and is idempotent: the link calls it on every stop path, more than once.
// An implementation may also have Probe(ctx), which the link calls after each heartbeat while the node
// is disabled to re-check the claim (agent.md section 4.8.1).
type ClaimLoop interface {
	Start(ctx context.Context)
	Stop(ctx context.Context) error
}

// Deps are what the link needs. Config, State, Status and Snapshots-or-its-sources are required.
type Deps struct {
	Config     ConfigStore
	State      StateStore
	Credential CredentialStore
	LinkFile   LinkStore
	// Status is statusapi's builder (Deps.Builder() of statusapi); the heartbeat and the snapshot
	// read the node through it.
	Status StatusSource
	// Snapshot holds the sources of the state snapshot besides Status. BootID, Status and Config are
	// filled in by New.
	Snapshot SnapshotOptions

	Hardware HardwareView
	Host     HostSource
	Keys     KeySource
	Claim    ClaimLoop
	Events   EventSubscriber
	Bus      Publisher
	Alerts   AlertSink
	// Inflight returns the commands in flight: the count (slotsUsed of the vitals) and the record
	// GET /cloud shows (nil for none). The claim loop provides it.
	Inflight func() (count int, info map[string]any)
	// Online says whether the node is in (or going to) online mode: *modes.Engine.Online.
	Online func() bool

	Clock Clock
	HTTP  Doer
	Rand  RandDuration
	// Version is the agent version, "0.1.0"; the heartbeat's cliVersion is "aiagent/<Version>".
	Version   string
	Hostname  func() (string, error)
	StartedAt time.Time
	Logger    *slog.Logger
}

// Link is the cloud link client. Create it with New; wire it as modes.Deps.Link, statusapi's
// CloudView, catalog.Fetcher.Cloud (through FetchCatalog), the key pusher of the key routes and
// api.CloudDeps.Link.
type Link struct {
	d      Deps
	clk    Clock
	log    *slog.Logger
	rnd    RandDuration
	bootID string
	snaps  *SnapshotBuilder

	counters Counters
	evq      EventQueue
	observer HostObserver // used by the heartbeat goroutine only
	hostSrc  HostSource

	opSem chan struct{} // lifecycle operations (Start, Stop, Enroll, Unenroll, Shutdown) one at a time

	sigMu   sync.Mutex
	pending *relinkReq
	sigCh   chan struct{}

	stateWake chan struct{}
	keyWake   chan struct{}

	mu sync.Mutex
	st linkData
}

// linkData is the mutable state of the link, guarded by Link.mu.
type linkData struct {
	state   State
	since   time.Time
	lastErr string
	url     string
	cred    string

	rec     LinkRecord
	haveRec bool
	hs      Handshake
	haveHS  bool
	// features are the serverFeatures of the last register or heartbeat answer.
	features []string

	lastHB, lastClaim time.Time

	sup *supervisor

	// stateFallback: the cloud has no state endpoint, the reduced snapshot rides in the capabilities.
	stateFallback bool
	noExtra       bool
	noVitals      bool
	hb403         int
	hbFails       int
	failingSince  time.Time
	failAlert     bool

	keyUnsyncedSince time.Time
}

type supervisor struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type relinkReq struct {
	revoked bool
	reason  string
}

// New returns a Link in the unenrolled state; Start decides what it becomes.
func New(d Deps) (*Link, error) {
	if d.Config == nil || d.State == nil || d.Status == nil {
		return nil, errors.New("cloud: the link needs a config store, a state store and a status source")
	}
	if d.HTTP == nil {
		// One http client (and so one connection pool) for the whole link.
		d.HTTP = NewHTTPClient()
	}
	l := &Link{d: d, clk: d.Clock, rnd: d.Rand, hostSrc: d.Host,
		opSem: make(chan struct{}, 1), sigCh: make(chan struct{}, 1),
		stateWake: make(chan struct{}, 1), keyWake: make(chan struct{}, 1)}
	if l.clk == nil {
		l.clk = RealClock{}
	}
	if l.hostSrc == nil {
		l.hostSrc = &ProcHost{}
	}
	l.log = d.Logger
	if l.log == nil {
		l.log = slog.Default()
	}
	l.log = l.log.With("component", "cloud")
	l.bootID = newUUID()
	opts := d.Snapshot
	opts.Status, opts.BootID, opts.Config = d.Status, l.bootID, d.Config
	if opts.Now == nil {
		opts.Now = l.clk.Now
	}
	sb, err := NewSnapshotBuilder(opts)
	if err != nil {
		return nil, err
	}
	l.snaps = sb
	l.st.state, l.st.since = StateUnenrolled, l.clk.Now()
	return l, nil
}

// BootID is the uuid of this agent process (the snapshot's boot_id).
func (l *Link) BootID() string { return l.bootID }

// Counters returns the heartbeat counters; the claim loop increments them.
func (l *Link) Counters() *Counters { return &l.counters }

func (l *Link) online() bool { return l.d.Online == nil || l.d.Online() }

func (l *Link) cfg() config.Config { return l.d.Config.Get() }

// ---------------------------------------------------------------------------------------------------
// Lifecycle serialization

// lock takes the lifecycle semaphore, giving up when ctx ends. It serializes the operations that
// start and stop the loops (Start, Stop, Shutdown, Enroll, Unenroll) so two of them never interleave;
// it is the link's own and nothing else waits on it.
func (l *Link) lock(ctx context.Context) error {
	select {
	case l.opSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tryLock takes the semaphore only when it is free: enroll and unenroll answer 409 when another change
// is running instead of queueing behind it.
func (l *Link) tryLock() bool {
	select {
	case l.opSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *Link) unlock() { <-l.opSem }

// ---------------------------------------------------------------------------------------------------
// State

// setState moves to s with the error text of the transition; it publishes cloud.state and logs when
// the state or the error changed.
func (l *Link) setState(s State, lastErr string) {
	l.mu.Lock()
	prev := l.st.state
	changed := prev != s || l.st.lastErr != lastErr
	if prev != s {
		l.st.since = l.clk.Now()
	}
	l.st.state, l.st.lastErr = s, lastErr
	l.mu.Unlock()
	if !changed {
		return
	}
	if prev != s {
		l.log.Info("cloud link state", "state", string(s), "previous", string(prev))
	}
	if l.d.Bus != nil {
		if err := l.d.Bus.Publish(events.TypeCloudState, "", events.CloudState{State: string(s), Previous: string(prev), LastError: lastErr}); err != nil {
			l.log.Debug("cloud.state event refused", "error", err.Error())
		}
	}
}

// State returns the current link state.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.state
}

func (l *Link) raise(code string, details map[string]any, opts ...doctor.RaiseOption) {
	if l.d.Alerts != nil {
		if err := l.d.Alerts.Raise(code, details, opts...); err != nil {
			l.log.Debug("alert refused", "code", code, "error", err.Error())
		}
	}
}

func (l *Link) clear(codes ...string) {
	if l.d.Alerts == nil {
		return
	}
	for _, c := range codes {
		l.d.Alerts.Clear(c)
	}
}

// clearLinkAlerts withdraws the alerts that describe a broken link.
func (l *Link) clearLinkAlerts() {
	l.clear(doctor.CLD051, doctor.CLD052, doctor.CLD053, doctor.CLD054)
}

func (l *Link) becomeLinked() {
	l.mu.Lock()
	// hb403 is not reset here: the second 403 after registering again is the one that counts.
	l.st.hbFails, l.st.failingSince, l.st.failAlert = 0, time.Time{}, false
	l.mu.Unlock()
	l.clearLinkAlerts()
	l.setState(StateLinked, "")
}

// hasServer reports whether the link holds a server node id and a credential, so a call can be made.
func (l *Link) hasServer() (id string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.rec.ServerNodeID, l.st.haveRec && l.st.cred != "" && l.st.url != ""
}

func (l *Link) callable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.st.state {
	case StateLinked, StateBackoff, StateDisabled:
		return l.st.haveRec
	}
	return false
}

func (l *Link) client() *Client {
	l.mu.Lock()
	url := l.st.url
	l.mu.Unlock()
	return NewClient(ClientOptions{
		BaseURL: url, HTTP: l.d.HTTP, UserAgent: "aiagent/" + l.d.Version,
		Credential: func() string {
			l.mu.Lock()
			defer l.mu.Unlock()
			return l.st.cred
		},
	})
}

func (l *Link) serverPath(suffix string) (string, bool) {
	id, ok := l.hasServer()
	if !ok {
		return "", false
	}
	return "/api/nodes/" + pathEscape(id) + suffix, true
}

func (l *Link) cliVersion() string { return "aiagent/" + l.d.Version }

func (l *Link) hostname() string {
	fn := l.d.Hostname
	if fn == nil {
		fn = os.Hostname
	}
	h, _ := fn()
	return h
}

func platformString() string { return runtime.GOOS + "/" + runtime.GOARCH }

// ---------------------------------------------------------------------------------------------------
// Supervisor control

// startSupervisor runs the supervisor goroutine; startLinked skips the register phase (Enroll has
// already registered). l.mu is not held.
func (l *Link) startSupervisor(startLinked bool) { l.startSupervisorAfter(startLinked, nil, 0) }

// startSupervisorAfter is startSupervisor with the backoff the caller already used and a wait before the
// first register attempt (Enroll registered once and failed: the retry honours the backoff timer).
func (l *Link) startSupervisorAfter(startLinked bool, bo *Backoff, firstWait time.Duration) {
	l.takeRelink() // a request left over from a call made while no loop ran is stale
	ctx, cancel := context.WithCancel(context.Background())
	sup := &supervisor{cancel: cancel, done: make(chan struct{})}
	l.mu.Lock()
	l.st.sup = sup
	l.mu.Unlock()
	go func() {
		defer close(sup.done)
		l.supervise(ctx, startLinked, bo, firstWait)
	}()
}

// stopSupervisor cancels the supervisor and waits for it (and with it every loop) within ctx.
func (l *Link) stopSupervisor(ctx context.Context) error {
	l.mu.Lock()
	sup := l.st.sup
	l.st.sup = nil
	l.mu.Unlock()
	if sup == nil {
		return nil
	}
	sup.cancel()
	select {
	case <-sup.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// signalRelink asks the supervisor to register again (or, with revoked, to stop and probe hourly).
// A revoked request is never downgraded by a later plain one.
func (l *Link) signalRelink(r relinkReq) {
	l.sigMu.Lock()
	if l.pending == nil || (r.revoked && !l.pending.revoked) {
		cp := r
		l.pending = &cp
	}
	l.sigMu.Unlock()
	select {
	case l.sigCh <- struct{}{}:
	default:
	}
}

func (l *Link) takeRelink() (relinkReq, bool) {
	l.sigMu.Lock()
	defer l.sigMu.Unlock()
	if l.pending == nil {
		return relinkReq{}, false
	}
	r := *l.pending
	l.pending = nil
	return r, true
}

// noteAuth turns a 401 from any call into the revoked state. It reports whether err was one.
func (l *Link) noteAuth(err error) bool {
	if ae := asAPIError(err); ae != nil && ae.Status == 401 {
		l.signalRelink(relinkReq{revoked: true, reason: "the cloud rejected the credential"})
		return true
	}
	return false
}

// wait blocks for d on the link's clock, or until ctx ends (false) or a wake channel fires (true).
func (l *Link) wait(ctx context.Context, d time.Duration, wake ...<-chan struct{}) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := l.clk.NewTimer(d)
	defer t.Stop()
	var w1, w2 <-chan struct{}
	if len(wake) > 0 {
		w1 = wake[0]
	}
	if len(wake) > 1 {
		w2 = wake[1]
	}
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	case <-w1:
		return true
	case <-w2:
		return true
	}
}

func (l *Link) kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------------------------------
// LinkController (modes.LinkController)

// Start begins the link when a credential exists and the node is online; otherwise it does nothing
// visible: no credential is unenrolled, a credential in offline or survival mode is stopped
// (agent.md section 4.8.1). It returns at once; the loops run until Stop or Shutdown.
func (l *Link) Start(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	return l.startLocked()
}

func (l *Link) startLocked() error {
	if l.supervising() {
		return nil
	}
	cred, err := l.d.Credential.Read()
	if err != nil {
		if errors.Is(err, ErrNoCredential) {
			l.setState(StateUnenrolled, "")
			return nil
		}
		l.log.Warn("the cloud credential cannot be read", "error", logx.ScrubString(err.Error()))
		l.setState(StateUnenrolled, "the cloud credential cannot be read")
		return nil
	}
	cfg := l.cfg()
	rec, haveRec := l.d.LinkFile.Load()
	url := cfg.Cloud.URL
	if url == "" && haveRec {
		url = rec.URL
	}
	if url == "" {
		l.setState(StateUnenrolled, "")
		return nil
	}
	l.mu.Lock()
	l.st.cred, l.st.url = cred, url
	if haveRec {
		l.st.rec, l.st.haveRec = rec, true
		l.st.features = append([]string(nil), rec.ServerFeatures...)
		if rec.Handshake != nil {
			l.st.hs, l.st.haveHS = handshakeFromRecord(*rec.Handshake), true
		}
	}
	l.mu.Unlock()
	if err := CheckURL(url, cfg.Cloud.DevInsecure); err != nil {
		l.log.Warn("cloud.url is not usable", "code", doctor.CLD062, "problem", err.Error())
		l.setState(StateRegistering, "cloud.url must start with https://")
		return nil
	}
	if !l.online() {
		l.setState(StateStopped, "")
		return nil
	}
	l.setState(StateRegistering, "")
	l.startSupervisor(false)
	return nil
}

func (l *Link) supervising() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.sup != nil
}

// Stop is the link_stop step (agent.md section 4.8.8): stop the claim loop and the loops, push a last
// snapshot (3 s), send {"status": "offline"} (3 s), deregister (3 s), all errors ignored, and move to
// stopped. A link that never registered only moves to stopped. Waiting for an in-flight command
// (step 2) is the claim loop's part of ClaimLoop.Stop.
func (l *Link) Stop(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	l.stopLocked(ctx, true)
	if l.State() != StateUnenrolled {
		l.setState(StateStopped, "")
	}
	return nil
}

// Shutdown is Stop for an agent shutdown: the same, without the deregister (the node is only
// restarting and registers again at start) and the state is left as it is.
func (l *Link) Shutdown(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	l.stopLocked(ctx, false)
	return nil
}

func (l *Link) stopLocked(ctx context.Context, deregister bool) {
	wasCallable := l.callable()
	if l.d.Claim != nil {
		if err := l.d.Claim.Stop(ctx); err != nil {
			l.log.Warn("the command loop did not stop cleanly", "error", logx.ScrubString(err.Error()))
		}
	}
	if err := l.stopSupervisor(ctx); err != nil {
		l.log.Warn("the cloud loops did not stop in time", "error", err.Error())
	}
	if !wasCallable {
		return
	}
	// Last snapshot, offline heartbeat, deregister: each its own 3 s, none can fail the stop.
	l.finalPush(ctx)
	l.sendOffline(ctx)
	if deregister {
		l.deregister(ctx)
	}
}

func (l *Link) finalPush(ctx context.Context) {
	if !l.hasStateEndpoint() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, stopCallTimeout)
	defer cancel()
	l.pushStateOnce(cctx, stopCallTimeout)
}

func (l *Link) sendOffline(ctx context.Context) {
	path, ok := l.serverPath("/heartbeat")
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, stopCallTimeout)
	defer cancel()
	if _, err := l.client().Call(cctx, "POST", path, map[string]any{"status": "offline"}, stopCallTimeout); err != nil {
		l.log.Debug("offline heartbeat failed", "error", errText(err))
	}
}

func (l *Link) deregister(ctx context.Context) {
	path, ok := l.serverPath("/deregister")
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, stopCallTimeout)
	defer cancel()
	if _, err := l.client().Call(cctx, "POST", path, map[string]any{}, stopCallTimeout); err != nil {
		l.log.Debug("deregister failed", "error", errText(err))
	}
}

// errText is the log text of a call error: the status and code of an API error, the scrubbed
// transport message otherwise. It never includes a response body.
func errText(err error) string {
	if ae := asAPIError(err); ae != nil {
		return ae.Error()
	}
	return logx.ScrubString(err.Error())
}

// ---------------------------------------------------------------------------------------------------
// Enroll and unenroll

func (l *Link) invalid(field, reason string) error {
	return api.NewError(api.CodeInvalidRequest, 400, map[string]any{"field": field, "reason": reason})
}

// validCredential checks the shape of a node credential: nod_ and a body of token characters.
func validCredential(c string) bool {
	if !strings.HasPrefix(c, "nod_") || len(c) < 12 || len(c) > 512 {
		return false
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return false
		}
	}
	return true
}

// Enroll implements api.CloudLink: it stores the credential (0600), registers synchronously (10 s) and
// starts the link. A network failure keeps the credential and answers backoff, a 401 from the cloud
// restores what was there and answers LAI-CLD-051, offline and survival mode store the credential and
// answer stopped, a non-https address answers LAI-CLD-062.
func (l *Link) Enroll(ctx context.Context, rawURL, credential string) (api.CloudEnrolled, error) {
	cfg := l.cfg()
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if err := CheckURL(rawURL, cfg.Cloud.DevInsecure); err != nil {
		return api.CloudEnrolled{}, api.NewError(doctor.CLD062, 422, map[string]any{"field": "url", "reason": err.Error()})
	}
	if !validCredential(credential) {
		return api.CloudEnrolled{}, l.invalid("credential", "not_a_node_credential")
	}
	if !l.tryLock() {
		return api.CloudEnrolled{}, api.NewError(api.CodeBusy, 409, map[string]any{"reason": "link_busy"})
	}
	defer l.unlock()

	if err := l.stopSupervisor(ctx); err != nil {
		return api.CloudEnrolled{}, err
	}
	if l.d.Claim != nil {
		_ = l.d.Claim.Stop(ctx)
	}
	prevCred, prevErr := l.d.Credential.Read()
	if err := l.d.Credential.Write(credential); err != nil {
		return api.CloudEnrolled{}, l.internal(err)
	}
	prevURL := l.currentURL()
	if prevURL == "" {
		prevURL = cfg.Cloud.URL
	}
	l.mu.Lock()
	l.st.cred, l.st.url = credential, rawURL
	prevRec, hadRec := l.st.rec, l.st.haveRec
	l.st.haveRec, l.st.hs, l.st.haveHS, l.st.features = false, Handshake{}, false, nil
	l.st.stateFallback, l.st.noExtra, l.st.noVitals, l.st.hb403 = false, false, false, 0
	l.mu.Unlock()
	_ = l.d.State.Mutate(func(s *state.State) error { s.Key.CloudSynced = false; return nil })
	l.clearLinkAlerts()
	l.clear(doctor.CLD056, doctor.CLD060)

	if !l.online() {
		l.persistURL(rawURL)
		l.setState(StateStopped, "")
		return api.CloudEnrolled{State: string(StateStopped)}, nil
	}
	l.setState(StateRegistering, "")

	bo := NewBackoff(l.rnd)
	res := l.registerOnce(ctx)
	switch res.outcome {
	case regOK:
		if err := l.handshake(ctx); err == nil {
			l.persistURL(rawURL)
			l.becomeLinked()
			l.startSupervisor(true)
			id, _ := l.hasServer()
			return api.CloudEnrolled{State: string(StateLinked), ServerNodeID: id, Reattached: res.reattached}, nil
		} else if l.noteAuthRevoked(err) {
			return l.rejectEnroll(prevCred, prevErr, prevURL, prevRec, hadRec)
		} else {
			l.persistURL(rawURL)
			l.setState(StateRegistering, errText(err))
			l.startSupervisorAfter(false, bo, bo.Fail(retryAfterOf(err)))
			return api.CloudEnrolled{State: string(StateBackoff), LastError: errText(err)}, nil
		}
	case regRevoked:
		return l.rejectEnroll(prevCred, prevErr, prevURL, prevRec, hadRec)
	default:
		l.persistURL(rawURL)
		msg := errText(res.err)
		l.setState(StateRegistering, msg)
		wait := bo.Fail(res.retryAfter)
		if res.outcome == regRejected {
			l.raise(doctor.CLD054, map[string]any{"status": res.status})
			wait = hourly
		}
		l.startSupervisorAfter(false, bo, wait)
		return api.CloudEnrolled{State: string(StateBackoff), LastError: msg}, nil
	}
}

// noteAuthRevoked reports whether err is a 401, without queueing a relink (Enroll handles it itself).
func (l *Link) noteAuthRevoked(err error) bool {
	ae := asAPIError(err)
	return ae != nil && ae.Status == 401
}

// rejectEnroll undoes an enroll the cloud answered 401 to: the previous credential comes back (or the
// file goes), the previous link resumes if there was one, and the call answers 422 LAI-CLD-051.
func (l *Link) rejectEnroll(prevCred string, prevErr error, prevURL string, prevRec LinkRecord, hadRec bool) (api.CloudEnrolled, error) {
	if prevErr == nil && prevCred != "" {
		_ = l.d.Credential.Write(prevCred)
		l.mu.Lock()
		l.st.cred, l.st.url = prevCred, prevURL
		l.st.rec, l.st.haveRec = prevRec, hadRec
		l.mu.Unlock()
		l.setState(StateRegistering, "")
		l.startSupervisor(false)
	} else {
		_ = l.d.Credential.Delete()
		l.mu.Lock()
		l.st.cred, l.st.url = "", ""
		l.mu.Unlock()
		l.setState(StateUnenrolled, "")
	}
	return api.CloudEnrolled{}, api.NewError(doctor.CLD051, 422, map[string]any{"reason": "credential_rejected"})
}

func (l *Link) currentURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.url
}

func (l *Link) internal(err error) error {
	l.log.Error("cloud link operation failed", "error", logx.ScrubString(err.Error()))
	return api.NewError(api.CodeInternal, 500, nil)
}

// persistURL writes cloud.url into node.yaml (comments kept). It is best effort: the running link
// uses the enrolled address either way, and a failure only means the address is not remembered over
// a restart.
func (l *Link) persistURL(url string) {
	if l.cfg().Cloud.URL == url {
		return
	}
	err := l.d.Config.Update(func(root *yaml.Node) error { return config.SetKey(root, url, "cloud", "url") })
	if err != nil {
		l.log.Warn("cloud.url could not be saved in node.yaml", "error", logx.ScrubString(err.Error()))
	}
}

// Unenroll implements api.CloudLink: stop everything, deregister (3 s, errors ignored), delete the
// credential and link.json.
func (l *Link) Unenroll(ctx context.Context) error {
	if !l.tryLock() {
		return api.NewError(api.CodeBusy, 409, map[string]any{"reason": "link_busy"})
	}
	defer l.unlock()
	if l.d.Claim != nil {
		_ = l.d.Claim.Stop(ctx)
	}
	wasCallable := l.callable()
	if err := l.stopSupervisor(ctx); err != nil {
		l.log.Warn("the cloud loops did not stop in time", "error", err.Error())
	}
	if wasCallable && l.online() {
		l.deregister(ctx)
	}
	var firstErr error
	if err := l.d.Credential.Delete(); err != nil {
		firstErr = err
	}
	if err := l.d.LinkFile.Delete(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.mu.Lock()
	l.st.cred, l.st.url, l.st.rec, l.st.haveRec = "", "", LinkRecord{}, false
	l.st.hs, l.st.haveHS, l.st.features = Handshake{}, false, nil
	l.st.stateFallback, l.st.lastHB, l.st.lastClaim = false, time.Time{}, time.Time{}
	l.mu.Unlock()
	l.evq.Clear()
	_ = l.d.State.Mutate(func(s *state.State) error { s.Key.CloudSynced = false; return nil })
	l.clearLinkAlerts()
	l.clear(doctor.CLD055, doctor.CLD056, doctor.CLD060, doctor.CLD061, doctor.AGT012)
	l.setState(StateUnenrolled, "")
	if firstErr != nil {
		return l.internal(firstErr)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------
// Views

// Cloud implements statusapi.CloudView.
func (l *Link) Cloud() statusapi.CloudInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	return statusapi.CloudInfo{
		Enrolled: l.st.state != StateUnenrolled, State: string(l.st.state), URL: l.st.url, LastHeartbeatAt: l.st.lastHB,
	}
}

// MaskCredential renders a credential as nod_<ellipsis><last four>: the only form that is shown.
func MaskCredential(c string) string { return logx.Redact(c) }

// CloudStatus implements api.CloudLink: the body of GET /cloud, from memory only.
func (l *Link) CloudStatus() api.Cloud {
	cfg := l.cfg()
	l.mu.Lock()
	st := l.st
	l.mu.Unlock()
	out := api.Cloud{
		Enrolled: st.state != StateUnenrolled, State: string(st.state), EligibleTypes: EligibleTypes(cfg.Cloud.Allow),
		ServerFeatures: append([]string{}, st.features...), StateTransport: "endpoint",
	}
	if out.ServerFeatures == nil {
		out.ServerFeatures = []string{}
	}
	if st.stateFallback {
		out.StateTransport = "capabilities"
	}
	if st.url != "" {
		out.URL = strPtrOrNil(st.url)
	} else if st.state == StateUnenrolled && cfg.Cloud.URL != "" {
		out.URL = strPtrOrNil(cfg.Cloud.URL)
	}
	if st.state != StateUnenrolled {
		out.Name = strPtrOrNil(cfg.Node.ID)
		out.Since = timeOut(st.since)
		out.Credential = strPtrOrNil(maskIf(st.cred))
	}
	if st.haveRec {
		out.ServerNodeID = strPtrOrNil(st.rec.ServerNodeID)
	}
	out.LastHeartbeatAt, out.LastClaimAt = timeOut(st.lastHB), timeOut(st.lastClaim)
	out.LastError = strPtrOrNil(st.lastErr)
	out.KeySynced = l.d.State.Snapshot().Key.CloudSynced
	if l.d.Inflight != nil {
		if _, info := l.d.Inflight(); len(info) > 0 {
			out.Inflight = info
		}
	}
	return out
}

func maskIf(c string) string {
	if c == "" {
		return ""
	}
	return MaskCredential(c)
}

// ---------------------------------------------------------------------------------------------------
// Seams for the command plane (AGENT-18)

// Session is what the claim loop needs to talk to the cloud.
type Session struct {
	Client       *Client
	ServerNodeID string
	// Plane is localai or eab (agent.md section 4.8.6).
	Plane          string
	ClaimWaitMaxMs int
	Features       []string
	Allow          []string
	Concurrency    int
	// ClaimPollSeconds is cloud.poll.claim_seconds: the pause after an empty plain poll.
	ClaimPollSeconds int
}

// Session returns the claim loop's view; ok is false unless the node is registered and the link is
// linked, backing off or disabled.
func (l *Link) Session() (Session, bool) {
	if !l.callable() {
		return Session{}, false
	}
	cfg := l.cfg()
	l.mu.Lock()
	defer l.mu.Unlock()
	s := Session{
		Client: l.clientLocked(), ServerNodeID: l.st.rec.ServerNodeID, Plane: PlaneLocalai, ClaimWaitMaxMs: 25000,
		Features: append([]string(nil), l.st.features...), Allow: append([]string(nil), cfg.Cloud.Allow...), Concurrency: cfg.Cloud.Concurrency,
		ClaimPollSeconds: cfg.Cloud.Poll.ClaimSeconds,
	}
	if l.st.haveHS {
		s.Plane, s.ClaimWaitMaxMs = l.st.hs.CommandPlane, l.st.hs.ClaimWaitMaxMs
	}
	return s, true
}

func (l *Link) clientLocked() *Client {
	return NewClient(ClientOptions{
		BaseURL: l.st.url, HTTP: l.d.HTTP, UserAgent: "aiagent/" + l.d.Version,
		Credential: func() string {
			l.mu.Lock()
			defer l.mu.Unlock()
			return l.st.cred
		},
	})
}

// ClaimResult reports the outcome of a claim call (nil is a 200) so the state machine follows the
// table of agent.md section 4.8.1: 200 returns a backoff or disabled link to linked, 401 revokes,
// 403 disables, 404 re-runs the handshake and then registers again, anything else is a claim
// failure and moves linked to backoff.
func (l *Link) ClaimResult(err error) {
	if err == nil {
		l.mu.Lock()
		l.st.lastClaim = l.clk.Now()
		s := l.st.state
		l.mu.Unlock()
		if s == StateBackoff || s == StateDisabled {
			l.clear(doctor.CLD052, doctor.CLD053)
			l.setState(StateLinked, "")
		}
		return
	}
	ae := asAPIError(err)
	switch {
	case ae != nil && ae.Status == 401:
		l.signalRelink(relinkReq{revoked: true, reason: "the cloud rejected the credential"})
	case ae != nil && ae.Status == 403:
		l.raise(doctor.CLD052, nil)
		l.setState(StateDisabled, "")
	case ae != nil && ae.Status == 404:
		l.signalRelink(relinkReq{reason: "the cloud does not know this node"})
	default:
		if s := l.State(); s == StateLinked {
			l.setState(StateBackoff, errText(err))
		}
	}
}

// ClaimDisabled is ClaimResult for a 403 (kept for readability at the call site).
func (l *Link) ClaimDisabled() { l.ClaimResult(&APIError{Status: 403}) }

// NoteCommandAuth lets the claim loop report a 401 it saw on renew, result or failure.
func (l *Link) NoteCommandAuth(err error) { l.noteAuth(err) }

// Disabled reports whether the cloud has disabled this node (the claim loop slows down).
func (l *Link) Disabled() bool { return l.State() == StateDisabled }
