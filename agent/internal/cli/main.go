// Package cli is the aictl implementation (docs/specs/aictl.md): the command tree, the global
// flags, the transport to the agent (unix socket, TCP plus token as fallback), the agent client with
// the error envelope decoded, and the exit-code map.
//
// This package is the framework (AICTL-01). A command is a *Command registered with Register from
// its own file (registry.go); it receives a *Call, asks it for the Client, and returns an error that
// ExitFor maps to the documented exit code. Output layout, confirmations and the PIN flow are
// AICTL-02; progress over SSE is AICTL-03.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// Env is everything the CLI reads from the process, so tests can run it hermetically.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Getenv reads an environment variable ("" when unset).
	Getenv func(string) string
	// Geteuid returns the effective user id (RequireRoot).
	Geteuid func() int
	// StdoutIsTTY says whether stdout is a terminal (colour depends on it).
	StdoutIsTTY bool
	// Terminal is the person at the keyboard for prompts (confirmations and the PIN). Nil means a
	// non-interactive terminal reading Stdin, so no prompt is ever shown.
	Terminal Terminal
	// Version is the CLI version compared with the agent's.
	Version string
	// ClientConfig, if set, may adjust the client configuration (test hooks).
	ClientConfig func(*Config)
}

// Main runs aictl with args (without the program name) and returns the exit code. cmd/aiagent calls
// it for argv[0] "aictl" and for "aiagent ctl".
func Main(args []string, stdout, stderr io.Writer) int {
	// A write to a closed pipe (aictl logs | head) must come back as EPIPE, not kill the process.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := Env{
		Stdin:    os.Stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Getenv:   os.Getenv,
		Geteuid:  os.Geteuid,
		Version:  app.Version,
		Terminal: NewOSTerminal(os.Stdin),
	}
	if f, ok := stdout.(*os.File); ok {
		if fi, err := f.Stat(); err == nil {
			env.StdoutIsTTY = fi.Mode()&os.ModeCharDevice != 0
		}
	}
	return Run(ctx, env, DefaultRegistry, args)
}

// Global holds the global flags of aictl.md section 3.1, after environment defaults.
type Global struct {
	JSON    bool
	Lang    Lang
	NoColor bool // effective: the flag, NO_COLOR set, TERM=dumb, --json, or stdout not a terminal
	Yes     bool
	Quiet   bool
	Verbose bool
	Socket  string
	URL     string
	// TokenFile is the token for the TCP fallback.
	TokenFile string
	// Timeout is the --timeout value; TimeoutSet says whether the user gave the flag (0 then means
	// no deadline).
	Timeout    time.Duration
	TimeoutSet bool
	PinStdin   bool
}

// langFromEnv is the default language: Spanish when $LANG starts with "es", else English.
func langFromEnv(getenv func(string) string) Lang { return i18n.Detect(getenv) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseGlobal parses the global flags, which come before the command name. Flags win over the
// environment (LOCALAI_SOCKET, LOCALAI_URL, LOCALAI_TOKEN_FILE), which wins over the defaults.
// It returns the arguments from the command name on. Because it stops at the first word that is not
// a flag, global flags after the command name belong to the command.
func parseGlobal(env Env, args []string) (Global, []string, error) {
	g := Global{
		Lang:      langFromEnv(env.Getenv),
		Socket:    firstNonEmpty(env.Getenv("LOCALAI_SOCKET"), DefaultSocket),
		URL:       firstNonEmpty(env.Getenv("LOCALAI_URL"), DefaultURL),
		TokenFile: firstNonEmpty(env.Getenv("LOCALAI_TOKEN_FILE"), DefaultTokenFile),
	}
	var noColor bool
	fs := flag.NewFlagSet("aictl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&g.JSON, "json", false, "")
	fs.Func("lang", "", func(s string) error {
		switch Lang(s) {
		case LangEN, LangES:
			g.Lang = Lang(s)
			return nil
		}
		return fmt.Errorf("must be en or es")
	})
	fs.BoolVar(&noColor, "no-color", false, "")
	fs.BoolVar(&g.Yes, "y", false, "")
	fs.BoolVar(&g.Yes, "yes", false, "")
	fs.BoolVar(&g.Quiet, "q", false, "")
	fs.BoolVar(&g.Quiet, "quiet", false, "")
	fs.BoolVar(&g.Verbose, "v", false, "")
	fs.BoolVar(&g.Verbose, "verbose", false, "")
	fs.StringVar(&g.Socket, "socket", g.Socket, "")
	fs.StringVar(&g.URL, "url", g.URL, "")
	fs.StringVar(&g.TokenFile, "token-file", g.TokenFile, "")
	fs.DurationVar(&g.Timeout, "timeout", 0, "")
	fs.BoolVar(&g.PinStdin, "pin-stdin", false, "")
	err := fs.Parse(args)
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			g.TimeoutSet = true
		}
	})
	g.NoColor = !out.ColorEnabled(env.StdoutIsTTY, noColor, g.JSON, env.Getenv)
	if err == nil && g.Timeout < 0 {
		err = errors.New("invalid value for -timeout: must not be negative")
	}
	return g, fs.Args(), err
}

