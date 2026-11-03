package resolve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// HuggingFace is the resolver of models.yaml `source.type: huggingface` (ark-sync.md section 4.9.1).
//
// For every file of the model it asks Hugging Face what it publishes for the pinned revision, without
// following the redirect to the CDN, and compares that with the catalog:
//
//	X-Repo-Commit  must equal the pinned revision
//	X-Linked-Etag  the LFS oid, must equal the catalog sha256
//	X-Linked-Size  must equal the catalog size_bytes
//
// Any disagreement is LAI-SYN-021 and nothing is downloaded. A small file that is not stored in LFS
// answers 200 with the git blob id as its ETag: its expected sha256 is the catalog's only. When the
// headers are missing (a proxy, an API change) the tree listing of the revision is read instead
// (`/api/models/<repo>/tree/<revision>?recursive=1`, `lfs.oid` and `lfs.size`).
//
// The version is the first 12 characters of the revision. The Hugging Face token (Web.HFToken) goes to
// the Hugging Face host only. There is no planner half: the catalog pins version and size exactly
// (plan.FromCatalog), so the dry run makes no request.
type HuggingFace struct {
	W *Web
}

// SourceType implements the registration key.
func (*HuggingFace) SourceType() string { return "huggingface" }

// Engine returns the run.ItemResolver of the type.
func (h *HuggingFace) Engine() WebItemResolver { return WebItemResolver{W: h.W, Src: h} }

var (
	hfRepoRE     = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*/)?[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hfRevisionRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Lookup resolves the model.
func (h *HuggingFace) Lookup(ctx context.Context, it plan.CatalogItem) (WebResolved, error) {
	w := h.W
	m := it.Model
	if m == nil || m.Source.Type != "huggingface" {
		return WebResolved{}, failPerm(CodeNoNewest, nil, "%s is not a Hugging Face model", it.ID)
	}
	repo, rev := m.Source.Repo, strings.ToLower(m.Source.Revision)
	switch {
	case !hfRepoRE.MatchString(repo):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "model %s names no usable Hugging Face repository", m.ID)
	case !hfRevisionRE.MatchString(rev):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "model %s is not pinned to a 40-digit revision", m.ID)
	case len(m.Files) == 0:
		return WebResolved{}, failPerm(CodeNoNewest, nil, "model %s lists no files", m.ID)
	}
	if err := w.requireOnline(); err != nil {
		return WebResolved{}, err
	}
	out := WebResolved{
		SourceType: "huggingface", Version: rev[:12], SortKey: w.sortKey(), Trust: manifest.TrustPinned, Producer: "http",
		Meta: map[string]string{"repo": repo, "revision": rev},
	}
	tree := &hfTree{h: h, repo: repo, rev: rev}
	for _, f := range m.Files {
		wf, err := h.file(ctx, m.ID, repo, rev, f.Name, f.Kind, f.URL, strings.ToLower(f.SHA256), f.SizeBytes, tree)
		if err != nil {
			return WebResolved{}, err
		}
		out.Files = append(out.Files, wf)
	}
	return out, nil
}

