package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// TailscaleCLI is an execx.Runner that behaves like the tailscale command line against an in-memory
// serve configuration: `status --json`, `serve status --json`, the serve and funnel commands the
// agent uses, nothing else. It records every argv, answers from fixture JSON, can be scripted to fail,
// time out or "succeed" without changing anything (a partial apply), and refuses (and remembers)
// every command outside the set the agent is allowed to run. It never starts a process and never
// touches a tailnet.
type TailscaleCLI struct {
	mu sync.Mutex

	// Installed is whether Lookup finds the binary. DaemonUp is whether tailscaled answers. Backend is
	// BackendState ("Running", "NeedsLogin", "Stopped"...). DNSName is Self.DNSName, with its dot.
	// PeersOnline is how many peers report Online.
	Installed   bool
	DaemonUp    bool
	Backend     string
	DNSName     string
	PeersOnline int

	serve  map[string]any
	calls  []execx.Cmd
	script map[string][]Step
	always map[string]Step
}

// Step scripts the answer of one command line.
type Step struct {
	// Res is returned as is (zero: exit 0, no output) when Err is nil and Sim is false.
	Res execx.Result
	// Err is returned as the run error (execx.ErrTimeout, a start error...).
	Err error
	// Simulate runs the real simulation after all (so a Step can be only "NoEffect").
	Simulate bool
	// NoEffect: with Simulate false the command exits 0 and changes nothing (a partial apply).
	NoEffect bool
}

// NewTailscaleCLI returns a CLI of a node that is installed, running, logged in, on a tailnet with
// one online peer, and has no serve configuration.
func NewTailscaleCLI() *TailscaleCLI {
	return &TailscaleCLI{Installed: true, DaemonUp: true, Backend: "Running", DNSName: "hp-linux.<TAILNET>.ts.net.",
		PeersOnline: 1, serve: map[string]any{}, script: map[string][]Step{}, always: map[string]Step{}}
}

var _ execx.Runner = (*TailscaleCLI)(nil)

// BinPath is what Lookup returns.
const BinPath = "/usr/bin/tailscale"

// Lookup is the `Lookup` option of modes.TailscaleOptions: BinPath, or execx.ErrNotFound when the node
// does not have tailscale.
func (c *TailscaleCLI) Lookup(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name != "tailscale" || !c.Installed {
		return "", fmt.Errorf("%w: %q", execx.ErrNotFound, name)
	}
	return BinPath, nil
}

// LoadServe replaces the serve configuration with fixture JSON (the output of
// `tailscale serve status --json`).
func (c *TailscaleCLI) LoadServe(b []byte) error {
	m := map[string]any{}
	if t := strings.TrimSpace(string(b)); t != "" {
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.serve = m
	c.mu.Unlock()
	return nil
}

// ServeJSON is the current serve configuration as `tailscale serve status --json` prints it.
func (c *TailscaleCLI) ServeJSON() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serveJSONLocked()
}

func (c *TailscaleCLI) serveJSONLocked() []byte {
	b, _ := json.Marshal(c.serve)
	return b
}

// Script makes the next calls of exactly this command line (arguments joined by single spaces, without
// the binary) answer with steps, in order; once they are used up the simulation answers again.
func (c *TailscaleCLI) Script(args string, steps ...Step) {
	c.mu.Lock()
	c.script[args] = append(c.script[args], steps...)
	c.mu.Unlock()
}

// ScriptAlways makes every call of this command line answer with step, after the Script steps.
func (c *TailscaleCLI) ScriptAlways(args string, step Step) {
	c.mu.Lock()
	c.always[args] = step
	c.mu.Unlock()
}

// Calls returns every argv run so far, joined by single spaces (without the binary), oldest first.
func (c *TailscaleCLI) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	for i, cmd := range c.calls {
		out[i] = strings.Join(cmd.Args, " ")
	}
	return out
}

// Cmds returns the recorded commands themselves (path, timeout, env).
func (c *TailscaleCLI) Cmds() []execx.Cmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]execx.Cmd(nil), c.calls...)
}

// Changes returns the calls that change configuration (serve ... and funnel ..., not the status
// reads), oldest first.
func (c *TailscaleCLI) Changes() []string {
	var out []string
	for _, a := range c.Calls() {
		if (strings.HasPrefix(a, "serve ") && !strings.HasPrefix(a, "serve status")) || strings.HasPrefix(a, "funnel ") {
			out = append(out, a)
		}
	}
	return out
}

// Unexpected returns the calls outside the supported set (for example `serve reset`, `down`, `logout`):
// a test of the agent's ownership rules asserts it is empty.
func (c *TailscaleCLI) Unexpected() []string {
	var out []string
	for _, a := range c.Calls() {
		if !supported(strings.Fields(a)) {
			out = append(out, a)
		}
	}
	return out
}

// Reset forgets the recorded calls and scripts, keeping the node and its configuration.
func (c *TailscaleCLI) Reset() {
	c.mu.Lock()
	c.calls = nil
	c.script = map[string][]Step{}
	c.always = map[string]Step{}
	c.mu.Unlock()
}

