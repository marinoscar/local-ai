package out

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

// DefaultWidth is the line width of aictl.md section 3.3 ("lines <= 80 columns").
const DefaultWidth = 80

var sizeUnits = []string{"B", "kB", "MB", "GB", "TB", "PB"}

// Size formats a byte count in decimal units with one decimal: "5.9 GB", "19.0 GB", "12.3 MB". From
// 100 of a unit up the decimal is dropped ("820 MB", "450 GB"), and a count under 1000 is "0 B" to
// "999 B". Storage sizes are decimal (kB = 1000 B), as on the model cards.
func Size(bytes int64) string {
	if bytes < 0 {
		return "-" + Size(-bytes)
	}
	if bytes < 1000 {
		return fmt.Sprintf("%d B", bytes)
	}
	v, i := float64(bytes), 0
	for v >= 1000 && i < len(sizeUnits)-1 {
		v /= 1000
		i++
	}
	s, r := formatMagnitude(v)
	if r >= 1000 && i < len(sizeUnits)-1 { // 999.9 kB rounds up to 1000: show 1.0 MB instead
		s, _ = formatMagnitude(r / 1000)
		i++
	}
	return s + " " + sizeUnits[i]
}

// formatMagnitude renders v with one decimal under 100 and none from 100, and returns the rounded
// value too.
func formatMagnitude(v float64) (string, float64) {
	if v >= 99.95 {
		r := math.Round(v)
		return fmt.Sprintf("%.0f", r), r
	}
	r := math.Round(v*10) / 10
	return fmt.Sprintf("%.1f", r), r
}

// GB formats a byte count as decimal gigabytes with one decimal, whatever its size: "8.0 GB".
func GB(bytes int64) string {
	return fmt.Sprintf("%.1f GB", float64(bytes)/1e9)
}

// GBFromMiB formats a memory size the agent reports in MiB (graphics memory, RAM) as gigabytes with
// one decimal, dividing by 1024 so that an "8 GB" graphics card reads "8.0 GB".
func GBFromMiB(mib int64) string {
	return fmt.Sprintf("%.1f GB", float64(mib)/1024)
}

// Duration formats a length of time: "7.1 s" under a minute, "3 min 20 s" under an hour (the seconds
// are left out when zero) and "2 h 5 min" from an hour up (rounded to the minute). Negative lengths
// count as zero.
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		tenths := math.Round(d.Seconds() * 10)
		if tenths < 600 {
			return fmt.Sprintf("%.1f s", tenths/10)
		}
	}
	if d < time.Hour {
		total := int64(math.Round(d.Seconds()))
		if total < 3600 {
			m, s := total/60, total%60
			if s == 0 {
				return fmt.Sprintf("%d min", m)
			}
			return fmt.Sprintf("%d min %d s", m, s)
		}
	}
	total := int64(math.Round(d.Minutes()))
	h, m := total/60, total%60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// Relative formats t relative to now in lang: "12 hours ago", "in 3 minutes", "just now". Amounts
// are rounded down. Under five seconds is "just now" (or "in a moment" for the future).
func Relative(t, now time.Time, lang i18n.Lang) string {
	d := t.Sub(now)
	future := d > 0
	abs := d
	if abs < 0 {
		abs = -abs
	}
	if abs < 5*time.Second {
		if future {
			return i18n.T(lang, i18n.KeySoon)
		}
		return i18n.T(lang, i18n.KeyJustNow)
	}
	var n int64
	var unit i18n.Key
	switch {
	case abs < time.Minute:
		n, unit = int64(abs/time.Second), i18n.KeyUnitSecond
	case abs < time.Hour:
		n, unit = int64(abs/time.Minute), i18n.KeyUnitMinute
	case abs < 24*time.Hour:
		n, unit = int64(abs/time.Hour), i18n.KeyUnitHour
	case abs < 30*24*time.Hour:
		n, unit = int64(abs/(24*time.Hour)), i18n.KeyUnitDay
	case abs < 365*24*time.Hour:
		n, unit = int64(abs/(30*24*time.Hour)), i18n.KeyUnitMonth
	default:
		n, unit = int64(abs/(365*24*time.Hour)), i18n.KeyUnitYear
	}
	pattern := i18n.KeyAgo
	if future {
		pattern = i18n.KeyIn
	}
	return i18n.T(lang, pattern, n, i18n.Plural(lang, unit, n))
}

// Absolute formats t as RFC 3339 in UTC, the form -v adds next to a relative time.
func Absolute(t time.Time) string { return t.UTC().Format(time.RFC3339) }

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// DisplayWidth is the number of columns s takes: its runes, with colour escapes not counted. The
// symbols and accents of aictl are all one column wide.
func DisplayWidth(s string) int {
	if strings.Contains(s, "\x1b") {
		s = ansiEscape.ReplaceAllString(s, "")
	}
	return utf8.RuneCountInString(s)
}

// Truncate shortens s to at most width columns, ending with ellipsis when it cuts. A string that
// carries colour escapes is returned whole (cutting inside an escape would break the terminal).
func Truncate(s string, width int, ellipsis string) string {
	if DisplayWidth(s) <= width || strings.Contains(s, "\x1b") {
		return s
	}
	keep := width - DisplayWidth(ellipsis)
	if keep < 0 {
		keep = 0
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:keep]), " ") + ellipsis
}

// token is one unbreakable piece of prose for Wrap.
type token struct {
	text  string
	width int
}

// Wrap breaks text into lines of at most width columns. The first line starts with first, every
// other with rest (the prefixes count toward the width). Words are split on white space, except
// that a `backticked command` stays on one line when it fits, so a user can copy it. A word wider
// than a whole line is kept whole on a line of its own (a path or an address is never cut). Text
// that is empty gives one line holding only the prefix, trimmed.
func Wrap(text string, width int, first, rest string) []string {
	if width <= 0 {
		width = DefaultWidth
	}
	avail := width - DisplayWidth(rest)
	if a := width - DisplayWidth(first); a < avail {
		avail = a
	}
	toks := tokenize(text, avail)
	lines := []string{}
	line := first
	empty := true
	for _, t := range toks {
		switch {
		case empty:
			line += t.text
			empty = false
		case DisplayWidth(line)+1+t.width <= width:
			line += " " + t.text
		default:
			lines = append(lines, strings.TrimRight(line, " "))
			line = rest + t.text
		}
	}
	return append(lines, strings.TrimRight(line, " "))
}

// tokenize splits text into words, keeping a backticked span (with any punctuation stuck to its
// end) as one token when it fits in avail columns. Backticks pair up in order; an unpaired one is
// an ordinary character.
func tokenize(text string, avail int) []token {
	rs := []rune(text)
	hold := make([]bool, len(rs)) // true: a space here does not end the word
	for a := 0; a < len(rs); a++ {
		if rs[a] != '`' {
			continue
		}
		b := a + 1
		for b < len(rs) && rs[b] != '`' {
			b++
		}
		if b == len(rs) {
			break
		}
		if b-a+1 <= avail {
			for k := a; k <= b; k++ {
				hold[k] = true
			}
		}
		a = b
	}
	var toks []token
	for i := 0; i < len(rs); {
		if isSpace(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && (!isSpace(rs[j]) || hold[j]) {
			j++
		}
		w := string(rs[i:j])
		toks = append(toks, token{w, DisplayWidth(w)})
		i = j
	}
	return toks
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }
