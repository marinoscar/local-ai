package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fakehf stands in for huggingface.co: the resolve URL answers HEAD like the real site (a 302 with the
// X-Linked-* headers for an LFS file, a 200 with a git blob ETag for a small one), and the tree API
// lists the revision.

type hfFake struct {
	status        int    // 0 = per kind
	lfsOID        string // X-Linked-Etag; "" = header absent
	lfsSize       string // X-Linked-Size; "" = header absent
	commit        string // X-Repo-Commit; "" = header absent
	small         bool   // a non-LFS file: 200 with an ETag
	contentLength int
	retryAfter    string
	failures      int // answer 429 this many times first
}

type fakeHF struct {
	srv *httptest.Server

	mu       sync.Mutex
	rev      string
	files    map[string]*hfFake  // by file name
	tree     []map[string]any    // the listing
	treeHops string              // redirect the tree API to this URL
	reqs     []string            // "METHOD path"
	auth     map[string][]string // Authorization header values seen, by "METHOD path"
	pages    map[string][]hfPage // paginated listings
}

type hfPage struct {
	entries []map[string]any
	next    string
}

func newFakeHF(t *testing.T, r *webRig, repo, rev string) *fakeHF {
	f := &fakeHF{rev: rev, files: map[string]*hfFake{}, auth: map[string][]string{}}
	f.srv = r.serve(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := req.Method + " " + req.URL.Path
		f.reqs = append(f.reqs, key)
		f.auth[key] = append(f.auth[key], req.Header.Get("Authorization"))
		resolve := "/" + repo + "/resolve/" + rev + "/"
		switch {
		case strings.HasPrefix(req.URL.Path, resolve):
			name := strings.TrimPrefix(req.URL.Path, resolve)
			ff, ok := f.files[name]
			if !ok {
				http.NotFound(w, req)
				return
			}
			if ff.failures > 0 {
				ff.failures--
				w.Header().Set("Retry-After", ff.retryAfter)
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			if ff.status != 0 {
				if ff.retryAfter != "" {
					w.Header().Set("Retry-After", ff.retryAfter)
				}
				w.WriteHeader(ff.status)
				return
			}
			if ff.commit != "" {
				w.Header().Set("X-Repo-Commit", ff.commit)
			}
			if ff.small {
				w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef01234567"`)
				w.Header().Set("Content-Length", strconv.Itoa(ff.contentLength))
				w.WriteHeader(http.StatusOK)
				return
			}
			if ff.lfsOID != "" {
				w.Header().Set("X-Linked-Etag", `"`+ff.lfsOID+`"`)
			}
			if ff.lfsSize != "" {
				w.Header().Set("X-Linked-Size", ff.lfsSize)
			}
			w.Header().Set("Location", "https://cdn.example.test/blob")
			w.WriteHeader(http.StatusFound)
		case req.URL.Path == "/api/models/"+repo+"/tree/"+rev:
			if f.treeHops != "" {
				http.Redirect(w, req, f.treeHops, http.StatusFound)
				return
			}
			page := req.URL.Query().Get("page")
			if len(f.pages) > 0 {
				p := f.pages[page]
				if len(p) > 0 {
					if p[0].next != "" {
						w.Header().Set("Link", `<`+f.srv.URL+p[0].next+`>; rel="next"`)
					}
					_ = json.NewEncoder(w).Encode(p[0].entries)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(f.tree)
		default:
			http.NotFound(w, req)
		}
	}))
	return f
}

func (f *fakeHF) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

const (
	hfRepoName = "org-alpha/model-x"
	hfRev      = "0123456789abcdef0123456789abcdef01234567"
)

var (
	hfSHA  = strings.Repeat("ab", 32)
	hfSHA2 = strings.Repeat("cd", 32)
)

func hfModel(files ...catalog.ModelFile) plan.CatalogItem {
	return plan.ModelItem(&catalog.Model{
		ID: "model-x", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: hfRepoName, Revision: hfRev}, Files: files,
	})
}

func weights(name, sha string, size int64) catalog.ModelFile {
	return catalog.ModelFile{Kind: "weights", Name: name, SHA256: sha, SizeBytes: size}
}

func hfSetup(t *testing.T) (*webRig, *fakeHF, *HuggingFace) {
	r := newWebRig(t)
	f := newFakeHF(t, r, hfRepoName, hfRev)
	r.w.HFBase = f.srv.URL
	return r, f, &HuggingFace{W: r.w}
}

// Headers that match the catalog give the file list with the exact URL, size and the LFS-cross-checked
// digest; the version is the first 12 characters of the revision; the token went to the (fake) host.
func TestHFHeadersMatch(t *testing.T) {
	r, f, h := hfSetup(t)
	f.files["a.gguf"] = &hfFake{lfsOID: hfSHA, lfsSize: "1234", commit: hfRev}
	f.files["sub dir/b.bin"] = &hfFake{small: true, contentLength: 77}
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 1234), weights("sub dir/b.bin", hfSHA2, 77)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != hfRev[:12] || got.SourceType != "huggingface" || got.Trust != manifest.TrustPinned || got.SortKey != "2026.11.02-1" {
		t.Errorf("result = %+v", got)
	}
	if got.Meta["repo"] != hfRepoName || got.Meta["revision"] != hfRev {
		t.Errorf("meta = %v", got.Meta)
	}
	a, b := got.Files[0], got.Files[1]
	if a.URL != f.srv.URL+"/"+hfRepoName+"/resolve/"+hfRev+"/a.gguf" || a.Size != 1234 || a.SHA256 != hfSHA || a.SHA256Source != "catalog+hf_lfs" || !a.Auth || a.Kind != "weights" {
		t.Errorf("file a = %+v", a)
	}
	// A small file is not in LFS: the catalog's digest stands alone; slashes stay, spaces are escaped.
	if b.URL != f.srv.URL+"/"+hfRepoName+"/resolve/"+hfRev+"/sub%20dir/b.bin" || b.SHA256Source != "catalog" || b.Size != 77 || b.SHA256 != hfSHA2 {
		t.Errorf("file b = %+v", b)
	}
	// The HEAD was made without following the redirect and carried the token.
	if f.count("HEAD /") != 2 || f.count("GET") != 0 {
		t.Errorf("requests = %v", f.reqs)
	}
	if v := f.auth["HEAD /"+hfRepoName+"/resolve/"+hfRev+"/a.gguf"]; len(v) != 1 || v[0] != "Bearer "+testToken {
		t.Errorf("authorization = %v", v)
	}
	noSecret(t, "the log", r.logs.String())
}

