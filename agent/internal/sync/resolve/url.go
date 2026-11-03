package resolve

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// URLSource is the resolver of ark.yaml `source.type: url`, the generic file source (ark-sync.md
// section 4.9.5): the GeoNames place index, the Ubuntu ISO, a tool bundle. An item is one `url` with
// its `sha256`, or a `files` list of them, resolved the same way. How the expected digest is known is
// the item's `checksum`:
//
//	pinned    (default) the catalog's sha256 per file; sha256_source "catalog". The version is the first
//	          12 hex digits of the first sha256. A HEAD gives the size.
//	sidecar   a SHA256SUMS-style file (`sidecar_url`) fetched over https from the same host as the
//	          file, with no redirect to another host; sha256_source "sidecar", trust sidecar. The
//	          version is the file's name.
//	computed  allowed only with `version_policy: latest` and `trust: tofu`: no digest is known before
//	          the download, the downloader's is recorded (sha256_source "computed_tofu", trust tofu).
//	          The version is the first 12 hex digits of the sha256 of the text built by a HEAD of every
//	          file, `<url>\n<ETag>\n<Last-Modified>\n<Content-Length>\n` per file in catalog order, so
//	          it is deterministic and changes when the origin changes any file; an unchanged version
//	          is "already current".
//
// `extract: true` (one file) makes the engine half unpack the verified archive into a directory item,
// exactly as for github_release.
//
// The planner half (Resolve) exists for the two cases the catalog cannot answer: computed and sidecar.
type URLSource struct {
	W *Web
}

// SourceType implements the registration key.
func (*URLSource) SourceType() string { return "url" }

// Engine returns the run.ItemResolver of the type.
func (u *URLSource) Engine() WebItemResolver { return WebItemResolver{W: u.W, Src: u} }

// The checksum modes of a url item.
const (
	checksumPinned   = "pinned"
	checksumSidecar  = "sidecar"
	checksumComputed = "computed"
)

// urlItem is a url item after its catalog fields were checked.
type urlItem struct {
	id       string
	checksum string
	trust    string
	policy   string
	sidecar  string
	extract  bool
	files    []urlFile
}

type urlFile struct {
	url  string
	name string
	sha  string
}

func readURLItem(it plan.CatalogItem) (urlItem, error) {
	if it.Content == nil || it.Content.Source.Type != "url" {
		return urlItem{}, failPerm(CodeNoNewest, nil, "%s is not a url item", it.ID)
	}
	src := it.Content.Source
	ui := urlItem{
		id: it.ID, checksum: src.Checksum, trust: src.Trust, policy: src.VersionPolicy, sidecar: src.SidecarURL,
		extract: src.Extract != nil && *src.Extract,
	}
	if ui.checksum == "" {
		ui.checksum = checksumPinned
	}
	list := src.Files
	if len(list) == 0 && src.URL != "" {
		list = []catalog.URLFile{{URL: src.URL, SHA256: src.SHA256}}
	}
	if len(list) == 0 {
		return urlItem{}, failPerm(CodeNoNewest, nil, "item %s has no URL", it.ID)
	}
	seen := map[string]bool{}
	for _, f := range list {
		u, err := url.Parse(f.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return urlItem{}, failPerm(CodeNoNewest, nil, "a file of item %s has no usable https URL", it.ID)
		}
		name := fileBase(f.URL)
		if name == "" {
			return urlItem{}, failPerm(CodeNoNewest, nil, "the URL %s of item %s has no usable file name", redactURL(f.URL), it.ID)
		}
		if seen[name] {
			return urlItem{}, failPerm(CodeNoNewest, nil, "item %s lists the file name %s twice", it.ID, name)
		}
		seen[name] = true
		ui.files = append(ui.files, urlFile{url: f.URL, name: name, sha: strings.ToLower(f.SHA256)})
	}
	if ui.extract && len(ui.files) != 1 {
		return urlItem{}, failPerm(CodeNoNewest, nil, "extract: true needs exactly one archive, item %s has %d files", it.ID, len(ui.files))
	}
	switch ui.checksum {
	case checksumPinned:
		for _, f := range ui.files {
			if !manifest.IsSHA256(f.sha) {
				return urlItem{}, failPerm(CodeNoNewest, nil, "file %s of item %s is not pinned (no sha256)", f.name, it.ID)
			}
		}
	case checksumSidecar:
		su, err := url.Parse(ui.sidecar)
		if err != nil || su.Scheme != "https" || su.Host == "" || su.User != nil {
			return urlItem{}, failPerm(CodeNoNewest, nil, "item %s has no usable https sidecar_url", it.ID)
		}
		for _, f := range ui.files {
			if fu, _ := url.Parse(f.url); !strings.EqualFold(fu.Host, su.Host) {
				return urlItem{}, failPerm(CodeNoNewest, nil, "the checksum file of item %s must come from the host of the file", it.ID)
			}
		}
	case checksumComputed:
		if ui.trust != "tofu" || ui.policy != "latest" {
			return urlItem{}, failPerm(CodeNoNewest, nil, "item %s computes its checksum: that needs version_policy: latest and trust: tofu", it.ID)
		}
	default:
		return urlItem{}, failPerm(CodeNoNewest, nil, "item %s has the unknown checksum mode %q", it.ID, Clip([]byte(ui.checksum), 20))
	}
	return ui, nil
}

