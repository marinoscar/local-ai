package out

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

// Options configure a Printer. Build them from the global flags and the environment (the cli
// package does); ColorEnabled and UTF8Enabled give the two decisions.
type Options struct {
	Stdout, Stderr io.Writer
	Lang           i18n.Lang
	// Color paints symbols and secondary text; UTF8 picks the symbol set.
	Color, UTF8 bool
	// Quiet hides everything but errors and essential results; Verbose adds details, request ids and
	// absolute times; JSON turns every human stdout method into a no-op (the JSON writer owns
	// stdout) and routes error problems to it.
	Quiet, Verbose, JSON bool
	// Width is the line width (default 80).
	Width int
	// Now is the clock for relative times (default time.Now); tests inject a fixed one.
	Now func() time.Time
}

// Problem is an error, warning or notice in the layout of aictl.md section 3.3: the fields of the
// agent's error envelope plus the request id and the wait time.
type Problem struct {
	Severity Severity
	// Code is a doctor code. It is printed in brackets, never translated, and may be empty for an
	// unexpected error.
	Code string
	// Message is one sentence that ends with a period; Fix is one sentence, commands in backticks.
	Message, Fix string
	Details      map[string]any
	RequestID    string
	// ShowRequestID prints the request id without -v (an answer aictl cannot read).
	ShowRequestID bool
	// RetryAfter is how long to wait before trying again (a rate-limited answer); zero is none.
	RetryAfter time.Duration
}

// Printer writes the human output of a command: results to stdout, warnings, errors and progress to
// stderr. It never prints secrets and never colours without Color. It is not safe for concurrent
// use by several goroutines writing the same stream.
type Printer struct {
	o    Options
	sym  Symbols
	json *JSON
}

