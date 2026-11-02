package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Severity is the kind of a Line, and picks its symbol and colour (aictl.md section 3.3).
type Severity int

// Severities, as in the symbol table of aictl.md section 3.3.
const (
	SevOK Severity = iota
	SevInfo
	SevWarning
	SevError
)

// Line is one finished line: a notice, or the line that replaces the live progress line.
type Line struct {
	Sev  Severity
	Text string
}

// Update is what the live line shows.
type Update struct {
	// Label is the human text, for example "Loading Qwen 3.5 9B".
	Label string
	// Key identifies the state. A new Key is a state change: on a terminal the previous line stays
	// on screen, off a terminal a new line is printed. The empty Key means "the Label". Updates
	// with the same Key only redraw (a progress percentage, for instance).
	Key string
	// Percent is 0 to 100 for a bar and a percentage; a negative value means "no bar".
	Percent float64
}

func (u Update) key() string {
	if u.Key != "" {
		return u.Key
	}
	return u.Label
}

// NoBar is the Percent of an update without a bar.
const NoBar = -1

// Renderer is what the Waiter draws on. TextRenderer is the real one; tests may pass their own.
// The Waiter calls it from one goroutine.
type Renderer interface {
	// Update sets what the live line shows (it may draw at once or on the next Tick).
	Update(u Update)
	// Notice prints a line of its own (the connection to the agent was lost, ...), then the live
	// line carries on below it.
	Notice(l Line)
	// Tick redraws the live line so the spinner and the elapsed time move; the Waiter calls it
	// about ten times a second.
	Tick()
	// Clear removes the live line and restores the terminal (the cursor). It is idempotent.
	Clear()
	// Finish clears the live line and prints final, when there is one.
	Finish(final *Line)
	// Interrupted prints the "Still running in the background" line after Ctrl-C.
	Interrupted()
}

// RenderOptions configure NewRenderer.
type RenderOptions struct {
	// Out receives everything (progress goes to stderr, aictl.md section 3.3).
	Out io.Writer
	// TTY draws one updating line with carriage returns; false prints one line per state change
	// and never emits "\r" or an escape sequence.
	TTY bool
	// Silent draws no progress and no notices (--json, --quiet). Final and Interrupted lines are
	// still printed unless JSON is set.
	Silent bool
	// JSON prints nothing at all: stdout belongs to the final document and stderr stays empty.
	JSON bool
	// Color adds ANSI colour (only meaningful on a terminal); UTF8 picks the symbols and the bar
	// characters, else the ASCII set of aictl.md section 3.3.
	Color bool
	UTF8  bool
	// Lang is "en" or "es".
	Lang string
	// Clock is the time source of the elapsed time and of the redraw throttle; default the real one.
	Clock config.Clock
	// MaxWidth is the width a live line is cut to (default 80, the style guide's limit).
	MaxWidth int
}

// RedrawInterval is the redraw throttle of a terminal line: at most ten redraws a second.
const RedrawInterval = 100 * time.Millisecond

// TextRenderer implements Renderer with the output rules of aictl.md section 3.3.
type TextRenderer struct {
	o     RenderOptions
	start time.Time

	mu       sync.Mutex
	cur      Update
	has      bool
	live     bool // a live line is on screen (TTY)
	hidden   bool // the cursor is hidden
	lastDraw time.Time
	frame    int
}

// NewRenderer returns a TextRenderer; its elapsed time starts now.
func NewRenderer(o RenderOptions) *TextRenderer {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Clock == nil {
		o.Clock = config.RealClock{}
	}
	if o.MaxWidth <= 0 {
		o.MaxWidth = 80
	}
	if o.Lang != "es" {
		o.Lang = "en"
	}
	return &TextRenderer{o: o, start: o.Clock.Now()}
}

