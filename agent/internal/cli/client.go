package cli

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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Config configures a Client. Zero values take the defaults of aictl.md section 3.1.
type Config struct {
	// Socket, URL and TokenFile are the transport settings (flags, then environment, then defaults).
	Socket    string
	URL       string
	TokenFile string
	// Lang is sent as Accept-Language and picks the language of errors aictl raises itself.
	Lang Lang
	// Version is the CLI version compared with the agent's /health version.
	Version string
	// Verbose prints "via socket" or "via tcp" to Stderr once. Debug logs every request and
	// response (bodies scrubbed with the agent's scrubber) to Stderr (LOCALAI_CLI_DEBUG=1).
	Verbose bool
	Debug   bool
	Stderr  io.Writer
	// Warn receives non-fatal findings (the version skew warning). Nil drops them.
	Warn func(*CLIError)

	// Test hooks. Nil means the real thing.
	DialUnix      func(ctx context.Context, path string) (net.Conn, error)
	ReadFile      func(path string) ([]byte, error)
	Sleep         func(ctx context.Context, d time.Duration) error
	RetryInterval time.Duration
	RetryWindow   time.Duration
}

// Response is a decoded-enough view of one agent response.
type Response struct {
	Status    int
	Header    http.Header
	Body      []byte
	RequestID string // X-Request-Id
	OpID      string // X-LocalAI-Op-Id
}

// Client talks to the agent. It picks the transport on the first request (unix socket, else TCP
// with the token file) and keeps it for the process. It is safe for concurrent use.
type Client struct {
	cfg  Config
	base *url.URL
	sock *http.Client
	tcp  *http.Client
	log  *slog.Logger

	mu        sync.Mutex
	mode      transportKind
	everUp    bool
	announced bool
	token     string
	// pinGrant is the admin PIN grant of this process (SetPinGrant): memory only, never written.
	pinGrant string

	healthOnce sync.Once
	health     api.Health
	healthErr  error
}

// NewClient validates cfg and returns a Client. It makes no request and reads no file. A --url that
// is not loopback is refused here (exit 2), before any request.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Lang == "" {
		cfg.Lang = LangEN
	}
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket
	}
	if cfg.URL == "" {
		cfg.URL = DefaultURL
	}
	if cfg.TokenFile == "" {
		cfg.TokenFile = DefaultTokenFile
	}
	if cfg.DialUnix == nil {
		cfg.DialUnix = dialUnix
	}
	if cfg.ReadFile == nil {
		cfg.ReadFile = os.ReadFile
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.RetryInterval <= 0 {
		cfg.RetryInterval = DefaultRetryInterval
	}
	if cfg.RetryWindow <= 0 {
		cfg.RetryWindow = DefaultRetryWindow
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}
	base, err := parseAgentURL(cfg.URL, cfg.Lang)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, base: base, tcp: newTCPHTTP()}
	c.sock = c.newSocketHTTP()
	if cfg.Debug {
		c.log = slog.New(logx.Scrub(slog.NewJSONHandler(cfg.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
	return c, nil
}

// Close releases idle connections.
func (c *Client) Close() {
	c.sock.CloseIdleConnections()
	c.tcp.CloseIdleConnections()
}

const (
	// pinVerifyPath is POST /pin/verify (agent.md section 3.8.3).
	pinVerifyPath = "/pin/verify"
	// pinGrantHeader carries the grant on a retried admin request (agent.md section 3.9.1 step 6).
	pinGrantHeader = "X-LocalAI-Pin-Grant"
)

// SetPinGrant makes every later request carry the admin PIN grant in X-LocalAI-Pin-Grant (agent.md
// section 3.9.1 step 6), so an [admin] command works for an operator. The grant lives only in this
// Client, for the life of the command; it is never written anywhere and never logged. An empty
// string forgets it.
func (c *Client) SetPinGrant(grant string) {
	c.mu.Lock()
	c.pinGrant = grant
	c.mu.Unlock()
}

func (c *Client) grant() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pinGrant
}

// secretPath reports whether a request to path carries or returns a secret the debug log must not
// show: the PIN goes out in the body of POST /pin/verify and the grant comes back in its answer.
func secretPath(path string) bool { return path == pinVerifyPath }

// Transport reports which transport the client settled on: "socket", "tcp" or "" before the first
// request.
func (c *Client) Transport() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mode == viaNone {
		return ""
	}
	return c.mode.String()
}

// Health returns GET /health, once per process. The first call is also the transport probe and a
// version comparison: a different agent version raises the version skew warning and goes on.
func (c *Client) Health(ctx context.Context) (api.Health, error) {
	c.healthOnce.Do(func() {
		var h api.Health
		_, err := c.do(ctx, http.MethodGet, "/health", nil, &h)
		if err != nil {
			c.healthErr = err
			return
		}
		c.health = h
		if c.cfg.Version != "" && h.Version != c.cfg.Version && c.cfg.Warn != nil {
			msg := fmt.Sprintf(pick(c.cfg.Lang,
				"aictl %s and the agent %s are different versions.",
				"aictl %s y el agente %s son de versiones distintas."), c.cfg.Version, h.Version)
			c.cfg.Warn(NewCLIError(c.cfg.Lang, CodeVersionSkew, msg).
				WithDetails("cli_version", c.cfg.Version, "agent_version", h.Version))
		}
	})
	return c.health, c.healthErr
}

// Do sends one API request: path is relative to /api/v1 (and may carry a query), in is the JSON
// body (nil: none on a read, "{}" on a mutation; []byte and json.RawMessage go out as they are), out
// receives a 2xx body (a pointer to a struct, or *json.RawMessage for the body unchanged; nil to
// ignore it). Unknown fields in the answer are ignored. A non-2xx answer is an *APIError (the
// Response is returned with it); an undecodable one is an exit-1 CLIError.
//
// Every API call checks the agent version once (Health) before its first request.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) (*Response, error) {
	if path != "/health" {
		if _, err := c.Health(ctx); err != nil {
			return nil, err
		}
	}
	return c.do(ctx, method, path, in, out)
}

