package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

const (
	kxBook    = "library_en_all_nopic"
	kxNewest  = "2026-03"
	kxFile    = "library_en_all_nopic_2026-03.zim"
	kxDirPath = "/zim/example/"
)

// fakeKiwix is a kiwix origin (the directory listing, the sidecars and the ZIM, which it redirects to a
// mirror unless the request asks for a direct download) and the mirror behind it.
type fakeKiwix struct {
	t       *testing.T
	origin  *httptest.Server
	mirror  *httptest.Server
	zim     []byte
	sum     string
	listing []byte

	mu   sync.Mutex
	reqs []string // "origin GET /path accept=..." and "mirror GET /path range=..."

	// knobs, set before the first request
	sidecarBody     func() (status int, body string) // nil: the right sidecar
	sidecarRedirect bool                             // 302 the sidecar to the mirror, which serves a valid body
	headNotAllowed  bool                             // 405 to HEAD of the ZIM
	listingStatus   int                              // 0: 200
	mirrorMode      string                           // "", "norange", "wrongsize"
	originRedirects bool                             // redirect even a direct request
	noZIMOnMirror   bool                             // the mirror answers 404
	mirrorRedirect  string                           // the mirror redirects every request to this URL prefix
}

func newFakeKiwix(t *testing.T) *fakeKiwix {
	t.Helper()
	f := &fakeKiwix{t: t, zim: readFixture(t, tinyZIMPath), listing: readFixture(t, kiwixListingPath)}
	h := sha256.Sum256(f.zim)
	f.sum = hex.EncodeToString(h[:])
	f.mirror = httptest.NewTLSServer(http.HandlerFunc(f.serveMirror))
	f.origin = httptest.NewTLSServer(http.HandlerFunc(f.serveOrigin))
	t.Cleanup(f.mirror.Close)
	t.Cleanup(f.origin.Close)
	return f
}

func (f *fakeKiwix) log(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, fmt.Sprintf(format, a...))
}

func (f *fakeKiwix) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// count is the number of logged requests that contain every part.
func (f *fakeKiwix) count(parts ...string) int {
	n := 0
	for _, r := range f.requests() {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(r, p)
		}
		if ok {
			n++
		}
	}
	return n
}

func (f *fakeKiwix) base() string { return f.origin.URL + kxDirPath }

