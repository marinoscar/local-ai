package modes_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

const (
	cloudURL    = "https://cloud.example.test"
	cloudHealth = "HEAD https://cloud.example.test/api/health"
	hfHead      = "HEAD https://huggingface.co"
)

type fakeTailnet struct {
	up    atomic.Bool
	calls atomic.Int32
}

func (f *fakeTailnet) TailnetUp(context.Context) bool {
	f.calls.Add(1)
	return f.up.Load()
}

type proberRig struct {
	t      *testing.T
	st     *state.Store
	clk    *cfake.Clock
	http   *fakeHTTP
	tail   *fakeTailnet
	p      *modes.ConnectivityProber
	online atomic.Bool
	// onlineCalls counts the loop's policy checks: it moves once per loop iteration, which is the
	// signal that a tick was consumed even when the iteration then skips its pass.
	onlineCalls atomic.Int32

	mu  sync.Mutex
	set modes.ProberSettings
}

func newProberRig(t *testing.T, cloud string, mods ...func(*modes.ProberOptions)) *proberRig {
	t.Helper()
	r := &proberRig{t: t, clk: cfake.NewClock(t0), http: newFakeHTTP(), tail: &fakeTailnet{}, set: modes.ProberSettings{CloudURL: cloud}}
	r.st = openStore(t, statefake.New(), r.clk)
	t.Cleanup(func() { _ = r.st.Close() })
	r.online.Store(true)
	o := modes.ProberOptions{
		State: r.st, HTTP: r.http, Tailnet: r.tail, Clock: r.clk, Online: func() bool { r.onlineCalls.Add(1); return r.online.Load() },
		Settings: func() modes.ProberSettings {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.set
		},
	}
	for _, m := range mods {
		m(&o)
	}
	p, err := modes.NewProber(o)
	if err != nil {
		t.Fatal(err)
	}
	r.p = p
	t.Cleanup(p.Stop)
	return r
}

func (r *proberRig) conn() state.Connectivity { return r.st.Snapshot().Connectivity }

func (r *proberRig) probe() { r.p.Probe(context.Background()) }

// script sets the answers of the two HTTP checks.
func (r *proberRig) script(cloud, hf httpReply) {
	r.http.on("HEAD", cloudURL+"/api/health", cloud)
	r.http.on("HEAD", "https://huggingface.co", hf)
}

var (
	ok200   = httpReply{status: 200}
	down503 = httpReply{status: 503}
	netDown = httpReply{err: &url.Error{Op: "Head", Err: errors.New("network is unreachable")}}
)

func dnsFail() httpReply {
	return httpReply{err: &url.Error{Op: "Head", Err: &net.DNSError{Err: "no such host", Name: "cloud.example.test", IsNotFound: true}}}
}

func TestProbeResultClasses(t *testing.T) {
	tests := []struct {
		name      string
		cloud, hf httpReply
		tailnet   bool
		want      string
		wantCalls []string
		wantTail  int32
	}{
		{"cloud answers 2xx: full", ok200, httpReply{}, false, "full", []string{cloudHealth}, 0},
		{"cloud 204 is a 2xx", httpReply{status: 204}, httpReply{}, false, "full", []string{cloudHealth}, 0},
		{"cloud 503, internet 200: internet", down503, ok200, false, "internet", []string{cloudHealth, hfHead}, 0},
		{"cloud redirect is not healthy, internet 301: internet", httpReply{status: 301}, httpReply{status: 301}, false, "internet", []string{cloudHealth, hfHead}, 0},
		{"cloud unreachable, internet 302: internet", netDown, httpReply{status: 302}, false, "internet", []string{cloudHealth, hfHead}, 0},
		{"internet 404 is not an answer", netDown, httpReply{status: 404}, true, "tailnet", []string{cloudHealth, hfHead}, 1},
		{"no internet, tailnet up: tailnet", netDown, netDown, true, "tailnet", []string{cloudHealth, hfHead}, 1},
		{"everything fails: none", netDown, netDown, false, "none", []string{cloudHealth, hfHead}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newProberRig(t, cloudURL)
			r.script(tc.cloud, tc.hf)
			r.tail.up.Store(tc.tailnet)
			r.probe()
			c := r.conn()
			if c.Value != tc.want {
				t.Errorf("value = %q, want %q", c.Value, tc.want)
			}
			if c.Since == nil || !c.Since.Equal(t0) {
				t.Errorf("since = %v, want %v", c.Since, t0)
			}
			if (tc.want == "none") != (c.NoneSince != nil) {
				t.Errorf("none_since = %v for %q", c.NoneSince, tc.want)
			}
			if got := r.http.Calls(); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Errorf("requests = %v, want %v", got, tc.wantCalls)
			}
			if got := r.tail.calls.Load(); got != tc.wantTail {
				t.Errorf("tailnet checks = %d, want %d", got, tc.wantTail)
			}
			if !r.p.LastCheckedAt().Equal(t0) || r.p.Probes() != 1 {
				t.Errorf("checked_at %v probes %d", r.p.LastCheckedAt(), r.p.Probes())
			}
		})
	}
}