// The size and the digest of a file the catalog leaves without a size come from Hugging Face.
func TestHFSizeFromSource(t *testing.T) {
	_, f, h := hfSetup(t)
	f.files["a.gguf"] = &hfFake{lfsOID: hfSHA, lfsSize: "4096", commit: hfRev}
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", strings.ToUpper(hfSHA), 0)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Files[0].Size != 4096 || got.Files[0].SHA256 != hfSHA {
		t.Errorf("file = %+v", got.Files[0])
	}
}

// Anything the source says that the catalog does not is LAI-SYN-021, red and permanent: the oid, the
// size, the commit. Nothing is returned, so nothing is downloaded.
func TestHFMismatchIsSYN021(t *testing.T) {
	cases := map[string]*hfFake{
		"oid":    {lfsOID: hfSHA2, lfsSize: "10", commit: hfRev},
		"size":   {lfsOID: hfSHA, lfsSize: "11", commit: hfRev},
		"commit": {lfsOID: hfSHA, lfsSize: "10", commit: strings.Repeat("9", 40)},
		"small":  {small: true, contentLength: 11},
	}
	for name, ff := range cases {
		t.Run(name, func(t *testing.T) {
			_, f, h := hfSetup(t)
			f.files["a.gguf"] = ff
			got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
			codeIs(t, err, doctor.SYN021, true)
			if len(got.Files) != 0 {
				t.Errorf("a refused item still lists files: %+v", got)
			}
		})
	}
}

