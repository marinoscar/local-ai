package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests of the GitHub source (CAT-04). Everything runs against an httptest TLS server that fakes
// both the GitHub API and raw.githubusercontent.com, so the https-only rule is exercised for real;
// the one timeout test uses a round tripper that blocks until its context ends, never a sleep.

const ghRepo = "org-alpha/lab"

// fakeGH is a fake of the GitHub API and the raw host for one repository.
type fakeGH struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	hits []string // "METHOD path?query" of every request
	hdrs []http.Header

	// refs are the full ref names matching-refs lists, in this order.
	refs []string
	// tags holds the files of each tag: path (as under catalog/) -> content.
	tags map[string]map[string]string
	// pageSize > 0 paginates the tag listing with a Link header.
	pageSize int
	// api, when set, answers every API request instead of the normal handler (return true if handled).
	api func(w http.ResponseWriter, r *http.Request) bool
	// raw, when set, is asked first for every raw request.
	raw func(w http.ResponseWriter, r *http.Request, tag, path string) bool
	// noProfileListing makes the contents API answer 500 (profiles fall back to the default list).
	noProfileListing bool
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	f := &fakeGH{t: t, tags: map[string]map[string]string{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// source is the GitHub source wired to the fake.
func (f *fakeGH) source() *GitHub {
	return &GitHub{RawBase: f.srv.URL + "/" + ghRepo, APIBase: f.srv.URL, Client: f.srv.Client()}
}

// publish adds a tag holding the catalog of dir.
func (f *fakeGH) publish(tag, dir string) {
	files := map[string]string{}
	for _, bf := range bundleOf(f.t, dir).Files {
		files[bf.Path] = bf.Content
	}
	f.tags[tag] = files
	f.refs = append(f.refs, "refs/tags/"+tag)
}

// publishVersion publishes catalog-<v> built from a valid catalog stamped v.
func (f *fakeGH) publishVersion(v string) { f.publish(TagPrefix+v, versionedDir(f.t, v)) }

func (f *fakeGH) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hits...)
}

