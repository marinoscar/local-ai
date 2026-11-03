package resolve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/sync/download"
)

// originMetaTimeout bounds one metadata request (a listing, a sidecar, a HEAD).
const originMetaTimeout = 2 * time.Minute

// readOnlyHTTP is the metadata client of the kiwix and pmtiles resolvers: GET and HEAD only, https only
// (a redirect to http is refused), at most 10 redirects, TLS 1.2 or later with the system roots, no
// proxy (the agent unit sets none, and HTTPS_PROXY is ignored here), no transparent decompression, and
// a User-Agent that names the agent. The sync downloader has its own client; this one never carries a
// token and never downloads a payload.
type readOnlyHTTP struct {
	// Client replaces the default client (tests, an air-gapped CA). nil builds the default.
	Client *http.Client
	// Version is the agent version in the User-Agent; default "dev".
	Version string

	once sync.Once
	dflt *http.Client
}

func (h *readOnlyHTTP) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	h.once.Do(func() {
		h.dflt = &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: download.DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout:   download.TLSHandshakeTimeout,
				ResponseHeaderTimeout: download.ResponseHeaderTimeout,
				ForceAttemptHTTP2:     true,
				DisableCompression:    true,
				MaxIdleConns:          4,
				IdleConnTimeout:       90 * time.Second,
			},
			CheckRedirect: originCheckRedirect,
		}
	})
	return h.dflt
}

// originCheckRedirect follows at most download.MaxRedirects and never to a URL that is not https.
func originCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= download.MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", download.MaxRedirects)
	}
	if req.URL.Scheme != "https" {
		return download.ErrRedirectHTTP
	}
	return nil
}

// originNoRedirect is c with redirects switched off: the 3xx answer itself is returned.
func originNoRedirect(c *http.Client) *http.Client {
	cc := *c
	cc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cc
}

func (h *readOnlyHTTP) userAgent() string {
	v := h.Version
	if v == "" {
		v = "dev"
	}
	return download.UserAgent(v)
}

// originResponse is a metadata answer: the status, the headers and (for a GET) the bounded body.
type originResponse struct {
	Status int
	Header http.Header
	Body   []byte
	// Truncated: the body was longer than the cap.
	Truncated bool
}

// do makes one request with a timeout. follow says whether redirects are followed (https only). The
// body of a GET is read up to maxBody bytes. The error is free of the request URL's query.
func (h *readOnlyHTTP) do(ctx context.Context, method, rawURL string, follow bool, maxBody int64, hdr map[string]string) (originResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, originMetaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return originResponse{}, errors.New("bad URL")
	}
	req.Header.Set("User-Agent", h.userAgent())
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := h.client()
	if !follow {
		c = originNoRedirect(c)
	}
	resp, err := c.Do(req)
	if err != nil {
		return originResponse{}, originURLErr(err)
	}
	defer resp.Body.Close()
	out := originResponse{Status: resp.StatusCode, Header: resp.Header}
	if method != http.MethodHead && maxBody > 0 {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		if rerr != nil {
			return originResponse{}, originURLErr(rerr)
		}
		if int64(len(b)) > maxBody {
			b, out.Truncated = b[:maxBody], true
		}
		out.Body = b
	}
	return out, nil
}

// originURLErr drops the URL a *url.Error repeats (it may carry a query) and keeps the cause.
func originURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", download.RedactURL(ue.URL), ue.Err)
	}
	return err
}

// originContentRange reads "bytes <start>-<end>/<total>" (total -1 for "*"); ok is false for anything
// else.
func originContentRange(v string) (start, end, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, -1, false
	}
	rng, tot, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, -1, false
	}
	total = -1
	if tot != "*" {
		t, err := strconv.ParseInt(tot, 10, 64)
		if err != nil || t < 0 {
			return 0, 0, -1, false
		}
		total = t
	}
	if rng == "*" {
		return 0, 0, total, true
	}
	a, b, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, -1, false
	}
	s, err1 := strconv.ParseInt(a, 10, 64)
	e, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || s < 0 || e < s {
		return 0, 0, -1, false
	}
	return s, e, total, true
}
