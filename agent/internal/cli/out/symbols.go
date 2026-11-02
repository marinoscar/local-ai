// Package out is the output layer of aictl (docs/specs/aictl.md sections 3.3 and 3.5): symbols and
// colours with an ASCII fallback, the problem layout (message, code, fix), tables and key/value
// blocks, size, duration and relative-time formatting, 80-column wrapping, and the one-document
// --json writer.
//
// The package knows nothing about the agent, the transport or the command tree: it takes plain
// values (strings, a Problem) and two writers, so commands and the framework share one layout and
// the golden tests pin it.
package out

import "strings"

// Severity is how serious a problem is. It picks the symbol and the colour, and whether --quiet
// hides it. The values match the severities of the doctor registry.
type Severity string

// Severities.
const (
	SevInfo     Severity = "info"
	SevWarning  Severity = "warning"
	SevError    Severity = "error"
	SevCritical Severity = "critical"
)

// Symbols is one symbol set: UTF-8 or the ASCII fallback.
type Symbols struct {
	OK, Info, Warn, Error, Progress, Arrow string
	// Ellipsis ends a truncated table cell.
	Ellipsis string
	// Spinner is the frame sequence of the progress spinner on a TTY.
	Spinner []string
}

// UTF8Symbols and ASCIISymbols are the two sets of aictl.md section 3.3.
var (
	UTF8Symbols = Symbols{
		OK: "✓", Info: "i", Warn: "!", Error: "✗", Progress: "…", Arrow: "→", Ellipsis: "…",
		Spinner: strings.Split("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", ""),
	}
	ASCIISymbols = Symbols{
		OK: "[ok]", Info: "[i]", Warn: "[!]", Error: "[x]", Progress: "...", Arrow: "->", Ellipsis: "...",
		Spinner: []string{"|", "/", "-", "\\"},
	}
)

// SymbolsFor returns the UTF-8 set when utf8 is true, else the ASCII one.
func SymbolsFor(utf8 bool) Symbols {
	if utf8 {
		return UTF8Symbols
	}
	return ASCIISymbols
}

// UTF8Enabled reports whether the terminal can print UTF-8: the first of LC_ALL, LC_CTYPE and LANG
// that is set decides, and it must name UTF-8 (or UTF8). With none set the answer is no, so a bare
// recovery shell gets the ASCII symbols.
func UTF8Enabled(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := strings.ToUpper(getenv(k)); v != "" {
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return false
}

// ColorEnabled is the colour decision of aictl.md section 3.1: on only when stdout is a terminal and
// none of --no-color, NO_COLOR (set and not empty), TERM=dumb and --json applies. The symbols are
// printed either way, so meaning never depends on colour.
func ColorEnabled(stdoutIsTTY, noColorFlag, jsonMode bool, getenv func(string) string) bool {
	if !stdoutIsTTY || noColorFlag || jsonMode {
		return false
	}
	if getenv != nil && (getenv("NO_COLOR") != "" || getenv("TERM") == "dumb") {
		return false
	}
	return true
}

// ANSI select-graphic-rendition codes of the colour table.
const (
	ansiReset  = "\x1b[0m"
	codeGreen  = "32"
	codeBlue   = "34"
	codeYellow = "33"
	codeRed    = "31"
	codeCyan   = "36"
	codeDim    = "2"
	// codeBoldRed is the critical symbol: red and bold.
	codeBoldRed = "1;31"
)