// With the linked headers missing (a proxy, an API change) the tree listing of the revision is read,
// once however many files need it, and its lfs.oid and lfs.size are checked the same way.
func TestHFFallbackToTheTreeAPI(t *testing.T) {
	_, f, h := hfSetup(t)
	f.files["a.gguf"] = &hfFake{}
	f.files["cfg.json"] = &hfFake{}
	f.tree = []map[string]any{
		{"type": "directory", "path": "sub"},
		{"type": "file", "path": "a.gguf", "size": 133, "lfs": map[string]any{"oid": hfSHA, "size": 1234}},
		{"type": "file", "path": "cfg.json", "size": 55},
	}
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 1234), weights("cfg.json", hfSHA2, 55)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Files[0].SHA256Source != "catalog+hf_lfs" || got.Files[0].Size != 1234 {
		t.Errorf("lfs file = %+v", got.Files[0])
	}
	if got.Files[1].SHA256Source != "catalog" || got.Files[1].Size != 55 {
		t.Errorf("small file = %+v", got.Files[1])
	}
	if n := f.count("GET /api/models/"); n != 1 {
		t.Errorf("the listing was read %d times, want once", n)
	}

	// The fallback checks too: a different oid, a different size, a file the revision does not have.
	cases := map[string]func(tree []map[string]any) []map[string]any{
		"oid": func(t []map[string]any) []map[string]any {
			t[1]["lfs"] = map[string]any{"oid": hfSHA2, "size": 1234}
			return t
		},
		"size": func(t []map[string]any) []map[string]any {
			t[1]["lfs"] = map[string]any{"oid": hfSHA, "size": 1}
			return t
		},
		"small":   func(t []map[string]any) []map[string]any { t[2]["size"] = 56; return t },
		"missing": func(t []map[string]any) []map[string]any { return t[:2] },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			_, f2, h2 := hfSetup(t)
			f2.files["a.gguf"], f2.files["cfg.json"] = &hfFake{}, &hfFake{}
			f2.tree = mut([]map[string]any{
				{"type": "directory", "path": "sub"},
				{"type": "file", "path": "a.gguf", "size": 133, "lfs": map[string]any{"oid": hfSHA, "size": 1234}},
				{"type": "file", "path": "cfg.json", "size": 55},
			})
			want := doctor.SYN021
			if name == "missing" {
				want = doctor.SYN030
			}
			_, err := h2.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 1234), weights("cfg.json", hfSHA2, 55)))
			codeIs(t, err, want, true)
		})
	}
}

// The listing is paged with Link headers; the next page is followed only on the same host.
func TestHFTreePaging(t *testing.T) {
	_, f, h := hfSetup(t)
	f.files["a.gguf"], f.files["b.gguf"] = &hfFake{}, &hfFake{}
	f.pages = map[string][]hfPage{
		"":  {{entries: []map[string]any{{"type": "file", "path": "a.gguf", "size": 1, "lfs": map[string]any{"oid": hfSHA, "size": 5}}}, next: "/api/models/" + hfRepoName + "/tree/" + hfRev + "?recursive=1&page=2"}},
		"2": {{entries: []map[string]any{{"type": "file", "path": "b.gguf", "size": 1, "lfs": map[string]any{"oid": hfSHA2, "size": 6}}}}},
	}
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 5), weights("b.gguf", hfSHA2, 6)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Files[1].SHA256Source != "catalog+hf_lfs" {
		t.Errorf("files = %+v", got.Files)
	}
	if got := nextLink(`<https://elsewhere.example.test/x>; rel="next"`, "https://huggingface.co"); got != "" {
		t.Errorf("a next link to another host was followed: %q", got)
	}
}

