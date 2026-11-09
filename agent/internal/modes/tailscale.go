package modes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Timeouts and limits of the Tailscale Serve manager (agent.md section 4.7.2).
const (
	// TailscaleReadTimeout bounds `tailscale status --json` and `tailscale serve status --json`.
	TailscaleReadTimeout = 3 * time.Second
	// TailscaleChangeTimeout bounds a command that changes the serve or funnel configuration.
	TailscaleChangeTimeout = 10 * time.Second
	// DefaultReconcileEvery is the drift reconciler period (agent.md section 4.7.4).
	DefaultReconcileEvery = 5 * time.Minute
	// DefaultRetryEvery is how soon the reconciler tries again while the last pass found tailscaled
	// not running or not logged in yet (LAI-NET-002, LAI-NET-003): at boot the agent can start before
	// tailscaled is Running, and the finding must not wait a whole period to clear.
	DefaultRetryEvery = 15 * time.Second

	tailscaleMaxOut = 1 << 20
	backendRunning  = "Running"
	defaultLlama    = 8080
)

// TailscaleSettings is the part of node.yaml the manager reads, fetched on every pass so a live edit
// of tailscale.* (agent.md section 3.4, "Live") takes effect at the next reconcile.
type TailscaleSettings struct {
	// Manage is tailscale.manage: false means the agent never runs `tailscale serve` or
	// `tailscale funnel`; it only reports.
	Manage bool
	// Pattern is tailscale.serve_pattern: "A", "B" or "none" (anything else counts as none).
	Pattern string
	// LlamaPort is ports.llama, the port our listeners forward to (8080 when zero).
	LlamaPort int
}

func (s TailscaleSettings) pattern() string {
	switch s.Pattern {
	case PatternA, PatternB:
		return s.Pattern
	}
	return PatternNone
}

func (s TailscaleSettings) target() target {
	if s.LlamaPort <= 0 {
		return target{defaultLlama}
	}
	return target{s.LlamaPort}
}

// SettingsFromConfig adapts a config source (*config.Store.Get) to the settings func of
// TailscaleOptions.
func SettingsFromConfig(get func() config.Config) func() TailscaleSettings {
	return func() TailscaleSettings {
		c := get()
		return TailscaleSettings{Manage: c.Tailscale.Manage, Pattern: c.Tailscale.ServePattern, LlamaPort: c.Ports.Llama}
	}
}

// TailscaleOptions configure NewTailscaleManager.
type TailscaleOptions struct {
	// Run runs the tailscale binary; required. Tests pass modes/fake.TailscaleCLI.
	Run execx.Runner
	// Lookup resolves "tailscale" to an absolute path; default execx.Lookup (the fixed PATH only).
	Lookup func(name string) (string, error)
	// Settings returns the current node.yaml values; required.
	Settings func() TailscaleSettings
	// State records state.tailscale after every observation. Optional.
	State StateStore
	// Online reports whether online policy applies now (the engine's Online); the drift reconciler
	// does nothing otherwise. Nil means never online, so the reconciler never changes anything.
	Online func() bool
	// Clock drives the reconciler; default real.
	Clock config.Clock
	// Interval is the reconcile period; default DefaultReconcileEvery.
	Interval time.Duration
	// RetryInterval is the period of the early retry while tailscaled is not ready; default
	// DefaultRetryEvery (never longer than Interval).
	RetryInterval time.Duration
	// HTTP is the client of CheckServeReachable; default a dedicated client without proxy env.
	HTTP HTTPDoer
	// Logger defaults to a discarding logger. Messages never carry the tailnet name.
	Logger *slog.Logger
}