func (f *fakeGH) apiRequests() (n int) {
	for _, h := range f.requests() {
		if strings.Contains(h, "/repos/") {
			n++
		}
	}
	return n
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.URL.RequestURI())
	f.hdrs = append(f.hdrs, r.Header.Clone())
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/"+ghRepo+"/"):
		if f.api != nil && f.api(w, r) {
			return
		}
		f.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/"+ghRepo+"/"):
		rest := strings.TrimPrefix(r.URL.Path, "/"+ghRepo+"/")
		tag, path, ok := strings.Cut(rest, "/catalog/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if f.raw != nil && f.raw(w, r, tag, path) {
			return
		}
		content, ok := f.tags[tag][path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(content))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGH) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/git/matching-refs/tags/"+TagPrefix):
		type ref struct {
			Ref    string            `json:"ref"`
			Object map[string]string `json:"object"`
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		refs := f.refs
		if f.pageSize > 0 {
			lo, hi := (page-1)*f.pageSize, page*f.pageSize
			if lo > len(refs) {
				lo = len(refs)
			}
			if hi < len(refs) {
				q := r.URL.Query()
				q.Set("page", strconv.Itoa(page+1))
				w.Header().Set("Link", fmt.Sprintf(`<https://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, q.Encode()))
			} else {
				hi = len(refs)
			}
			refs = refs[lo:hi]
		}
		out := []ref{}
		for _, name := range refs {
			out = append(out, ref{Ref: name, Object: map[string]string{"type": "tag"}})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasSuffix(r.URL.Path, "/contents/catalog/profiles"):
		if f.noProfileListing {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		tag := r.URL.Query().Get("ref")
		type entry struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		out := []entry{}
		var names []string
		for p := range f.tags[tag] {
			if rest, ok := strings.CutPrefix(p, "profiles/"); ok {
				names = append(names, rest)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, entry{Name: n, Type: "file"})
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGH) rateLimit(w http.ResponseWriter, status int) {
	w.Header().Set("X-RateLimit-Remaining", "0")
	http.Error(w, `{"message":"API rate limit exceeded"}`, status)
}

func asFetchError(t *testing.T, err error) *FetchError {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("want a *FetchError, got %T %v", err, err)
	}
	return fe
}

// roundTripFunc is a fake transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewGitHub(t *testing.T) {
	cases := []struct {
		name, base, wantRaw, wantAPI string
	}{
		{"default", "", DefaultRawBase, DefaultAPIBase},
		{"trailing slash and spaces", " https://raw.githubusercontent.com/org-alpha/lab/ ", "https://raw.githubusercontent.com/org-alpha/lab", DefaultAPIBase},
		{"a mirror never reaches the GitHub API", "https://mirror.example.test/org-alpha/lab", "https://mirror.example.test/org-alpha/lab", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewGitHub(c.base)
			if g.RawBase != c.wantRaw || g.APIBase != c.wantAPI {
				t.Fatalf("%+v", g)
			}
		})
	}
}

func TestResolveStable(t *testing.T) {
	tags := func(names ...string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = "refs/tags/" + n
		}
		return out
	}
	cases := []struct {
		name string
		refs []string
		want string
	}{
		{"one tag", tags("catalog-2026.11.02-1"), "catalog-2026.11.02-1"},
		{"catalog-stable is ignored even when listed last", tags("catalog-2026.11.02-1", "catalog-2026.11.03-2", "catalog-stable"), "catalog-2026.11.03-2"},
		{"catalog-stable first", tags("catalog-stable", "catalog-2026.10.01-1"), "catalog-2026.10.01-1"},
		{"the sequence number is numeric: 10 beats 9", tags("catalog-2026.11.02-9", "catalog-2026.11.02-10"), "catalog-2026.11.02-10"},
		{"day 10 beats day 09", tags("catalog-2026.11.09-3", "catalog-2026.11.10-1"), "catalog-2026.11.10-1"},
		{"month then year then day", tags("catalog-2026.12.01-1", "catalog-2027.01.01-1", "catalog-2026.12.31-1"), "catalog-2027.01.01-1"},
		{"order of the listing does not matter", tags("catalog-2026.11.10-1", "catalog-2026.11.02-1", "catalog-2026.11.03-1"), "catalog-2026.11.10-1"},
		{"other tags are ignored", tags("catalog-notes", "catalog-2026.11.02-1", "catalog-2026.11.9-3", "catalog-2030.01.01-0", "catalog-2026.02.30-1", "catalog-2026.11.02-1-rc1"), "catalog-2026.11.02-1"},
		{"a foreign ref is ignored", []string{"refs/heads/catalog-2030.01.01-1", "refs/tags/catalog-2026.11.02-1"}, "catalog-2026.11.02-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGH(t)
			f.refs = c.refs
			res, err := f.source().Resolve(context.Background(), "stable")
			if err != nil || res.Tag != c.want || res.Via != "matching-refs" || res.APIErr != nil {
				t.Fatalf("%+v %v", res, err)
			}
			if got := f.requests(); len(got) != 1 || !strings.HasPrefix(got[0], "GET /repos/"+ghRepo+"/git/matching-refs/tags/catalog-") {
				t.Errorf("requests: %v", got)
			}
		})
	}
	t.Run("an empty channel means stable", func(t *testing.T) {
		f := newFakeGH(t)
		f.refs = tags("catalog-2026.11.02-1")
		if res, err := f.source().Resolve(context.Background(), ""); err != nil || res.Tag != "catalog-2026.11.02-1" {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

func TestResolveStablePaginates(t *testing.T) {
	f := newFakeGH(t)
	f.pageSize = 2
	for _, v := range []string{"2026.01.01-1", "2026.02.01-1", "2026.03.01-1", "2026.04.01-1", "2026.05.01-1"} {
		f.refs = append(f.refs, "refs/tags/"+TagPrefix+v)
	}
	f.refs = append(f.refs, "refs/tags/"+StableTag)
	res, err := f.source().Resolve(context.Background(), "stable")
	if err != nil || res.Tag != "catalog-2026.05.01-1" {
		t.Fatalf("%+v %v", res, err)
	}
	if n := f.apiRequests(); n != 3 {
		t.Errorf("want 3 pages, got %d: %v", n, f.requests())
	}
}

// Every way the listing can fail ends in the moving tag, never in an error: the node must still
// be able to refresh from catalog-stable.
func TestResolveStableFallsBackToTheMovingTag(t *testing.T) {
	cases := []struct {
		name string
		set  func(f *fakeGH, g *GitHub)
		why  string // text in APIErr
	}{
		{"rate limited 403", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { f.rateLimit(w, 403); return true }
		}, ReasonRateLimited},
		{"rate limited 429", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "slow down", 429); return true }
		}, ReasonRateLimited},
		{"403 with a Retry-After (secondary limit)", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "abuse", 403)
				return true
			}
		}, ReasonRateLimited},
		{"plain 403", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "no", 403); return true }
		}, ReasonHTTPStatus},
		{"500", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "boom", 500); return true }
		}, ReasonHTTPStatus},
		{"503", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { http.Error(w, "down", 503); return true }
		}, ReasonHTTPStatus},
		{"404", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { http.NotFound(w, r); return true }
		}, ReasonNotFound},
		{"an answer that is not JSON", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool { _, _ = w.Write([]byte("<html>")); return true }
		}, ReasonBadListing},
		{"an answer that is an object", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool {
				_, _ = w.Write([]byte(`{"message":"x"}`))
				return true
			}
		}, ReasonBadListing},
		{"no catalog tag exists yet", func(f *fakeGH, _ *GitHub) { f.refs = []string{"refs/tags/" + StableTag} }, "no catalog-<version> tag"},
		{"no API configured (a mirror)", func(_ *fakeGH, g *GitHub) { g.APIBase = "" }, "no API configured"},
		{"the API host is not https", func(_ *fakeGH, g *GitHub) { g.APIBase = strings.Replace(g.APIBase, "https:", "http:", 1) }, ReasonNotHTTPS},
		{"a Link to another host is not followed", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Link", `<https://evil.example.test/x?page=2>; rel="next"`)
				_, _ = w.Write([]byte(`[]`))
				return true
			}
		}, ReasonBadListing},
		{"a listing that never ends is not trusted", func(f *fakeGH, _ *GitHub) {
			f.api = func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("Link", fmt.Sprintf(`<https://%s%s?page=9>; rel="next"`, r.Host, r.URL.Path))
				_, _ = w.Write([]byte(`[]`))
				return true
			}
		}, "too many tags"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGH(t)
			f.refs = []string{"refs/tags/catalog-2026.11.02-1", "refs/tags/" + StableTag}
			g := f.source()
			c.set(f, g)
			res, err := g.Resolve(context.Background(), "stable")
			if err != nil || res.Tag != StableTag || res.Via != "stable-tag" || res.APIErr == nil {
				t.Fatalf("%+v %v", res, err)
			}
			if !strings.Contains(res.APIErr.Error(), c.why) {
				t.Errorf("APIErr %q does not mention %q", res.APIErr, c.why)
			}
		})
	}
}

