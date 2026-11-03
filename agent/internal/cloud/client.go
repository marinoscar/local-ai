package cloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// maxReplyBytes bounds a reply body: the catalog bundle (2 MB, JSON-escaped) is the largest.
const maxReplyBytes = 8 << 20

// Doer is the part of *http.Client the cloud client uses.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewHTTPClient returns the one http.Client of the cloud link. TLS verification is always on (the
// default transport verifies the chain and the host name; nothing here turns it off) and the minimum
// version is TLS 1.2. Per-request deadlines come from the context, not from the client.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 60 * time.Second
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		// A redirect would carry the node credential to another address: never follow one.
		return http.ErrUseLastResponse
	}}
}

// CheckURL validates cloud.url: https://host[:port] without a path, or http://127.0.0.1:<port> when
// devInsecure is true (agent.md section 4.8, implementation note 1). The error text is for people;
// callers add the doctor code LAI-CLD-062.
func CheckURL(raw string, devInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid address: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		port, perr := strconv.Atoi(u.Port())
		if u.Hostname() != "127.0.0.1" || perr != nil || port < 1 || port > 65535 || !devInsecure {
			return errors.New("must start with https:// (http://127.0.0.1:<port> is allowed only with dev_insecure: true)")
		}
	default:
		return errors.New("must start with https:// (http://127.0.0.1:<port> is allowed only with dev_insecure: true)")
	}
	switch {
	case u.Hostname() == "":
		return errors.New("needs a host name")
	case u.User != nil:
		return errors.New("must not contain a user name or password")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.ForceQuery, u.Fragment != "", strings.Contains(raw, "#"):
		return errors.New("must be an address without a path, query or fragment")
	}
	return nil
}

// ClientOptions configure a Client.
type ClientOptions struct {
	// BaseURL is cloud.url without a trailing slash.
	BaseURL string
	// Credential returns the node credential for the Authorization header.
	Credential func() string
	// HTTP defaults to NewHTTPClient().
	HTTP Doer
	// UserAgent defaults to "aiagent".
	UserAgent string
}

// Client sends the requests of the link. It holds no lock across a call and is safe for concurrent
// use.
type Client struct {
	base string
	cred func() string
	http Doer
	ua   string
}

// NewClient returns a Client.
func NewClient(o ClientOptions) *Client {
	c := &Client{base: strings.TrimRight(o.BaseURL, "/"), cred: o.Credential, http: o.HTTP, ua: o.UserAgent}
	if c.http == nil {
		c.http = NewHTTPClient()
	}
	if c.ua == "" {
		c.ua = "aiagent"
	}
	return c
}

// BaseURL is the address the client talks to.
func (c *Client) BaseURL() string { return c.base }

// Reply is a 2xx answer with the EAB envelope removed.
type Reply struct {
	Status int
	Header http.Header
	// Data is the "data" member of the envelope, or the whole body when it has none.
	Data json.RawMessage
}

// Decode reads Data into out.
func (r *Reply) Decode(out any) error {
	if len(r.Data) == 0 {
		return nil
	}
	return json.Unmarshal(r.Data, out)
}

// RequestOption adjusts one request.
type RequestOption func(*http.Request)

// WithHeader sets a request header.
func WithHeader(k, v string) RequestOption { return func(r *http.Request) { r.Header.Set(k, v) } }

