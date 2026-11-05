package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// fakeOrigin is a file host: each path has a body (or just headers) and a sidecar can be served.
type fakeOrigin struct {
	srv *httptest.Server

	mu    sync.Mutex
	files map[string]*originFile
	reqs  []string
}

type originFile struct {
	body         []byte
	etag         string
	lastModified string
	status       int    // 0 = 200
	redirect     string // answer 302 to this URL
	noLength     bool
}

func newFakeOrigin(r *webRig) *fakeOrigin {
	o := &fakeOrigin{files: map[string]*originFile{}}
	o.srv = r.serve(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.reqs = append(o.reqs, req.Method+" "+req.URL.Path)
		f, ok := o.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		if f.redirect != "" {
			http.Redirect(w, req, f.redirect, http.StatusFound)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		if f.etag != "" {
			w.Header().Set("ETag", f.etag)
		}
		if f.lastModified != "" {
			w.Header().Set("Last-Modified", f.lastModified)
		}
		if !f.noLength {
			w.Header().Set("Content-Length", strconv.Itoa(len(f.body)))
		}
		w.WriteHeader(http.StatusOK)
		if req.Method != http.MethodHead {
			_, _ = w.Write(f.body)
		}
	}))
	return o
}

func (o *fakeOrigin) put(path string, body []byte, etag, lm string) *originFile {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := &originFile{body: body, etag: etag, lastModified: lm}
	o.files[path] = f
	return f
}

func (o *fakeOrigin) gets() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, r := range o.reqs {
		if strings.HasPrefix(r, "GET ") {
			n++
		}
	}
	return n
}

func (o *fakeOrigin) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.reqs)
}

func urlSetup(t *testing.T) (*webRig, *fakeOrigin, *URLSource) {
	r := newWebRig(t)
	o := newFakeOrigin(r)
	return r, o, &URLSource{W: r.w}
}

func tofuItem(o *fakeOrigin, names ...string) plan.CatalogItem {
	var files []catalog.URLFile
	for _, n := range names {
		files = append(files, catalog.URLFile{URL: o.srv.URL + "/export/" + n})
	}
	return urlContent("places-region-alpha", "maps", catalog.ContentSource{
		Type: "url", Files: files, VersionPolicy: "latest", Checksum: "computed", Trust: "tofu",
	})
}

// A pinned single file: the version is the first 12 hex digits of its sha256, the size comes from a HEAD
// and the digest is the catalog's alone.
func TestURLPinnedSingle(t *testing.T) {
	_, o, u := urlSetup(t)
	body := []byte("tool bundle bytes")
	sha := sum256(body)
	o.put("/dl/tool-bundle.tar.gz", body, `"e1"`, "")
	got, err := u.Lookup(context.Background(), urlContent("kit-tool-bundle", "kit", catalog.ContentSource{
		Type: "url", URL: o.srv.URL + "/dl/tool-bundle.tar.gz?token=SECRET", SHA256: strings.ToUpper(sha),
	}))
	if err != nil {
		t.Fatal(err)
	}
	f := got.Files[0]
	if got.Version != sha[:12] || got.Trust != manifest.TrustPinned || got.SortKey != "2026.11.02-1" || got.Extract {
		t.Errorf("result = %+v", got)
	}
	if f.Path != "tool-bundle.tar.gz" || f.Size != int64(len(body)) || f.SHA256 != sha || f.SHA256Source != "catalog" || f.Auth || f.Kind != "file" {
		t.Errorf("file = %+v", f)
	}
	// What is recorded about the source never carries the query of a URL.
	if strings.Contains(got.Meta["url"], "SECRET") || got.Meta["url"] != o.srv.URL+"/dl/tool-bundle.tar.gz" {
		t.Errorf("meta = %v", got.Meta)
	}
}

