package docsrender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// browser is a fake Chromium: it records its argv and writes a small PDF with the page count the test
// asks for. It is an execx.Runner, so nothing is started.
type browser struct {
	mu    sync.Mutex
	calls []execx.Cmd
	// pages is the page count to write per HTML base name; default 1.
	pages map[string]int
	// fail makes the named HTML pages fail with exit status 1.
	fail map[string]bool
}

func (b *browser) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	b.mu.Lock()
	b.calls = append(b.calls, c)
	b.mu.Unlock()
	var pdf, html string
	for _, a := range c.Args {
		if v, ok := strings.CutPrefix(a, "--print-to-pdf="); ok {
			pdf = v
		}
		if v, ok := strings.CutPrefix(a, "file://"); ok {
			html = v
		}
	}
	base := filepath.Base(html)
	if b.fail[base] {
		return execx.Result{ExitCode: 1, Stderr: []byte("Failed to print\nsecond line")}, nil
	}
	n := 1
	if v, ok := b.pages[base]; ok {
		n = v
	}
	var sb strings.Builder
	sb.WriteString("%PDF-1.4\n<< /Type /Catalog /Pages 2 0 R >>\n<< /Type /Pages /Count " + fmt.Sprint(n) + " >>\n")
	for i := 0; i < n; i++ {
		sb.WriteString("<< /Type /Page /Parent 2 0 R >>\n")
	}
	return execx.Result{}, os.WriteFile(pdf, []byte(sb.String()), 0o600)
}

func hasBrowser(name string) func(string) (string, error) {
	return func(n string) (string, error) {
		if n == name {
			return "/usr/bin/" + n, nil
		}
		return "", execx.ErrNotFound
	}
}

var noBrowser = func(string) (string, error) { return "", execx.ErrNotFound }

func baseConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inventory.fixture.yaml"), readFixture(t, "inventory.fixture.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		Src:           fixtureSrc(t),
		Outs:          []string{filepath.Join(root, "out")},
		InventoryPath: filepath.Join(root, "inventory.fixture.yaml"),
		ExamplePath:   filepath.Join("testdata", "inventory.example.yaml"),
		NoPDF:         true,
		NodeID:        "ai-n3",
		Now:           time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC),
		Lookup:        noBrowser,
		TmpDir:        t.TempDir(),
	}
}

func warningKinds(r *Report) map[WarningKind]int {
	m := map[WarningKind]int{}
	for _, w := range r.Warnings {
		m[w.Kind]++
	}
	return m
}

