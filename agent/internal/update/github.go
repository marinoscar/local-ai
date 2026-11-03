package update

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Defaults of the GitHub client (agent.md section 4.10 step 1).
const (
	DefaultAPIBase      = "https://api.github.com"
	DefaultDownloadBase = "https://github.com"
	// DefaultRepo is update.repo of node.yaml when it is not set.
	DefaultRepo = "marinoscar/local-ai"
	// ChannelStable and ChannelPrerelease are the values of update.channel.
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"

	// APITimeout bounds each API request.
	APITimeout = 30 * time.Second
	// maxAPIBody bounds an API answer: a list of 30 releases with their assets is well under it.
	maxAPIBody = 8 << 20
	// maxRedirects bounds a redirect chain.
	maxRedirects = 5
)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Source is the GitHub release client: no token, standard TLS verification, HTTPS only, redirects only to
// GitHub's own hosts (agent.md section 4.10, implementation note 3).
type Source struct {
	// Repo is "owner/name" (update.repo).
	Repo string
	// Channel is stable (the default) or prerelease: which releases a request without a version considers.
	Channel string
	// Client is the HTTP client; nil is http.DefaultClient. Its CheckRedirect is replaced by the policy
	// above, and its Timeout is not used (each request has its own context deadline).
	Client *http.Client
	// APIBase and DownloadBase default to GitHub; tests point them at a fake server. They must be https.
	APIBase, DownloadBase string
	// AllowHost says whether a redirect may go to a host; nil allows github.com, api.github.com and
	// *.githubusercontent.com (where release assets are served from).
	AllowHost func(host string) bool
	// UserAgent defaults to "aiagent".
	UserAgent string
}

// ReleaseInfo is a release chosen to install.
type ReleaseInfo struct {
	Version    Version
	Tag        string
	Prerelease bool
	// Assets are the file names GitHub lists for the release.
	Assets []string
}

// Has reports whether the release lists an asset.
func (r ReleaseInfo) Has(name string) bool {
	for _, a := range r.Assets {
		if a == name {
			return true
		}
	}
	return false
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func (s Source) apiBase() string {
	if s.APIBase != "" {
		return strings.TrimRight(s.APIBase, "/")
	}
	return DefaultAPIBase
}

func (s Source) downloadBase() string {
	if s.DownloadBase != "" {
		return strings.TrimRight(s.DownloadBase, "/")
	}
	return DefaultDownloadBase
}

func (s Source) repo() string {
	if s.Repo != "" {
		return s.Repo
	}
	return DefaultRepo
}

func (s Source) userAgent() string {
	if s.UserAgent != "" {
		return s.UserAgent
	}
	return "aiagent"
}

// validate refuses a configuration that could send a request anywhere but a GitHub-like HTTPS endpoint.
func (s Source) validate() error {
	if !repoRE.MatchString(s.repo()) || strings.Contains(s.repo(), "..") {
		return fail(doctor.AGT020, "resolve", "update.repo in node.yaml is not a GitHub repository name such as owner/name.", nil)
	}
	for _, b := range []string{s.apiBase(), s.downloadBase()} {
		u, err := url.Parse(b)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fail(doctor.AGT099, "resolve", "The update server address is not an https address.", nil)
		}
	}
	return nil
}

func (s Source) allowHost(h string) bool {
	if s.AllowHost != nil {
		return s.AllowHost(h)
	}
	return h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

// client returns a copy of the HTTP client with the redirect policy installed: https only, GitHub hosts only,
// at most five hops.
func (s Source) client() *http.Client {
	base := s.Client
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	c.Timeout = 0
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to a non-https address refused")
		}
		if !s.allowHost(req.URL.Hostname()) {
			return fmt.Errorf("redirect to %s refused", req.URL.Hostname())
		}
		return nil
	}
	return &c
}

// assetURL is the download address of a release asset. It is built from the validated repository, the tag
// of a parsed version and a fixed asset name, never from an address in an API answer.
func (s Source) assetURL(tag, name string) string {
	return s.downloadBase() + "/" + s.repo() + "/releases/download/" + tag + "/" + name
}

