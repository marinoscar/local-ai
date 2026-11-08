package modes_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/modes/fake"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

func loadFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tailscale", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeHTTP is an HTTPDoer scripted by "<METHOD> <url>".
type fakeHTTP struct {
	mu     sync.Mutex
	routes map[string]httpReply
	calls  []string
}

type httpReply struct {
	status int
	err    error
}

func newFakeHTTP() *fakeHTTP { return &fakeHTTP{routes: map[string]httpReply{}} }

func (f *fakeHTTP) on(method, url string, r httpReply) *fakeHTTP {
	f.mu.Lock()
	f.routes[method+" "+url] = r
	f.mu.Unlock()
	return f
}

func (f *fakeHTTP) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	key := req.Method + " " + req.URL.String()
	f.mu.Lock()
	f.calls = append(f.calls, key)
	r, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: errors.New("fake: unexpected request")}
	}
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

const fakeHost = "hp-linux.<TAILNET>.ts.net"

type tsRig struct {
	t      *testing.T
	cli    *fake.TailscaleCLI
	mgr    *modes.TailscaleManager
	st     *state.Store
	clk    *cfake.Clock
	http   *fakeHTTP
	online atomic.Bool
	// onlineCalls counts the reconciler's policy checks (one per tick it consumes).
	onlineCalls atomic.Int32

	mu  sync.Mutex
	set modes.TailscaleSettings
}

func newTSRig(t *testing.T, pattern string, manage bool, fixtureName string) *tsRig {
	t.Helper()
	r := &tsRig{t: t, cli: fake.NewTailscaleCLI(), clk: cfake.NewClock(t0), http: newFakeHTTP(),
		set: modes.TailscaleSettings{Manage: manage, Pattern: pattern, LlamaPort: 8080}}
	r.st = openStore(t, statefake.New(), r.clk)
	t.Cleanup(func() { _ = r.st.Close() })
	if fixtureName != "" {
		if err := r.cli.LoadServe(loadFixture(t, fixtureName)); err != nil {
			t.Fatal(err)
		}
	}
	r.online.Store(true)
	mgr, err := modes.NewTailscaleManager(modes.TailscaleOptions{
		Run: r.cli, Lookup: r.cli.Lookup, State: r.st, Clock: r.clk, HTTP: r.http,
		Online:   func() bool { r.onlineCalls.Add(1); return r.online.Load() },
		Interval: 5 * time.Minute,
		Settings: func() modes.TailscaleSettings {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.set
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.mgr = mgr
	return r
}

func (r *tsRig) settings(f func(*modes.TailscaleSettings)) {
	r.mu.Lock()
	f(&r.set)
	r.mu.Unlock()
}

func (r *tsRig) applied() string { return r.st.Snapshot().Tailscale.AppliedPattern }

func (r *tsRig) seedApplied(p string) {
	r.t.Helper()
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Tailscale.AppliedPattern = p
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
}

// serveCfg is the fake's current configuration, decoded for assertions.
func (r *tsRig) serveText() string { return string(r.cli.ServeJSON()) }

func (r *tsRig) has(substr string) bool { return strings.Contains(r.serveText(), substr) }

func (r *tsRig) on() ([]modes.Warning, error) {
	return r.mgr.On(context.Background())
}

func (r *tsRig) off() ([]modes.Warning, error) {
	return r.mgr.Off(context.Background())
}

func resultCodes(rs []doctor.Result) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Code)
	}
	sort.Strings(out)
	return out
}

func warningCodes(ws []modes.Warning) []string {
	out := []string{}
	for _, w := range ws {
		out = append(out, w.Code)
	}
	sort.Strings(out)
	return out
}

func find(rs []doctor.Result, code string) *doctor.Result {
	for i := range rs {
		if rs[i].Code == code {
			return &rs[i]
		}
	}
	return nil
}

// noForbidden asserts the ownership rules: no command outside the supported set ran (serve reset,
// down, logout, funnel on a foreign port...), and every command went through the fixed PATH.
func (r *tsRig) noForbidden() {
	r.t.Helper()
	if u := r.cli.Unexpected(); len(u) > 0 {
		r.t.Errorf("forbidden or unknown tailscale commands ran: %v", u)
	}
	for _, c := range r.cli.Calls() {
		if strings.Contains(c, "reset") || strings.Contains(c, "logout") || strings.HasPrefix(c, "down") {
			r.t.Errorf("forbidden command: %s", c)
		}
	}
	for _, cmd := range r.cli.Cmds() {
		if cmd.Path != fake.BinPath || !reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}) || cmd.Timeout <= 0 || cmd.MaxOut <= 0 {
			r.t.Errorf("command %v: path %q env %v timeout %v maxout %d", cmd.Args, cmd.Path, cmd.Env, cmd.Timeout, cmd.MaxOut)
		}
	}
}

const (
	applyB   = "serve --bg --https=443 http://127.0.0.1:8080"
	applyA   = "serve --bg --tcp=18080 tcp://127.0.0.1:8080"
	offA     = "serve --tcp=18080 off"
	offB     = "serve --https=443 off"
	funnelB  = "funnel --https=443 off"
	cmdOffA  = "sudo tailscale serve --tcp=18080 off"
	cmdOffB  = "sudo tailscale serve --https=443 off"
	statusJS = "status --json"
	serveJS  = "serve status --json"
)