func TestProbeCloudURLVariants(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantCalls []string
		want      string
	}{
		{"trailing slash", cloudURL + "/", []string{cloudHealth, hfHead}, "internet"},
		{"surrounding spaces", " " + cloudURL + " ", []string{cloudHealth, hfHead}, "internet"},
		{"no cloud configured: only the internet check, and a good one is not 'full'", "", []string{hfHead}, "internet"},
		{"not an http URL is ignored", "ftp://cloud.example.test", []string{hfHead}, "internet"},
		{"no host is ignored", "https://", []string{hfHead}, "internet"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newProberRig(t, tc.url)
			r.http.on("HEAD", cloudURL+"/api/health", down503).on("HEAD", "https://huggingface.co", ok200)
			r.probe()
			if got := r.http.Calls(); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Errorf("requests = %v, want %v", got, tc.wantCalls)
			}
			if r.conn().Value != tc.want {
				t.Errorf("value = %q", r.conn().Value)
			}
		})
	}
	t.Run("dev_insecure loopback URL", func(t *testing.T) {
		r := newProberRig(t, "http://127.0.0.1:3000")
		r.http.on("HEAD", "http://127.0.0.1:3000/api/health", ok200)
		r.probe()
		if r.conn().Value != "full" {
			t.Errorf("value = %q", r.conn().Value)
		}
	})
}

func TestProbeHysteresis(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(ok200, ok200)
	r.probe()
	if r.conn().Value != "full" {
		t.Fatalf("value = %q", r.conn().Value)
	}
	fullSince := *r.conn().Since

	// One failing pass is not reported: a worse value needs two passes in a row.
	r.script(netDown, netDown)
	r.clk.Advance(30 * time.Second)
	firstFail := r.clk.Now()
	r.probe()
	if c := r.conn(); c.Value != "full" || c.NoneSince != nil || !c.Since.Equal(fullSince) {
		t.Fatalf("after one failure: %+v", c)
	}
	// A good pass resets the streak.
	r.script(ok200, ok200)
	r.clk.Advance(30 * time.Second)
	r.probe()
	r.script(netDown, netDown)
	r.clk.Advance(30 * time.Second)
	r.probe()
	if r.conn().Value != "full" {
		t.Fatalf("a glitch every other pass must not be reported: %+v", r.conn())
	}
	r.clk.Advance(30 * time.Second)
	secondFail := r.clk.Now().Add(-30 * time.Second)
	r.probe()
	c := r.conn()
	if c.Value != "none" {
		t.Fatalf("two failures in a row: value = %q, want none", c.Value)
	}
	// none_since is the first failing pass of the streak (the evidence), since is the report.
	if c.NoneSince == nil || !c.NoneSince.Equal(secondFail) || c.NoneSince.Equal(firstFail) {
		t.Errorf("none_since = %v, want %v", c.NoneSince, secondFail)
	}
	if !c.Since.Equal(r.clk.Now()) {
		t.Errorf("since = %v, want %v", c.Since, r.clk.Now())
	}

	// none_since survives more failing passes...
	r.clk.Advance(30 * time.Second)
	r.probe()
	if got := r.conn().NoneSince; got == nil || !got.Equal(secondFail) {
		t.Errorf("none_since moved to %v", got)
	}
	// ...and an improvement is reported at once and clears it.
	r.script(down503, ok200)
	r.clk.Advance(30 * time.Second)
	r.probe()
	if c := r.conn(); c.Value != "internet" || c.NoneSince != nil {
		t.Errorf("recovery: %+v", c)
	}
	// Worse again (full -> none) only after two passes; none_since is the new streak's first pass.
	r.script(ok200, ok200)
	r.clk.Advance(30 * time.Second)
	r.probe()
	r.script(netDown, netDown)
	r.clk.Advance(30 * time.Second)
	again := r.clk.Now()
	r.probe()
	r.clk.Advance(30 * time.Second)
	r.probe()
	if c := r.conn(); c.Value != "none" || c.NoneSince == nil || !c.NoneSince.Equal(again) {
		t.Errorf("second outage: %+v (want none_since %v)", c, again)
	}
}

