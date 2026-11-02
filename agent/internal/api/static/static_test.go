package static

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tree is the fixture: a root with a few files and a sibling directory outside it.
type tree struct {
	base    string // temp dir holding root and outside
	root    string
	outside string
}

func newTree(t *testing.T) tree {
	t.Helper()
	base := t.TempDir()
	tr := tree{base: base, root: filepath.Join(base, "root"), outside: filepath.Join(base, "outside")}
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(tr.root, "index.html"), "<html>home</html>")
	write(filepath.Join(tr.root, "a.txt"), "alpha")
	write(filepath.Join(tr.root, "sub", "b.txt"), "bravo")
	write(filepath.Join(tr.root, "sub", "index.html"), "<html>sub</html>")
	write(filepath.Join(tr.root, ".env"), "SECRET=1")
	write(filepath.Join(tr.root, ".git", "config"), "[core]")
	write(filepath.Join(tr.root, "name%.txt"), "percent")
	write(filepath.Join(tr.outside, "secret.txt"), "TOP-SECRET")
	write(filepath.Join(tr.outside, "dir", "x.txt"), "outside-dir")
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(tr.root, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	link(filepath.Join(tr.outside, "secret.txt"), "leak.txt")      // file escaping the root
	link(tr.outside, "leakdir")                                    // directory escaping the root
	link("../outside/secret.txt", "relleak.txt")                   // relative escape
	link("a.txt", "inside-link.txt")                               // link that stays inside
	link("sub", "inside-dir")                                      // directory link that stays inside
	link(".env", "dotlink.txt")                                    // visible link to a dotfile
	link("loop-b", "loop-a")                                       // loop
	link("loop-a", "loop-b")                                       // loop
	link(filepath.Join(tr.root, "a.txt"), "abs-inside-link.txt")   // absolute link that stays inside
	link("/etc/hostname", "etc.txt")                               // absolute link elsewhere
	link("sub/index.html", filepath.Join("sub", "index-link.txt")) // for the index test below
	return tr
}

func TestSafeJoin(t *testing.T) {
	tr := newTree(t)
	realRoot, err := filepath.EvalSymlinks(tr.root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		req  string
		want error  // nil: success
		rel  string // expected path below the root on success
	}{
		{"file", "a.txt", nil, "a.txt"},
		{"nested", "sub/b.txt", nil, "sub/b.txt"},
		{"directory", "sub", nil, "sub"},
		{"directory trailing slash", "sub/", nil, "sub"},
		{"root empty", "", nil, "."},
		{"root slash", "/", nil, "."},
		{"literal percent in a name", "name%.txt", nil, "name%.txt"},
		{"inside link file", "inside-link.txt", nil, "a.txt"},
		{"inside link dir", "inside-dir/b.txt", nil, "sub/b.txt"},
		{"absolute link inside", "abs-inside-link.txt", nil, "a.txt"},

		{"dotdot", "..", ErrUnsafePath, ""},
		{"dotdot up", "../outside/secret.txt", ErrUnsafePath, ""},
		{"dotdot inside", "sub/../a.txt", ErrUnsafePath, ""},
		{"deep dotdot", "../../../../etc/passwd", ErrUnsafePath, ""},
		{"dot segment", "./a.txt", ErrUnsafePath, ""},
		{"encoded dotdot", "%2e%2e/outside/secret.txt", ErrUnsafePath, ""},
		{"encoded dotdot upper", "%2E%2E/%2E%2E/etc/passwd", ErrUnsafePath, ""},
		{"half encoded", ".%2e/outside/secret.txt", ErrUnsafePath, ""},
		{"encoded slash", "sub%2fb.txt", ErrUnsafePath, ""},
		{"encoded slash upper", "sub%2Fb.txt", ErrUnsafePath, ""},
		{"encoded backslash", "sub%5cb.txt", ErrUnsafePath, ""},
		{"encoded nul", "a.txt%00.png", ErrUnsafePath, ""},
		{"nul", "a.txt\x00.png", ErrUnsafePath, ""},
		{"control char", "a\x01.txt", ErrUnsafePath, ""},
		{"newline", "a\n.txt", ErrUnsafePath, ""},
		{"backslash", `sub\b.txt`, ErrUnsafePath, ""},
		{"backslash dotdot", `..\outside\secret.txt`, ErrUnsafePath, ""},
		{"absolute", "/etc/passwd", ErrUnsafePath, ""},
		{"absolute into root", filepath.Join(tr.root, "a.txt"), ErrUnsafePath, ""},
		{"double slash", "sub//b.txt", ErrUnsafePath, ""},
		{"double slash start", "//etc/passwd", ErrUnsafePath, ""},
		{"dotfile", ".env", ErrUnsafePath, ""},
		{"dot directory", ".git/config", ErrUnsafePath, ""},
		{"dotfile nested", "sub/.hidden", ErrUnsafePath, ""},
		{"link to dotfile", "dotlink.txt", ErrUnsafePath, ""},

		{"symlink file escape", "leak.txt", ErrOutsideRoot, ""},
		{"symlink dir escape", "leakdir/dir/x.txt", ErrOutsideRoot, ""},
		{"symlink dir itself", "leakdir", ErrOutsideRoot, ""},
		{"relative symlink escape", "relleak.txt", ErrOutsideRoot, ""},
		{"absolute link elsewhere", "etc.txt", ErrOutsideRoot, ""},

		{"missing", "nope.txt", ErrNotFound, ""},
		{"missing nested", "sub/nope.txt", ErrNotFound, ""},
		{"file as directory", "a.txt/x", ErrNotFound, ""},
		{"symlink loop", "loop-a", ErrNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SafeJoin(tr.root, tc.req)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("SafeJoin(%q) = %q, %v; want error %v", tc.req, got, err, tc.want)
				}
				if got != "" {
					t.Fatalf("SafeJoin returned a path with an error: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SafeJoin(%q): %v", tc.req, err)
			}
			if want := filepath.Join(realRoot, filepath.FromSlash(tc.rel)); got != want {
				t.Fatalf("SafeJoin(%q) = %q, want %q", tc.req, got, want)
			}
		})
	}
}

