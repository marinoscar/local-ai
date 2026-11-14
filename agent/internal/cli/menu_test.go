package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// fakeHost scripts the keyboard and records what the menu ran.
type fakeHost struct {
	keys     []menuKey
	captures map[string]capture // by strings.Join(args, " ")
	runs     [][]string
	runCode  int
	pauses   int
	screen   bytes.Buffer
	frames   []string // the screen at each ReadKey
}

type capture struct {
	out  string
	code int
}

func (h *fakeHost) ReadKey() (menuKey, error) {
	h.frames = append(h.frames, h.screen.String())
	if len(h.keys) == 0 {
		return menuKey{}, io.EOF
	}
	k := h.keys[0]
	h.keys = h.keys[1:]
	return k, nil
}

func (h *fakeHost) Run(args []string) int {
	h.runs = append(h.runs, append([]string(nil), args...))
	return h.runCode
}

func (h *fakeHost) Capture(args []string) ([]byte, int) {
	c, ok := h.captures[strings.Join(args, " ")]
	if !ok {
		return nil, ExitUnreachable
	}
	return []byte(c.out), c.code
}

func (h *fakeHost) Pause()            { h.pauses++ }
func (h *fakeHost) Clear()            { h.screen.Reset() }
func (h *fakeHost) Screen() io.Writer { return &h.screen }

func key(k menuKeyKind) menuKey { return menuKey{kind: k} }
func digit(d int) menuKey       { return menuKey{kind: keyDigit, digit: d} }

const statusOK = `{"node":{"id":"ai-n9","display_name":"Test node"},"mode":{"value":"online"},
"models":{"default":"m-one","loaded":["m-one"],"loading":[],"present":2},"alerts":[]}`

const modelsJSON = `{"default":"m-one","models":[
 {"id":"m-one","display_name":"Model One","present":true,"loadable":true,"loaded":true,"default":true,"in_profile":true,"size_bytes":1000000000},
 {"id":"m-two","display_name":"Model Two","present":true,"loadable":true,"loaded":false,"in_profile":true},
 {"id":"m-three","display_name":"Model Three","present":false,"loadable":false,"loaded":false,"in_profile":true}]}`

func newTestMenu(h *fakeHost, l Lang) *menu {
	if h.captures == nil {
		h.captures = map[string]capture{"status": {statusOK, 0}, "models": {modelsJSON, 0}}
	}
	return &menu{host: h, lang: l, sym: out.UTF8Symbols, root: menuTree()}
}

func TestMenuRunsTheChosenCommand(t *testing.T) {
	cases := []struct {
		name string
		keys []menuKey
		want [][]string
	}{
		{"enter on the first item", []menuKey{key(keyEnter)}, [][]string{{"status"}}},
		{"down then enter", []menuKey{key(keyDown), key(keyEnter)}, [][]string{{"doctor"}}},
		{"up wraps to the last item, which is a text screen", []menuKey{key(keyUp), key(keyEnter)}, nil},
		{"digit shortcut", []menuKey{digit(2)}, [][]string{{"doctor"}}},
		{"submenu by digit then item", []menuKey{digit(5), digit(4)}, [][]string{{"mode", "survival"}}},
		{"back leaves the submenu; the cursor stays where it was", []menuKey{digit(5), key(keyBack), key(keyEnter), digit(1)}, [][]string{{"mode"}}},
		{"back then home", []menuKey{digit(5), key(keyBack), key(keyHome), key(keyEnter)}, [][]string{{"status"}}},
		{"back on the main menu does nothing", []menuKey{key(keyBack), key(keyEnter)}, [][]string{{"status"}}},
		{"a digit past the end does nothing", []menuKey{digit(3), digit(9), key(keyEnter)}, [][]string{{"key", "show"}}},
		{"other keys are ignored", []menuKey{key(keyOther), key(keyEnter)}, [][]string{{"status"}}},
		{"end then home", []menuKey{key(keyEnd), key(keyHome), key(keyEnter)}, [][]string{{"status"}}},
		{"quit at once", []menuKey{key(keyQuit), key(keyEnter)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeHost{keys: tc.keys}
			if code := newTestMenu(h, LangEN).run(); code != ExitOK {
				t.Fatalf("exit %d", code)
			}
			if len(h.runs) != len(tc.want) {
				t.Fatalf("runs = %v, want %v", h.runs, tc.want)
			}
			for i := range tc.want {
				if strings.Join(h.runs[i], " ") != strings.Join(tc.want[i], " ") {
					t.Fatalf("run %d = %v, want %v", i, h.runs[i], tc.want[i])
				}
			}
		})
	}
}