// file resolves one file of the model.
func (h *HuggingFace) file(ctx context.Context, model, repo, rev, name, kind, own, want string, wantSize int64, tree *hfTree) (WebFile, error) {
	w := h.W
	if !manifest.ValidFilePath(name) {
		return WebFile{}, failPerm(CodeNoNewest, nil, "model %s lists the unusable file name %q", model, Clip([]byte(name), 60))
	}
	if !manifest.IsSHA256(want) {
		return WebFile{}, failPerm(CodeNoNewest, nil, "file %s of model %s is not pinned (no sha256)", name, model)
	}
	target := own
	if target == "" {
		target = w.hfBase() + "/" + repo + "/resolve/" + rev + "/" + escapePath(name)
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return WebFile{}, failPerm(CodeNoNewest, nil, "file %s of model %s has no usable URL", name, model)
	}
	wf := WebFile{Path: name, Kind: kind, URL: target, Size: wantSize, SHA256: want, SHA256Source: "catalog"}
	if !strings.EqualFold(u.Host, w.hfHost()) {
		return wf, nil // a file the catalog takes from elsewhere: only its own pin applies
	}
	wf.Auth = true

	resp, err := w.withRateLimit(ctx, CodeHFLogin, "Hugging Face", func() (*http.Response, error) {
		return w.do(ctx, http.MethodHead, target, reqOpts{auth: true})
	})
	if err != nil {
		return WebFile{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return WebFile{}, fail(CodeHFLogin, nil, "Hugging Face wants a login for %s (HTTP %d)", name, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return WebFile{}, failPerm(CodeRefused, nil, "Hugging Face does not have %s at revision %s", name, rev[:12])
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return WebFile{}, fail(CodeNoNewest, nil, "Hugging Face answered HTTP %d for %s", resp.StatusCode, name)
	case resp.StatusCode != http.StatusOK && !(resp.StatusCode >= 300 && resp.StatusCode < 400):
		return WebFile{}, fail(CodeNoNewest, nil, "Hugging Face answered HTTP %d for %s", resp.StatusCode, name)
	}

	if commit := strings.ToLower(strings.TrimSpace(resp.Header.Get("X-Repo-Commit"))); commit != "" && commit != rev {
		return WebFile{}, failPerm(CodeSourceDisagrees, nil, "Hugging Face serves commit %s for %s, the catalog pins %s", short12(commit), name, rev[:12])
	}
	oid := unquoteETag(resp.Header.Get("X-Linked-Etag"))
	size := int64(-1)
	if v := strings.TrimSpace(resp.Header.Get("X-Linked-Size")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			size = n
		}
	}
	if manifest.IsSHA256(oid) {
		return h.verdict(wf, name, oid, size)
	}
	if resp.StatusCode == http.StatusOK {
		// Not stored in LFS: the expected sha256 is the catalog's only; the size is the body's.
		if resp.ContentLength >= 0 {
			if wf.Size > 0 && resp.ContentLength != wf.Size {
				return WebFile{}, failPerm(CodeSourceDisagrees, nil, "%s is %d bytes at Hugging Face, the catalog says %d", name, resp.ContentLength, wf.Size)
			}
			if wf.Size == 0 {
				wf.Size = resp.ContentLength
			}
		}
		return wf, nil
	}
	// A redirect with no usable LFS headers: ask for the listing of the revision.
	ent, err := tree.find(ctx, name)
	if err != nil {
		return WebFile{}, err
	}
	if ent.LFS != nil && manifest.IsSHA256(strings.ToLower(ent.LFS.OID)) {
		return h.verdict(wf, name, strings.ToLower(ent.LFS.OID), ent.LFS.Size)
	}
	if wf.Size > 0 && ent.Size != wf.Size {
		return WebFile{}, failPerm(CodeSourceDisagrees, nil, "%s is %d bytes at Hugging Face, the catalog says %d", name, ent.Size, wf.Size)
	}
	if wf.Size == 0 {
		wf.Size = ent.Size
	}
	return wf, nil
}

// verdict compares what Hugging Face publishes for an LFS file with the catalog's pin.
func (h *HuggingFace) verdict(wf WebFile, name, oid string, size int64) (WebFile, error) {
	if oid != wf.SHA256 {
		return WebFile{}, failPerm(CodeSourceDisagrees, nil, "the sha256 Hugging Face publishes for %s (%s) is not the catalog's (%s)", name, short12(oid), short12(wf.SHA256))
	}
	if size >= 0 {
		if wf.Size > 0 && size != wf.Size {
			return WebFile{}, failPerm(CodeSourceDisagrees, nil, "%s is %d bytes at Hugging Face, the catalog says %d", name, size, wf.Size)
		}
		if wf.Size == 0 {
			wf.Size = size
		}
	}
	wf.SHA256Source = "catalog+hf_lfs"
	return wf, nil
}

// hfTreeEntry is one entry of the tree API.
type hfTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// hfTree reads the listing of one revision once, however many files need it.
type hfTree struct {
	h       *HuggingFace
	repo    string
	rev     string
	loaded  bool
	entries map[string]hfTreeEntry
}

const maxTreePages = 50

func (t *hfTree) find(ctx context.Context, name string) (hfTreeEntry, error) {
	if !t.loaded {
		if err := t.load(ctx); err != nil {
			return hfTreeEntry{}, err
		}
		t.loaded = true
	}
	e, ok := t.entries[name]
	if !ok {
		return hfTreeEntry{}, failPerm(CodeRefused, nil, "the listing of revision %s at Hugging Face has no file %s", t.rev[:12], name)
	}
	return e, nil
}

func (t *hfTree) load(ctx context.Context) error {
	w := t.h.W
	t.entries = map[string]hfTreeEntry{}
	next := w.hfBase() + "/api/models/" + t.repo + "/tree/" + t.rev + "?recursive=1"
	for page := 0; next != ""; page++ {
		if page >= maxTreePages {
			return fail(CodeNoNewest, nil, "the listing of %s has more than %d pages", t.repo, maxTreePages)
		}
		resp, err := w.withRateLimit(ctx, CodeHFLogin, "Hugging Face", func() (*http.Response, error) {
			// Same-host redirects only: a repository that moved stays on the site, and the token never
			// follows a redirect to another host.
			return w.do(ctx, http.MethodGet, next, reqOpts{follow: true, auth: true, accept: "application/json"})
		})
		if err != nil {
			return err
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody+1))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return fail(CodeHFLogin, nil, "Hugging Face wants a login to list %s (HTTP %d)", t.repo, resp.StatusCode)
		case resp.StatusCode == http.StatusNotFound:
			return failPerm(CodeRefused, nil, "Hugging Face has no listing of %s at revision %s", t.repo, t.rev[:12])
		case resp.StatusCode != http.StatusOK:
			return fail(CodeNoNewest, nil, "Hugging Face answered HTTP %d to the listing of %s", resp.StatusCode, t.repo)
		case rerr != nil:
			return fail(CodeNoNewest, nil, "the listing of %s could not be read: %s", t.repo, cleanNetErr(rerr))
		case len(body) > maxMetaBody:
			return fail(CodeNoNewest, nil, "the listing of %s is larger than %d bytes", t.repo, maxMetaBody)
		}
		var list []hfTreeEntry
		if err := json.Unmarshal(body, &list); err != nil {
			return fail(CodeNoNewest, err, "the listing of %s is not the expected JSON", t.repo)
		}
		for _, e := range list {
			if e.Type == "file" {
				t.entries[e.Path] = e
			}
		}
		next = nextLink(resp.Header.Get("Link"), w.hfBase())
	}
	return nil
}

// nextLink reads the rel="next" URL of a Link header; "" when there is none or it points away from
// the site (the listing is never followed to another host).
func nextLink(h, base string) string {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end <= start {
			return ""
		}
		next := part[start+1 : end]
		bu, err1 := url.Parse(base)
		nu, err2 := url.Parse(next)
		if err1 != nil || err2 != nil || !strings.EqualFold(bu.Host, nu.Host) {
			return ""
		}
		return next
	}
	return ""
}

// unquoteETag reads an ETag header value as a lower-case string: no quotes, no weak marker.
func unquoteETag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "W/")
	v = strings.Trim(v, `"`)
	return strings.ToLower(v)
}

// escapePath escapes each segment of a slash path and keeps the slashes.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
