package ark_test

import (
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

var updateStartHere = flag.Bool("update", false, "rewrite the START-HERE golden files")

func fullInput() ark.StartHereInput {
	return ark.StartHereInput{
		Overall: "yellow", SurvivalCapable: true,
		LastSync:       time.Date(2026, 11, 2, 4, 2, 9, 0, time.UTC),
		CatalogVersion: "2026.11.02-1", WrittenBy: "ai-n3",
		GeneratedAt: time.Date(2026, 11, 2, 4, 2, 10, 0, time.UTC),
		Items: []ark.StartHereItem{
			{ID: "qwen3.5-9b-q4km", Name: "Qwen 3.5 9B (Q4_K_M)", Version: "0123456789ab", Bytes: 5870000000, Status: "verified"},
			{ID: "wikipedia-en-nopic", Name: "Wikipedia, English, no pictures", Version: "2026-07", Bytes: 47800000000, Status: "stale"},
			{ID: "stackexchange-electronics", Name: "Stack Exchange: Electronics", Status: "skipped"},
			{ID: "docs-bundle", Version: "catalog-2026.11.02-1", Bytes: 4200000, Status: "verified"},
			{ID: "old-thing", Version: "1", Bytes: 999, Status: "orphan"},
		},
	}
}

func allLinks() ark.StartHereLinks {
	return ark.StartHereLinks{
		RebuildGuide: true, DocsIndex: true, Survival: true, Ready: true, KiwixTools: true, KiwixLibrary: true,
		Cards: []ark.StartHereCard{{NodeID: "ai-n3", Href: "localai/docs/survival-card-ai-n3.pdf"}, {NodeID: "ai-n1", Href: "localai/docs/survival-card-ai-n1.html"}},
	}
}

func goldenCheck(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "starthere", name)
	if *updateStartHere {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file (run go test ./internal/ark -run StartHere -update and review the diff)", name)
	}
}

func TestStartHereGolden(t *testing.T) {
	full, err := ark.RenderStartHere(fullInput(), allLinks())
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "full.html", full)

	// A drive that was initialised and never synced: no report, no manuals, nothing to link to.
	bare, err := ark.RenderStartHere(ark.StartHereInput{}, ark.StartHereLinks{})
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "bare.html", bare)

	again, _ := ark.RenderStartHere(fullInput(), allLinks())
	if string(again) != string(full) {
		t.Error("the same input gave different bytes")
	}
}

func TestStartHereSaysWhatItMust(t *testing.T) {
	page, err := ark.RenderStartHere(fullInput(), allLinks())
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	for _, want := range []string{
		"AI-ARK drive. Start here.", "Disco AI-ARK. Empiece aquí.",
		"http://127.0.0.1:7070", "http://127.0.0.1:8888",
		"kiwix-serve --port 8888 --library localai/kiwix/library.xml",
		"localai/kit/tools/kiwix-tools/",
		`href="localai/docs/06-rebuild-a-node-offline.html"`, `href="localai/docs/index.html"`,
		`href="localai/docs/01-survival-mode.html"`, `href="localai/docs/survival-card-ai-n3.pdf"`,
		`href="localai/docs/survival-card-ai-n1.html"`, `href="localai/READY.json"`,
		"2026-11-02 04:02 UTC", "2026.11.02-1", "ai-n3",
		"Qwen 3.5 9B (Q4_K_M)", "5.9 GB", "47.8 GB", "docs-bundle", "verified / ", "verificado",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// English first, then Spanish, in every block.
	if en, es := strings.Index(s, "If you have a working AI computer"), strings.Index(s, "Si tiene un equipo de IA que funciona"); en < 0 || es < en {
		t.Errorf("English at %d, Spanish at %d", en, es)
	}
}

// A name from the catalog or a manifest is data: it is escaped, never markup.
func TestStartHereEscapesWhatItIsGiven(t *testing.T) {
	page, err := ark.RenderStartHere(ark.StartHereInput{CatalogVersion: `<b>1</b>`, WrittenBy: `"><script>x</script>`, Items: []ark.StartHereItem{
		{ID: "x", Name: `<script>alert(1)</script><img src="http://evil.example.test/a.png">`, Version: `"><a href="http://evil.example.test">`, Status: "verified"},
	}}, ark.StartHereLinks{})
	if err != nil {
		t.Fatal(err)
	}
	assertNoActiveContent(t, string(page))
	if !strings.Contains(string(page), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the name is not escaped text")
	}
}

func TestStartHereNeedsNothingFromTheOutside(t *testing.T) {
	for name, in := range map[string]struct {
		in    ark.StartHereInput
		links ark.StartHereLinks
	}{
		"full": {fullInput(), allLinks()},
		"bare": {ark.StartHereInput{}, ark.StartHereLinks{}},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := ark.RenderStartHere(in.in, in.links)
			if err != nil {
				t.Fatal(err)
			}
			assertSelfContained(t, string(page))
		})
	}
}