func TestSafeJoinRoot(t *testing.T) {
	tr := newTree(t)
	t.Run("empty root", func(t *testing.T) {
		if _, err := SafeJoin("", "a.txt"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing root (drive ejected)", func(t *testing.T) {
		if _, err := SafeJoin(filepath.Join(tr.base, "gone"), "a.txt"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("root is a symlink", func(t *testing.T) {
		alias := filepath.Join(tr.base, "alias")
		if err := os.Symlink(tr.root, alias); err != nil {
			t.Skip(err)
		}
		got, err := SafeJoin(alias, "sub/b.txt")
		if err != nil {
			t.Fatal(err)
		}
		real, _ := filepath.EvalSymlinks(filepath.Join(tr.root, "sub", "b.txt"))
		if got != real {
			t.Fatalf("got %q, want %q", got, real)
		}
		if _, err := SafeJoin(alias, "leak.txt"); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("escape through an aliased root: %v", err)
		}
	})
	t.Run("root with trailing slash and dots", func(t *testing.T) {
		if _, err := SafeJoin(tr.root+"/sub/..//", "a.txt"); err != nil {
			t.Fatalf("a messy but real root must work: %v", err)
		}
	})
	t.Run("sibling prefix is not inside", func(t *testing.T) {
		// root "/x/root" must not accept "/x/root-evil" through a link.
		evil := filepath.Join(tr.base, "root-evil")
		if err := os.MkdirAll(evil, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(evil, "f.txt"), []byte("x"), 0o644)
		if err := os.Symlink(evil, filepath.Join(tr.root, "evil")); err != nil {
			t.Skip(err)
		}
		if _, err := SafeJoin(tr.root, "evil/f.txt"); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("err = %v, want ErrOutsideRoot", err)
		}
	})
}

// rec serves path with method and headers and returns the recorder and the error.
func serve(t *testing.T, method, path string, hdr map[string]string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	r := httptest.NewRequest(method, "/x", nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	err := ServeFile(w, r, path)
	return w, err
}

func TestServeFileRanges(t *testing.T) {
	dir := t.TempDir()
	data := []byte("0123456789abcdefghij") // 20 bytes
	p := filepath.Join(dir, "tiles.pmtiles")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		method       string
		hdr          map[string]string
		status       int
		body         string
		contentRange string
		errStatus    int
		errReason    string
	}{
		{"whole file", "GET", nil, 200, string(data), "", 0, ""},
		{"single range", "GET", map[string]string{"Range": "bytes=2-5"}, 206, "2345", "bytes 2-5/20", 0, ""},
		{"open ended", "GET", map[string]string{"Range": "bytes=15-"}, 206, "fghij", "bytes 15-19/20", 0, ""},
		{"suffix", "GET", map[string]string{"Range": "bytes=-4"}, 206, "ghij", "bytes 16-19/20", 0, ""},
		{"end past size is clamped", "GET", map[string]string{"Range": "bytes=18-999"}, 206, "ij", "bytes 18-19/20", 0, ""},
		{"first byte", "GET", map[string]string{"Range": "bytes=0-0"}, 206, "0", "bytes 0-0/20", 0, ""},
		{"multi range refused", "GET", map[string]string{"Range": "bytes=0-1,5-6"}, 0, "", "bytes */20", 416, "multiple_ranges"},
		{"multi range spaces", "GET", map[string]string{"Range": "bytes=0-1, 5-6, 9-"}, 0, "", "bytes */20", 416, "multiple_ranges"},
		{"unsatisfiable", "GET", map[string]string{"Range": "bytes=50-60"}, 0, "", "bytes */20", 416, "range_not_satisfiable"},
		{"zero suffix", "GET", map[string]string{"Range": "bytes=-0"}, 0, "", "bytes */20", 416, "range_not_satisfiable"},
		{"start at size", "GET", map[string]string{"Range": "bytes=20-"}, 0, "", "bytes */20", 416, "range_not_satisfiable"},
		{"HEAD", "HEAD", nil, 200, "", "", 0, ""},
		{"HEAD with range", "HEAD", map[string]string{"Range": "bytes=2-5"}, 206, "", "bytes 2-5/20", 0, ""},
		{"POST", "POST", nil, 0, "", "", 405, "method_not_allowed"},
		{"PUT", "PUT", nil, 0, "", "", 405, "method_not_allowed"},
		{"DELETE", "DELETE", nil, 0, "", "", 405, "method_not_allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := serve(t, tc.method, p, tc.hdr)
			if tc.errStatus != 0 {
				var se *Error
				if !errors.As(err, &se) || se.Status != tc.errStatus || se.Reason != tc.errReason {
					t.Fatalf("err = %v, want status %d reason %s", err, tc.errStatus, tc.errReason)
				}
				if w.Body.Len() != 0 || w.Code != 200 && w.Code != 0 {
					t.Fatalf("an error must not write anything: %d %q", w.Code, w.Body.String())
				}
				if tc.contentRange != "" && se.Header.Get("Content-Range") != tc.contentRange {
					t.Fatalf("Content-Range = %q", se.Header.Get("Content-Range"))
				}
				if tc.errStatus == 405 && se.Header.Get("Allow") != "GET, HEAD" {
					t.Fatalf("Allow = %q", se.Header.Get("Allow"))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if got := w.Body.String(); got != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
			if got := w.Header().Get("Content-Range"); got != tc.contentRange {
				t.Fatalf("Content-Range = %q, want %q", got, tc.contentRange)
			}
			if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Fatalf("Accept-Ranges = %q", got)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("nosniff missing: %q", got)
			}
			if tc.method == "HEAD" {
				if cl := w.Header().Get("Content-Length"); cl == "" {
					t.Fatal("HEAD must carry Content-Length")
				}
			}
		})
	}
}

func TestServeFileHeaders(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.html": "text/html; charset=utf-8", "a.css": "text/css; charset=utf-8", "a.json": "application/json; charset=utf-8",
		"a.pdf": "application/pdf", "a.woff2": "font/woff2", "a.pmtiles": "application/octet-stream", "A.PNG": "image/png",
	}
	for name, want := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := serve(t, "GET", p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.Header().Get("Content-Type"); got != want {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, want)
		}
		if w.Header().Get("ETag") == "" || w.Header().Get("Last-Modified") == "" {
			t.Errorf("%s: no validators: %v", name, w.Header())
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q", name, got)
		}
	}

	t.Run("caller Cache-Control is kept", func(t *testing.T) {
		p := filepath.Join(dir, "a.css")
		r := httptest.NewRequest("GET", "/x", nil)
		w := httptest.NewRecorder()
		w.Header().Set("Cache-Control", "max-age=60")
		if err := ServeFile(w, r, p); err != nil {
			t.Fatal(err)
		}
		if got := w.Header().Get("Cache-Control"); got != "max-age=60" {
			t.Fatalf("Cache-Control = %q", got)
		}
	})
	t.Run("304 on If-None-Match", func(t *testing.T) {
		p := filepath.Join(dir, "a.css")
		w, _ := serve(t, "GET", p, nil)
		etag := w.Header().Get("ETag")
		w2, err := serve(t, "GET", p, map[string]string{"If-None-Match": etag})
		if err != nil {
			t.Fatal(err)
		}
		if w2.Code != http.StatusNotModified || w2.Body.Len() != 0 {
			t.Fatalf("status = %d body %q", w2.Code, w2.Body.String())
		}
	})
	t.Run("unknown extension is sniffed", func(t *testing.T) {
		p := filepath.Join(dir, "blob.bin")
		_ = os.WriteFile(p, []byte("\x89PNG\r\n\x1a\n...."), 0o644)
		w, err := serve(t, "GET", p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.Header().Get("Content-Type"); got != "image/png" {
			t.Fatalf("Content-Type = %q", got)
		}
	})
}

func TestServeFileNotRegular(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, path string
	}{
		{"directory", dir},
		{"missing", filepath.Join(dir, "missing")},
		{"device", "/dev/null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := serve(t, "GET", tc.path, nil)
			var se *Error
			if !errors.As(err, &se) || se.Status != 404 || se.Reason != "not_found" {
				t.Fatalf("err = %v", err)
			}
			if w.Body.Len() != 0 {
				t.Fatalf("wrote %q", w.Body.String())
			}
		})
	}
}

