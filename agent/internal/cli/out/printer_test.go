package out

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

var fixedNow = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

type variant struct {
	lang  i18n.Lang
	color bool
	utf8  bool
}

func (v variant) suffix() string {
	c, u := "plain", "ascii"
	if v.color {
		c = "color"
	}
	if v.utf8 {
		u = "utf8"
	}
	return string(v.lang) + "." + c + "." + u
}

// variants is the matrix of the golden tests: en/es x colour on/off x UTF-8/ASCII.
func variants() []variant {
	var vs []variant
	for _, l := range []i18n.Lang{i18n.EN, i18n.ES} {
		for _, c := range []bool{true, false} {
			for _, u := range []bool{true, false} {
				vs = append(vs, variant{l, c, u})
			}
		}
	}
	return vs
}

func newPrinter(v variant, mod func(*Options)) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var so, se bytes.Buffer
	o := Options{Stdout: &so, Stderr: &se, Lang: v.lang, Color: v.color, UTF8: v.utf8, Now: func() time.Time { return fixedNow }}
	if mod != nil {
		mod(&o)
	}
	return New(o), &so, &se
}

// render shows ESC as \e so colour goldens are readable and diff well.
func render(so, se string) string {
	var b strings.Builder
	if so != "" {
		b.WriteString("== stdout ==\n" + so)
	}
	if se != "" {
		b.WriteString("== stderr ==\n" + se)
	}
	return strings.ReplaceAll(b.String(), "\x1b", `\e`)
}

type goldenCase struct {
	name string
	mod  func(*Options)
	run  func(p *Printer, v variant)
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{name: "problem", run: func(p *Printer, _ variant) {
			p.Problem(Problem{Severity: SevError, Code: "LAI-LLM-001",
				Message: "The AI model service is not running.",
				Fix:     "Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`."})
		}},
		{name: "problem-severities", run: func(p *Printer, _ variant) {
			p.Problem(Problem{Severity: SevInfo, Code: "LAI-CLI-010", Message: "You stopped waiting.", Fix: "Check with `aictl status`."})
			p.Problem(Problem{Severity: SevWarning, Code: "LAI-HW-012", Message: "Running on battery.", Fix: "Plug in the charger."})
			p.Problem(Problem{Severity: SevError, Code: "LAI-CLI-005", Message: "Unknown option --x."})
			p.Problem(Problem{Severity: SevCritical, Code: "LAI-AGT-004", Message: "The battery is almost empty.", Fix: "Plug in the charger now."})
			p.Problem(Problem{Severity: SevError, Message: "No code on this one."})
		}},
		{name: "problem-wrapped", run: func(p *Printer, _ variant) {
			p.Problem(Problem{Severity: SevError, Code: "LAI-LLM-009",
				Message: "The model qwen3.6-35b-a3b-q4km failed to start. The previous model was started again.",
				Fix:     "Run `aictl logs --unit llama` to see why, or choose a smaller model with `aictl models --loadable`."})
			p.Problem(Problem{Severity: SevWarning, Code: "LAI-HW-005",
				Message: "This message is exactly long enough that the code has to move to its own line below.",
				Fix:     "Nothing to do."})
		}},
		{name: "problem-verbose", mod: func(o *Options) { o.Verbose = true }, run: func(p *Printer, _ variant) {
			p.Problem(Problem{Severity: SevError, Code: "LAI-SEC-004",
				Message: "Too many wrong PIN attempts.", Fix: "Wait, then try again.",
				Details:    map[string]any{"b": 2, "a": "x"},
				RequestID:  "req_ABCDEFGHIJKL",
				RetryAfter: 4*time.Minute + 12*time.Second})
		}},
		{name: "problem-request-id", run: func(p *Printer, _ variant) {
			p.Problem(Problem{Severity: SevError, Code: "LAI-CLI-009", Message: "The agent sent an answer aictl does not understand.",
				RequestID: "req_ABCDEFGHIJKL", ShowRequestID: true})
		}},
		{name: "lines", run: func(p *Printer, _ variant) {
			p.OK("The model is loaded.")
			p.Info("Nothing to do.")
			p.Warn("The disk is almost full.")
			p.Progress("Loading Qwen 3.5 9B")
			p.Secondary("qwen3.5-9b-q4km  /srv/localai")
			p.Blank()
			p.Result("essential result")
			p.Error("Something went wrong.")
			p.Critical("Something went very wrong.")
			p.PrintSecretWarning()
		}},
		{name: "table", run: func(p *Printer, _ variant) {
			p.Table(
				[]Col{{Title: "MODEL"}, {Title: "SIZE", Right: true}, {Title: "STATUS"}, {Title: "NOTES"}},
				[][]string{
					{"qwen3.5-9b-q4km", Size(5_900_000_000), "loaded", "default · chat, reasoning, coder"},
					{"small-vlm-q4km", Size(3_200_000_000), "stored", "vision"},
					{"qwen3.6-35b-a3b-q4km", Size(19_000_000_000), "absent", "does not fit: needs 11.0 GB of graphics memory, this computer has 8.0 GB"},
					{"whisper-small", Size(466_000_000), "stored"},
				})
		}},
		{name: "kv", run: func(p *Printer, v variant) {
			s := p.Symbols()
			p.KV([]Pair{
				{"Mode", "online      (since " + p.When(fixedNow.Add(-17*time.Hour)) + ")"},
				{"Internet", s.OK + " full      (cloud reachable)"},
				{"Graphics", GBFromMiB(6791) + " / " + GBFromMiB(8192) + " used"},
				{"Services", s.OK + " llama   " + s.OK + " kiwix   " + s.Warn + " whisper"},
				{"Ark", s.Warn + " internal 82% ready · last sync " + p.When(fixedNow.Add(-12*time.Hour)) + " · no ark drive, plug one in to copy everything"},
			})
		}},
	}
}