func TestProbeDownAfterOne(t *testing.T) {
	r := newProberRig(t, cloudURL, func(o *modes.ProberOptions) { o.DownAfter = 1 })
	r.script(ok200, ok200)
	r.probe()
	r.script(netDown, netDown)
	r.probe()
	if r.conn().Value != "none" {
		t.Errorf("value = %q: with DownAfter 1 a failure is reported at once", r.conn().Value)
	}
}

func TestProbeStepsDownThroughTheLevels(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(ok200, ok200)
	r.probe()
	r.script(down503, ok200)
	r.probe()
	r.probe()
	if r.conn().Value != "internet" {
		t.Errorf("full -> internet: %q", r.conn().Value)
	}
	r.tail.up.Store(true)
	r.script(netDown, netDown)
	r.probe()
	r.probe()
	if r.conn().Value != "tailnet" {
		t.Errorf("internet -> tailnet: %q", r.conn().Value)
	}
}

func TestSuggestSurvivalAfterThirtyMinutesOfNone(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(netDown, netDown)
	r.probe() // first pass adopts none at once
	noneAt := t0
	if got := r.conn().NoneSince; got == nil || !got.Equal(noneAt) {
		t.Fatalf("none_since = %v", got)
	}
	if got := r.p.ConnectivityResults(); len(got) != 0 {
		t.Fatalf("results right after the first failure: %v", got)
	}
	r.clk.Advance(30 * time.Minute)
	if got := r.p.ConnectivityResults(); len(got) != 0 {
		t.Errorf("exactly 30 minutes is not more than 30 minutes: %v", got)
	}
	r.clk.Advance(time.Second)
	rs := r.p.ConnectivityResults()
	if got := resultCodes(rs); !reflect.DeepEqual(got, []string{doctor.NET011}) {
		t.Fatalf("codes = %v, want NET-011", got)
	}
	if c, _ := doctor.Lookup(doctor.NET011); c.Severity != doctor.Info {
		t.Errorf("NET-011 is %s", c.Severity)
	}

	// The engine's GET /mode flag follows the same state: suggest, never switch.
	rg := newRig(t, withSeed(func(s *state.State) {
		s.Connectivity = state.Connectivity{Value: state.ConnNone, NoneSince: ptrTime(t0)}
	}))
	if rg.eng.Info().SuggestSurvival {
		t.Error("suggested at once")
	}
	rg.clk.Advance(31 * time.Minute)
	if info := rg.eng.Info(); !info.SuggestSurvival || info.Mode != modes.Online {
		t.Errorf("info = %+v", info)
	}

	// Recovery clears the alert.
	r.script(ok200, ok200)
	r.probe()
	if got := r.p.ConnectivityResults(); len(got) != 0 {
		t.Errorf("results after recovery: %v", got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestProbeIsSkippedWhenNotOnline(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.online.Store(false)
	r.probe()
	if len(r.http.Calls()) != 0 || r.tail.calls.Load() != 0 || r.p.Probes() != 0 {
		t.Errorf("a pass ran outside online mode: %v", r.http.Calls())
	}
	if v := r.conn().Value; v != "" && v != state.ConnNotChecked {
		t.Errorf("value = %q", v)
	}
}

func TestInternetUpCloudDownRaisesNET012(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(down503, ok200)
	r.probe()
	if got := resultCodes(r.p.ConnectivityResults()); !reflect.DeepEqual(got, []string{doctor.NET012}) {
		t.Errorf("codes = %v", got)
	}
	r.script(ok200, ok200)
	r.probe()
	if got := r.p.ConnectivityResults(); len(got) != 0 {
		t.Errorf("not cleared: %v", got)
	}
	// No cloud configured: internet works and there is no cloud that fails to answer.
	r2 := newProberRig(t, "")
	r2.http.on("HEAD", "https://huggingface.co", ok200)
	r2.probe()
	if got := r2.p.ConnectivityResults(); len(got) != 0 {
		t.Errorf("NET-012 without a cloud: %v", got)
	}
}

func TestDNSFailureOfTheCloudHostRaisesNET013(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(dnsFail(), ok200)
	r.probe()
	if got := r.p.ConnectivityResults(); len(resultCodes(got)) != 1 || resultCodes(got)[0] != doctor.NET012 {
		// One pass is not enough for NET-013 (hysteresis), but the internet-only value is reported.
		t.Errorf("after one pass: %v", resultCodes(got))
	}
	r.probe()
	got := resultCodes(r.p.ConnectivityResults())
	if want := []string{doctor.NET012, doctor.NET013}; !reflect.DeepEqual(got, want) {
		t.Errorf("codes = %v, want both (the DNS failure is specific to the cloud host)", got)
	}
	if c, _ := doctor.Lookup(doctor.NET013); c.Severity != doctor.Warning {
		t.Errorf("NET-013 is %s", c.Severity)
	}
	// A cloud that fails for another reason clears NET-013 and keeps NET-012.
	r.script(down503, ok200)
	r.probe()
	if got := resultCodes(r.p.ConnectivityResults()); !reflect.DeepEqual(got, []string{doctor.NET012}) {
		t.Errorf("codes = %v", got)
	}
	// DNS broken everywhere: none, and the lookup problem is what to fix first.
	r2 := newProberRig(t, cloudURL)
	r2.script(dnsFail(), dnsFail())
	r2.probe()
	r2.probe()
	got = resultCodes(r2.p.ConnectivityResults())
	if want := []string{doctor.NET013}; !reflect.DeepEqual(got, want) || r2.conn().Value != "none" {
		t.Errorf("codes = %v value %q", got, r2.conn().Value)
	}
}

func TestProberLoopStartStop(t *testing.T) {
	r := newProberRig(t, cloudURL)
	r.script(ok200, ok200)
	r.p.Start()
	r.p.Start() // idempotent
	waitFor(t, "the first pass", func() bool { return r.p.Probes() == 1 })
	if r.conn().Value != "full" { // Probes counts a pass once it is recorded
		t.Fatalf("value = %q", r.conn().Value)
	}
	if r.clk.Tickers() != 1 {
		t.Fatalf("tickers = %d", r.clk.Tickers())
	}
	r.clk.Advance(30 * time.Second)
	waitFor(t, "the second pass", func() bool { return r.p.Probes() == 2 })

	// A pass is skipped, not stopped, while the node is not online; the next one runs again.
	r.online.Store(false)
	seen := r.onlineCalls.Load()
	r.clk.Advance(30 * time.Second)
	waitFor(t, "the loop to consume the tick", func() bool { return r.onlineCalls.Load() > seen })
	if r.p.Probes() != 2 {
		t.Errorf("probed while not online: %d", r.p.Probes())
	}
	r.online.Store(true)

	r.p.Stop()
	r.p.Stop() // idempotent
	waitFor(t, "ticker stopped", func() bool { return r.clk.Tickers() == 0 })
	// Stop waited for the loop to end and its ticker is gone: no tick can reach anything.
	n := r.p.Probes()
	seen = r.onlineCalls.Load()
	r.clk.Advance(5 * time.Minute)
	if r.onlineCalls.Load() != seen || r.p.Probes() != n {
		t.Error("probing went on after Stop")
	}
	if got := r.p.ConnectivityResults(); got != nil {
		t.Errorf("a stopped prober reports %v", got)
	}

	// Start again: fresh run.
	r.p.Start()
	waitFor(t, "a pass after restart", func() bool { return r.p.Probes() == n+1 })
	r.p.Stop()
}

// blockingHTTP parks every request until released, ignoring the request context: a pass that does not
// notice it was cancelled.
type blockingHTTP struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingHTTP) Do(req *http.Request) (*http.Response, error) {
	b.entered <- struct{}{}
	<-b.release
	return nil, &url.Error{Op: "Head", Err: errors.New("late")}
}

func TestStopDropsThePassInFlight(t *testing.T) {
	bh := &blockingHTTP{entered: make(chan struct{}, 4), release: make(chan struct{})}
	r := newProberRig(t, cloudURL, func(o *modes.ProberOptions) { o.HTTP = bh })
	r.p.Start()
	select {
	case <-bh.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass never started")
	}
	start := time.Now()
	r.p.Stop() // gives up waiting after half a second
	if time.Since(start) > 3*time.Second {
		t.Errorf("Stop took %v", time.Since(start))
	}
	// The engine's prober_stop then records not_checked...
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Connectivity.Value = state.ConnNotChecked
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// ...and the late pass, finishing afterwards, must not overwrite it.
	close(bh.release)
	// The pass is over once it has reached its last check (the tailnet one); any write it could still
	// make is queued before this flush, so the state read below sees it.
	waitFor(t, "the late pass to finish its checks", func() bool { return r.tail.calls.Load() == 1 })
	if err := r.st.MutateSync(func(*state.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := r.conn().Value; got != state.ConnNotChecked {
		t.Errorf("a pass that was in flight at Stop wrote %q", got)
	}
	if r.p.Probes() != 0 {
		t.Errorf("a cancelled pass counted: %d", r.p.Probes())
	}
}

func TestStartResumesFromTheRecordedValue(t *testing.T) {
	r := newProberRig(t, cloudURL)
	earlier := t0.Add(-10 * time.Minute)
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Connectivity = state.Connectivity{Value: state.ConnNone, Since: ptrTime(earlier), NoneSince: ptrTime(earlier)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.script(netDown, netDown)
	r.p.Start()
	waitFor(t, "a pass", func() bool { return r.p.Probes() == 1 })
	r.p.Stop()
	c := r.conn()
	if c.Value != "none" || c.NoneSince == nil || !c.NoneSince.Equal(earlier) {
		t.Errorf("a restart in online mode must keep none_since: %+v", c)
	}
}

func TestProberThroughTheEngine(t *testing.T) {
	var p *modes.ConnectivityProber
	var hh *fakeHTTP
	var eng atomic.Pointer[modes.Engine]
	r := newRig(t, withMode(modes.Offline), withDeps(func(d *modes.Deps) {
		hh = newFakeHTTP().on("HEAD", cloudURL+"/api/health", ok200)
		var err error
		p, err = modes.NewProber(modes.ProberOptions{
			State: d.State, HTTP: hh, Settings: func() modes.ProberSettings { return modes.ProberSettings{CloudURL: cloudURL} },
			Online: func() bool { e := eng.Load(); return e != nil && e.Online() },
		})
		if err != nil {
			t.Fatal(err)
		}
		d.Prober = p
	}))
	eng.Store(r.eng)
	t.Cleanup(p.Stop)

	if got := r.eng.Info().Connectivity; got != state.ConnNotChecked {
		t.Fatalf("offline connectivity = %q", got)
	}
	r.mustRequest(modes.Online, true)
	waitFor(t, "the first probe", func() bool { return r.st.Snapshot().Connectivity.Value == state.ConnFull })
	if got := r.eng.Info().Connectivity; got != "full" {
		t.Errorf("online connectivity = %q", got)
	}

	r.mustRequest(modes.Offline, true)
	if got := r.st.Snapshot().Connectivity; got.Value != state.ConnNotChecked || got.NoneSince != nil {
		t.Errorf("after offline: %+v", got)
	}
	// prober_stop waited for the loop to end, so nothing can probe from here on (the loop's own
	// stop is covered by TestProberLoopStartStop on a manual clock).
	if got := p.ConnectivityResults(); got != nil {
		t.Errorf("results while offline: %v", got)
	}
}

// gatedStore is a StateStore whose Mutate parks until released, to hold a pass between "the checks
// finished" and "the result is recorded".
type gatedStore struct {
	modes.StateStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedStore(st modes.StateStore) *gatedStore {
	return &gatedStore{StateStore: st, entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (g *gatedStore) Mutate(fn func(*state.State) error) error {
	g.entered <- struct{}{}
	<-g.release
	return g.StateStore.Mutate(fn)
}

func (g *gatedStore) open() { g.once.Do(func() { close(g.release) }) }

// Regression (#221): a test that waited for Probes() == 1 and then read state.connectivity saw an
// empty value, because the pass was counted before its result was recorded. A pass is now counted only
// once recorded, and this test holds the write to prove it: it fails against the old order every time.
func TestAPassIsCountedOnlyOnceItsResultIsRecorded(t *testing.T) {
	var gate *gatedStore
	r := newProberRig(t, cloudURL, func(o *modes.ProberOptions) {
		gate = newGatedStore(o.State)
		o.State = gate
	})
	t.Cleanup(gate.open)
	r.script(ok200, ok200)
	r.p.Start()
	select {
	case <-gate.entered: // the checks are done and the write is parked
	case <-time.After(60 * time.Second):
		t.Fatal("the pass never reached its write")
	}
	if n := r.p.Probes(); n != 0 {
		t.Fatalf("Probes = %d while the result is not recorded yet", n)
	}
	if v := r.conn().Value; v != "" && v != state.ConnNotChecked {
		t.Fatalf("value = %q before the write", v)
	}
	gate.open()
	waitFor(t, "the pass to be counted", func() bool { return r.p.Probes() == 1 })
	if v := r.conn().Value; v != "full" {
		t.Fatalf("value = %q: a counted pass must already be recorded", v)
	}
}