// Call is what a command's Run receives.
type Call struct {
	// Ctx carries the command's overall deadline (--timeout, else the command's default) and is
	// cancelled by Ctrl-C.
	Ctx     context.Context
	Global  Global
	Command *Command
	// Path is the command words that were matched, for example ["key", "show"].
	Path []string
	// Args are the positional arguments (flags removed); Flags holds the parsed command flags.
	Args  []string
	Flags *flag.FlagSet
	Stdin io.Reader
	// Stdout and Stderr are the process streams. Stdout swallows a broken pipe.
	Stdout io.Writer
	Stderr io.Writer
	Lang   Lang
	// Out is the output layer: symbols, colour, problems, tables, the --json writer. Commands print
	// through it so every command looks and fails the same way (package out).
	Out *out.Printer
	// Term is the person at the keyboard for prompts; Confirm, ConfirmTyped and Admin use it.
	Term Terminal
	// Timeout is the deadline in force (0: none).
	Timeout time.Duration

	env      Env
	client   *Client
	clientEr error
	warnings []*CLIError
}

// Client returns the agent client, created on first use. Local commands that never call it never
// validate --url or touch the transport, so offline recovery works whatever the network settings.
func (c *Call) Client() (*Client, error) {
	if c.client != nil || c.clientEr != nil {
		return c.client, c.clientEr
	}
	cfg := Config{
		Socket:    c.Global.Socket,
		URL:       c.Global.URL,
		TokenFile: c.Global.TokenFile,
		Lang:      c.Lang,
		Version:   c.env.Version,
		Verbose:   c.Global.Verbose,
		Debug:     c.env.Getenv("LOCALAI_CLI_DEBUG") == "1",
		Stderr:    c.Stderr,
		Warn:      c.Warn,
	}
	if c.env.ClientConfig != nil {
		c.env.ClientConfig(&cfg)
	}
	c.client, c.clientEr = NewClient(cfg)
	return c.client, c.clientEr
}

// Bool returns the value of the command's boolean flag name (false if it is not declared).
func (c *Call) Bool(name string) bool {
	f := c.Flags.Lookup(name)
	return f != nil && f.Value.String() == "true"
}