func TestResolveStableAPITimeout(t *testing.T) {
	var asked time.Duration
	g := &GitHub{
		RawBase: "https://raw.example.test/" + ghRepo, APIBase: "https://api.example.test", APITimeout: time.Millisecond,
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if dl, ok := r.Context().Deadline(); ok {
				asked = time.Until(dl)
			}
			<-r.Context().Done() // a hung server: only the deadline ends this
			return nil, r.Context().Err()
		})},
	}
	res, err := g.Resolve(context.Background(), "stable")
	if err != nil || res.Tag != StableTag || res.APIErr == nil || asked <= 0 {
		t.Fatalf("%+v %v (deadline in %v)", res, err, asked)
	}
	// the default is the 10 s of the issue
	if g2 := (&GitHub{}); g2.apiTimeout() != 10*time.Second || g2.fileTimeout() != 30*time.Second {
		t.Errorf("default timeouts %v %v", g2.apiTimeout(), g2.fileTimeout())
	}
}

func TestResolveCancelledContextIsNotAFallback(t *testing.T) {
	f := newFakeGH(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.source().Resolve(ctx, "stable"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestResolvePin(t *testing.T) {
	f := newFakeGH(t)
	res, err := f.source().Resolve(context.Background(), "catalog-2026.11.02-1")
	if err != nil || res.Tag != "catalog-2026.11.02-1" || res.Via != "pin" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(f.requests()) != 0 {
		t.Errorf("a pin must not touch the network: %v", f.requests())
	}
}

func TestResolveBadChannel(t *testing.T) {
	for _, ch := range []string{"latest", "catalog-stable", "catalog-", "catalog-2026.13.01-1", "catalog-2026.11.9-3", "catalog-2026.11.02-0", "2026.11.02-1", "../x", "STABLE"} {
		t.Run(ch, func(t *testing.T) {
			f := newFakeGH(t)
			_, err := f.source().Resolve(context.Background(), ch)
			fe := asFetchError(t, err)
			if fe.Code() != CodeGitHubNotFound || fe.Reason != ReasonBadChannel || len(f.requests()) != 0 {
				t.Fatalf("%v requests %v", err, f.requests())
			}
		})
	}
}

func TestFetchCatalogStable(t *testing.T) {
	f := newFakeGH(t)
	f.publishVersion(v1)
	f.publishVersion(v2)
	f.tags[StableTag] = f.tags["catalog-"+v2]
	f.refs = append(f.refs, "refs/tags/"+StableTag)

	b, err := f.source().FetchCatalog(context.Background(), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != v2 {
		t.Fatalf("version %q", b.Version)
	}
	want := bundleOf(t, versionedDir(t, v2))
	if len(b.Files) != len(want.Files) {
		t.Fatalf("files %d want %d", len(b.Files), len(want.Files))
	}
	byPath := func(bu *Bundle) map[string]BundleFile {
		m := map[string]BundleFile{}
		for _, f := range bu.Files {
			m[f.Path] = f
		}
		return m
	}
	if !reflect.DeepEqual(byPath(b), byPath(want)) {
		t.Error("the bundle differs from the catalog directory of the tag (path, sha256 or content)")
	}
	if b.BundleSHA256 != BundleDigest(want.Files) || len(b.BundleSHA256) != 64 {
		t.Errorf("bundle digest %q", b.BundleSHA256)
	}
	// the privacy rules: no credential of any kind, a User-Agent, JSON accept only on the API
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, h := range f.hdrs {
		for _, bad := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if h.Get(bad) != "" {
				t.Errorf("request %s sent %s", f.hits[i], bad)
			}
		}
		if h.Get("User-Agent") != userAgentGitHub {
			t.Errorf("request %s User-Agent %q", f.hits[i], h.Get("User-Agent"))
		}
		isAPI := strings.Contains(f.hits[i], "/repos/")
		if isAPI != (h.Get("Accept") == "application/vnd.github+json") {
			t.Errorf("request %s Accept %q", f.hits[i], h.Get("Accept"))
		}
	}
	// the tag listing, CATALOG_VERSION first, the profile listing of the tag, then every other file
	if len(f.hits) != 2+len(want.Files) {
		t.Fatalf("requests: %v", f.hits)
	}
	wantReqs := []string{
		"GET /repos/" + ghRepo + "/git/matching-refs/tags/catalog-?per_page=100",
		"GET /" + ghRepo + "/catalog-" + v2 + "/catalog/CATALOG_VERSION",
		"GET /repos/" + ghRepo + "/contents/catalog/profiles?ref=catalog-" + v2,
	}
	if !reflect.DeepEqual(f.hits[:3], wantReqs) {
		t.Errorf("requests: %v", f.hits)
	}
}

func TestBundleDigestMatchesTheCloudRule(t *testing.T) {
	// cloud-app.md 3.6: sha256 over the sorted "<path>\0<sha256>\n" lines.
	files := []BundleFile{{Path: "models.yaml", SHA256: strings.Repeat("b", 64)}, {Path: "CATALOG_VERSION", SHA256: strings.Repeat("A", 64)}}
	sum := sha256.Sum256([]byte("CATALOG_VERSION\x00" + strings.Repeat("a", 64) + "\n" + "models.yaml\x00" + strings.Repeat("b", 64) + "\n"))
	if got := BundleDigest(files); got != hex.EncodeToString(sum[:]) {
		t.Errorf("digest %s", got)
	}
	if BundleDigest(files) != BundleDigest([]BundleFile{files[1], files[0]}) {
		t.Error("order of the files changed the digest")
	}
}

func TestFetchCatalogPinAndStableFallback(t *testing.T) {
	f := newFakeGH(t)
	f.publishVersion(v1)
	f.publishVersion(v2)
	f.tags[StableTag] = f.tags["catalog-"+v2]

	t.Run("a pin fetches that tag and never lists", func(t *testing.T) {
		before := f.apiRequests()
		b, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1)
		if err != nil || b.Version != v1 {
			t.Fatalf("%v %v", b, err)
		}
		// the profile listing is an API call; the tag listing is not
		for _, h := range f.requests()[before:] {
			if strings.Contains(h, "matching-refs") {
				t.Errorf("pin listed tags: %s", h)
			}
		}
	})
	t.Run("the API rate limited: catalog-stable is read", func(t *testing.T) {
		f.api = func(w http.ResponseWriter, r *http.Request) bool { f.rateLimit(w, 403); return true }
		defer func() { f.api = nil }()
		b, err := f.source().FetchCatalog(context.Background(), "stable")
		if err != nil || b.Version != v2 {
			t.Fatalf("%v %v", b, err)
		}
		var sawStable bool
		for _, h := range f.requests() {
			sawStable = sawStable || strings.Contains(h, "/"+ghRepo+"/"+StableTag+"/catalog/CATALOG_VERSION")
		}
		if !sawStable {
			t.Error("catalog-stable was not fetched")
		}
	})
	t.Run("a stale catalog-stable is read as it is", func(t *testing.T) {
		// catalog-stable still points at v1 while v2 exists: only the API path sees v2
		f.tags[StableTag] = f.tags["catalog-"+v1]
		f.refs = []string{"refs/tags/catalog-" + v1, "refs/tags/catalog-" + v2, "refs/tags/" + StableTag}
		b, err := f.source().FetchCatalog(context.Background(), "stable")
		if err != nil || b.Version != v2 {
			t.Fatalf("listing available: %v %v (the highest tag must win over a stale moving tag)", b, err)
		}
		f.api = func(w http.ResponseWriter, r *http.Request) bool { f.rateLimit(w, 429); return true }
		defer func() { f.api = nil }()
		b, err = f.source().FetchCatalog(context.Background(), "stable")
		if err != nil || b.Version != v1 {
			t.Fatalf("listing down: %v %v (the moving tag is what the fallback means)", b, err)
		}
	})
}

func TestFetchCatalogProfileDiscovery(t *testing.T) {
	t.Run("the repository decides which profiles exist", func(t *testing.T) {
		f := newFakeGH(t)
		dir := versionedDir(t, v1)
		copyTree(t, versionedDir(t, v1), dir)
		f.publish("catalog-"+v1, dir)
		f.tags["catalog-"+v1]["profiles/gpu-24gb.yaml"] = f.tags["catalog-"+v1]["profiles/cuda-12gb.yaml"]
		f.tags["catalog-"+v1]["profiles/notes.txt"] = "not a profile"
		delete(f.tags["catalog-"+v1], "profiles/small-8gb.yaml")
		b, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, bf := range b.Files {
			got = append(got, bf.Path)
		}
		want := []string{"CATALOG_VERSION", "models.yaml", "ark.yaml", "profiles/ark-drive-full.yaml", "profiles/cpu-only.yaml", "profiles/cuda-12gb.yaml", "profiles/gpu-24gb.yaml", "profiles/max-128gb.yaml"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("files %v\nwant  %v", got, want)
		}
	})
	t.Run("listing unavailable: the documented profiles", func(t *testing.T) {
		f := newFakeGH(t)
		f.publishVersion(v1)
		f.noProfileListing = true
		b, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1)
		if err != nil || len(b.Files) != 3+len(DefaultProfileIDs) {
			t.Fatalf("%v files %d", err, len(b.Files))
		}
	})
	t.Run("listing unavailable and a documented profile is gone: nothing is delivered", func(t *testing.T) {
		f := newFakeGH(t)
		f.publishVersion(v1)
		f.noProfileListing = true
		delete(f.tags["catalog-"+v1], "profiles/cpu-only.yaml")
		b, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1)
		fe := asFetchError(t, err)
		if b != nil || fe.Code() != CodeGitHubIncomplete {
			t.Fatalf("%v %v", b, err)
		}
	})
}

