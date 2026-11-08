package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the compile progress of `aictl rebuild-llama` (issue #433): ninja's "[n/N]" lines, recorded
// from a CUDA build in testdata/rebuild-llama/ninja-cuda.log, become the step's name, the percentage
// and an estimate of the time left.

func ninjaFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rebuild-llama", "ninja-cuda.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNinjaMeterReadsTheRecordedBuild(t *testing.T) {
	var m ninjaMeter
	start := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	var got []compileStatus
	for i, line := range strings.Split(ninjaFixture(t), "\n") {
		if st, ok := m.observe(line, start.Add(time.Duration(i)*time.Minute)); ok {
			got = append(got, st)
		}
	}
	if len(got) != 12 {
		t.Fatalf("got %d status lines, want 12 (the warning and the empty line are not steps)", len(got))
	}
	want := []struct {
		i          int
		step, verb string
		pct        float64
	}{
		{0, "ggml.c", "compile", 100.0 / 12},
		{1, "ggml-backend.cpp", "compile", 200.0 / 12},
		{3, "libggml-base.a", "link", 400.0 / 12},
		{4, "fattn.cu", "compile", 500.0 / 12},
		{11, "llama-server", "link", 100},
	}
	for _, w := range want {
		st := got[w.i]
		if st.Step != w.step || st.Verb != w.verb || st.Percent != w.pct || st.Done != w.i+1 || st.Total != 12 {
			t.Errorf("line %d = %+v, want %s %s %.1f%%", w.i+1, st, w.verb, w.step, w.pct)
		}
	}
	if got[0].HasLeft || got[1].HasLeft {
		t.Error("no estimate may be given after one or two steps")
	}
	// Line 5 is the fifth step, 5 minutes after the first (the warning line took a minute too): 4
	// steps in 5 min, 7 steps left.
	if st := got[4]; !st.HasLeft || st.Left != time.Duration(float64(5*time.Minute)/4*7) {
		t.Errorf("estimate at step 5 = %v (%v), want 8 min 45 s", st.Left, st.HasLeft)
	}
	if st := got[11]; !st.HasLeft || st.Left != 0 {
		t.Errorf("the last step leaves nothing: %+v", st)
	}
}

func TestNinjaMeterTable(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		lines []string
		at    []time.Duration
		ok    bool
		want  compileStatus
	}{
		{name: "not a status line", lines: []string{"-- Configuring done"}, at: []time.Duration{0}},
		{name: "zero total", lines: []string{"[0/0] nothing"}, at: []time.Duration{0}},
		{name: "done beyond total", lines: []string{"[5/4] x"}, at: []time.Duration{0}},
		{name: "other step keeps its words", lines: []string{"[1/3] Generating build details from Git"}, at: []time.Duration{0}, ok: true,
			want: compileStatus{Done: 1, Total: 3, Step: "Generating build details from Git", Percent: 100.0 / 3}},
		{name: "too early for an estimate", lines: []string{"[1/100] a", "[2/100] a", "[3/100] a", "[4/100] a"},
			at: []time.Duration{0, time.Second, 2 * time.Second, 3 * time.Second}, ok: true,
			want: compileStatus{Done: 4, Total: 100, Step: "a", Percent: 4}},
		{name: "estimate from the pace since the first line", lines: []string{"[50/100] a", "[53/100] a"},
			at: []time.Duration{0, 30 * time.Second}, ok: true,
			want: compileStatus{Done: 53, Total: 100, Step: "a", Percent: 53, Left: time.Duration(float64(30*time.Second) / 3 * 47), HasLeft: true}},
		{name: "a new count starts the pace again", lines: []string{"[1/10] a", "[5/10] a", "[1/4] b", "[2/4] b"},
			at: []time.Duration{0, time.Hour, time.Hour + time.Second, time.Hour + 2*time.Second}, ok: true,
			want: compileStatus{Done: 2, Total: 4, Step: "b", Percent: 50}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m ninjaMeter
			var st compileStatus
			var ok bool
			for i, l := range tc.lines {
				st, ok = m.observe(l, t0.Add(tc.at[i]))
			}
			if ok != tc.ok || st != tc.want {
				t.Errorf("got %+v %v, want %+v %v", st, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLeftText(t *testing.T) {
	for _, tc := range []struct {
		d      time.Duration
		en, es string
	}{
		{20 * time.Second, "less than a minute left", "queda menos de un minuto"},
		{40*time.Minute + 20*time.Second, "about 40 min left", "quedan unos 40 min"},
		{80*time.Minute + 40*time.Second, "about 1 h 21 min left", "quedan unos 1 h 21 min"},
	} {
		if got := leftText(LangEN, tc.d); got != tc.en {
			t.Errorf("%v en = %q", tc.d, got)
		}
		if got := leftText(LangES, tc.d); got != tc.es {
			t.Errorf("%v es = %q", tc.d, got)
		}
	}
}

func TestLineSplitterJoinsPiecesAndSplitsLines(t *testing.T) {
	var got []string
	s := &lineSplitter{fn: func(l string) { got = append(got, l) }}
	for _, p := range []string{"[1/2] Bui", "lding x\n[2/2] ", "Linking y\n\npartial"} {
		_, _ = s.Write([]byte(p))
	}
	if strings.Join(got, "|") != "[1/2] Building x|[2/2] Linking y" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("x", ninjaMaxLine+10) + "\n"
	got = nil
	_, _ = s.Write([]byte(long))
	if len(got) != 2 || len(got[0]) != ninjaMaxLine {
		t.Errorf("a runaway line must be cut: %d pieces", len(got))
	}
}

// minuteClock makes llamaClock move a minute on every read.
func minuteClock(t *testing.T) {
	t.Helper()
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	llamaClock = func() time.Time {
		at = at.Add(time.Minute)
		return at
	}
}

func TestRebuildLlamaPlainProgressOffATerminal(t *testing.T) {
	f := newLlamaFix(t)
	minuteClock(t)
	f.eng.ninja = ninjaFixture(t)
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	errOut := unwrap(r.errOut)
	if strings.Contains(errOut, "\r") || strings.Contains(errOut, "\x1b") {
		t.Errorf("off a terminal nothing is redrawn:\n%q", errOut)
	}
	// A plain line each time the build passes another tenth: 8% (1 of 12) is not one.
	for _, want := range []string{
		"… Compiling: 16% done (2 of 12)",
		"… Compiling: 41% done (5 of 12), about",
		"… Compiling: 100% done (12 of 12), less than a minute left",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "(1 of 12)") {
		t.Errorf("the first step is under a tenth:\n%s", errOut)
	}
	if n := strings.Count(errOut, "Compiling:"); n > 10 {
		t.Errorf("%d progress lines, want at most one per tenth", n)
	}
	es := f.run(t, "--lang", "es", "rebuild-llama", "--yes")
	if !strings.Contains(unwrap(es.errOut), "… Compilando: 100 % hecho (12 de 12)") {
		t.Errorf("Spanish progress:\n%s", es.errOut)
	}
}

func TestRebuildLlamaLiveProgressOnATerminal(t *testing.T) {
	f := newLlamaFix(t)
	minuteClock(t)
	old := llamaStderrTTY
	llamaStderrTTY = func(*Call) bool { return true }
	t.Cleanup(func() { llamaStderrTTY = old })
	f.eng.ninja = ninjaFixture(t)
	r := f.run(t, "rebuild-llama", "--yes")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	for _, want := range []string{"Compiling fattn.cu  ", "Linking llama-server", "12/12", "5/12", "min left", "\r\x1b[K"} {
		if !strings.Contains(stripColour(r.errOut), want) {
			t.Errorf("stderr lacks %q:\n%q", want, r.errOut)
		}
	}
	if strings.Contains(r.errOut, "Compiling:") {
		t.Error("on a terminal the plain lines are not printed")
	}
	for _, l := range strings.Split(stripColour(r.errOut), "\r") {
		if l = strings.TrimSuffix(strings.TrimPrefix(l, "\x1b[K"), "\x1b[K"); len([]rune(l)) > 80 && !strings.Contains(l, "\n") {
			t.Errorf("a live line is wider than 80 columns: %q", l)
		}
	}
}

// stripColour removes the colour escapes (not the line controls).
func stripColour(s string) string {
	for _, c := range []string{"\x1b[36m", "\x1b[2m", "\x1b[0m", "\x1b[?25l", "\x1b[?25h"} {
		s = strings.ReplaceAll(s, c, "")
	}
	return s
}

func TestRebuildLlamaJSONAndQuietShowNoCompileProgress(t *testing.T) {
	f := newLlamaFix(t)
	f.eng.ninja = ninjaFixture(t)
	js := f.run(t, "--json", "rebuild-llama", "--yes")
	if js.code != 0 || js.errOut != "" || f.lastReq(t).Output != nil {
		t.Fatalf("--json: exit %d, stderr %q, output %v", js.code, js.errOut, f.lastReq(t).Output)
	}
	q := f.run(t, "--quiet", "rebuild-llama", "--yes")
	if q.code != 0 || strings.Contains(q.errOut, "Compiling") {
		t.Fatalf("--quiet: exit %d\n%s", q.code, q.errOut)
	}
}

func TestHeartbeatStaysQuietWhileTheCompileTalks(t *testing.T) {
	newLlamaFix(t) // puts llamaClock back afterwards
	p := &llamaPrinter{c: &Call{Lang: LangEN}}
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	llamaClock = func() time.Time { return now }
	if p.compiledRecently(llamaHeartbeatEvery) {
		t.Error("nothing compiled yet")
	}
	p.lastCompile = now.Add(-10 * time.Second)
	if !p.compiledRecently(llamaHeartbeatEvery) {
		t.Error("a ninja line 10 s ago is recent")
	}
	p.lastCompile = now.Add(-2 * llamaHeartbeatEvery)
	if p.compiledRecently(llamaHeartbeatEvery) {
		t.Error("a ninja line a minute ago is not recent")
	}
}