func TestMenuModelPickers(t *testing.T) {
	cases := []struct {
		name    string
		keys    []menuKey
		want    string
		message string
	}{
		{"load offers only models that can load and are not loaded", []menuKey{digit(4), digit(3), key(keyEnter)}, "load m-two", ""},
		{"unload offers the loaded model", []menuKey{digit(4), digit(4), key(keyEnter)}, "unload m-one", ""},
		{"default offers stored models; pick the second", []menuKey{digit(4), digit(5), key(keyDown), key(keyEnter)}, "default m-two", ""},
		{"download offers models of the profile not stored", []menuKey{digit(4), digit(6), digit(1)}, "download m-three", ""},
		{"back from a picker runs nothing", []menuKey{digit(4), digit(7), key(keyBack)}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeHost{keys: tc.keys}
			newTestMenu(h, LangEN).run()
			got := ""
			if len(h.runs) > 0 {
				got = strings.Join(h.runs[0], " ")
			}
			if got != tc.want {
				t.Fatalf("ran %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMenuPickerEmptyAndAgentDown(t *testing.T) {
	h := &fakeHost{keys: []menuKey{digit(4), digit(4)}, captures: map[string]capture{
		"status": {statusOK, 0},
		"models": {`{"models":[{"id":"m-two","present":true,"loadable":true}]}`, 0},
	}}
	newTestMenu(h, LangEN).run()
	if len(h.runs) != 0 || h.pauses != 1 || !strings.Contains(h.frames[len(h.frames)-1], "Load a model") {
		t.Fatalf("runs %v pauses %d", h.runs, h.pauses)
	}

	h = &fakeHost{keys: []menuKey{digit(4), digit(3)}, captures: map[string]capture{"status": {statusOK, 0}}}
	m := newTestMenu(h, LangEN)
	m.run()
	if len(h.runs) != 0 || h.pauses != 1 {
		t.Fatalf("agent down: runs %v pauses %d", h.runs, h.pauses)
	}
}

func TestMenuShowsTheTypedCommandAndTheResult(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{{0, "✓ Done."}, {6, "It did not finish (exit code 6)"}, {8, "with warnings"}, {9, "found problems"}, {ExitInterrupted, "Stopped."}} {
		h := &fakeHost{keys: []menuKey{digit(2)}, runCode: tc.code}
		m := newTestMenu(h, LangEN)
		m.run()
		// The screen at the final ReadKey was redrawn; the result was written before Pause.
		if h.pauses != 1 {
			t.Fatalf("pauses %d", h.pauses)
		}
		var screen bytes.Buffer
		h2 := &fakeHost{captures: h.captures, runCode: tc.code}
		m2 := newTestMenu(h2, LangEN)
		m2.runCommand(menuTree()[1], []string{"doctor"})
		screen.WriteString(h2.screen.String())
		for _, want := range []string{"Typed command: aictl doctor", tc.want} {
			if !strings.Contains(screen.String(), want) {
				t.Fatalf("screen lacks %q:\n%s", want, screen.String())
			}
		}
	}
}

func TestMenuHeader(t *testing.T) {
	cases := []struct {
		name   string
		status *capture
		lang   Lang
		want   []string
	}{
		{"all good", &capture{statusOK, 0}, LangEN, []string{"Test node", "mode online", "model m-one", "✓ Everything is working."}},
		{"agent down", nil, LangEN, []string{"The agent is not answering", "Find and fix problems"}},
		{"a problem with its code and fix", &capture{`{"node":{"id":"ai-n9"},"mode":{"value":"offline"},"alerts":[
			{"code":"LAI-SVC-001","severity":"error","title":"The library has no books","fix":"Plug in the ark drive."},
			{"code":"LAI-SVC-005","severity":"info","title":"ignored"}]}`, 0}, LangEN,
			[]string{"ai-n9", "no model loaded", "1 problem(s) need attention", "LAI-SVC-001", "Fix: Plug in the ark drive."}},
		{"spanish", &capture{statusOK, 0}, LangES, []string{"modo online", "✓ Todo funciona."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &fakeHost{captures: map[string]capture{}}
			if tc.status != nil {
				h.captures["status"] = *tc.status
			}
			text := strings.Join(newTestMenu(h, tc.lang).header(), "\n")
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Fatalf("header lacks %q:\n%s", w, text)
				}
			}
		})
	}
}

// Every screen fits 80x24 with the longest header, in both languages (ADR-0009 rule 7).
func TestMenuFitsEightyByTwentyFour(t *testing.T) {
	header := []string{
		"Local AI · a-long-node-name · mode survival · model a-model-id-of-thirty-chars-xx",
		strings.Repeat("x", 78), strings.Repeat("y", 78), strings.Repeat("z", 50),
	}
	for _, l := range []Lang{LangEN, LangES} {
		var walk func(title string, items []menuItem)
		walk = func(title string, items []menuItem) {
			if len(items) > 10 {
				t.Errorf("%s: %d items; digits reach only ten", title, len(items))
			}
			m := &menu{lang: l, sym: out.UTF8Symbols}
			var b bytes.Buffer
			m.draw(&b, header, &menuFrame{title: title, items: items}, true)
			lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
			if len(lines) > 24 {
				t.Errorf("%s (%s): %d lines", title, l, len(lines))
			}
			for _, ln := range lines {
				if n := utf8.RuneCountInString(ln); n > 80 {
					t.Errorf("%s (%s): %d columns: %q", title, l, n, ln)
				}
			}
			for _, it := range items {
				if len(it.sub) > 0 {
					walk(pick(l, it.en, it.es), it.sub)
				}
			}
		}
		walk("root", menuTree())
		h := emergencyHelp(l, out.UTF8Symbols)
		for _, ln := range strings.Split(h, "\n") {
			if utf8.RuneCountInString(ln) > 80 {
				t.Errorf("help (%s) too wide: %q", l, ln)
			}
		}
	}
}

// Every item is an existing command (so the menu cannot drift from the CLI) and has both labels.
func TestMenuItemsAreRealCommands(t *testing.T) {
	applyLate(DefaultRegistry)
	var walk func(items []menuItem)
	walk = func(items []menuItem) {
		for _, it := range items {
			if it.en == "" || it.es == "" {
				t.Errorf("item %q/%q lacks a label", it.en, it.es)
			}
			n := 0
			for _, set := range []bool{len(it.args) > 0, len(it.sub) > 0, it.info != nil} {
				if set {
					n++
				}
			}
			if n != 1 {
				t.Errorf("item %q must have exactly one of args, sub, info", it.en)
			}
			if it.pick != nil && len(it.args) == 0 {
				t.Errorf("item %q picks a value for no command", it.en)
			}
			if len(it.args) > 0 && it.args[0] != "help" {
				args := it.args
				if it.pick != nil {
					args = append(append([]string(nil), args...), "some-value")
				}
				res, _, err := DefaultRegistry.resolve(args)
				if err != nil {
					t.Errorf("item %q: aictl %s does not resolve: %v", it.en, strings.Join(args, " "), err)
					continue
				}
				if p := it.pick; p != nil && len(p.source) > 0 {
					if _, _, err := DefaultRegistry.resolve(p.source); err != nil {
						t.Errorf("item %q: picker source %v does not resolve", it.en, p.source)
					}
				}
				// Flags the menu passes must be declared by the command.
				fs := flag.NewFlagSet("t", flag.ContinueOnError)
				if res.cmd.Flags != nil {
					res.cmd.Flags(fs)
				}
				for _, a := range res.rest {
					if strings.HasPrefix(a, "-") {
						name := strings.TrimLeft(a, "-")
						if fs.Lookup(name) == nil {
							t.Errorf("item %q: %s has no flag %s", it.en, strings.Join(res.path, " "), a)
						}
					}
				}
			}
			walk(it.sub)
		}
	}
	walk(menuTree())
}

func TestParseKey(t *testing.T) {
	cases := map[string]menuKeyKind{
		"\x1b[A": keyUp, "\x1bOA": keyUp, "k": keyUp,
		"\x1b[B": keyDown, "j": keyDown,
		"\x1b[D": keyBack, "\x1b": keyBack, "\x7f": keyBack,
		"\x1b[C": keyEnter, "\r": keyEnter, "\n": keyEnter, " ": keyEnter,
		"\x1b[H": keyHome, "\x1b[F": keyEnd,
		"q": keyQuit, "\x03": keyQuit, "\x04": keyQuit, "": keyQuit,
		"x": keyOther, "\x1b[Z": keyOther,
	}
	for in, want := range cases {
		if got := parseKey([]byte(in)).kind; got != want {
			t.Errorf("parseKey(%q) = %v, want %v", in, got, want)
		}
	}
	if k := parseKey([]byte("7")); k.kind != keyDigit || k.digit != 7 {
		t.Errorf("digit: %+v", k)
	}
}

type fakeKeys struct{ keys []menuKey }

func (f *fakeKeys) ReadKey() (menuKey, error) {
	if len(f.keys) == 0 {
		return menuKey{}, errors.New("no more keys")
	}
	k := f.keys[0]
	f.keys = f.keys[1:]
	return k, nil
}

type ttyTerminal struct{}

func (ttyTerminal) Interactive() bool                          { return true }
func (ttyTerminal) ReadLine(context.Context) (string, error)   { return "", io.EOF }
func (ttyTerminal) ReadSecret(context.Context) (string, error) { return "", io.EOF }

// Run with no arguments opens the menu only for a person at a terminal; everything else is unchanged.
func TestRunNoArgsMenuGate(t *testing.T) {
	env := func(getenv map[string]string, tty bool, keys KeySource) Env {
		return Env{
			Getenv:      func(k string) string { return getenv[k] },
			StdoutIsTTY: tty, Terminal: ttyTerminal{}, Keys: keys,
		}
	}
	cases := []struct {
		name     string
		env      Env
		args     []string
		wantCode int
		menu     bool
	}{
		{"terminal opens the menu", env(nil, true, &fakeKeys{keys: []menuKey{key(keyQuit)}}), nil, ExitOK, true},
		{"no key source: command list", env(nil, true, nil), nil, ExitUsage, false},
		{"stdout piped: command list", env(nil, false, &fakeKeys{}), nil, ExitUsage, false},
		{"AICTL_PLAIN=1: command list", env(map[string]string{"AICTL_PLAIN": "1"}, true, &fakeKeys{}), nil, ExitUsage, false},
		{"TERM=dumb: command list", env(map[string]string{"TERM": "dumb"}, true, &fakeKeys{}), nil, ExitUsage, false},
		{"--json: usage error", env(nil, true, &fakeKeys{}), []string{"--json"}, ExitUsage, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			tc.env.Stdout, tc.env.Stderr = &stdout, &stderr
			code := Run(context.Background(), tc.env, NewRegistry(), tc.args)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if got := strings.Contains(stdout.String(), "What do you want to do?"); got != tc.menu {
				t.Fatalf("menu shown = %v, want %v; stdout %q", got, tc.menu, stdout.String())
			}
		})
	}
}

func TestMenuWorkloadPicker(t *testing.T) {
	h := &fakeHost{keys: []menuKey{digit(4), digit(1), key(keyDown), key(keyEnter)}, captures: map[string]capture{
		"status": {statusOK, 0},
		"workloads": {`{"workloads":[{"name":"coding","label":{"en":"Coding","es":"Programación"},"models":["m-one"],"active":true,"fits":true,"missing":[]},` +
			`{"name":"huge","label":{"en":"Huge","es":"Enorme"},"models":["m-big"],"active":false,"fits":false,"missing":[]},` +
			`{"name":"music","label":{"en":"Music","es":"Música"},"models":["m-two"],"active":false,"fits":true,"missing":[]}],"active":"coding"}`, 0},
	}}
	newTestMenu(h, LangEN).run()
	if len(h.runs) != 1 || strings.Join(h.runs[0], " ") != "workloads activate music" {
		t.Fatalf("runs = %v", h.runs)
	}
	frames := strings.Join(h.frames, "\n")
	if !strings.Contains(frames, "Coding (now)") || strings.Contains(frames, "Huge") {
		t.Errorf("the picker shows the wrong choices:\n%s", frames)
	}
}