// Update implements Renderer.
func (r *TextRenderer) Update(u Update) {
	if r.o.JSON || r.o.Silent {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !r.has || u.key() != r.cur.key()
	prev := r.cur
	hadPrev := r.has
	r.cur, r.has = u, true
	if !r.o.TTY {
		if changed {
			r.printLine(Line{Sev: SevInfo, Text: u.Label}, true)
		}
		return
	}
	if changed && hadPrev {
		// Keep the finished state on screen and start a new live line under it.
		r.clearLive()
		r.printLine(Line{Sev: SevInfo, Text: prev.Label}, true)
		r.draw(true)
		return
	}
	r.draw(changed)
}

// Notice implements Renderer.
func (r *TextRenderer) Notice(l Line) {
	if r.o.JSON || r.o.Silent {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLive()
	r.printLine(l, false)
	if r.o.TTY && r.has {
		r.draw(true)
	}
}

// Tick implements Renderer.
func (r *TextRenderer) Tick() {
	if !r.o.TTY || r.o.JSON || r.o.Silent {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.draw(false)
}

// Clear implements Renderer.
func (r *TextRenderer) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLive()
	r.showCursor()
}

// Finish implements Renderer.
func (r *TextRenderer) Finish(final *Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLive()
	r.showCursor()
	if final != nil && !r.o.JSON {
		r.printLine(*final, false)
	}
}

// Interrupted implements Renderer.
func (r *TextRenderer) Interrupted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearLive()
	r.showCursor()
	if r.o.JSON {
		return
	}
	r.printLine(Line{Sev: SevInfo, Text: r.msg(msgStillRunning)}, false)
}

// draw paints the live line, at most once per RedrawInterval unless force is set.
func (r *TextRenderer) draw(force bool) {
	if !r.has || r.o.Out == nil {
		return
	}
	now := r.o.Clock.Now()
	if !force && r.live && now.Sub(r.lastDraw) < RedrawInterval {
		return
	}
	r.lastDraw = now
	if !r.hidden {
		io.WriteString(r.o.Out, "\x1b[?25l")
		r.hidden = true
	}
	fmt.Fprintf(r.o.Out, "\r%s\x1b[K", r.liveLine(now))
	r.live = true
	r.frame++
}

func (r *TextRenderer) clearLive() {
	if r.live {
		io.WriteString(r.o.Out, "\r\x1b[K")
		r.live = false
	}
}

func (r *TextRenderer) showCursor() {
	if r.hidden {
		io.WriteString(r.o.Out, "\x1b[?25h")
		r.hidden = false
	}
}

// liveLine is "<spinner> <label>  <bar>  <pct>%  (<elapsed>)", cut to MaxWidth-1 columns so it
// never wraps (a wrapped line cannot be redrawn).
func (r *TextRenderer) liveLine(now time.Time) string {
	var tail strings.Builder
	if r.cur.Percent >= 0 {
		p := r.cur.Percent
		if p > 100 {
			p = 100
		}
		fmt.Fprintf(&tail, "  %s  %3.0f%%", r.bar(p), p)
	}
	elapsed := "  (" + FormatElapsed(now.Sub(r.start), false) + ")"
	sym := r.spinner()
	room := r.o.MaxWidth - 1 - utf8.RuneCountInString(sym) - 1 - utf8.RuneCountInString(tail.String()) - utf8.RuneCountInString(elapsed)
	label := truncate(r.cur.Label, room, r.o.UTF8)
	return r.paint(36, sym) + " " + label + tail.String() + r.paint(2, elapsed)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (r *TextRenderer) spinner() string {
	if !r.o.UTF8 {
		return "..."
	}
	return spinnerFrames[r.frame%len(spinnerFrames)]
}

// bar draws ten cells: "███████░░░" (UTF-8) or "#######---" (ASCII).
func (r *TextRenderer) bar(pct float64) string {
	const cells = 10
	n := int(pct/100*cells + 0.5)
	if n > cells {
		n = cells
	}
	full, empty := "█", "░"
	if !r.o.UTF8 {
		full, empty = "#", "-"
	}
	return strings.Repeat(full, n) + strings.Repeat(empty, cells-n)
}

// printLine prints one finished line. A progress line (state) wears the "in progress" symbol; the
// others wear the symbol of their severity.
func (r *TextRenderer) printLine(l Line, state bool) {
	sym, code := r.symbol(l.Sev)
	if state {
		sym, code = r.ellipsis(), 36
	}
	fmt.Fprintf(r.o.Out, "%s %s\n", r.paint(code, sym), l.Text)
}

func (r *TextRenderer) ellipsis() string {
	if r.o.UTF8 {
		return "…"
	}
	return "..."
}

func (r *TextRenderer) symbol(s Severity) (string, int) {
	u := r.o.UTF8
	pick := func(a, b string) string {
		if u {
			return a
		}
		return b
	}
	switch s {
	case SevOK:
		return pick("✓", "[ok]"), 32
	case SevWarning:
		return pick("!", "[!]"), 33
	case SevError:
		return pick("✗", "[x]"), 31
	}
	return pick("i", "[i]"), 34
}

// paint wraps s in an ANSI colour when colour is on.
func (r *TextRenderer) paint(code int, s string) string {
	if !r.o.Color || s == "" {
		return s
	}
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", code, s)
}

// truncate cuts s to at most max columns (runes), ending in an ellipsis when it had to cut.
func truncate(s string, max int, uni bool) string {
	if max < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	mark := "..."
	if uni {
		mark = "…"
	}
	keep := max - utf8.RuneCountInString(mark)
	if keep < 0 {
		keep = 0
	}
	rs := []rune(s)
	return string(rs[:keep]) + mark
}

// FormatElapsed renders a duration as the style guide wants (aictl.md section 3.3): "7.1 s",
// "3 min 20 s", "1 h 5 min". With precise false a duration under a minute is whole seconds
// ("5 s"), as in the live line.
func FormatElapsed(d time.Duration, precise bool) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		if precise {
			return fmt.Sprintf("%.1f s", d.Seconds())
		}
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		m, s := int(d.Minutes()), int(d.Seconds())%60
		if s == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %d s", m, s)
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// Message ids of the texts this package prints itself.
const (
	msgStillRunning = iota
	msgLost
	msgBack
)

var messages = [...][2]string{
	msgStillRunning: {
		"Still running in the background. Check with `aictl status`.",
		"Sigue en ejecución en segundo plano. Revise con `aictl status`.",
	},
	msgLost: {
		"Lost the connection to the agent. Waiting for it to come back.",
		"Se perdió la conexión con el agente. Esperando a que vuelva.",
	},
	msgBack: {
		"Reconnected to the agent.",
		"Reconectado con el agente.",
	},
}

func (r *TextRenderer) msg(id int) string { return text(r.o.Lang, id) }

func text(lang string, id int) string {
	if lang == "es" {
		return messages[id][1]
	}
	return messages[id][0]
}
