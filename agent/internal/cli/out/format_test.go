package out

import (
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

func TestSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1500, "1.5 kB"},
		{12_345_678, "12.3 MB"},
		{99_949_000, "99.9 MB"},
		{99_950_000, "100 MB"},
		{820_000_000, "820 MB"},
		{999_499_999, "999 MB"},
		{999_950_000, "1.0 GB"}, // rounds up to 1000 MB: shown as 1.0 GB
		{5_900_000_000, "5.9 GB"},
		{19_000_000_000, "19.0 GB"},
		{450_000_000_000, "450 GB"},
		{1_200_000_000_000, "1.2 TB"},
		{-1500, "-1.5 kB"},
	}
	for _, tc := range tests {
		if got := Size(tc.bytes); got != tc.want {
			t.Errorf("Size(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestGPUMemory(t *testing.T) {
	if got := GBFromMiB(8192); got != "8.0 GB" {
		t.Errorf("GBFromMiB(8192) = %q", got)
	}
	if got := GBFromMiB(6791); got != "6.6 GB" {
		t.Errorf("GBFromMiB(6791) = %q", got)
	}
	if got := GB(19_000_000_000); got != "19.0 GB" {
		t.Errorf("GB = %q", got)
	}
	if got := GB(820_000_000); got != "0.8 GB" {
		t.Errorf("GB always shows gigabytes: %q", got)
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0.0 s"},
		{0, "0.0 s"},
		{7100 * time.Millisecond, "7.1 s"},
		{59 * time.Second, "59.0 s"},
		{59960 * time.Millisecond, "1 min"}, // rounds up to a minute
		{60 * time.Second, "1 min"},
		{200 * time.Second, "3 min 20 s"},
		{59*time.Minute + 59*time.Second, "59 min 59 s"},
		{3599600 * time.Millisecond, "1 h"},
		{2*time.Hour + 5*time.Minute, "2 h 5 min"},
		{2 * time.Hour, "2 h"},
		{90 * time.Minute, "1 h 30 min"},
		{26*time.Hour + 20*time.Second, "26 h"},
	}
	for _, tc := range tests {
		if got := Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRelative(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		offset time.Duration
		en, es string
	}{
		{"now", 0, "just now", "ahora mismo"},
		{"4 s ago", -4 * time.Second, "just now", "ahora mismo"},
		{"soon", 3 * time.Second, "in a moment", "en un momento"},
		{"5 s ago", -5 * time.Second, "5 seconds ago", "hace 5 segundos"},
		{"1 minute ago", -90 * time.Second, "1 minute ago", "hace 1 minuto"},
		{"in 3 minutes", 3*time.Minute + 20*time.Second, "in 3 minutes", "en 3 minutos"},
		{"12 hours ago", -12*time.Hour - 30*time.Minute, "12 hours ago", "hace 12 horas"},
		{"1 hour ago", -time.Hour, "1 hour ago", "hace 1 hora"},
		{"2 days ago", -50 * time.Hour, "2 days ago", "hace 2 días"},
		{"in 1 day", 25 * time.Hour, "in 1 day", "en 1 día"},
		{"3 months ago", -100 * 24 * time.Hour, "3 months ago", "hace 3 meses"},
		{"2 years ago", -800 * 24 * time.Hour, "2 years ago", "hace 2 años"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := now.Add(tc.offset)
			if got := Relative(at, now, i18n.EN); got != tc.en {
				t.Errorf("en = %q, want %q", got, tc.en)
			}
			if got := Relative(at, now, i18n.ES); got != tc.es {
				t.Errorf("es = %q, want %q", got, tc.es)
			}
		})
	}
}

func TestAbsolute(t *testing.T) {
	loc := time.FixedZone("x", -6*3600)
	at := time.Date(2026, 11, 2, 9, 4, 5, 0, loc)
	if got := Absolute(at); got != "2026-11-02T15:04:05Z" {
		t.Errorf("Absolute = %q", got)
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		width       int
		first, rest string
		want        []string
	}{
		{"fits", "A short sentence.", 80, "", "", []string{"A short sentence."}},
		{"breaks at spaces", "one two three four five", 12, "", "", []string{"one two", "three four", "five"}},
		{"prefixes count", "one two three", 12, "> ", "  ", []string{"> one two", "  three"}},
		{"hanging indent", "alpha beta gamma delta", 14, "Label  ", "       ", []string{"Label  alpha", "       beta", "       gamma", "       delta"}},
		{"command stays whole", "Run `aictl restart llama` now.", 24, "", "",
			[]string{"Run", "`aictl restart llama`", "now."}},
		{"command too long is split at spaces", "Run `aictl logs --unit llama` now", 12, "", "",
			[]string{"Run `aictl", "logs --unit", "llama` now"}},
		{"long word kept whole", "see /very/long/path/that/does/not/fit ok", 10, "", "",
			[]string{"see", "/very/long/path/that/does/not/fit", "ok"}},
		{"unpaired backtick is plain", "a ` b c", 5, "", "", []string{"a ` b", "c"}},
		{"white space collapses", "a   b\n c", 80, "", "", []string{"a b c"}},
		{"empty", "", 80, "  ", "  ", []string{""}},
		{"accents count as one column", "áéíóú áéíóú", 11, "", "", []string{"áéíóú áéíóú"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Wrap(tc.text, tc.width, tc.first, tc.rest)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("Wrap = %q, want %q", got, tc.want)
			}
		})
	}
}

// No wrapped line of prose is wider than the width, except one unbreakable word alone on its line.
func TestWrapNeverExceedsWidth(t *testing.T) {
	text := "Start the service with `sudo systemctl start localai-agent`. If it keeps stopping, run " +
		"`journalctl -u localai-agent -n 50` and read the last lines, then report the request id."
	for _, width := range []int{30, 40, 60, 80} {
		for _, l := range Wrap(text, width, "  → Fix: ", "         ") {
			if DisplayWidth(l) > width {
				t.Errorf("width %d: line %q is %d columns", width, l, DisplayWidth(l))
			}
		}
	}
}

func TestDisplayWidthAndTruncate(t *testing.T) {
	if got := DisplayWidth("\x1b[32m✓ ok\x1b[0m"); got != 4 {
		t.Errorf("DisplayWidth with colour = %d, want 4", got)
	}
	if got := Truncate("abcdefghij", 6, "…"); got != "abcde…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("abcdefghij", 6, "..."); got != "abc..." {
		t.Errorf("Truncate ascii = %q", got)
	}
	if got := Truncate("short", 6, "…"); got != "short" {
		t.Errorf("Truncate fits = %q", got)
	}
	coloured := "\x1b[2mabcdefghij\x1b[0m"
	if got := Truncate(coloured, 4, "…"); got != coloured {
		t.Errorf("a coloured cell is never cut: %q", got)
	}
}