func TestServeUnder(t *testing.T) {
	tr := newTree(t)
	get := func(rel string) (*httptest.ResponseRecorder, *Error) {
		r := httptest.NewRequest("GET", "/x", nil)
		w := httptest.NewRecorder()
		return w, AsError(ServeUnder(w, r, tr.root, rel, "index.html"))
	}
	tests := []struct {
		rel    string
		status int // 0: expect an *Error with 404
		body   string
	}{
		{"", 200, "<html>home</html>"},
		{"/", 200, "<html>home</html>"},
		{"sub/", 200, "<html>sub</html>"},
		{"sub", 200, "<html>sub</html>"},
		{"a.txt", 200, "alpha"},
		{"inside-dir", 200, "<html>sub</html>"},
		{"leak.txt", 0, ""},
		{"leakdir/dir/x.txt", 0, ""},
		{".env", 0, ""},
		{"../outside/secret.txt", 0, ""},
		{"%2e%2e/outside/secret.txt", 0, ""},
		{"missing", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			w, err := get(tc.rel)
			if tc.status == 0 {
				if err == nil || err.Status != 404 {
					t.Fatalf("err = %v, want 404", err)
				}
				if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "TOP") {
					t.Fatalf("leaked content: %q", w.Body.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("got %d %q", w.Code, w.Body.String())
			}
		})
	}

	t.Run("directory without index", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(tr.root, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/x", nil)
		err := AsError(ServeUnder(httptest.NewRecorder(), r, tr.root, "empty", "index.html"))
		if err == nil || err.Status != 404 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("index that escapes the root", func(t *testing.T) {
		d := filepath.Join(tr.root, "trap")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(tr.outside, "secret.txt"), filepath.Join(d, "index.html")); err != nil {
			t.Skip(err)
		}
		w := httptest.NewRecorder()
		err := AsError(ServeUnder(w, httptest.NewRequest("GET", "/x", nil), tr.root, "trap/", "index.html"))
		if err == nil || err.Status != 404 || strings.Contains(w.Body.String(), "TOP-SECRET") {
			t.Fatalf("err = %v body %q", err, w.Body.String())
		}
	})
}

// A request must not leave descriptors behind: the drive can be ejected right after the response.
func TestServeFileClosesTheFile(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd")
	}
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 1<<16), 0o644); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	before := count()
	for i := 0; i < 50; i++ {
		_, _ = serve(t, "GET", p, nil)
		_, _ = serve(t, "GET", p, map[string]string{"Range": "bytes=0-9"})
		_, _ = serve(t, "GET", p, map[string]string{"Range": "bytes=0-1,4-5"})
		_, _ = serve(t, "POST", p, nil)
	}
	if after := count(); after > before {
		t.Fatalf("descriptors leaked: %d before, %d after", before, after)
	}
}

