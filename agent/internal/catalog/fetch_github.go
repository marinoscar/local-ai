package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The GitHub source of the catalog fetcher (catalog.md section 1, priority 2: not enrolled, online).
//
// A node that is not enrolled reads the catalog straight from the repository at a release tag:
//
//  1. node.yaml catalog.channel names the tag: "stable" or "catalog-<CATALOG_VERSION>" (a pin).
//  2. "stable" is resolved by listing GET <api>/repos/<owner>/<repo>/git/matching-refs/tags/catalog-
//     (unauthenticated) and taking the highest catalog-<YYYY.MM.DD-N>, fields compared as numbers;
//     catalog-stable itself is ignored. If the listing fails for any reason (rate limit, outage,
//     timeout, damaged answer) the moving tag catalog-stable is used instead.
//  3. Every file is read from <catalog.base_url>/<tag>/catalog/<file> and all of them arrive before
//     anything is handed on, so a catalog is never a partial mix. Fetcher.Refresh then stages the
//     bundle and runs Install (lint, atomic swap).
//
// The network is the only thing this file touches, through an *http.Client that tests replace with
// the client of an httptest server. HTTPS only, no proxy from the environment, no token, no cookie.

// Defaults of the GitHub source (node.yaml catalog.base_url defaults to DefaultRawBase).
const (
	// DefaultRawBase is the raw.githubusercontent.com base of the repository.
	DefaultRawBase = "https://raw.githubusercontent.com/marinoscar/local-ai"
	// DefaultAPIBase is the GitHub REST API, used only to list tags and profile files.
	DefaultAPIBase = "https://api.github.com"
	// TagPrefix starts every catalog release tag: catalog-<CATALOG_VERSION>.
	TagPrefix = "catalog-"
	// StableTag is the moving tag that always points at the newest catalog tag (the fallback).
	StableTag = "catalog-stable"
	// ChannelStable is the default node.yaml catalog.channel.
	ChannelStable = "stable"

	// GitHubAPITimeout bounds the tag listing; GitHubFileTimeout bounds each raw file.
	GitHubAPITimeout  = 10 * time.Second
	GitHubFileTimeout = 30 * time.Second
)

const (
	rawHost      = "raw.githubusercontent.com"
	maxListBytes = 1 << 20 // a tag or directory listing page
	maxListPages = 10      // 100 refs a page; more than this and the listing is not trusted
)

// DefaultProfileIDs are the profile files of catalog.md section 1, fetched when the repository
// cannot be asked which profiles a tag holds (the API is down or rate limited).
var DefaultProfileIDs = []string{"small-8gb", "cuda-12gb", "max-128gb", "cpu-only", "ark-drive-full"}

// Reasons carried by a *FetchError (Reason field) and logged by the agent; never shown to a
// non-expert, who gets the doctor code and its fix.
const (
	ReasonBadChannel      = "bad_channel"      // node.yaml catalog.channel is neither stable nor catalog-<version>
	ReasonNotHTTPS        = "not_https"        // catalog.base_url or a redirect is not https
	ReasonNotFound        = "not_found"        // HTTP 404
	ReasonRateLimited     = "rate_limited"     // HTTP 403 with no quota left, or 429
	ReasonHTTPStatus      = "http_status"      // any other non-200 answer
	ReasonUnreachable     = "unreachable"      // DNS, connect, TLS or timeout
	ReasonTruncated       = "truncated"        // the body ended before its declared length
	ReasonTooLarge        = "too_large"        // a file or the bundle exceeds MaxBundleBytes
	ReasonVersionMismatch = "version_mismatch" // CATALOG_VERSION differs from the tag suffix
	ReasonBadListing      = "bad_listing"      // the tag or profile listing could not be understood
)

// FetchError is a failed GitHub fetch. Its doctor code is LAI-CAT-006 (could not download),
// LAI-CAT-007 (the channel names a catalog that does not exist) or LAI-CAT-008 (what arrived was
// incomplete or inconsistent, nothing was installed). The previous catalog is always kept.
type FetchError struct {
	code   string
	Reason string
	// URL is the address that failed ("" when none applies). It never carries a credential.
	URL string
	Err error
}