// Resolve picks the release to install (step 1). want is the version asked for ("" for the newest of the
// channel). An explicit version may be a pre-release whatever the channel says. The version always comes
// from a strict parse of the tag, never from a path or the body of an answer alone.
func (s Source) Resolve(ctx context.Context, want string) (ReleaseInfo, error) {
	if err := s.validate(); err != nil {
		return ReleaseInfo{}, err
	}
	if want != "" {
		v, err := ParseVersion(want)
		if err != nil {
			return ReleaseInfo{}, fail(doctor.AGT020, "resolve", "The version is not valid; use a version such as 1.2.3.", err)
		}
		var rel ghRelease
		code, err := s.getJSON(ctx, s.apiBase()+"/repos/"+s.repo()+"/releases/tags/"+v.Tag(), &rel)
		if err != nil {
			if code == http.StatusNotFound {
				return ReleaseInfo{}, fail(doctor.AGT021, "resolve", "That version has not been released.", err)
			}
			return ReleaseInfo{}, err
		}
		got, ok := releaseOf(rel)
		if !ok || got.Version.Compare(v) != 0 || rel.Draft {
			return ReleaseInfo{}, fail(doctor.AGT021, "resolve", "That version has not been released.", nil)
		}
		return got, nil
	}

	var list []ghRelease
	if _, err := s.getJSON(ctx, s.apiBase()+"/repos/"+s.repo()+"/releases?per_page=30", &list); err != nil {
		return ReleaseInfo{}, err
	}
	var best *ReleaseInfo
	for _, rel := range list {
		if rel.Draft {
			continue
		}
		info, ok := releaseOf(rel)
		if !ok {
			continue
		}
		if s.Channel != ChannelPrerelease && (rel.Prerelease || info.Version.Prerelease()) {
			continue
		}
		if best == nil || info.Version.Compare(best.Version) > 0 {
			cp := info
			best = &cp
		}
	}
	if best == nil {
		return ReleaseInfo{}, fail(doctor.AGT021, "resolve", "No agent release was found.", nil)
	}
	return *best, nil
}

func releaseOf(r ghRelease) (ReleaseInfo, bool) {
	v, err := ParseTag(r.TagName)
	if err != nil {
		return ReleaseInfo{}, false
	}
	info := ReleaseInfo{Version: v, Tag: r.TagName, Prerelease: r.Prerelease || v.Prerelease()}
	for _, a := range r.Assets {
		info.Assets = append(info.Assets, a.Name)
	}
	return info, true
}

// getJSON GETs an API address and decodes the answer. It returns the HTTP status (0 when there was none).
func (s Source) getJSON(ctx context.Context, address string, dst any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, APITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, fail(doctor.AGT099, "resolve", "The update request could not be made.", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", s.userAgent())
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, netError("resolve", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fail(doctor.AGT009, "resolve",
			fmt.Sprintf("GitHub answered %d when asked for the releases.", resp.StatusCode), nil)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody+1))
	if err != nil {
		return resp.StatusCode, netError("resolve", err)
	}
	if len(body) > maxAPIBody {
		return resp.StatusCode, fail(doctor.AGT009, "resolve", "GitHub's answer was larger than allowed.", nil)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return resp.StatusCode, fail(doctor.AGT009, "resolve", "GitHub's answer could not be read.", err)
	}
	return resp.StatusCode, nil
}

// netError classifies a failed request: no connection at all (no internet, DNS, timeout) is LAI-AGT-023, as
// the spec says for a node with no internet; a certificate that does not verify is a failed update
// (LAI-AGT-009) with its own message, never "offline".
func netError(step string, err error) *Error {
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var he x509.HostnameError
	switch {
	case errors.As(err, &ua), errors.As(err, &ci), errors.As(err, &he):
		return fail(doctor.AGT009, step, "The connection to GitHub could not be verified (certificate).", err)
	case errors.Is(err, context.Canceled):
		return fail(doctor.AGT099, step, "The update was cancelled.", err)
	}
	var ue *url.Error
	if errors.As(err, &ue) && strings.Contains(ue.Error(), "redirect") {
		return fail(doctor.AGT009, step, "GitHub redirected the request somewhere that is not allowed.", err)
	}
	return fail(doctor.AGT023, step, "There is no internet connection to GitHub, so the agent cannot be updated.", err)
}