// New returns a Printer. Missing writers discard, a zero Width is DefaultWidth, an empty Lang is
// English.
func New(o Options) *Printer {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	if o.Lang == "" {
		o.Lang = i18n.EN
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Printer{o: o, sym: SymbolsFor(o.UTF8), json: NewJSON(o.Stdout)}
}

// JSON returns the --json writer on stdout, shared by every part of the command.
func (p *Printer) JSON() *JSON { return p.json }

// Symbols returns the symbol set in use.
func (p *Printer) Symbols() Symbols { return p.sym }

// Lang returns the output language.
func (p *Printer) Lang() i18n.Lang { return p.o.Lang }

// Stdout and Stderr return the underlying streams for output the Printer has no method for.
func (p *Printer) Stdout() io.Writer { return p.o.Stdout }

// Stderr returns the error stream.
func (p *Printer) Stderr() io.Writer { return p.o.Stderr }

// paint wraps s in an ANSI colour when colour is on.
func (p *Printer) paint(code, s string) string {
	if !p.o.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + ansiReset
}

// Dim renders secondary text (ids, paths, times) dim; plain without colour.
func (p *Printer) Dim(s string) string { return p.paint(codeDim, s) }

// Cyan renders in-progress text; plain without colour.
func (p *Printer) Cyan(s string) string { return p.paint(codeCyan, s) }

func (p *Printer) stdoutLine(s string) {
	fmt.Fprintln(p.o.Stdout, s)
}

// wrapped writes msg after symbol sym (painted with code), wrapped to the width, continuation lines
// aligned under the text, to w.
func (p *Printer) wrapped(w io.Writer, sym, code, msg string) {
	for i, l := range Wrap(msg, p.o.Width, sym+" ", strings.Repeat(" ", DisplayWidth(sym)+1)) {
		if i == 0 {
			l = p.paint(code, sym) + l[len(sym):]
		}
		fmt.Fprintln(w, l)
	}
}

// OK prints a success line to stdout: "✓ message". Hidden by --quiet and --json.
func (p *Printer) OK(msg string) {
	if p.o.JSON || p.o.Quiet {
		return
	}
	p.wrapped(p.o.Stdout, p.sym.OK, codeGreen, msg)
}

// Info prints a neutral line to stdout: "i message". Hidden by --quiet and --json.
func (p *Printer) Info(msg string) {
	if p.o.JSON || p.o.Quiet {
		return
	}
	p.wrapped(p.o.Stdout, p.sym.Info, codeBlue, msg)
}

// Warn prints a warning line to stderr: "! message". Hidden by --quiet.
func (p *Printer) Warn(msg string) {
	if p.o.Quiet {
		return
	}
	p.wrapped(p.o.Stderr, p.sym.Warn, codeYellow, msg)
}

// Error prints an error line to stderr: "✗ message". With --json the error is the envelope on
// stdout instead (no code, no fix).
func (p *Printer) Error(msg string) {
	p.Problem(Problem{Severity: SevError, Message: msg})
}

// Critical is Error with a bold symbol, for a problem that stops the node from doing its job.
func (p *Printer) Critical(msg string) {
	p.Problem(Problem{Severity: SevCritical, Message: msg})
}

// Progress prints an in-progress line to stderr: "… message". Hidden by --quiet and --json.
func (p *Printer) Progress(msg string) {
	if p.o.JSON || p.o.Quiet {
		return
	}
	p.wrapped(p.o.Stderr, p.sym.Progress, codeCyan, msg)
}

// Result prints text to stdout as it is: the essential result of a command (an address, a key, a
// path). It is shown even with --quiet and hidden only by --json.
func (p *Printer) Result(s string) {
	if p.o.JSON {
		return
	}
	p.stdoutLine(s)
}

// Secondary prints dim text to stdout (ids, paths, times). Hidden by --quiet and --json.
func (p *Printer) Secondary(s string) {
	if p.o.JSON || p.o.Quiet {
		return
	}
	p.stdoutLine(p.Dim(s))
}

// Blank prints an empty line to stdout (between a summary and its problems). Hidden by --quiet and
// --json.
func (p *Printer) Blank() {
	if p.o.JSON || p.o.Quiet {
		return
	}
	p.stdoutLine("")
}

// When formats t for people: relative to now ("12 hours ago"); with -v the RFC 3339 time follows in
// parentheses.
func (p *Printer) When(t time.Time) string {
	s := Relative(t, p.o.Now(), p.o.Lang)
	if p.o.Verbose {
		s += " (" + Absolute(t) + ")"
	}
	return s
}

// PrintSecretWarning prints the warning that goes with a secret printed for the user, to stderr, so
// it never mixes into piped output. It is shown with --quiet (it is a safety message) and hidden by
// --json, where stderr stays empty.
func (p *Printer) PrintSecretWarning() {
	if p.o.JSON {
		return
	}
	p.wrapped(p.o.Stderr, p.sym.Warn, codeYellow, i18n.T(p.o.Lang, i18n.KeySecretWarning))
}

// Problem prints pr in the layout of aictl.md section 3.3 to stderr:
//
//	✗ message.  [CODE]
//	  → Fix: the fix, wrapped under itself.
//
// An error or critical problem with --json is written as the failure envelope on stdout and nothing
// goes to stderr. --quiet hides warnings and notices, never errors. -v adds the details object and
// the request id; a wait time adds "→ Wait: 4 min 12 s".
func (p *Printer) Problem(pr Problem) {
	sev := pr.Severity
	if sev == "" {
		sev = SevError
	}
	fatal := sev == SevError || sev == SevCritical
	if fatal && p.o.JSON {
		_ = p.json.Error(pr)
		return
	}
	if !fatal && p.o.Quiet {
		return
	}
	var sym, color string
	switch sev {
	case SevInfo:
		sym, color = p.sym.Info, codeBlue
	case SevWarning:
		sym, color = p.sym.Warn, codeYellow
	case SevCritical:
		sym, color = p.sym.Error, codeBoldRed
	default:
		sym, color = p.sym.Error, codeRed
	}
	w := p.o.Stderr
	lines := Wrap(pr.Message, p.o.Width, sym+" ", strings.Repeat(" ", DisplayWidth(sym)+1))
	last := len(lines) - 1
	if pr.Code != "" {
		tag := "[" + pr.Code + "]"
		if DisplayWidth(lines[last])+2+len(tag) > p.o.Width {
			lines = append(lines, strings.Repeat(" ", DisplayWidth(sym)+1)+tag)
			last++
		} else {
			lines[last] += "  " + tag
		}
	}
	for i, l := range lines {
		if i == 0 {
			l = p.paint(color, sym) + l[len(sym):]
		}
		if pr.Code != "" && i == last {
			tag := "[" + pr.Code + "]"
			l = l[:len(l)-len(tag)] + p.Dim(tag)
		}
		fmt.Fprintln(w, l)
	}
	arrow := p.sym.Arrow
	if pr.Fix != "" {
		first := "  " + arrow + " " + i18n.T(p.o.Lang, i18n.KeyFixLabel) + " "
		for _, l := range Wrap(pr.Fix, p.o.Width, first, strings.Repeat(" ", DisplayWidth(first))) {
			fmt.Fprintln(w, l)
		}
	}
	if pr.RetryAfter > 0 {
		fmt.Fprintf(w, "  %s %s %s\n", arrow, i18n.T(p.o.Lang, i18n.KeyWaitLabel), Duration(pr.RetryAfter))
	}
	if p.o.Verbose && len(pr.Details) > 0 {
		if b, err := json.Marshal(pr.Details); err == nil {
			fmt.Fprintln(w, p.Dim("  details: "+string(b)))
		}
	}
	if pr.RequestID != "" && (p.o.Verbose || pr.ShowRequestID) {
		fmt.Fprintln(w, p.Dim("  request: "+pr.RequestID))
	}
}