var (
	tagRE  = regexp.MustCompile(`<[a-zA-Z][^>]*>`)
	attrRE = regexp.MustCompile(`(?i)\s(href|src|action|srcset|poster|data|formaction)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	addrRE = regexp.MustCompile(`https?://[^\s<"')]*`)
)

// assertNoActiveContent is the offline rule on the markup: no script, no embedded or remote resource,
// and no link with a scheme or a host. It reads real tags only; escaped text is not markup.
func assertNoActiveContent(t *testing.T, page string) {
	t.Helper()
	low := strings.ToLower(page)
	for _, bad := range []string{"<script", "<link", "<iframe", "<img", "<object", "<embed", "<video", "<audio", "<form", "@import", "url(", "javascript:", "<base", "<meta http-equiv"} {
		if strings.Contains(low, bad) {
			t.Errorf("the page contains %q", bad)
		}
	}
	for _, tag := range tagRE.FindAllString(page, -1) {
		for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
			v := m[3] + m[4] + m[5]
			if !strings.EqualFold(m[1], "href") {
				t.Errorf("attribute %s=%q fetches a resource", m[1], v)
				continue
			}
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(v, "/") || strings.Contains(v, "..") {
				t.Errorf("href %q is not a plain relative path", v)
			}
		}
	}
}

// assertSelfContained adds the rule on the text: the only addresses in the page are the two local ones,
// and they are never links.
func assertSelfContained(t *testing.T, page string) {
	t.Helper()
	assertNoActiveContent(t, page)
	for _, m := range addrRE.FindAllString(page, -1) {
		if m != "http://127.0.0.1:7070" && m != "http://127.0.0.1:8888" {
			t.Errorf("an address in the page: %s", m)
		}
	}
}

// pageLinks lists the href targets of the real tags of a page.
func pageLinks(page string) []string {
	var out []string
	for _, tag := range tagRE.FindAllString(page, -1) {
		for _, m := range attrRE.FindAllStringSubmatch(tag, -1) {
			out = append(out, m[3]+m[4]+m[5])
		}
	}
	return out
}

// fixtureDrive builds a synced AI-ARK drive in a temp dir and returns its mount.
func fixtureDrive(t *testing.T, files ...string) string {
	t.Helper()
	mount := t.TempDir()
	for _, f := range files {
		writeFile(t, filepath.Join(mount, filepath.FromSlash(f)), "x\n")
	}
	return mount
}

var syncedDrive = []string{
	"localai/ROOT.json", "localai/READY.json",
	"localai/docs/index.html", "localai/docs/index.json", "localai/docs/01-survival-mode.html",
	"localai/docs/06-rebuild-a-node-offline.html", "localai/docs/04-ark-drive.html",
	"localai/docs/survival-card-ai-n3.pdf", "localai/docs/survival-card-ai-n3.html", "localai/docs/survival-card-ai-n1.html",
	"localai/kiwix/library.xml", "localai/kit/tools/kiwix-tools/kiwix-tools_linux-x86_64-3.7.0.tar.gz",
}