func TestGolden(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, gc := range goldenCases() {
		for _, v := range variants() {
			name := gc.name + "." + v.suffix()
			t.Run(name, func(t *testing.T) {
				p, so, se := newPrinter(v, gc.mod)
				gc.run(p, v)
				got := render(so.String(), se.String())
				path := filepath.Join(dir, name+".txt")
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("missing golden file (run: go test ./internal/cli/out -update): %v", err)
				}
				if got != string(want) {
					t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
				}
			})
		}
	}
}

// Every line a golden case prints, in every variant without colour, is within 80 columns (the one
// exception is -v details, which are JSON for a person who asked for detail).
func TestGoldenLinesWithinWidth(t *testing.T) {
	for _, gc := range goldenCases() {
		if gc.name == "problem-verbose" {
			continue
		}
		for _, v := range variants() {
			if v.color {
				continue
			}
			p, so, se := newPrinter(v, gc.mod)
			gc.run(p, v)
			for _, l := range strings.Split(so.String()+se.String(), "\n") {
				if DisplayWidth(l) > DefaultWidth {
					t.Errorf("%s.%s: line of %d columns: %q", gc.name, v.suffix(), DisplayWidth(l), l)
				}
			}
		}
	}
}

func TestColorEnabled(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	tests := []struct {
		name    string
		tty     bool
		noColor bool
		json    bool
		env     func(string) string
		want    bool
	}{
		{"terminal", true, false, false, env(), true},
		{"not a terminal", false, false, false, env(), false},
		{"--no-color", true, true, false, env(), false},
		{"NO_COLOR set", true, false, false, env("NO_COLOR", "1"), false},
		{"NO_COLOR empty is not set", true, false, false, env("NO_COLOR", ""), true},
		{"TERM=dumb", true, false, false, env("TERM", "dumb"), false},
		{"TERM=xterm", true, false, false, env("TERM", "xterm-256color"), true},
		{"--json", true, false, true, env(), false},
		{"nil getenv", true, false, false, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorEnabled(tc.tty, tc.noColor, tc.json, tc.env); got != tc.want {
				t.Errorf("ColorEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUTF8Enabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", nil, false},
		{"C", map[string]string{"LANG": "C"}, false},
		{"POSIX", map[string]string{"LANG": "POSIX"}, false},
		{"en_US.UTF-8", map[string]string{"LANG": "en_US.UTF-8"}, true},
		{"es_CR.utf8", map[string]string{"LANG": "es_CR.utf8"}, true},
		{"LC_ALL wins over LANG", map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "C"}, false},
		{"LC_ALL UTF-8 wins over LANG C", map[string]string{"LANG": "C", "LC_ALL": "C.UTF-8"}, true},
		{"LC_CTYPE before LANG", map[string]string{"LANG": "C", "LC_CTYPE": "en_US.UTF-8"}, true},
		{"latin-1", map[string]string{"LANG": "es_ES.ISO-8859-1"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UTF8Enabled(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Errorf("UTF8Enabled = %v, want %v", got, tc.want)
			}
		})
	}
	if UTF8Enabled(nil) {
		t.Error("nil getenv must give ASCII")
	}
}

// Colour is only ever added around symbols and secondary text; stripping it leaves the plain output.
func TestColorStrippedEqualsPlain(t *testing.T) {
	for _, gc := range goldenCases() {
		for _, u := range []bool{true, false} {
			pc, soc, sec := newPrinter(variant{i18n.EN, true, u}, gc.mod)
			gc.run(pc, variant{})
			pp, sop, sep := newPrinter(variant{i18n.EN, false, u}, gc.mod)
			gc.run(pp, variant{})
			if got := ansiEscape.ReplaceAllString(soc.String()+sec.String(), ""); got != sop.String()+sep.String() {
				t.Errorf("%s: colour output minus escapes differs from plain\n%q\n%q", gc.name, got, sop.String()+sep.String())
			}
		}
	}
}

func TestColorCodes(t *testing.T) {
	p, so, se := newPrinter(variant{i18n.EN, true, true}, nil)
	p.OK("a")
	p.Info("b")
	p.Warn("c")
	p.Error("d")
	p.Critical("e")
	p.Progress("f")
	p.Secondary("g")
	for _, want := range []string{
		"\x1b[32m✓\x1b[0m a", "\x1b[34mi\x1b[0m b", "\x1b[2mg\x1b[0m",
	} {
		if !strings.Contains(so.String(), want) {
			t.Errorf("stdout lacks %q: %q", want, so.String())
		}
	}
	for _, want := range []string{
		"\x1b[33m!\x1b[0m c", "\x1b[31m✗\x1b[0m d", "\x1b[1;31m✗\x1b[0m e", "\x1b[36m…\x1b[0m f",
	} {
		if !strings.Contains(se.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, se.String())
		}
	}
}

