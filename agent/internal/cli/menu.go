package cli

// The full-screen menu of aictl (ADR-0009, issue #425; aictl.md section 3.7). `aictl` with no
// arguments, on an interactive terminal, opens it, so a person who has never used a terminal can
// operate the node by reading the screen and pressing the arrow keys and Enter.
//
// The menu holds no business logic. Every item runs an ordinary aictl command in this process
// (Run with the same registry), so the PIN flow, confirmations, progress bars, doctor codes and exit
// codes are exactly those of the typed command. Pickers read the command's own --json output (the
// stable machine contract, aictl.md section 3.5). After each command the menu shows the typed form
// ("aictl load qwen3.5-9b-q4km"), so the fast path is learned by using the slow one.
//
// Everything that touches the operating system (raw keys, running a command, the screen) is behind
// menuHost, so menu_test.go drives the menu with a scripted fake.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// menuKey is one key press, reduced to what the menu understands.
type menuKey struct {
	kind  menuKeyKind
	digit int // for keyDigit: 0..9
}

type menuKeyKind int

const (
	keyOther menuKeyKind = iota
	keyUp
	keyDown
	keyEnter
	keyBack // Esc, Left, Backspace
	keyQuit // q, Ctrl-C, Ctrl-D, end of input
	keyDigit
	keyHome
	keyEnd
)

// menuHost is the operating system as the menu sees it.
type menuHost interface {
	// ReadKey waits for one key. An error ends the menu (the terminal went away).
	ReadKey() (menuKey, error)
	// Run runs one aictl command visibly, with prompts, and returns its exit code.
	Run(args []string) int
	// Capture runs one aictl command with --json and no prompts and returns its stdout and exit code.
	Capture(args []string) ([]byte, int)
	// Pause waits until the person presses Enter.
	Pause()
	// Clear clears the screen and puts the cursor at the top.
	Clear()
	// Screen is where the menu draws.
	Screen() io.Writer
}

// menuItem is one line of a menu. Exactly one of args, pick, sub and info is set.
type menuItem struct {
	en, es string
	// args is the command to run, without global flags.
	args []string
	// pick chooses a value first; the command run is args + the chosen value.
	pick *menuPicker
	sub  []menuItem
	// info shows a text screen.
	info func(l Lang, sym out.Symbols) string
	// hintEN/hintES is a short note shown after the label, for example "(admin PIN)".
	hintEN, hintES string
}

func (it menuItem) label(l Lang) string {
	s := pick(l, it.en, it.es)
	if h := pick(l, it.hintEN, it.hintES); h != "" {
		s += "  " + h
	}
	if len(it.sub) > 0 || it.pick != nil {
		s += " ›"
	}
	return s
}

// menuPicker is a list of choices read from a command's --json output.
type menuPicker struct {
	// source is the read-only command whose --json output lists the choices.
	source []string
	// choices turns that output into choices; nil source means static choices from choices(nil).
	choices func(raw []byte, l Lang) []menuChoice
	// titleEN/ES is the question; emptyEN/ES is shown when there is nothing to choose.
	titleEN, titleES string
	emptyEN, emptyES string
}

type menuChoice struct {
	label string
	value string
}

// menu is one running menu session.
type menu struct {
	host menuHost
	lang Lang
	sym  out.Symbols
	// color says whether the selected line may be drawn in reverse video.
	color bool
	root  []menuItem
}

// menuFrame is one level of the menu stack.
type menuFrame struct {
	title  string
	items  []menuItem
	cursor int
}

// run is the main loop. It returns the exit code of the menu itself (0 when the person quits).
func (m *menu) run() int {
	stack := []menuFrame{{title: pick(m.lang, "What do you want to do?", "¿Qué quiere hacer?"), items: m.root}}
	header := m.header()
	for {
		f := &stack[len(stack)-1]
		m.host.Clear()
		m.draw(m.host.Screen(), header, f, len(stack) > 1)
		k, err := m.host.ReadKey()
		if err != nil {
			return ExitOK
		}
		switch k.kind {
		case keyUp:
			f.cursor = (f.cursor + len(f.items) - 1) % len(f.items)
		case keyDown:
			f.cursor = (f.cursor + 1) % len(f.items)
		case keyHome:
			f.cursor = 0
		case keyEnd:
			f.cursor = len(f.items) - 1
		case keyBack:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case keyQuit:
			m.host.Clear()
			return ExitOK
		case keyDigit:
			// 1..9 are the first nine items, 0 the tenth; a digit past the end does nothing.
			i := k.digit - 1
			if k.digit == 0 {
				i = 9
			}
			if i >= 0 && i < len(f.items) {
				f.cursor = i
				if next := m.choose(f.items[i]); next != nil {
					stack = append(stack, *next)
				} else {
					header = m.header()
				}
			}
		case keyEnter:
			if next := m.choose(f.items[f.cursor]); next != nil {
				stack = append(stack, *next)
			} else {
				header = m.header()
			}
		}
	}
}

