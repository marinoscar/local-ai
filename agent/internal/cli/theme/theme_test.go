package theme

import "testing"

func TestDetectDepth(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		env  map[string]string
		want Depth
	}{
		{map[string]string{}, Basic},
		{map[string]string{"TERM": "xterm"}, Basic},
		{map[string]string{"TERM": "xterm-256color"}, Ansi256},
		{map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, TrueColor},
		{map[string]string{"COLORTERM": "24bit"}, TrueColor},
	} {
		if got := DetectDepth(env(tc.env)); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.env, got, tc.want)
		}
	}
	if DetectDepth(nil) != Basic {
		t.Error("nil getenv is Basic")
	}
}

// Basic keeps the codes aictl always printed (golden tests and plain terminals see no change); full
// colour is the Corona tokens.
func TestSGR(t *testing.T) {
	cases := map[string]string{
		SGR(OK, Basic):           "32",
		SGR(Critical, Basic):     "1;31",
		SGR(Progress, Basic):     "36",
		SGR(Selected, Basic):     "7",
		SGR(OK, TrueColor):       "38;2;74;222;128",
		SGR(Accent, TrueColor):   "1;38;2;242;181;68",
		SGR(Selected, TrueColor): "1;38;2;26;18;6;48;2;242;181;68",
		SGR(Warn, Ansi256):       "38;5;214",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if Paint(false, TrueColor, OK, "x") != "x" || Paint(true, TrueColor, OK, "") != "" {
		t.Error("Paint must leave text alone when off or empty")
	}
	if got := Paint(true, Basic, Error, "✗"); got != "\x1b[31m✗\x1b[0m" {
		t.Errorf("Paint = %q", got)
	}
}
