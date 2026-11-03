package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// Codes of the kiwix resolver besides the shared ones (CodeNoVersion is LAI-SYN-033).
const (
	// CodeKiwixSidecar: the .sha256 file of a ZIM is missing, redirected, malformed or for another file
	// (LAI-SYN-022). The old copy is kept and the next run tries again.
	CodeKiwixSidecar = doctor.SYN022
)

// Bounds of the kiwix metadata requests.
const (
	kiwixListingMax = 8 << 20 // a directory listing
	kiwixSidecarMax = 4 << 10 // "<64 hex>  <file>\n"
)

// kiwixSidecarRe is the body of a sidecar: 64 lowercase hex digits, two spaces, the file name, and at
// most one line end. Uppercase hex, a single space or a missing name are all refused.
var kiwixSidecarRe = regexp.MustCompile(`^([0-9a-f]{64})  ([^\s/\\]+)\r?\n?$`)

// Kiwix is the kiwix resolver (ark-sync.md section 4.9.2): the newest dated ZIM of a book, found by
// parsing the directory listing of base_url, with its sha256 from the origin's own .sha256 file.
//
// It has two halves over one type. Resolve is the planner's half (plan.Resolver: version and size,
// registered with resolve.Register). ResolveItem is the engine's half (the files with their URL and
// checksum, registered by the application as a run.ItemResolver). Neither writes anything: the ZIM is
// downloaded by the sync downloader through the Source this resolver gives it, and its internal MD5 is
// checked by the validator of RegisterZIMValidator.
type Kiwix struct {
	// Env supplies Online (the second lock in front of every network call), Logger and Now. Required.
	Env *Env
	// HTTP is the client of the metadata requests and of the downloads' Source. nil builds the default:
	// TLS 1.2 or later, system roots, no proxy, redirects only to https and at most 10. A test passes
	// one that trusts its server.
	HTTP *http.Client
	// Version is the agent version in the User-Agent; default "dev".
	Version string

	once sync.Once
	ro   *readOnlyHTTP
}

// web is the metadata client built from HTTP and Version.
func (k *Kiwix) web() *readOnlyHTTP {
	k.once.Do(func() { k.ro = &readOnlyHTTP{Client: k.HTTP, Version: k.Version} })
	return k.ro
}

// SourceType implements NamedResolver.
func (*Kiwix) SourceType() string { return "kiwix" }

// kiwixSource is a validated kiwix catalog entry.
type kiwixSource struct {
	book    string
	base    *url.URL // https, with a trailing slash in its path
	policy  string   // "latest" or "pinned"
	pinned  *kiwixPin
	catalog plan.CatalogItem
}

type kiwixPin struct {
	file   string
	sha256 string
}

// fileURL is base_url + the file name.
func (s kiwixSource) fileURL(name string) string {
	u := *s.base
	u.Path, u.RawPath = path.Join(s.base.Path, name), ""
	return u.String()
}

// source validates the catalog fields of a kiwix item. Everything that becomes a URL or a pattern is
// checked here: the book against a strict character set, the base URL for https, a host and no
// credentials, query or fragment, and a pinned file for a plain .zim name.
func (k *Kiwix) source(it plan.CatalogItem) (kiwixSource, error) {
	if it.Content == nil || it.Content.Source.Type != "kiwix" {
		return kiwixSource{}, fail(CodeRefused, nil, "item %s is not a kiwix item", it.ID)
	}
	src := it.Content.Source
	u, err := url.Parse(src.BaseURL)
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return kiwixSource{}, fail(CodeRefused, nil, "item %s has no usable base_url", it.ID)
	case u.Scheme != "https":
		return kiwixSource{}, fail(CodeRefused, nil, "item %s: base_url must be https", it.ID)
	case u.User != nil, u.RawQuery != "", u.Fragment != "":
		return kiwixSource{}, fail(CodeRefused, nil, "item %s: base_url carries credentials, a query or a fragment", it.ID)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	s := kiwixSource{book: src.Book, base: u, policy: src.VersionPolicy, catalog: it}
	switch s.policy {
	case "latest":
		if !kiwixBookRe.MatchString(s.book) {
			return kiwixSource{}, fail(CodeRefused, nil, "item %s: %q is not a usable book name", it.ID, src.Book)
		}
		if src.Checksum != "sidecar" {
			return kiwixSource{}, fail(CodeRefused, nil, "item %s follows the newest file and needs checksum: sidecar", it.ID)
		}
	case "pinned":
		p := src.Pinned
		if p == nil || p.File == "" {
			return kiwixSource{}, fail(CodeRefused, nil, "item %s is pinned but names no file", it.ID)
		}
		if !strings.HasSuffix(p.File, ".zim") || p.File != path.Base(p.File) || !manifest.SafeName(p.File) {
			return kiwixSource{}, fail(CodeRefused, nil, "item %s: %q is not a usable ZIM file name", it.ID, truncateMsg(p.File))
		}
		sum := strings.ToLower(strings.TrimSpace(p.SHA256))
		if sum != "" && !manifest.IsSHA256(sum) {
			return kiwixSource{}, fail(CodeRefused, nil, "item %s: the pinned sha256 is not 64 hex digits", it.ID)
		}
		s.pinned = &kiwixPin{file: p.File, sha256: sum}
	default:
		return kiwixSource{}, fail(CodeRefused, nil, "item %s: version_policy %q is not latest or pinned", it.ID, truncateMsg(src.VersionPolicy))
	}
	return s, nil
}