func (f *fakeKiwix) serveOrigin(w http.ResponseWriter, r *http.Request) {
	f.log("origin %s %s accept=%s range=%s", r.Method, r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Range"))
	switch {
	case r.URL.Path == kxDirPath:
		if f.listingStatus != 0 {
			http.Error(w, "no", f.listingStatus)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(f.listing)
	case r.URL.Path == kxDirPath+kxFile+".sha256":
		if f.sidecarRedirect {
			http.Redirect(w, r, f.mirror.URL+r.URL.Path, http.StatusFound)
			return
		}
		status, body := http.StatusOK, f.sum+"  "+kxFile+"\n"
		if f.sidecarBody != nil {
			status, body = f.sidecarBody()
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	case r.URL.Path == kxDirPath+kxFile:
		if r.Method == http.MethodHead && f.headNotAllowed {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Accept") == "application/octet-stream" && !f.originRedirects {
			http.ServeContent(w, r, kxFile, time.Time{}, strings.NewReader(string(f.zim)))
			return
		}
		http.Redirect(w, r, f.mirror.URL+r.URL.Path, http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeKiwix) serveMirror(w http.ResponseWriter, r *http.Request) {
	f.log("mirror %s %s range=%s", r.Method, r.URL.Path, r.Header.Get("Range"))
	if strings.HasSuffix(r.URL.Path, ".sha256") {
		_, _ = io.WriteString(w, f.sum+"  "+kxFile+"\n")
		return
	}
	if f.mirrorRedirect != "" {
		http.Redirect(w, r, f.mirrorRedirect+r.URL.Path, http.StatusFound)
		return
	}
	if f.noZIMOnMirror {
		http.NotFound(w, r)
		return
	}
	switch f.mirrorMode {
	case "norange":
		r.Header.Del("Range")
	case "wrongsize":
		if r.Method == http.MethodGet { // HEAD tells the truth, the GET body is another file
			longer := append(append([]byte(nil), f.zim...), make([]byte, 1000)...)
			http.ServeContent(w, r, kxFile, time.Time{}, strings.NewReader(string(longer)))
			return
		}
	}
	http.ServeContent(w, r, kxFile, time.Time{}, strings.NewReader(string(f.zim)))
}

func (f *fakeKiwix) kiwix() *Kiwix {
	c := *f.origin.Client() // trusts the test certificate; the redirect rule is the production one
	c.CheckRedirect = originCheckRedirect
	return &Kiwix{Env: &Env{Online: func() bool { return true }}, HTTP: &c}
}

func kiwixCatalog(base, policy string) plan.CatalogItem {
	src := catalog.ContentSource{Type: "kiwix", Book: kxBook, BaseURL: base, VersionPolicy: policy, Checksum: "sidecar"}
	if policy == "pinned" {
		src.Pinned = &catalog.KiwixPinned{File: kxFile}
	}
	return contentItem("library", "kiwix", src)
}

func itemErr(t *testing.T, err error) *item.ResolveError {
	t.Helper()
	var re *item.ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("want an *item.ResolveError, got %T %v", err, err)
	}
	return re
}

func TestKiwixPlansTheNewestFileAndItsSize(t *testing.T) {
	f := newFakeKiwix(t)
	got, err := f.kiwix().Resolve(context.Background(), kiwixCatalog(f.base(), "latest"))
	if err != nil {
		t.Fatal(err)
	}
	want := plan.Resolved{Version: kxNewest, SortKey: kxNewest, SizeBytes: int64(len(f.zim))}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if n := f.count(".sha256"); n != 0 {
		t.Fatalf("the planner must not read the sidecar, it did %d times", n)
	}
	if n := f.count("mirror HEAD"); n != 1 {
		t.Fatalf("the size must come from a HEAD that followed the mirror redirect: %v", f.requests())
	}
}

func TestKiwixResolveItemLatestReadsTheSidecarFromTheOrigin(t *testing.T) {
	f := newFakeKiwix(t)
	res, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != kxNewest || res.SortKey != kxNewest || res.Trust != manifest.TrustSidecar || res.Dir || res.Resolver != "kiwix" {
		t.Fatalf("resolution %+v", res)
	}
	if res.Source.Type != "kiwix" || res.Source.String("book") != kxBook || res.Source.String("file") != kxFile || res.Source.String("version_policy") != "latest" {
		t.Fatalf("source %+v", res.Source)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files %+v", res.Files)
	}
	fl := res.Files[0]
	if fl.Path != kxFile || fl.Kind != "zim" || fl.URL != f.base()+kxFile || fl.Size != int64(len(f.zim)) ||
		fl.SHA256 != f.sum || fl.SHA256Source != "kiwix_sidecar" || fl.Auth || fl.ZimMD5OK == nil || fl.Source == nil {
		t.Fatalf("file %+v", fl)
	}
	if n := f.count("origin GET", ".sha256"); n != 1 {
		t.Fatalf("the sidecar is read from the origin once: %v", f.requests())
	}
	if n := f.count("mirror", ".sha256"); n != 0 {
		t.Fatalf("the sidecar must never come from the mirror: %v", f.requests())
	}
}

func TestKiwixSidecarFailuresAreLAISYN022(t *testing.T) {
	good := func(f *fakeKiwix) string { return f.sum }
	cases := []struct {
		name string
		body func(f *fakeKiwix) (int, string)
	}{
		{"empty", func(f *fakeKiwix) (int, string) { return 200, "" }},
		{"malformed", func(f *fakeKiwix) (int, string) { return 200, "not a checksum file\n" }},
		{"one space", func(f *fakeKiwix) (int, string) { return 200, good(f) + " " + kxFile + "\n" }},
		{"wrong filename", func(f *fakeKiwix) (int, string) { return 200, good(f) + "  other_2026-03.zim\n" }},
		{"no filename", func(f *fakeKiwix) (int, string) { return 200, good(f) + "\n" }},
		{"uppercase hex", func(f *fakeKiwix) (int, string) { return 200, strings.ToUpper(good(f)) + "  " + kxFile + "\n" }},
		{"short hex", func(f *fakeKiwix) (int, string) { return 200, good(f)[:60] + "  " + kxFile + "\n" }},
		{"non hex", func(f *fakeKiwix) (int, string) { return 200, strings.Repeat("g", 64) + "  " + kxFile + "\n" }},
		{"two lines", func(f *fakeKiwix) (int, string) {
			return 200, good(f) + "  " + kxFile + "\n" + good(f) + "  " + kxFile + "\n"
		}},
		{"trailing junk", func(f *fakeKiwix) (int, string) { return 200, good(f) + "  " + kxFile + " extra\n" }},
		{"too long", func(f *fakeKiwix) (int, string) { return 200, strings.Repeat("a", 5000) }},
		{"missing", func(f *fakeKiwix) (int, string) { return 404, "gone" }},
		{"server error", func(f *fakeKiwix) (int, string) { return 500, "" }},
		{"redirect status without Location", func(f *fakeKiwix) (int, string) { return 302, "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeKiwix(t)
			f.sidecarBody = func() (int, string) { return tc.body(f) }
			_, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
			re := itemErr(t, err)
			if re.Code != doctor.SYN022 || re.Permanent {
				t.Fatalf("code %s permanent=%v (%v), want a retryable LAI-SYN-022", re.Code, re.Permanent, err)
			}
		})
	}
}

func TestKiwixSidecarAcceptsTheCRLFLineEnd(t *testing.T) {
	f := newFakeKiwix(t)
	f.sidecarBody = func() (int, string) { return 200, f.sum + "  " + kxFile + "\r\n" }
	if _, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{}); err != nil {
		t.Fatal(err)
	}
	f.sidecarBody = func() (int, string) { return 200, f.sum + "  " + kxFile }
	if _, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{}); err != nil {
		t.Fatalf("no line end: %v", err)
	}
}

func TestKiwixSidecarRedirectIsRefusedEvenToAValidBody(t *testing.T) {
	f := newFakeKiwix(t)
	f.sidecarRedirect = true
	_, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
	if re := itemErr(t, err); re.Code != doctor.SYN022 {
		t.Fatalf("code %s, want LAI-SYN-022", re.Code)
	}
	if n := f.count("mirror", ".sha256"); n != 0 {
		t.Fatalf("the redirect was followed: %v", f.requests())
	}
}

func TestKiwixListingWithoutADatedFileIsLAISYN033(t *testing.T) {
	f := newFakeKiwix(t)
	it := kiwixCatalog(f.base(), "latest")
	it.Content.Source.Book = "no_such_book"
	_, err := f.kiwix().ResolveItem(context.Background(), it, plan.ItemPlan{})
	if re := itemErr(t, err); re.Code != doctor.SYN033 || re.Permanent {
		t.Fatalf("code %s permanent=%v, want a retryable LAI-SYN-033", re.Code, re.Permanent)
	}
	if _, err := f.kiwix().Resolve(context.Background(), it); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("planner half: %v", err)
	}

	f2 := newFakeKiwix(t)
	f2.listing = []byte(strings.Repeat("<a href=\"x\">", 1<<20)) // longer than the cap
	if _, err := f2.kiwix().Resolve(context.Background(), kiwixCatalog(f2.base(), "latest")); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("a listing over the cap: %v", err)
	}
	f3 := newFakeKiwix(t)
	f3.listingStatus = http.StatusServiceUnavailable
	if _, err := f3.kiwix().Resolve(context.Background(), kiwixCatalog(f3.base(), "latest")); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("a listing that answers 503: %v", err)
	}
}