// Run implements execx.Runner.
func (c *TailscaleCLI) Run(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cmd.Path != BinPath {
		return execx.Result{}, fmt.Errorf("fake: unexpected binary %q", cmd.Path)
	}
	c.calls = append(c.calls, cmd)
	key := strings.Join(cmd.Args, " ")
	if q := c.script[key]; len(q) > 0 {
		st := q[0]
		c.script[key] = q[1:]
		return c.answer(cmd.Args, st)
	}
	if st, ok := c.always[key]; ok {
		return c.answer(cmd.Args, st)
	}
	return c.simulate(cmd.Args)
}

func (c *TailscaleCLI) answer(args []string, st Step) (execx.Result, error) {
	if st.Err != nil {
		return st.Res, st.Err
	}
	if st.Simulate {
		return c.simulate(args)
	}
	return st.Res, nil
}

func supported(a []string) bool {
	switch {
	case len(a) == 2 && a[0] == "status" && a[1] == "--json":
		return true
	case len(a) == 3 && a[0] == "serve" && a[1] == "status" && a[2] == "--json":
		return true
	case len(a) == 4 && a[0] == "serve" && a[1] == "--bg" && (strings.HasPrefix(a[2], "--tcp=") || strings.HasPrefix(a[2], "--https=")):
		return true
	case len(a) == 3 && a[0] == "serve" && a[2] == "off" && (strings.HasPrefix(a[1], "--tcp=") || strings.HasPrefix(a[1], "--https=")):
		return true
	case len(a) == 3 && a[0] == "funnel" && a[2] == "off" && (strings.HasPrefix(a[1], "--tcp=") || strings.HasPrefix(a[1], "--https=")):
		return true
	}
	return false
}

var downText = []byte("failed to connect to local tailscaled; it doesn't appear to be running (sudo systemctl start tailscaled ?)\n")

func fail(msg string) (execx.Result, error) {
	return execx.Result{ExitCode: 1, Stderr: []byte(msg + "\n")}, nil
}

func (c *TailscaleCLI) simulate(a []string) (execx.Result, error) {
	if !supported(a) {
		return fail("fake: unsupported tailscale command: " + strings.Join(a, " "))
	}
	if !c.DaemonUp {
		return execx.Result{ExitCode: 1, Stderr: downText}, nil
	}
	switch {
	case a[0] == "status":
		return execx.Result{Stdout: c.statusJSON()}, nil
	case a[1] == "status":
		return execx.Result{Stdout: c.serveJSONLocked()}, nil
	case a[0] == "serve" && a[1] == "--bg":
		return c.apply(a[2], a[3])
	case a[0] == "serve":
		c.remove(a[1])
	default: // funnel off
		c.funnelOff(a[1])
	}
	return execx.Result{}, nil
}

func (c *TailscaleCLI) statusJSON() []byte {
	peers := map[string]any{}
	for i := 0; i < c.PeersOnline; i++ {
		peers[fmt.Sprintf("peer-%d", i)] = map[string]any{"Online": true}
	}
	peers["peer-offline"] = map[string]any{"Online": false}
	b, _ := json.Marshal(map[string]any{
		"Version": "1.0.0-fake", "BackendState": c.Backend,
		"Self": map[string]any{"DNSName": c.DNSName, "Online": c.Backend == "Running"}, "Peer": peers,
	})
	return b
}

func sub(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	v := map[string]any{}
	m[key] = v
	return v
}

func (c *TailscaleCLI) host() string { return strings.TrimSuffix(c.DNSName, ".") }

func (c *TailscaleCLI) apply(flag, dest string) (execx.Result, error) {
	switch {
	case strings.HasPrefix(flag, "--tcp="):
		port := strings.TrimPrefix(flag, "--tcp=")
		sub(c.serve, "TCP")[port] = map[string]any{"TCPForward": strings.TrimPrefix(dest, "tcp://")}
	case strings.HasPrefix(flag, "--https="):
		port := strings.TrimPrefix(flag, "--https=")
		sub(c.serve, "TCP")[port] = map[string]any{"HTTPS": true}
		web := sub(sub(c.serve, "Web"), c.host()+":"+port)
		sub(web, "Handlers")["/"] = map[string]any{"Proxy": dest}
	}
	return execx.Result{}, nil
}

func (c *TailscaleCLI) remove(flag string) {
	switch {
	case strings.HasPrefix(flag, "--tcp="):
		delete(sub(c.serve, "TCP"), strings.TrimPrefix(flag, "--tcp="))
	case strings.HasPrefix(flag, "--https="):
		port := strings.TrimPrefix(flag, "--https=")
		delete(sub(c.serve, "TCP"), port)
		delete(sub(c.serve, "Web"), c.host()+":"+port)
		delete(sub(c.serve, "AllowFunnel"), c.host()+":"+port)
	}
	for _, k := range []string{"TCP", "Web", "AllowFunnel"} {
		if len(sub(c.serve, k)) == 0 {
			delete(c.serve, k)
		}
	}
}

func (c *TailscaleCLI) funnelOff(flag string) {
	port := strings.TrimPrefix(strings.TrimPrefix(flag, "--tcp="), "--https=")
	af := sub(c.serve, "AllowFunnel")
	for k := range af {
		if strings.HasSuffix(k, ":"+port) {
			delete(af, k)
		}
	}
	if len(af) == 0 {
		delete(c.serve, "AllowFunnel")
	}
}

// ErrScripted is a convenient error for a Step that fails to start.
var ErrScripted = errors.New("fake: scripted failure")
