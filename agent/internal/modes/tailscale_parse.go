package modes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// The ports of the two serve patterns (agent.md section 4.7.1). The target is always
// 127.0.0.1:<ports.llama>; only the published port differs.
const (
	// PatternAPort is the tailnet TCP port of pattern A.
	PatternAPort = 18080
	// PatternBPort is the tailnet HTTPS port of pattern B.
	PatternBPort = 443
)

// The serve patterns of node.yaml tailscale.serve_pattern.
const (
	PatternA    = "A"
	PatternB    = "B"
	PatternNone = "none"
)

// tsStatus is what the agent reads from `tailscale status --json`: only the fields it uses, parsed
// tolerantly (unknown fields ignored, missing ones zero).
type tsStatus struct {
	BackendState string
	// DNSName is Self.DNSName without its trailing dot; "" when unknown.
	DNSName string
	// PeersOnline counts the peers reported Online.
	PeersOnline int
}

func parseTSStatus(b []byte) (tsStatus, error) {
	var raw struct {
		BackendState string `json:"BackendState"`
		Self         *struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
		Peer map[string]*struct {
			Online bool `json:"Online"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &raw); err != nil {
		return tsStatus{}, fmt.Errorf("tailscale status is not JSON: %w", err)
	}
	st := tsStatus{BackendState: raw.BackendState}
	if raw.Self != nil {
		st.DNSName = strings.TrimSuffix(strings.TrimSpace(raw.Self.DNSName), ".")
	}
	for _, p := range raw.Peer {
		if p != nil && p.Online {
			st.PeersOnline++
		}
	}
	return st, nil
}

// serveTCP is one entry of the TCP map of `tailscale serve status --json`.
type serveTCP struct {
	HTTPS        bool   `json:"HTTPS"`
	HTTP         bool   `json:"HTTP"`
	TCPForward   string `json:"TCPForward"`
	TerminateTLS string `json:"TerminateTLS"`
}

// serveHandler is one handler of a Web entry. Only Proxy matters to the agent; the other kinds
// (Path, Text, Redirect) are kept as a marker so a handler that is not a proxy is never "ours".
type serveHandler struct {
	Proxy    string `json:"Proxy"`
	Path     string `json:"Path"`
	Text     string `json:"Text"`
	Redirect string `json:"Redirect"`
}

type serveWeb struct {
	Handlers map[string]*serveHandler `json:"Handlers"`
}

// serveConfig is `tailscale serve status --json`, reduced to what the agent classifies. Web keys are
// "<DNSName-without-dot>:<port>".
type serveConfig struct {
	TCP         map[string]*serveTCP `json:"TCP"`
	Web         map[string]*serveWeb `json:"Web"`
	AllowFunnel map[string]bool      `json:"AllowFunnel"`
}

// parseServeConfig parses the output of `tailscale serve status --json`. Empty output and JSON null
// mean "no serve configuration" (an unconfigured Tailscale prints `{}`, an old one nothing); anything
// that is not a JSON object is an error, never silently "empty": the caller must not conclude that
// nothing is published from output it could not read.
func parseServeConfig(b []byte) (serveConfig, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return serveConfig{}, nil
	}
	if b[0] != '{' {
		return serveConfig{}, fmt.Errorf("tailscale serve status is not a JSON object")
	}
	var c serveConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return serveConfig{}, fmt.Errorf("tailscale serve status is not valid JSON: %w", err)
	}
	return c, nil
}

// listener is how one pattern's port looks in a serve config.
type listener int

const (
	// listenerFree: nothing is served on the pattern's port.
	listenerFree listener = iota
	// listenerOurs: exactly our listener (agent.md section 4.7.1); the only kind the agent removes.
	listenerOurs
	// listenerMixed: pattern B's "/" handler is ours but the host:443 entry carries other handlers.
	// `tailscale serve --https=443 off` would remove those too, so the agent never touches it.
	listenerMixed
	// listenerForeign: something else is served on the port.
	listenerForeign
)

func (l listener) String() string {
	switch l {
	case listenerFree:
		return "free"
	case listenerOurs:
		return "ours"
	case listenerMixed:
		return "mixed"
	}
	return "foreign"
}

// target is the llama endpoint our listeners forward to: 127.0.0.1:<port>.
type target struct{ port int }

func (t target) hostPort() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(t.port)) }
func (t target) proxyURL() string { return "http://" + t.hostPort() }

// isOurs reports whether a TCPForward value ("127.0.0.1:8080") points to the target.
func (t target) isForward(v string) bool {
	host, port, err := net.SplitHostPort(strings.TrimSpace(v))
	return err == nil && isLoopbackName(host) && port == strconv.Itoa(t.port)
}

// isProxy reports whether a handler Proxy value ("http://127.0.0.1:8080", with or without a trailing
// slash) points to the target.
func (t target) isProxy(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if p := strings.Trim(u.EscapedPath(), "/"); p != "" {
		return false // a sub-path is a different upstream
	}
	return isLoopbackName(u.Hostname()) && u.Port() == strconv.Itoa(t.port)
}

func isLoopbackName(h string) bool {
	h = strings.ToLower(h)
	return h == "127.0.0.1" || h == "localhost"
}

// patternA classifies the TCP entry of the pattern A port.
func (c serveConfig) patternA(t target) listener {
	e := c.TCP[strconv.Itoa(PatternAPort)]
	if e == nil {
		return listenerFree
	}
	if t.isForward(e.TCPForward) && e.TerminateTLS == "" && !e.HTTPS && !e.HTTP {
		return listenerOurs
	}
	return listenerForeign
}

// webEntries returns the Web entries served on port, keyed by their "<host>:<port>" key. When host is
// known only that host's entry is returned.
func (c serveConfig) webEntries(host string, port int) map[string]*serveWeb {
	out := map[string]*serveWeb{}
	suffix := ":" + strconv.Itoa(port)
	for k, w := range c.Web {
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		h := strings.TrimSuffix(k, suffix)
		if host != "" && !strings.EqualFold(h, host) {
			continue
		}
		if w == nil {
			w = &serveWeb{}
		}
		out[k] = w
	}
	return out
}

// patternB classifies the HTTPS 443 listener of host.
//
//	TCP["443"].HTTPS == true and Web["<host>:443"].Handlers["/"].Proxy is the target, as the only handler
//
// is ours. The same with other handlers beside it is mixed; a TCP entry on 443 that is not a plain
// HTTPS terminator, or any other handler on "/", is foreign.
func (c serveConfig) patternB(host string, t target) listener {
	tcp := c.TCP[strconv.Itoa(PatternBPort)]
	web := c.webEntries(host, PatternBPort)
	if tcp == nil && len(web) == 0 {
		return listenerFree
	}
	if tcp != nil && (!tcp.HTTPS || tcp.TCPForward != "" || tcp.TerminateTLS != "") {
		return listenerForeign
	}
	ours, others := 0, 0
	for _, w := range web {
		for path, h := range w.Handlers {
			if h == nil {
				others++
				continue
			}
			if path == "/" && t.isProxy(h.Proxy) && h.Path == "" && h.Text == "" && h.Redirect == "" {
				ours++
			} else {
				others++
			}
		}
	}
	switch {
	case ours == 1 && others == 0 && tcp != nil:
		return listenerOurs
	case ours >= 1 && tcp != nil:
		return listenerMixed
	case tcp == nil && ours == 0 && others == 0:
		return listenerFree // an empty Web entry with no TCP: nothing is served
	}
	return listenerForeign
}

// extra is a listener, not ours, that forwards to the llama port.
type extra struct {
	// Kind is "tcp" or "https"; Port the tailnet port.
	Kind string
	Port string
	// Mixed: it is the "/" handler of an HTTPS entry that carries other handlers.
	Mixed bool
}

// command is the exact command that removes the listener, "" when removing it would also remove
// someone else's handlers.
func (x extra) command() string {
	switch {
	case x.Mixed:
		return ""
	case x.Kind == "tcp":
		return "sudo tailscale serve --tcp=" + x.Port + " off"
	}
	return "sudo tailscale serve --https=" + x.Port + " off"
}

// extras lists every listener that forwards to the llama port and is not the pattern listener the
// caller recognised as ours (the ports in ownedTCP / ownedHTTPS). Results are sorted.
func (c serveConfig) extras(t target, ownedTCP, ownedHTTPS map[string]bool) []extra {
	var out []extra
	for port, e := range c.TCP {
		if e != nil && t.isForward(e.TCPForward) && !ownedTCP[port] {
			out = append(out, extra{Kind: "tcp", Port: port})
		}
	}
	httpsPorts := map[string]bool{} // port -> the entry carries other handlers too
	for key, w := range c.Web {
		if w == nil {
			continue
		}
		_, port, err := net.SplitHostPort(key)
		if err != nil {
			continue
		}
		for _, h := range w.Handlers {
			if h != nil && t.isProxy(h.Proxy) && !ownedHTTPS[port] {
				httpsPorts[port] = httpsPorts[port] || len(w.Handlers) > 1
			}
		}
	}
	for port, mixed := range httpsPorts {
		out = append(out, extra{Kind: "https", Port: port, Mixed: mixed})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// funnelOn lists the funnel flags that are set for our two pattern ports ("443" for host:443, "18080"
// for any host), by port.
func (c serveConfig) funnelOn(host string) []int {
	var ports []int
	seen := map[int]bool{}
	for key, on := range c.AllowFunnel {
		if !on {
			continue
		}
		h, p, err := net.SplitHostPort(key)
		if err != nil {
			continue
		}
		n, _ := strconv.Atoi(p)
		switch {
		case n == PatternAPort, n == PatternBPort && (host == "" || strings.EqualFold(h, host)):
			if !seen[n] {
				seen[n] = true
				ports = append(ports, n)
			}
		}
	}
	sort.Ints(ports)
	return ports
}
