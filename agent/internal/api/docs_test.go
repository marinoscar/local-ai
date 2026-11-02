package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const docsIndexJSON = `{"rendered_at":"2026-11-02T10:00:00Z","docs":[
 {"slug":"01-survival-mode","title":"Survival mode","title_es":"Modo de supervivencia","html":"/docs/01-survival-mode.html","pdf":"/docs/01-survival-mode.pdf"},
 {"slug":"02-no-pdf","title":"No PDF","title_es":"Sin PDF","html":"/docs/02-no-pdf.html"},
 {"slug":"","title":"No slug","title_es":"","html":"/docs/x.html"},
 {"slug":"evil","title":"Evil","title_es":"","html":"/docs/../../etc/passwd"},
 {"slug":"offsite","title":"Offsite","title_es":"","html":"https://example.test/x.html"},
 {"slug":"03-bad-pdf","title":"Bad pdf","title_es":"","html":"/docs/03.html","pdf":"/etc/passwd"}
]}`

type docsFixture struct {
	base     string
	internal string
	drive    string
	outside  string
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newDocsFixture builds an internal root with rendered docs, a drive root with others, and files
// outside both that must never be served.
func newDocsFixture(t *testing.T) docsFixture {
	t.Helper()
	base := t.TempDir()
	f := docsFixture{base: base, internal: filepath.Join(base, "srv"), drive: filepath.Join(base, "drive"), outside: filepath.Join(base, "outside")}
	d := filepath.Join(f.internal, "docs")
	writeFile(t, filepath.Join(d, "index.json"), docsIndexJSON)
	writeFile(t, filepath.Join(d, "index.html"), "<html>internal index</html>")
	writeFile(t, filepath.Join(d, "01-survival-mode.html"), "<html>survival</html>")
	writeFile(t, filepath.Join(d, "01-survival-mode.pdf"), "%PDF-1.4 0123456789")
	writeFile(t, filepath.Join(d, "style.css"), "body{}")
	writeFile(t, filepath.Join(d, "sub", "index.html"), "<html>sub</html>")
	writeFile(t, filepath.Join(d, ".env"), "SECRET=1")
	writeFile(t, filepath.Join(d, ".git", "config"), "[core]")
	writeFile(t, filepath.Join(f.drive, "docs", "index.json"), `{"rendered_at":"2026-01-01T00:00:00Z","docs":[]}`)
	writeFile(t, filepath.Join(f.drive, "docs", "index.html"), "<html>drive index</html>")
	writeFile(t, filepath.Join(f.outside, "secret.txt"), "TOP-SECRET")
	writeFile(t, filepath.Join(f.outside, "secret.html"), "<html>TOP-SECRET</html>")
	return f
}

// rawGet sends raw bytes over a fresh TCP connection (no client normalizes the path) and returns the
// status and the body of the answer.
func rawGet(t *testing.T, addr, raw string) (int, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no HTTP answer: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func docsServer(t *testing.T, roots func() []string) *Server {
	t.Helper()
	return pageServer(t, DocsRoutes(DocsOptions{Roots: roots})...)
}

func getJSONIndex(t *testing.T, h http.Handler) DocsIndex {
	t.Helper()
	rec := recordOK(t, do(h, req{target: "/api/v1/docs/index", header: map[string]string{"X-Test-Principal": "bearer"}}))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var out DocsIndex
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	return out
}

func TestDocsServe(t *testing.T) {
	f := newDocsFixture(t)
	h := docsServer(t, func() []string { return []string{f.internal, f.drive} }).Handler()

	t.Run("index for /docs/", func(t *testing.T) {
		rec := do(h, req{target: "/docs/"})
		if rec.Code != 200 || rec.Body.String() != "<html>internal index</html>" {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("HTML carries the docs CSP and the other global headers", func(t *testing.T) {
		rec := do(h, req{target: "/docs/01-survival-mode.html"})
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
		got := rec.Header()
		if got.Get("Content-Security-Policy") != "default-src 'self'; img-src 'self' data:" {
			t.Fatalf("CSP = %q", got.Get("Content-Security-Policy"))
		}
		if got.Get("X-Content-Type-Options") != "nosniff" || got.Get("Referrer-Policy") != "no-referrer" || got.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Fatalf("headers: %v", got)
		}
		if got.Get("Content-Type") != "text/html; charset=utf-8" || got.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("headers: %v", got)
		}
	})
	t.Run("a folder serves its index", func(t *testing.T) {
		for _, p := range []string{"/docs/sub/", "/docs/sub"} {
			rec := do(h, req{target: p})
			if rec.Code != 200 || rec.Body.String() != "<html>sub</html>" || rec.Header().Get("Content-Security-Policy") != DocsContentSecurityPolicy {
				t.Fatalf("%s: %d %q %v", p, rec.Code, rec.Body.String(), rec.Header())
			}
		}
	})
	t.Run("non-HTML has no CSP but nosniff", func(t *testing.T) {
		for _, p := range []string{"/docs/01-survival-mode.pdf", "/docs/style.css", "/docs/index.json"} {
			rec := do(h, req{target: p})
			if rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("%s: %d %v", p, rec.Code, rec.Header())
			}
		}
		if ct := do(h, req{target: "/docs/01-survival-mode.pdf"}).Header().Get("Content-Type"); ct != "application/pdf" {
			t.Fatalf("pdf Content-Type = %q", ct)
		}
	})
	t.Run("Range on a PDF", func(t *testing.T) {
		rec := do(h, req{target: "/docs/01-survival-mode.pdf", header: map[string]string{"Range": "bytes=5-8"}})
		if rec.Code != 206 || rec.Body.String() != "1.4 " || rec.Header().Get("Content-Range") != "bytes 5-8/19" {
			t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
		}
	})
	t.Run("multi-range is 416 as an envelope", func(t *testing.T) {
		rec := do(h, req{target: "/docs/01-survival-mode.pdf", header: map[string]string{"Range": "bytes=0-1,4-5"}})
		e := expectError(t, rec, 416, CodeInvalidRequest)
		if e.Error.Details["reason"] != "multiple_ranges" || rec.Header().Get("Content-Range") != "bytes */19" {
			t.Fatalf("%+v %v", e, rec.Header())
		}
	})
	t.Run("HEAD", func(t *testing.T) {
		rec := do(h, req{method: "HEAD", target: "/docs/01-survival-mode.pdf"})
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "19" {
			t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
		}
	})
	t.Run("/docs redirects to /docs/", func(t *testing.T) {
		rec := do(h, req{target: "/docs"})
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/docs/" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
	})
	t.Run("only GET and HEAD", func(t *testing.T) {
		for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			rec := do(h, req{method: m, target: "/docs/index.html", header: jsonHeaders(nil)})
			expectError(t, rec, 405, CodeInvalidRequest)
		}
	})
	t.Run("missing file: page for browsers, envelope for the rest", func(t *testing.T) {
		rec := do(h, req{target: "/docs/nope.html", header: hdr("text/html")})
		if rec.Code != 404 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "LAI-AGT-021") {
			t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
		e := expectError(t, do(h, req{target: "/docs/nope.html"}), 404, CodeNotFound)
		if e.Error.Details["reason"] != "not_found" {
			t.Fatalf("%+v", e)
		}
	})
}

// Attempts to read outside <root>/docs, through the full middleware chain and, for the raw cases,
// over a real connection so nothing normalizes the path first.
func TestDocsTraversalAndSymlinks(t *testing.T) {
	f := newDocsFixture(t)
	d := filepath.Join(f.internal, "docs")
	for name, target := range map[string]string{
		"leak.html":    filepath.Join(f.outside, "secret.html"),
		"leak.txt":     filepath.Join(f.outside, "secret.txt"),
		"leakdir":      f.outside,
		"rel-leak.txt": "../../outside/secret.txt",
		"to-index.txt": "index.html", // fine: stays inside
	} {
		if err := os.Symlink(target, filepath.Join(d, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	// A sibling that shares the prefix of the docs folder: <root>/docs-evil.
	writeFile(t, filepath.Join(f.internal, "docs-evil", "x.txt"), "TOP-SECRET")
	if err := os.Symlink(filepath.Join(f.internal, "docs-evil"), filepath.Join(d, "evil")); err != nil {
		t.Skip(err)
	}
	// The folder holds a link to a file in the content root outside docs/: also refused (docs/ is the root).
	writeFile(t, filepath.Join(f.internal, "models.yaml"), "TOP-SECRET")
	if err := os.Symlink(filepath.Join(f.internal, "models.yaml"), filepath.Join(d, "models.txt")); err != nil {
		t.Skip(err)
	}

	s := docsServer(t, func() []string { return []string{f.internal} })
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	s.port.Store(int32(portOf(t, srv.URL)))
	host := strings.TrimPrefix(srv.URL, "http://")

	paths := []string{
		// raw traversal
		"/docs/../../etc/passwd", "/docs/../index.json", "/docs/sub/../../../etc/passwd", "/docs/..", "/docs/../",
		// encoded traversal, single and double
		"/docs/%2e%2e/%2e%2e/etc/passwd", "/docs/%2E%2E/%2E%2E/etc/passwd", "/docs/..%2f..%2fetc%2fpasswd", "/docs/%2e%2e%2f%2e%2e%2fetc/passwd",
		"/docs/%252e%252e/%252e%252e/etc/passwd", "/docs/..%5c..%5cetc%5cpasswd", "/docs/%c0%ae%c0%ae/etc/passwd", "/docs/..;/..;/etc/passwd",
		// NUL and control characters
		"/docs/index.html%00.png", "/docs/%00", "/docs/index.html%0a", "/docs/a%0d%0ab",
		// absolute-looking, double slash
		"/docs//etc/passwd", "/docs/%2fetc%2fpasswd",
		// dotfiles
		"/docs/.env", "/docs/.git/config", "/docs/%2eenv", "/docs/sub/.hidden", "/docs/.",
		// symlinks that leave docs/
		"/docs/leak.html", "/docs/leak.txt", "/docs/leakdir/secret.txt", "/docs/rel-leak.txt", "/docs/evil/x.txt", "/docs/models.txt", "/docs/leakdir/",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			raw := "GET " + p + " HTTP/1.1\r\nHost: " + host + "\r\nAccept: */*\r\nConnection: close\r\n\r\n"
			status, body := rawGet(t, srv.Listener.Addr().String(), raw)
			if strings.Contains(body, "TOP-SECRET") || strings.Contains(body, "SECRET=1") || strings.Contains(body, "[core]") || strings.Contains(body, "root:") {
				t.Fatalf("%s leaked content: %q", p, body)
			}
			if status != 404 && status != 400 {
				t.Fatalf("%s: status %d, want 404 or 400 (body %q)", p, status, body)
			}
		})
	}

	t.Run("a link inside docs still works", func(t *testing.T) {
		rec := do(s.Handler(), req{target: "/docs/to-index.txt", host: host})
		if rec.Code != 200 || rec.Body.String() != "<html>internal index</html>" {
			t.Fatalf("%d %q", rec.Code, rec.Body.String())
		}
	})
}

func TestDocsRootSelection(t *testing.T) {
	f := newDocsFixture(t)
	var roots []string
	h := docsServer(t, func() []string { return roots }).Handler()

	t.Run("no roots", func(t *testing.T) {
		roots = nil
		idx := getJSONIndex(t, h)
		if idx.Root != nil || idx.RenderedAt != nil || idx.Docs == nil || len(idx.Docs) != 0 {
			t.Fatalf("%+v", idx)
		}
		rec := do(h, req{target: "/api/v1/docs/index", header: map[string]string{"X-Test-Principal": "bearer"}})
		if !strings.Contains(rec.Body.String(), `"docs":[]`) {
			t.Fatalf("docs must be [] not null: %s", rec.Body.String())
		}
		e := expectError(t, do(h, req{target: "/docs/"}), 404, CodeNotFound)
		if e.Error.Details["reason"] != "docs_not_rendered" {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("internal first", func(t *testing.T) {
		roots = []string{f.internal, f.drive}
		idx := getJSONIndex(t, h)
		if rootOf(idx) != filepath.Join(f.internal, "docs") {
			t.Fatalf("root = %q", rootOf(idx))
		}
		if rec := do(h, req{target: "/docs/"}); rec.Body.String() != "<html>internal index</html>" {
			t.Fatalf("served %q", rec.Body.String())
		}
	})
	t.Run("a root without docs/index.json is skipped", func(t *testing.T) {
		bare := filepath.Join(f.base, "bare")
		writeFile(t, filepath.Join(bare, "docs", "index.html"), "<html>bare</html>") // docs but no index.json
		roots = []string{bare, filepath.Join(f.base, "missing"), f.drive}
		idx := getJSONIndex(t, h)
		if rootOf(idx) != filepath.Join(f.drive, "docs") || len(idx.Docs) != 0 {
			t.Fatalf("%+v", idx)
		}
		if rec := do(h, req{target: "/docs/"}); rec.Body.String() != "<html>drive index</html>" {
			t.Fatalf("served %q", rec.Body.String())
		}
	})
	t.Run("roots are read per request", func(t *testing.T) {
		roots = []string{f.drive}
		if rootOf(getJSONIndex(t, h)) != filepath.Join(f.drive, "docs") {
			t.Fatal("drive not used")
		}
		roots = []string{f.internal, f.drive}
		if rootOf(getJSONIndex(t, h)) != filepath.Join(f.internal, "docs") {
			t.Fatal("internal not preferred again")
		}
	})
	t.Run("docs folder that is a link out of the root is not used", func(t *testing.T) {
		root := filepath.Join(f.base, "linked")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(f.outside, "docs-elsewhere", "index.json"), `{"docs":[]}`)
		if err := os.Symlink(filepath.Join(f.outside, "docs-elsewhere"), filepath.Join(root, "docs")); err != nil {
			t.Skip(err)
		}
		roots = []string{root, f.drive}
		if idx := getJSONIndex(t, h); rootOf(idx) != filepath.Join(f.drive, "docs") {
			t.Fatalf("followed a link out of the root: %+v", idx)
		}
	})
}

func TestDocsIndexEndpoint(t *testing.T) {
	f := newDocsFixture(t)
	h := docsServer(t, func() []string { return []string{f.internal} }).Handler()

	idx := getJSONIndex(t, h)
	if rootOf(idx) != filepath.Join(f.internal, "docs") || idx.RenderedAt == nil || *idx.RenderedAt != "2026-11-02T10:00:00Z" {
		t.Fatalf("%+v", idx)
	}
	want := []DocEntry{
		{Slug: "01-survival-mode", Title: "Survival mode", TitleES: "Modo de supervivencia", HTML: "/docs/01-survival-mode.html", PDF: "/docs/01-survival-mode.pdf"},
		{Slug: "02-no-pdf", Title: "No PDF", TitleES: "Sin PDF", HTML: "/docs/02-no-pdf.html"},
		{Slug: "03-bad-pdf", Title: "Bad pdf", HTML: "/docs/03.html"}, // pdf outside /docs/ blanked
	}
	if len(idx.Docs) != len(want) {
		t.Fatalf("docs = %+v", idx.Docs)
	}
	for i := range want {
		if idx.Docs[i] != want[i] {
			t.Errorf("docs[%d] = %+v, want %+v", i, idx.Docs[i], want[i])
		}
	}

	t.Run("wire shape", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/docs/index", header: map[string]string{"X-Test-Principal": "bearer"}})
		var raw map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"root", "rendered_at", "docs"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("missing %q: %s", k, rec.Body.String())
			}
		}
		first := raw["docs"].([]any)[0].(map[string]any)
		for _, k := range []string{"slug", "title", "title_es", "html", "pdf"} {
			if _, ok := first[k]; !ok {
				t.Errorf("entry misses %q", k)
			}
		}
		if second := raw["docs"].([]any)[1].(map[string]any); second["pdf"] != nil {
			t.Errorf("an entry without a PDF must omit it: %v", second)
		}
	})
	t.Run("needs an operator", func(t *testing.T) {
		expectError(t, do(h, req{target: "/api/v1/docs/index"}), 401, CodeUnauthenticated)
		expectError(t, do(h, req{target: "/api/v1/docs/index", header: map[string]string{"X-Test-Principal": "portal"}}), 401, CodePortalSessionEnded)
	})
	t.Run("only GET", func(t *testing.T) {
		expectError(t, do(h, req{method: "POST", target: "/api/v1/docs/index", header: jsonHeaders(nil)}), 405, CodeInvalidRequest)
	})
}

func TestDocsIndexCorrupt(t *testing.T) {
	for name, content := range map[string]string{
		"not json":   "{{{",
		"wrong type": `{"docs": "none"}`,
		"too large":  `{"docs":[],"pad":"` + strings.Repeat("x", maxDocsIndexBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "docs", "index.json"), content)
			writeFile(t, filepath.Join(root, "docs", "index.html"), "<html>still served</html>")
			h := docsServer(t, func() []string { return []string{root} }).Handler()
			idx := getJSONIndex(t, h)
			if rootOf(idx) != filepath.Join(root, "docs") || idx.Docs == nil || len(idx.Docs) != 0 || idx.RenderedAt != nil {
				t.Fatalf("%+v", idx)
			}
			if rec := do(h, req{target: "/docs/"}); rec.Code != 200 {
				t.Fatalf("pages must still be served: %d", rec.Code)
			}
		})
	}
}

func TestDocsLink(t *testing.T) {
	for s, want := range map[string]bool{
		"/docs/a.html": true, "/docs/sub/a.html": true, "/docs/": false, "/docs": false, "": false,
		"/docs/../a": false, "/docs/a/..": false, "https://x.test/docs/a": false, "docs/a": false, `/docs/a\b`: false, "/docs/a\x00": false,
	} {
		if got := docsLink(s); got != want {
			t.Errorf("docsLink(%q) = %v, want %v", s, got, want)
		}
	}
}

// rootOf is the root of an index, "" when null.
func rootOf(i DocsIndex) string {
	if i.Root == nil {
		return ""
	}
	return *i.Root
}