// A real server: Range over TCP, the .pmtiles case of agent.md section 9.2 scenario 7.
func TestRangeOverTCP(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 4096)
	copy(data, "PMTiles\x03")
	for i := 8; i < len(data); i++ {
		data[i] = byte(i % 251)
	}
	p := filepath.Join(dir, "test-map.pmtiles")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := ServeFile(w, r, p); err != nil {
			se := AsError(err)
			for k, v := range se.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(se.Status)
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(body, data[100:200]) {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
	if resp.Header.Get("Content-Range") != "bytes 100-199/"+strconv.Itoa(len(data)) {
		t.Fatalf("Content-Range = %q", resp.Header.Get("Content-Range"))
	}

	req, _ = http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Range", "bytes=0-9,20-29")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 416 {
		t.Fatalf("multi-range status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/") {
		t.Fatalf("multipart answer: %s", ct)
	}
}

func TestErrorText(t *testing.T) {
	e := &Error{Status: 404, Reason: "not_found", Err: ErrOutsideRoot}
	if !errors.Is(e, ErrOutsideRoot) || !strings.Contains(e.Error(), "404") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if AsError(nil) != nil {
		t.Fatal("AsError(nil)")
	}
	if got := AsError(ErrUnsafePath); got.Status != 404 {
		t.Fatalf("status = %d", got.Status)
	}
	_ = time.Second
}
