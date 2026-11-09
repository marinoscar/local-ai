// Package download is the resumable downloader of ark sync (ark-sync.md sections 4.6 to 4.8). It
// is a library: the sync engine (SYNC-07) and the resolvers (SYNC-09 to 11) call Fetch, which
// downloads one file into <root>/.staging/<item>/<version>/<file>.part with HTTP Range resume,
// per-64-MiB checkpoints, a streaming sha256, retries with capped exponential backoff, a free-space
// guard and the shared bandwidth bucket, and returns a verified .part. Everything it touches is
// injected: the HTTP client, the clock, the filesystem, the free-space probe and the bucket.
//
// Rules this package keeps (AGENTS.md, ark-sync.md section 6):
//
//   - it never deletes anything: its FS has no Remove. It truncates its own .part and renames it
//     after verification, and never touches a verified copy;
//   - a file is only renamed into place (Commit) after its size and sha256 matched;
//   - TLS verification is always on, a redirect to http is refused, and the Hugging Face token goes
//     only to the auth host and is dropped on any redirect to another host;
//   - no secret reaches a log line or an error: URLs are reduced to scheme, host and path, hashes
//     are logged as first4...last2, and headers are never logged or stored.
package download

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// The HTTP client rules of ark-sync.md section 4.6.
const (
	DialTimeout           = 15 * time.Second
	TLSHandshakeTimeout   = 15 * time.Second
	ResponseHeaderTimeout = 30 * time.Second
	// StallTimeout: no body bytes for this long aborts the request and retries.
	StallTimeout = 60 * time.Second
	MaxRedirects = 10
	// AuthHost is the one host that gets the Authorization header (the Hugging Face token).
	AuthHost = "huggingface.co"
	// ProjectURL is in the User-Agent.
	ProjectURL = "https://github.com/marinoscar/local-ai"
)

// UserAgent is the User-Agent of every request: localai-agent/<version> (+<project url>).
func UserAgent(version string) string {
	return "localai-agent/" + version + " (+" + ProjectURL + ")"
}

// Resp is what a Source says about the response to Open. A Source reports HTTP statuses (a local
// source translates its situation into the same ones).
type Resp struct {
	// Status is the HTTP status: 200, 206, 416, 401, 403, 404, 429, 5xx, ...
	Status int
	// Offset is the first byte the body delivers: the start of Content-Range for a 206, else 0.
	Offset int64
	// Size is the total size of the file when the source knows it (from Content-Range or
	// Content-Length of a 200), else -1.
	Size int64
	// ETag and LastModified are the validators the next resume sends in If-Range.
	ETag, LastModified string
	// RetryAfter is the raw Retry-After header, "" when absent.
	RetryAfter string
	// HuggingFace: the source is the authenticated host, so 401, 403 and 429 mean "needs a token"
	// or "rate-limited" (CodeHFLogin).
	HuggingFace bool
}

// Source opens a byte stream of one file from an offset. validator is the ETag (or Last-Modified)
// saved at the checkpoint, "" when none; a source that finds the file changed answers as a server
// whose If-Range failed: 200 from byte 0. Open returns a nil body for any status that carries no
// bytes (416, 4xx, 5xx). The body is closed by the caller.
type Source interface {
	Open(ctx context.Context, offset int64, validator string) (io.ReadCloser, Resp, error)
}

// ClientConfig builds the HTTP client.
type ClientConfig struct {
	// Version is the agent version in the User-Agent; default "dev".
	Version string
	// RootCAs replaces the system roots (tests, an air-gapped CA); nil means the system roots
	// (/etc/ssl/certs/ca-certificates.crt). Verification itself cannot be turned off.
	RootCAs *x509.CertPool
	// DialContext replaces the dialer (tests); default a net.Dialer with DialTimeout.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// AuthHosts are the hosts that may receive the token; default {AuthHost}.
	AuthHosts []string
	// Token returns the Hugging Face token at request time ("" when there is none). It is called
	// for every request that may carry it and its result is never stored or logged.
	Token func() string
}

// HTTPClient makes Sources over one shared transport.
type HTTPClient struct {
	c     *http.Client
	ua    string
	auth  map[string]bool
	token func() string
}

// NewHTTPClient builds the client: HTTP/1.1 and HTTP/2, TLS 1.2 or later, system roots, dial and
// TLS handshake 15 s, response headers 30 s, at most 10 redirects, none of them to plain http, no
// proxy (the agent unit sets no proxy environment, and HTTPS_PROXY is ignored here), and no
// transparent decompression (Range offsets and sizes count the bytes on the wire).
func NewHTTPClient(cfg ClientConfig) *HTTPClient {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	dial := cfg.DialContext
	if dial == nil {
		dial = netx.Default.Dialer(DialTimeout)
	}
	// Every dial goes through the network fence (netx): a sync never runs during a drill, and if one
	// did, the drill would see it.
	tr := netx.NewTransport("sync.download", dial)
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs}
	tr.TLSHandshakeTimeout = TLSHandshakeTimeout
	tr.ResponseHeaderTimeout = ResponseHeaderTimeout
	tr.DisableCompression = true
	tr.MaxIdleConns = 8
	tr.IdleConnTimeout = 90 * time.Second
	h := &HTTPClient{ua: UserAgent(cfg.Version), auth: map[string]bool{}, token: cfg.Token}
	hosts := cfg.AuthHosts
	if len(hosts) == 0 {
		hosts = []string{AuthHost}
	}
	for _, a := range hosts {
		h.auth[strings.ToLower(a)] = true
	}
	h.c = &http.Client{Transport: tr, CheckRedirect: checkRedirect}
	return h
}

