package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// runResult is what a Run produced.
type runResult struct {
	code        int
	out, errOut string
}

// runOpts adjusts one run.
type runOpts struct {
	env   map[string]string
	euid  int
	tty   bool
	stdin string
	out   io.Writer // replaces stdout (a broken pipe)
	h     *harness  // client hooks from a harness (sleeper, token reads)
}

func runCLI(t *testing.T, reg *Registry, o runOpts, args ...string) runResult {
	t.Helper()
	var out, errb bytes.Buffer
	var stdout io.Writer = &out
	if o.out != nil {
		stdout = o.out
	}
	env := Env{
		Stdin:       strings.NewReader(o.stdin),
		Stdout:      stdout,
		Stderr:      &errb,
		Getenv:      func(k string) string { return o.env[k] },
		Geteuid:     func() int { return o.euid },
		StdoutIsTTY: o.tty,
		Version:     "0.0.0-dev",
	}
	if o.h != nil {
		env.ClientConfig = func(c *Config) {
			c.Sleep = o.h.sleeps.Sleep
			c.DialUnix = o.h.cfg.DialUnix
			c.ReadFile = o.h.cfg.ReadFile
			c.Stderr = &errb
		}
	}
	code := Run(context.Background(), env, reg, args)
	return runResult{code, out.String(), errb.String()}
}