// choose acts on an item. It returns the frame to push for a submenu, or nil when the item ran.
func (m *menu) choose(it menuItem) *menuFrame {
	switch {
	case len(it.sub) > 0:
		return &menuFrame{title: pick(m.lang, it.en, it.es), items: it.sub}
	case it.info != nil:
		m.host.Clear()
		fmt.Fprintln(m.host.Screen(), it.info(m.lang, m.sym))
		m.host.Pause()
	case it.pick != nil:
		value, ok := m.pickValue(it)
		if ok {
			m.runCommand(it, append(append([]string(nil), it.args...), value))
		}
	default:
		m.runCommand(it, it.args)
	}
	return nil
}

// runCommand runs args visibly and tells the person how it went and what to type next time.
func (m *menu) runCommand(it menuItem, args []string) {
	w := m.host.Screen()
	m.host.Clear()
	fmt.Fprintf(w, "%s %s\n", m.sym.Arrow, pick(m.lang, it.en, it.es))
	fmt.Fprintf(w, "  %s aictl %s\n\n", pick(m.lang, "Typed command:", "Comando escrito:"), strings.Join(args, " "))
	code := m.host.Run(args)
	fmt.Fprintln(w)
	switch code {
	case ExitOK:
		fmt.Fprintf(w, "%s %s\n", m.sym.OK, pick(m.lang, "Done.", "Listo."))
	case ExitWarnings:
		fmt.Fprintf(w, "%s %s\n", m.sym.Warn, pick(m.lang,
			"Finished, with warnings. Read the lines marked above; each says what to do.",
			"Terminó, con advertencias. Lea las líneas marcadas arriba; cada una dice qué hacer."))
	case ExitErrors:
		fmt.Fprintf(w, "%s %s\n", m.sym.Error, pick(m.lang,
			"Finished, and found problems. Each problem above has a LAI-… code and how to fix it.",
			"Terminó y encontró problemas. Cada problema de arriba tiene un código LAI-… y cómo arreglarlo."))
	case ExitInterrupted:
		fmt.Fprintf(w, "%s %s\n", m.sym.Info, pick(m.lang, "Stopped.", "Detenido."))
	default:
		fmt.Fprintf(w, "%s %s\n", m.sym.Error, pick(m.lang,
			fmt.Sprintf("It did not finish (exit code %d). The message above says why and what to do.", code),
			fmt.Sprintf("No terminó (código de salida %d). El mensaje de arriba dice por qué y qué hacer.", code)))
	}
	m.host.Pause()
}

// pickValue shows the choices of it.pick and returns the chosen value.
func (m *menu) pickValue(it menuItem) (string, bool) {
	p := it.pick
	var raw []byte
	if len(p.source) > 0 {
		var code int
		raw, code = m.host.Capture(p.source)
		if code != ExitOK {
			m.message(pick(m.lang,
				"The agent did not answer, so the list cannot be shown. Choose \"Find and fix problems\" in the main menu.",
				"El agente no respondió, así que no se puede mostrar la lista. Elija \"Encontrar y arreglar problemas\" en el menú principal."))
			return "", false
		}
	}
	choices := p.choices(raw, m.lang)
	if len(choices) == 0 {
		m.message(pick(m.lang, p.emptyEN, p.emptyES))
		return "", false
	}
	f := menuFrame{title: pick(m.lang, p.titleEN, p.titleES)}
	for _, c := range choices {
		f.items = append(f.items, menuItem{en: c.label, es: c.label})
	}
	for {
		m.host.Clear()
		m.draw(m.host.Screen(), nil, &f, true)
		k, err := m.host.ReadKey()
		if err != nil {
			return "", false
		}
		switch k.kind {
		case keyUp:
			f.cursor = (f.cursor + len(f.items) - 1) % len(f.items)
		case keyDown:
			f.cursor = (f.cursor + 1) % len(f.items)
		case keyHome:
			f.cursor = 0
		case keyEnd:
			f.cursor = len(f.items) - 1
		case keyBack, keyQuit:
			return "", false
		case keyDigit:
			i := k.digit - 1
			if k.digit == 0 {
				i = 9
			}
			if i >= 0 && i < len(choices) {
				return choices[i].value, true
			}
		case keyEnter:
			return choices[f.cursor].value, true
		}
	}
}