// TailscaleManager implements TailscaleController (agent.md section 4.7): it applies the configured
// serve pattern in online mode, removes both patterns in offline and survival, reconciles drift every
// five minutes, switches Funnel off for our ports, and reports what it finds as doctor results. It
// only ever changes listeners it recognises as its own (4.7.1) and never runs `serve reset`,
// `down`, `logout` or `funnel` on a port it does not own.
//
// It also is an app worker (the reconciler loop), the source of the net.serve_state and
// net.serve_reachable doctor checks, and the TailnetChecker of the connectivity prober.
type TailscaleManager struct {
	runner   execx.Runner
	lookup   func(string) (string, error)
	settings func() TailscaleSettings
	st       StateStore
	online   func() bool
	clock    config.Clock
	interval time.Duration
	retry    time.Duration
	http     HTTPDoer
	log      *slog.Logger

	// mu serializes the passes (On, Off, Reconcile): one set of changing commands at a time.
	mu sync.Mutex

	// cmu guards the cache below, so Status and ServeResults never wait behind a 10 s command.
	cmu     sync.Mutex
	results []doctor.Result
	backend string
	haveBE  bool
	dns     string
	applied bool

	loopMu     sync.Mutex
	loopCancel context.CancelFunc
	loopDone   chan struct{}
}

var _ TailscaleController = (*TailscaleManager)(nil)