func TestFetchCatalogFailures(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		set     func(f *fakeGH)
		code    string
		reason  string
	}{
		{"pinned tag does not exist", "catalog-2030.01.01-1", func(f *fakeGH) {}, CodeGitHubNotFound, ReasonNotFound},
		{"stable fallback tag does not exist yet", "stable", func(f *fakeGH) { f.refs = nil }, CodeGitHubNotFound, ReasonNotFound},
		{"a file of the tag is missing", "catalog-" + v1, func(f *fakeGH) { delete(f.tags["catalog-"+v1], "ark.yaml") }, CodeGitHubIncomplete, ReasonNotFound},
		{"a profile is missing", "catalog-" + v1, func(f *fakeGH) {
			f.noProfileListing = true
			delete(f.tags["catalog-"+v1], "profiles/small-8gb.yaml")
		}, CodeGitHubIncomplete, ReasonNotFound},
		{"raw is rate limited", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool { f.rateLimit(w, 429); return true }
		}, CodeGitHubFetch, ReasonRateLimited},
		{"raw answers 403 without a quota", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool { f.rateLimit(w, 403); return true }
		}, CodeGitHubFetch, ReasonRateLimited},
		{"raw answers 502", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				http.Error(w, "bad", 502)
				return true
			}
		}, CodeGitHubFetch, ReasonHTTPStatus},
		{"the last file answers 500 after the others arrived", "catalog-" + v1, func(f *fakeGH) {
			f.noProfileListing = true
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				if path == "profiles/small-8gb.yaml" {
					http.Error(w, "boom", 500)
					return true
				}
				return false
			}
		}, CodeGitHubFetch, ReasonHTTPStatus},
		{"truncated: the connection ends before the declared length", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				if path == "models.yaml" {
					w.Header().Set("Content-Length", "100000")
					_, _ = w.Write([]byte("schema_version: 1\nmodels:\n"))
					return true
				}
				return false
			}
		}, CodeGitHubIncomplete, ReasonTruncated},
		{"a file declares more than the 2 MB limit", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				if path == "models.yaml" {
					w.Header().Set("Content-Length", strconv.Itoa(MaxBundleBytes+1))
					return true
				}
				return false
			}
		}, CodeGitHubIncomplete, ReasonTooLarge},
		{"a file with no declared length is cut at the limit", "catalog-" + v1, func(f *fakeGH) {
			f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				if path == "models.yaml" {
					w.Header().Set("Transfer-Encoding", "chunked")
					chunk := strings.Repeat("x", 64<<10)
					for i := 0; i <= MaxBundleBytes/len(chunk); i++ {
						_, _ = w.Write([]byte(chunk))
					}
					return true
				}
				return false
			}
		}, CodeGitHubIncomplete, ReasonTooLarge},
		{"the tag holds another CATALOG_VERSION", "catalog-" + v1, func(f *fakeGH) {
			f.tags["catalog-"+v1]["CATALOG_VERSION"] = v2 + "\n"
		}, CodeGitHubIncomplete, ReasonVersionMismatch},
		{"CATALOG_VERSION is empty", "catalog-" + v1, func(f *fakeGH) { f.tags["catalog-"+v1]["CATALOG_VERSION"] = "" }, CodeGitHubIncomplete, ReasonVersionMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGH(t)
			f.publishVersion(v1)
			c.set(f)
			b, err := f.source().FetchCatalog(context.Background(), c.channel)
			if b != nil {
				t.Fatalf("a failed fetch must deliver no bundle, got %d files", len(b.Files))
			}
			fe := asFetchError(t, err)
			if fe.Code() != c.code || fe.Reason != c.reason {
				t.Fatalf("code %s reason %s: %v", fe.Code(), fe.Reason, err)
			}
			if !strings.HasPrefix(err.Error(), c.code) {
				t.Errorf("message must start with the doctor code: %q", err)
			}
		})
	}
}

