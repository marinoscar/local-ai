package offlinescan

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func messages(name, text string) []string {
	var out []string
	for _, f := range ScanText(name, text) {
		out = append(out, f.Message)
	}
	return out
}

// The cases below are the cases of scripts/tests/check-offline-assets.test.mjs, with the same inputs
// and the same expected messages.
func TestScanTextRules(t *testing.T) {
	cases := []struct {
		name, file, text string
		want             []string
	}{
		{"svg and xlink namespaces", "a.js", `el("http://www.w3.org/2000/svg"); x("http://www.w3.org/1999/xlink")`, nil},
		{"loopback urls", "a.js", `fetch("http://127.0.0.1:7070/api"); u="http://localhost:8080/x"`, nil},
		{"another host", "a.js", "ok\nfetch(\"https://example.org/x.js\")\n", []string{"https://example.org/x.js"}},
		{"licence comment bang", "a.js", "/*! Lib v1 | MIT | https://example.org/lib/license */\nvar a=1;", nil},
		{"licence comment tag", "a.js", "/** @license see https://example.org/l */\nvar a=1;", nil},
		{"licence file", "LICENSE.txt", "see https://www.apache.org/licenses/LICENSE-2.0", nil},
		{"licence file in a directory, any case", "third_party/License.md", "see https://example.org/l", nil},
		{"same url outside a licence comment", "a.js", `/* a note */ var u = "https://example.org/x";`, []string{"https://example.org/x"}},
		{"react decoder text", "a.js", `var u="https://react.dev/errors/"+e`, nil},
		{"service worker", "a.js", `navigator.serviceWorker.register("/sw.js")`, []string{"service worker registration"}},
		{"service worker across lines", "a.js", "navigator\n  .serviceWorker\n\t.register('/sw.js')", []string{"service worker registration"}},
		{"getRegistration is not a registration", "a.js", `navigator.serviceWorker.getRegistration()`, nil},
		{"uppercase scheme", "a.js", `x("HTTPS://EXAMPLE.ORG/a.js")`, []string{"HTTPS://EXAMPLE.ORG/a.js"}},
		{"a url ends at a no-break space", "a.html", "go to https://example.org/a\u00a0now", []string{"https://example.org/a"}},
		{"a url ends at a quote, a paren, a backslash", "a.js", `a("https://one.example.org/x") b('https://two.example.org/y') c(https://three.example.org/z) "https://four.example.org\q"`,
			[]string{"https://one.example.org/x", "https://two.example.org/y", "https://three.example.org/z", "https://four.example.org"}},
		{"localhost prefix is a prefix match", "a.js", `x("http://localhost.example.org/a")`, nil}, // the script's rule, kept as it is
		{"plain http to another host", "a.js", `x("http://example.org/")`, []string{"http://example.org/"}},
	}
	for _, c := range cases {
		if got := messages(c.file, c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestForbiddenNamesFailEvenInACommentOrOutsideAURL(t *testing.T) {
	for _, word := range []string{"cdn", "CDN", "Cdn", "unpkg", "jsdelivr", "fonts.googleapis", "FONTS.GOOGLEAPIS"} {
		if got := ScanText("a.js", "// loaded from the "+word+" later"); len(got) < 1 {
			t.Errorf("%s: not found", word)
		}
	}
	// Inside a licence comment a URL is fine but a forbidden name is still not.
	got := ScanText("a.js", "/*! built with unpkg */")
	if len(got) != 1 || got[0].Message != `forbidden name "unpkg"` {
		t.Errorf("licence comment with a forbidden name: %v", got)
	}
}

func TestFindingLineNumbers(t *testing.T) {
	f := ScanText("a.js", "ok\nfetch(\"https://example.org/x.js\")\n")
	if len(f) != 1 || f[0].Line != 2 {
		t.Fatalf("%v", f)
	}
	if s := (Finding{File: "assets/a.js", Line: 3, Message: "m"}).String(); s != "assets/a.js:3: m" {
		t.Errorf("String() = %q", s)
	}
	crlf := ScanText("a.js", "a\r\nb\r\nimport 'https://example.org/x'")
	if len(crlf) != 1 || crlf[0].Line != 3 {
		t.Errorf("CRLF line = %v", crlf)
	}
}

func TestScanFSSkipsBinaryAndCountsTextFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"portal.html":        {Data: []byte("<!doctype html><title>x</title>")},
		"assets/a.woff2":     {Data: []byte("cdn unpkg https://example.org")},
		"assets/b.PNG":       {Data: []byte("https://example.org")},
		"maps/world.pmtiles": {Data: []byte("https://example.org")},
	}
	res, err := ScanFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || len(res.Findings) != 0 {
		t.Errorf("result = %+v", res)
	}
	fsys["assets/bad.js"] = &fstest.MapFile{Data: []byte(`import "https://unpkg.com/x.js"`)}
	res, _ = ScanFS(fsys)
	if res.Files != 2 || len(res.Findings) != 2 { // the URL and the forbidden name
		t.Fatalf("result = %+v", res)
	}
	if res.Findings[0].File != "assets/bad.js" || res.Findings[0].Line != 1 || res.Findings[0].Message != "https://unpkg.com/x.js" {
		t.Errorf("finding = %+v", res.Findings[0])
	}
}

func TestScanFSEmptyTree(t *testing.T) {
	res, err := ScanFS(fstest.MapFS{})
	if err != nil || res.Files != 0 || len(res.Findings) != 0 {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func lines(res Result) []string {
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.String())
	}
	return out
}

func golden(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// The committed negative fixture of the script and a tree that exercises the corners (path order across
// directories, case, licence ranges, a no-break space, CRLF, a service worker over three lines) must give
// the findings the script gave when the golden files were written.
func TestMatchesTheScriptOnItsFixtures(t *testing.T) {
	for _, c := range []struct{ dir, golden string }{
		{filepath.Join("..", "..", "..", "scripts", "testdata", "offline-assets", "bad"), "bad.golden"},
		{filepath.Join("testdata", "tree"), "tree.golden"},
	} {
		res, err := ScanFS(os.DirFS(c.dir))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := lines(res), golden(t, c.golden); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", c.dir, got, want)
		}
	}
}

// With node installed the script itself is the oracle: run it on the same trees and compare. (CI's go
// job has no node; the golden files above carry the comparison there.)
func TestMatchesTheScriptLive(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the golden files cover the comparison")
	}
	script := filepath.Join("..", "..", "..", "scripts", "check-offline-assets.mjs")
	for _, dir := range []string{filepath.Join("..", "..", "..", "scripts", "testdata", "offline-assets", "bad"), filepath.Join("testdata", "tree")} {
		cmd := exec.Command(node, script, dir)
		out, _ := cmd.CombinedOutput() // exit 1 when there are findings
		var want []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" && !strings.HasPrefix(l, "✗") && !strings.HasPrefix(l, "✓") {
				want = append(want, l)
			}
		}
		res, err := ScanFS(os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		if got := lines(res); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n go   %q\n node %q", dir, got, want)
		}
	}
}