// head is what a HEAD of one file answered.
type head struct {
	ok                     bool
	etag, modified, length string
	size                   int64 // -1 when unknown
	status                 int
}

// head asks for a file's headers, following redirects (mirrors). A 404 or 410 is the source not having
// the file (permanent); any other non-2xx answer is returned as not ok, for the caller to judge.
func (u *URLSource) head(ctx context.Context, rawURL string) (head, error) {
	w := u.W
	resp, err := w.withRateLimit(ctx, CodeNoNewest, "the server", func() (*http.Response, error) {
		return w.do(ctx, http.MethodHead, rawURL, reqOpts{follow: true})
	})
	if err != nil {
		return head{}, err
	}
	_ = resp.Body.Close()
	h := head{status: resp.StatusCode, size: -1}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return head{}, failPerm(CodeRefused, nil, "the server no longer has %s (HTTP %d)", redactURL(rawURL), resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return h, nil
	}
	h.ok = true
	h.etag = resp.Header.Get("ETag")
	h.modified = resp.Header.Get("Last-Modified")
	h.length = resp.Header.Get("Content-Length")
	if h.length == "" && resp.ContentLength >= 0 {
		h.length = strconv.FormatInt(resp.ContentLength, 10)
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(h.length), 10, 64); err == nil && n >= 0 {
		h.size = n
	}
	return h, nil
}

// Lookup resolves the item for the engine.
func (u *URLSource) Lookup(ctx context.Context, it plan.CatalogItem) (WebResolved, error) {
	ui, err := readURLItem(it)
	if err != nil {
		return WebResolved{}, err
	}
	w := u.W
	if err := w.requireOnline(); err != nil {
		return WebResolved{}, err
	}
	out := WebResolved{
		SourceType: "url", Producer: "http", Extract: ui.extract,
		Meta: map[string]string{"url": redactURL(ui.files[0].url)},
	}
	heads := make([]head, len(ui.files))
	for i, f := range ui.files {
		h, err := u.head(ctx, f.url)
		if err != nil {
			return WebResolved{}, err
		}
		if !h.ok && ui.checksum == checksumComputed {
			return WebResolved{}, fail(CodeNoNewest, nil, "the server answered HTTP %d for %s", h.status, redactURL(f.url))
		}
		heads[i] = h
	}
	var sums map[string]string
	switch ui.checksum {
	case checksumPinned:
		out.Version, out.SortKey, out.Trust = short12(ui.files[0].sha), w.sortKey(), manifest.TrustPinned
	case checksumSidecar:
		if sums, err = u.sidecar(ctx, ui); err != nil {
			return WebResolved{}, err
		}
		out.Version, out.SortKey, out.Trust = ui.files[0].name, w.sortKey(), manifest.TrustSidecar
		out.Meta["sidecar_url"] = redactURL(ui.sidecar)
	default: // computed
		out.Version, out.SortKey, out.Trust = computedVersion(ui, heads), w.syncTime(), manifest.TrustTOFU
	}
	if !manifest.SafeName(out.Version) {
		return WebResolved{}, failPerm(CodeNoNewest, nil, "item %s has no usable version", ui.id)
	}
	for i, f := range ui.files {
		wf := WebFile{Path: f.name, Kind: "file", URL: f.url}
		if s := heads[i].size; s > 0 {
			wf.Size = s
		}
		switch ui.checksum {
		case checksumPinned:
			wf.SHA256, wf.SHA256Source = f.sha, "catalog"
		case checksumSidecar:
			sum, ok := sums[f.name]
			if !ok {
				return WebResolved{}, fail(CodeNoNewest, nil, "the checksum file lists no %s", f.name)
			}
			wf.SHA256, wf.SHA256Source = sum, "sidecar"
		default:
			wf.SHA256Source = "computed_tofu"
		}
		out.Files = append(out.Files, wf)
	}
	return out, nil
}

