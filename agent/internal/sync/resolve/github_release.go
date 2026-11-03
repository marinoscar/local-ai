package resolve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// GitHubRelease is the resolver of ark.yaml `source.type: github_release` (ark-sync.md section 4.9.4).
//
// It reads the release record of the pinned tag (`GET /repos/<repo>/releases/tags/<tag>`, no
// credentials: the unauthenticated limit of 60 requests an hour is far above the ten a run needs, and
// one record is cached for ten minutes so two assets of a release cost one request) and finds the
// asset by name. An asset that is not there is LAI-SYN-033. When GitHub publishes the asset's
// `digest` (`sha256:<hex>`) it must equal the catalog's pin, else LAI-SYN-021; the size is the
// asset's own. The download is the asset's `browser_download_url`; `extract: true` makes the engine
// half unpack the verified archive into a directory item.
//
// A 403 or 429 with `X-RateLimit-Remaining: 0` (or Retry-After) is transient: the resolver waits for
// the reset when it comes within 15 minutes and asks once more, else the item fails for this run.
// The version is the tag. There is no planner half: the tag and the size come from the catalog.
type GitHubRelease struct {
	W *Web
}

// SourceType implements the registration key.
func (*GitHubRelease) SourceType() string { return "github_release" }

// Engine returns the run.ItemResolver of the type.
func (g *GitHubRelease) Engine() WebItemResolver { return WebItemResolver{W: g.W, Src: g} }

// ghRelease is the part of the release record the resolver reads.
type ghRelease struct {
	Assets []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	State              string `json:"state"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

var (
	ghRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)
	ghDigest = regexp.MustCompile(`^sha256:([0-9A-Fa-f]{64})$`)
)

// Lookup resolves the release asset.
func (g *GitHubRelease) Lookup(ctx context.Context, it plan.CatalogItem) (WebResolved, error) {
	w := g.W
	if it.Content == nil || it.Content.Source.Type != "github_release" {
		return WebResolved{}, failPerm(CodeNoNewest, nil, "%s is not a GitHub release item", it.ID)
	}
	src := it.Content.Source
	want := strings.ToLower(src.SHA256)
	switch {
	case !ghRepoRE.MatchString(src.Repo):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "item %s names no usable GitHub repository", it.ID)
	case !manifest.SafeName(src.Tag):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "item %s names no usable release tag", it.ID)
	case !manifest.SafeName(src.Asset) || !manifest.ValidFilePath(src.Asset):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "item %s names no usable asset", it.ID)
	case !manifest.IsSHA256(want):
		return WebResolved{}, failPerm(CodeNoNewest, nil, "asset %s of item %s is not pinned (no sha256)", src.Asset, it.ID)
	}
	if err := w.requireOnline(); err != nil {
		return WebResolved{}, err
	}
	rel, err := g.release(ctx, src.Repo, src.Tag)
	if err != nil {
		return WebResolved{}, err
	}
	var asset *ghAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == src.Asset {
			asset = &rel.Assets[i]
			break
		}
	}
	if asset == nil {
		return WebResolved{}, fail(CodeNoNewest, nil, "release %s of %s has no asset %s", src.Tag, src.Repo, src.Asset)
	}
	if asset.State != "" && asset.State != "uploaded" {
		return WebResolved{}, fail(CodeNoNewest, nil, "asset %s of release %s is not uploaded yet", src.Asset, src.Tag)
	}
	source := "catalog"
	if asset.Digest != "" {
		if m := ghDigest.FindStringSubmatch(asset.Digest); m != nil {
			if strings.ToLower(m[1]) != want {
				return WebResolved{}, failPerm(CodeSourceDisagrees, nil, "the sha256 GitHub publishes for %s (%s) is not the catalog's (%s)", src.Asset, short12(strings.ToLower(m[1])), short12(want))
			}
			source = "catalog+github_digest"
		}
		// Another algorithm in the digest field: nothing to cross-check, the pin stands alone.
	}
	dl, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || dl.Scheme != "https" || dl.Host == "" || dl.User != nil {
		return WebResolved{}, fail(CodeNoNewest, nil, "asset %s of release %s has no usable download URL", src.Asset, src.Tag)
	}
	size := asset.Size
	if size < 0 {
		size = 0
	}
	return WebResolved{
		SourceType: "github_release", Version: src.Tag, SortKey: w.sortKey(), Trust: manifest.TrustPinned, Producer: "http",
		Extract: src.Extract != nil && *src.Extract,
		Files: []WebFile{{
			Path: src.Asset, Kind: "file", URL: asset.BrowserDownloadURL, Size: size, SHA256: want, SHA256Source: source,
		}},
		Meta: map[string]string{"repo": src.Repo, "tag": src.Tag, "asset": src.Asset},
	}, nil
}

// release returns the release record of a tag, from the cache when it is fresh.
func (g *GitHubRelease) release(ctx context.Context, repo, tag string) (*ghRelease, error) {
	w := g.W
	key := repo + "@" + tag
	w.mu.Lock()
	if c, ok := w.releases[key]; ok && w.now().Sub(c.at) < releaseTTL {
		w.mu.Unlock()
		return c.rel, nil
	}
	w.mu.Unlock()

	u := w.githubAPI() + "/repos/" + repo + "/releases/tags/" + url.PathEscape(tag)
	resp, err := w.withRateLimit(ctx, CodeNoNewest, "GitHub", func() (*http.Response, error) {
		return w.do(ctx, http.MethodGet, u, reqOpts{follow: true, accept: "application/vnd.github+json"})
	})
	if err != nil {
		return nil, err
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody+1))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, failPerm(CodeRefused, nil, "GitHub has no release %s of %s", tag, repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fail(CodeNoNewest, nil, "GitHub answered HTTP %d for release %s of %s", resp.StatusCode, tag, repo)
	case rerr != nil:
		return nil, fail(CodeNoNewest, nil, "the record of release %s could not be read: %s", tag, cleanNetErr(rerr))
	case len(body) > maxMetaBody:
		return nil, fail(CodeNoNewest, nil, "the record of release %s is larger than %d bytes", tag, maxMetaBody)
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fail(CodeNoNewest, err, "the record of release %s is not the expected JSON", tag)
	}
	w.mu.Lock()
	if w.releases == nil {
		w.releases = map[string]cachedRelease{}
	}
	w.releases[key] = cachedRelease{rel: &rel, at: w.now()}
	w.mu.Unlock()
	return &rel, nil
}
