package docsrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoDocs is the real documentation tree of this repository: the renderer must handle every file in
// it, because these are the manuals an operator reads with no internet.
func repoDocs(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(p, "runbooks")); err != nil || !fi.IsDir() {
		t.Skip("the repository docs tree is not available")
	}
	return p
}

var (
	linkTagRE   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	scriptTagRE = regexp.MustCompile(`(?is)<(script|iframe|object|embed|video|audio|source)\b`)
	imgTagRE    = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	cssURLRE    = regexp.MustCompile(`(?i)url\(|@import|https?://`)
	remoteRE    = regexp.MustCompile(`(?i)(src|href)\s*=\s*"(https?:)?//`)
	styleAttrRE = regexp.MustCompile(`(?i)<[a-z][^>]*\sstyle\s*=`)
)

// assertSelfContained fails when an output page could fetch anything: a remote <link>, <img> or
// <script>, or a CSS url()/@import.
func assertSelfContained(t *testing.T, name string, b []byte) {
	t.Helper()
	s := string(b)
	for _, tag := range linkTagRE.FindAllString(s, -1) {
		if remoteRE.MatchString(tag) || !strings.Contains(tag, `rel="stylesheet"`) {
			t.Errorf("%s: unexpected <link>: %s", name, tag)
		}
	}
	for _, tag := range imgTagRE.FindAllString(s, -1) {
		if !strings.Contains(tag, `src="data:image/`) {
			t.Errorf("%s: <img> that is not a data: URI: %s", name, tag)
		}
	}
	if m := scriptTagRE.FindString(s); m != "" {
		t.Errorf("%s: forbidden element %s", name, m)
	}
	if styleAttrRE.MatchString(s) {
		t.Errorf("%s: inline style attribute (the CSP of /docs/ forbids it)", name)
	}
}

func TestRealDocsTreeRendersCleanly(t *testing.T) {
	src := repoDocs(t)
	b, err := Build(BuildOptions{Src: src, CardFile: "survival-card-ai-n3.html"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Pages) < 20 {
		t.Fatalf("only %d pages rendered", len(b.Pages))
	}
	for _, w := range b.Warnings {
		t.Errorf("warning %d in %s: %s", w.Kind, w.Path, w.Message)
	}
	slugs := map[string]string{}
	for _, d := range b.Pages {
		if prev, dup := slugs[d.Slug]; dup {
			t.Errorf("slug %q is used by %s and %s", d.Slug, prev, d.Rel)
		}
		slugs[d.Slug] = d.Rel
		html := b.Files[d.HTML]
		if len(html) == 0 {
			t.Errorf("%s: no output", d.Rel)
			continue
		}
		assertSelfContained(t, d.HTML, html)
		if d.Title == "" || d.Title == d.Slug {
			t.Errorf("%s: no # heading for a title", d.Rel)
		}
		if strings.ContainsAny(string(html), "") {
			t.Errorf("%s: a placeholder sentinel leaked into the page", d.Rel)
		}
	}
	for _, css := range []string{"print.css", "card.css"} {
		if cssURLRE.Match(b.Files[css]) {
			t.Errorf("%s fetches something", css)
		}
	}
	if b.CardTemplate == nil {
		t.Error("the survival card template was not found")
	}
	if _, ok := slugs["01-survival-mode"]; !ok {
		t.Error("the survival runbook has no page")
	}
}