// computedVersion is the version of a computed item: the first 12 hex digits of the sha256 of the
// per-file `<url>\n<ETag>\n<Last-Modified>\n<Content-Length>\n` text, in catalog order.
func computedVersion(ui urlItem, heads []head) string {
	var b strings.Builder
	for i, f := range ui.files {
		b.WriteString(f.url + "\n" + heads[i].etag + "\n" + heads[i].modified + "\n" + heads[i].length + "\n")
	}
	return sha256Hex([]byte(b.String()))[:12]
}

// sidecar fetches and parses the checksum file.
func (u *URLSource) sidecar(ctx context.Context, ui urlItem) (map[string]string, error) {
	w := u.W
	resp, err := w.withRateLimit(ctx, CodeNoNewest, "the server", func() (*http.Response, error) {
		return w.do(ctx, http.MethodGet, ui.sidecar, reqOpts{follow: true, sameHost: true, accept: "text/plain, */*"})
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fail(CodeNoNewest, nil, "the checksum file %s answered HTTP %d", redactURL(ui.sidecar), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSidecar+1))
	if err != nil {
		return nil, fail(CodeNoNewest, nil, "the checksum file could not be read: %s", cleanNetErr(err))
	}
	if len(body) > maxSidecar {
		return nil, fail(CodeNoNewest, nil, "the checksum file is larger than %d bytes", maxSidecar)
	}
	sums, err := ParseSums(body)
	if err != nil {
		return nil, fail(CodeNoNewest, err, "the checksum file %s is not usable", redactURL(ui.sidecar))
	}
	return sums, nil
}

// ErrBadSums: a SHA256SUMS-style file is malformed.
var ErrBadSums = errors.New("resolve: malformed checksum file")

var sumsLine = regexp.MustCompile(`^([0-9A-Fa-f]{64}) [ *](.+)$`)

// ParseSums reads a SHA256SUMS-style file: `<64 hex><space><space or *><file name>` per line. Blank
// lines and lines starting with # are ignored; anything else is an error, and so is an empty file or
// a name listed twice with different digests. The names are returned as written, without a leading
// "./", with lower-case digests.
func ParseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if n == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := sumsLine.FindStringSubmatch(line)
		if m == nil {
			return nil, errors.Join(ErrBadSums, errors.New("line "+strconv.Itoa(n)+" is not '<sha256>  <name>'"))
		}
		name := strings.TrimPrefix(m[2], "./")
		sum := strings.ToLower(m[1])
		if old, dup := out[name]; dup && old != sum {
			return nil, errors.Join(ErrBadSums, errors.New("a name is listed twice with different digests"))
		}
		out[name] = sum
	}
	if err := sc.Err(); err != nil {
		return nil, errors.Join(ErrBadSums, err)
	}
	if len(out) == 0 {
		return nil, errors.Join(ErrBadSums, errors.New("no checksum lines"))
	}
	return out, nil
}

// Resolve is the planner half: version and size, metadata only. A pinned item is what the catalog
// says and a sidecar item's version is its file name (both from the catalog alone: the dry run makes no
// request for them, and the exact size comes with the engine half's HEAD); only a computed item needs
// the network, for the HEAD rule above.
func (u *URLSource) Resolve(ctx context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	ui, err := readURLItem(it)
	if err != nil {
		return plan.Resolved{}, err
	}
	switch ui.checksum {
	case checksumPinned:
		return plan.Resolved{Version: short12(ui.files[0].sha), SizeBytes: it.CatalogBytes()}, nil
	case checksumSidecar:
		return plan.Resolved{Version: ui.files[0].name, SizeBytes: it.CatalogBytes()}, nil
	}
	w := u.W
	if err := w.requireOnline(); err != nil {
		return plan.Resolved{}, err
	}
	var total int64
	heads := make([]head, len(ui.files))
	for i, f := range ui.files {
		h, err := u.head(ctx, f.url)
		if err != nil {
			return plan.Resolved{}, err
		}
		if !h.ok {
			return plan.Resolved{}, fail(CodeNoNewest, nil, "the server answered HTTP %d for %s", h.status, redactURL(f.url))
		}
		heads[i] = h
		if h.size > 0 {
			total += h.size
		}
	}
	if total == 0 {
		total = it.CatalogBytes()
	}
	return plan.Resolved{Version: computedVersion(ui, heads), SortKey: w.syncTime(), SizeBytes: total}, nil
}
