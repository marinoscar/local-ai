package progress_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d       time.Duration
		precise bool
		want    string
	}{
		{0, false, "0 s"},
		{5 * time.Second, false, "5 s"},
		{5900 * time.Millisecond, false, "5 s"},
		{7100 * time.Millisecond, true, "7.1 s"},
		{59 * time.Second, true, "59.0 s"},
		{time.Minute, false, "1 min"},
		{200 * time.Second, false, "3 min 20 s"},
		{200 * time.Second, true, "3 min 20 s"},
		{time.Hour, false, "1 h"},
		{65 * time.Minute, false, "1 h 5 min"},
		{-time.Second, false, "0 s"},
	}
	for _, tt := range tests {
		if got := progress.FormatElapsed(tt.d, tt.precise); got != tt.want {
			t.Errorf("FormatElapsed(%v, %v) = %q, want %q", tt.d, tt.precise, got, tt.want)
		}
	}
}

func newText(o progress.RenderOptions) (*progress.TextRenderer, *syncBuf, *cfake.Clock) {
	clock := cfake.NewClock(epoch)
	out := &syncBuf{}
	o.Out, o.Clock = out, clock
	return progress.NewRenderer(o), out, clock
}

// The live line has the spinner, the label, the bar, the percentage and the elapsed time.
func TestLiveLineLayout(t *testing.T) {
	tests := []struct {
		name string
		ro   progress.RenderOptions
		u    progress.Update
		want string
	}{
		{"utf8 with bar", progress.RenderOptions{TTY: true, UTF8: true},
			progress.Update{Label: "Loading Qwen 3.5 9B", Percent: 70}, "\r⠋ Loading Qwen 3.5 9B  ███████░░░   70%  (0 s)\x1b[K"},
		{"ascii with bar", progress.RenderOptions{TTY: true},
			progress.Update{Label: "Loading Qwen 3.5 9B", Percent: 100}, "\r... Loading Qwen 3.5 9B  ##########  100%  (0 s)\x1b[K"},
		{"no bar", progress.RenderOptions{TTY: true, UTF8: true},
			progress.Update{Label: "Waiting", Percent: progress.NoBar}, "\r⠋ Waiting  (0 s)\x1b[K"},
		{"percent over 100 is capped", progress.RenderOptions{TTY: true, UTF8: true},
			progress.Update{Label: "X", Percent: 250}, "\r⠋ X  ██████████  100%  (0 s)\x1b[K"},
		{"colour", progress.RenderOptions{TTY: true, UTF8: true, Color: true},
			progress.Update{Label: "X", Percent: progress.NoBar}, "\r\x1b[36m⠋\x1b[0m X\x1b[2m  (0 s)\x1b[0m\x1b[K"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, out, _ := newText(tt.ro)
			r.Update(tt.u)
			got := strings.TrimPrefix(out.String(), "\x1b[?25l")
			if got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// A live line never wraps: it is cut to 79 columns with an ellipsis.
func TestLiveLineIsCutToTheTerminalWidth(t *testing.T) {
	r, out, _ := newText(progress.RenderOptions{TTY: true, UTF8: true})
	r.Update(progress.Update{Label: strings.Repeat("a long label ", 12), Percent: 42})
	body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(out.String(), "\x1b[?25l"), "\r"), "\x1b[K")
	if n := utf8.RuneCountInString(body); n > 79 {
		t.Fatalf("line is %d columns: %q", n, body)
	}
	if !strings.Contains(body, "…") || !strings.HasSuffix(body, "42%  (0 s)") {
		t.Fatalf("line = %q", body)
	}
}

// Redraws are throttled to ten a second; a state change draws at once.
func TestRedrawThrottle(t *testing.T) {
	r, out, clock := newText(progress.RenderOptions{TTY: true, UTF8: true})
	draws := func() int { return strings.Count(out.String(), "\x1b[K") }

	r.Update(progress.Update{Label: "Loading", Percent: 1})
	for p := 2.0; p < 50; p++ { // many updates within the same 100 ms
		r.Update(progress.Update{Label: "Loading", Percent: p})
	}
	if n := draws(); n != 1 {
		t.Fatalf("draws = %d, want 1 inside the throttle window", n)
	}
	clock.Advance(99 * time.Millisecond)
	r.Tick()
	if n := draws(); n != 1 {
		t.Fatalf("draws = %d at 99 ms", n)
	}
	clock.Advance(time.Millisecond)
	r.Tick()
	if n := draws(); n != 2 {
		t.Fatalf("draws = %d at 100 ms, want 2", n)
	}
	// A new state is never throttled: the previous one is committed on its own line.
	r.Update(progress.Update{Label: "Verifying", Percent: progress.NoBar})
	got := out.String()
	if !strings.Contains(got, "\r\x1b[K… Loading\n") || !strings.HasSuffix(got, "⠹ Verifying  (0 s)\x1b[K") && !strings.Contains(got, "Verifying") {
		t.Fatalf("transcript = %q", visible(got))
	}
}

// Updates with the same Key but a different Label are the same state; a new Key is a new line off a terminal.
func TestKeyDecidesWhatIsAStateChange(t *testing.T) {
	r, out, _ := newText(progress.RenderOptions{UTF8: true})
	r.Update(progress.Update{Key: "sync", Label: "Syncing a.gguf", Percent: 10})
	r.Update(progress.Update{Key: "sync", Label: "Syncing b.gguf", Percent: 20})
	r.Update(progress.Update{Key: "verify", Label: "Verifying", Percent: progress.NoBar})
	r.Update(progress.Update{Key: "verify", Label: "Verifying", Percent: 50})
	if got, want := out.String(), "… Syncing a.gguf\n… Verifying\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// A notice goes on its own line; on a terminal the live line is redrawn under it.
func TestNoticeOnATerminal(t *testing.T) {
	r, out, _ := newText(progress.RenderOptions{TTY: true, UTF8: true})
	r.Update(progress.Update{Label: "Loading", Percent: progress.NoBar})
	r.Notice(progress.Line{Sev: progress.SevInfo, Text: "Reconnected to the agent."})
	got := out.String()
	if !strings.Contains(got, "\r\x1b[Ki Reconnected to the agent.\n\r") || !strings.HasSuffix(got, "Loading  (0 s)\x1b[K") {
		t.Fatalf("transcript = %q", visible(got))
	}
}

// Notices and finals honour severity symbols, in both symbol sets.
func TestFinishSymbols(t *testing.T) {
	tests := []struct {
		sev   progress.Severity
		utf8  bool
		want  string
		color string
	}{
		{progress.SevOK, true, "✓ done\n", "\x1b[32m✓\x1b[0m done\n"},
		{progress.SevError, true, "✗ done\n", "\x1b[31m✗\x1b[0m done\n"},
		{progress.SevWarning, true, "! done\n", "\x1b[33m!\x1b[0m done\n"},
		{progress.SevInfo, true, "i done\n", "\x1b[34mi\x1b[0m done\n"},
		{progress.SevOK, false, "[ok] done\n", ""},
		{progress.SevError, false, "[x] done\n", ""},
		{progress.SevWarning, false, "[!] done\n", ""},
		{progress.SevInfo, false, "[i] done\n", ""},
	}
	for _, tt := range tests {
		r, out, _ := newText(progress.RenderOptions{UTF8: tt.utf8})
		r.Finish(&progress.Line{Sev: tt.sev, Text: "done"})
		if got := out.String(); got != tt.want {
			t.Errorf("sev %d utf8=%v: %q, want %q", tt.sev, tt.utf8, got, tt.want)
		}
		if tt.color != "" {
			rc, outc, _ := newText(progress.RenderOptions{UTF8: true, Color: true})
			rc.Finish(&progress.Line{Sev: tt.sev, Text: "done"})
			if got := outc.String(); got != tt.color {
				t.Errorf("sev %d coloured: %q, want %q", tt.sev, got, tt.color)
			}
		}
	}
}

// Clear and Finish are idempotent and restore the cursor exactly once.
func TestClearIsIdempotent(t *testing.T) {
	r, out, _ := newText(progress.RenderOptions{TTY: true, UTF8: true})
	r.Update(progress.Update{Label: "x", Percent: progress.NoBar})
	r.Clear()
	r.Clear()
	r.Finish(nil)
	if n := strings.Count(out.String(), "\x1b[?25h"); n != 1 {
		t.Fatalf("cursor restored %d times: %q", n, visible(out.String()))
	}
}
