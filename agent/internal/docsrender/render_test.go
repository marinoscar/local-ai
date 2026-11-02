package docsrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var mdHrefRE = regexp.MustCompile(`href="[^"]*\.md`)

func fixtureValues(t *testing.T) map[string]string {
	return fixturePairs(t).Values("ai-n3")
}

func buildFixture(t *testing.T) *Built {
	t.Helper()
	b, err := Build(BuildOptions{Src: fixtureSrc(t), Values: fixtureValues(t), CardFile: "survival-card-ai-n3.html"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildGoldenPages(t *testing.T) {
	b := buildFixture(t)
	for _, name := range []string{"01-first.html", "mini.html", "02-second.html"} {
		golden(t, name, b.Files[name])
	}
}

func TestBuildPagesAndSlugs(t *testing.T) {
	b := buildFixture(t)
	got := map[string]*Doc{}
	for _, d := range b.Pages {
		got[d.Rel] = d
	}
	// source/ is not rendered, the card template is not a page, every folder's README is prefixed.
	want := map[string]string{
		"README.md":             "docs-README",
		"runbooks/README.md":    "runbooks-README",
		"runbooks/01-first.md":  "01-first",
		"runbooks/02-second.md": "02-second",
		"specs/mini.md":         "mini",
		"architecture/arch.md":  "arch",
	}
	if len(got) != len(want) {
		t.Errorf("pages = %d, want %d", len(got), len(want))
	}
	for rel, slug := range want {
		d := got[rel]
		if d == nil || d.Slug != slug || d.HTML != slug+".html" {
			t.Errorf("%s: %+v, want slug %s", rel, d, slug)
		}
	}
	if _, ok := got["source/old.md"]; ok {
		t.Error("the source/ archive was rendered")
	}
	if _, ok := got["runbooks/00-survival-card.md"]; ok {
		t.Error("the card template was rendered as an ordinary page")
	}
	if b.CardTemplate == nil {
		t.Error("the card template was not kept")
	}
	// Order: runbooks, specs, architecture, then the rest.
	var kinds []string
	for _, d := range b.Pages {
		kinds = append(kinds, d.Kind)
	}
	if strings.Join(kinds, ",") != "runbook,runbook,runbook,spec,architecture,other" {
		t.Errorf("order = %v", kinds)
	}
	if d := got["runbooks/01-first.md"]; d.Title != "01. First runbook: connect" || d.TitleES != "" {
		t.Errorf("titles = %q / %q", d.Title, d.TitleES)
	}
	if d := got["runbooks/02-second.md"]; d.TitleES != "Segunda guía" {
		t.Errorf("front matter title_es = %q", d.TitleES)
	}
	if d := got["specs/mini.md"]; d.TitleES != "Especificación mínima" {
		t.Errorf("comment title_es = %q", d.TitleES)
	}
}

func TestBuildFillsPlaceholders(t *testing.T) {
	b := buildFixture(t)
	page := string(b.Files["01-first.html"])
	for _, want := range []string{
		"192.0.2.21", "192.0.2.23", // filled, also inside the fenced code block
		"ssh casey@192.0.2.21",
		"&lt;NO_SUCH_VALUE&gt;", // unknown placeholders stay, and are visible
		"&lt;BRAINS&gt;",        // node-state names are card-only
	} {
		if !strings.Contains(page, want) {
			t.Errorf("01-first.html lacks %q", want)
		}
	}
	if strings.Contains(page, "&lt;TAILNET_IP_AI_N1&gt;") || strings.Contains(page, "&lt;DESKTOP_USER&gt;") {
		t.Error("a known placeholder was left unfilled")
	}
	second := string(b.Files["02-second.html"])
	if !strings.Contains(second, "person-a@example.test") || !strings.Contains(second, "<strong>bold casey</strong>") {
		t.Errorf("02-second.html: values not filled (inline markup must survive): %s", second)
	}
}

func TestBuildWithoutValuesKeepsPlaceholders(t *testing.T) {
	b, err := Build(BuildOptions{Src: fixtureSrc(t)})
	if err != nil {
		t.Fatal(err)
	}
	page := string(b.Files["01-first.html"])
	for _, want := range []string{"&lt;TAILNET_IP_AI_N1&gt;", "&lt;DESKTOP_USER&gt;", "ssh &lt;DESKTOP_USER&gt;@&lt;TAILNET_IP_AI_N1&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("lacks %q", want)
		}
	}
}

func TestBuildValueIsEscapedNotInterpreted(t *testing.T) {
	src := writeTree(t, map[string]string{"a.md": "# A\n\nValue: <X_VAL> and `<X_VAL>`.\n\n```\n<X_VAL>\n```\n"})
	b, err := Build(BuildOptions{Src: src, Values: map[string]string{"X_VAL": `<img src=x onerror=1> **b** & "q"`}})
	if err != nil {
		t.Fatal(err)
	}
	page := string(b.Files["a.html"])
	if strings.Contains(page, "<img") || strings.Contains(page, "<strong>") {
		t.Errorf("a value was interpreted as markup: %s", page)
	}
	if n := strings.Count(page, "&lt;img src=x onerror=1&gt; **b** &amp; &#34;q&#34;"); n != 3 {
		t.Errorf("escaped value appears %d times, want 3: %s", n, page)
	}
}

func TestBuildLinks(t *testing.T) {
	b := buildFixture(t)
	first := string(b.Files["01-first.html"])
	cases := []struct{ name, want string }{
		{"card template", `<a href="survival-card-ai-n3.html">Survival card</a>`},
		{"page with anchor", `<a href="mini.html#a-table">Mini spec</a>`},
		{"page", `<a href="mini.html">top</a>`},
		{"same page anchor", `<a href="#start-here">Start here</a>`},
		{"duplicate heading anchor", `<a href="#start-here-1">again</a>`},
		{"folder without README", `<a href="index.html">all specs</a>`},
		{"outside the docs", `the CI script (` + DefaultGitHubBase + `scripts/agent-ci.sh)`},
		{"file that is not a page", `data (` + DefaultGitHubBase + `docs/specs/data.json)`},
		{"unrendered archive", `old notes (` + DefaultGitHubBase + `docs/source/old.md)`},
		{"external", `<a href="https://example.test/page">the project site</a>`},
		{"unsafe scheme", `Unsafe: click.</li>`}, // text only, no address
	}
	for _, c := range cases {
		if !strings.Contains(first, c.want) {
			t.Errorf("%s: %q not in page", c.name, c.want)
		}
	}
	if strings.Contains(first, "javascript:") || mdHrefRE.MatchString(first) {
		t.Error("an unsafe or unrewritten link is left in the page")
	}
	if !strings.Contains(first, `<h2 id="start-here">`) || !strings.Contains(first, `<h2 id="start-here-1">`) {
		t.Error("heading ids missing")
	}
	// Folder links resolve to the folder's README page when there is one.
	root := string(b.Files["docs-README.html"])
	for _, want := range []string{`<a href="runbooks-README.html">Runbooks</a>`, `<a href="index.html">Specs</a>`, `<a href="01-first.html#start-here">`} {
		if !strings.Contains(root, want) {
			t.Errorf("docs-README.html lacks %q", want)
		}
	}
	// A page in another folder.
	if !strings.Contains(string(b.Files["arch.html"]), `<a href="01-first.html">first runbook</a>`) {
		t.Error("link from architecture/ not rewritten")
	}
}

func TestBuildBrokenLinksAreReported(t *testing.T) {
	b := buildFixture(t)
	var msgs []string
	for _, w := range b.Warnings {
		if w.Kind == WarnBrokenLink {
			msgs = append(msgs, w.Path+": "+w.Message)
		}
	}
	want := []string{
		`runbooks/02-second.md: link "03-missing.md": file not found`,
		`runbooks/02-second.md: link "01-first.md#nope": no heading with that anchor in runbooks/01-first.md`,
	}
	if strings.Join(msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("broken links:\n%s\nwant:\n%s", strings.Join(msgs, "\n"), strings.Join(want, "\n"))
	}
	// The page still renders; the dead link is plain text, a dead anchor stays a link.
	second := string(b.Files["02-second.html"])
	if strings.Contains(second, `href="03-missing`) || !strings.Contains(second, "the missing page") {
		t.Errorf("dead link rendering: %s", second)
	}
}

func TestBuildSelfContainedOutput(t *testing.T) {
	b := buildFixture(t)
	for name, body := range b.Files {
		if strings.HasSuffix(name, ".html") {
			assertSelfContained(t, name, body)
		}
	}
	first := string(b.Files["01-first.html"])
	if strings.Contains(first, "https://example.test/pic.png") || !strings.Contains(first, `<span class="noimage">[a remote picture]</span>`) {
		t.Error("a remote image was not replaced by its text")
	}
	if !strings.Contains(first, `<img src="data:image/gif;base64,`) {
		t.Error("an inline data: image was dropped")
	}
	mini := string(b.Files["mini.html"])
	if strings.Contains(mini, "<script") || strings.Contains(mini, "alert(") && !strings.Contains(mini, "raw HTML omitted") {
		t.Errorf("raw HTML reached the page: %s", mini)
	}
	if !strings.Contains(mini, `<div class="pagebreak"></div>`) || !strings.Contains(mini, `align="center"`) {
		t.Error("page break or table alignment missing (both must avoid inline styles)")
	}
	if !strings.Contains(mini, `<link rel="stylesheet" href="print.css">`) {
		t.Error("the page does not link its CSS")
	}
	for _, css := range []string{"print.css", "card.css"} {
		if cssURLRE.Match(b.Files[css]) {
			t.Errorf("%s reaches for the network", css)
		}
	}
}

func TestBuildWarnsAboutMalformedDocuments(t *testing.T) {
	src := writeTree(t, map[string]string{
		"a.md": "---\ntitle_es: [unclosed\n---\n# Bad front matter\n",
		"b.md": "---\nnever closed\n# Heading\n",
		"c.md": "# Bytes \xff\xfe and a NUL \x00 here\n\n```\nunclosed fence\n",
		"d.md": "",
	})
	b, err := Build(BuildOptions{Src: src})
	if err != nil {
		t.Fatal(err)
	}
	notes := map[string]bool{}
	for _, w := range b.Warnings {
		if w.Kind == WarnDocument {
			notes[w.Path] = true
		}
	}
	for _, p := range []string{"a.md", "b.md", "c.md"} {
		if !notes[p] {
			t.Errorf("no warning for %s", p)
		}
	}
	if notes["d.md"] {
		t.Error("an empty file is not malformed")
	}
	for _, d := range b.Pages {
		if len(b.Files[d.HTML]) == 0 {
			t.Errorf("%s produced no page", d.Rel)
		}
		if d.Rel == "d.md" && d.Title != "d" {
			t.Errorf("a document with no heading is titled %q, want its slug", d.Title)
		}
	}
	if !strings.Contains(string(b.Files["c.html"]), "unclosed fence") {
		t.Error("the content of an unclosed fence was lost")
	}
}

func TestBuildErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := writeTree(t, map[string]string{"ok.md": "# Ok\n"})
	if err := os.Symlink(filepath.Join(unreadable, "nowhere.md"), filepath.Join(unreadable, "dangling.md")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		src  string
	}{
		{"missing folder", filepath.Join(t.TempDir(), "nope")},
		{"not a folder", file},
	}
	for _, c := range cases {
		_, err := Build(BuildOptions{Src: c.src})
		e, ok := err.(*Error)
		if !ok || e.Kind != KindIO {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A dangling symlink named *.md is not a regular file and is skipped, not a crash.
	if b, err := Build(BuildOptions{Src: unreadable}); err != nil || len(b.Pages) != 1 {
		t.Errorf("dangling link: %v", err)
	}
}

func TestSlugAssignment(t *testing.T) {
	got := assignSlugs([]string{"a/x.md", "b/x.md", "y.md", "c/README.md", "README.md"})
	want := map[string]string{"a/x.md": "a-x", "b/x.md": "b-x", "y.md": "y", "c/README.md": "c-README", "README.md": "docs-README"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Start here":               "start-here",
		"4.22 `aictl docs render`": "422-aictl-docs-render",
		"A & B":                    "a--b",
		"Café menu_item-1":         "café-menu_item-1",
		"3.8.29 GET /docs/index":   "3829-get-docsindex",
		"":                         "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