func TestKiwixSizeFallsBackToARangedGetWhenHEADIsRefused(t *testing.T) {
	f := newFakeKiwix(t)
	f.headNotAllowed = true
	got, err := f.kiwix().Resolve(context.Background(), kiwixCatalog(f.base(), "latest"))
	if err != nil || got.SizeBytes != int64(len(f.zim)) {
		t.Fatalf("%+v %v", got, err)
	}
	if f.count("range=bytes=0-0") == 0 {
		t.Fatalf("no ranged GET: %v", f.requests())
	}
}

func TestKiwixPinnedNeedsNoListingAndKeepsTheCatalogChecksum(t *testing.T) {
	f := newFakeKiwix(t)
	it := kiwixCatalog(f.base(), "pinned")
	it.Content.Source.Pinned.SHA256 = strings.ToUpper(f.sum) // the catalog may write it in either case
	res, err := f.kiwix().ResolveItem(context.Background(), it, plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	fl := res.Files[0]
	if res.Version != kxNewest || res.Trust != manifest.TrustPinned || fl.SHA256 != f.sum || fl.SHA256Source != "catalog" || fl.Size != int64(len(f.zim)) || fl.Path != kxFile {
		t.Fatalf("%+v", res)
	}
	if f.count("GET "+kxDirPath+" ") != 0 || f.count(".sha256") != 0 {
		t.Fatalf("a pinned item reads neither the listing nor the sidecar: %v", f.requests())
	}
	// the planner half answers from the file name
	got, err := f.kiwix().Resolve(context.Background(), it)
	if err != nil || got.Version != kxNewest || got.SizeBytes != int64(len(f.zim)) {
		t.Fatalf("planner: %+v %v", got, err)
	}
}

func TestKiwixPinnedWithoutAChecksumTakesTheSidecar(t *testing.T) {
	f := newFakeKiwix(t)
	res, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "pinned"), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Trust != manifest.TrustSidecar || res.Files[0].SHA256 != f.sum || res.Files[0].SHA256Source != "kiwix_sidecar" {
		t.Fatalf("%+v", res)
	}
}