func TestFetchCatalogUnreachable(t *testing.T) {
	f := newFakeGH(t)
	g := f.source()
	f.srv.Close() // nothing listens any more
	b, err := g.FetchCatalog(context.Background(), "catalog-"+v1)
	fe := asFetchError(t, err)
	if b != nil || fe.Code() != CodeGitHubFetch || fe.Reason != ReasonUnreachable {
		t.Fatalf("%v %v", b, err)
	}
	// and "stable" with both API and raw down is the same failure, after the fallback
	_, err = g.FetchCatalog(context.Background(), "stable")
	if fe := asFetchError(t, err); fe.Code() != CodeGitHubFetch {
		t.Fatalf("%v", err)
	}
}

func TestFetchCatalogFileTimeout(t *testing.T) {
	g := &GitHub{RawBase: "https://raw.example.test/" + ghRepo, FileTimeout: time.Millisecond,
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}}
	_, err := g.FetchCatalog(context.Background(), "catalog-"+v1)
	fe := asFetchError(t, err)
	if fe.Code() != CodeGitHubFetch || fe.Reason != ReasonUnreachable {
		t.Fatalf("%v", err)
	}
}

func TestFetchCatalogCallerCancelIsNotAGitHubFailure(t *testing.T) {
	f := newFakeGH(t)
	f.publishVersion(v1)
	ctx, cancel := context.WithCancel(context.Background())
	f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
		cancel()
		http.Error(w, "x", 500)
		return true
	}
	_, err := f.source().FetchCatalog(ctx, "catalog-"+v1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestOnlyHTTPS(t *testing.T) {
	t.Run("an http base_url is refused before any request", func(t *testing.T) {
		f := newFakeGH(t)
		f.publishVersion(v1)
		g := f.source()
		g.RawBase = strings.Replace(g.RawBase, "https:", "http:", 1)
		_, err := g.FetchCatalog(context.Background(), "catalog-"+v1)
		fe := asFetchError(t, err)
		if fe.Code() != CodeGitHubFetch || fe.Reason != ReasonNotHTTPS {
			t.Fatalf("%v", err)
		}
		if len(f.requests()) != 0 {
			t.Errorf("requests were made: %v", f.requests())
		}
	})
	t.Run("a redirect to http is refused", func(t *testing.T) {
		f := newFakeGH(t)
		f.publishVersion(v1)
		f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
			http.Redirect(w, r, "http://mirror.example.test/"+path, http.StatusFound)
			return true
		}
		_, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1)
		fe := asFetchError(t, err)
		if fe.Code() != CodeGitHubFetch || !strings.Contains(err.Error(), "non-https") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a redirect within https is followed", func(t *testing.T) {
		f := newFakeGH(t)
		f.publishVersion(v1)
		f.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
			if r.URL.Query().Get("moved") == "" {
				u := *r.URL
				q := u.Query()
				q.Set("moved", "1")
				u.RawQuery = q.Encode()
				http.Redirect(w, r, u.String(), http.StatusFound)
				return true
			}
			return false
		}
		if b, err := f.source().FetchCatalog(context.Background(), "catalog-"+v1); err != nil || b.Version != v1 {
			t.Fatalf("%v %v", b, err)
		}
	})
	t.Run("the default client ignores proxy variables", func(t *testing.T) {
		t.Setenv("HTTPS_PROXY", "https://proxy.example.test:3128")
		tr, ok := (&GitHub{}).client().Transport.(*http.Transport)
		if !ok || tr.Proxy != nil {
			t.Fatalf("transport %T proxy set: %v", tr, ok && tr.Proxy != nil)
		}
	})
	t.Run("the base_url must end in owner/repo", func(t *testing.T) {
		for _, base := range []string{"https://raw.githubusercontent.com", "https://raw.githubusercontent.com/org-alpha", "https://raw.githubusercontent.com/a/b/c"} {
			g := NewGitHub(base)
			g.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("offline: %s", r.URL)
			})}
			res, err := g.Resolve(context.Background(), "stable")
			if err != nil || res.Tag != StableTag || res.APIErr == nil {
				t.Errorf("%s: %+v %v", base, res, err)
			}
		}
	})
}

