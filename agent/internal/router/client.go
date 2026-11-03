package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Defaults of the client (llama-router.md section 3.6).
const (
	// DefaultPort is the router's loopback port (llama.port of node.yaml).
	DefaultPort = 8080
	// DefaultTimeout is the per-call timeout of every call except ChatSmoke.
	DefaultTimeout = 5 * time.Second
	// DefaultChatTimeout bounds ChatSmoke: it generates tokens, and a CPU-only node needs far more
	// than the 5 s of a status call.
	DefaultChatTimeout = 30 * time.Second
	// maxModelsBody bounds GET /models; maxBody every other response. A larger body is malformed.
	maxModelsBody = 8 << 20
	maxBody       = 1 << 20
)

// Errors of the client. Use errors.Is; CodeOf maps them to doctor codes.
var (
	// ErrDown: the router did not answer (connection refused, reset or timed out) or answered 502,
	// 503 or 504. Callers report it as LAI-LLM-004.
	ErrDown = errors.New("router: the router is not answering")
	// ErrUnauthorized: the router answered 401 to our key, after one retry with a re-read key when
	// the key had changed. Callers report it as LAI-LLM-005.
	ErrUnauthorized = errors.New("router: the router rejected our key")
	// ErrRedirect: the router answered with a redirect. The client never follows one, so the key
	// cannot be sent to another place.
	ErrRedirect = errors.New("router: unexpected redirect")
	// ErrNotFound: the router answered 404.
	ErrNotFound = errors.New("router: not found")
	// ErrBadBaseURL: Options.BaseURL is not a plain http URL of a loopback address.
	ErrBadBaseURL = errors.New("router: the base URL must be http on a loopback address")
	// ErrBadModelID: a model id that cannot be sent (empty, too long, control characters).
	ErrBadModelID = errors.New("router: invalid model id")
	// ErrRefused: the client was asked for a call outside its allow list (a download, a delete,
	// autoload). It is a programming error; nothing was sent.
	ErrRefused = errors.New("router: this call is not allowed")
)

// HTTPError is an error answer of the router. The router's error object is
// {"error": {"code", "message", "type"}}; Message is that message, scrubbed and cut to 200 bytes.
type HTTPError struct {
	Status  int
	Type    string
	Message string
}

// Error implements error.
func (e *HTTPError) Error() string {
	if e.Message == "" {
		return "router: HTTP " + strconv.Itoa(e.Status)
	}
	return "router: HTTP " + strconv.Itoa(e.Status) + ": " + e.Message
}

// Is makes errors.Is(err, ErrUnauthorized), ErrDown and ErrNotFound work on an answer.
func (e *HTTPError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrDown:
		return e.Status == http.StatusBadGateway || e.Status == http.StatusServiceUnavailable || e.Status == http.StatusGatewayTimeout
	}
	return false
}

// Router is the typed view of llama-server's router API (llama-router.md section 3.6). The orchestrator,
// the watcher, the doctor and the bench depend on this interface; internal/router/fake serves a
// scripted router over HTTP for their tests. Every call honours ctx and its own timeout, and none
// of them ever puts the key in an error or a log line.
type Router interface {
	// Health calls GET /health, which needs no key. nil means the router is up and ready.
	Health(ctx context.Context) error
	// Models calls GET /models: the status of every preset (and cache model).
	Models(ctx context.Context) ([]ModelStatus, error)
	// Reload calls GET /models?reload=1: the router re-reads models.ini. A running model whose
	// preset changed or was removed is unloaded by the router.
	Reload(ctx context.Context) error
	// Load calls POST /models/load. It returns when the router has accepted the request; readiness
	// is observed through Models. A model that is already running is an *HTTPError (400).
	Load(ctx context.Context, id string) error
	// Unload calls POST /models/unload. A model that is not running is an *HTTPError (400).
	Unload(ctx context.Context, id string) error
	// Progress opens GET /models/sse and returns its events. The channel closes when the stream
	// ends or ctx is cancelled; the caller reconnects. Progress is for display, never correctness.
	Progress(ctx context.Context) (<-chan Progress, error)
	// Props calls GET /props?model=<id>: the context size and build of a loaded model.
	Props(ctx context.Context, id string) (Props, error)
	// ChatSmoke sends one 8-token chat completion to a loaded model (doctor and drill).
	ChatSmoke(ctx context.Context, model string) error
}