// 401 and 403 mean a login is needed (LAI-SYN-034, transient: the person may add a token); 404 means
// the revision does not have the file (LAI-SYN-030, permanent); a server error is transient.
func TestHFStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
		perm   bool
	}{
		{http.StatusUnauthorized, doctor.SYN034, false},
		{http.StatusForbidden, doctor.SYN034, false},
		{http.StatusNotFound, doctor.SYN030, true},
		{http.StatusGone, doctor.SYN030, true},
		{http.StatusInternalServerError, doctor.SYN033, false},
		{http.StatusTeapot, doctor.SYN033, false},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			r, f, h := hfSetup(t)
			f.files["a.gguf"] = &hfFake{status: c.status}
			_, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
			codeIs(t, err, c.code, c.perm)
			noSecret(t, "the error", err.Error())
			noSecret(t, "the log", r.logs.String())
		})
	}
	// Without a token configured the request carries no Authorization header.
	r, f, h := hfSetup(t)
	r.w.HFToken = nil
	f.files["a.gguf"] = &hfFake{status: http.StatusUnauthorized}
	_, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
	codeIs(t, err, doctor.SYN034, false)
	if v := f.auth["HEAD /"+hfRepoName+"/resolve/"+hfRev+"/a.gguf"]; len(v) != 1 || v[0] != "" {
		t.Errorf("authorization without a token = %v", v)
	}
}

// 429 is transient: the resolver waits for Retry-After (inside the cap) and asks once more; a limit that
// does not lift in time, or lifts too far away, is LAI-SYN-034 and costs no wait.
func TestHFRateLimit(t *testing.T) {
	r, f, h := hfSetup(t)
	f.files["a.gguf"] = &hfFake{lfsOID: hfSHA, lfsSize: "10", commit: hfRev, failures: 1, retryAfter: "7"}
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
	if err != nil || got.Files[0].SHA256Source != "catalog+hf_lfs" {
		t.Fatalf("after one 429: %+v, %v", got, err)
	}
	if s := r.slept(); len(s) != 1 || s[0] != 7*time.Second {
		t.Errorf("waited %v, want [7s]", s)
	}

	r, f, h = hfSetup(t)
	f.files["a.gguf"] = &hfFake{failures: 5, retryAfter: "3"}
	_, err = h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
	codeIs(t, err, doctor.SYN034, false)
	if s := r.slept(); len(s) != 1 {
		t.Errorf("waits = %v, want one", s)
	}

	r, f, h = hfSetup(t)
	f.files["a.gguf"] = &hfFake{failures: 5, retryAfter: "7200"}
	_, err = h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)))
	codeIs(t, err, doctor.SYN034, false)
	if s := r.slept(); len(s) != 0 {
		t.Errorf("waited %v for a limit beyond the cap", s)
	}
}

// The token goes to the Hugging Face host and nowhere else: a redirect to another host (here: the same
// loopback address on another port, which Go itself would treat as the same domain) drops it, and a
// redirect on the same host keeps it.
func TestHFTokenDroppedOnCrossHostRedirect(t *testing.T) {
	r, f, h := hfSetup(t)
	var mu sync.Mutex
	var seen []string
	other := r.serve(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Header.Get("Authorization"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "file", "path": "a.gguf", "size": 1, "lfs": map[string]any{"oid": hfSHA, "size": 5}}})
	}))
	f.files["a.gguf"] = &hfFake{}
	f.treeHops = other.URL + "/listing"
	got, err := h.Lookup(context.Background(), hfModel(weights("a.gguf", hfSHA, 5)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Files[0].SHA256Source != "catalog+hf_lfs" {
		t.Errorf("file = %+v", got.Files[0])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("the other host saw %d requests", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("the Authorization header followed a redirect to another host: %q", seen[0])
	}
	if v := f.auth["GET /api/models/"+hfRepoName+"/tree/"+hfRev]; len(v) != 1 || v[0] != "Bearer "+testToken {
		t.Errorf("the first hop did not carry the token: %v", v)
	}
	noSecret(t, "the log", r.logs.String())
}

// The token is never sent to a host that is not the Hugging Face host, even for a file whose own URL
// the catalog gives: that file is taken as pinned, with no request at all.
func TestHFForeignHostFileGetsNoToken(t *testing.T) {
	r, f, h := hfSetup(t)
	m := hfModel(catalog.ModelFile{Kind: "weights", Name: "x.bin", SHA256: hfSHA, SizeBytes: 9, URL: "https://files.example.test/x.bin"})
	got, err := h.Lookup(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Files[0]; f.Auth || f.URL != "https://files.example.test/x.bin" || f.SHA256Source != "catalog" {
		t.Errorf("file = %+v", f)
	}
	if len(f.reqs) != 0 {
		t.Errorf("requests = %v", f.reqs)
	}
	// And do() itself never attaches the token to another host.
	var gotAuth string
	srv := r.serve(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) { gotAuth = req.Header.Get("Authorization") }))
	resp, err := r.w.do(context.Background(), http.MethodHead, srv.URL+"/x", reqOpts{auth: true})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("the token went to %s", srv.URL)
	}
}