// Every relative link of the page resolves on a synced drive, and a page written on a drive that lacks
// the manuals has no link at all (and says so).
func TestStartHereLinksResolveOnASyncedDrive(t *testing.T) {
	fs := newFS()
	mount := fixtureDrive(t, syncedDrive...)
	if err := ark.WriteStartHere(fs, mount, fullInput()); err != nil {
		t.Fatal(err)
	}
	page := readFile(t, filepath.Join(mount, "START-HERE.html"))
	assertSelfContained(t, page)
	links := pageLinks(page)
	if len(links) < 6 {
		t.Fatalf("only %d links in the page", len(links))
	}
	for _, target := range links {
		if !exists(filepath.Join(mount, filepath.FromSlash(target))) {
			t.Errorf("the link %q does not resolve on the drive", target)
		}
	}
	if !strings.Contains(page, "survival-card-ai-n3.pdf") || strings.Contains(page, "survival-card-ai-n3.html") {
		t.Error("the PDF of a card must win over its HTML")
	}
	if !strings.Contains(page, "survival-card-ai-n1.html") {
		t.Error("a card with no PDF links its HTML")
	}
	if m := mode(t, filepath.Join(mount, "START-HERE.html")); m != 0o644 {
		t.Errorf("mode %o, want 644", m)
	}
	if names := names(t, mount); strings.Join(names, ",") != "START-HERE.html,localai" {
		t.Errorf("top level %v", names)
	}

	t.Run("a drive without the manuals gets no dead link", func(t *testing.T) {
		mount := fixtureDrive(t, "localai/ROOT.json")
		if err := ark.WriteStartHere(fs, mount, ark.StartHereInput{}); err != nil {
			t.Fatal(err)
		}
		page := readFile(t, filepath.Join(mount, "START-HERE.html"))
		assertSelfContained(t, page)
		if m := pageLinks(page); len(m) != 0 {
			t.Errorf("links on a bare drive: %v", m)
		}
		for _, want := range []string{"not on this drive yet", "todavía no está en este disco", "No readiness report yet"} {
			if !strings.Contains(page, want) {
				t.Errorf("the bare page lacks %q", want)
			}
		}
	})
	t.Run("a symbolic link does not count as a manual", func(t *testing.T) {
		mount := fixtureDrive(t, "localai/docs/index.html")
		outside := filepath.Join(t.TempDir(), "outside.html")
		writeFile(t, outside, "x")
		if err := os.Symlink(outside, filepath.Join(mount, "localai/docs/01-survival-mode.html")); err != nil {
			t.Skip(err)
		}
		l := ark.ProbeStartHereLinks(fs, mount)
		if !l.DocsIndex || l.Survival {
			t.Errorf("%+v", l)
		}
	})
	t.Run("a card of a strange name is ignored", func(t *testing.T) {
		mount := fixtureDrive(t, "localai/docs/survival-card-.pdf", "localai/docs/survival-card-A B.pdf", "localai/docs/survival-card-ok1.html")
		l := ark.ProbeStartHereLinks(fs, mount)
		if len(l.Cards) != 1 || l.Cards[0].NodeID != "ok1" {
			t.Errorf("%+v", l.Cards)
		}
	})
}

func TestStartHereWriteFailures(t *testing.T) {
	t.Run("the page is replaced atomically and no temp file is left", func(t *testing.T) {
		fs := newFS()
		mount := fixtureDrive(t, "localai/ROOT.json")
		writeFile(t, filepath.Join(mount, "START-HERE.html"), "old page")
		if err := ark.WriteStartHere(fs, mount, fullInput()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(readFile(t, filepath.Join(mount, "START-HERE.html")), "old page") {
			t.Error("the old page is still there")
		}
		if got := names(t, mount); len(got) != 2 {
			t.Errorf("top level %v", got)
		}
	})
	t.Run("a failed rename keeps the old page whole and leaves nothing behind", func(t *testing.T) {
		fs := newFS()
		mount := fixtureDrive(t, "localai/ROOT.json")
		writeFile(t, filepath.Join(mount, "START-HERE.html"), "old page")
		fs.FailOn("rename", -1)
		if err := ark.WriteStartHere(fs, mount, fullInput()); err == nil {
			t.Fatal("no error")
		}
		if readFile(t, filepath.Join(mount, "START-HERE.html")) != "old page" {
			t.Error("the old page was damaged")
		}
		if got := names(t, mount); len(got) != 2 {
			t.Errorf("top level %v", got)
		}
	})
	t.Run("a directory in the way is refused", func(t *testing.T) {
		fs := newFS()
		mount := fixtureDrive(t, "START-HERE.html/inner")
		if err := ark.WriteStartHere(fs, mount, fullInput()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("a missing mount is an error, not a panic", func(t *testing.T) {
		if err := ark.WriteStartHere(newFS(), filepath.Join(t.TempDir(), "gone"), fullInput()); err == nil {
			t.Fatal("no error")
		}
	})
}

func TestStartHereSize(t *testing.T) {
	page, _ := ark.RenderStartHere(ark.StartHereInput{Items: []ark.StartHereItem{
		{ID: "a", Bytes: 999, Status: "verified"}, {ID: "b", Bytes: 1500, Status: "verified"}, {ID: "c", Bytes: 2_500_000, Status: "verified"},
		{ID: "d", Bytes: 3_000_000_000_000, Status: "verified"}, {ID: "e", Status: "mystery"},
	}}, ark.StartHereLinks{})
	s := string(page)
	for _, want := range []string{"999 B", "1.5 KB", "2.5 MB", "3.0 TB", `mystery / <span lang="es">mystery</span>`} {
		if !strings.Contains(s, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}