// Several files are resolved the same way, in catalog order, each with its own digest and size.
func TestURLPinnedMultiFile(t *testing.T) {
	_, o, u := urlSetup(t)
	a, b := []byte("alpha"), []byte("bravo bravo")
	o.put("/x/a.zip", a, "", "")
	o.put("/x/b.zip", b, "", "")
	got, err := u.Lookup(context.Background(), urlContent("multi", "maps", catalog.ContentSource{
		Type: "url", Files: []catalog.URLFile{{URL: o.srv.URL + "/x/b.zip", SHA256: sum256(b)}, {URL: o.srv.URL + "/x/a.zip", SHA256: sum256(a)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != sum256(b)[:12] || len(got.Files) != 2 {
		t.Fatalf("result = %+v", got)
	}
	if got.Files[0].Path != "b.zip" || got.Files[0].Size != int64(len(b)) || got.Files[1].Path != "a.zip" || got.Files[1].SHA256 != sum256(a) {
		t.Errorf("files = %+v", got.Files)
	}
}

// A file the catalog gives a name lands under that name (a path inside the item), so a release's signed
// SHA256SUMS, whose own name the engine reserves, can be a file of the item (issue #360).
func TestURLPinnedFileNames(t *testing.T) {
	_, o, u := urlSetup(t)
	prog, sums, sig := []byte("program"), []byte("sums"), []byte("signature")
	o.put("/rel/prog", prog, "", "")
	o.put("/rel/SHA256SUMS", sums, "", "")
	o.put("/rel/SHA256SUMS.minisig", sig, "", "")
	got, err := u.Lookup(context.Background(), urlContent("kit-agent-binaries", "kit", catalog.ContentSource{
		Type: "url", Files: []catalog.URLFile{
			{URL: o.srv.URL + "/rel/prog", SHA256: sum256(prog)},
			{URL: o.srv.URL + "/rel/SHA256SUMS", SHA256: sum256(sums), Name: "release/SHA256SUMS"},
			{URL: o.srv.URL + "/rel/SHA256SUMS.minisig", SHA256: sum256(sig), Name: "release/SHA256SUMS.minisig"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
		if !manifest.ValidFilePath(f.Path) {
			t.Errorf("%q is not a usable path", f.Path)
		}
	}
	if strings.Join(paths, " ") != "prog release/SHA256SUMS release/SHA256SUMS.minisig" {
		t.Errorf("paths = %v", paths)
	}
	// The digests stay each file's own, and the version stays the first file's.
	if got.Files[1].SHA256 != sum256(sums) || got.Files[2].SHA256 != sum256(sig) || got.Version != sum256(prog)[:12] {
		t.Errorf("result = %+v", got)
	}

	// The reserved name without a folder is still refused, as are names that leave the item folder or
	// repeat another file's name.
	sha := strings.Repeat("ab", 32)
	for name, files := range map[string][]catalog.URLFile{
		"the reserved name":   {{URL: "https://d.example.test/r/x", SHA256: sha, Name: "SHA256SUMS"}},
		"the manifest name":   {{URL: "https://d.example.test/r/x", SHA256: sha, Name: "MANIFEST.json"}},
		"a parent folder":     {{URL: "https://d.example.test/r/x", SHA256: sha, Name: "../x"}},
		"an absolute path":    {{URL: "https://d.example.test/r/x", SHA256: sha, Name: "/x"}},
		"a name used twice":   {{URL: "https://d.example.test/r/x", SHA256: sha, Name: "a/x"}, {URL: "https://d.example.test/r/y", SHA256: sha, Name: "a/x"}},
		"a name as a default": {{URL: "https://d.example.test/r/x", SHA256: sha}, {URL: "https://d.example.test/r/y", SHA256: sha, Name: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := u.Lookup(context.Background(), urlContent("x", "kit", catalog.ContentSource{Type: "url", Files: files}))
			codeIs(t, err, doctor.SYN033, true)
		})
	}
	// A release URL with the reserved name and no catalog name is the failure the issue found.
	_, err = u.Lookup(context.Background(), urlContent("x", "kit", catalog.ContentSource{
		Type: "url", Files: []catalog.URLFile{{URL: "https://d.example.test/r/SHA256SUMS", SHA256: sha}}}))
	codeIs(t, err, doctor.SYN033, true)
}

// An origin that cannot answer a HEAD does not stop a pinned item (the size is then unknown); one that
// no longer has the file does (LAI-SYN-030).
func TestURLPinnedHeadFailures(t *testing.T) {
	_, o, u := urlSetup(t)
	body := []byte("x")
	f := o.put("/x/a.bin", body, "", "")
	f.status = http.StatusMethodNotAllowed
	it := urlContent("p", "kit", catalog.ContentSource{Type: "url", URL: o.srv.URL + "/x/a.bin", SHA256: sum256(body)})
	got, err := u.Lookup(context.Background(), it)
	if err != nil || got.Files[0].Size != 0 {
		t.Fatalf("405: %+v, %v", got, err)
	}
	_, err = u.Lookup(context.Background(), urlContent("p", "kit", catalog.ContentSource{Type: "url", URL: o.srv.URL + "/x/gone.bin", SHA256: sum256(body)}))
	codeIs(t, err, doctor.SYN030, true)
}

// A sidecar gives the digest: SHA256SUMS over https from the host of the file, matched by the file's
// name; the version is that name and the trust is "sidecar".
func TestURLSidecarOK(t *testing.T) {
	_, o, u := urlSetup(t)
	iso := []byte("iso bytes")
	o.put("/rel/26.04/ubuntu-26.04-live-server-amd64.iso", iso, "", "")
	it := urlContent("kit-ubuntu-iso-26.04", "kit", catalog.ContentSource{
		Type: "url", URL: o.srv.URL + "/rel/26.04/ubuntu-26.04-live-server-amd64.iso", Checksum: "sidecar", SidecarURL: o.srv.URL + "/rel/26.04/SHA256SUMS",
	})
	o.put("/rel/26.04/SHA256SUMS", []byte(
		sum256([]byte("zzz"))+"  other.iso\r\n# a comment\n\n"+sum256(iso)+" *./ubuntu-26.04-live-server-amd64.iso\n"), "", "")
	got, err := u.Lookup(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	f := got.Files[0]
	if got.Version != "ubuntu-26.04-live-server-amd64.iso" || got.Trust != manifest.TrustSidecar || f.SHA256 != sum256(iso) || f.SHA256Source != "sidecar" || f.Size != int64(len(iso)) {
		t.Errorf("result = %+v", got)
	}
	if got.Meta["sidecar_url"] != o.srv.URL+"/rel/26.04/SHA256SUMS" {
		t.Errorf("meta = %v", got.Meta)
	}
}

// A sidecar that is malformed, that does not list the file, that is empty, or that is not served from the
// origin is refused (LAI-SYN-033: nothing can be downloaded without a digest, and the next run asks again).
func TestURLSidecarBad(t *testing.T) {
	good := sum256([]byte("iso"))
	cases := map[string]struct {
		body    string
		status  int
		code    string
		perm    bool
		sidecar func(o *fakeOrigin) string
	}{
		"malformed line":  {body: good + "  a.iso\nthis is not a checksum line\n", code: doctor.SYN033},
		"short digest":    {body: "abcdef  a.iso\n", code: doctor.SYN033},
		"empty":           {body: "\n# nothing\n", code: doctor.SYN033},
		"name not listed": {body: good + "  other.iso\n", code: doctor.SYN033},
		"name differs":    {body: good + "  A.iso\n", code: doctor.SYN033},
		"two digests":     {body: good + "  a.iso\n" + strings.Repeat("0", 64) + "  a.iso\n", code: doctor.SYN033},
		"no sidecar":      {status: http.StatusNotFound, code: doctor.SYN033},
		"other host":      {code: doctor.SYN033, perm: true, sidecar: func(*fakeOrigin) string { return "https://elsewhere.example.test/SHA256SUMS" }},
		"http sidecar":    {code: doctor.SYN033, perm: true, sidecar: func(o *fakeOrigin) string { return strings.Replace(o.srv.URL, "https", "http", 1) + "/SHA256SUMS" }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, o, u := urlSetup(t)
			o.put("/r/a.iso", []byte("iso"), "", "")
			sf := o.put("/r/SHA256SUMS", []byte(c.body), "", "")
			sf.status = c.status
			side := o.srv.URL + "/r/SHA256SUMS"
			if c.sidecar != nil {
				side = c.sidecar(o)
			}
			_, err := u.Lookup(context.Background(), urlContent("iso", "kit", catalog.ContentSource{
				Type: "url", URL: o.srv.URL + "/r/a.iso", Checksum: "sidecar", SidecarURL: side,
			}))
			codeIs(t, err, c.code, c.perm)
		})
	}
}

// A checksum file must come from the origin: a redirect to another host is refused, a redirect inside
// the host is followed.
func TestURLSidecarRedirects(t *testing.T) {
	r, o, u := urlSetup(t)
	other := newFakeOrigin(r)
	iso := []byte("iso")
	o.put("/r/a.iso", iso, "", "")
	other.put("/SHA256SUMS", []byte(sum256(iso)+"  a.iso\n"), "", "")
	o.put("/r/SHA256SUMS", nil, "", "").redirect = other.srv.URL + "/SHA256SUMS"
	it := urlContent("iso", "kit", catalog.ContentSource{Type: "url", URL: o.srv.URL + "/r/a.iso", Checksum: "sidecar", SidecarURL: o.srv.URL + "/r/SHA256SUMS"})
	_, err := u.Lookup(context.Background(), it)
	codeIs(t, err, doctor.SYN033, false)
	if other.count() != 0 {
		t.Error("the redirect to another host was followed")
	}
	o.put("/r/SHA256SUMS", nil, "", "").redirect = "/r/real-sums"
	o.put("/r/real-sums", []byte(sum256(iso)+"  a.iso\n"), "", "")
	if got, err := u.Lookup(context.Background(), it); err != nil || got.Files[0].SHA256 != sum256(iso) {
		t.Errorf("a redirect on the same host: %+v, %v", got, err)
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("a", 64)
	h2 := strings.Repeat("B", 64)
	ok, err := ParseSums([]byte("\ufeff" + h + "  one.bin\r\n" + h2 + " *two.bin\n#c\n\n" + h + "  ./three bin.iso\n" + h + "  one.bin\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ok["one.bin"] != h || ok["two.bin"] != strings.ToLower(h2) || ok["three bin.iso"] != h || len(ok) != 3 {
		t.Errorf("sums = %v", ok)
	}
	for name, in := range map[string]string{
		"empty":         "",
		"comments only": "# x\n",
		"one space":     h + " one.bin\n",
		"no name":       h + "  \n",
		"non hex":       strings.Repeat("g", 64) + "  one.bin\n",
		"long digest":   h + "a  one.bin\n",
		"clear signed":  "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\n" + h + "  one.bin\n",
		"conflict":      h + "  one.bin\n" + h2 + "  one.bin\n",
	} {
		if _, err := ParseSums([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A computed (trust on first use) item: no digest is known, the version is the first 12 hex digits of the
// sha256 of the per-file "<url>\n<ETag>\n<Last-Modified>\n<Content-Length>\n" text in catalog order.
func TestURLComputedTOFU(t *testing.T) {
	_, o, u := urlSetup(t)
	o.put("/export/CR.zip", []byte("12345"), `"etag-cr"`, "Mon, 02 Nov 2026 10:00:00 GMT")
	o.put("/export/PA.zip", []byte("1234567"), `"etag-pa"`, "Sun, 01 Nov 2026 10:00:00 GMT")
	it := tofuItem(o, "CR.zip", "PA.zip")

	got, err := u.Lookup(context.Background(), it)
	if err != nil {
		t.Fatal(err)
	}
	text := o.srv.URL + "/export/CR.zip\n\"etag-cr\"\nMon, 02 Nov 2026 10:00:00 GMT\n5\n" +
		o.srv.URL + "/export/PA.zip\n\"etag-pa\"\nSun, 01 Nov 2026 10:00:00 GMT\n7\n"
	if want := sum256([]byte(text))[:12]; got.Version != want {
		t.Errorf("version = %s, want %s", got.Version, want)
	}
	if got.Trust != manifest.TrustTOFU || got.SortKey != "2026-11-02T12:00:00Z" {
		t.Errorf("result = %+v", got)
	}
	for i, f := range got.Files {
		if f.SHA256 != "" || f.SHA256Source != "computed_tofu" || f.Size != []int64{5, 7}[i] {
			t.Errorf("file %d = %+v", i, f)
		}
	}
	if got.Files[0].Path != "CR.zip" || got.Files[1].Path != "PA.zip" {
		t.Errorf("order = %+v", got.Files)
	}

	// Stable across runs, whatever else changes: the clock moves, the version does not.
	again, err := u.Lookup(context.Background(), it)
	if err != nil || again.Version != got.Version {
		t.Fatalf("second run: %+v, %v", again, err)
	}

	// It changes when the origin changes any one file: an ETag, a date, a length.
	o.put("/export/PA.zip", []byte("1234567"), `"etag-pa-2"`, "Sun, 01 Nov 2026 10:00:00 GMT")
	etag, _ := u.Lookup(context.Background(), it)
	o.put("/export/PA.zip", []byte("1234567"), `"etag-pa"`, "Tue, 03 Nov 2026 10:00:00 GMT")
	date, _ := u.Lookup(context.Background(), it)
	o.put("/export/PA.zip", []byte("12345678"), `"etag-pa"`, "Sun, 01 Nov 2026 10:00:00 GMT")
	length, _ := u.Lookup(context.Background(), it)
	seen := map[string]bool{got.Version: true}
	for name, v := range map[string]string{"etag": etag.Version, "date": date.Version, "length": length.Version} {
		if v == "" || seen[v] {
			t.Errorf("%s change did not give a new version (%s)", name, v)
		}
		seen[v] = true
	}
	// The order of the catalog is part of the rule.
	o.put("/export/PA.zip", []byte("1234567"), `"etag-pa"`, "Sun, 01 Nov 2026 10:00:00 GMT")
	swapped, _ := u.Lookup(context.Background(), tofuItem(o, "PA.zip", "CR.zip"))
	if swapped.Version == got.Version {
		t.Error("the version ignores the order of the files")
	}
}

// computed needs version_policy: latest and trust: tofu, and every file must answer its HEAD.
func TestURLComputedGuards(t *testing.T) {
	_, o, u := urlSetup(t)
	o.put("/export/CR.zip", []byte("1"), `"e"`, "")
	base := catalog.ContentSource{Type: "url", Files: []catalog.URLFile{{URL: o.srv.URL + "/export/CR.zip"}}, Checksum: "computed"}
	for _, mut := range map[string]func(*catalog.ContentSource){
		"no trust":      func(s *catalog.ContentSource) { s.VersionPolicy = "latest" },
		"pinned policy": func(s *catalog.ContentSource) { s.Trust, s.VersionPolicy = "tofu", "pinned" },
		"no policy":     func(s *catalog.ContentSource) { s.Trust = "tofu" },
	} {
		src := base
		mut(&src)
		_, err := u.Lookup(context.Background(), urlContent("x", "maps", src))
		codeIs(t, err, doctor.SYN033, true)
		_, err = u.Resolve(context.Background(), urlContent("x", "maps", src))
		codeIs(t, err, doctor.SYN033, true)
	}
	// A file whose HEAD fails makes the version unknowable: transient.
	o.files["/export/CR.zip"].status = http.StatusServiceUnavailable
	_, err := u.Lookup(context.Background(), tofuItem(o, "CR.zip"))
	codeIs(t, err, doctor.SYN033, false)
	o.files["/export/CR.zip"].status = 0
	_, err = u.Lookup(context.Background(), tofuItem(o, "CR.zip", "NOPE.zip"))
	codeIs(t, err, doctor.SYN030, true)
}

// Unusable catalog entries are refused before any request.
func TestURLUnusableItems(t *testing.T) {
	_, o, u := urlSetup(t)
	sha := strings.Repeat("ab", 32)
	cases := map[string]catalog.ContentSource{
		"no url":         {Type: "url", SHA256: sha},
		"http":           {Type: "url", URL: "http://data.example.test/a.bin", SHA256: sha},
		"credentials":    {Type: "url", URL: "https://" + "person-a" + ":" + "x" + "@data.example.test/a.bin", SHA256: sha},
		"no file name":   {Type: "url", URL: "https://data.example.test/", SHA256: sha},
		"no sha":         {Type: "url", URL: "https://data.example.test/a.bin"},
		"twice":          {Type: "url", Files: []catalog.URLFile{{URL: "https://d.example.test/a/x.bin", SHA256: sha}, {URL: "https://d.example.test/b/x.bin", SHA256: sha}}},
		"extract two":    {Type: "url", Files: []catalog.URLFile{{URL: "https://d.example.test/a.zip", SHA256: sha}, {URL: "https://d.example.test/b.zip", SHA256: sha}}, Extract: truePtr()},
		"unknown":        {Type: "url", URL: "https://data.example.test/a.bin", Checksum: "md5"},
		"sidecar no url": {Type: "url", URL: "https://data.example.test/a.bin", Checksum: "sidecar"},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := u.Lookup(context.Background(), urlContent("x", "kit", src))
			codeIs(t, err, doctor.SYN033, true)
		})
	}
	if o.count() != 0 {
		t.Errorf("%d requests", o.count())
	}
}

// The planner half: a pinned item is what plan.FromCatalog says (no request needed for the version), a
// sidecar item is its file name, a computed item follows the HEAD rule and agrees with the engine half.
func TestURLPlannerHalf(t *testing.T) {
	_, o, u := urlSetup(t)
	body := []byte("pinned")
	o.put("/dl/p.bin", body, "", "")
	pinned := urlContent("p", "kit", catalog.ContentSource{Type: "url", URL: o.srv.URL + "/dl/p.bin", SHA256: sum256(body)})
	pinned.Content.ExpectedSizeBytes = 99
	res, err := u.Resolve(context.Background(), pinned)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := plan.FromCatalog(pinned, ""); res.Version != want.Version || res.SizeBytes != 99 {
		t.Errorf("pinned = %+v, FromCatalog = %+v", res, want)
	}

	o.put("/r/a.iso", []byte("iso!"), "", "")
	side := urlContent("iso", "kit", catalog.ContentSource{Type: "url", URL: o.srv.URL + "/r/a.iso", Checksum: "sidecar", SidecarURL: o.srv.URL + "/r/SHA256SUMS"})
	side.Content.ExpectedSizeBytes = 4
	if res, err = u.Resolve(context.Background(), side); err != nil || res.Version != "a.iso" || res.SizeBytes != 4 {
		t.Errorf("sidecar = %+v, %v", res, err)
	}
	// Neither of those made a request: the planner's answer for a pinned or sidecar item is the catalog's.
	if o.count() != 0 {
		t.Errorf("%d requests for a pinned and a sidecar item", o.count())
	}

	o.put("/export/CR.zip", []byte("12345"), `"e1"`, "lm")
	o.put("/export/PA.zip", []byte("123"), `"e2"`, "lm")
	tofu := tofuItem(o, "CR.zip", "PA.zip")
	res, err = u.Resolve(context.Background(), tofu)
	if err != nil {
		t.Fatal(err)
	}
	full, err := u.Lookup(context.Background(), tofu)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != full.Version || res.SizeBytes != 8 || res.SortKey == "" {
		t.Errorf("computed planner = %+v, engine = %s", res, full.Version)
	}
	// A computed item with an unreachable origin is unresolved, never guessed.
	o.files["/export/PA.zip"].status = http.StatusBadGateway
	if _, err := u.Resolve(context.Background(), tofu); err == nil {
		t.Error("an unreachable computed item resolved")
	}
}

// Through the engine half: the Resolution of a plain url item is a file item; a computed one records the
// downloader's digest later (no sha256 now) and the tofu trust.
func TestURLEngineResolution(t *testing.T) {
	_, o, u := urlSetup(t)
	o.put("/export/CR.zip", []byte("12345"), `"e1"`, "")
	res, err := u.Engine().Resolve(context.Background(), tofuItem(o, "CR.zip"), plan.ItemPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir || res.Trust != manifest.TrustTOFU || len(res.Files) != 1 || res.Files[0].SHA256 != "" || res.Files[0].SHA256Source != "computed_tofu" || res.Files[0].Size != 5 || res.Resolver != "url" {
		t.Errorf("resolution = %+v", res)
	}
	if res.Source.Type != "url" || res.Source.String("url") != o.srv.URL+"/export/CR.zip" {
		t.Errorf("source = %+v", res.Source)
	}
}