// Without colour there is never an escape, and the symbols are still printed.
func TestNoColorKeepsSymbols(t *testing.T) {
	p, so, se := newPrinter(variant{i18n.EN, false, true}, nil)
	p.OK("a")
	p.Problem(Problem{Severity: SevError, Code: "LAI-CLI-005", Message: "b.", Fix: "c."})
	all := so.String() + se.String()
	if strings.Contains(all, "\x1b") {
		t.Errorf("escape in plain output: %q", all)
	}
	if !strings.Contains(all, "✓ a") || !strings.Contains(all, "✗ b.  [LAI-CLI-005]") {
		t.Errorf("symbols missing: %q", all)
	}
}

func TestStreams(t *testing.T) {
	p, so, se := newPrinter(variant{i18n.EN, false, true}, nil)
	p.OK("ok")
	p.Info("info")
	p.Result("res")
	p.Warn("warn")
	p.Error("err")
	p.Progress("prog")
	if got := so.String(); got != "✓ ok\ni info\nres\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := se.String(); got != "! warn\n✗ err\n… prog\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestQuiet(t *testing.T) {
	p, so, se := newPrinter(variant{i18n.EN, false, true}, func(o *Options) { o.Quiet = true })
	p.OK("ok")
	p.Info("info")
	p.Secondary("sec")
	p.Blank()
	p.Warn("warn")
	p.Progress("prog")
	p.Problem(Problem{Severity: SevWarning, Message: "w."})
	p.Problem(Problem{Severity: SevInfo, Message: "i."})
	p.Result("essential")
	p.Table([]Col{{Title: "A"}}, [][]string{{"1"}})
	p.Error("boom")
	p.PrintSecretWarning()
	if got := so.String(); got != "essential\nA\n1\n" {
		t.Errorf("quiet stdout = %q", got)
	}
	if got := se.String(); !strings.HasPrefix(got, "✗ boom\n") || !strings.Contains(got, "secret key") || strings.Contains(got, "warn") {
		t.Errorf("quiet stderr = %q (errors and the secret warning only)", got)
	}
}

// With --json no human text reaches stdout, and an error becomes the envelope.
func TestJSONMode(t *testing.T) {
	p, so, se := newPrinter(variant{i18n.EN, false, true}, func(o *Options) { o.JSON = true })
	p.OK("ok")
	p.Info("info")
	p.Result("res")
	p.Secondary("sec")
	p.Blank()
	p.Progress("prog")
	p.Table([]Col{{Title: "A"}}, [][]string{{"1"}})
	p.KV([]Pair{{"a", "b"}})
	p.PrintSecretWarning()
	if so.Len() != 0 || se.Len() != 0 {
		t.Fatalf("human output in --json mode: stdout %q stderr %q", so, se)
	}
	p.Problem(Problem{Severity: SevError, Code: "LAI-CLI-007", Message: "Needs a confirmation.", Fix: "Add `--yes`."})
	if se.Len() != 0 {
		t.Errorf("stderr must stay empty on failure: %q", se)
	}
	var env struct {
		Error struct {
			Code, Message, Fix string
			Details            map[string]any
		}
	}
	if err := json.Unmarshal(so.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON document: %v: %q", err, so)
	}
	if env.Error.Code != "LAI-CLI-007" || env.Error.Message != "Needs a confirmation." || env.Error.Fix != "Add `--yes`." || env.Error.Details == nil {
		t.Errorf("envelope = %+v", env)
	}
	if strings.Count(so.String(), "\n") != 1 {
		t.Errorf("exactly one line expected: %q", so)
	}
}

func TestWhen(t *testing.T) {
	at := fixedNow.Add(-12 * time.Hour)
	p, _, _ := newPrinter(variant{i18n.EN, false, true}, nil)
	if got := p.When(at); got != "12 hours ago" {
		t.Errorf("When = %q", got)
	}
	pv, _, _ := newPrinter(variant{i18n.ES, false, true}, func(o *Options) { o.Verbose = true })
	if got := pv.When(at); got != "hace 12 horas (2026-11-02T03:00:00Z)" {
		t.Errorf("When -v = %q", got)
	}
}

func TestProblemNeverPrintsHTTPStatusOrChain(t *testing.T) {
	p, _, se := newPrinter(variant{i18n.EN, false, true}, nil)
	p.Problem(Problem{Severity: SevError, Code: "LAI-AGT-022", Message: "Another operation is running.", Fix: "Wait for it."})
	for _, bad := range []string{"HTTP", "409", "goroutine", "runtime."} {
		if strings.Contains(se.String(), bad) {
			t.Errorf("problem output contains %q: %q", bad, se)
		}
	}
}