// Call sends method path with body (nil for none; a []byte or json.RawMessage is sent as it is, anything else
// is marshalled) and a timeout. A non-2xx answer is an *APIError, a transport failure the transport's
// error; a 304 is a Reply with Status 304 and no data.
func (c *Client) Call(ctx context.Context, method, path string, body any, timeout time.Duration, opts ...RequestOption) (*Reply, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case json.RawMessage:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cloud: encode request: %w", err)
		}
		rd = bytes.NewReader(raw)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, fmt.Errorf("cloud: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cred != nil {
		if cred := c.cred(); cred != "" {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, scrubTransportError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if err != nil {
		return nil, scrubTransportError(err)
	}
	if len(raw) > maxReplyBytes {
		return nil, errors.New("cloud: the answer is larger than the limit")
	}
	if resp.StatusCode == http.StatusNotModified {
		return &Reply{Status: resp.StatusCode, Header: resp.Header}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, parseAPIError(resp, raw)
	}
	return &Reply{Status: resp.StatusCode, Header: resp.Header, Data: unwrap(raw)}, nil
}

// scrubTransportError keeps the transport's message (it names the host and the failure) but runs it
// through the scrubber, so an address of the tailnet or a token can never reach a log through it.
func scrubTransportError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &NetError{Msg: logx.ScrubString(err.Error()), err: err}
}

// NetError is a request that never got an answer (DNS, connect, TLS, timeout, reset).
type NetError struct {
	Msg string
	err error
}

func (e *NetError) Error() string { return "cloud: " + e.Msg }
func (e *NetError) Unwrap() error { return e.err }

// unwrap removes the {data, meta} envelope. A body without one is returned as it is.
func unwrap(raw []byte) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return raw
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw
	}
	if d, ok := env["data"]; ok {
		return d
	}
	return raw
}

// APIError is a non-2xx answer. Code and Reason are the cloud's own ("details.code" and
// "details.reason" of the error body, agent.md section 4.8.2).
type APIError struct {
	Status int
	// Code is details.code, for example LAI-CLD-008; "" when the body has none.
	Code string
	// Reason is details.reason, for example stale_seq.
	Reason string
	// Message is the server's message, scrubbed and cut to 200 characters. Never log it for the key route.
	Message string
	// Paths are the Zod issue paths of a 400, never their values.
	Paths []string
	// RetryAfter is the Retry-After header of a 429 or 503, 0 when absent.
	RetryAfter time.Duration

	detailsText string
}

func (e *APIError) Error() string {
	s := "cloud: HTTP " + strconv.Itoa(e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// Mentions reports whether the error details contain word (case-insensitive): a 400 that "mentions
// extra" is the cloud saying it does not know vitals.extra (agent.md section 4.8.3).
func (e *APIError) Mentions(word string) bool {
	return strings.Contains(e.detailsText, strings.ToLower(word))
}

// parseAPIError reads the error body tolerantly: the details object may sit at the top level or inside
// "error", the message may be a string at either place.
func parseAPIError(resp *http.Response, raw []byte) *APIError {
	e := &APIError{Status: resp.StatusCode}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				e.RetryAfter = d
			}
		}
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return e
	}
	var inner map[string]json.RawMessage
	if r, ok := top["error"]; ok {
		_ = json.Unmarshal(r, &inner)
	}
	pick := func(key string) json.RawMessage {
		if v, ok := top[key]; ok {
			return v
		}
		return inner[key]
	}
	if m := pick("message"); m != nil {
		var s string
		if json.Unmarshal(m, &s) == nil {
			e.Message = cut(logx.ScrubString(s), 200)
		}
	}
	if d := pick("details"); d != nil {
		e.detailsText = strings.ToLower(string(d))
		var det struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
			Issues []struct {
				Path []any `json:"path"`
			} `json:"issues"`
		}
		if json.Unmarshal(d, &det) == nil {
			e.Code, e.Reason = det.Code, det.Reason
			for _, is := range det.Issues {
				parts := make([]string, 0, len(is.Path))
				for _, p := range is.Path {
					parts = append(parts, fmt.Sprint(p))
				}
				e.Paths = append(e.Paths, cut(logx.ScrubString(strings.Join(parts, ".")), 120))
			}
		}
	}
	if e.Code == "" {
		// Some envelopes put a doctor code at the top level, next to the message.
		var s string
		if c := pick("code"); c != nil && json.Unmarshal(c, &s) == nil {
			if _, known := doctor.Lookup(s); known {
				e.Code = s
			}
		}
	}
	return e
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// asAPIError returns err as an *APIError, or nil when it is a transport failure or something else.
func asAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}