// String returns the value of the command's flag name ("" if it is not declared).
func (c *Call) String(name string) string {
	if f := c.Flags.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// Warn queues a warning. Warnings go to stderr after the command finishes, except with --quiet, and
// with --json when the command failed (stderr stays empty on failure, aictl.md section 3.5).
func (c *Call) Warn(e *CLIError) { c.warnings = append(c.warnings, e) }

// Err builds a CLIError in the user's language; message "" uses the code's standard sentence.
func (c *Call) Err(code, message string) *CLIError { return NewCLIError(c.Lang, code, message) }

// RequireRoot returns the exit-4 LAI-CLI-006 error unless the process runs as root. Root commands
// call it first, before touching any file.
func RequireRoot(c *Call) error {
	if c.env.Geteuid != nil && c.env.Geteuid() == 0 {
		return nil
	}
	return c.Err(CodeNeedRoot, "")
}

// guardWriter turns a broken pipe on stdout into silence: it records the failure, swallows it and
// every later write, and the run ends with exit 0 (aictl.md section 5).
type guardWriter struct {
	w      io.Writer
	broken bool
}

func (g *guardWriter) Write(p []byte) (int, error) {
	if g.broken {
		return len(p), nil
	}
	n, err := g.w.Write(p)
	if err != nil && errors.Is(err, syscall.EPIPE) {
		g.broken = true
		return len(p), nil
	}
	return n, err
}

// Run is Main with its inputs explicit. It parses the global flags, resolves the command in reg,
// parses the command's flags, applies the deadline, runs it and maps the result to an exit code.
func Run(ctx context.Context, env Env, reg *Registry, args []string) int {
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}
	if env.Stdin == nil {
		env.Stdin = strings.NewReader("")
	}
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	gw := &guardWriter{w: env.Stdout}
	env.Stdout = gw
	if env.Terminal == nil {
		env.Terminal = newStreamTerminal(env.Stdin)
	}

	g, rest, err := parseGlobal(env, args)
	printer := newPrinter(env, g)
	rep := reporter{p: printer, g: g, env: env}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeHelp(env.Stdout, reg)
			return ExitOK
		}
		code := rep.fail(NewCLIError(g.Lang, CodeUsage, usageMessage(g.Lang, err)))
		if !g.JSON {
			shortUsage(env.Stderr, "")
		}
		return code
	}
	if len(rest) == 0 {
		if g.JSON {
			return rep.fail(NewCLIError(g.Lang, CodeUsage, pick(g.Lang, "No command given.", "No se indicó ningún comando.")))
		}
		writeHelp(env.Stderr, reg)
		return ExitUsage
	}
	if rest[0] == "help" {
		return runHelp(rep, reg, rest[1:])
	}

	res, unknown, rerr := reg.resolve(rest)
	if rerr != nil {
		switch {
		case unknown != "" && res.cmd == nil:
			code := rep.fail(NewCLIError(g.Lang, CodeUsage, pick(g.Lang,
				fmt.Sprintf("Unknown command %q.", unknown), fmt.Sprintf("Comando desconocido %q.", unknown))))
			if !g.JSON {
				shortUsage(env.Stderr, "")
			}
			return code
		case unknown != "":
			code := rep.fail(NewCLIError(g.Lang, CodeUsage, pick(g.Lang,
				fmt.Sprintf("Unknown subcommand %q for %s.", unknown, strings.Join(res.path, " ")),
				fmt.Sprintf("Subcomando desconocido %q para %s.", unknown, strings.Join(res.path, " ")))))
			if !g.JSON {
				writeCommandHelp(env.Stderr, res)
			}
			return code
		default: // a group command without a subcommand
			if g.JSON {
				return rep.fail(NewCLIError(g.Lang, CodeUsage, pick(g.Lang,
					fmt.Sprintf("%s needs a subcommand.", strings.Join(res.path, " ")),
					fmt.Sprintf("%s necesita un subcomando.", strings.Join(res.path, " ")))))
			}
			if hasHelpArg(res.rest) {
				writeCommandHelp(env.Stdout, res)
				return ExitOK
			}
			writeCommandHelp(env.Stderr, res)
			return ExitUsage
		}
	}

	fs := flag.NewFlagSet(strings.Join(res.path, " "), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if res.cmd.Flags != nil {
		res.cmd.Flags(fs)
	}
	positional, perr := parseInterspersed(fs, res.rest)
	if perr != nil {
		if errors.Is(perr, flag.ErrHelp) {
			writeCommandHelp(env.Stdout, res)
			return ExitOK
		}
		code := rep.fail(NewCLIError(g.Lang, CodeUsage, usageMessage(g.Lang, perr)))
		if !g.JSON {
			shortUsage(env.Stderr, strings.Join(res.path, " "))
		}
		return code
	}

	timeout := res.cmd.Timeout
	if g.TimeoutSet {
		timeout = g.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c := &Call{
		Ctx: ctx, Global: g, Command: res.cmd, Path: res.path, Args: positional, Flags: fs,
		Stdin: env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr, Lang: g.Lang, Out: printer, Term: env.Terminal,
		Timeout: timeout, env: env,
	}
	runErr := normalize(res.cmd.Run(c), c)
	if c.client != nil {
		c.client.Close()
	}
	rep.warn(c.warnings, runErr != nil)
	if runErr != nil {
		return rep.fail(runErr)
	}
	return ExitOK
}

// normalize gives a bare context error its code: the deadline is LAI-CLI-004, Ctrl-C is LAI-CLI-010.
func normalize(err error, c *Call) error {
	if err == nil {
		return nil
	}
	var ce *CLIError
	var ae *APIError
	var ee *ExitError
	if errors.As(err, &ce) || errors.As(err, &ae) || errors.As(err, &ee) {
		return err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return c.Err(CodeTimeout, "").WithCause(err)
	case errors.Is(err, context.Canceled):
		return c.Err(CodeInterrupted, "").WithCause(err)
	}
	return err
}

func runHelp(rep reporter, reg *Registry, topic []string) int {
	env := rep.env
	if len(topic) == 0 {
		writeHelp(env.Stdout, reg)
		return ExitOK
	}
	res, unknown, err := reg.resolve(topic)
	if err != nil && res.cmd == nil || unknown != "" {
		word := unknown
		if word == "" {
			word = topic[0]
		}
		code := rep.fail(NewCLIError(rep.g.Lang, CodeUsage, pick(rep.g.Lang,
			fmt.Sprintf("No help for %q.", word), fmt.Sprintf("No hay ayuda para %q.", word))))
		return code
	}
	writeCommandHelp(env.Stdout, res)
	return ExitOK
}

func hasHelpArg(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// usageMessage turns a flag package error into a sentence without the package's wording.
func usageMessage(lang Lang, err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: "):
		opt := strings.TrimPrefix(msg, "flag provided but not defined: ")
		return pick(lang, fmt.Sprintf("Unknown option %s.", opt), fmt.Sprintf("Opción desconocida %s.", opt))
	case strings.HasPrefix(msg, "flag needs an argument: "):
		opt := strings.TrimPrefix(msg, "flag needs an argument: ")
		return pick(lang, fmt.Sprintf("The option %s needs a value.", opt), fmt.Sprintf("La opción %s necesita un valor.", opt))
	}
	return pick(lang, "Invalid option: "+msg+".", "Opción no válida: "+msg+".")
}

// parseInterspersed parses fs over args allowing flags before, between and after positional
// arguments (stdlib flag stops at the first positional). Everything after a lone "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	return append(positional, tail...), nil
}