// testRegistry has a few commands that cover the framework paths.
func testRegistry() *Registry {
	reg := NewRegistry()
	reg.MustAdd(&Command{
		Name: "ping", Summary: "Ask the agent for /models.", Timeout: 10 * time.Second,
		Run: func(c *Call) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if _, err := cl.Do(c.Ctx, http.MethodGet, "/models", nil, &raw); err != nil {
				return err
			}
			c.Stdout.Write(append([]byte(raw), '\n'))
			return nil
		},
	})
	reg.MustAdd(&Command{
		Name: "local", Summary: "A command that never touches the agent.", Marks: MarkLocal | MarkRoot,
		Run: func(c *Call) error { io.WriteString(c.Stdout, "local ok\n"); return nil },
	})
	reg.MustAdd(&Command{
		Name: "root", Summary: "Needs root.", Marks: MarkRoot,
		Run: func(c *Call) error {
			if err := RequireRoot(c); err != nil {
				return err
			}
			io.WriteString(c.Stdout, "root ok\n")
			return nil
		},
	})
	reg.MustAdd(&Command{
		Name: "load", Summary: "Load a model.", Usage: "<model> [--wait]", Timeout: 90 * time.Second,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("wait", false, "wait until it is loaded")
			fs.String("profile", "", "profile name")
		},
		Run: func(c *Call) error {
			wait := c.Flags.Lookup("wait").Value.String()
			profile := c.Flags.Lookup("profile").Value.String()
			io.WriteString(c.Stdout, strings.Join(append([]string{"load", wait, profile}, c.Args...), "|")+"\n")
			return nil
		},
	})
	reg.MustAdd(&Command{
		Name: "key", Summary: "Work with the key.",
		Sub: []*Command{
			{Name: "show", Summary: "Show it.", Marks: MarkAdmin, Run: func(c *Call) error { io.WriteString(c.Stdout, "key show\n"); return nil }},
			{Name: "copy", Summary: "Copy it.", Run: func(c *Call) error { return nil }},
		},
	})
	reg.MustAdd(&Command{
		Name: "wait", Summary: "Blocks until the deadline.", Timeout: time.Hour,
		Run: func(c *Call) error {
			io.WriteString(c.Stdout, "timeout="+c.Timeout.String()+"\n")
			if c.Timeout == 0 || c.Timeout >= time.Hour {
				return nil
			}
			<-c.Ctx.Done()
			return c.Ctx.Err()
		},
	})
	reg.MustAdd(&Command{
		Name: "warn", Summary: "Returns the finished-with-warnings code.",
		Run: func(c *Call) error { io.WriteString(c.Stdout, "2 warnings\n"); return ExitWith(ExitWarnings) },
	})
	reg.MustAdd(&Command{
		Name: "plain", Summary: "Returns an untyped error.",
		Run: func(c *Call) error { return errors.New("something odd") },
	})
	reg.MustAdd(&Command{
		Name: "chatty", Summary: "Writes a lot to stdout.",
		Run: func(c *Call) error {
			for i := 0; i < 3; i++ {
				if _, err := io.WriteString(c.Stdout, "line\n"); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return reg
}

func TestRunDispatchAndHelp(t *testing.T) {
	reg := testRegistry()
	cases := []struct {
		name     string
		args     []string
		code     int
		outHas   []string
		errHas   []string
		outEmpty bool
	}{
		{"--help", []string{"--help"}, ExitOK, []string{"Usage:", "Commands:", "key", "Global flags", "[local] [root]"}, nil, false},
		{"-h", []string{"-h"}, ExitOK, []string{"Usage:"}, nil, false},
		{"help", []string{"help"}, ExitOK, []string{"Usage:"}, nil, false},
		{"help for a command", []string{"help", "load"}, ExitOK, []string{"aictl [global flags] load <model> [--wait]", "-wait", "Default timeout: 1m30s"}, nil, false},
		{"help for a subcommand", []string{"help", "key", "show"}, ExitOK, []string{"key show", "[admin]"}, nil, false},
		{"command --help", []string{"load", "--help"}, ExitOK, []string{"load <model>"}, nil, false},
		{"group --help", []string{"key", "--help"}, ExitOK, []string{"Subcommands:", "show", "copy"}, nil, false},
		{"help for an unknown command", []string{"help", "nope"}, ExitUsage, nil, []string{"[LAI-CLI-005]"}, true},
		{"no arguments prints help and fails", nil, ExitUsage, nil, []string{"Usage:", "Commands:"}, true},
		{"unknown command", []string{"bogus"}, ExitUsage, nil, []string{`Unknown command "bogus"`, "[LAI-CLI-005]", "Run `aictl help`"}, true},
		{"unknown global flag", []string{"--bogus", "local"}, ExitUsage, nil, []string{"Unknown option -bogus", "[LAI-CLI-005]"}, true},
		{"unknown command flag", []string{"load", "--bogus"}, ExitUsage, nil, []string{"Unknown option -bogus", "[LAI-CLI-005]", "aictl [global flags] load"}, true},
		{"flag missing its value", []string{"--socket"}, ExitUsage, nil, []string{"needs a value", "[LAI-CLI-005]"}, true},
		{"bad --lang", []string{"--lang", "fr", "local"}, ExitUsage, nil, []string{"[LAI-CLI-005]"}, true},
		{"bad --timeout", []string{"--timeout", "soon", "local"}, ExitUsage, nil, []string{"[LAI-CLI-005]"}, true},
		{"negative --timeout", []string{"--timeout", "-5s", "local"}, ExitUsage, nil, []string{"[LAI-CLI-005]"}, true},
		{"unknown subcommand", []string{"key", "bogus"}, ExitUsage, nil, []string{`Unknown subcommand "bogus" for key`, "[LAI-CLI-005]"}, true},
		{"group without a subcommand", []string{"key"}, ExitUsage, nil, []string{"Subcommands:"}, true},
		{"local command", []string{"local"}, ExitOK, []string{"local ok"}, nil, false},
		{"subcommand", []string{"key", "show"}, ExitOK, []string{"key show"}, nil, false},
		{"flags after the positional", []string{"load", "m1", "--wait"}, ExitOK, []string{"load|true||m1"}, nil, false},
		{"flags between positionals", []string{"load", "m1", "--profile", "p", "m2"}, ExitOK, []string{"load|false|p|m1|m2"}, nil, false},
		{"-- ends the flags", []string{"load", "--", "--wait"}, ExitOK, []string{"load|false||--wait"}, nil, false},
		{"global flags go before the command", []string{"-v", "--yes", "--json", "local"}, ExitOK, []string{"local ok"}, nil, false},
		{"exit 8 prints nothing more", []string{"warn"}, ExitWarnings, []string{"2 warnings"}, nil, false},
		{"an untyped error shows its text, exit 1", []string{"plain"}, ExitFailed, nil, []string{"something odd"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, reg, runOpts{}, tc.args...)
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", r.code, tc.code, r.out, r.errOut)
			}
			for _, s := range tc.outHas {
				if !strings.Contains(r.out, s) {
					t.Errorf("stdout lacks %q:\n%s", s, r.out)
				}
			}
			for _, s := range tc.errHas {
				if !strings.Contains(r.errOut, s) {
					t.Errorf("stderr lacks %q:\n%s", s, r.errOut)
				}
			}
			if tc.outEmpty && r.out != "" {
				t.Errorf("stdout = %q, want empty", r.out)
			}
		})
	}
}

func TestHelpWithoutCommands(t *testing.T) {
	r := runCLI(t, NewRegistry(), runOpts{}, "--help")
	if r.code != ExitOK || !strings.Contains(r.out, "No commands are available") {
		t.Fatalf("exit %d out %q", r.code, r.out)
	}
}

// Every global flag of aictl.md section 3.1 is parsed and documented in help.
func TestGlobalFlagsDocumented(t *testing.T) {
	for _, f := range []string{"--json", "--lang", "--no-color", "-y, --yes", "-q, --quiet", "-v, --verbose", "--socket", "--url",
		"--token-file", "--timeout", "--pin-stdin", "-h, --help"} {
		if !strings.Contains(globalFlagsHelp, f) {
			t.Errorf("help lacks %s", f)
		}
	}
	g, rest, err := parseGlobal(Env{Getenv: func(string) string { return "" }},
		[]string{"--json", "--lang", "es", "--no-color", "-y", "-q", "-v", "--socket", "/s", "--url", "http://localhost:1",
			"--token-file", "/t", "--timeout", "90s", "--pin-stdin", "cmd", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	want := Global{JSON: true, Lang: LangES, NoColor: true, Yes: true, Quiet: true, Verbose: true, Socket: "/s", URL: "http://localhost:1",
		TokenFile: "/t", Timeout: 90 * time.Second, TimeoutSet: true, PinStdin: true}
	if g != want {
		t.Errorf("global = %+v\nwant     %+v", g, want)
	}
	if strings.Join(rest, " ") != "cmd -v" {
		t.Errorf("rest = %v (flags after the command name belong to the command)", rest)
	}
	for _, long := range []string{"--yes", "--quiet", "--verbose"} {
		g, _, err := parseGlobal(Env{Getenv: func(string) string { return "" }}, []string{long})
		if err != nil || !(g.Yes || g.Quiet || g.Verbose) {
			t.Errorf("%s not parsed: %v %+v", long, err, g)
		}
	}
}

// Flags win over the environment, which wins over the defaults.
func TestGlobalPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want Global
	}{
		{"defaults", nil, nil, Global{Lang: LangEN, Socket: DefaultSocket, URL: DefaultURL, TokenFile: DefaultTokenFile, NoColor: true}},
		{"environment overrides the defaults",
			map[string]string{"LOCALAI_SOCKET": "/env.sock", "LOCALAI_URL": "http://127.0.0.1:9", "LOCALAI_TOKEN_FILE": "/env-token"}, nil,
			Global{Lang: LangEN, Socket: "/env.sock", URL: "http://127.0.0.1:9", TokenFile: "/env-token", NoColor: true}},
		{"flags override the environment",
			map[string]string{"LOCALAI_SOCKET": "/env.sock", "LOCALAI_URL": "http://127.0.0.1:9", "LOCALAI_TOKEN_FILE": "/env-token"},
			[]string{"--socket", "/flag.sock", "--url", "http://localhost:5", "--token-file", "/flag-token"},
			Global{Lang: LangEN, Socket: "/flag.sock", URL: "http://localhost:5", TokenFile: "/flag-token", NoColor: true}},
		{"a flag overrides only its own variable",
			map[string]string{"LOCALAI_SOCKET": "/env.sock", "LOCALAI_URL": "http://127.0.0.1:9"}, []string{"--url", "http://localhost:5"},
			Global{Lang: LangEN, Socket: "/env.sock", URL: "http://localhost:5", TokenFile: DefaultTokenFile, NoColor: true}},
		{"empty environment variables count as unset", map[string]string{"LOCALAI_SOCKET": ""}, nil,
			Global{Lang: LangEN, Socket: DefaultSocket, URL: DefaultURL, TokenFile: DefaultTokenFile, NoColor: true}},
		{"LANG es_CR is Spanish", map[string]string{"LANG": "es_CR.UTF-8"}, nil,
			Global{Lang: LangES, Socket: DefaultSocket, URL: DefaultURL, TokenFile: DefaultTokenFile, NoColor: true}},
		{"--lang beats LANG", map[string]string{"LANG": "es_CR.UTF-8"}, []string{"--lang", "en"},
			Global{Lang: LangEN, Socket: DefaultSocket, URL: DefaultURL, TokenFile: DefaultTokenFile, NoColor: true}},
		{"LANG C is English", map[string]string{"LANG": "C"}, nil,
			Global{Lang: LangEN, Socket: DefaultSocket, URL: DefaultURL, TokenFile: DefaultTokenFile, NoColor: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _, err := parseGlobal(Env{Getenv: func(k string) string { return tc.env[k] }}, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if g != tc.want {
				t.Errorf("got  %+v\nwant %+v", g, tc.want)
			}
		})
	}
}

func TestNoColorImplied(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		tty  bool
		args []string
		want bool
	}{
		{"terminal, nothing set", nil, true, nil, false},
		{"flag", nil, true, []string{"--no-color"}, true},
		{"NO_COLOR set", map[string]string{"NO_COLOR": "1"}, true, nil, true},
		{"TERM=dumb", map[string]string{"TERM": "dumb"}, true, nil, true},
		{"stdout not a terminal", nil, false, nil, true},
	}
	for _, tc := range cases {
		g, _, err := parseGlobal(Env{Getenv: func(k string) string { return tc.env[k] }, StdoutIsTTY: tc.tty}, tc.args)
		if err != nil || g.NoColor != tc.want {
			t.Errorf("%s: NoColor = %v (err %v), want %v", tc.name, g.NoColor, err, tc.want)
		}
	}
}

func TestSpanishErrors(t *testing.T) {
	r := runCLI(t, testRegistry(), runOpts{env: map[string]string{"LANG": "es_CR.UTF-8"}}, "bogus")
	if r.code != ExitUsage || !strings.Contains(r.errOut, "Comando desconocido") || !strings.Contains(r.errOut, "→ Solución: Ejecute `aictl help`.") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	if !strings.Contains(r.errOut, "✗") {
		t.Errorf("UTF-8 locale should print the ✗ symbol: %q", r.errOut)
	}
	ascii := runCLI(t, testRegistry(), runOpts{}, "bogus")
	if !strings.Contains(ascii.errOut, "[x] ") {
		t.Errorf("a non-UTF-8 locale should print [x]: %q", ascii.errOut)
	}
}

func TestJSONModeFailureIsAnEnvelopeOnStdout(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"unknown command", []string{"--json", "bogus"}, CodeUsage, ExitUsage},
		{"unknown flag", []string{"--json", "load", "--bogus"}, CodeUsage, ExitUsage},
		{"no command", []string{"--json"}, CodeUsage, ExitUsage},
		{"needs root", []string{"--json", "root"}, CodeNeedRoot, ExitDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runCLI(t, testRegistry(), runOpts{euid: 1000}, tc.args...)
			if r.code != tc.exit {
				t.Fatalf("exit %d, want %d", r.code, tc.exit)
			}
			if r.errOut != "" {
				t.Errorf("stderr must stay empty in --json mode: %q", r.errOut)
			}
			var env struct {
				Error struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Fix     string         `json:"fix"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			dec := json.NewDecoder(strings.NewReader(r.out))
			if err := dec.Decode(&env); err != nil {
				t.Fatalf("stdout is not JSON: %q", r.out)
			}
			if dec.More() {
				t.Errorf("more than one document on stdout: %q", r.out)
			}
			if env.Error.Code != tc.code || env.Error.Message == "" || env.Error.Fix == "" || env.Error.Details == nil {
				t.Errorf("envelope = %+v", env.Error)
			}
		})
	}
}

func TestRequireRoot(t *testing.T) {
	r := runCLI(t, testRegistry(), runOpts{euid: 1000}, "root")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-006]") || !strings.Contains(r.errOut, "sudo aictl") {
		t.Errorf("non-root: exit %d stderr %q", r.code, r.errOut)
	}
	r = runCLI(t, testRegistry(), runOpts{euid: 0}, "root")
	if r.code != ExitOK || r.out != "root ok\n" {
		t.Errorf("root: exit %d out %q", r.code, r.out)
	}
}

func TestTimeouts(t *testing.T) {
	reg := testRegistry()
	t.Run("the command default applies", func(t *testing.T) {
		r := runCLI(t, reg, runOpts{}, "wait")
		if r.code != ExitOK || r.out != "timeout=1h0m0s\n" {
			t.Fatalf("exit %d out %q", r.code, r.out)
		}
	})
	t.Run("--timeout overrides it, and expiry is exit 7", func(t *testing.T) {
		r := runCLI(t, reg, runOpts{}, "--timeout", "50ms", "wait")
		if r.code != ExitTimeout || !strings.Contains(r.errOut, "[LAI-CLI-004]") || !strings.Contains(r.errOut, "aictl status") {
			t.Fatalf("exit %d stderr %q", r.code, r.errOut)
		}
		if !strings.Contains(r.out, "timeout=50ms") {
			t.Errorf("stdout = %q", r.out)
		}
	})
	t.Run("--timeout 0 means no deadline", func(t *testing.T) {
		r := runCLI(t, reg, runOpts{}, "--timeout", "0", "wait")
		if r.code != ExitOK || r.out != "timeout=0s\n" {
			t.Fatalf("exit %d out %q", r.code, r.out)
		}
	})
}

func TestCtrlCIsExit130(t *testing.T) {
	reg := NewRegistry()
	reg.MustAdd(&Command{Name: "block", Summary: "Waits for Ctrl-C.", Run: func(c *Call) error { <-c.Ctx.Done(); return c.Ctx.Err() }})
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb bytes.Buffer
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	code := Run(ctx, Env{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }, Version: "x"}, reg, []string{"block"})
	if code != ExitInterrupted || !strings.Contains(errb.String(), "[LAI-CLI-010]") || !strings.Contains(errb.String(), "aictl status") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
}

// A broken pipe on stdout (aictl logs | head) is exit 0 and silent.
type brokenPipe struct{ calls int }

func (b *brokenPipe) Write(p []byte) (int, error) {
	b.calls++
	return 0, syscall.EPIPE
}

func TestBrokenPipeIsExitZero(t *testing.T) {
	bp := &brokenPipe{}
	r := runCLI(t, testRegistry(), runOpts{out: bp}, "chatty")
	if r.code != ExitOK || r.errOut != "" {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	if bp.calls != 1 {
		t.Errorf("stdout written %d times after the pipe broke", bp.calls)
	}
	// an EPIPE returned by a command is exit 0 too, with no message
	reg := NewRegistry()
	reg.MustAdd(&Command{Name: "pipe", Summary: "x", Run: func(c *Call) error { return &wrapErr{syscall.EPIPE} }})
	r = runCLI(t, reg, runOpts{}, "pipe")
	if r.code != ExitOK || r.errOut != "" {
		t.Errorf("returned EPIPE: exit %d stderr %q", r.code, r.errOut)
	}
}

type wrapErr struct{ err error }

func (w *wrapErr) Error() string { return "write: " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }

func TestAPIErrorsArePrintedInPlainWords(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.RespondError(http.MethodGet, "/models", 409, api.CodeBusy, "Another operation is running.", "Wait for it to finish.",
		map[string]any{"op_id": "op_1"})
	cfg := func(o *runOpts) {
		o.env = map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL}
	}
	var o runOpts
	cfg(&o)
	o.h = h
	r := runCLI(t, testRegistry(), o, "ping")
	if r.code != ExitRefused {
		t.Fatalf("exit %d, stderr %q", r.code, r.errOut)
	}
	for _, want := range []string{"Another operation is running.  [" + api.CodeBusy + "]", "→ Fix: Wait for it to finish."} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	for _, never := range []string{"409", "HTTP", "details:", "req_"} {
		if strings.Contains(r.errOut, never) {
			t.Errorf("human output shows %q without -v:\n%s", never, r.errOut)
		}
	}
	// -v adds details and the request id
	o.env["LANG"] = "en_US.UTF-8"
	r = runCLI(t, testRegistry(), o, "-v", "ping")
	for _, want := range []string{`details: {"op_id":"op_1"}`, "request: req_ABCDEFGHJKLM", "via socket"} {
		if !strings.Contains(r.errOut, want) {
			t.Errorf("-v stderr lacks %q:\n%s", want, r.errOut)
		}
	}
	// --json: the agent's envelope on stdout, stderr empty
	r = runCLI(t, testRegistry(), o, "--json", "ping")
	if r.code != ExitRefused || r.errOut != "" || !strings.Contains(r.out, `"code":"`+api.CodeBusy+`"`) {
		t.Errorf("--json: exit %d stdout %q stderr %q", r.code, r.out, r.errOut)
	}
}

func TestBadResponsePrintsRequestID(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Respond(http.MethodGet, "/models", 200, "<html>")
	r := runCLI(t, testRegistry(), runOpts{h: h, env: map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath}}, "ping")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-CLI-009]") || !strings.Contains(r.errOut, "request: req_ABCDEFGHJKLM") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
}

func TestAgentDownThroughRun(t *testing.T) {
	h := newHarness(t, fake.Options{NoSocket: true, NoTCP: true})
	env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL, "LOCALAI_TOKEN_FILE": h.cfg.TokenFile}
	r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, "ping")
	if r.code != ExitUnreachable || !strings.Contains(r.errOut, "[LAI-CLI-001]") || !strings.Contains(r.errOut, "sudo systemctl start localai-agent") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
}

func TestPermissionDeniedThroughRun(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.cfg.DialUnix = func(context.Context, string) (net.Conn, error) { return nil, dialErr(syscall.EACCES) }
	h.unreadable()
	env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL}
	r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, "ping")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "[LAI-CLI-002]") || !strings.Contains(r.errOut, "usermod -aG localai-operators") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
}

// A non-loopback --url is refused with exit 2 and nothing reaches the agent.
func TestNonLoopbackURLThroughRun(t *testing.T) {
	h := newHarness(t, fake.Options{})
	env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath}
	r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, "--url", "http://example.test:7070", "ping")
	if r.code != ExitUsage || !strings.Contains(r.errOut, "[LAI-CLI-005]") || !strings.Contains(r.errOut, "--url") {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	if n := len(h.agent.Requests()); n != 0 {
		t.Errorf("%d requests reached the agent", n)
	}
}

// Local (offline recovery) commands never look at the transport settings.
func TestLocalCommandIgnoresBadTransportSettings(t *testing.T) {
	env := map[string]string{"LOCALAI_URL": "http://example.test:7070"}
	r := runCLI(t, testRegistry(), runOpts{env: env}, "local")
	if r.code != ExitOK || r.out != "local ok\n" {
		t.Fatalf("exit %d out %q stderr %q", r.code, r.out, r.errOut)
	}
}

func TestVersionSkewWarningThroughRun(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantWarn bool
		wantCode int
	}{
		{"warns on stderr and continues", []string{"ping"}, true, ExitOK},
		{"--quiet hides it", []string{"-q", "ping"}, false, ExitOK},
		{"--json keeps stdout clean but still warns on success", []string{"--json", "ping"}, true, ExitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.Options{Version: "9.9.9"})
			h.agent.Respond(http.MethodGet, "/models", 200, `{"models":[]}`)
			env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL}
			r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, tc.args...)
			if r.code != tc.wantCode || r.out != `{"models":[]}`+"\n" {
				t.Fatalf("exit %d out %q", r.code, r.out)
			}
			if got := strings.Contains(r.errOut, "[LAI-CLI-003]"); got != tc.wantWarn {
				t.Errorf("warning shown = %v, want %v: %q", got, tc.wantWarn, r.errOut)
			}
		})
	}
	t.Run("--json hides the warning when the command fails", func(t *testing.T) {
		h := newHarness(t, fake.Options{Version: "9.9.9"})
		env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_URL": h.agent.URL}
		r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, "--json", "ping") // /models is a 404 here
		if r.code != ExitNotFound || r.errOut != "" {
			t.Fatalf("exit %d stderr %q", r.code, r.errOut)
		}
	})
}

func TestDebugEnvironmentVariable(t *testing.T) {
	h := newHarness(t, fake.Options{})
	h.agent.Respond(http.MethodGet, "/models", 200, `{"models":[]}`)
	env := map[string]string{"LOCALAI_SOCKET": h.agent.SocketPath, "LOCALAI_CLI_DEBUG": "1"}
	r := runCLI(t, testRegistry(), runOpts{env: env, h: h}, "ping")
	if r.code != ExitOK || !strings.Contains(r.errOut, `"path":"/api/v1/models"`) {
		t.Fatalf("exit %d stderr %q", r.code, r.errOut)
	}
	env["LOCALAI_CLI_DEBUG"] = ""
	r = runCLI(t, testRegistry(), runOpts{env: env, h: h}, "ping")
	if strings.Contains(r.errOut, "/api/v1") {
		t.Errorf("debug output without LOCALAI_CLI_DEBUG: %q", r.errOut)
	}
}
