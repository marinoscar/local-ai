package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Hello World", "hello-world"},
		{"5. Redaction (public repo)", "5-redaction-public-repo"},
		{"🚨 In an emergency", "-in-an-emergency"},
		{"`aictl doctor`", "aictl-doctor"},
		{"§4.12 Survival portal", "412-survival-portal"},
		{"A & B", "a--b"},
		{"snake_case_name", "snake_case_name"},
		{"Café Menu", "café-menu"},
		{"Q&A: what/why?", "qa-whatwhy"},
		{"[Link text](http://x.test/y) here", "link-text-here"},
		{"**Bold** and _emph_", "bold-and-emph"},
		{"Already-hyphenated title", "already-hyphenated-title"},
		{"Trailing hashes ##", "trailing-hashes"},
		{"1.2.3 Version", "123-version"},
		{"Ports: 7070 / 8080", "ports-7070--8080"},
		{"Use `go test ./...` now", "use-go-test--now"},
		{"Image ![alt text](a.png) in heading", "image-alt-text-in-heading"},
		{"HTML <b>tag</b> here", "html-tag-here"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		from, link, want string
		ok               bool
	}{
		{"docs/specs/agent.md", "../architecture/overview.md", "docs/architecture/overview.md", true},
		{"docs/specs/agent.md", "catalog.md", "docs/specs/catalog.md", true},
		{"docs/specs/agent.md", "./catalog.md", "docs/specs/catalog.md", true},
		{"README.md", "docs/README.md", "docs/README.md", true},
		{"docs/specs/agent.md", "/AGENTS.md", "AGENTS.md", true},
		{"docs/a.md", "../../outside.md", "", false},
		{"a.md", "../x.md", "", false},
	}
	for _, c := range cases {
		got, ok := resolve(c.from, c.link)
		if got != c.want || ok != c.ok {
			t.Errorf("resolve(%q,%q) = %q,%v want %q,%v", c.from, c.link, got, ok, c.want, c.ok)
		}
	}
}

func TestParseMarkdown(t *testing.T) {
	src := strings.Join([]string{
		"# Title",                          // 1
		"See [a](a.md) and ![img](i.png).", // 2
		"Code `[no](nope.md)` ignored.",    // 3
		"```",                              // 4
		"[fenced](fenced.md)",              // 5
		"# not a heading",                  // 6
		"```",                              // 7
		"<!-- [c](comment.md) -->",         // 8
		"<!--",                             // 9
		"[c2](comment2.md)",                // 10
		"-->",                              // 11
		"[paren](a_(b).md) and [t](t.md \"title\") and [ang](<sp ace.md>)", // 12
		"[ref]: ref.md#sec",        // 13
		"<https://example.test/x>", // 14
		"## Dup",                   // 15
		"## Dup",                   // 16
		"Setext Head",              // 17
		"-----------",              // 18
		"<a id=\"custom-id\"></a>", // 19
	}, "\n")
	d := parseMarkdown(src)
	var got []string
	for _, l := range d.Links {
		got = append(got, l.Target)
	}
	want := []string{"a.md", "i.png", "a_(b).md", "t.md", "sp ace.md", "ref.md#sec", "https://example.test/x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links = %q, want %q", got, want)
	}
	for _, a := range []string{"title", "dup", "dup-1", "setext-head", "custom-id"} {
		if !d.Anchors[a] {
			t.Errorf("missing anchor %q in %v", a, d.Anchors)
		}
	}
	if d.Anchors["not-a-heading"] {
		t.Error("heading inside a fence must be ignored")
	}
	if d.Links[0].Line != 2 {
		t.Errorf("line number = %d, want 2", d.Links[0].Line)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "# Top\n[ok](docs/a.md#section-one) [dir](docs) [self](#top) [dec](docs/a.md#section%2Done)\n")
	write(t, root, "docs/a.md", "## Section One\n[up](../README.md#top) [gone](missing.md) [badanchor](../README.md#nope)\n[outside](../../x.md) [line](../code.go#L10) [mail](mailto:person@example.test)\n")
	write(t, root, "code.go", "package x\n")
	c := NewChecker(root)
	findings, ext, links := c.CheckFiles([]string{"README.md", "docs/a.md"})
	if links != 10 {
		t.Errorf("links = %d, want 10", links)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.File+":"+itoa(f.Line)+":"+f.Target)
	}
	want := []string{"docs/a.md:2:missing.md", "docs/a.md:2:../README.md#nope", "docs/a.md:3:../../x.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %q, want %q", got, want)
	}
	if len(ext) != 0 {
		t.Errorf("unexpected external links: %v", ext)
	}
	if s := findings[0].String(); !strings.HasPrefix(s, "BROKEN docs/a.md:2 -> missing.md") {
		t.Errorf("format = %q", s)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestExternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
		case "/nohead":
			if r.Method == http.MethodHead {
				w.WriteHeader(405)
			}
		case "/gone":
			w.WriteHeader(404)
		case "/busy":
			w.WriteHeader(429)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	// httptest listens on 127.0.0.1, which skipExternal filters: call fetchOne directly.
	check := func(path string) ExternalResult { return fetchOne(srv.URL+path, []string{"x.md:1"}, srv.Client()) }
	if r := check("/ok"); r.Broken || r.Warning != "" {
		t.Errorf("/ok: %+v", r)
	}
	if r := check("/nohead"); r.Broken || r.Warning != "" {
		t.Errorf("/nohead should fall back to GET: %+v", r)
	}
	if r := check("/gone"); !r.Broken {
		t.Errorf("/gone should be broken: %+v", r)
	}
	if r := check("/busy"); r.Broken || r.Warning == "" {
		t.Errorf("/busy should warn, not break: %+v", r)
	}
	if !skipExternal("http://localhost:7070/x") || !skipExternal("https://<HOST>/x") || skipExternal("https://github.com/x") {
		t.Error("skipExternal misclassified")
	}
}