// A catalog that does not pin the model completely is refused before any request.
func TestHFUnpinnedIsRefused(t *testing.T) {
	_, f, h := hfSetup(t)
	cases := map[string]plan.CatalogItem{
		"no sha":      hfModel(weights("a.gguf", "", 10)),
		"short sha":   hfModel(weights("a.gguf", "abc", 10)),
		"bad name":    hfModel(weights("../a.gguf", hfSHA, 10)),
		"no files":    hfModel(),
		"floating":    plan.ModelItem(&catalog.Model{ID: "m", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: hfRepoName, Revision: "main"}, Files: []catalog.ModelFile{weights("a", hfSHA, 1)}}),
		"bad repo":    plan.ModelItem(&catalog.Model{ID: "m", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "a b/c", Revision: hfRev}, Files: []catalog.ModelFile{weights("a", hfSHA, 1)}}),
		"not hf":      plan.ModelItem(&catalog.Model{ID: "m", Engine: "llama", Source: catalog.ModelSource{Type: "other"}, Files: []catalog.ModelFile{weights("a", hfSHA, 1)}}),
		"not a model": urlContent("c", "maps", catalog.ContentSource{Type: "huggingface"}),
	}
	for name, it := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.Lookup(context.Background(), it)
			codeIs(t, err, doctor.SYN033, true)
		})
	}
	if len(f.reqs) != 0 {
		t.Errorf("requests = %v", f.reqs)
	}
}

// Through the engine half: the Resolution a machine downloads, with the Hugging Face login marked on
// every file and the source recorded without a secret.
func TestHFEngineResolution(t *testing.T) {
	_, f, h := hfSetup(t)
	f.files["a.gguf"] = &hfFake{lfsOID: hfSHA, lfsSize: "10", commit: hfRev}
	res, err := h.Engine().Resolve(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != hfRev[:12] || res.Dir || res.Trust != manifest.TrustPinned || res.Resolver != "huggingface" || len(res.Files) != 1 {
		t.Fatalf("resolution = %+v", res)
	}
	if fl := res.Files[0]; !fl.Auth || fl.Size != 10 || fl.SHA256 != hfSHA || fl.SHA256Source != "catalog+hf_lfs" || fl.Path != "a.gguf" || fl.Kind != "weights" {
		t.Errorf("file = %+v", fl)
	}
	if res.Source.Type != "huggingface" || res.Source.String("repo") != hfRepoName || res.Source.String("revision") != hfRev {
		t.Errorf("source = %+v", res.Source)
	}
	// A source error comes out as the machine's error type.
	f.files["a.gguf"].lfsOID = hfSHA2
	_, err = h.Engine().Resolve(context.Background(), hfModel(weights("a.gguf", hfSHA, 10)), plan.ItemPlan{})
	if CodeOf(err) != doctor.SYN021 {
		t.Errorf("err = %v", err)
	}
}
