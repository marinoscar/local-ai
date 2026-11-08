package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// KeySource reads single key presses from the terminal for the menu. Main gives the real one
// (stdin in raw mode); a nil KeySource means there is no menu, so tests of Run never see it.
type KeySource interface {
	ReadKey() (menuKey, error)
}

// NewOSKeys returns the KeySource on in (normally os.Stdin), or nil when in is not a terminal.
func NewOSKeys(in *os.File) KeySource {
	if !term.IsTerminal(int(in.Fd())) {
		return nil
	}
	return &osKeys{in: in}
}

type osKeys struct{ in *os.File }

// ReadKey switches the terminal to raw mode for one read and back, so a command the menu runs next
// sees a normal terminal. SIGTERM or SIGHUP while waiting ends the menu.
func (k *osKeys) ReadKey() (menuKey, error) {
	fd := int(k.in.Fd())
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return menuKey{}, err
	}
	defer func() { _ = term.Restore(fd, saved) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := k.in.Read(buf)
		ch <- res{buf[:n], err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return menuKey{kind: keyQuit}, nil
		}
		return parseKey(r.b), nil
	case <-sig:
		return menuKey{}, io.EOF
	}
}

// parseKey turns the bytes of one key press into a menuKey: arrows and Home/End as ANSI or SS3
// sequences, vi keys j/k, digits, Enter, Esc, Backspace, q, Ctrl-C and Ctrl-D.
func parseKey(b []byte) menuKey {
	if len(b) == 0 {
		return menuKey{kind: keyQuit}
	}
	s := string(b)
	switch s {
	case "\x1b[A", "\x1bOA", "k":
		return menuKey{kind: keyUp}
	case "\x1b[B", "\x1bOB", "j":
		return menuKey{kind: keyDown}
	case "\x1b[D", "\x1bOD", "\x1b", "\x7f", "\x08", "h":
		return menuKey{kind: keyBack}
	case "\x1b[C", "\x1bOC", "l":
		return menuKey{kind: keyEnter}
	case "\x1b[H", "\x1bOH", "\x1b[1~":
		return menuKey{kind: keyHome}
	case "\x1b[F", "\x1bOF", "\x1b[4~":
		return menuKey{kind: keyEnd}
	case "\r", "\n", "\r\n", " ":
		return menuKey{kind: keyEnter}
	case "q", "Q", "\x03", "\x04":
		return menuKey{kind: keyQuit}
	}
	if len(b) == 1 && b[0] >= '0' && b[0] <= '9' {
		return menuKey{kind: keyDigit, digit: int(b[0] - '0')}
	}
	return menuKey{kind: keyOther}
}

// menuAvailable says whether `aictl` with no arguments opens the menu: a real keyboard, stdout on a
// terminal that is not "dumb", and AICTL_PLAIN not set. Everything else keeps the old behaviour (the
// command list on stderr, exit 2), so scripts and pipes never see the menu.
func menuAvailable(env Env) bool {
	return env.Keys != nil && env.Terminal != nil && env.Terminal.Interactive() && env.StdoutIsTTY &&
		env.Getenv("AICTL_PLAIN") == "" && env.Getenv("TERM") != "dumb"
}

// runMenu opens the menu. Commands run through Run with the same registry and the global flags the
// person gave (language, socket, URL, token file), each with its own Ctrl-C, so stopping a long
// command returns to the menu instead of closing it.
func runMenu(env Env, reg *Registry, g Global) int {
	h := &osMenuHost{env: env, reg: reg, keys: env.Keys, prefix: menuGlobalArgs(g)}
	m := &menu{
		host:  h,
		lang:  g.Lang,
		sym:   out.SymbolsFor(out.UTF8Enabled(env.Getenv)),
		color: !g.NoColor,
		root:  menuTree(),
	}
	return m.run()
}

// menuGlobalArgs carries the person's global flags to every command the menu runs.
func menuGlobalArgs(g Global) []string {
	a := []string{"--lang", string(g.Lang), "--socket", g.Socket, "--url", g.URL, "--token-file", g.TokenFile}
	if g.NoColor {
		a = append(a, "--no-color")
	}
	if g.Verbose {
		a = append(a, "--verbose")
	}
	return a
}

type osMenuHost struct {
	env    Env
	reg    *Registry
	keys   KeySource
	prefix []string
}

func (h *osMenuHost) ReadKey() (menuKey, error) { return h.keys.ReadKey() }

func (h *osMenuHost) Run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, h.env, h.reg, append(append([]string(nil), h.prefix...), args...))
}

func (h *osMenuHost) Capture(args []string) ([]byte, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	env := h.env
	env.Stdout, env.Stderr, env.StdoutIsTTY = &buf, io.Discard, false
	env.Terminal, env.Keys = newStreamTerminal(strings.NewReader("")), nil
	all := append(append([]string(nil), h.prefix...), "--json")
	code := Run(ctx, env, h.reg, append(all, args...))
	return buf.Bytes(), code
}

func (h *osMenuHost) Pause() {
	_, _ = io.WriteString(h.env.Stderr, "\n"+pick(Lang(h.prefix[1]),
		"Press Enter to go back to the menu.", "Pulse Enter para volver al menú.")+" ")
	_, _ = h.env.Terminal.ReadLine(context.Background())
}

func (h *osMenuHost) Clear()            { _, _ = io.WriteString(h.env.Stdout, "\x1b[H\x1b[2J") }
func (h *osMenuHost) Screen() io.Writer { return h.env.Stdout }