func truncateMsg(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "..."
	}
	return s
}

// ---- the three requests -----------------------------------------------------------------------------

// newest reads the listing of base_url and returns the newest dated file of the book (step 1 and 2).
func (k *Kiwix) newest(ctx context.Context, s kiwixSource) (kiwixEntry, error) {
	r, err := k.web().do(ctx, http.MethodGet, s.base.String(), true, kiwixListingMax, nil)
	switch {
	case err != nil:
		return kiwixEntry{}, fail(CodeNoVersion, err, "cannot read the listing of the %s files", s.book)
	case r.Status != http.StatusOK:
		return kiwixEntry{}, fail(CodeNoVersion, nil, "the listing of the %s files answered %d", s.book, r.Status)
	case r.Truncated:
		return kiwixEntry{}, fail(CodeNoVersion, nil, "the listing of the %s files is too large", s.book)
	}
	entries := parseKiwixListing(r.Body, s.book)
	if len(entries) == 0 {
		return kiwixEntry{}, fail(CodeNoVersion, nil, "the listing has no dated %s file", s.book)
	}
	return entries[0], nil
}

// sidecar reads <base_url><file>.sha256 from the origin with redirects switched off (step 3): a redirect
// would let a mirror serve both a bad file and a matching hash. Every failure is LAI-SYN-022.
func (k *Kiwix) sidecar(ctx context.Context, s kiwixSource, file string) (string, error) {
	r, err := k.web().do(ctx, http.MethodGet, s.fileURL(file)+".sha256", false, kiwixSidecarMax, nil)
	switch {
	case err != nil:
		return "", fail(CodeKiwixSidecar, err, "cannot read the checksum file of %s", file)
	case r.Status >= 300 && r.Status < 400:
		return "", fail(CodeKiwixSidecar, nil, "the origin redirected the checksum file of %s, which is refused", file)
	case r.Status != http.StatusOK:
		return "", fail(CodeKiwixSidecar, nil, "the checksum file of %s answered %d", file, r.Status)
	case r.Truncated:
		return "", fail(CodeKiwixSidecar, nil, "the checksum file of %s is too long", file)
	}
	m := kiwixSidecarRe.FindSubmatch(r.Body)
	if m == nil {
		return "", fail(CodeKiwixSidecar, nil, "the checksum file of %s is not '<64 lowercase hex>  <file name>'", file)
	}
	if string(m[2]) != file {
		return "", fail(CodeKiwixSidecar, nil, "the checksum file of %s names another file", file)
	}
	return string(m[1]), nil
}

// size is the byte size of a file (step 4): HEAD with redirects followed, and, when the server refuses
// HEAD, a one-byte ranged GET whose Content-Range carries the total.
func (k *Kiwix) size(ctx context.Context, rawURL string) (int64, error) {
	r, err := k.web().do(ctx, http.MethodHead, rawURL, true, 0, nil)
	if err == nil && r.Status == http.StatusOK {
		if n, perr := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64); perr == nil && n > 0 {
			return n, nil
		}
	}
	r, rerr := k.web().do(ctx, http.MethodGet, rawURL, true, 1, map[string]string{"Range": "bytes=0-0"})
	if rerr == nil && r.Status == http.StatusPartialContent {
		if _, _, total, ok := originContentRange(r.Header.Get("Content-Range")); ok && total > 0 {
			return total, nil
		}
	}
	if err == nil {
		err = rerr
	}
	if err == nil {
		err = errors.New("the server did not say how large the file is")
	}
	return 0, fail(CodeNoVersion, err, "cannot read the size of %s", download.RedactURL(rawURL))
}

// ---- the planner's half -----------------------------------------------------------------------------