func (m *menu) message(s string) {
	m.host.Clear()
	fmt.Fprintln(m.host.Screen(), wrap(s, 78))
	m.host.Pause()
}

// header is the "is it OK?" block at the top of every main screen (ADR-0009 rule 3), read from
// `aictl status --json`. It never fails: when the agent does not answer it says so and what to do.
func (m *menu) header() []string {
	raw, code := m.host.Capture([]string{"status"})
	var st wireStatus
	if code != ExitOK || json.Unmarshal(raw, &st) != nil {
		return []string{
			"Local AI",
			m.sym.Error + " " + pick(m.lang,
				"The agent is not answering. Choose \"Find and fix problems\" (2).",
				"El agente no responde. Elija \"Encontrar y arreglar problemas\" (2)."),
		}
	}
	name := st.Node.DisplayName
	if name == "" {
		name = st.Node.ID
	}
	brain := pick(m.lang, "no model loaded", "ningún modelo cargado")
	if st.Models != nil {
		switch {
		case len(st.Models.Loaded) > 0:
			brain = st.Models.Loaded[0]
		case len(st.Models.Loading) > 0:
			brain = st.Models.Loading[0] + " " + pick(m.lang, "(loading)", "(cargando)")
		}
	}
	lines := []string{truncate(fmt.Sprintf("Local AI · %s · %s %s · %s %s", name,
		pick(m.lang, "mode", "modo"), st.Mode.Value, pick(m.lang, "model", "modelo"), brain), 78)}
	var problems []wireAlert
	for _, a := range st.Alerts {
		if a.Severity == string(out.SevError) || a.Severity == string(out.SevCritical) || a.Severity == string(out.SevWarning) {
			problems = append(problems, a)
		}
	}
	if len(problems) == 0 {
		lines = append(lines, m.sym.OK+" "+pick(m.lang, "Everything is working.", "Todo funciona."))
		return lines
	}
	first := problems[0]
	count := pick(m.lang,
		fmt.Sprintf("%d problem(s) need attention", len(problems)),
		fmt.Sprintf("%d problema(s) necesitan atención", len(problems)))
	lines = append(lines, truncate(fmt.Sprintf("%s %s: %s (%s)", m.sym.Error, count, first.Title, first.Code), 78))
	if first.Fix != "" {
		lines = append(lines, truncate("  "+pick(m.lang, "Fix: ", "Arreglo: ")+first.Fix, 78))
	}
	lines = append(lines, "  "+pick(m.lang, "More: choose \"Find and fix problems\" (2).", "Más: elija \"Encontrar y arreglar problemas\" (2)."))
	return lines
}

// draw renders one screen. It fits 80x24 with the longest menu (ten items).
func (m *menu) draw(w io.Writer, header []string, f *menuFrame, nested bool) {
	for _, h := range header {
		fmt.Fprintln(w, " "+truncate(h, 78))
	}
	if len(header) > 0 {
		fmt.Fprintln(w, " "+strings.Repeat("─", 60))
	}
	fmt.Fprintf(w, " %s\n\n", f.title)
	for i, it := range f.items {
		num := " "
		switch {
		case i < 9:
			num = fmt.Sprint(i + 1)
		case i == 9:
			num = "0"
		}
		line := fmt.Sprintf("%s %s  %s", marker(i == f.cursor), num, it.label(m.lang))
		if i == f.cursor && m.color {
			line = "\x1b[7m" + line + "\x1b[0m"
		}
		fmt.Fprintln(w, "  "+line)
	}
	fmt.Fprintln(w)
	if nested {
		fmt.Fprintln(w, " "+pick(m.lang,
			"↑↓ move · Enter choose · number = shortcut · Esc back · q quit",
			"↑↓ mover · Enter elegir · número = atajo · Esc volver · q salir"))
	} else {
		fmt.Fprintln(w, " "+pick(m.lang,
			"↑↓ move · Enter choose · number = shortcut · q quit",
			"↑↓ mover · Enter elegir · número = atajo · q salir"))
	}
}

func marker(on bool) string {
	if on {
		return "▶"
	}
	return " "
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// wrap breaks s into lines of at most n columns at spaces.
func wrap(s string, n int) string {
	var b strings.Builder
	col := 0
	for i, word := range strings.Fields(s) {
		if i > 0 {
			if col+1+len([]rune(word)) > n {
				b.WriteString("\n")
				col = 0
			} else {
				b.WriteString(" ")
				col++
			}
		}
		b.WriteString(word)
		col += len([]rune(word))
	}
	return b.String()
}
