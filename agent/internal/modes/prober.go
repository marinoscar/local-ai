package modes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Defaults of the connectivity prober (agent.md section 4.5, modes.md section 4).
const (
	// DefaultProbeEvery is the probe period in online mode.
	DefaultProbeEvery = 30 * time.Second
	// DefaultProbeTimeout is the timeout of each check of a pass.
	DefaultProbeTimeout = 3 * time.Second
	// DefaultDownAfter is how many consecutive passes must agree on a worse result before the
	// reported value gets worse (hysteresis). An improvement is reported at once.
	DefaultDownAfter = 2

	// ProbeInternetURL is the internet check: a HEAD that answers 2xx or 3xx.
	ProbeInternetURL = "https://huggingface.co"
	// ProbeCloudPath is appended to cloud.url for the cloud check.
	ProbeCloudPath = "/api/health"
)

// TailnetChecker is the tailnet half of the prober: BackendState Running and at least one peer
// Online. *TailscaleManager implements it.
type TailnetChecker interface {
	TailnetUp(ctx context.Context) bool
}

// ProberSettings is the part of node.yaml the prober reads, on every pass.
type ProberSettings struct {
	// CloudURL is cloud.url; empty means no cloud (the safe defaults), and the cloud check is skipped.
	CloudURL string
}

// ProberSettingsFromConfig adapts a config source (*config.Store.Get).
func ProberSettingsFromConfig(get func() config.Config) func() ProberSettings {
	return func() ProberSettings { return ProberSettings{CloudURL: get().Cloud.URL} }
}

// ProberOptions configure NewProber.
type ProberOptions struct {
	// State records state.connectivity; required.
	State StateStore
	// Settings returns the cloud URL; nil means no cloud.
	Settings func() ProberSettings
	// Online reports whether online policy applies (the engine's Online); checked before every pass,
	// not only at start (agent.md section 4.2.5). Nil means always.
	Online func() bool
	// HTTP is the client of the two HEAD checks. Default: a dedicated client with no proxy env,
	// DisableKeepAlives and 3 s timeouts, which does not follow redirects.
	HTTP HTTPDoer
	// Tailnet is the tailnet check; nil means the tailnet is never up.
	Tailnet TailnetChecker
	// Clock drives the period and the timestamps; default real.
	Clock config.Clock
	// Interval, Timeout, SuggestAfter and DownAfter override the defaults above and
	// DefaultSuggestSurvivalAfter.
	Interval, Timeout, SuggestAfter time.Duration
	DownAfter                       int
	// Logger defaults to a discarding logger.
	Logger *slog.Logger
}

// ConnectivityProber derives state.connectivity (agent.md section 4.5): every 30 s in online mode,
// HEAD <cloud.url>/api/health gives full, HEAD https://huggingface.co gives internet, a running
// tailscale with an online peer gives tailnet, anything else none. It records the value, when it
// began (since) and when none began (none_since), raises LAI-NET-011 after 30 minutes of none, and
// never changes the mode. It implements Prober (the engine's prober_start and prober_stop steps).
//
// A worse value is reported only after DownAfter consecutive passes agree (hysteresis); a better one
// at once. none_since is the time of the first failing pass of the streak.
type ConnectivityProber struct {
	st       StateStore
	settings func() ProberSettings
	online   func() bool
	http     HTTPDoer
	tailnet  TailnetChecker
	clock    config.Clock
	interval time.Duration
	timeout  time.Duration
	suggest  time.Duration
	downN    int
	log      *slog.Logger

	// gen changes on Start and Stop; a write from an older generation is dropped, so a pass that
	// was in flight when the engine stopped the prober never overwrites its not_checked.
	gen atomic.Uint64

	mu      sync.Mutex
	running bool
	// stopped is set by Stop and cleared by Start: a stopped prober reports no problems.
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}

	cur         string
	worse       int
	worseSince  time.Time
	dnsStreak   int
	cloudKnown  bool
	lastChecked time.Time
	passes      int
}

var _ Prober = (*ConnectivityProber)(nil)

