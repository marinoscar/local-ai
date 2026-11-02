package catalog

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeHub answers HEAD requests the way Hugging Face and plain servers do. Every host of the
// catalog is routed to it by hubTransport, so the fixture keeps its example.test URLs.
type fakeHub struct {
	cat      *Catalog
	requests atomic.Int64
	// per-file overrides, keyed by the model file name
	size     map[string]int64
	oid      map[string]string
	status   map[string]int // HTTP status for the resolve URL of a file
	direct   map[string]bool
	plain    map[string]bool // redirect without X-Linked-Size (not an LFS file)
	notFound map[string]bool // path substrings answered with 404 (ark URLs)
}

func newFakeHub(t *testing.T) (*fakeHub, LintOptions, string) {
	dir := validDir(t)
	cat, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{cat: cat, size: map[string]int64{}, oid: map[string]string{}, status: map[string]int{}, direct: map[string]bool{}, plain: map[string]bool{}, notFound: map[string]bool{}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: hubTransport{host: u.Host}}
	return h, LintOptions{Online: true, Client: client}, dir
}

type hubTransport struct{ host string }

func (h hubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = h.host
	return http.DefaultTransport.RoundTrip(r2)
}

func (h *fakeHub) find(name string) (ModelFile, bool) {
	for _, m := range h.cat.Models {
		for _, f := range m.Files {
			if f.Name == name {
				return f, true
			}
		}
	}
	return ModelFile{}, false
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.requests.Add(1)
	if r.Method != http.MethodHead {
		http.Error(w, "HEAD only", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	for sub := range h.notFound {
		if strings.Contains(p, sub) {
			http.NotFound(w, r)
			return
		}
	}
	switch {
	case strings.Contains(p, "/resolve/"):
		name := p[strings.Index(p, "/resolve/")+len("/resolve/"):]
		name = name[strings.Index(name, "/")+1:]
		f, ok := h.find(name)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if st := h.status[name]; st != 0 {
			w.WriteHeader(st)
			return
		}
		size, oid := f.SizeBytes, f.SHA256
		if v, ok := h.size[name]; ok {
			size = v
		}
		if v, ok := h.oid[name]; ok {
			oid = v
		}
		if h.direct[name] {
			// a small file stored in git: 200 with Content-Length, no LFS headers
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			return
		}
		if !h.plain[name] {
			w.Header().Set("X-Linked-Size", strconv.FormatInt(size, 10))
			w.Header().Set("X-Linked-Etag", `"`+oid+`"`)
		}
		w.Header().Set("Location", "/cdn/"+name)
		w.WriteHeader(http.StatusFound)
	case strings.HasPrefix(p, "/cdn/"):
		name := strings.TrimPrefix(p, "/cdn/")
		f, _ := h.find(name)
		size := f.SizeBytes
		if v, ok := h.size[name]; ok {
			size = v
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	case strings.HasPrefix(p, "/models/"):
		name := strings.TrimPrefix(p, "/models/")
		f, _ := h.find(name)
		size := f.SizeBytes
		if v, ok := h.size[name]; ok {
			size = v
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	default:
		// ark URLs, github assets, kiwix listings
		w.WriteHeader(http.StatusOK)
	}
}

func TestOnlineAllGood(t *testing.T) {
	h, opts, dir := newFakeHub(t)
	rep := Lint(dir, opts)
	if !rep.Valid() {
		t.Fatalf("online lint of the valid fixture:\n%s", problemsText(rep))
	}
	// 9 Hugging Face files + 1 url model file + 9 ark URLs (3 kiwix, 4 url, 1 release, 1 git): every URL is checked once.
	if n := h.requests.Load(); n != 19 {
		t.Errorf("%d HEAD requests, want 19", n)
	}
}

func TestOnlineProblems(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *fakeHub)
		code  string
		file  string
		path  string
		msg   string
	}{
		{"HF size mismatch", func(h *fakeHub) { h.size["Model-Alpha-9B-Q4_K_M.gguf"] = 1234 }, CodePinning, fM, "models[0].files[0].name", "size_bytes is 5870000000 but the server reports 1234"},
		{"HF oid mismatch", func(h *fakeHub) { h.oid["Model-Beta-1B-Q4_K_M.gguf"] = strings.Repeat("e", 64) }, CodePinning, fM, "models[1].files[1].name", "LFS oid"},
		{"HF file not found", func(h *fakeHub) { h.status["ggml-small.bin"] = http.StatusNotFound }, CodePinning, fM, "models[4].files[0].name", "HTTP 404"},
		{"HF non-LFS file with a wrong Content-Length", func(h *fakeHub) {
			h.direct["chat_template.jinja"] = true
			h.size["chat_template.jinja"] = 1
		}, CodePinning, fM, "models[1].files[2].name", "reports 1"},
		{"url model size mismatch", func(h *fakeHub) { h.size["model-delta-embed-q8.gguf"] = 5 }, CodePinning, fM, "models[3].files[0].url", "size_bytes is 140000000 but the server reports 5"},
		{"ark url not found", func(h *fakeHub) { h.notFound["tool-bundle"] = true }, CodePinning, fA, "content[7].source.url", "HTTP 404"},
		{"github release asset not found", func(h *fakeHub) { h.notFound["map-assets-1.2.3.tar.gz"] = true }, CodePinning, fA, "content[4].source.asset", "HTTP 404"},
		{"kiwix base_url not found", func(h *fakeHub) { h.notFound["/zim/alpha/"] = true }, CodePinning, fA, "content[0].source.base_url", "HTTP 404"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, opts, dir := newFakeHub(t)
			c.setup(h)
			rep := Lint(dir, opts)
			if !has(rep, c.code, c.file, c.path) {
				t.Fatalf("want %s %s:%s, got:\n%s", c.code, c.file, c.path, problemsText(rep))
			}
			found := false
			for _, p := range rep.Problems {
				found = found || (p.Path == c.path && strings.Contains(p.Message, c.msg))
			}
			if !found {
				t.Errorf("no message for %s contains %q:\n%s", c.path, c.msg, problemsText(rep))
			}
		})
	}
}

func TestOnlineNonLFSRedirectFollowsToContentLength(t *testing.T) {
	// A redirect without X-Linked-Size is not an LFS file: the check follows it and compares the
	// Content-Length of the final answer.
	h, opts, dir := newFakeHub(t)
	h.plain["chat_template.jinja"] = true
	if rep := Lint(dir, opts); !rep.Valid() {
		t.Fatal(problemsText(rep))
	}
	h.size["chat_template.jinja"] = 7
	rep := Lint(dir, opts)
	if !has(rep, CodePinning, fM, "models[1].files[2].name") {
		t.Fatalf("size mismatch behind a plain redirect not found:\n%s", problemsText(rep))
	}
}

func TestOnlineSkippedWhenOfflineRulesFail(t *testing.T) {
	h, opts, dir := newFakeHub(t)
	applyEdits(t, dir, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	rep := Lint(dir, opts)
	if len(rep.Problems) != 1 || rep.Problems[0].Code != CodePinning {
		t.Fatalf("%s", problemsText(rep))
	}
	if n := h.requests.Load(); n != 0 {
		t.Errorf("%d requests were made although the offline rules failed", n)
	}
}

func TestOnlineNetworkFailureIsReported(t *testing.T) {
	dir := validDir(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL)
	srv.Close() // nothing listens any more
	rep := Lint(dir, LintOptions{Online: true, Client: &http.Client{Transport: hubTransport{host: u.Host}}})
	if rep.Valid() {
		t.Fatal("unreachable hosts must be reported")
	}
	for _, p := range rep.Problems {
		if p.Code != CodePinning || !strings.Contains(p.Message, "could not check") {
			t.Fatalf("unexpected problem: %+v", p)
		}
	}
}

func TestOnlineEndpointOverride(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if strings.Contains(r.URL.Path, "/resolve/") && gotPath == "" {
			gotPath = r.URL.Path
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := validDir(t)
	// Only the Hugging Face and GitHub hosts are redirected; everything else would hit the real
	// example.test names, so this test only inspects the first resolve URL and ignores the report.
	opts := LintOptions{Online: true, HFEndpoint: srv.URL, GitHubEndpoint: srv.URL, Client: &http.Client{Transport: hubTransport{host: strings.TrimPrefix(srv.URL, "http://")}}}
	Lint(dir, opts)
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(gotPath, "/org-") || !strings.Contains(gotPath, "/resolve/") {
		t.Errorf("unexpected HF path %q", gotPath)
	}
}