func (e *FetchError) Error() string {
	msg := e.code + ": GitHub catalog fetch failed (" + e.Reason + ")"
	if e.URL != "" {
		msg += " " + e.URL
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Code is the doctor code of the failure.
func (e *FetchError) Code() string { return e.code }

// Unwrap exposes the transport error.
func (e *FetchError) Unwrap() error { return e.Err }

func fetchErr(code, reason, u string, err error) *FetchError {
	return &FetchError{code: code, Reason: reason, URL: u, Err: err}
}

// GitHub is the GitHub source of the Fetcher. Build it with NewGitHub; the exported fields exist
// for tests and for a fork or mirror.
type GitHub struct {
	// RawBase is node.yaml catalog.base_url: https://raw.githubusercontent.com/<owner>/<repo>.
	RawBase string
	// APIBase is the REST API root used to list tags and profile files. Empty means "do not ask the
	// API": stable then resolves to the moving tag and profiles to DefaultProfileIDs. NewGitHub sets
	// it only for raw.githubusercontent.com, so a mirror never talks to api.github.com by accident.
	APIBase string
	// Client performs the requests (default: no proxy, redirects limited to https).
	Client *http.Client
	// APITimeout and FileTimeout default to GitHubAPITimeout and GitHubFileTimeout.
	APITimeout  time.Duration
	FileTimeout time.Duration
}

var _ GitHubSource = (*GitHub)(nil)

// NewGitHub returns the source for node.yaml catalog.base_url ("" = DefaultRawBase).
func NewGitHub(rawBase string) *GitHub {
	rawBase = strings.TrimRight(strings.TrimSpace(rawBase), "/")
	if rawBase == "" {
		rawBase = DefaultRawBase
	}
	g := &GitHub{RawBase: rawBase}
	if u, err := url.Parse(rawBase); err == nil && strings.EqualFold(u.Host, rawHost) {
		g.APIBase = DefaultAPIBase
	}
	return g
}

// Resolution says which tag a channel resolved to and how.
type Resolution struct {
	Tag string
	// Via is "pin", "matching-refs" or "stable-tag".
	Via string
	// APIErr is why the tag listing was not used (nil when it was, or was not needed).
	APIErr error
}

// Resolve maps node.yaml catalog.channel to a tag. It never fails for "stable": a listing that
// cannot be used falls back to the moving tag, with the reason in Resolution.APIErr.
func (g *GitHub) Resolve(ctx context.Context, channel string) (Resolution, error) {
	channel = strings.TrimSpace(channel)
	switch {
	case channel == "" || channel == ChannelStable:
		tag, err := g.highestTag(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return Resolution{}, ctx.Err()
			}
			return Resolution{Tag: StableTag, Via: "stable-tag", APIErr: err}, nil
		}
		return Resolution{Tag: tag, Via: "matching-refs"}, nil
	case strings.HasPrefix(channel, TagPrefix) && ValidVersion(strings.TrimPrefix(channel, TagPrefix)):
		return Resolution{Tag: channel, Via: "pin"}, nil
	}
	return Resolution{}, fetchErr(CodeGitHubNotFound, ReasonBadChannel, "",
		fmt.Errorf("channel %q is neither %q nor %s<YYYY.MM.DD-N>", truncate(channel, 60), ChannelStable, TagPrefix))
}

// FetchCatalog implements GitHubSource: resolve the channel, then read every file of the tag.
func (g *GitHub) FetchCatalog(ctx context.Context, channel string) (*Bundle, error) {
	res, err := g.Resolve(ctx, channel)
	if err != nil {
		return nil, err
	}
	return g.fetchTag(ctx, res)
}

// highestTag lists catalog-* tags and returns the one with the highest version.
func (g *GitHub) highestTag(ctx context.Context) (string, error) {
	repo, err := g.repoPath()
	if err != nil {
		return "", err
	}
	if g.APIBase == "" {
		return "", errors.New("no API configured")
	}
	next := strings.TrimRight(g.APIBase, "/") + "/repos/" + repo + "/git/matching-refs/tags/" + TagPrefix + "?per_page=100"
	best := ""
	for page := 0; next != ""; page++ {
		if page >= maxListPages {
			return "", errors.New("too many tags to list")
		}
		body, link, err := g.get(ctx, next, g.apiTimeout(), maxListBytes, true)
		if err != nil {
			return "", err
		}
		var refs []struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(body, &refs); err != nil {
			return "", fetchErr(CodeGitHubFetch, ReasonBadListing, next, errors.New("answer is not a list of refs"))
		}
		for _, r := range refs {
			tag := strings.TrimPrefix(r.Ref, "refs/tags/")
			if tag == r.Ref || tag == StableTag || !strings.HasPrefix(tag, TagPrefix) {
				continue
			}
			v := strings.TrimPrefix(tag, TagPrefix)
			if !ValidVersion(v) {
				continue // not a catalog release (a stray or malformed tag)
			}
			if best == "" || CompareVersions(v, strings.TrimPrefix(best, TagPrefix)) > 0 {
				best = tag
			}
		}
		next, err = g.nextPage(link)
		if err != nil {
			return "", err
		}
	}
	if best == "" {
		return "", errors.New("no catalog-<version> tag exists")
	}
	return best, nil
}

// nextPage returns the rel="next" URL of a Link header, refusing another host or a non-https URL.
func (g *GitHub) nextPage(link string) (string, error) {
	if link == "" {
		return "", nil
	}
	for _, part := range strings.Split(link, ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 || !strings.Contains(strings.ReplaceAll(strings.Join(seg[1:], ";"), " ", ""), `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(seg[0]), "<>")
		u, err := url.Parse(raw)
		base, berr := url.Parse(g.APIBase)
		if err != nil || berr != nil || u.Scheme != "https" || u.Host != base.Host {
			return "", fetchErr(CodeGitHubFetch, ReasonBadListing, "", errors.New("next page is not on the API host"))
		}
		return u.String(), nil
	}
	return "", nil
}

// profileIDs asks the API which profile files a tag holds, else returns DefaultProfileIDs.
func (g *GitHub) profileIDs(ctx context.Context, tag string) []string {
	repo, err := g.repoPath()
	if err != nil || g.APIBase == "" {
		return DefaultProfileIDs
	}
	u := strings.TrimRight(g.APIBase, "/") + "/repos/" + repo + "/contents/catalog/profiles?ref=" + url.QueryEscape(tag)
	body, _, err := g.get(ctx, u, g.apiTimeout(), maxListBytes, true)
	if err != nil {
		return DefaultProfileIDs
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &entries) != nil {
		return DefaultProfileIDs
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name, ".yaml"); ok && e.Type == "file" && profileIDRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return DefaultProfileIDs
	}
	sort.Strings(ids)
	return ids
}

var profileIDRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// fetchTag reads CATALOG_VERSION, models.yaml, ark.yaml and every profile of one tag.
func (g *GitHub) fetchTag(ctx context.Context, res Resolution) (*Bundle, error) {
	tag := res.Tag
	b := &Bundle{}
	total := 0
	read := func(name string) (string, error) {
		body, _, err := g.get(ctx, g.rawURL(tag, name), g.fileTimeout(), int64(MaxBundleBytes-total), false)
		if err != nil {
			return "", err
		}
		total += len(body)
		sum := sha256.Sum256(body)
		b.Files = append(b.Files, BundleFile{Path: name, SHA256: hex.EncodeToString(sum[:]), Content: string(body)})
		return string(body), nil
	}

	vcontent, err := read(FileVersion)
	if err != nil {
		var fe *FetchError
		if errors.As(err, &fe) && fe.Reason == ReasonNotFound {
			return nil, fetchErr(CodeGitHubNotFound, ReasonNotFound, fe.URL,
				fmt.Errorf("tag %s has no catalog/%s", tag, FileVersion))
		}
		return nil, err
	}
	b.Version = strings.TrimRight(vcontent, "\r\n")
	if suffix := strings.TrimPrefix(tag, TagPrefix); tag != StableTag && b.Version != suffix {
		return nil, fetchErr(CodeGitHubIncomplete, ReasonVersionMismatch, g.rawURL(tag, FileVersion),
			fmt.Errorf("tag %s holds CATALOG_VERSION %q", tag, truncate(b.Version, 40)))
	}

	names := []string{"models.yaml", "ark.yaml"}
	for _, id := range g.profileIDs(ctx, tag) {
		names = append(names, "profiles/"+id+".yaml")
	}
	for _, n := range names {
		if _, err := read(n); err != nil {
			var fe *FetchError
			if errors.As(err, &fe) && fe.Reason == ReasonNotFound {
				// the tag exists but is not a whole catalog: nothing is installed
				return nil, fetchErr(CodeGitHubIncomplete, ReasonNotFound, fe.URL, fmt.Errorf("tag %s has no catalog/%s", tag, n))
			}
			return nil, err
		}
	}
	b.BundleSHA256 = BundleDigest(b.Files)
	return b, nil
}

// BundleDigest is the bundleSha256 of cloud-app.md section 3.6: sha256 over the lines
// "<path>\0<sha256>\n" sorted by path. A catalog imported by the cloud from the same tag has the
// same digest.
func BundleDigest(files []BundleFile) string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		lines = append(lines, f.Path+"\x00"+strings.ToLower(f.SHA256)+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		_, _ = io.WriteString(h, l)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (g *GitHub) rawURL(tag, name string) string {
	return strings.TrimRight(g.RawBase, "/") + "/" + url.PathEscape(tag) + "/catalog/" + name
}

// repoPath is "<owner>/<repo>" from the path of RawBase.
func (g *GitHub) repoPath() (string, error) {
	u, err := url.Parse(g.RawBase)
	if err != nil {
		return "", fetchErr(CodeGitHubFetch, ReasonNotHTTPS, "", errors.New("catalog.base_url is not a URL"))
	}
	p := strings.Trim(u.Path, "/")
	if strings.Count(p, "/") != 1 {
		return "", fetchErr(CodeGitHubFetch, ReasonBadListing, "", errors.New("catalog.base_url must end in <owner>/<repo>"))
	}
	return p, nil
}

func (g *GitHub) apiTimeout() time.Duration {
	if g.APITimeout > 0 {
		return g.APITimeout
	}
	return GitHubAPITimeout
}

func (g *GitHub) fileTimeout() time.Duration {
	if g.FileTimeout > 0 {
		return g.FileTimeout
	}
	return GitHubFileTimeout
}

// client returns a copy of the configured client that refuses any redirect to a non-https URL.
func (g *GitHub) client() *http.Client {
	var c http.Client
	if g.Client != nil {
		c = *g.Client
	} else {
		c.Transport = &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to a non-https URL refused")
		}
		return nil
	}
	return &c
}

// get performs one GET and returns the body (at most limit bytes) and the Link header. A status
// other than 200, a transport failure, a body longer than limit and a body shorter than its
// declared length are all *FetchError. api marks a request to the REST API.
func (g *GitHub) get(ctx context.Context, rawURL string, timeout time.Duration, limit int64, api bool) ([]byte, string, error) {
	if u, err := url.Parse(rawURL); err != nil || u.Scheme != "https" {
		return nil, "", fetchErr(CodeGitHubFetch, ReasonNotHTTPS, "", errors.New("only https URLs are used; check catalog.base_url"))
	}
	if limit < 0 {
		limit = 0
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fetchErr(CodeGitHubFetch, ReasonNotHTTPS, "", err)
	}
	req.Header.Set("User-Agent", userAgentGitHub)
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := g.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fetchErr(CodeGitHubFetch, ReasonUnreachable, rawURL, urlErr(err))
	}
	defer resp.Body.Close()
	if ctx.Err() != nil { // the caller gave up while the answer was on its way: not GitHub's failure
		return nil, "", ctx.Err()
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", fetchErr(CodeGitHubFetch, ReasonNotFound, rawURL, errors.New("HTTP 404"))
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0",
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != "":
		return nil, "", fetchErr(CodeGitHubFetch, ReasonRateLimited, rawURL, fmt.Errorf("HTTP %d", resp.StatusCode))
	default:
		return nil, "", fetchErr(CodeGitHubFetch, ReasonHTTPStatus, rawURL, fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength > limit {
		return nil, "", fetchErr(CodeGitHubIncomplete, ReasonTooLarge, rawURL, fmt.Errorf("%d bytes is over the %d byte limit", resp.ContentLength, limit))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, "", ctx.Err()
	case err != nil && errors.Is(err, io.ErrUnexpectedEOF):
		return nil, "", fetchErr(CodeGitHubIncomplete, ReasonTruncated, rawURL, err)
	case err != nil:
		return nil, "", fetchErr(CodeGitHubFetch, ReasonUnreachable, rawURL, urlErr(err))
	case int64(len(body)) > limit:
		return nil, "", fetchErr(CodeGitHubIncomplete, ReasonTooLarge, rawURL, fmt.Errorf("over the %d byte limit", limit))
	case resp.ContentLength >= 0 && int64(len(body)) != resp.ContentLength:
		return nil, "", fetchErr(CodeGitHubIncomplete, ReasonTruncated, rawURL, fmt.Errorf("got %d of %d bytes", len(body), resp.ContentLength))
	}
	return body, resp.Header.Get("Link"), nil
}

const userAgentGitHub = "aiagent-catalog"
