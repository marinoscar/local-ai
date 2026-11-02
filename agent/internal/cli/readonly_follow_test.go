package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
)

// logs flags and -f, status --watch, and the small pure helpers of the read-only commands.

func TestLogsFlagsAndQuery(t *testing.T) {
	utcLogs(t)
	oldNow := logsNow
	logsNow = func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	defer func() { logsNow = oldNow }()

	f := newROFix(t, fake.Options{})
	f.serve("/logs", "logs")
	last := func() string { rs := f.ag.Requests(); return rs[len(rs)-1].Path }

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"logs"}, "/logs?lines=100&unit=agent"},
		{[]string{"logs", "--unit", "llama", "-n", "50"}, "/logs?lines=50&unit=llama"},
		{[]string{"logs", "--since", "15m"}, "/logs?lines=100&since=2026-11-02T11%3A45%3A00Z&unit=agent"},
		{[]string{"logs", "--since", "2h", "--unit", "sync"}, "/logs?lines=100&since=2026-11-02T10%3A00%3A00Z&unit=sync"},
		{[]string{"logs", "--since", "2026-11-02T08:00:00+02:00"}, "/logs?lines=100&since=2026-11-02T06%3A00%3A00Z&unit=agent"},
		{[]string{"logs", "-n", "2000", "--unit", "drill"}, "/logs?lines=2000&unit=drill"},
	}
	for _, tc := range cases {
		if r := f.run(tc.args...); r.code != 0 {
			t.Fatalf("%v: exit %d %s", tc.args, r.code, r.errOut)
		}
		if got := last(); got != tc.want {
			t.Errorf("%v: request %q, want %q", tc.args, got, tc.want)
		}
	}
	before := len(f.ag.Requests())
	for _, args := range [][]string{
		{"logs", "--unit", "kernel"}, {"logs", "-n", "0"}, {"logs", "-n", "2001"}, {"logs", "-n", "-3"},
		{"logs", "--since", "yesterday"}, {"logs", "--since", "-5m"}, {"logs", "-n", "many"},
	} {
		r := f.run(args...)
		if r.code != ExitUsage || !strings.Contains(r.errOut, "[LAI-CLI-005]") || r.out != "" {
			t.Errorf("%v: exit %d out %q err %q", args, r.code, r.out, r.errOut)
		}
	}
	if len(f.ag.Requests()) != before {
		t.Error("a usage error must not reach the agent")
	}
}

// followBatches serves /logs with one batch per request and cancels ctx after the last one.
func followBatches(t *testing.T, f *roFix, cancel context.CancelFunc, batches [][]wireLogLine) *[]string {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	f.ag.Handle("GET", "/logs", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := len(paths)
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		var lines []wireLogLine
		if n < len(batches) {
			lines = batches[n]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"unit": "x", "lines": lines, "truncated": false})
		if n+1 >= len(batches) {
			cancel()
		}
	})
	return &paths
}