// NewProber returns a prober. Nothing runs until Start.
func NewProber(o ProberOptions) (*ConnectivityProber, error) {
	if o.State == nil {
		return nil, errors.New("modes: ProberOptions needs State")
	}
	p := &ConnectivityProber{st: o.State, settings: o.Settings, online: o.Online, http: o.HTTP, tailnet: o.Tailnet,
		clock: o.Clock, interval: o.Interval, timeout: o.Timeout, suggest: o.SuggestAfter, downN: o.DownAfter, log: o.Logger}
	if p.clock == nil {
		p.clock = config.RealClock{}
	}
	if p.interval <= 0 {
		p.interval = DefaultProbeEvery
	}
	if p.timeout <= 0 {
		p.timeout = DefaultProbeTimeout
	}
	if p.suggest <= 0 {
		p.suggest = DefaultSuggestSurvivalAfter
	}
	if p.downN <= 0 {
		p.downN = DefaultDownAfter
	}
	if p.http == nil {
		p.http = newProbeClient(p.timeout)
	}
	if p.log == nil {
		p.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return p, nil
}

// rank orders the values from worst to best.
func rank(v string) int {
	switch v {
	case state.ConnFull:
		return 3
	case state.ConnInternet:
		return 2
	case state.ConnTailnet:
		return 1
	}
	return 0
}

func isProbed(v string) bool {
	return v == state.ConnFull || v == state.ConnInternet || v == state.ConnTailnet || v == state.ConnNone
}

// Start begins probing: one pass at once, then one every 30 s while the node is online. It is
// idempotent and does not block.
func (p *ConnectivityProber) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	gen := p.gen.Add(1)
	p.running, p.stopped, p.cancel, p.done = true, false, cancel, done
	// Resume from the recorded value (an agent restart in online mode keeps none_since), else from scratch.
	p.cur, p.worse, p.dnsStreak, p.cloudKnown = "", 0, 0, false
	if v := p.st.Snapshot().Connectivity.Value; isProbed(v) {
		p.cur = v
	}
	tk := p.clock.NewTicker(p.interval)
	go func() {
		defer close(done)
		defer tk.Stop()
		for {
			if p.online == nil || p.online() {
				p.probe(ctx, gen)
			}
			select {
			case <-ctx.Done():
				return
			case <-tk.C():
			}
		}
	}()
}

// Stop ends probing and forgets the verdict held in memory; it waits up to half a second for a pass in
// flight (the pass is cancelled and its write dropped). The state value is the engine's to reset
// (prober_stop sets not_checked). It is idempotent.
func (p *ConnectivityProber) Stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.running, p.stopped, p.cancel, p.done = false, true, nil, nil
	p.gen.Add(1)
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		p.log.Warn("connectivity prober did not stop in time")
	}
}

// Probe runs one pass now (the loop calls it; so do tests) and records the result. It does nothing
// when the node is not online.
func (p *ConnectivityProber) Probe(ctx context.Context) {
	if p.online != nil && !p.online() {
		return
	}
	p.probe(ctx, p.gen.Load())
}

// Probes is the number of passes whose result is recorded in state.connectivity, for tests and
// diagnostics.
func (p *ConnectivityProber) Probes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.passes
}

// LastCheckedAt is when the last pass finished (GET /status connectivity.checked_at); the zero time
// before the first pass.
func (p *ConnectivityProber) LastCheckedAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastChecked
}

type rawProbe struct {
	value string
	// cloudConfigured: a cloud URL was checked (or was to be).
	cloudConfigured bool
	// cloudDNS: the cloud check failed with a DNS lookup error.
	cloudDNS bool
}