// Options configures NewClient.
type Options struct {
	// BaseURL is the router's address, default http://127.0.0.1:<Port>. It must be http on a
	// loopback IP or localhost, with no credentials, path, query or fragment.
	BaseURL string
	// Port is used when BaseURL is empty; default DefaultPort.
	Port int
	// Keys supplies the key. Required for every call except Health.
	Keys KeySource
	// Timeout is the per-call timeout, default DefaultTimeout. ChatTimeout bounds ChatSmoke,
	// default DefaultChatTimeout.
	Timeout, ChatTimeout time.Duration
	// Transport replaces the default transport (tests only). The default ignores the proxy
	// environment, dials loopback addresses only and uses no compression.
	Transport http.RoundTripper
	// Logger receives the key-rotation notice (the fingerprint, never the key). Default slog.Default().
	Logger *slog.Logger
}

// Client is the real Router. It is safe for concurrent use.
type Client struct {
	base        *url.URL
	keys        KeySource
	timeout     time.Duration
	chatTimeout time.Duration
	hc          *http.Client
	log         *slog.Logger
}

var _ Router = (*Client)(nil)

// NewClient returns a Client, or ErrBadBaseURL when the base URL is not a loopback http URL.
func NewClient(opts Options) (*Client, error) {
	base := opts.BaseURL
	if base == "" {
		port := opts.Port
		if port == 0 {
			port = DefaultPort
		}
		base = "http://127.0.0.1:" + strconv.Itoa(port)
	}
	u, err := parseBase(base)
	if err != nil {
		return nil, err
	}
	c := &Client{base: u, keys: opts.Keys, timeout: opts.Timeout, chatTimeout: opts.ChatTimeout, log: opts.Logger}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.chatTimeout <= 0 {
		c.chatTimeout = DefaultChatTimeout
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.log = c.log.With("component", "router")
	rt := opts.Transport
	if rt == nil {
		rt = newTransport()
	}
	c.hc = &http.Client{
		Transport: rt,
		// A redirect is never followed: the key must not travel to a place the agent did not choose.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

// KeyRotated tells the client that the key was rotated: the next call re-reads the key file even if
// its modification time looks unchanged. The watcher calls it on key.rotated.
func (c *Client) KeyRotated() {
	if inv, ok := c.keys.(KeyInvalidator); ok {
		inv.Invalidate()
	}
}

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || !loopbackHost(u.Hostname()) {
		return nil, ErrBadBaseURL
	}
	return &url.URL{Scheme: "http", Host: u.Host}, nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// newTransport is the hardened default: no proxy, loopback dials only, no compression.
func newTransport() *http.Transport {
	d := &net.Dialer{
		Timeout: 3 * time.Second,
		// Refuse to connect anywhere but loopback, whatever the name resolved to.
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil || !loopbackHost(host) {
				return errors.New("router: refusing to dial a non-loopback address")
			}
			return nil
		},
	}
	return &http.Transport{
		Proxy:              nil,
		DialContext:        d.DialContext,
		MaxIdleConns:       2,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
}

// route is one entry of the allow list: the only calls the client can make.
type route struct{ method, path string }

var allowed = map[route]bool{
	{http.MethodGet, "/health"}:               true,
	{http.MethodGet, "/models"}:               true,
	{http.MethodPost, "/models/load"}:         true,
	{http.MethodPost, "/models/unload"}:       true,
	{http.MethodGet, "/models/sse"}:           true,
	{http.MethodGet, "/props"}:                true,
	{http.MethodPost, "/v1/chat/completions"}: true,
	{http.MethodGet, "/v1/models"}:            true,
}

// call describes one request.
type call struct {
	method, path string
	query        url.Values
	body         any
	noAuth       bool
	timeout      time.Duration // 0: the client default
	maxBody      int64         // 0: maxBody
	stream       bool          // SSE: no overall timeout, the body is returned open
}

// reply is a finished call.
type reply struct {
	status int
	body   []byte
	stream io.ReadCloser // set for stream calls on success
	cancel context.CancelFunc
}

func (c *Client) build(ctx context.Context, cl call, key string) (*http.Request, error) {
	u := *c.base
	u.Path = cl.path
	if len(cl.query) > 0 {
		u.RawQuery = cl.query.Encode()
	}
	var rd io.Reader
	if cl.body != nil {
		b, err := json.Marshal(cl.body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, cl.method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "aiagent")
	if cl.stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if cl.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !cl.noAuth {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

// do sends one call. It checks the allow list, attaches the key, retries once after a 401 when the
// key file changed, and classifies failures into the package's errors. The key is never part of an
// error.
func (c *Client) do(ctx context.Context, cl call) (*reply, error) {
	if !allowed[route{cl.method, cl.path}] || cl.query.Has("autoload") {
		return nil, fmt.Errorf("%w: %s %s", ErrRefused, cl.method, cl.path)
	}
	retried := false
	for {
		key := ""
		if !cl.noAuth {
			if c.keys == nil {
				return nil, fmt.Errorf("%w: no key source", ErrKeyUnavailable)
			}
			k, err := c.keys.Key()
			if err != nil {
				return nil, err
			}
			key = k
		}
		rep, err := c.once(ctx, cl, key)
		if err != nil {
			return nil, err
		}
		if rep.status == http.StatusUnauthorized && !cl.noAuth && !retried {
			retried = true
			// The key may have been rotated between our read and the router's restart: read the
			// file again, and send once more only when it really is a different key.
			if r, ok := c.keys.(KeyRefresher); ok {
				if changed, rerr := r.Refresh(); rerr == nil && changed {
					c.log.Info("the llama key file changed; retrying with the new key", "fingerprint", c.fingerprint())
					continue
				}
			}
		}
		if rep.status/100 != 2 {
			return nil, c.statusErr(rep.status, rep.body)
		}
		return rep, nil
	}
}

func (c *Client) fingerprint() string {
	if c.keys == nil {
		return ""
	}
	k, err := c.keys.Key()
	if err != nil {
		return ""
	}
	return Fingerprint(k)
}

// discard releases a stream reply.
func (r *reply) discard() {
	if r.stream != nil {
		_ = r.stream.Close()
	}
	if r.cancel != nil {
		r.cancel()
	}
}

// once sends the request and reads the answer, or opens the stream. Any HTTP status is a reply
// (the caller classifies it); only transport failures, redirects and oversized bodies are errors.
func (c *Client) once(ctx context.Context, cl call, key string) (*reply, error) {
	timeout := cl.timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	var rctx context.Context
	var cancel context.CancelFunc
	if cl.stream {
		// The stream lives as long as ctx; only the wait for the response headers is bounded.
		rctx, cancel = context.WithCancel(ctx)
	} else {
		rctx, cancel = context.WithTimeout(ctx, timeout)
	}
	req, err := c.build(rctx, cl, key)
	if err != nil {
		cancel()
		return nil, err
	}
	var timer *time.Timer
	if cl.stream {
		timer = time.AfterFunc(timeout, cancel)
	}
	resp, err := c.hc.Do(req)
	if timer != nil && !timer.Stop() && err == nil {
		// The header deadline fired just as the answer arrived: treat it as a timeout.
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("%w: no answer within %s", ErrDown, timeout)
	}
	if err != nil {
		cancel()
		return nil, c.netErr(ctx, err, timeout)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		cancel()
		return nil, ErrRedirect
	}
	if cl.stream && resp.StatusCode/100 == 2 {
		return &reply{status: resp.StatusCode, stream: resp.Body, cancel: cancel}, nil
	}
	limit := cl.maxBody
	if limit <= 0 {
		limit = maxBody
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	_ = resp.Body.Close()
	cancel()
	if rerr != nil {
		return nil, c.netErr(ctx, rerr, timeout)
	}
	if int64(len(body)) > limit {
		return nil, malformed("response over %d bytes", limit)
	}
	return &reply{status: resp.StatusCode, body: body}, nil
}

// netErr turns a transport failure into ErrDown, or into ctx's own error when the caller cancelled.
func (c *Client) netErr(ctx context.Context, err error, timeout time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: no answer within %s", ErrDown, timeout)
	}
	// The error text of net/http holds the method, the URL and the cause, never a header.
	return fmt.Errorf("%w: %s", ErrDown, logx.ScrubString(err.Error()))
}

// statusErr builds the *HTTPError of a non-2xx answer from the router's error object.
func (c *Client) statusErr(status int, body []byte) error {
	e := &HTTPError{Status: status}
	var obj struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &obj) == nil {
		e.Message = clip(logx.ScrubString(obj.Error.Message), 200)
		e.Type = clip(obj.Error.Type, 64)
	}
	return e
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Health implements Router. The call carries no Authorization header.
func (c *Client) Health(ctx context.Context) error {
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/health", noAuth: true})
	if err != nil {
		return err
	}
	// 200 means ready. A JSON body with another status is not ready; an empty or non-JSON body on a
	// 200 is accepted, so an odd build does not make a healthy router look down.
	var h struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(rep.body, &h) == nil && h.Status != "" && h.Status != "ok" {
		return fmt.Errorf("%w: health status %q", ErrDown, clip(h.Status, 32))
	}
	return nil
}

// Models implements Router.
func (c *Client) Models(ctx context.Context) ([]ModelStatus, error) {
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/models", maxBody: maxModelsBody})
	if err != nil {
		return nil, err
	}
	return ParseModels(rep.body)
}

// Reload implements Router.
func (c *Client) Reload(ctx context.Context) error {
	_, err := c.do(ctx, call{method: http.MethodGet, path: "/models", query: url.Values{"reload": {"1"}}, maxBody: maxModelsBody})
	return err
}

// Load implements Router.
func (c *Client) Load(ctx context.Context, id string) error { return c.act(ctx, "/models/load", id) }

// Unload implements Router.
func (c *Client) Unload(ctx context.Context, id string) error {
	return c.act(ctx, "/models/unload", id)
}

func (c *Client) act(ctx context.Context, path, id string) error {
	if !validID(id) {
		return ErrBadModelID
	}
	rep, err := c.do(ctx, call{method: http.MethodPost, path: path, body: map[string]string{"model": id}})
	if err != nil {
		return err
	}
	var r struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(rep.body, &r); err != nil || r.Success == nil {
		return malformed("%s answered without success", path)
	}
	if !*r.Success {
		return &HTTPError{Status: rep.status, Message: "the router refused the request"}
	}
	return nil
}

// Props implements Router.
func (c *Client) Props(ctx context.Context, id string) (Props, error) {
	if !validID(id) {
		return Props{}, ErrBadModelID
	}
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/props", query: url.Values{"model": {id}}})
	if err != nil {
		return Props{}, err
	}
	return ParseProps(rep.body)
}

// ChatSmoke implements Router: one user message, max_tokens 8, no streaming, no autoload query.
func (c *Client) ChatSmoke(ctx context.Context, model string) error { return c.Chat(ctx, model, 8) }

// Chat is ChatSmoke with the size of the completion chosen by the caller: one user message, at most
// maxTokens tokens, no streaming, no autoload query. It is not part of Router: `aictl rebuild-llama`
// asks for 16 tokens in the post-switch check of a new build (llama-router.md section 4.10.3). The
// call is bounded by ctx and by Options.ChatTimeout.
func (c *Client) Chat(ctx context.Context, model string, maxTokens int) error {
	if !validID(model) {
		return ErrBadModelID
	}
	if maxTokens < 1 {
		maxTokens = 1
	}
	rep, err := c.do(ctx, call{
		method: http.MethodPost, path: "/v1/chat/completions", timeout: c.chatTimeout,
		body: map[string]any{
			"model":       model,
			"messages":    []map[string]string{{"role": "user", "content": "Reply with the word OK."}},
			"max_tokens":  maxTokens,
			"temperature": 0,
			"stream":      false,
		},
	})
	if err != nil {
		return err
	}
	var r struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(rep.body, &r); err != nil || len(r.Choices) == 0 {
		return malformed("chat completion without choices")
	}
	return nil
}

// Progress implements Router.
func (c *Client) Progress(ctx context.Context) (<-chan Progress, error) {
	rep, err := c.do(ctx, call{method: http.MethodGet, path: "/models/sse", stream: true})
	if err != nil {
		return nil, err
	}
	ch := make(chan Progress, 32)
	go func() {
		defer close(ch)
		defer rep.discard()
		// Closing the body unblocks the read when ctx ends.
		stop := context.AfterFunc(ctx, func() { _ = rep.stream.Close() })
		defer stop()
		_, _ = readEvents(rep.stream, func(p Progress) bool {
			select {
			case ch <- p:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return ch, nil
}