// Resolve implements plan.Resolver: the target version and the exact size, metadata only. A pinned
// item keeps the version of its file name (the size is read when the origin answers, else the
// catalog's). A latest item reads the listing and the size of the newest file; the sidecar is read by
// the engine's half, so a missing checksum file is a failure of the run, not an unresolved item.
func (k *Kiwix) Resolve(ctx context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	s, err := k.source(it)
	if err != nil {
		return plan.Resolved{}, err
	}
	if err := k.Env.requireOnline(); err != nil {
		return plan.Resolved{}, err
	}
	if s.policy == "pinned" {
		version, sortKey := kiwixPinnedVersion(s)
		size := it.CatalogBytes()
		if n, serr := k.size(ctx, s.fileURL(s.pinned.file)); serr == nil {
			size = n
		}
		return plan.Resolved{Version: version, SortKey: sortKey, SizeBytes: size}, nil
	}
	e, err := k.newest(ctx, s)
	if err != nil {
		return plan.Resolved{}, err
	}
	size, err := k.size(ctx, s.fileURL(e.File))
	if err != nil {
		return plan.Resolved{}, err
	}
	return plan.Resolved{Version: e.Version, SortKey: e.Version, SizeBytes: size}, nil
}

// kiwixPinnedVersion is the version of a pinned file: the "YYYY-MM" of its name, else the first 12
// digits of its sha256 (the planner's rule, plan.FromCatalog), else its name without ".zim".
func kiwixPinnedVersion(s kiwixSource) (version, sortKey string) {
	if r, ok := plan.FromCatalog(s.catalog, ""); ok && r.Version != "" {
		return r.Version, r.SortKey
	}
	v := strings.TrimSuffix(s.pinned.file, ".zim")
	return v, v
}

// ---- the engine's half ------------------------------------------------------------------------------

// ResolveItem is the engine's half (run.ItemResolver, bound with run.ItemResolverFunc): the files of
// the target version with their URL, size and checksum. The newest file is found again here, so a
// ZIM replaced since the plan was made is the one fetched. Errors are *item.ResolveError carrying the
// doctor code: LAI-SYN-033 when no dated file is found, LAI-SYN-022 for a sidecar that is missing,
// redirected, malformed or for another file.
func (k *Kiwix) ResolveItem(ctx context.Context, it plan.CatalogItem, _ plan.ItemPlan) (item.Resolution, error) {
	res, err := k.resolveItem(ctx, it)
	if err != nil {
		return item.Resolution{}, kiwixItemErr(err)
	}
	return res, nil
}

func (k *Kiwix) resolveItem(ctx context.Context, it plan.CatalogItem) (item.Resolution, error) {
	s, err := k.source(it)
	if err != nil {
		return item.Resolution{}, err
	}
	if err := k.Env.requireOnline(); err != nil {
		return item.Resolution{}, err
	}
	var (
		file, version, sortKey, sum, sumSource string
		trust                                  manifest.Trust
		fallbackSize                           = it.CatalogBytes()
	)
	switch {
	case s.policy == "pinned":
		file = s.pinned.file
		version, sortKey = kiwixPinnedVersion(s)
		if s.pinned.sha256 != "" {
			sum, sumSource, trust = s.pinned.sha256, "catalog", manifest.TrustPinned
		}
	default:
		e, nerr := k.newest(ctx, s)
		if nerr != nil {
			return item.Resolution{}, nerr
		}
		file, version, sortKey = e.File, e.Version, e.Version
	}
	if sum == "" {
		if sum, err = k.sidecar(ctx, s, file); err != nil {
			return item.Resolution{}, err
		}
		sumSource, trust = "kiwix_sidecar", manifest.TrustSidecar
	}
	fileURL := s.fileURL(file)
	size, err := k.size(ctx, fileURL)
	if err != nil {
		if s.policy != "pinned" || fallbackSize <= 0 {
			return item.Resolution{}, err
		}
		size = fallbackSize // a pinned item can be fetched without knowing the size ahead
	}
	src := manifest.Source{Type: "kiwix"}
	for _, kv := range [][2]string{{"book", s.book}, {"base_url", s.base.String()}, {"version_policy", s.policy}, {"file", file}} {
		if kv[1] == "" {
			continue
		}
		if src, err = src.With(kv[0], kv[1]); err != nil {
			return item.Resolution{}, fail(CodeRefused, err, "item %s: a source field is not usable", it.ID)
		}
	}
	return item.Resolution{
		Version: version, SortKey: sortKey, Source: src, Trust: trust, Resolver: "kiwix",
		Files: []item.File{{
			Path: file, Kind: "zim", URL: fileURL, Size: size, SHA256: sum, SHA256Source: sumSource,
			ZimMD5OK: new(bool),
			Source:   &kiwixDownload{k: k, url: fileURL, size: size},
		}},
	}, nil
}