func TestFetchErrorText(t *testing.T) {
	e := fetchErr(CodeGitHubFetch, ReasonRateLimited, "https://x.example.test/a", errors.New("HTTP 429"))
	if got := e.Error(); got != CodeGitHubFetch+": GitHub catalog fetch failed (rate_limited) https://x.example.test/a: HTTP 429" {
		t.Errorf("%q", got)
	}
	if !errors.Is(e, e.Err) || e.Code() != CodeGitHubFetch {
		t.Error("Unwrap or Code")
	}
}

// --- through Fetcher.Refresh: install, keep the previous catalog, report the source ---

func refreshFixture(t *testing.T) (*Fetcher, *fakeGH) {
	t.Helper()
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
	gh := newFakeGH(t)
	f.GitHub = gh.source()
	return f, gh
}

func TestRefreshThroughGitHubStable(t *testing.T) {
	f, gh := refreshFixture(t)
	gh.publishVersion(v1)
	gh.publishVersion(v2)
	gh.refs = append(gh.refs, "refs/tags/"+StableTag)
	res, err := f.Refresh(context.Background())
	if err != nil || res.Source != SourceGitHub || res.Previous != v1 || !res.Changed || res.Catalog.Version != v2 {
		t.Fatalf("%+v %v", res, err)
	}
	if versionOf(t, f.Dir) != v2 || versionOf(t, f.Dir+".prev") != v1 {
		t.Error("install did not swap")
	}
	if entries, _ := os.ReadDir(f.TempDir); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
}