func TestKiwixPinnedSizeFallsBackToTheCatalog(t *testing.T) {
	f := newFakeKiwix(t)
	f.headNotAllowed = true
	f.noZIMOnMirror = true
	it := kiwixCatalog(f.base(), "pinned")
	it.Content.Source.Pinned.SHA256 = f.sum
	it.Content.ExpectedSizeBytes = 4242
	res, err := f.kiwix().ResolveItem(context.Background(), it, plan.ItemPlan{})
	if err != nil || res.Files[0].Size != 4242 {
		t.Fatalf("%+v %v", res, err)
	}
	it.Content.ExpectedSizeBytes = 0
	if _, err := f.kiwix().ResolveItem(context.Background(), it, plan.ItemPlan{}); CodeOf(err) != doctor.SYN033 {
		t.Fatalf("no size anywhere: %v", err)
	}
}

func TestKiwixRefusesToTouchTheNetworkWhenOffline(t *testing.T) {
	f := newFakeKiwix(t)
	for _, online := range []func() bool{nil, func() bool { return false }} {
		k := f.kiwix()
		k.Env.Online = online
		_, err := k.ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
		if re := itemErr(t, err); re.Code != doctor.SYN002 || !re.Permanent {
			t.Fatalf("%+v", re)
		}
		if _, err := k.Resolve(context.Background(), kiwixCatalog(f.base(), "latest")); CodeOf(err) != doctor.SYN002 {
			t.Fatalf("planner half: %v", err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests while offline", n)
	}
}

func TestKiwixRefusesAnUnusableCatalogEntry(t *testing.T) {
	f := newFakeKiwix(t)
	mod := func(fn func(s *catalog.ContentSource)) plan.CatalogItem {
		it := kiwixCatalog(f.base(), "latest")
		fn(&it.Content.Source)
		return it
	}
	cases := map[string]plan.CatalogItem{
		"http base":         mod(func(s *catalog.ContentSource) { s.BaseURL = "http://download.example.test/zim/" }),
		"no host":           mod(func(s *catalog.ContentSource) { s.BaseURL = "https:///zim/" }),
		"credentials":       mod(func(s *catalog.ContentSource) { s.BaseURL = "https://user:pw@download.example.test/zim/" }),
		"query":             mod(func(s *catalog.ContentSource) { s.BaseURL = "https://download.example.test/zim/?x=1" }),
		"book with slash":   mod(func(s *catalog.ContentSource) { s.Book = "a/b" }),
		"book with space":   mod(func(s *catalog.ContentSource) { s.Book = "a b" }),
		"empty book":        mod(func(s *catalog.ContentSource) { s.Book = "" }),
		"unknown policy":    mod(func(s *catalog.ContentSource) { s.VersionPolicy = "sometimes" }),
		"latest with a pin": mod(func(s *catalog.ContentSource) { s.Checksum = "pinned" }),
		"pinned no block":   mod(func(s *catalog.ContentSource) { s.VersionPolicy = "pinned" }),
		"pinned traversal": mod(func(s *catalog.ContentSource) {
			s.VersionPolicy = "pinned"
			s.Pinned = &catalog.KiwixPinned{File: "../x.zim"}
		}),
		"pinned not zim": mod(func(s *catalog.ContentSource) {
			s.VersionPolicy = "pinned"
			s.Pinned = &catalog.KiwixPinned{File: "x.bin"}
		}),
		"pinned bad sha": mod(func(s *catalog.ContentSource) {
			s.VersionPolicy = "pinned"
			s.Pinned = &catalog.KiwixPinned{File: "x.zim", SHA256: "abc"}
		}),
		"not a kiwix source": mod(func(s *catalog.ContentSource) { s.Type = "url" }),
	}
	for name, it := range cases {
		_, err := f.kiwix().ResolveItem(context.Background(), it, plan.ItemPlan{})
		if re := itemErr(t, err); re.Code != doctor.SYN030 || !re.Permanent {
			t.Errorf("%s: %+v", name, re)
		}
		if _, err := f.kiwix().Resolve(context.Background(), it); CodeOf(err) != doctor.SYN030 {
			t.Errorf("%s (planner): %v", name, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests for unusable entries", n)
	}
}

func TestKiwixRedirectToPlainHTTPIsRefused(t *testing.T) {
	f := newFakeKiwix(t)
	plainTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") }))
	defer plainTarget.Close()
	f.mirrorRedirect = plainTarget.URL
	if _, err := f.kiwix().Resolve(context.Background(), kiwixCatalog(f.base(), "latest")); err == nil {
		t.Fatal("a redirect to http must fail")
	}
}

// ---- the download source -----------------------------------------------------------------------------

func (f *fakeKiwix) source(t *testing.T) *kiwixDownload {
	t.Helper()
	res, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Files[0].Source.(*kiwixDownload)
}

func readAll(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	if body == nil {
		t.Fatal("no body")
	}
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKiwixSourceFollowsTheMirrorAndResumes(t *testing.T) {
	f := newFakeKiwix(t)
	src := f.source(t)
	body, resp, err := src.Open(context.Background(), 0, "")
	if err != nil || resp.Status != 200 || resp.Size != int64(len(f.zim)) {
		t.Fatalf("%+v %v", resp, err)
	}
	if string(readAll(t, body)) != string(f.zim) || f.count("mirror GET") != 1 {
		t.Fatalf("not served by the mirror: %v", f.requests())
	}
	body, resp, err = src.Open(context.Background(), 100, resp.ETag)
	if err != nil || resp.Status != 206 || resp.Offset != 100 || resp.Size != int64(len(f.zim)) {
		t.Fatalf("resume: %+v %v", resp, err)
	}
	if string(readAll(t, body)) != string(f.zim[100:]) {
		t.Fatal("the resumed bytes are wrong")
	}
	if src.direct {
		t.Fatal("a healthy mirror must not trigger the fallback")
	}
	body, resp, err = src.Open(context.Background(), int64(len(f.zim)), "")
	if err != nil || resp.Status != 416 || body != nil || resp.Size != int64(len(f.zim)) {
		t.Fatalf("416: %+v %v", resp, err)
	}
}

func TestKiwixSourceGoesDirectAfterTwoMirrorAnswersThatIgnoreRange(t *testing.T) {
	f := newFakeKiwix(t)
	f.mirrorMode = "norange"
	src := f.source(t)

	// First strike: the 200 is returned as it is (the downloader restarts the file from zero).
	body, resp, err := src.Open(context.Background(), 100, "")
	if err != nil || resp.Status != 200 {
		t.Fatalf("first: %+v %v", resp, err)
	}
	_ = readAll(t, body)
	if src.direct {
		t.Fatal("one strike must not switch yet")
	}
	// Second strike: the origin is asked, with Accept: application/octet-stream and no redirect.
	body, resp, err = src.Open(context.Background(), 100, "")
	if err != nil || resp.Status != 206 || resp.Offset != 100 {
		t.Fatalf("second: %+v %v", resp, err)
	}
	if string(readAll(t, body)) != string(f.zim[100:]) {
		t.Fatal("the direct bytes are wrong")
	}
	if !src.direct || f.count("origin GET", kxFile, "accept=application/octet-stream", "range=bytes=100-") != 1 {
		t.Fatalf("not served directly: %v", f.requests())
	}
	// Every later attempt goes straight to the origin.
	mirrorBefore := f.count("mirror GET")
	body, resp, err = src.Open(context.Background(), 0, "")
	if err != nil || resp.Status != 200 {
		t.Fatalf("third: %+v %v", resp, err)
	}
	_ = readAll(t, body)
	if f.count("mirror GET") != mirrorBefore {
		t.Fatalf("the mirror was used again: %v", f.requests())
	}
}

func TestKiwixSourceGoesDirectAtOnceWhenTheMirrorHasAnotherSize(t *testing.T) {
	f := newFakeKiwix(t)
	f.mirrorMode = "wrongsize"
	src := f.source(t)
	body, resp, err := src.Open(context.Background(), 0, "")
	if err != nil || resp.Status != 200 || resp.Size != int64(len(f.zim)) {
		t.Fatalf("%+v %v", resp, err)
	}
	if string(readAll(t, body)) != string(f.zim) || !src.direct || f.count("origin GET", "accept=application/octet-stream") != 1 {
		t.Fatalf("not direct: %v", f.requests())
	}
}

func TestKiwixSourceRefusesAnOriginThatRedirectsADirectDownload(t *testing.T) {
	f := newFakeKiwix(t)
	f.mirrorMode = "wrongsize"
	f.originRedirects = true
	src := f.source(t)
	_, _, err := src.Open(context.Background(), 0, "")
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindPermanent || de.Code != doctor.SYN030 {
		t.Fatalf("want a permanent LAI-SYN-030, got %v", err)
	}
}

func TestKiwixSourceReportsMissingFilesAsStatus(t *testing.T) {
	f := newFakeKiwix(t)
	src := f.source(t)
	f.noZIMOnMirror = true
	body, resp, err := src.Open(context.Background(), 0, "")
	if err != nil || body != nil || resp.Status != 404 {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestKiwixSourceThroughTheRealDownloader(t *testing.T) {
	for _, mode := range []string{"", "wrongsize"} {
		t.Run("mirror "+mode, func(t *testing.T) {
			f := newFakeKiwix(t)
			f.mirrorMode = mode
			res, err := f.kiwix().ResolveItem(context.Background(), kiwixCatalog(f.base(), "latest"), plan.ItemPlan{})
			if err != nil {
				t.Fatal(err)
			}
			fl := res.Files[0]
			root := t.TempDir()
			d := download.New(download.Options{})
			out, err := d.Fetch(context.Background(), download.FileJob{
				RootID: "internal", Root: root, Item: "library", Version: res.Version, Name: fl.Path, URL: fl.URL,
				ExpectedSize: fl.Size, ExpectedSHA256: fl.SHA256, Commit: true, Source: fl.Source,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if out.SHA256 != f.sum {
				t.Fatalf("digest %s", out.SHA256)
			}
			p := filepath.Join(root, ".staging", "library", res.Version, fl.Path)
			if err := VerifyZIMFile(context.Background(), p); err != nil {
				t.Fatalf("the downloaded ZIM does not verify: %v", err)
			}
			if _, err := os.Stat(p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