func TestOnApplyTable(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		pattern     string
		manage      bool
		wantChanges []string
		wantCodes   []string
		wantApplied string
		check       func(t *testing.T, r *tsRig, rs []doctor.Result)
	}{
		{name: "empty config, pattern B", fixture: "none", pattern: "B", manage: true,
			wantChanges: []string{applyB}, wantApplied: "B",
			check: func(t *testing.T, r *tsRig, _ []doctor.Result) {
				if !r.has(`"/":{"Proxy":"http://127.0.0.1:8080"}`) {
					t.Errorf("listener missing: %s", r.serveText())
				}
			}},
		{name: "empty config, pattern A", fixture: "none", pattern: "A", manage: true,
			wantChanges: []string{applyA}, wantApplied: "A"},
		{name: "A only, wants A: nothing to do", fixture: "a_only", pattern: "A", manage: true, wantApplied: "A"},
		{name: "B only, wants B: nothing to do", fixture: "b_only", pattern: "B", manage: true, wantApplied: "B"},
		{name: "A+B (ai-n3 today), wants B: the extra A is reported and kept", fixture: "a_and_b", pattern: "B", manage: true,
			wantCodes: []string{doctor.NET005}, wantApplied: "B",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				if got := find(rs, doctor.NET005).Details["command"]; got != cmdOffA {
					t.Errorf("command = %v, want %q", got, cmdOffA)
				}
				if !r.has(`"18080"`) {
					t.Error("the extra listener was removed")
				}
			}},
		{name: "A+B, wants A: the extra B is reported and kept", fixture: "a_and_b", pattern: "A", manage: true,
			wantCodes: []string{doctor.NET005}, wantApplied: "A",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				if got := find(rs, doctor.NET005).Details["command"]; got != cmdOffB {
					t.Errorf("command = %v, want %q", got, cmdOffB)
				}
			}},
		{name: "foreign handler on 443 blocks B", fixture: "foreign_443", pattern: "B", manage: true,
			wantCodes: []string{doctor.NET005},
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				if find(rs, doctor.NET005).Details["blocked_by"] != "foreign" {
					t.Errorf("details = %v", rs[0].Details)
				}
				if !r.has("127.0.0.1:3000") {
					t.Error("the foreign handler is gone")
				}
			}},
		{name: "foreign forward on 18080 blocks A", fixture: "foreign_18080", pattern: "A", manage: true, wantCodes: []string{doctor.NET005}},
		{name: "our / handler beside others on 443 blocks B", fixture: "mixed_443", pattern: "B", manage: true,
			wantCodes: []string{doctor.NET005}, wantApplied: "",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				if find(rs, doctor.NET005).Details["blocked_by"] != "mixed" {
					t.Errorf("details = %v", rs[0].Details)
				}
			}},
		{name: "funnel on for our port is turned off", fixture: "funnel_on", pattern: "B", manage: true,
			wantChanges: []string{funnelB}, wantCodes: []string{doctor.NET009}, wantApplied: "B",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				d := find(rs, doctor.NET009).Details
				if d["turned_off"] != true || d["port"] != 443 {
					t.Errorf("details = %v", d)
				}
				if r.has("AllowFunnel") || !r.has("127.0.0.1:8080") {
					t.Errorf("funnel must be off and the listener kept: %s", r.serveText())
				}
			}},
		{name: "foreign listeners to our port are reported, never removed", fixture: "extras", pattern: "B", manage: true,
			wantChanges: []string{applyB}, wantCodes: []string{doctor.NET005, doctor.NET005}, wantApplied: "B",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				var cmds []string
				for _, x := range rs {
					cmds = append(cmds, x.Details["command"].(string))
				}
				sort.Strings(cmds)
				want := []string{"sudo tailscale serve --https=8443 off", "sudo tailscale serve --tcp=9999 off"}
				if !reflect.DeepEqual(cmds, want) {
					t.Errorf("commands = %v, want %v", cmds, want)
				}
				if !r.has(`"9999"`) || !r.has(`:8443"`) {
					t.Error("a foreign listener was removed")
				}
			}},
		{name: "pattern none removes both of ours", fixture: "a_and_b", pattern: "none", manage: true,
			wantChanges: []string{offA, offB}, wantApplied: "",
			check: func(t *testing.T, r *tsRig, _ []doctor.Result) {
				if r.serveText() != "{}" {
					t.Errorf("left over: %s", r.serveText())
				}
			}},
		{name: "pattern none leaves a foreign listener", fixture: "foreign_443", pattern: "none", manage: true,
			check: func(t *testing.T, r *tsRig, _ []doctor.Result) {
				if !r.has("127.0.0.1:3000") {
					t.Error("foreign listener removed")
				}
			}},
		{name: "manage false, empty config: nothing is run", fixture: "none", pattern: "B", manage: false},
		{name: "manage false, funnel on: reported, not changed", fixture: "funnel_on", pattern: "B", manage: false,
			wantCodes: []string{doctor.NET009}, wantApplied: "B",
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				d := find(rs, doctor.NET009).Details
				if d["turned_off"] != false || d["command"] != "sudo tailscale funnel --https=443 off" {
					t.Errorf("details = %v", d)
				}
				if !r.has("AllowFunnel") {
					t.Error("funnel was changed")
				}
			}},
		{name: "manage false, A+B wants B: reported", fixture: "a_and_b", pattern: "B", manage: false,
			wantCodes: []string{doctor.NET005}, wantApplied: "B"},
		{name: "manage false, pattern none: our listener is reported, not removed", fixture: "a_and_b", pattern: "none", manage: false,
			wantCodes: []string{doctor.NET004}, wantApplied: "B"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, tc.pattern, tc.manage, tc.fixture)
			ws, err := r.on()
			if err != nil {
				t.Fatal(err)
			}
			rs := r.mgr.ServeResults()
			if got := resultCodes(rs); !reflect.DeepEqual(got, orEmpty(tc.wantCodes)) {
				t.Errorf("codes = %v, want %v", got, tc.wantCodes)
			}
			if got := r.cli.Changes(); !reflect.DeepEqual(got, tc.wantChanges) && !(len(got) == 0 && len(tc.wantChanges) == 0) {
				t.Errorf("changes = %v, want %v", got, tc.wantChanges)
			}
			if got := r.applied(); got != tc.wantApplied {
				t.Errorf("state.tailscale.applied_pattern = %q, want %q", got, tc.wantApplied)
			}
			// Every non-informational result is a warning of the transition.
			if got, want := warningCodes(ws), resultCodes(rs); !reflect.DeepEqual(got, want) {
				t.Errorf("warnings = %v, want %v", got, want)
			}
			if !tc.manage {
				for _, c := range r.cli.Calls() {
					if strings.HasPrefix(c, "serve ") && !strings.HasPrefix(c, "serve status") || strings.HasPrefix(c, "funnel") {
						t.Errorf("manage false ran: %s", c)
					}
				}
			}
			if tc.check != nil {
				tc.check(t, r, rs)
			}
			r.noForbidden()
		})
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	sorted := append([]string(nil), s...)
	sort.Strings(sorted)
	return sorted
}

