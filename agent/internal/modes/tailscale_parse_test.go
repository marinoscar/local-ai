package modes

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const fixtureHost = "hp-linux.<TAILNET>.ts.net"

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tailscale", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClassifyFixtures(t *testing.T) {
	llama := target{8080}
	tests := []struct {
		fixture   string
		a, b      listener
		extras    []string // "<kind>:<port>[:mixed]"
		funnelOn  []int
		wantCmdOf map[string]string
	}{
		{fixture: "none", a: listenerFree, b: listenerFree},
		{fixture: "a_only", a: listenerOurs, b: listenerFree, extras: []string{"tcp:18080"}},
		{fixture: "b_only", a: listenerFree, b: listenerOurs, extras: []string{"https:443"}},
		{fixture: "a_and_b", a: listenerOurs, b: listenerOurs, extras: []string{"https:443", "tcp:18080"}},
		{fixture: "foreign_443", a: listenerFree, b: listenerForeign},
		{fixture: "foreign_18080", a: listenerForeign, b: listenerFree},
		{fixture: "funnel_on", a: listenerFree, b: listenerOurs, extras: []string{"https:443"}, funnelOn: []int{443}},
		{fixture: "mixed_443", a: listenerFree, b: listenerMixed, extras: []string{"https:443:mixed"}},
		{fixture: "extras", a: listenerFree, b: listenerFree, extras: []string{"https:8443", "tcp:9999"}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			cfg, err := parseServeConfig(fixture(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.patternA(llama); got != tc.a {
				t.Errorf("pattern A = %s, want %s", got, tc.a)
			}
			if got := cfg.patternB(fixtureHost, llama); got != tc.b {
				t.Errorf("pattern B = %s, want %s", got, tc.b)
			}
			// With nothing owned, every listener to the llama port is listed.
			var got []string
			for _, x := range cfg.extras(llama, nil, nil) {
				s := x.Kind + ":" + x.Port
				if x.Mixed {
					s += ":mixed"
				}
				got = append(got, s)
			}
			if !reflect.DeepEqual(got, tc.extras) {
				t.Errorf("extras = %v, want %v", got, tc.extras)
			}
			if got := cfg.funnelOn(fixtureHost); !reflect.DeepEqual(got, tc.funnelOn) {
				t.Errorf("funnel = %v, want %v", got, tc.funnelOn)
			}
		})
	}
}

func TestClassifyUsesTheConfiguredLlamaPort(t *testing.T) {
	cfg, err := parseServeConfig(fixture(t, "a_and_b"))
	if err != nil {
		t.Fatal(err)
	}
	// Our listeners forward to 8080; with ports.llama 9090 nothing of that config is ours.
	if a, b := cfg.patternA(target{9090}), cfg.patternB(fixtureHost, target{9090}); a != listenerForeign || b != listenerForeign {
		t.Errorf("A = %s, B = %s, want foreign for both", a, b)
	}
}