func TestLogsFollowDeduplicatesAndStopsOnInterrupt(t *testing.T) {
	utcLogs(t)
	old := logsFollowInterval
	logsFollowInterval = time.Millisecond
	defer func() { logsFollowInterval = old }()

	ln := func(at, msg string) wireLogLine { return wireLogLine{At: at, Priority: 6, Message: msg} }
	a, b := ln("2026-11-02T08:00:05Z", "A"), ln("2026-11-02T08:00:12Z", "B")
	c, d := ln("2026-11-02T08:00:12Z", "C"), ln("2026-11-02T08:00:14Z", "D")
	twin := ln("2026-11-02T08:00:14Z", "D") // a genuinely new, identical line in the same second
	batches := [][]wireLogLine{
		{a, b},       // the opening request
		{b, c, d},    // since is inclusive: B comes back and is skipped; C shares B's second and is new
		{d, twin, d}, // D is skipped once; the second and third identical D are new
		{},           // nothing new
	}

	t.Run("human", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		paths := followBatches(t, f, cancel, batches)
		r := f.exec(ctx, runOpts{}, "logs", "-f")
		want := "08:00:05  A\n08:00:12  B\n08:00:12  C\n08:00:14  D\n08:00:14  D\n08:00:14  D\n"
		if r.code != 0 || r.out != want || r.errOut != "" {
			t.Fatalf("exit %d\nout %q\nwant %q\nerr %q", r.code, r.out, want, r.errOut)
		}
		wantPaths := []string{
			"/api/v1/logs?lines=100&unit=agent",
			"/api/v1/logs?lines=2000&since=2026-11-02T08%3A00%3A12Z&unit=agent",
			"/api/v1/logs?lines=2000&since=2026-11-02T08%3A00%3A14Z&unit=agent",
			"/api/v1/logs?lines=2000&since=2026-11-02T08%3A00%3A14Z&unit=agent",
		}
		if fmt.Sprint(*paths) != fmt.Sprint(wantPaths) {
			t.Errorf("requests\n got %v\nwant %v", *paths, wantPaths)
		}
	})
	t.Run("json lines", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		followBatches(t, f, cancel, batches)
		r := f.exec(ctx, runOpts{}, "--json", "logs", "-f")
		lines := strings.Split(strings.TrimRight(r.out, "\n"), "\n")
		if r.code != 0 || r.errOut != "" || len(lines) != 6 {
			t.Fatalf("exit %d, %d lines, stderr %q:\n%s", r.code, len(lines), r.errOut, r.out)
		}
		for _, l := range lines {
			var o map[string]any
			if err := json.Unmarshal([]byte(l), &o); err != nil || len(o) != 3 || o["at"] == nil || o["priority"] == nil || o["message"] == nil {
				t.Errorf("not a {at,priority,message} line: %q", l)
			}
		}
	})
	t.Run("an error while following ends the command with its exit code", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		var n int
		f.ag.Handle("GET", "/logs", func(w http.ResponseWriter, _ *http.Request) {
			n++
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"lines":[{"at":"2026-11-02T08:00:05Z","priority":6,"message":"A"}]}`)
				return
			}
			fake.WriteEnvelope(w, 401, api.CodeUnauthenticated, "Sign in.", "Sign in again.", nil)
		})
		r := f.run("logs", "-f")
		if r.code != ExitDenied || !strings.Contains(r.errOut, "["+api.CodeUnauthenticated+"]") || !strings.HasPrefix(r.out, "08:00:05") {
			t.Errorf("exit %d out %q err %q", r.code, r.out, r.errOut)
		}
	})
}

func TestStatusWatch(t *testing.T) {
	old := statusWatchInterval
	statusWatchInterval = time.Millisecond
	defer func() { statusWatchInterval = old }()
	const clear = "\x1b[H\x1b[2J"

	t.Run("redraws until Ctrl-C and survives a failed refresh", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/models", "models")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		n := 0
		f.ag.Handle("GET", "/status", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			n++
			k := n
			mu.Unlock()
			switch {
			case k == 2:
				fake.WriteEnvelope(w, 500, api.CodeInternal, "Broken.", "Report it.", nil)
			case k >= 3:
				cancel()
				fallthrough
			default:
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, roBody(t, "status.online"))
			}
		})
		r := f.exec(ctx, runOpts{tty: true}, "status", "--watch")
		if r.code != 0 {
			t.Fatalf("Ctrl-C ends --watch with exit 0, got %d (stderr %q)", r.code, r.errOut)
		}
		if got := strings.Count(r.out, clear); got != 2 { // the third request was cancelled before it could draw
			t.Errorf("%d redraws, want 2", got)
		}
		if !strings.Contains(r.out, "Node ai-n3") || !strings.Contains(r.out, "[LAI-AGT-099]") {
			t.Errorf("frames:\n%s", r.out)
		}
	})
	t.Run("needs a terminal and refuses --json", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/status", "status.online")
		for _, tc := range []struct {
			args []string
			tty  bool
		}{{[]string{"status", "--watch"}, false}, {[]string{"--json", "status", "--watch"}, true}} {
			r := f.runWith(runOpts{tty: tc.tty}, tc.args...)
			if r.code != ExitUsage || r.out != "" && !strings.HasPrefix(r.out, "{") {
				t.Errorf("%v tty=%v: exit %d out %q err %q", tc.args, tc.tty, r.code, r.out, r.errOut)
			}
		}
		if n := f.ag.Count("/status"); n != 0 {
			t.Errorf("--watch refused but %d requests were made", n)
		}
	})
	t.Run("an overall --timeout ends it with the timeout code", func(t *testing.T) {
		f := newROFix(t, fake.Options{})
		f.serve("/status", "status.online")
		f.serve("/models", "models")
		r := f.runWith(runOpts{tty: true}, "--timeout", "50ms", "status", "--watch")
		if r.code != ExitTimeout || !strings.Contains(r.errOut, "[LAI-CLI-004]") {
			t.Errorf("exit %d err %q", r.code, r.errOut)
		}
	})
}

// An event from the agent redraws at once; the interval is made so long that only the event can
// explain the second frame.
func TestStatusWatchRedrawsOnEvents(t *testing.T) {
	oldI, oldG := statusWatchInterval, statusWatchMinGap
	statusWatchInterval, statusWatchMinGap = time.Hour, time.Millisecond
	defer func() { statusWatchInterval, statusWatchMinGap = oldI, oldG }()
	const clear = "\x1b[H\x1b[2J"

	f := newROFix(t, fake.Options{})
	f.serve("/models", "models")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	draws := 0
	f.ag.Handle("GET", "/status", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		draws++
		k := draws
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, roBody(t, "status.online"))
		if k == 2 {
			go cancel() // the redraw the event asked for has been served: leave
		}
	})
	f.ag.Handle("GET", "/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": keepalive\n\n") // a keepalive is not an event
		fl.Flush()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "id: 1\ndata: {\"id\":1,\"type\":\"mode.changed\",\"at\":\"2026-11-02T08:00:00Z\",\"node\":\"ai-n3\",\"data\":{}}\n\n")
		fl.Flush()
		<-r.Context().Done()
	})
	done := make(chan runResult, 1)
	go func() { done <- f.exec(ctx, runOpts{tty: true}, "status", "--watch") }()
	select {
	case r := <-done:
		if r.code != 0 || strings.Count(r.out, clear) < 1 || !strings.Contains(r.out, "Node ai-n3") {
			t.Errorf("exit %d stderr %q out %q", r.code, r.errOut, r.out)
		}
		mu.Lock()
		defer mu.Unlock()
		if draws != 2 {
			t.Errorf("%d status requests, want 2 (the first draw and the one the event asked for)", draws)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no redraw after the event")
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ok := map[string]string{
		"90s":                       "2026-11-02T11:58:30Z",
		"1h30m":                     "2026-11-02T10:30:00Z",
		"2026-11-02T09:00:00Z":      "2026-11-02T09:00:00Z",
		"2026-11-02T09:00:00.5Z":    "2026-11-02T09:00:00Z",
		"2026-11-02T09:00:00-06:00": "2026-11-02T15:00:00Z",
	}
	for in, want := range ok {
		got, err := parseSince(in, now)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "0s", "-1h", "2026-11-02", "12:00"} {
		if got, err := parseSince(in, now); err == nil {
			t.Errorf("parseSince(%q) = %q, want an error", in, got)
		}
	}
}

func TestLogLineTextRemovesControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"plain":           "plain",
		"tab\tkept":       "tab\tkept",
		"esc\x1b[2Jgone":  "esc [2Jgone",
		"bell\a\x00nul":   "bell  nul",
		"cr\rlf\nend":     "cr lf end",
		"c1\u009bcontrol": "c1 control",
		"unicode ✓ é 日本":  "unicode ✓ é 日本",
	} {
		if got := logLineText(in); got != want {
			t.Errorf("logLineText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadyPct(t *testing.T) {
	var r wireReady
	if _, ok := readyPct(&r); ok {
		t.Error("no counts: unknown")
	}
	r.Counts = map[string]int{"verified": 41, "missing": 2, "skipped": 7, "orphan": 9}
	if p, ok := readyPct(&r); !ok || p != 82 {
		t.Errorf("41 of 50 wanted (orphans do not count) = %v, %v", p, ok)
	}
	r.Counts = map[string]int{"orphan": 3}
	if _, ok := readyPct(&r); ok {
		t.Error("only orphans: nothing is wanted, unknown")
	}
	pct := 63.0
	r.ReadyPct = &pct
	if p, ok := readyPct(&r); !ok || p != 63 {
		t.Errorf("explicit ready_pct wins: %v %v", p, ok)
	}
}

func TestMaskCredentialNeverPrintsMoreThanTheTail(t *testing.T) {
	long := "nod_" + strings.Repeat("z", 40) + "q1w2"
	for in, want := range map[string]string{
		"":               "",
		"nod_" + "…a1b2": "nod_" + "…a1b2",
		"nod_...a1b2":    "nod_...a1b2",
		long:             "nod_…q1w2",
		"short":          "…",
	} {
		if got := maskCredential(in); got != want {
			t.Errorf("maskCredential(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(maskCredential(long), "zzzz") {
		t.Error("a full credential leaked")
	}
}