func TestOnApplyIsIdempotent(t *testing.T) {
	r := newTSRig(t, "B", true, "none")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	before := r.cli.Changes()
	for i := 0; i < 3; i++ {
		if ws, err := r.on(); err != nil || len(ws) != 0 {
			t.Fatalf("pass %d: %v, %v", i, ws, err)
		}
	}
	if got := r.cli.Changes(); !reflect.DeepEqual(got, before) || len(got) != 1 {
		t.Errorf("changes after repeats = %v, want exactly the first apply", got)
	}
}

func TestOnPatternFollowsLiveConfig(t *testing.T) {
	r := newTSRig(t, "B", true, "none")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	r.settings(func(s *modes.TailscaleSettings) { s.Pattern = "A" })
	ws, err := r.mgr.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// B stays (the operator removes it), A is added, and B is reported as the extra.
	if got := warningCodes(ws); !reflect.DeepEqual(got, []string{doctor.NET005}) || r.applied() != "A" {
		t.Errorf("warnings %v applied %q", got, r.applied())
	}
	if !r.has(`"18080"`) || !r.has(`:443"`) {
		t.Errorf("config: %s", r.serveText())
	}
}

func TestOnUsesTheConfiguredLlamaPort(t *testing.T) {
	r := newTSRig(t, "B", true, "none")
	r.settings(func(s *modes.TailscaleSettings) { s.LlamaPort = 9090 })
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	if got := r.cli.Changes(); !reflect.DeepEqual(got, []string{"serve --bg --https=443 http://127.0.0.1:9090"}) {
		t.Errorf("changes = %v", got)
	}
}

func TestOnFailures(t *testing.T) {
	apply := applyB
	tests := []struct {
		name      string
		setup     func(*tsRig)
		wantCodes []string
		wantCalls []string
		check     func(t *testing.T, r *tsRig, rs []doctor.Result)
	}{
		{name: "tailscale is not installed", setup: func(r *tsRig) { r.cli.Installed = false },
			wantCodes: []string{doctor.NET001}, wantCalls: nil},
		{name: "tailscaled is not running", setup: func(r *tsRig) { r.cli.DaemonUp = false },
			wantCodes: []string{doctor.NET002}, wantCalls: []string{statusJS}},
		{name: "not logged in", setup: func(r *tsRig) { r.cli.Backend = "NeedsLogin" },
			wantCodes: []string{doctor.NET003}, wantCalls: []string{statusJS}},
		{name: "stopped by the operator", setup: func(r *tsRig) { r.cli.Backend = "Stopped" },
			wantCodes: []string{doctor.NET003}, wantCalls: []string{statusJS}},
		{name: "status times out", setup: func(r *tsRig) { r.cli.Script(statusJS, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS},
			check: func(t *testing.T, _ *tsRig, rs []doctor.Result) {
				if !strings.Contains(rs[0].Details["reason"].(string), "timed out") {
					t.Errorf("details = %v", rs[0].Details)
				}
			}},
		{name: "status fails for another reason", setup: func(r *tsRig) {
			r.cli.Script(statusJS, fake.Step{Res: execx.Result{ExitCode: 2, Stderr: []byte("flag provided but not defined")}})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS}},
		{name: "status is not JSON", setup: func(r *tsRig) {
			r.cli.Script(statusJS, fake.Step{Res: execx.Result{Stdout: []byte("Logged out.")}})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS}},
		{name: "serve status times out", setup: func(r *tsRig) { r.cli.Script(serveJS, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS}},
		{name: "serve status is garbage", setup: func(r *tsRig) {
			r.cli.Script(serveJS, fake.Step{Res: execx.Result{Stdout: []byte("not json")}})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS}},
		{name: "serve status exits non-zero", setup: func(r *tsRig) {
			r.cli.Script(serveJS, fake.Step{Res: execx.Result{ExitCode: 1, Stderr: []byte("access denied")}})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS}},
		{name: "the apply command fails", setup: func(r *tsRig) {
			r.cli.Script(apply, fake.Step{Res: execx.Result{ExitCode: 1, Stderr: []byte("denied")}})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS, apply, serveJS},
			check: func(t *testing.T, _ *tsRig, rs []doctor.Result) {
				if rs[0].Details["command"] != "sudo tailscale "+apply {
					t.Errorf("the fix must name the command: %v", rs[0].Details)
				}
			}},
		{name: "the apply command times out", setup: func(r *tsRig) { r.cli.Script(apply, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS, apply, serveJS}},
		{name: "partial apply: exit 0 but no listener", setup: func(r *tsRig) { r.cli.Script(apply, fake.Step{NoEffect: true}) },
			wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS, apply, serveJS},
			check: func(t *testing.T, _ *tsRig, rs []doctor.Result) {
				if rs[0].Details["reason"] != "verify failed" {
					t.Errorf("details = %v", rs[0].Details)
				}
			}},
		{name: "the verify read fails", setup: func(r *tsRig) {
			r.cli.Script(serveJS, fake.Step{Simulate: true}, fake.Step{Err: execx.ErrTimeout})
		}, wantCodes: []string{doctor.NET004}, wantCalls: []string{statusJS, serveJS, apply, serveJS}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, "B", true, "none")
			tc.setup(r)
			ws, err := r.on()
			if err != nil {
				t.Fatal(err)
			}
			rs := r.mgr.ServeResults()
			if got := resultCodes(rs); !reflect.DeepEqual(got, tc.wantCodes) {
				t.Errorf("codes = %v, want %v", got, tc.wantCodes)
			}
			if got := warningCodes(ws); !reflect.DeepEqual(got, tc.wantCodes) {
				t.Errorf("warnings = %v, want %v", got, tc.wantCodes)
			}
			if got := r.cli.Calls(); !reflect.DeepEqual(got, tc.wantCalls) && !(len(got) == 0 && len(tc.wantCalls) == 0) {
				t.Errorf("calls = %v, want %v", got, tc.wantCalls)
			}
			if r.applied() != "" {
				t.Errorf("applied_pattern = %q after a failure", r.applied())
			}
			if tc.check != nil {
				tc.check(t, r, rs)
			}
			r.noForbidden()
		})
	}
}