func TestRefreshThroughGitHubAPIDownUsesCatalogStable(t *testing.T) {
	f, gh := refreshFixture(t)
	gh.publishVersion(v2)
	gh.tags[StableTag] = gh.tags["catalog-"+v2]
	gh.api = func(w http.ResponseWriter, r *http.Request) bool { gh.rateLimit(w, 403); return true }
	res, err := f.Refresh(context.Background())
	if err != nil || res.Source != SourceGitHub || res.Catalog.Version != v2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRefreshThroughGitHubPin(t *testing.T) {
	f, gh := refreshFixture(t)
	gh.publishVersion(v2)
	gh.publishVersion("2026.11.04-1") // newer, but the node pins v2
	f.Channel = "catalog-" + v2
	res, err := f.Refresh(context.Background())
	if err != nil || res.Catalog.Version != v2 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, h := range gh.requests() {
		if strings.Contains(h, "matching-refs") {
			t.Errorf("a pin listed tags: %s", h)
		}
	}
}

// A refresh that cannot complete leaves /etc/localai/catalog byte for byte as it was and says why
// with a doctor code the caller can read through the joined error.
func TestRefreshThroughGitHubFailureKeepsThePreviousCatalog(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		set     func(gh *fakeGH)
		code    string
	}{
		{"404 for a pinned tag", "catalog-2030.01.01-1", func(gh *fakeGH) {}, CodeGitHubNotFound},
		{"404 on one file", "catalog-" + v2, func(gh *fakeGH) { delete(gh.tags["catalog-"+v2], "ark.yaml") }, CodeGitHubIncomplete},
		{"partial download", "catalog-" + v2, func(gh *fakeGH) {
			gh.noProfileListing = true
			gh.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
				if path == "profiles/max-128gb.yaml" {
					w.Header().Set("Content-Length", "5000")
					_, _ = w.Write([]byte("schema_version: 1\n"))
					return true
				}
				return false
			}
		}, CodeGitHubIncomplete},
		{"version in the file differs from the tag", "catalog-" + v2, func(gh *fakeGH) { gh.tags["catalog-"+v2]["CATALOG_VERSION"] = "2026.11.09-9\n" }, CodeGitHubIncomplete},
		{"rate limited", "catalog-" + v2, func(gh *fakeGH) {
			gh.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool { gh.rateLimit(w, 429); return true }
		}, CodeGitHubFetch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, gh := refreshFixture(t)
			gh.publishVersion(v2)
			c.set(gh)
			f.Channel = c.channel
			before := snapshot(t, f.Dir)
			res, err := f.Refresh(context.Background())
			if res != nil || err == nil {
				t.Fatalf("%+v %v", res, err)
			}
			if fe := asFetchError(t, err); fe.Code() != c.code {
				t.Errorf("code %s want %s: %v", fe.Code(), c.code, err)
			}
			if !reflect.DeepEqual(snapshot(t, f.Dir), before) {
				t.Error("the live catalog changed")
			}
			if _, statErr := os.Stat(f.Dir + ".prev"); statErr == nil {
				t.Error("catalog.prev appeared: an install started")
			}
			if entries, _ := os.ReadDir(f.TempDir); len(entries) != 0 {
				t.Errorf("staging left behind: %v", entries)
			}
		})
	}
}