func TestPatternBVariants(t *testing.T) {
	llama := target{8080}
	mk := func(proxy string) serveConfig {
		return serveConfig{
			TCP: map[string]*serveTCP{"443": {HTTPS: true}},
			Web: map[string]*serveWeb{fixtureHost + ":443": {Handlers: map[string]*serveHandler{"/": {Proxy: proxy}}}},
		}
	}
	tests := []struct {
		name string
		cfg  serveConfig
		host string
		want listener
	}{
		{"exact", mk("http://127.0.0.1:8080"), fixtureHost, listenerOurs},
		{"trailing slash", mk("http://127.0.0.1:8080/"), fixtureHost, listenerOurs},
		{"localhost spelling", mk("http://localhost:8080"), fixtureHost, listenerOurs},
		{"host unknown matches any host", mk("http://127.0.0.1:8080"), "", listenerOurs},
		{"other host's entry is not ours to see", mk("http://127.0.0.1:8080"), "other.<TAILNET>.ts.net", listenerForeign},
		{"another port", mk("http://127.0.0.1:8081"), fixtureHost, listenerForeign},
		{"a sub-path upstream is another upstream", mk("http://127.0.0.1:8080/api"), fixtureHost, listenerForeign},
		{"a remote upstream", mk("http://192.0.2.10:8080"), fixtureHost, listenerForeign},
		{"text handler", serveConfig{
			TCP: map[string]*serveTCP{"443": {HTTPS: true}},
			Web: map[string]*serveWeb{fixtureHost + ":443": {Handlers: map[string]*serveHandler{"/": {Text: "hello"}}}},
		}, fixtureHost, listenerForeign},
		{"tcp forward on 443", serveConfig{TCP: map[string]*serveTCP{"443": {TCPForward: "127.0.0.1:8080"}}}, fixtureHost, listenerForeign},
		{"https terminated tcp forward", serveConfig{TCP: map[string]*serveTCP{"443": {HTTPS: true, TerminateTLS: fixtureHost}}}, fixtureHost, listenerForeign},
		// 443 is terminated for someone, possibly for a host other than ours: not ours to claim or remove.
		{"https terminator without our handler", serveConfig{TCP: map[string]*serveTCP{"443": {HTTPS: true}}, Web: map[string]*serveWeb{fixtureHost + ":443": nil}}, fixtureHost, listenerForeign},
		{"web entry without a tcp entry", serveConfig{Web: map[string]*serveWeb{fixtureHost + ":443": nil}}, fixtureHost, listenerFree},
		{"empty config", serveConfig{}, fixtureHost, listenerFree},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.patternB(tc.host, llama); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPatternATerminatedTCPIsForeign(t *testing.T) {
	cfg := serveConfig{TCP: map[string]*serveTCP{"18080": {TCPForward: "127.0.0.1:8080", TerminateTLS: fixtureHost}}}
	if got := cfg.patternA(target{8080}); got != listenerForeign {
		t.Errorf("got %s, want foreign: a TLS-terminating forward is not what the agent applies", got)
	}
}

func TestExtrasCommands(t *testing.T) {
	cfg, err := parseServeConfig(fixture(t, "extras"))
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	for _, x := range cfg.extras(target{8080}, nil, nil) {
		cmds = append(cmds, x.command())
	}
	want := []string{"sudo tailscale serve --https=8443 off", "sudo tailscale serve --tcp=9999 off"}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("commands = %v, want %v", cmds, want)
	}
	if (extra{Kind: "https", Port: "443", Mixed: true}).command() != "" {
		t.Error("a mixed listener has no safe remove command")
	}
}

func TestParseServeConfigTolerance(t *testing.T) {
	good := []struct{ name, in string }{
		{"empty output", ""},
		{"whitespace", " \n"},
		{"null", "null"},
		{"empty object", "{}"},
		{"unknown fields and shapes", `{"Foreground":{"x":1},"Services":{"svc:a":{}},"TCP":{"1":null},"Web":{"h:1":{"Handlers":{"/":null}}},"Future":[1,2]}`},
	}
	for _, tc := range good {
		if _, err := parseServeConfig([]byte(tc.in)); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	bad := []string{"[]", "tailscale: no serve config", `{"TCP":`, `{"TCP":{"443":"x"}}`, "42"}
	for _, in := range bad {
		if _, err := parseServeConfig([]byte(in)); err == nil {
			t.Errorf("%q must be an error, not an empty config", in)
		}
	}
}

func TestParseTSStatus(t *testing.T) {
	tests := []struct {
		fixture string
		want    tsStatus
	}{
		{"status_running", tsStatus{BackendState: "Running", DNSName: fixtureHost, PeersOnline: 1}},
		{"status_needs_login", tsStatus{BackendState: "NeedsLogin"}},
		{"status_no_peers", tsStatus{BackendState: "Running", DNSName: fixtureHost}},
	}
	for _, tc := range tests {
		got, err := parseTSStatus(fixture(t, tc.fixture))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.fixture, got, err, tc.want)
		}
	}
	if _, err := parseTSStatus([]byte("Logged out.")); err == nil {
		t.Error("non-JSON status must be an error")
	}
}