func TestFunnelOnForAMixedPortIsReportedNotChanged(t *testing.T) {
	r := newTSRig(t, "B", true, "")
	cfg := `{"TCP":{"443":{"HTTPS":true}},"Web":{"` + fakeHost + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8080"},"/docs":{"Proxy":"http://127.0.0.1:3000"}}}},` +
		`"AllowFunnel":{"` + fakeHost + `:443":true}}`
	if err := r.cli.LoadServe([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	rs := r.mgr.ServeResults()
	if got := resultCodes(rs); !reflect.DeepEqual(got, []string{doctor.NET005, doctor.NET009}) {
		t.Fatalf("codes = %v", got)
	}
	if d := find(rs, doctor.NET009).Details; d["turned_off"] != false {
		t.Errorf("details = %v", d)
	}
	if len(r.cli.Changes()) != 0 || !r.has("AllowFunnel") {
		t.Errorf("a shared port was changed: %v", r.cli.Changes())
	}
	r.noForbidden()
}

func TestOnFunnelOnWithoutOurListenerIsNotOursToJudge(t *testing.T) {
	r := newTSRig(t, "A", true, "")
	cfg := `{"TCP":{"443":{"HTTPS":true}},"Web":{"` + fakeHost + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}},` +
		`"AllowFunnel":{"` + fakeHost + `:443":true}}`
	if err := r.cli.LoadServe([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	// A is applied; the foreign funnel on 443 is someone else's decision.
	if got := r.cli.Changes(); !reflect.DeepEqual(got, []string{applyA}) {
		t.Errorf("changes = %v", got)
	}
	if got := resultCodes(r.mgr.ServeResults()); len(got) != 0 {
		t.Errorf("codes = %v", got)
	}
	r.noForbidden()
}

func TestOnFunnelOffFailureIsStillReported(t *testing.T) {
	r := newTSRig(t, "B", true, "funnel_on")
	r.cli.Script(funnelB, fake.Step{NoEffect: true})
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	rs := r.mgr.ServeResults()
	if d := find(rs, doctor.NET009).Details; d["turned_off"] != false {
		t.Errorf("details = %v: funnel is still on and the result must say so", d)
	}
	if !r.has("AllowFunnel") {
		t.Error("the fake should still have funnel on")
	}
}

func TestOnWithPatternNoneIgnoresAMissingTailscale(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*tsRig)
		calls []string
	}{
		{"not installed", func(r *tsRig) { r.cli.Installed = false }, nil},
		{"tailscaled down", func(r *tsRig) { r.cli.DaemonUp = false }, []string{statusJS}},
		{"logged out", func(r *tsRig) { r.cli.Backend = "NeedsLogin" }, []string{statusJS}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, "none", true, "none")
			tc.setup(r)
			ws, err := r.on()
			if err != nil || len(ws) != 0 || len(r.mgr.ServeResults()) != 0 {
				t.Fatalf("ws %v err %v results %v", ws, err, r.mgr.ServeResults())
			}
			if got := r.cli.Calls(); !reflect.DeepEqual(got, tc.calls) && !(len(got) == 0 && len(tc.calls) == 0) {
				t.Errorf("calls = %v, want %v", got, tc.calls)
			}
		})
	}
}

func TestOnStopsOnContextCancel(t *testing.T) {
	r := newTSRig(t, "B", true, "none")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.mgr.On(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(r.cli.Changes()) != 0 {
		t.Errorf("a cancelled step changed the config: %v", r.cli.Changes())
	}
	if _, err := r.mgr.Off(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Off err = %v", err)
	}
}

func TestDriftReapplyRaisesNET007OnceThenClears(t *testing.T) {
	r := newTSRig(t, "B", true, "b_only")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	if got := r.mgr.ServeResults(); len(got) != 0 || r.applied() != "B" {
		t.Fatalf("results %v applied %q", got, r.applied())
	}
	// Someone removed the listener behind the agent's back.
	if err := r.cli.LoadServe([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	ws, err := r.mgr.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rs := r.mgr.ServeResults()
	if got := resultCodes(rs); !reflect.DeepEqual(got, []string{doctor.NET007}) {
		t.Fatalf("codes = %v, want NET-007", got)
	}
	if len(ws) != 0 {
		t.Errorf("an info result is not a transition warning: %v", ws)
	}
	if c, _ := doctor.Lookup(doctor.NET007); c.Severity != doctor.Info {
		t.Errorf("NET-007 is %s, want info", c.Severity)
	}
	if got := r.cli.Changes(); !reflect.DeepEqual(got, []string{applyB}) {
		t.Errorf("changes = %v, want one re-apply", got)
	}
	// The next clean pass clears it.
	if _, err := r.mgr.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.mgr.ServeResults(); len(got) != 0 {
		t.Errorf("NET-007 must auto-clear, still have %v", got)
	}
}

func TestFirstApplyAfterOffIsNotDrift(t *testing.T) {
	r := newTSRig(t, "B", true, "b_only")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.off(); err != nil {
		t.Fatal(err)
	}
	if r.applied() != "" {
		t.Fatalf("applied = %q after Off", r.applied())
	}
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	if got := r.mgr.ServeResults(); len(got) != 0 {
		t.Errorf("results = %v: a re-apply after offline is not a restore", got)
	}
}

func TestOffTable(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		pattern     string
		wantChanges []string
		wantCodes   []string
	}{
		{"nothing there", "none", "B", nil, nil},
		{"A only, serve_pattern B", "a_only", "B", []string{offA}, nil},
		{"B only, serve_pattern A", "b_only", "A", []string{offB}, nil},
		{"A+B, serve_pattern B", "a_and_b", "B", []string{offA, offB}, nil},
		{"A+B, serve_pattern A", "a_and_b", "A", []string{offA, offB}, nil},
		{"A+B, serve_pattern none", "a_and_b", "none", []string{offA, offB}, nil},
		{"funnel on", "funnel_on", "B", []string{offB}, nil},
		{"foreign 443 is not touched", "foreign_443", "B", nil, nil},
		{"foreign 18080 is not touched", "foreign_18080", "A", nil, nil},
		{"foreign forwards to our port are reported, not removed", "extras", "B", nil, []string{doctor.NET005, doctor.NET005}},
		{"our / beside other handlers is reported as still published", "mixed_443", "B", nil, []string{doctor.NET006}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, tc.pattern, true, tc.fixture)
			before := r.serveText()
			ws, err := r.off()
			if err != nil {
				t.Fatal(err)
			}
			rs := r.mgr.ServeResults()
			if got := resultCodes(rs); !reflect.DeepEqual(got, orEmpty(tc.wantCodes)) {
				t.Errorf("codes = %v, want %v", got, tc.wantCodes)
			}
			if got := warningCodes(ws); !reflect.DeepEqual(got, orEmpty(tc.wantCodes)) {
				t.Errorf("warnings = %v, want %v", got, tc.wantCodes)
			}
			if got := r.cli.Changes(); !reflect.DeepEqual(got, tc.wantChanges) && !(len(got) == 0 && len(tc.wantChanges) == 0) {
				t.Errorf("changes = %v, want %v", got, tc.wantChanges)
			}
			if len(tc.wantChanges) == 0 && r.serveText() != before {
				t.Errorf("config changed: %s -> %s", before, r.serveText())
			}
			if len(tc.wantChanges) > 0 && (r.has("127.0.0.1:8080") || r.has("TCPForward")) {
				t.Errorf("ours still there: %s", r.serveText())
			}
			if len(tc.wantChanges) > 0 && r.applied() != "" {
				t.Errorf("applied = %q", r.applied())
			}
			r.noForbidden()
		})
	}
}

func TestOffRemovesOnlyOursBesideForeign(t *testing.T) {
	r := newTSRig(t, "B", true, "")
	cfg := `{"TCP":{"18080":{"TCPForward":"127.0.0.1:8080"},"8443":{"HTTPS":true},"2222":{"TCPForward":"127.0.0.1:22"}},` +
		`"Web":{"` + fakeHost + `:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`
	if err := r.cli.LoadServe([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.off(); err != nil {
		t.Fatal(err)
	}
	if got := r.cli.Changes(); !reflect.DeepEqual(got, []string{offA}) {
		t.Errorf("changes = %v, want only our A", got)
	}
	if !r.has(`"2222"`) || !r.has(`"8443"`) || r.has(`"18080"`) {
		t.Errorf("config = %s", r.serveText())
	}
}

func TestOffFailures(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		setup     func(*tsRig)
		wantCodes []string
		check     func(t *testing.T, r *tsRig, rs []doctor.Result)
	}{
		{name: "tailscale is not installed: nothing exposed", fixture: "none", setup: func(r *tsRig) { r.cli.Installed = false }},
		{name: "tailscaled is down: nothing exposed", fixture: "a_and_b", setup: func(r *tsRig) { r.cli.DaemonUp = false }},
		{name: "logged out still removes ours", fixture: "b_only", setup: func(r *tsRig) { r.cli.Backend = "NeedsLogin" }},
		{name: "residual listener after the command: NET-006 with the exact command", fixture: "b_only",
			setup:     func(r *tsRig) { r.cli.Script(offB, fake.Step{NoEffect: true}) },
			wantCodes: []string{doctor.NET006},
			check: func(t *testing.T, _ *tsRig, rs []doctor.Result) {
				if got := rs[0].Details["commands"]; !reflect.DeepEqual(got, []string{cmdOffB}) {
					t.Errorf("commands = %v", got)
				}
			}},
		{name: "the off command fails and the listener stays", fixture: "a_and_b",
			setup: func(r *tsRig) {
				r.cli.Script(offA, fake.Step{Res: execx.Result{ExitCode: 1, Stderr: []byte("denied")}})
			},
			wantCodes: []string{doctor.NET006},
			check: func(t *testing.T, r *tsRig, rs []doctor.Result) {
				if got := rs[0].Details["commands"]; !reflect.DeepEqual(got, []string{cmdOffA}) {
					t.Errorf("commands = %v: only the survivor is named", got)
				}
				if r.has("443") {
					t.Error("B must still have been removed")
				}
			}},
		{name: "the off command times out", fixture: "a_only",
			setup:     func(r *tsRig) { r.cli.Script(offA, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET006}},
		{name: "the verify read fails: cannot tell", fixture: "a_only",
			setup: func(r *tsRig) {
				r.cli.Script(serveJS, fake.Step{Simulate: true}, fake.Step{Err: execx.ErrTimeout})
			},
			wantCodes: []string{doctor.NET004}},
		{name: "serve status times out", fixture: "a_only",
			setup:     func(r *tsRig) { r.cli.Script(serveJS, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET004}},
		{name: "serve status is garbage", fixture: "a_only",
			setup: func(r *tsRig) {
				r.cli.Script(serveJS, fake.Step{Res: execx.Result{Stdout: []byte("<html>")}})
			},
			wantCodes: []string{doctor.NET004}},
		{name: "status times out", fixture: "a_only",
			setup:     func(r *tsRig) { r.cli.Script(statusJS, fake.Step{Err: execx.ErrTimeout}) },
			wantCodes: []string{doctor.NET004}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, "B", true, tc.fixture)
			tc.setup(r)
			ws, err := r.off()
			if err != nil {
				t.Fatal(err)
			}
			rs := r.mgr.ServeResults()
			if got := resultCodes(rs); !reflect.DeepEqual(got, orEmpty(tc.wantCodes)) {
				t.Errorf("codes = %v, want %v", got, tc.wantCodes)
			}
			// The engine retries on NET-004 and NET-006 warnings: they must be in the warnings.
			if got := warningCodes(ws); !reflect.DeepEqual(got, orEmpty(tc.wantCodes)) {
				t.Errorf("warnings = %v, want %v", got, tc.wantCodes)
			}
			if tc.check != nil {
				tc.check(t, r, rs)
			}
			r.noForbidden()
		})
	}
}

func TestOffWithManageFalseRunsNoServeCommand(t *testing.T) {
	r := newTSRig(t, "B", false, "a_and_b")
	ws, err := r.off()
	if err != nil {
		t.Fatal(err)
	}
	rs := r.mgr.ServeResults()
	if got := resultCodes(rs); !reflect.DeepEqual(got, []string{doctor.NET006}) {
		t.Fatalf("codes = %v", got)
	}
	if got := rs[0].Details["commands"]; !reflect.DeepEqual(got, []string{cmdOffA, cmdOffB}) {
		t.Errorf("commands = %v", got)
	}
	if got := warningCodes(ws); !reflect.DeepEqual(got, []string{doctor.NET006}) {
		t.Errorf("warnings = %v", got)
	}
	if len(r.cli.Changes()) != 0 || !r.has(`"18080"`) {
		t.Errorf("manage false changed something: %v %s", r.cli.Changes(), r.serveText())
	}
	r.noForbidden()
}

func TestOffThenOnReplacesTheFindings(t *testing.T) {
	r := newTSRig(t, "B", true, "a_and_b")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	if got := resultCodes(r.mgr.ServeResults()); !reflect.DeepEqual(got, []string{doctor.NET005}) {
		t.Fatalf("codes = %v", got)
	}
	if _, err := r.off(); err != nil {
		t.Fatal(err)
	}
	if got := r.mgr.ServeResults(); len(got) != 0 {
		t.Errorf("after a clean Off the online findings must be gone, have %v", got)
	}
}

func TestReconcile(t *testing.T) {
	t.Run("not online: nothing runs", func(t *testing.T) {
		r := newTSRig(t, "B", true, "none")
		r.online.Store(false)
		ws, err := r.mgr.Reconcile(context.Background())
		if err != nil || ws != nil || len(r.cli.Calls()) != 0 {
			t.Fatalf("ws %v err %v calls %v", ws, err, r.cli.Calls())
		}
	})
	t.Run("online: applies", func(t *testing.T) {
		r := newTSRig(t, "B", true, "none")
		if _, err := r.mgr.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := r.cli.Changes(); !reflect.DeepEqual(got, []string{applyB}) {
			t.Errorf("changes = %v", got)
		}
	})
	t.Run("manage false: reports only", func(t *testing.T) {
		r := newTSRig(t, "B", false, "none")
		if _, err := r.mgr.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(r.cli.Changes()) != 0 {
			t.Errorf("changes = %v", r.cli.Changes())
		}
	})
	t.Run("a pass that waited for Off does not re-apply after it", func(t *testing.T) {
		r := newTSRig(t, "B", true, "b_only")
		if _, err := r.on(); err != nil {
			t.Fatal(err)
		}
		// The engine turns Online false at the first step of the transition, then runs Off.
		r.online.Store(false)
		if _, err := r.off(); err != nil {
			t.Fatal(err)
		}
		if _, err := r.mgr.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r.has("127.0.0.1:8080") {
			t.Errorf("reconcile re-published the node after Off: %s", r.serveText())
		}
	})
}

func TestReconcilerLoop(t *testing.T) {
	r := newTSRig(t, "B", true, "b_only")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if r.mgr.Name() != "tailscale" {
		t.Errorf("name = %q", r.mgr.Name())
	}
	if err := r.mgr.Start(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	waitFor(t, "the loop's ticker", func() bool { return r.clk.Tickers() == 1 })
	if len(r.cli.Calls()) != 0 {
		t.Fatal("the loop must not run before the first period")
	}
	// Drift: the listener disappears; the next period restores it.
	if err := r.cli.LoadServe([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	r.seedApplied("B")
	r.clk.Advance(5 * time.Minute)
	waitFor(t, "the drift pass", func() bool { return len(r.cli.Changes()) == 1 })
	waitFor(t, "NET-007", func() bool { return find(r.mgr.ServeResults(), doctor.NET007) != nil })

	// Not online: the next period does nothing.
	r.online.Store(false)
	n := len(r.cli.Calls())
	seen := r.onlineCalls.Load()
	r.clk.Advance(5 * time.Minute)
	waitFor(t, "the loop to consume the tick", func() bool { return r.onlineCalls.Load() > seen })
	if got := len(r.cli.Calls()); got != n {
		t.Errorf("a pass ran while the node is not online (%d -> %d calls)", n, got)
	}

	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer scancel()
	if err := r.mgr.Stop(sctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ticker stopped", func() bool { return r.clk.Tickers() == 0 })
	if err := r.mgr.Stop(sctx); err != nil { // idempotent
		t.Fatal(err)
	}
}

func TestReconcilerRetriesSoonWhileTailscaleIsNotReady(t *testing.T) {
	// At boot the agent can start before tailscaled is Running: the first pass finds LAI-NET-003.
	r := newTSRig(t, "B", true, "none")
	r.cli.SetBackend("Starting")
	ws, err := r.on()
	if err != nil || !reflect.DeepEqual(warningCodes(ws), []string{doctor.NET003}) {
		t.Fatalf("ws %v err %v", ws, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer scancel()
		_ = r.mgr.Stop(sctx)
	}()
	waitFor(t, "the loop's ticker", func() bool { return r.clk.Tickers() == 1 })

	// Still starting: the next retry tick passes again and keeps the finding.
	seen := r.onlineCalls.Load()
	r.clk.Advance(modes.DefaultRetryEvery)
	waitFor(t, "a retry pass", func() bool { return r.onlineCalls.Load() > seen })
	if find(r.mgr.ServeResults(), doctor.NET003) == nil {
		t.Fatalf("results = %v", resultCodes(r.mgr.ServeResults()))
	}

	// tailscaled is up: the following retry tick clears LAI-NET-003 and publishes, long before the
	// five-minute period.
	r.cli.SetBackend("Running")
	r.clk.Advance(modes.DefaultRetryEvery)
	waitFor(t, "NET-003 cleared", func() bool { return find(r.mgr.ServeResults(), doctor.NET003) == nil })
	waitFor(t, "the pattern applied", func() bool { return r.has("127.0.0.1:8080") })

	// Ready: retry ticks no longer run passes; only the period does.
	seen = r.onlineCalls.Load()
	r.clk.Advance(modes.DefaultRetryEvery)
	r.clk.Advance(modes.DefaultRetryEvery)
	time.Sleep(50 * time.Millisecond)
	if got := r.onlineCalls.Load(); got != seen {
		t.Errorf("passes ran on retry ticks while tailscale is ready (%d -> %d)", seen, got)
	}
}

func TestStatusAndTailnetURL(t *testing.T) {
	t.Run("pattern B", func(t *testing.T) {
		r := newTSRig(t, "B", true, "none")
		if _, err := r.on(); err != nil {
			t.Fatal(err)
		}
		st := r.mgr.Status()
		if !st.Manage || st.Pattern != "B" || !st.Applied || st.BackendState != "Running" {
			t.Errorf("status = %+v", st)
		}
		if got := r.mgr.TailnetURL(context.Background()); got != "https://"+fakeHost+"/v1" {
			t.Errorf("url = %q", got)
		}
	})
	t.Run("pattern A", func(t *testing.T) {
		r := newTSRig(t, "A", true, "none")
		if got := r.mgr.TailnetURL(context.Background()); got != "http://"+fakeHost+":18080/v1" {
			t.Errorf("url = %q (the name is read on demand)", got)
		}
	})
	t.Run("pattern none", func(t *testing.T) {
		r := newTSRig(t, "none", true, "none")
		if got := r.mgr.TailnetURL(context.Background()); got != "" || len(r.cli.Calls()) != 0 {
			t.Errorf("url = %q calls %v", got, r.cli.Calls())
		}
		if st := r.mgr.Status(); st.Applied || st.BackendState != "" {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("tailscale unavailable", func(t *testing.T) {
		r := newTSRig(t, "B", true, "none")
		r.cli.DaemonUp = false
		if got := r.mgr.TailnetURL(context.Background()); got != "" {
			t.Errorf("url = %q", got)
		}
		if _, err := r.on(); err != nil {
			t.Fatal(err)
		}
		if st := r.mgr.Status(); st.BackendState != "" || st.Applied {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("not applied", func(t *testing.T) {
		r := newTSRig(t, "B", true, "foreign_443")
		if _, err := r.on(); err != nil {
			t.Fatal(err)
		}
		if st := r.mgr.Status(); st.Applied || st.BackendState != "Running" {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestTailnetUp(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*tsRig)
		want  bool
	}{
		{"running with a peer", func(*tsRig) {}, true},
		{"running, no peer online", func(r *tsRig) { r.cli.PeersOnline = 0 }, false},
		{"logged out", func(r *tsRig) { r.cli.Backend = "NeedsLogin" }, false},
		{"daemon down", func(r *tsRig) { r.cli.DaemonUp = false }, false},
		{"not installed", func(r *tsRig) { r.cli.Installed = false }, false},
		{"timeout", func(r *tsRig) { r.cli.Script(statusJS, fake.Step{Err: execx.ErrTimeout}) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, "B", true, "")
			tc.setup(r)
			if got := r.mgr.TailnetUp(context.Background()); got != tc.want {
				t.Errorf("TailnetUp = %v, want %v", got, tc.want)
			}
			if len(r.cli.Changes()) != 0 {
				t.Errorf("a read changed something: %v", r.cli.Changes())
			}
		})
	}
}

func TestCheckServeReachable(t *testing.T) {
	timeoutErr := &url.Error{Op: "Get", Err: context.DeadlineExceeded}
	refused := &url.Error{Op: "Get", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	tests := []struct {
		name    string
		pattern string
		reply   httpReply
		want    []string
		url     string
	}{
		{"B answers 200", "B", httpReply{status: 200}, nil, "GET https://" + fakeHost + "/health"},
		{"B answers 401: reachable", "B", httpReply{status: 401}, nil, "GET https://" + fakeHost + "/health"},
		{"B times out while the certificate is issued", "B", httpReply{err: timeoutErr}, []string{doctor.NET008}, "GET https://" + fakeHost + "/health"},
		{"B refuses", "B", httpReply{err: refused}, []string{doctor.NET004}, "GET https://" + fakeHost + "/health"},
		{"A answers", "A", httpReply{status: 200}, nil, "GET http://" + fakeHost + ":18080/health"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTSRig(t, tc.pattern, true, "")
			r.http.on("GET", strings.TrimPrefix(tc.url, "GET "), tc.reply)
			got := resultCodes(r.mgr.CheckServeReachable(context.Background()))
			if !reflect.DeepEqual(got, orEmpty(tc.want)) {
				t.Errorf("codes = %v, want %v", got, tc.want)
			}
			if c := r.http.Calls(); !reflect.DeepEqual(c, []string{tc.url}) {
				t.Errorf("requests = %v", c)
			}
		})
	}
	t.Run("pattern none and an unreadable name do nothing", func(t *testing.T) {
		r := newTSRig(t, "none", true, "")
		if got := r.mgr.CheckServeReachable(context.Background()); got != nil || len(r.http.Calls()) != 0 {
			t.Errorf("got %v, requests %v", got, r.http.Calls())
		}
		r2 := newTSRig(t, "B", true, "")
		r2.cli.DaemonUp = false
		if got := r2.mgr.CheckServeReachable(context.Background()); got != nil || len(r2.http.Calls()) != 0 {
			t.Errorf("got %v, requests %v", got, r2.http.Calls())
		}
	})
}

func TestDoctorNetChecksOverTheManager(t *testing.T) {
	r := newTSRig(t, "B", true, "a_and_b")
	if _, err := r.on(); err != nil {
		t.Fatal(err)
	}
	reg := doctor.NewRegistry()
	if err := doctor.RegisterNetChecks(reg, doctor.NetSources{Serve: r.mgr, Reachable: r.mgr}); err != nil {
		t.Fatal(err)
	}
	rep, err := reg.Run(context.Background(), doctor.RunOptions{Areas: []string{"NET"}, FastOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var found *doctor.Result
	for i := range rep.Results {
		if rep.Results[i].Code == doctor.NET005 {
			found = &rep.Results[i]
		}
	}
	if found == nil || found.Check != doctor.CheckNetServeState || found.Details["command"] != cmdOffA {
		t.Fatalf("report = %+v", rep)
	}
	if found.Title == "" || found.Fix == "" {
		t.Errorf("the runner must fill the texts: %+v", found)
	}
}

func TestOnOffThroughTheEngine(t *testing.T) {
	var mgr *modes.TailscaleManager
	var cli *fake.TailscaleCLI
	var eng atomic.Pointer[modes.Engine]
	r := newRig(t, withMode(modes.Offline), withDeps(func(d *modes.Deps) {
		cli = fake.NewTailscaleCLI()
		if err := cli.LoadServe(loadFixture(t, "a_and_b")); err != nil {
			t.Fatal(err)
		}
		var err error
		mgr, err = modes.NewTailscaleManager(modes.TailscaleOptions{
			Run: cli, Lookup: cli.Lookup, State: d.State,
			Settings: func() modes.TailscaleSettings {
				return modes.TailscaleSettings{Manage: true, Pattern: "B", LlamaPort: 8080}
			},
			Online: func() bool { e := eng.Load(); return e != nil && e.Online() },
		})
		if err != nil {
			t.Fatal(err)
		}
		d.Tailscale = mgr
	}))
	eng.Store(r.eng)

	if res := r.mustRequest(modes.Offline, true); !res.Already {
		t.Fatalf("already offline expected: %+v", res)
	}
	// Resume applies the steady-state policy of offline: tailscale_off.
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.eng.Resume(rctx); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if got := cli.Changes(); !reflect.DeepEqual(got, []string{offA, offB}) {
		t.Fatalf("offline policy changes = %v", got)
	}

	res := r.mustRequest(modes.Online, true)
	if !res.Done || len(res.Warnings) != 0 {
		t.Fatalf("online: %+v", res)
	}
	if got := cli.Changes(); !reflect.DeepEqual(got, []string{offA, offB, applyB}) {
		t.Errorf("changes = %v", got)
	}
	if st := r.st.Snapshot().Tailscale; st.AppliedPattern != "B" || st.AppliedAt == nil {
		t.Errorf("state.tailscale = %+v", st)
	}

	// A failing removal becomes a warning of the transition and the engine retries every 60 s.
	cli.Script(offB, fake.Step{NoEffect: true})
	res = r.mustRequest(modes.Offline, true)
	if !res.Done || !hasWarning(res.Warnings, doctor.NET006) {
		t.Fatalf("offline with a stuck listener: %+v", res)
	}
	// The engine's retry loop creates its ticker on its own goroutine: wait for it before ticking.
	waitFor(t, "the retry timer", func() bool { return r.clk.Tickers() == 1 })
	r.clk.Advance(modes.DefaultTailscaleRetry)
	waitFor(t, "the retry to remove it", func() bool { return !strings.Contains(string(cli.ServeJSON()), "127.0.0.1:8080") })
	cli.Unexpected()
	if u := cli.Unexpected(); len(u) != 0 {
		t.Errorf("unexpected commands %v", u)
	}
}

func hasWarning(ws []modes.Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}