func TestRunFixtureHTMLOnly(t *testing.T) {
	cfg := baseConfig(t)
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pages != 6 || rep.Runbooks != 3 || rep.Specs != 1 || rep.PDFs != 0 || rep.Card != "survival-card-ai-n3" || !rep.Inventory || rep.NodeID != "ai-n3" {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Outputs) != 1 || rep.Outputs[0] != cfg.Outs[0] {
		t.Errorf("outputs = %v", rep.Outputs)
	}
	got := readTreeFiles(t, cfg.Outs[0])
	for _, name := range []string{"index.html", "index.json", "print.css", "card.css", "survival-card-ai-n3.html", "01-first.html", "mini.html", "docs-README.html"} {
		if got[name] == "" {
			t.Errorf("missing %s", name)
		}
	}
	for name, body := range got {
		if strings.HasSuffix(name, ".html") {
			assertSelfContained(t, name, []byte(body))
		}
		if strings.Contains(body, "192.0.2.") && !strings.Contains(name, "first") {
			t.Errorf("%s holds a real value it should not", name)
		}
	}
	golden(t, "index.json", []byte(strings.ReplaceAll(got["index.json"], cfg.Outs[0], "/srv/localai/docs")))
	golden(t, "index.html", []byte(got["index.html"]))
	// index.json has the shape the agent reads (agent.md 3.8.29).
	var idx struct {
		Root       string `json:"root"`
		RenderedAt string `json:"rendered_at"`
		Docs       []struct {
			Slug, Title string
			TitleES     *string `json:"title_es"`
			Kind, HTML  string
			PDF         *string `json:"pdf"`
		} `json:"docs"`
	}
	if err := json.Unmarshal([]byte(got["index.json"]), &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Root != cfg.Outs[0] || idx.RenderedAt != "2026-11-02T15:04:05Z" || len(idx.Docs) != 7 {
		t.Errorf("index = %+v", idx)
	}
	if d := idx.Docs[0]; d.Slug != "survival-card-ai-n3" || d.Kind != "card" || d.PDF != nil || d.HTML != "/docs/survival-card-ai-n3.html" {
		t.Errorf("first doc = %+v", d)
	}
	if kinds := warningKinds(rep); kinds[WarnNoPDF] != 0 {
		t.Errorf("--no-pdf must not warn about the printer: %v", kinds)
	}
}

func TestRunMissingInventoryLeavesPlaceholders(t *testing.T) {
	cfg := baseConfig(t)
	cfg.InventoryPath = ""
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inventory || warningKinds(rep)[WarnNoInventory] != 1 {
		t.Errorf("report = %+v", rep)
	}
	got := readTreeFiles(t, cfg.Outs[0])
	if !strings.Contains(got["01-first.html"], "&lt;TAILNET_IP_AI_N1&gt;") || strings.Contains(got["01-first.html"], "192.0.2") {
		t.Error("placeholders were not left as they are")
	}
	// The card is still made; <DESKTOP_USER> stays visible.
	if !strings.Contains(got["survival-card-ai-n3.html"], "&lt;DESKTOP_USER&gt;") {
		t.Error("the card has no visible placeholder")
	}
}

func TestRunInventoryErrors(t *testing.T) {
	cfg := baseConfig(t)
	if err := os.WriteFile(cfg.InventoryPath, []byte("nodes: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), cfg)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindInventory || e.Path != cfg.InventoryPath {
		t.Errorf("bad inventory: %v", err)
	}
	cfg = baseConfig(t)
	cfg.InventoryPath = filepath.Join(t.TempDir(), "gone.yaml") // found earlier, vanished (or unreadable)
	if _, err := Run(context.Background(), cfg); !errors.As(err, &e) || e.Kind != KindIO {
		t.Errorf("unreadable inventory: %v", err)
	}
	// An inventory without the example next to the docs cannot be paired: a warning, not a failure.
	cfg = baseConfig(t)
	cfg.ExamplePath = filepath.Join(t.TempDir(), "gone.yaml")
	rep, err := Run(context.Background(), cfg)
	if err != nil || warningKinds(rep)[WarnNoInventory] != 1 {
		t.Errorf("missing example: %v %v", err, rep)
	}
}

func TestRunFindsTheExampleNextToTheDocs(t *testing.T) {
	cfg := baseConfig(t)
	cfg.ExamplePath = ""
	parent := filepath.Dir(cfg.Src)
	if err := os.WriteFile(filepath.Join(parent, ExampleFile), readFixture(t, "inventory.example.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Src is a temp folder with the example in its parent (the layout of a repository checkout).
	src := filepath.Join(parent, "docs")
	if err := os.Rename(cfg.Src, src); err != nil {
		t.Fatal(err)
	}
	cfg.Src = src
	rep, err := Run(context.Background(), cfg)
	if err != nil || !rep.Inventory {
		t.Fatalf("%v %+v", err, rep)
	}
	if got := readTreeFiles(t, cfg.Outs[0]); !strings.Contains(got["01-first.html"], "192.0.2.21") {
		t.Error("values were not filled")
	}
}

func TestRunRefusesAnOutputInsideAGitCheckoutAndWritesNothing(t *testing.T) {
	cfg := baseConfig(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	good := cfg.Outs[0]
	cfg.Outs = []string{good, filepath.Join(repo, "out")} // one bad folder refuses the whole run
	rep, err := Run(context.Background(), cfg)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindUsage {
		t.Fatalf("err = %v", err)
	}
	if len(rep.Outputs) != 0 || exists(good) || exists(filepath.Join(repo, "out")) {
		t.Error("something was written before the refusal")
	}
	if _, err := Run(context.Background(), Config{Src: cfg.Src}); !errors.As(err, &e) || e.Kind != KindUsage {
		t.Errorf("no --out: %v", err)
	}
}

func TestRunTwoOutputsAndDuplicates(t *testing.T) {
	cfg := baseConfig(t)
	a := cfg.Outs[0]
	b := filepath.Join(filepath.Dir(a), "drive", "localai", "docs")
	cfg.Outs = []string{a, b, a + "/"}
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Outputs) != 2 {
		t.Fatalf("outputs = %v", rep.Outputs)
	}
	for _, out := range rep.Outputs {
		var idx struct{ Root string }
		if err := json.Unmarshal([]byte(readTreeFiles(t, out)["index.json"]), &idx); err != nil || idx.Root != out {
			t.Errorf("%s: index root = %q (%v)", out, idx.Root, err)
		}
	}
}

func TestRunOneOutputFailsTheOtherIsStillWritten(t *testing.T) {
	cfg := baseConfig(t)
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Outs = []string{filepath.Join(blocker, "docs"), cfg.Outs[0]}
	rep, err := Run(context.Background(), cfg)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindIO {
		t.Fatalf("err = %v", err)
	}
	if len(rep.Outputs) != 1 || !exists(filepath.Join(cfg.Outs[1], "index.html")) {
		t.Errorf("the good output was not written: %v", rep.Outputs)
	}
}

func TestRunCardWithSecretWritesNothing(t *testing.T) {
	cfg := baseConfig(t)
	card := filepath.Join(cfg.Src, "runbooks", "00-survival-card.md")
	body, _ := os.ReadFile(card)
	if err := os.WriteFile(card, append(body, []byte("\nSSH to <TAILNET_IP_AI_N3>\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), cfg)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCardSecret {
		t.Fatalf("err = %v", err)
	}
	if exists(cfg.Outs[0]) {
		t.Error("output written although the card was refused")
	}
}

func TestRunMissingCardTemplateWarns(t *testing.T) {
	cfg := baseConfig(t)
	if err := os.Remove(filepath.Join(cfg.Src, "runbooks", "00-survival-card.md")); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), cfg)
	if err != nil || rep.Card != "" || warningKinds(rep)[WarnNoCard] != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	// The link to the card then points at the repository, not at a file that does not exist.
	if !strings.Contains(readTreeFiles(t, cfg.Outs[0])["01-first.html"], "Survival card (https://github.com/") {
		t.Error("link to the missing card not turned into text")
	}
}

func TestRunNodeIDSources(t *testing.T) {
	write := func(dir, name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("node.yaml", func(t *testing.T) {
		cfg := baseConfig(t)
		cfg.NodeID = ""
		cfg.State.NodeYAML = write(t.TempDir(), "node.yaml", "node:\n  id: ai-n1\n")
		rep, err := Run(context.Background(), cfg)
		if err != nil || rep.Card != "survival-card-ai-n1" {
			t.Fatalf("%v %+v", err, rep)
		}
		if !strings.Contains(readTreeFiles(t, cfg.Outs[0])["survival-card-ai-n1.html"], "<strong>avery</strong>") {
			t.Error("the card does not use this node's desktop user")
		}
	})
	t.Run("flag beats node.yaml", func(t *testing.T) {
		cfg := baseConfig(t)
		cfg.State.NodeYAML = write(t.TempDir(), "node.yaml", "node:\n  id: ai-n1\n")
		if rep, err := Run(context.Background(), cfg); err != nil || rep.Card != "survival-card-ai-n3" {
			t.Fatalf("%v %+v", err, rep)
		}
	})
	t.Run("hostname in the inventory", func(t *testing.T) {
		cfg := baseConfig(t)
		cfg.NodeID, cfg.Hostname = "", "box-one"
		inv := "nodes:\n  - id: ai-n1\n    machine: box-one\n    desktop_user: avery\n"
		cfg.InventoryPath = write(t.TempDir(), "inv.yaml", inv)
		if rep, err := Run(context.Background(), cfg); err != nil || rep.Card != "survival-card-ai-n1" {
			t.Fatalf("%v %+v", err, rep)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		cfg := baseConfig(t)
		cfg.NodeID = ""
		rep, err := Run(context.Background(), cfg)
		if err != nil || rep.Card != "survival-card-"+UnconfiguredNode || warningKinds(rep)[WarnNodeUnknown] != 1 {
			t.Fatalf("%v %+v", err, rep)
		}
	})
}

func TestRunPDFs(t *testing.T) {
	cfg := baseConfig(t)
	cfg.NoPDF = false
	br := &browser{}
	cfg.Runner, cfg.Lookup = br, hasBrowser("chromium")
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PDFs != 7 || len(br.calls) != 7 {
		t.Errorf("pdfs = %d, browser calls = %d", rep.PDFs, len(br.calls))
	}
	got := readTreeFiles(t, cfg.Outs[0])
	if !strings.HasPrefix(got["survival-card-ai-n3.pdf"], "%PDF") || !strings.HasPrefix(got["01-first.pdf"], "%PDF") {
		t.Error("PDFs were not written to the output")
	}
	var idx struct {
		Docs []struct {
			Slug string
			PDF  *string
		}
	}
	if err := json.Unmarshal([]byte(got["index.json"]), &idx); err != nil {
		t.Fatal(err)
	}
	for _, d := range idx.Docs {
		if d.PDF == nil || *d.PDF != "/docs/"+d.Slug+".pdf" {
			t.Errorf("%s: pdf = %v", d.Slug, d.PDF)
		}
	}
	// The fixed argv: no shell, the file URL last, a profile folder of our own, a minimal environment.
	c := br.calls[0]
	if c.Path != "/usr/bin/chromium" || c.Timeout != PDFTimeout || len(c.Env) != 2 || !strings.HasPrefix(c.Env[0], "PATH=") {
		t.Errorf("cmd = %+v", c)
	}
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{"--headless", "--no-sandbox", "--print-to-pdf=", "--user-data-dir=", "file://"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %s: %s", want, joined)
		}
	}
	if !strings.HasPrefix(c.Args[len(c.Args)-1], "file://") {
		t.Error("the page is not the last argument")
	}
	if left, _ := filepath.Glob(filepath.Join(cfg.TmpDir, "aictl-docs-*")); len(left) != 0 {
		t.Errorf("scratch folder left behind: %v", left)
	}
}

func TestRunNoPrinterIsAnInfoAndHTMLOnly(t *testing.T) {
	cfg := baseConfig(t)
	cfg.NoPDF = false // a browser is looked for, none found
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if warningKinds(rep)[WarnNoPDF] != 1 || rep.PDFs != 0 {
		t.Errorf("report = %+v", rep)
	}
	if got := readTreeFiles(t, cfg.Outs[0]); got["survival-card-ai-n3.html"] == "" || got["survival-card-ai-n3.pdf"] != "" {
		t.Error("expected the card as HTML only")
	}
}

func TestRunCardOverflowIsRefusedAndWritesNothing(t *testing.T) {
	cfg := baseConfig(t)
	cfg.NoPDF = false
	cfg.Runner, cfg.Lookup = &browser{pages: map[string]int{"survival-card-ai-n3.html": 2}}, hasBrowser("chromium")
	_, err := Run(context.Background(), cfg)
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindCardPages {
		t.Fatalf("err = %v", err)
	}
	if exists(cfg.Outs[0]) {
		t.Error("output written although the card overflows")
	}
}

func TestRunOnePageFailsToPrint(t *testing.T) {
	cfg := baseConfig(t)
	cfg.NoPDF = false
	cfg.Runner, cfg.Lookup = &browser{fail: map[string]bool{"mini.html": true}}, hasBrowser("google-chrome")
	rep, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PDFs != 6 || warningKinds(rep)[WarnPDFFailed] != 1 {
		t.Errorf("report = %+v", rep)
	}
	got := readTreeFiles(t, cfg.Outs[0])
	if got["mini.pdf"] != "" || got["mini.html"] == "" {
		t.Error("a failed page must keep its HTML and have no PDF")
	}
	var w Warning
	for _, x := range rep.Warnings {
		if x.Kind == WarnPDFFailed {
			w = x
		}
	}
	if !strings.Contains(w.Message, "status 1") || strings.Contains(w.Message, "second line") {
		t.Errorf("warning = %q", w.Message)
	}
}

func TestRunBrokenLinksDoNotStopTheRun(t *testing.T) {
	cfg := baseConfig(t)
	rep, err := Run(context.Background(), cfg)
	if err != nil || warningKinds(rep)[WarnBrokenLink] != 2 {
		t.Fatalf("%v %v", err, warningKinds(rep))
	}
}

func TestRunNeverMentionsRealValuesInWarnings(t *testing.T) {
	cfg := baseConfig(t)
	rep, _ := Run(context.Background(), cfg)
	for _, w := range rep.Warnings {
		if strings.Contains(w.Message, "192.0.2") || strings.Contains(w.Message, "example-tailnet") {
			t.Errorf("warning leaks a value: %q", w.Message)
		}
	}
}

func TestPDFHelpers(t *testing.T) {
	cases := []struct {
		name string
		pdf  string
		want int
	}{
		{"empty", "", 0},
		{"one page, tree not counted", "/Type /Pages /Kids [3 0 R]\n/Type /Page\n", 1},
		{"no space", "/Type/Page/Parent 2 0 R", 1},
		{"three pages", "/Type /Page\n/Type /Page /Parent\n/Type /Page", 3},
		{"page at the end of the data", "x /Type /Page", 1},
		{"catalog only", "/Type /Catalog /Type /Pages", 0},
	}
	for _, c := range cases {
		if got := CountPages([]byte(c.pdf)); got != c.want {
			t.Errorf("%s: CountPages = %d, want %d", c.name, got, c.want)
		}
	}
	if p := FindPrinter(hasBrowser("chromium-browser")); p != "/usr/bin/chromium-browser" {
		t.Errorf("FindPrinter = %q", p)
	}
	if p := FindPrinter(noBrowser); p != "" {
		t.Errorf("FindPrinter(none) = %q", p)
	}
	if got := fmt.Sprint(firstLine([]byte("  one\ntwo"))); got != "one" {
		t.Errorf("firstLine = %q", got)
	}
}

func TestPDFMakerErrors(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m := PDFMaker{Runner: &browser{fail: map[string]bool{"bad.html": true}}, Bin: "/usr/bin/chromium"}
	if err := m.Make(ctx, filepath.Join(dir, "bad.html"), filepath.Join(dir, "bad.pdf"), dir); err == nil {
		t.Error("a non-zero exit must be an error")
	}
	// Exit 0 but no file written.
	silent := PDFMaker{Runner: runnerFunc(func(execx.Cmd) (execx.Result, error) { return execx.Result{}, nil }), Bin: "/usr/bin/chromium"}
	if err := silent.Make(ctx, filepath.Join(dir, "a.html"), filepath.Join(dir, "a.pdf"), dir); err == nil {
		t.Error("no PDF written must be an error")
	}
	timeout := PDFMaker{Runner: runnerFunc(func(execx.Cmd) (execx.Result, error) { return execx.Result{}, execx.ErrTimeout }), Bin: "/usr/bin/chromium"}
	if err := timeout.Make(ctx, filepath.Join(dir, "a.html"), filepath.Join(dir, "a.pdf"), dir); !errors.Is(err, execx.ErrTimeout) {
		t.Errorf("timeout error = %v", err)
	}
}

type runnerFunc func(execx.Cmd) (execx.Result, error)

func (f runnerFunc) Run(_ context.Context, c execx.Cmd) (execx.Result, error) { return f(c) }