// kiwixItemErr turns a resolver error into the engine's *item.ResolveError, keeping its doctor code.
// Nothing here is permanent: a listing without a dated file, a bad sidecar and a refused metadata
// request are all retried by the next run (the old copy stays); a catalog entry the resolver cannot use
// (LAI-SYN-030) and the offline refusal are not retried within the run.
func kiwixItemErr(err error) error {
	var re *item.ResolveError
	if errors.As(err, &re) {
		return err
	}
	code := CodeOf(err)
	if code == "" {
		code = CodeNoVersion
	}
	return &item.ResolveError{Code: code, Permanent: code == CodeRefused || code == CodeOffline, Err: err}
}

// ---- the download source ----------------------------------------------------------------------------

// kiwixDownload is the download.Source of a ZIM. It asks the URL as it is, following the mirror
// redirect (mirrors support Range). When a mirror misbehaves it falls back to the origin, once and for
// good for this download: a wrong size is a mirror that holds another file; a 200 to a Range request
// twice is a mirror that cannot resume. The fallback sends "Accept: application/octet-stream" to the
// origin host of base_url and refuses redirects, which is slower but deterministic (ark-sync.md
// section 4.9.2).
type kiwixDownload struct {
	k    *Kiwix
	url  string
	size int64

	mu      sync.Mutex
	strikes int
	direct  bool
}

// kiwixRangeStrikes is the number of 200 answers to a Range request after which the origin is used.
const kiwixRangeStrikes = 2

// Open implements download.Source.
func (d *kiwixDownload) Open(ctx context.Context, offset int64, validator string) (io.ReadCloser, download.Resp, error) {
	for attempt := 0; ; attempt++ {
		d.mu.Lock()
		direct := d.direct
		d.mu.Unlock()
		body, resp, err := d.open(ctx, direct, offset, validator)
		if err != nil || direct {
			return body, resp, err
		}
		bad := ""
		switch {
		case (resp.Status == http.StatusOK || resp.Status == http.StatusPartialContent) && d.size > 0 && resp.Size >= 0 && resp.Size != d.size:
			bad = "size"
		case offset > 0 && resp.Status == http.StatusOK:
			bad = "range"
		}
		if bad == "" {
			return body, resp, nil
		}
		d.mu.Lock()
		if bad == "size" {
			d.strikes = kiwixRangeStrikes // a mirror with another file is not retried
		} else {
			d.strikes++
		}
		switchNow := d.strikes >= kiwixRangeStrikes
		if switchNow {
			d.direct = true
		}
		d.mu.Unlock()
		if !switchNow || attempt > 0 {
			return body, resp, nil
		}
		if body != nil {
			_ = body.Close()
		}
		d.k.Env.log().Warn("a kiwix mirror misbehaved, fetching from the origin", "why", bad)
	}
}

func (d *kiwixDownload) open(ctx context.Context, direct bool, offset int64, validator string) (io.ReadCloser, download.Resp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return nil, download.Resp{}, &download.Error{Kind: download.KindPermanent, Code: download.CodeRefused, Permanent: true, Err: errors.New("bad URL")}
	}
	req.Header.Set("User-Agent", d.k.web().userAgent())
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	c := d.k.web().client()
	if direct {
		req.Header.Set("Accept", "application/octet-stream")
		c = originNoRedirect(c)
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, download.Resp{}, ctx.Err()
		}
		err = originURLErr(err)
		if errors.Is(err, download.ErrRedirectHTTP) {
			return nil, download.Resp{}, &download.Error{Kind: download.KindPermanent, Code: download.CodeRefused, Permanent: true, Err: err}
		}
		return nil, download.Resp{}, err
	}
	r := download.Resp{
		Status: resp.StatusCode, Size: -1, ETag: resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"), RetryAfter: resp.Header.Get("Retry-After"),
	}
	if direct && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, download.Resp{}, &download.Error{
			Kind: download.KindPermanent, Code: download.CodeRefused, Permanent: true,
			Err: errors.New("the origin redirected the download, which is refused"),
		}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		r.Size = resp.ContentLength
	case http.StatusPartialContent:
		start, _, total, ok := originContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			_ = resp.Body.Close()
			return nil, download.Resp{}, errors.New("a 206 answer without a usable Content-Range")
		}
		r.Offset, r.Size = start, total
	case http.StatusRequestedRangeNotSatisfiable:
		_, _, r.Size, _ = originContentRange(resp.Header.Get("Content-Range"))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, r, nil
	}
	return resp.Body, r, nil
}