// Stream sends a request and returns the live response for the caller to read and close, for
// server-sent events and log following (AICTL-03). Transport choice, the startup retry and the
// version check are the same as for Do; a non-2xx answer is returned as an error and the body is
// consumed. Streaming commands pass a context without a deadline unless the user set --timeout.
func (c *Client) Stream(ctx context.Context, method, path string, in any) (*http.Response, error) {
	return c.StreamWith(ctx, method, path, in, nil)
}

// StreamWith is Stream with extra request headers (Last-Event-ID on a reconnect, AICTL-03). The
// headers are copied; Accept is always text/event-stream.
func (c *Client) StreamWith(ctx context.Context, method, path string, in any, hdr http.Header) (*http.Response, error) {
	if _, err := c.Health(ctx); err != nil {
		return nil, err
	}
	body, err := c.encode(method, in)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	for k, vs := range hdr {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	h.Set("Accept", "text/event-stream")
	resp, err := c.send(ctx, method, path, body, h)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	r, err := c.readResponse(resp)
	if err != nil {
		return nil, c.readErr(ctx, err)
	}
	return nil, c.decodeError(r)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (*Response, error) {
	body, err := c.encode(method, in)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, method, path, body, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r, err := c.readResponse(resp)
	if err != nil {
		return r, c.readErr(ctx, err)
	}
	if c.log != nil {
		if secretPath(path) {
			c.log.Debug("response", "status", r.Status, "request_id", r.RequestID)
		} else {
			c.log.Debug("response", "status", r.Status, "request_id", r.RequestID, bodyAttr(r.Body))
		}
	}
	if r.Status/100 != 2 {
		return r, c.decodeError(r)
	}
	if out == nil || len(bytes.TrimSpace(r.Body)) == 0 {
		return r, nil
	}
	switch o := out.(type) {
	case *json.RawMessage:
		if !json.Valid(r.Body) {
			return r, c.badResponse(r, "invalid JSON")
		}
		*o = append((*o)[:0], r.Body...)
	default:
		if err := json.Unmarshal(r.Body, out); err != nil {
			return r, c.badResponse(r, err.Error())
		}
	}
	return r, nil
}

// encode returns the request body: the JSON of in, "{}" for a mutation without one, nothing for a
// read. A mutation always carries a body because it always carries Content-Type.
func (c *Client) encode(method string, in any) ([]byte, error) {
	switch b := in.(type) {
	case nil:
	case []byte:
		return b, nil
	case json.RawMessage:
		return b, nil
	default:
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, NewCLIError(c.cfg.Lang, CodeUsage, "").WithCause(err)
		}
		return raw, nil
	}
	if isMutating(method) {
		return []byte("{}"), nil
	}
	return nil, nil
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// readErr passes through a CLIError (an oversized answer) and classifies a read failure.
func (c *Client) readErr(ctx context.Context, err error) error {
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce
	}
	return c.classify(ctx, err)
}

func (c *Client) readResponse(resp *http.Response) (*Response, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	r := &Response{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      body,
		RequestID: resp.Header.Get("X-Request-Id"),
		OpID:      resp.Header.Get("X-LocalAI-Op-Id"),
	}
	if len(body) > maxResponseBody {
		return r, c.badResponse(r, "answer too large")
	}
	return r, nil
}

// badResponse is the exit-1 error for an answer aictl cannot decode; it carries the request id so
// the user can report it.
func (c *Client) badResponse(r *Response, why string) *CLIError {
	e := NewCLIError(c.cfg.Lang, CodeBadResponse, "").WithDetails("status", r.Status, "reason", why)
	e.RequestID = r.RequestID
	return e
}

// decodeError turns a non-2xx response into an *APIError, or a bad-response error when the body is
// not the agent's envelope.
func (c *Client) decodeError(r *Response) error {
	var env struct {
		Error *struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Fix     string         `json:"fix"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &env); err != nil || env.Error == nil || env.Error.Code == "" {
		return c.badResponse(r, "not an error envelope")
	}
	return &APIError{
		Status:     r.Status,
		Code:       env.Error.Code,
		Message:    env.Error.Message,
		Fix:        env.Error.Fix,
		Details:    env.Error.Details,
		RequestID:  r.RequestID,
		OpID:       r.OpID,
		RetryAfter: retryAfter(r.Header),
	}
}

// retryAfter reads a Retry-After header given in seconds (the only form the agent sends); anything
// else is zero.
func retryAfter(h http.Header) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// send sends one request with transport selection and the retry while the agent starts (or
// restarts): a 503 with the starting code, or a lost connection after the agent had answered
// before, is retried every RetryInterval for up to RetryWindow, then it is exit 3. The caller owns
// the returned body.
func (c *Client) send(ctx context.Context, method, path string, body []byte, hdr http.Header) (*http.Response, error) {
	maxRetries := int(c.cfg.RetryWindow / c.cfg.RetryInterval)
	for retries := 0; ; retries++ {
		resp, err := c.attempt(ctx, method, path, body, hdr)
		reason := ""
		switch {
		case err != nil:
			var ce *CLIError
			if !errors.As(err, &ce) || ce.Code != CodeUnreachable || !c.wasUp() {
				return nil, err
			}
			c.resetTransport()
			reason = "restarting"
		default:
			c.markUp()
			if resp.StatusCode != http.StatusServiceUnavailable {
				return resp, nil
			}
			b, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if rerr != nil {
				return nil, c.classify(ctx, rerr)
			}
			resp.Body = io.NopCloser(bytes.NewReader(b))
			if !isStartingBody(b) {
				return resp, nil
			}
			reason = "starting"
		}
		if retries >= maxRetries {
			msg := pick(c.cfg.Lang, "The AI node agent is still starting.", "El agente del nodo de IA todavía está arrancando.")
			if reason == "restarting" {
				msg = ""
			}
			e := NewCLIError(c.cfg.Lang, CodeUnreachable, msg).WithDetails("reason", reason)
			if resp != nil {
				e.RequestID = resp.Header.Get("X-Request-Id")
				resp.Body.Close()
			}
			return nil, e
		}
		if resp != nil {
			resp.Body.Close()
		}
		if serr := c.cfg.Sleep(ctx, c.cfg.RetryInterval); serr != nil {
			return nil, c.classify(ctx, serr)
		}
	}
}

// isStartingBody reports whether a 503 body is the agent's "starting or stopping" envelope.
func isStartingBody(b []byte) bool {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(b, &env) == nil && env.Error.Code == api.CodeStarting
}

func (c *Client) wasUp() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.everUp
}

func (c *Client) markUp() {
	c.mu.Lock()
	c.everUp = true
	c.mu.Unlock()
}

func (c *Client) resetTransport() {
	c.mu.Lock()
	c.mode = viaNone
	c.mu.Unlock()
}

func (c *Client) currentMode() transportKind {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// settle records the transport that worked and announces it once under -v.
func (c *Client) settle(k transportKind) {
	c.mu.Lock()
	c.mode = k
	announce := c.cfg.Verbose && !c.announced
	c.announced = true
	c.mu.Unlock()
	if announce {
		fmt.Fprintf(c.cfg.Stderr, "via %s\n", k)
	}
}

// attempt makes one request. With no transport chosen yet it tries the socket first and falls back
// to TCP when connecting fails (the socket path is missing, nobody listens, or the user may not
// open it) and the token file is readable. The token file is read only on that path.
func (c *Client) attempt(ctx context.Context, method, path string, body []byte, hdr http.Header) (*http.Response, error) {
	switch c.currentMode() {
	case viaSocket:
		resp, err := c.once(ctx, viaSocket, "", method, path, body, hdr)
		if err != nil {
			return nil, c.wrapFailure(ctx, err)
		}
		return resp, nil
	case viaTCP:
		tok, err := c.readToken()
		if err != nil {
			return nil, c.unreachable(err, nil)
		}
		resp, err := c.once(ctx, viaTCP, tok, method, path, body, hdr)
		if err != nil {
			return nil, c.wrapFailure(ctx, err)
		}
		return resp, nil
	}

	resp, sockErr := c.once(ctx, viaSocket, "", method, path, body, hdr)
	if sockErr == nil {
		c.settle(viaSocket)
		return resp, nil
	}
	if ctx.Err() != nil || !isDialError(sockErr) {
		return nil, c.wrapFailure(ctx, sockErr)
	}
	tok, tokErr := c.readToken()
	if tokErr != nil {
		return nil, c.unreachable(sockErr, tokErr)
	}
	resp, tcpErr := c.once(ctx, viaTCP, tok, method, path, body, hdr)
	if tcpErr == nil {
		c.settle(viaTCP)
		return resp, nil
	}
	if ctx.Err() != nil || !isDialError(tcpErr) {
		return nil, c.wrapFailure(ctx, tcpErr)
	}
	return nil, c.unreachable(sockErr, tcpErr)
}

// wrapFailure classifies an error from a transport that was working.
func (c *Client) wrapFailure(ctx context.Context, err error) error {
	if ctx.Err() == nil && isDialError(err) {
		return c.unreachable(err, nil)
	}
	return c.classify(ctx, err)
}

// once builds and sends one request over kind. The socket request carries no Authorization (the
// agent identifies the caller by SO_PEERCRED); the TCP request carries the bearer token and a Host
// header equal to the URL's host:port (agent.md section 3.9.1 step 2).
func (c *Client) once(ctx context.Context, kind transportKind, token, method, path string, body []byte, hdr http.Header) (*http.Response, error) {
	target := "http://" + socketHost + APIPrefix + path
	hc := c.sock
	if kind == viaTCP {
		target = strings.TrimRight(c.base.String(), "/") + APIPrefix + path
		hc = c.tcp
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, NewCLIError(c.cfg.Lang, CodeUsage, "").WithCause(err)
	}
	for k, v := range hdr {
		req.Header[k] = append([]string(nil), v...)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Accept-Language", string(c.cfg.Lang))
	req.Header.Set("User-Agent", "aictl/"+c.cfg.Version)
	if isMutating(method) {
		req.Header.Set("Content-Type", "application/json")
	}
	if g := c.grant(); g != "" && !secretPath(path) {
		req.Header.Set(pinGrantHeader, g)
	}
	if kind == viaTCP {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Host = req.URL.Host
	}
	if c.log != nil {
		attrs := []any{"method", method, "path", APIPrefix + path, "via", kind.String(), "authorization", req.Header.Get("Authorization")}
		if body != nil && !secretPath(path) {
			attrs = append(attrs, bodyAttr(body))
		}
		c.log.Debug("request", attrs...)
	}
	return hc.Do(req)
}

// bodyAttr turns a body into a log attribute: JSON bodies stay structured so the scrubber can
// redact secret-named fields; anything else is a truncated string.
func bodyAttr(b []byte) slog.Attr {
	if json.Valid(b) {
		return slog.Any("body", json.RawMessage(b))
	}
	const max = 2048
	s := string(b)
	if len(s) > max {
		s = s[:max] + "..."
	}
	return slog.String("body", s)
}