// A catalog that arrives whole but fails lint is the Fetcher's LAI-CAT-002, not a fetch failure.
func TestRefreshThroughGitHubLintFailureIsARejection(t *testing.T) {
	f, gh := refreshFixture(t)
	bad := versionedDir(t, v2)
	applyEdits(t, bad, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	gh.publish("catalog-"+v2, bad)
	f.Channel = "catalog-" + v2
	before := snapshot(t, f.Dir)
	_, err := f.Refresh(context.Background())
	var re *RejectedError
	if !errors.As(err, &re) || re.Code() != CodeRejected {
		t.Fatalf("%v", err)
	}
	if !reflect.DeepEqual(snapshot(t, f.Dir), before) {
		t.Error("the live catalog changed")
	}
}

func TestGitHubDoesNotRetryByItself(t *testing.T) {
	// one refresh makes one attempt per file: the nightly sync is the retry, and a hammering client
	// is how an unauthenticated node loses its 60 requests an hour.
	f, gh := refreshFixture(t)
	gh.publishVersion(v2)
	gh.raw = func(w http.ResponseWriter, r *http.Request, tag, path string) bool {
		if path == FileVersion {
			http.Error(w, "boom", 500)
			return true
		}
		return false
	}
	f.Channel = "catalog-" + v2
	if _, err := f.Refresh(context.Background()); err == nil {
		t.Fatal("want an error")
	}
	n := 0
	for _, h := range gh.requests() {
		if strings.HasSuffix(h, "/catalog/"+FileVersion) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("CATALOG_VERSION requested %d times", n)
	}
}