// NewTailscaleManager returns a manager. Nothing runs until On, Off, Reconcile or Start.
func NewTailscaleManager(o TailscaleOptions) (*TailscaleManager, error) {
	if o.Run == nil || o.Settings == nil {
		return nil, errors.New("modes: TailscaleOptions needs Run and Settings")
	}
	m := &TailscaleManager{runner: o.Run, lookup: o.Lookup, settings: o.Settings, st: o.State, online: o.Online,
		clock: o.Clock, interval: o.Interval, retry: o.RetryInterval, http: o.HTTP, log: o.Logger}
	if m.lookup == nil {
		m.lookup = execx.Lookup
	}
	if m.clock == nil {
		m.clock = config.RealClock{}
	}
	if m.interval <= 0 {
		m.interval = DefaultReconcileEvery
	}
	if m.retry <= 0 {
		m.retry = DefaultRetryEvery
	}
	if m.retry > m.interval {
		m.retry = m.interval
	}
	if m.http == nil {
		m.http = newProbeClient(30 * time.Second)
	}
	if m.log == nil {
		m.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return m, nil
}

// ---- running tailscale ---------------------------------------------------------------------------

type statusKind int

const (
	statusOK statusKind = iota
	statusNotInstalled
	statusDaemonDown
	statusFailed
)

// tsRun runs the tailscale binary with a fixed argv and the fixed PATH.
func (m *TailscaleManager) tsRun(ctx context.Context, timeout time.Duration, args ...string) (execx.Result, error) {
	path, err := m.lookup("tailscale")
	if err != nil {
		return execx.Result{}, fmt.Errorf("%w: tailscale", errNotInstalled)
	}
	return m.runner.Run(ctx, execx.Cmd{Path: path, Args: args, Timeout: timeout, MaxOut: tailscaleMaxOut,
		Env: []string{"PATH=" + execx.FixedPath}})
}

var errNotInstalled = errors.New("tailscale is not installed")

// failReason turns a failed run into the short, tailnet-name-free reason doctor details carry. ok is
// true when the command ran and exited 0.
func (m *TailscaleManager) failReason(res execx.Result, err error, what string) (reason string, ok bool) {
	switch {
	case err == nil && res.ExitCode == 0:
		return "", true
	case errors.Is(err, errNotInstalled):
		return "not_installed", false
	case errors.Is(err, execx.ErrTimeout):
		return what + " timed out", false
	case err != nil:
		return what + " could not run", false
	}
	if len(res.Stderr) > 0 {
		m.log.Debug("tailscale command failed", "what", what, "exit", res.ExitCode, "stderr", firstBytes(res.Stderr, 200))
	}
	return what + " exited " + strconv.Itoa(res.ExitCode), false
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// daemonDownText recognises the output of the tailscale CLI when tailscaled is not running.
func daemonDownText(b ...[]byte) bool {
	for _, x := range b {
		s := strings.ToLower(string(x))
		for _, p := range []string{"failed to connect to local", "doesn't appear to be running", "is tailscaled running",
			"connection refused", "no such file or directory", "tailscaled is not running"} {
			if strings.Contains(s, p) {
				return true
			}
		}
	}
	return false
}

// readStatus runs `tailscale status --json` (3 s) and classifies the outcome.
func (m *TailscaleManager) readStatus(ctx context.Context) (tsStatus, statusKind, string) {
	res, err := m.tsRun(ctx, TailscaleReadTimeout, "status", "--json")
	if errors.Is(err, errNotInstalled) {
		m.setStatus(tsStatus{}, false)
		return tsStatus{}, statusNotInstalled, "not_installed"
	}
	if reason, ok := m.failReason(res, err, "status"); !ok {
		if err == nil {
			// A non-zero exit with JSON on stdout still tells the state (some versions do this when
			// logged out).
			if st, perr := parseTSStatus(res.Stdout); perr == nil && st.BackendState != "" {
				m.setStatus(st, true)
				return st, statusOK, ""
			}
			if daemonDownText(res.Stderr, res.Stdout) {
				m.setStatus(tsStatus{}, false)
				return tsStatus{}, statusDaemonDown, "daemon_down"
			}
		}
		return tsStatus{}, statusFailed, reason
	}
	st, perr := parseTSStatus(res.Stdout)
	if perr != nil {
		return tsStatus{}, statusFailed, "status unreadable"
	}
	m.setStatus(st, true)
	return st, statusOK, ""
}

func (m *TailscaleManager) setStatus(st tsStatus, known bool) {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.backend, m.haveBE = st.BackendState, known
	if known && st.DNSName != "" {
		m.dns = st.DNSName
	}
}

// readServe runs `tailscale serve status --json` (3 s). A non-zero exit, a timeout or output that is
// not a JSON object is an error: the caller never concludes "nothing is published" from it.
func (m *TailscaleManager) readServe(ctx context.Context) (serveConfig, string) {
	res, err := m.tsRun(ctx, TailscaleReadTimeout, "serve", "status", "--json")
	if reason, ok := m.failReason(res, err, "serve status"); !ok {
		return serveConfig{}, reason
	}
	cfg, perr := parseServeConfig(res.Stdout)
	if perr != nil {
		return serveConfig{}, "serve status unreadable"
	}
	return cfg, ""
}

// change runs one command that changes the configuration (10 s); ctx errors are returned as errors.
func (m *TailscaleManager) change(ctx context.Context, args ...string) (string, error) {
	res, err := m.tsRun(ctx, TailscaleChangeTimeout, args...)
	if cerr := ctx.Err(); cerr != nil {
		return "", cerr
	}
	reason, _ := m.failReason(res, err, args[0])
	return reason, nil
}

// ---- commands -------------------------------------------------------------------------------------

func applyArgs(pattern string, t target) []string {
	if pattern == PatternA {
		return []string{"serve", "--bg", "--tcp=" + strconv.Itoa(PatternAPort), "tcp://" + t.hostPort()}
	}
	return []string{"serve", "--bg", "--https=" + strconv.Itoa(PatternBPort), t.proxyURL()}
}

func removeArgs(pattern string) []string {
	if pattern == PatternA {
		return []string{"serve", "--tcp=" + strconv.Itoa(PatternAPort), "off"}
	}
	return []string{"serve", "--https=" + strconv.Itoa(PatternBPort), "off"}
}

func funnelArgs(port int) []string {
	if port == PatternAPort {
		return []string{"funnel", "--tcp=" + strconv.Itoa(port), "off"}
	}
	return []string{"funnel", "--https=" + strconv.Itoa(port), "off"}
}

// shellLine is the command an operator types: the argv with sudo in front.
func shellLine(args []string) string { return "sudo tailscale " + strings.Join(args, " ") }

// ---- results --------------------------------------------------------------------------------------

func res(code string, details map[string]any) doctor.Result {
	return doctor.Result{Code: code, Details: details}
}

// warningOf turns a doctor result into the warning a transition records: the code's English title,
// plus the command to run when the details carry one.
func warningOf(r doctor.Result) Warning {
	title, _, _ := doctor.Text(r.Code, doctor.LangEN)
	msg := title
	if cmd, ok := r.Details["command"].(string); ok && cmd != "" {
		msg += " Run: " + cmd
	}
	if cmds, ok := r.Details["commands"].([]string); ok && len(cmds) > 0 {
		msg += " Run: " + strings.Join(cmds, " and ")
	}
	return Warning{Code: r.Code, Message: msg}
}

// warningsOf is the transition warnings of a pass: every result except the informational ones.
func warningsOf(rs []doctor.Result) []Warning {
	var out []Warning
	for _, r := range rs {
		if c, ok := doctor.Lookup(r.Code); ok && c.Severity == doctor.Info && r.Severity == "" {
			continue
		}
		out = append(out, warningOf(r))
	}
	return out
}

func (m *TailscaleManager) setResults(rs []doctor.Result) {
	m.cmu.Lock()
	m.results = rs
	m.cmu.Unlock()
}

// ServeResults implements doctor.NetSources.Serve: what the last pass found (net.serve_state, fast).
// It is replaced by every pass, so a condition that went away (including LAI-NET-007, which a pass
// raises once after a restore) disappears at the next one.
func (m *TailscaleManager) ServeResults() []doctor.Result {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	out := make([]doctor.Result, len(m.results))
	for i, r := range m.results {
		r.Details = copyMap(r.Details)
		out[i] = r
	}
	return out
}

func copyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TailscaleStatus is the tailscale block of GET /status (agent.md section 3.8.4).
type TailscaleStatus struct {
	Manage  bool
	Pattern string
	// Applied: our listener for Pattern was seen in the last read of the serve configuration.
	Applied bool
	// BackendState is Tailscale's BackendState from the last `tailscale status --json`; "" (JSON null)
	// when tailscale is not installed, tailscaled is not running or nothing was read yet.
	BackendState string
}

// Status returns the cached picture; it never runs a process.
func (m *TailscaleManager) Status() TailscaleStatus {
	s := m.settings()
	m.cmu.Lock()
	defer m.cmu.Unlock()
	st := TailscaleStatus{Manage: s.Manage, Pattern: s.pattern(), Applied: m.applied && s.pattern() != PatternNone}
	if m.haveBE {
		st.BackendState = m.backend
	}
	return st
}

// TailnetURL is the tailnet base URL of GET /key (agent.md section 3.8.13): A gives
// http://<dnsname>:18080/v1, B gives https://<dnsname>/v1, and "" when the pattern is none or the
// node's tailnet name is not known. The name comes from the last status read; when there is none it
// reads one (3 s).
func (m *TailscaleManager) TailnetURL(ctx context.Context) string {
	pattern := m.settings().pattern()
	if pattern == PatternNone {
		return ""
	}
	m.cmu.Lock()
	dns := m.dns
	m.cmu.Unlock()
	if dns == "" {
		if st, kind, _ := m.readStatus(ctx); kind == statusOK {
			dns = st.DNSName
		}
	}
	switch {
	case dns == "":
		return ""
	case pattern == PatternA:
		return "http://" + dns + ":" + strconv.Itoa(PatternAPort) + "/v1"
	}
	return "https://" + dns + "/v1"
}

// SelfAddress is the node's own Tailscale IPv4 address and its MagicDNS name (without the trailing dot), read
// from `tailscale status --json` (3 s). Both are "" when tailscale is not installed, not running or logged out.
// The address is admin-only data: the caller sends it to the cloud and never logs it (issue #437).
func (m *TailscaleManager) SelfAddress(ctx context.Context) (ip, machine string) {
	st, kind, _ := m.readStatus(ctx)
	if kind != statusOK || st.BackendState != backendRunning {
		return "", ""
	}
	return st.IPv4, st.DNSName
}

// TailnetUp implements TailnetChecker for the prober: BackendState is Running and at least one peer is
// Online. It reads only and never waits for a pass.
func (m *TailscaleManager) TailnetUp(ctx context.Context) bool {
	st, kind, _ := m.readStatus(ctx)
	return kind == statusOK && st.BackendState == backendRunning && st.PeersOnline > 0
}

// recordApplied writes state.tailscale when the observed pattern changed. pattern "" means none of
// ours is published.
func (m *TailscaleManager) recordApplied(pattern string, present bool) {
	m.cmu.Lock()
	m.applied = present
	m.cmu.Unlock()
	if m.st == nil {
		return
	}
	now := m.clock.Now().UTC().Truncate(time.Second)
	if err := m.st.Mutate(func(s *state.State) error {
		if s.Tailscale.AppliedPattern == pattern {
			return nil
		}
		s.Tailscale.AppliedPattern = pattern
		if pattern == "" {
			s.Tailscale.AppliedAt = nil
		} else {
			s.Tailscale.AppliedAt = &now
		}
		return nil
	}); err != nil {
		m.log.Warn("state.tailscale not recorded", "error", err.Error())
	}
}

func (m *TailscaleManager) appliedPattern() string {
	if m.st == nil {
		return ""
	}
	return m.st.Snapshot().Tailscale.AppliedPattern
}

// observedPattern is the pattern of ours in cfg, preferring want when both are there.
func observedPattern(a, b listener, want string) string {
	switch {
	case a == listenerOurs && b == listenerOurs:
		if want == PatternA || want == PatternB {
			return want
		}
		return PatternB
	case a == listenerOurs:
		return PatternA
	case b == listenerOurs:
		return PatternB
	}
	return ""
}

// ---- the three entry points -----------------------------------------------------------------------

// On applies the configured pattern (agent.md section 4.7.2): the engine's tailscale_on step. Every
// problem is a warning; the returned error is only a context error (the step timed out or the agent is
// shutting down). With tailscale.manage false it changes nothing and only reports.
func (m *TailscaleManager) On(ctx context.Context) ([]Warning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onLocked(ctx)
}

func (m *TailscaleManager) onLocked(ctx context.Context) ([]Warning, error) {
	rs, err := m.apply(ctx)
	if err != nil {
		return nil, err
	}
	m.setResults(rs)
	return warningsOf(rs), nil
}

// Off removes both of our patterns (agent.md section 4.7.3): the engine's tailscale_off step, retried
// by the engine every 60 s while the node is not online when it warns LAI-NET-004 or LAI-NET-006. A
// node whose tailscaled is not running exposes nothing, so that is success. With tailscale.manage
// false it removes nothing and reports LAI-NET-006 for a listener of ours that is there.
func (m *TailscaleManager) Off(ctx context.Context) ([]Warning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs, err := m.remove(ctx)
	if err != nil {
		return nil, err
	}
	m.setResults(rs)
	return warningsOf(rs), nil
}

// Reconcile is one drift pass (agent.md section 4.7.4): in online mode it re-applies the pattern when
// our listener went missing (LAI-NET-007), and re-checks everything else apply checks. It does nothing
// when the node is not online. The config watcher calls it after an edit of tailscale.*.
func (m *TailscaleManager) Reconcile(ctx context.Context) ([]Warning, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Checked under mu: a transition away from online has already turned this false before its
	// tailscale_off could take the lock, so a pass that was waiting here never re-applies after Off.
	if m.online == nil || !m.online() {
		return nil, nil
	}
	return m.onLocked(ctx)
}

// ---- apply ----------------------------------------------------------------------------------------

func (m *TailscaleManager) apply(ctx context.Context) ([]doctor.Result, error) {
	s := m.settings()
	want := s.pattern()

	st, kind, reason := m.readStatus(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if want == PatternNone {
		// Nothing is to be published, so a missing, stopped or logged-out Tailscale is not a problem.
		if kind == statusNotInstalled || kind == statusDaemonDown || (kind == statusOK && st.BackendState != backendRunning) {
			return nil, nil
		}
	}
	switch kind {
	case statusNotInstalled:
		return []doctor.Result{res(doctor.NET001, nil)}, nil
	case statusDaemonDown:
		return []doctor.Result{res(doctor.NET002, nil)}, nil
	case statusFailed:
		return []doctor.Result{res(doctor.NET004, map[string]any{"reason": reason})}, nil
	}
	if st.BackendState != backendRunning {
		return []doctor.Result{res(doctor.NET003, map[string]any{"backend_state": st.BackendState})}, nil
	}

	cfg, reason := m.readServe(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reason != "" {
		return []doctor.Result{res(doctor.NET004, map[string]any{"reason": reason})}, nil
	}
	if want == PatternNone {
		return m.removeOurs(ctx, s, cfg, st.DNSName, false)
	}
	return m.applyPattern(ctx, s, cfg, st.DNSName)
}

// applyPattern is §4.7.2 from "want is A or B" on: the listener, the other pattern, foreign
// listeners to our port, and Funnel.
func (m *TailscaleManager) applyPattern(ctx context.Context, s TailscaleSettings, cfg serveConfig, host string) ([]doctor.Result, error) {
	want, t := s.pattern(), s.target()
	var out []doctor.Result

	state0 := m.patternState(cfg, want, host, t)
	switch state0 {
	case listenerForeign, listenerMixed:
		// Another listener uses the port the pattern needs: never touch it, never apply.
		m.recordObserved(cfg, host, t, want)
		blocked := []doctor.Result{res(doctor.NET005, blockedDetails(want, state0))}
		if state0 == listenerMixed {
			// Our "/" handler is published beside others: if Funnel is on for the port it is public.
			// Never changed (not ours alone), always reported.
			fr, err := m.funnelGuard(ctx, s, cfg, host)
			if err != nil {
				return nil, err
			}
			blocked = append(blocked, fr...)
		}
		return blocked, nil
	case listenerFree:
		if !s.Manage {
			m.recordObserved(cfg, host, t, want)
			break
		}
		drift := m.appliedPattern() == want
		args := applyArgs(want, t)
		reason, err := m.change(ctx, args...)
		if err != nil {
			return nil, err
		}
		cfg2, rr := m.readServe(ctx)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case rr != "":
			return []doctor.Result{res(doctor.NET004, map[string]any{"reason": firstNonEmpty(reason, rr), "command": shellLine(args)})}, nil
		case m.patternState(cfg2, want, host, t) != listenerOurs:
			// The command may have exited 0 and still not produced our listener (a partial apply).
			return []doctor.Result{res(doctor.NET004, map[string]any{"reason": firstNonEmpty(reason, "verify failed"), "command": shellLine(args)})}, nil
		}
		cfg = cfg2
		m.log.Info("tailscale serve applied", "pattern", want)
		if drift {
			out = append(out, res(doctor.NET007, map[string]any{"pattern": want}))
		}
	}

	m.recordObserved(cfg, host, t, want)

	// The other pattern's listener of ours: reported, never removed here (4.7.2).
	other := PatternA
	if want == PatternA {
		other = PatternB
	}
	if m.patternState(cfg, other, host, t) == listenerOurs {
		out = append(out, res(doctor.NET005, map[string]any{"pattern": other, "extra": true, "command": shellLine(removeArgs(other))}))
	}
	out = append(out, m.extraResults(cfg, host, t)...)

	fr, err := m.funnelGuard(ctx, s, cfg, host)
	if err != nil {
		return nil, err
	}
	return append(out, fr...), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func blockedDetails(pattern string, l listener) map[string]any {
	port := PatternBPort
	if pattern == PatternA {
		port = PatternAPort
	}
	return map[string]any{"pattern": pattern, "port": port, "blocked_by": l.String()}
}

func (m *TailscaleManager) patternState(cfg serveConfig, pattern, host string, t target) listener {
	if pattern == PatternA {
		return cfg.patternA(t)
	}
	return cfg.patternB(host, t)
}

// recordObserved records which pattern of ours the config shows.
func (m *TailscaleManager) recordObserved(cfg serveConfig, host string, t target, want string) {
	a, b := cfg.patternA(t), cfg.patternB(host, t)
	obs := observedPattern(a, b, want)
	m.recordApplied(obs, want != PatternNone && m.patternState(cfg, want, host, t) == listenerOurs)
}

// extraResults reports the listeners to our llama port that are not ours (4.7.1: left alone). In
// apply, our listeners of either pattern are not extras (the other pattern is reported on its own).
func (m *TailscaleManager) extraResults(cfg serveConfig, host string, t target) []doctor.Result {
	ownedTCP, ownedHTTPS := map[string]bool{}, map[string]bool{}
	if cfg.patternA(t) == listenerOurs {
		ownedTCP[strconv.Itoa(PatternAPort)] = true
	}
	if l := cfg.patternB(host, t); l == listenerOurs || l == listenerMixed {
		// A mixed 443 is reported on its own (the pattern's port is shared), not once more as an extra.
		ownedHTTPS[strconv.Itoa(PatternBPort)] = true
	}
	var out []doctor.Result
	for _, x := range cfg.extras(t, ownedTCP, ownedHTTPS) {
		d := map[string]any{"extra": true, "kind": x.Kind, "port": x.Port}
		if c := x.command(); c != "" {
			d["command"] = c
		} else {
			d["shared_port"] = true
		}
		out = append(out, res(doctor.NET005, d))
	}
	return out
}

// funnelGuard turns Funnel off for our port (4.7.2) when it is on and the port serves our listener.
// A port that does not serve our (sole) listener is left alone and only reported.
func (m *TailscaleManager) funnelGuard(ctx context.Context, s TailscaleSettings, cfg serveConfig, host string) ([]doctor.Result, error) {
	t := s.target()
	var out []doctor.Result
	for _, port := range cfg.funnelOn(host) {
		l := cfg.patternB(host, t)
		if port == PatternAPort {
			l = cfg.patternA(t)
		}
		if l == listenerFree || l == listenerForeign {
			continue // not our port's funnel: nothing of ours is exposed by it
		}
		args := funnelArgs(port)
		d := map[string]any{"port": port, "turned_off": false, "command": shellLine(args)}
		if l == listenerOurs && s.Manage {
			reason, err := m.change(ctx, args...)
			if err != nil {
				return nil, err
			}
			cfg2, rr := m.readServe(ctx)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if reason == "" && rr == "" && !contains(cfg2.funnelOn(host), port) {
				d["turned_off"] = true
				m.log.Warn("tailscale funnel was on for our port and has been turned off", "port", port)
			}
		}
		out = append(out, res(doctor.NET009, d))
	}
	return out, nil
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---- remove ---------------------------------------------------------------------------------------

func (m *TailscaleManager) remove(ctx context.Context) ([]doctor.Result, error) {
	st, kind, reason := m.readStatus(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch kind {
	case statusNotInstalled, statusDaemonDown:
		// Nothing runs, so nothing is published (agent.md section 5).
		m.cmu.Lock()
		m.applied = false
		m.cmu.Unlock()
		return nil, nil
	case statusFailed:
		return []doctor.Result{res(doctor.NET004, map[string]any{"reason": reason})}, nil
	}
	cfg, reason := m.readServe(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reason != "" {
		return []doctor.Result{res(doctor.NET004, map[string]any{"reason": reason})}, nil
	}
	return m.removeOurs(ctx, m.settings(), cfg, st.DNSName, true)
}

// removeOurs is §4.7.3: remove each of ours, re-read, verify. offline says the node must not be
// published at all (an offline or survival transition); otherwise it is the online "none" pattern.
func (m *TailscaleManager) removeOurs(ctx context.Context, s TailscaleSettings, cfg serveConfig, host string, offline bool) ([]doctor.Result, error) {
	t := s.target()
	stillCode := doctor.NET004
	if offline {
		stillCode = doctor.NET006
	}
	var out []doctor.Result
	var todo []string
	for _, p := range []string{PatternA, PatternB} {
		switch m.patternState(cfg, p, host, t) {
		case listenerOurs:
			todo = append(todo, p)
		case listenerMixed:
			// Removing 443 would remove other people's handlers too: report, never touch.
			code := doctor.NET005
			if offline {
				code = doctor.NET006
			}
			out = append(out, res(code, map[string]any{"pattern": p, "port": PatternBPort, "shared_port": true}))
		}
	}
	if len(todo) == 0 {
		m.recordApplied("", false)
		return append(out, m.extraResults(cfg, host, t)...), nil
	}

	var cmds []string
	for _, p := range todo {
		cmds = append(cmds, shellLine(removeArgs(p)))
	}
	if !s.Manage {
		// The agent never runs `tailscale serve` here; the operator removes it.
		m.recordObserved(cfg, host, t, s.pattern())
		return append(out, res(stillCode, map[string]any{"commands": cmds, "manage": false})), nil
	}

	var failed []string
	for _, p := range todo {
		reason, err := m.change(ctx, removeArgs(p)...)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			failed = append(failed, reason)
		}
	}
	cfg2, rr := m.readServe(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rr != "" {
		return append(out, res(doctor.NET004, map[string]any{"reason": rr, "commands": cmds})), nil
	}
	var left []string
	for _, p := range todo {
		if m.patternState(cfg2, p, host, t) == listenerOurs {
			left = append(left, shellLine(removeArgs(p)))
		}
	}
	if len(left) > 0 {
		d := map[string]any{"commands": left}
		if len(failed) > 0 {
			d["reason"] = strings.Join(failed, "; ")
		}
		m.recordObserved(cfg2, host, t, s.pattern())
		return append(out, res(stillCode, d)), nil
	}
	m.recordApplied("", false)
	m.log.Info("tailscale serve listeners removed", "patterns", len(todo))
	return append(out, m.extraResults(cfg2, host, t)...), nil
}

// ---- reachability (the slow doctor check) -------------------------------------------------------

// CheckServeReachable implements doctor.NetSources.Reachable (net.serve_reachable): one request to the
// node's own tailnet URL with a 30 s budget, because the first request of pattern B can take about
// 20 s while Tailscale issues the certificate. A timeout is LAI-NET-008 (info); any HTTP answer, even
// an error status, means the listener is reachable. Other failures are LAI-NET-004 with the reason.
func (m *TailscaleManager) CheckServeReachable(ctx context.Context) []doctor.Result {
	if m.settings().pattern() == PatternNone {
		return nil
	}
	base := m.TailnetURL(ctx)
	if base == "" {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, doctor.ServeReachableTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, strings.TrimSuffix(base, "/v1")+"/health", nil)
	if err != nil {
		return nil
	}
	resp, err := m.http.Do(req)
	if err != nil {
		if rctx.Err() != nil || isTimeout(err) {
			return []doctor.Result{res(doctor.NET008, nil)}
		}
		if ctx.Err() != nil {
			return nil
		}
		return []doctor.Result{res(doctor.NET004, map[string]any{"reason": "the listener did not answer"})}
	}
	_ = resp.Body.Close()
	return nil
}

// ---- the reconciler loop (app worker) -------------------------------------------------------------

// Name implements app.Worker.
func (m *TailscaleManager) Name() string { return "tailscale" }

// notReady reports whether the last pass found tailscaled not running or not logged in yet.
func (m *TailscaleManager) notReady() bool {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	for _, r := range m.results {
		if r.Code == doctor.NET002 || r.Code == doctor.NET003 {
			return true
		}
	}
	return false
}

// Start runs the drift reconciler: a pass every Interval (5 min) while the node is online, and every
// RetryInterval (15 s) while the last pass found tailscaled not running or not logged in yet, so a
// boot-time LAI-NET-002/003 clears soon after tailscaled is up. One ticker at the retry period drives
// both. It returns at once; the loop ends with ctx or Stop.
func (m *TailscaleManager) Start(ctx context.Context) error {
	m.loopMu.Lock()
	defer m.loopMu.Unlock()
	if m.loopCancel != nil {
		return nil
	}
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.loopCancel, m.loopDone = cancel, done
	tk := m.clock.NewTicker(m.retry)
	last := m.clock.Now()
	go func() {
		defer close(done)
		defer tk.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-tk.C():
			}
			now := m.clock.Now()
			if now.Sub(last) < m.interval && !m.notReady() {
				continue
			}
			last = now
			pctx, pcancel := context.WithTimeout(lctx, 60*time.Second)
			if _, err := m.Reconcile(pctx); err != nil && lctx.Err() == nil {
				m.log.Warn("tailscale reconcile aborted", "error", err.Error())
			}
			pcancel()
		}
	}()
	return nil
}

// Stop ends the reconciler loop and waits for it within ctx.
func (m *TailscaleManager) Stop(ctx context.Context) error {
	m.loopMu.Lock()
	cancel, done := m.loopCancel, m.loopDone
	m.loopCancel, m.loopDone = nil, nil
	m.loopMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
