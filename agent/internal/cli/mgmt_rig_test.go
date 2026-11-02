package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// The rig of the AICTL-06 command tests: a fake agent (socket and TCP), the real command tree
// (DefaultRegistry with the late registrations applied), and a run helper that returns exit code,
// stdout and stderr. Secrets are built at run time (the repository's redaction check and secret
// scanner refuse key-shaped literals, even fake ones).

// mgKey is a 64-hex stand-in for the llama key.
func mgKey() string { return strings.Repeat("c0de", 16) }

// mgCred is a stand-in for a node credential.
func mgCred() string { return "nod" + "_" + strings.Repeat("Z9", 20) }

func mgRegistry() *Registry {
	applyLate(DefaultRegistry)
	return DefaultRegistry
}

type mgRig struct {
	t  *testing.T
	h  *harness
	ev *fake.Events
}

func newMgRig(t *testing.T, o fake.Options) *mgRig {
	t.Helper()
	return &mgRig{t: t, h: newHarness(t, o)}
}

// events attaches the scripted event stream to the fake agent.
func (r *mgRig) events() *fake.Events {
	if r.ev == nil {
		r.ev = r.h.agent.Events(r.t, events.Options{})
	}
	return r.ev
}

func (r *mgRig) agent() *fake.Agent { return r.h.agent }

// mgRun describes one invocation.
type mgRun struct {
	args  []string
	term  Terminal
	tty   bool // stdout is a terminal
	euid  int
	env   map[string]string
	ctx   context.Context
	stdin string
}

func (r *mgRig) env(o mgRun) Env {
	vars := map[string]string{
		"LOCALAI_SOCKET": r.h.agent.SocketPath, "LOCALAI_URL": r.h.agent.URL, "LOCALAI_TOKEN_FILE": r.h.cfg.TokenFile,
		"LANG": "en_US.UTF-8",
	}
	for k, v := range o.env {
		vars[k] = v
	}
	return Env{
		Stdin:       strings.NewReader(o.stdin),
		Getenv:      func(k string) string { return vars[k] },
		Geteuid:     func() int { return o.euid },
		StdoutIsTTY: o.tty,
		Terminal:    o.term,
		Version:     "0.0.0-dev",
	}
}

func (r *mgRig) run(o mgRun) followOut {
	r.t.Helper()
	ch, _ := r.runAsync(o)
	return finish(r.t, ch)
}

// runAsync starts the command and returns its stderr for a test that watches progress.
func (r *mgRig) runAsync(o mgRun) (<-chan followOut, *lockedBuf) {
	var out, errb lockedBuf
	env := r.env(o)
	env.Stdout, env.Stderr = &out, &errb
	env.ClientConfig = func(c *Config) {
		c.Sleep = r.h.sleeps.Sleep
		c.DialUnix = r.h.cfg.DialUnix
		c.ReadFile = r.h.cfg.ReadFile
		c.Stderr = &errb
	}
	ctx := o.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ch := make(chan followOut, 1)
	go func() {
		code := Run(ctx, env, mgRegistry(), o.args)
		ch <- followOut{code, out.String(), errb.String()}
	}()
	return ch, &errb
}

// mgArgs is shorthand for a run with only arguments.
func mgArgs(a ...string) mgRun { return mgRun{args: a} }

// withTerm gives the run an interactive terminal whose person types lines.
func (o mgRun) withTerm(lines ...string) mgRun {
	o.term = fake.Typed(true, lines...)
	return o
}

func (o mgRun) setStdin(s string) mgRun {
	o.stdin = s
	return o
}

func (o mgRun) lang(l string) mgRun {
	if o.env == nil {
		o.env = map[string]string{}
	}
	o.env["LANG"] = l
	return o
}

func (o mgRun) setenv(k, v string) mgRun {
	e := map[string]string{}
	for kk, vv := range o.env {
		e[kk] = vv
	}
	e[k] = v
	o.env = e
	return o
}

// checkRun compares exit code, stdout and stderr with a golden (a pointer to "" or no golden skips).
func checkRun(t *testing.T, got followOut, code int, stdout, stderr string) {
	t.Helper()
	if got.code != code || got.out != stdout || got.errOut != stderr {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nwant:\n%s", got.code, code, got.out, stdout, got.errOut, stderr)
	}
}

// secretFree fails when any of the texts holds the secret.
func secretFree(t *testing.T, secret string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, secret) {
			t.Fatalf("a secret leaked into %q", s)
		}
	}
}

// requestLines lists the requests the fake saw as "METHOD /path", without the health probe and the
// event subscription.
func (r *mgRig) requestLines() []string {
	var out []string
	for _, q := range r.h.agent.Requests() {
		if q.Path == "/health" || strings.HasPrefix(q.Path, "/events") {
			continue
		}
		out = append(out, q.Method+" "+q.Path)
	}
	return out
}

// lastBody returns the body of the last request for method and path.
func (r *mgRig) lastBody(method, path string) string {
	reqs := r.h.agent.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method && reqs[i].Path == path {
			return reqs[i].Body
		}
	}
	r.t.Fatalf("no %s %s request in %v", method, path, r.requestLines())
	return ""
}