func (p *ConnectivityProber) probe(ctx context.Context, gen uint64) {
	raw := p.check(ctx)
	if ctx.Err() != nil {
		return // cancelled mid-pass: its answer is not a measurement
	}
	now := p.clock.Now().UTC()

	p.mu.Lock()
	if p.gen.Load() != gen {
		p.mu.Unlock()
		return
	}
	p.lastChecked = now
	p.cloudKnown = raw.cloudConfigured
	if raw.cloudDNS {
		p.dnsStreak++
	} else {
		p.dnsStreak = 0
	}
	prev := p.cur
	next := raw.value
	noneAt := now
	switch {
	case prev == "" || rank(raw.value) >= rank(prev):
		p.worse = 0
	default:
		if p.worse == 0 {
			p.worseSince = now
		}
		p.worse++
		if p.worse >= p.downN {
			noneAt = p.worseSince
			p.worse = 0
		} else {
			next = prev // not yet: keep reporting the better value
		}
	}
	p.cur = next
	p.mu.Unlock()

	if err := p.st.Mutate(func(s *state.State) error {
		if p.gen.Load() != gen {
			return nil
		}
		c := &s.Connectivity
		if c.Value != next {
			ts := now
			c.Value, c.Since = next, &ts
		}
		switch {
		case next != state.ConnNone:
			c.NoneSince = nil
		case c.NoneSince == nil:
			ts := noneAt
			c.NoneSince = &ts
		}
		return nil
	}); err != nil {
		p.log.Warn("state.connectivity not recorded", "error", err.Error())
	}
	// A pass counts once its result is recorded, so Probes is a completion signal: a reader that
	// sees the count reach n also sees the value of the n-th pass in state.connectivity.
	p.mu.Lock()
	if p.gen.Load() == gen {
		p.passes++
	}
	p.mu.Unlock()
	if next != prev {
		p.log.Info("connectivity changed", "from", prev, "to", next)
	}
}

// check runs the probes in order and stops at the first that succeeds (agent.md section 4.5).
func (p *ConnectivityProber) check(ctx context.Context) rawProbe {
	var out rawProbe
	if base := p.cloudBase(); base != "" {
		out.cloudConfigured = true
		ok, err := p.head(ctx, base+ProbeCloudPath, false)
		if ok {
			out.value = state.ConnFull
			return out
		}
		var dns *net.DNSError
		out.cloudDNS = errors.As(err, &dns)
	}
	if ok, _ := p.head(ctx, ProbeInternetURL, true); ok {
		out.value = state.ConnInternet
		return out
	}
	if p.tailnet != nil {
		tctx, cancel := context.WithTimeout(ctx, p.timeout)
		up := p.tailnet.TailnetUp(tctx)
		cancel()
		if up {
			out.value = state.ConnTailnet
			return out
		}
	}
	out.value = state.ConnNone
	return out
}

// cloudBase is cloud.url without a trailing slash, "" when there is no usable cloud URL.
func (p *ConnectivityProber) cloudBase() string {
	if p.settings == nil {
		return ""
	}
	raw := strings.TrimRight(strings.TrimSpace(p.settings().CloudURL), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return raw
}

// head sends a HEAD with the pass timeout. okRedirect accepts 3xx besides 2xx.
func (p *ConnectivityProber) head(ctx context.Context, target string, okRedirect bool) (bool, error) {
	rctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodHead, target, nil)
	if err != nil {
		return false, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	code := resp.StatusCode
	if code >= 200 && code < 300 || okRedirect && code >= 300 && code < 400 {
		return true, nil
	}
	return false, fmt.Errorf("status %d", code)
}

// ConnectivityResults implements doctor.NetSources.Connectivity (net.connectivity, fast): it reads
// what the prober already knows and probes nothing. While the prober is stopped (offline, survival)
// it reports nothing.
//
//	LAI-NET-011  the value has been none for more than 30 minutes
//	LAI-NET-012  the value is internet: the internet answers, the configured cloud does not
//	LAI-NET-013  the cloud host's lookup failed with a DNS error on DownAfter consecutive passes
func (p *ConnectivityProber) ConnectivityResults() []doctor.Result {
	p.mu.Lock()
	stopped, cur, cloud, dns := p.stopped, p.cur, p.cloudKnown, p.dnsStreak >= p.downN
	p.mu.Unlock()
	if stopped {
		return nil
	}
	var out []doctor.Result
	c := p.st.Snapshot().Connectivity
	if c.Value == state.ConnNone && c.NoneSince != nil {
		if d := p.clock.Now().Sub(*c.NoneSince); d > p.suggest {
			out = append(out, doctor.Result{Code: doctor.NET011, Details: map[string]any{"none_for_minutes": int(d / time.Minute)}})
		}
	}
	if cur == state.ConnInternet && cloud {
		out = append(out, doctor.Result{Code: doctor.NET012})
	}
	if dns {
		out = append(out, doctor.Result{Code: doctor.NET013})
	}
	return out
}