// ErrRedirectHTTP: a redirect to a URL that is not https was refused.
var ErrRedirectHTTP = errors.New("download: redirect to a non-https URL refused")

// checkRedirect: at most MaxRedirects, never to a non-https URL, and the Authorization header is
// dropped as soon as the host differs from the first request's (Go would keep it for a subdomain).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("download: stopped after %d redirects", MaxRedirects)
	}
	if req.URL.Scheme != "https" {
		return ErrRedirectHTTP
	}
	if !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
		req.Header.Del("Authorization")
	}
	return nil
}

// Transport is the shared transport (TLS rules, timeouts, no proxy, no transparent decompression), for
// the clients of small metadata requests (the resolvers' HEAD and JSON reads) that must dial the way
// the downloads do. The caller sets its own redirect policy on its own http.Client.
func (h *HTTPClient) Transport() http.RoundTripper { return h.c.Transport }

// Client returns the underlying *http.Client: the same transport, TLS rules and https-only redirect rule
// as the downloads, for the metadata requests of the resolvers (a directory listing, a HEAD). The caller
// must not change it; a request that must not follow redirects clones it first.
func (h *HTTPClient) Client() *http.Client { return h.c }

// Source returns the Source of one URL. With useAuth the request carries the token, but only when
// the URL is https and its host is an auth host.
func (h *HTTPClient) Source(rawURL string, useAuth bool) Source {
	return &httpSource{h: h, url: rawURL, useAuth: useAuth}
}

type httpSource struct {
	h       *HTTPClient
	url     string
	useAuth bool
}

// Open implements Source.
func (s *httpSource) Open(ctx context.Context, offset int64, validator string) (io.ReadCloser, Resp, error) {
	u, err := url.Parse(s.url)
	if err != nil {
		return nil, Resp{}, &Error{Kind: KindPermanent, Code: CodeRefused, Permanent: true, Err: fmt.Errorf("download: bad URL")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, Resp{}, &Error{Kind: KindPermanent, Code: CodeRefused, Permanent: true, Err: fmt.Errorf("download: bad URL")}
	}
	req.Header.Set("User-Agent", s.h.ua)
	req.Header.Set("Accept-Encoding", "identity")
	hf := s.h.auth[strings.ToLower(u.Hostname())]
	if s.useAuth && hf && u.Scheme == "https" && s.h.token != nil {
		if tok := s.h.token(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	resp, err := s.h.c.Do(req)
	if err != nil {
		return nil, Resp{}, sanitizeErr(err)
	}
	r := Resp{
		Status:       resp.StatusCode,
		Size:         -1,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		RetryAfter:   resp.Header.Get("Retry-After"),
		HuggingFace:  s.useAuth && hf,
	}
	switch resp.StatusCode {
	case http.StatusOK:
		r.Size = resp.ContentLength
	case http.StatusPartialContent:
		start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			_ = resp.Body.Close()
			return nil, Resp{}, &retryable{err: errors.New("download: 206 without a usable Content-Range"), restart: true}
		}
		r.Offset, r.Size = start, total
	case http.StatusRequestedRangeNotSatisfiable:
		_, _, r.Size, _ = parseContentRange(resp.Header.Get("Content-Range"))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, r, nil
	}
	return resp.Body, r, nil
}

// parseContentRange reads "bytes <start>-<end>/<total>" and "bytes */<total>". total is -1 when the
// server says "*". ok is false for anything else.
func parseContentRange(v string) (start, end, total int64, ok bool) {
	v = strings.TrimSpace(v)
	rest, found := strings.CutPrefix(v, "bytes ")
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

// RedactURL reduces a URL to scheme, host and path: no user info, query or fragment. A signed
// download URL keeps its credential in the query, so this is what every log line and error uses. An
// unparseable string becomes "(invalid url)".
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid url)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// sanitizeErr turns what the HTTP client returns into an error that is safe to log: a *url.Error
// carries the full URL (query included), so only its cause is kept, with the redacted URL. TLS
// certificate errors and refused redirects are permanent; everything else (dial, reset, timeout) is
// left to the retry policy.
func sanitizeErr(err error) error {
	if isCtx(err) {
		return err
	}
	var ue *url.Error
	cause := err
	where := ""
	if errors.As(err, &ue) {
		cause = ue.Err
		where = RedactURL(ue.URL)
	}
	var (
		unknown  x509.UnknownAuthorityError
		invalid  x509.CertificateInvalidError
		hostname x509.HostnameError
		verify   *tls.CertificateVerificationError
	)
	switch {
	case errors.Is(cause, ErrRedirectHTTP), errors.As(cause, &unknown), errors.As(cause, &invalid), errors.As(cause, &hostname), errors.As(cause, &verify):
		return &Error{Kind: KindPermanent, Code: CodeRefused, Permanent: true, Err: fmt.Errorf("%s: %w", where, scrub(cause))}
	}
	return fmt.Errorf("%s: %w", where, scrub(cause))
}

// scrub strips any URL out of the message of a plain error (net/http's redirect errors and some
// transport errors repeat the request URL).
func scrub(err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "?") {
		return err
	}
	return &scrubbed{msg: scrubQuery(msg), cause: err}
}

type scrubbed struct {
	msg   string
	cause error
}

func (s *scrubbed) Error() string { return s.msg }
func (s *scrubbed) Unwrap() error { return s.cause }

// scrubQuery removes "?query" runs (up to whitespace or a quote) from a message.
func scrubQuery(msg string) string {
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		if msg[i] != '?' {
			b.WriteByte(msg[i])
			continue
		}
		j := i
		for j < len(msg) && !strings.ContainsRune(" \t\r\n\"'<>", rune(msg[j])) {
			j++
		}
		b.WriteString("?(redacted)")
		i = j - 1
	}
	return b.String()
}
