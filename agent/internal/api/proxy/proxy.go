// Package proxy is the reverse-proxy helper behind /chat/, /library/ and the other loopback mounts
// of agent.md section 3.11. A mount is one proxy.New(Options) handler registered as an api.Route.
//
// The rules every mount gets, whatever its options:
//   - the upstream is a fixed loopback address (127.0.0.1:<port>); New panics on anything else, and
//     the connection is dialled to that address whatever the request says, so no header, path or
//     redirect can point the proxy at another machine;
//   - Host is the upstream's; the client's Host, X-Forwarded-*, Forwarded, X-Real-Ip, Via and every
//     hop-by-hop header (including those named in Connection) are dropped;
//   - the client's credentials never reach the upstream: Authorization, Proxy-Authorization,
//     X-Api-Key and Cookie are always removed (the agent's own session cookies must not leak to a
//     service that has no business with them). A mount that must authenticate to the upstream adds
//     the credential itself through InjectHeaders;
//   - responses stream: FlushInterval is -1, so every chunk (server-sent events, tokens) is written
//     the moment it arrives; WebSocket upgrades are passed through;
//   - the upstream's own HTTP proxy settings are never read, and compression is passed through
//     untouched.
//
// Nothing in this package logs a header, a header value or a request body, so a key added by
// InjectHeaders cannot reach a log through it.
//
// Offline first: the proxy talks to loopback only.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// Defaults of a mount that does not set its own (agent.md section 3.11).
const (
	// DefaultBodyLimit is the request body limit: 1 MiB.
	DefaultBodyLimit int64 = 1 << 20
	// DefaultResponseTimeout is the total time one exchange may take: 600 s.
	DefaultResponseTimeout = 600 * time.Second
)

// Decision is what a PathPolicy decides about a request.
type Decision int

// The decisions.
const (
	// Allow forwards the request to the upstream.
	Allow Decision = iota
	// Deny answers 403 with the doctor code the policy returned.
	Deny
	// Intercept serves the request with the handler the policy returned instead of forwarding it.
	Intercept
)

// Options configures one mount.
type Options struct {
	// Target is the upstream, "http://127.0.0.1:<port>" (a base path is allowed and is prepended to
	// every request path). New panics unless the scheme is http and the host is 127.0.0.1 with a
	// port: no LAN address, no DNS name, no IPv6, no user info.
	Target *url.URL
	// StripPrefix is removed from the start of the request path before it is forwarded
	// ("/chat": /chat/props becomes /props, /chat becomes /). Empty keeps the path as it is, which
	// is what /library/ needs (kiwix-serve runs with --urlRootLocation=/library).
	StripPrefix string
	// SetForwardedPrefix adds X-Forwarded-Prefix: <StripPrefix> to the upstream request.
	SetForwardedPrefix bool
	// StripRequestHeaders are removed from the client's request in addition to the ones every
	// mount drops (see the package comment).
	StripRequestHeaders []string
	// InjectHeaders runs last on the upstream request, after all stripping: it sets the headers the
	// mount adds (chat: Authorization: Bearer <key>). The proxy never logs what it sets.
	InjectHeaders func(*http.Request)
	// BodyLimit is the largest request body, in bytes: 0 means DefaultBodyLimit. A request that
	// declares more, or sends more, is answered 413 and never reaches the upstream in full.
	// The mount's api.Route.MaxBody must be at least as large, or the route's limit applies first.
	BodyLimit int64
	// ResponseTimeout is the most one whole exchange may take, headers and body: 0 means
	// DefaultResponseTimeout, a negative value means no total cap (streams). A WebSocket
	// connection is not capped once upgraded. The mount's api.Route.Timeout must be 0, or the
	// route's deadline cuts the stream.
	ResponseTimeout time.Duration
	// IdleTimeout is the most the exchange may go without a byte moving in either direction,
	// before the response starts and while it streams: 0 means no idle limit (chat: 10 minutes).
	IdleTimeout time.Duration
	// MethodPolicy refuses a request by its method: a non-nil error is answered with api.WriteError
	// (return an *api.Error such as ReadOnly's). It runs before PathPolicy.
	MethodPolicy func(*http.Request) error
	// PathPolicy decides per request. It receives a copy of the request whose URL.Path is the path
	// the upstream would see (StripPrefix removed), so a policy table can be written in the
	// upstream's own paths. For Intercept the handler is served with the original request, with
	// its body intact and limited. For Deny the string is the doctor code of the 403.
	PathPolicy func(*http.Request) (Decision, http.Handler, string)
	// ResponseHook runs on every upstream response after the helper's own header rules (nosniff, frame
	// headers) and before the response is written; /library/ uses it to add a Content-Security-Policy
	// to HTML. It must not read or replace the body.
	ResponseHook func(*http.Response)
	// DropSetCookie removes Set-Cookie from the upstream's responses.
	DropSetCookie bool
	// FrameHeaders adds X-Frame-Options: SAMEORIGIN and Referrer-Policy: no-referrer to responses
	// (the chat mount). X-Content-Type-Options: nosniff is always added; the upstream's own
	// Content-Security-Policy is always kept.
	FrameHeaders bool
	// DownStatus is the status when the upstream is down: 502 (the default) or 503.
	DownStatus int
	// DownCode is the doctor code of the answer when the upstream is down or too slow; empty means
	// api.CodeRouterDown, the code for llama. Chat passes api.CodeChatDown, library
	// api.CodeLibraryDown.
	DownCode string
	// DownLinks chooses the buttons of the page shown when the upstream is down or too slow (nil: the
	// default back-to-the-Portal button). It sees the request, so the labels follow its language.
	DownLinks func(r *http.Request) []api.PageLink
	// Logger receives the warnings of failed exchanges. Nil: slog.Default.
	Logger *slog.Logger
}

// ReadOnly is a MethodPolicy that allows GET and HEAD only, as /library/ and /maps/ do: any other
// method is 405 with Allow: GET, HEAD.
func ReadOnly(r *http.Request) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	e := api.NewError(api.CodeInvalidRequest, http.StatusMethodNotAllowed, map[string]any{"reason": "method_not_allowed", "allow": []string{"GET", "HEAD"}})
	e.Header = http.Header{"Allow": []string{"GET, HEAD"}}
	return e
}

// Causes the watchdog gives to a cancelled exchange.
var (
	errTotalTimeout = errors.New("proxy: response timeout")
	errIdleTimeout  = errors.New("proxy: idle timeout")
)

type proxy struct {
	opts       Options
	host       string // 127.0.0.1:<port>
	basePath   string // Target.Path without a trailing slash, "" for none
	prefix     string // StripPrefix without a trailing slash
	bodyLimit  int64
	total      time.Duration // 0: no cap
	idle       time.Duration // 0: no limit
	downCode   string
	downLinks  func(r *http.Request) []api.PageLink
	downStatus int
	log        *slog.Logger
	rp         *httputil.ReverseProxy
	stripReq   []string
}

type watchKey struct{}

// New returns the handler of one mount. It panics, like http.Handle, when the options are a
// programming error: a Target that is not http://127.0.0.1:<port>, an unusable StripPrefix, or a
// DownStatus other than 0, 502 and 503.
func New(opts Options) http.Handler {
	p := &proxy{opts: opts}
	t := opts.Target
	switch {
	case t == nil:
		panic("proxy: Target is required")
	case t.Scheme != "http":
		panic(fmt.Sprintf("proxy: Target scheme must be http, got %q", t.Scheme))
	case t.User != nil, t.RawQuery != "", t.Fragment != "":
		panic("proxy: Target must carry no user info, query or fragment")
	}
	host, port, err := net.SplitHostPort(t.Host)
	if err != nil || host != "127.0.0.1" {
		panic(fmt.Sprintf("proxy: Target host must be 127.0.0.1:<port>, got %q", t.Host))
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		panic(fmt.Sprintf("proxy: Target port is not valid: %q", port))
	}
	p.host = t.Host
	p.basePath = strings.TrimSuffix(t.EscapedPath(), "/")
	if sp := opts.StripPrefix; sp != "" {
		if !strings.HasPrefix(sp, "/") || sp == "/" || strings.ContainsAny(sp, "%?#") {
			panic(fmt.Sprintf("proxy: StripPrefix must be a path such as /chat, got %q", sp))
		}
		p.prefix = strings.TrimSuffix(sp, "/")
	}
	if opts.SetForwardedPrefix && p.prefix == "" {
		panic("proxy: SetForwardedPrefix needs a StripPrefix")
	}
	switch opts.DownStatus {
	case 0, http.StatusBadGateway, http.StatusServiceUnavailable:
	default:
		panic(fmt.Sprintf("proxy: DownStatus must be 502 or 503, got %d", opts.DownStatus))
	}
	p.downStatus = opts.DownStatus
	if p.downStatus == 0 {
		p.downStatus = http.StatusBadGateway
	}
	p.downCode = opts.DownCode
	p.downLinks = opts.DownLinks
	if p.downCode == "" {
		p.downCode = api.CodeRouterDown
	}
	p.bodyLimit = opts.BodyLimit
	if p.bodyLimit <= 0 {
		p.bodyLimit = DefaultBodyLimit
	}
	switch {
	case opts.ResponseTimeout == 0:
		p.total = DefaultResponseTimeout
	case opts.ResponseTimeout > 0:
		p.total = opts.ResponseTimeout
	}
	if opts.IdleTimeout > 0 {
		p.idle = opts.IdleTimeout
	}
	p.log = opts.Logger
	if p.log == nil {
		p.log = slog.Default()
	}
	p.log = p.log.With("component", "proxy")
	for _, h := range opts.StripRequestHeaders {
		p.stripReq = append(p.stripReq, http.CanonicalHeaderKey(h))
	}

	dial := netx.DialFor("api.proxy", 5*time.Second)
	pinned := p.host
	tr := netx.NewTransport("api.proxy", func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Whatever the request says, the connection goes to the pinned address.
		return dial(ctx, "tcp4", pinned)
	})
	tr.DisableCompression = true // pass the client's Accept-Encoding and the answer through
	tr.MaxIdleConnsPerHost = 8
	tr.IdleConnTimeout = 90 * time.Second
	tr.ExpectContinueTimeout = time.Second
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		FlushInterval:  -1,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
		ErrorLog:       slog.NewLogLogger(p.log.Handler(), slog.LevelWarn),
		Transport:      tr, // no proxy environment: the upstream is on this machine
	}
	return p
}

// ServeHTTP implements http.Handler.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.opts.MethodPolicy != nil {
		if err := p.opts.MethodPolicy(r); err != nil {
			api.WriteErrorPage(w, r, err)
			return
		}
	}
	if !p.pathOK(r) {
		api.WriteErrorPage(w, r, api.NewError(api.CodeNotFound, http.StatusNotFound, map[string]any{"reason": "unknown_route"}))
		return
	}
	if r.ContentLength > p.bodyLimit {
		api.WriteErrorPage(w, r, api.NewError(api.CodeTooLarge, http.StatusRequestEntityTooLarge, map[string]any{"limit": p.bodyLimit}))
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(w, r.Body, p.bodyLimit)
	}
	if p.opts.PathPolicy != nil {
		view := r.Clone(r.Context())
		view.URL.Path, view.URL.RawPath = p.upstreamPath(r)
		switch d, h, code := p.opts.PathPolicy(view); d {
		case Deny:
			if code == "" {
				code = api.CodeInvalidRequest
			}
			api.WriteErrorPage(w, r, api.NewError(code, http.StatusForbidden, map[string]any{"reason": "path_not_allowed"}))
			return
		case Intercept:
			if h == nil {
				api.WriteErrorPage(w, r, api.NewError(api.CodeInternal, http.StatusInternalServerError, map[string]any{"reason": "no_intercept_handler"}))
				return
			}
			h.ServeHTTP(w, r)
			return
		}
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	wd := &watchdog{cancel: cancel, idle: p.idle}
	if p.total > 0 {
		wd.totalT = time.AfterFunc(p.total, func() { cancel(errTotalTimeout) })
	}
	if p.idle > 0 {
		wd.idleT = time.AfterFunc(p.idle, func() { cancel(errIdleTimeout) })
	}
	defer wd.stop()
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &touchBody{ReadCloser: r.Body, wd: wd}
	}
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(ctx, watchKey{}, wd)))
}

// pathOK rejects a request path the proxy will not forward: one that does not start with the mount's
// prefix, or that carries a ".." segment or a NUL once decoded (the server's own guard has already
// refused unclean paths; this keeps the helper safe on its own).
func (p *proxy) pathOK(r *http.Request) bool {
	path := r.URL.Path
	if p.prefix != "" {
		// Both forms must start with the prefix on a segment boundary: "/chat%2Fprops" decodes to
		// "/chat/props" but is not under /chat/.
		for _, form := range []string{path, r.URL.EscapedPath()} {
			if form != p.prefix && !strings.HasPrefix(form, p.prefix+"/") {
				return false
			}
		}
	}
	if strings.ContainsRune(path, 0) {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// upstreamPath returns the decoded and the escaped path the upstream sees: the request path with
// StripPrefix removed (a bare prefix becomes "/") under the Target's base path.
func (p *proxy) upstreamPath(r *http.Request) (path, raw string) {
	path, raw = r.URL.Path, r.URL.EscapedPath()
	if p.prefix != "" {
		path = strings.TrimPrefix(path, p.prefix)
		raw = strings.TrimPrefix(raw, p.prefix)
	}
	if path == "" {
		path = "/"
	}
	if raw == "" {
		raw = "/"
	}
	if p.basePath != "" {
		base, _ := url.PathUnescape(p.basePath)
		path, raw = base+path, p.basePath+raw
	}
	return path, raw
}

// hopByHop are the headers that describe one connection, not the message (RFC 9110 section 7.6.1).
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// clientCredentials never travel to an upstream: the proxy is not a way to hand the agent's session
// or an operator's token to another program.
var clientCredentials = []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie"}

// rewrite builds the upstream request from the client's.
func (p *proxy) rewrite(pr *httputil.ProxyRequest) {
	in, out := pr.In, pr.Out
	out.URL.Scheme = "http"
	out.URL.Host = p.host
	out.URL.User = nil
	out.URL.Path, out.URL.RawPath = p.upstreamPath(in)
	out.URL.RawQuery = in.URL.RawQuery
	out.URL.ForceQuery = false
	out.Host = p.host // never the client's Host

	h := out.Header
	upgrade := websocketUpgrade(h)
	for _, v := range h.Values("Connection") { // headers named in Connection are hop-by-hop too
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
	for _, name := range []string{"Forwarded", "X-Real-Ip", "Via"} {
		h.Del(name)
	}
	for name := range h {
		if strings.HasPrefix(name, "X-Forwarded-") {
			delete(h, name)
		}
	}
	for _, name := range clientCredentials {
		h.Del(name)
	}
	for _, name := range p.stripReq {
		h.Del(name)
	}
	if upgrade != "" {
		h.Set("Connection", "Upgrade")
		h.Set("Upgrade", upgrade)
	}
	if p.opts.SetForwardedPrefix {
		h.Set("X-Forwarded-Prefix", p.prefix)
	}
	if p.opts.InjectHeaders != nil {
		p.opts.InjectHeaders(out)
	}
}

// websocketUpgrade returns "websocket" when the request asks for a WebSocket upgrade, "" otherwise.
// Any other Upgrade token (h2c, ...) is dropped with the other hop-by-hop headers.
func websocketUpgrade(h http.Header) string {
	connUpgrade := false
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				connUpgrade = true
			}
		}
	}
	if connUpgrade && strings.EqualFold(strings.TrimSpace(h.Get("Upgrade")), "websocket") {
		return "websocket"
	}
	return ""
}

// modifyResponse applies the response rules and arms the idle watch on the body.
func (p *proxy) modifyResponse(resp *http.Response) error {
	wd, _ := resp.Request.Context().Value(watchKey{}).(*watchdog)
	h := resp.Header
	if p.opts.DropSetCookie {
		h.Del("Set-Cookie")
	}
	h.Set("X-Content-Type-Options", "nosniff")
	if p.opts.FrameHeaders {
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
	}
	if loc := h.Get("Location"); loc != "" {
		h.Set("Location", p.rewriteLocation(loc))
	}
	if p.opts.ResponseHook != nil {
		p.opts.ResponseHook(resp)
	}
	if wd != nil {
		if resp.StatusCode == http.StatusSwitchingProtocols {
			wd.stop() // an upgraded connection has no deadline; the Body must stay a ReadWriteCloser
		} else {
			wd.touch()
			resp.Body = &touchBody{ReadCloser: resp.Body, wd: wd}
		}
	}
	return nil
}

// rewriteLocation keeps a redirect inside the mount: a path or an absolute URL of the upstream gets
// the prefix back, so the client never sees (or follows) 127.0.0.1:<port>.
func (p *proxy) rewriteLocation(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return loc
	}
	if u.Host != "" && u.Host != p.host {
		return loc // a redirect to somewhere else is not ours to touch
	}
	if u.Host == "" && !strings.HasPrefix(u.Path, "/") {
		return loc // relative to the current page: it stays inside the mount by itself
	}
	if u.Host == "" && strings.HasPrefix(loc, "//") {
		return loc
	}
	u.Scheme, u.Host = "", ""
	if p.basePath != "" {
		base, _ := url.PathUnescape(p.basePath)
		u.Path = strings.TrimPrefix(u.Path, base)
		u.RawPath = strings.TrimPrefix(u.RawPath, p.basePath)
	}
	if p.prefix != "" {
		u.Path = p.prefix + u.Path
		if u.RawPath != "" {
			u.RawPath = p.prefix + u.RawPath
		}
	}
	return u.String()
}

// errorHandler answers a failed upstream exchange before any response byte was written.
func (p *proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	var tooBig *http.MaxBytesError
	cause := context.Cause(r.Context())
	switch {
	case errors.As(err, &tooBig):
		api.WriteErrorPage(w, r, api.NewError(api.CodeTooLarge, http.StatusRequestEntityTooLarge, map[string]any{"limit": tooBig.Limit}))
		return
	case errors.Is(cause, errTotalTimeout), errors.Is(cause, errIdleTimeout):
		reason := "upstream_timeout"
		if errors.Is(cause, errIdleTimeout) {
			reason = "upstream_idle"
		}
		p.logFailure(r, p.downCode, http.StatusGatewayTimeout, reason, err)
		api.WriteErrorPageLinks(w, r, api.NewError(p.downCode, http.StatusGatewayTimeout, map[string]any{"reason": reason}), p.links(r))
		return
	case r.Context().Err() != nil:
		return // the client went away; nobody is left to answer
	}
	p.logFailure(r, p.downCode, p.downStatus, "upstream_down", err)
	api.WriteErrorPageLinks(w, r, api.NewError(p.downCode, p.downStatus, map[string]any{"reason": "upstream_down"}), p.links(r))
}

// links is the button list of a down page: the caller's, or nil for the default.
func (p *proxy) links(r *http.Request) []api.PageLink {
	if p.downLinks == nil {
		return nil
	}
	return p.downLinks(r)
}

// logFailure writes one warning per failed exchange: the code, the request id, the method and the
// path. Never a header, never the query string (it can carry a token), never the body.
func (p *proxy) logFailure(r *http.Request, code string, status int, reason string, err error) {
	p.log.Warn("proxy.upstream_failed",
		"code", code, "http_status", status, "reason", reason, "req_id", api.RequestID(r),
		"method", r.Method, "path", r.URL.Path, "error", err.Error())
}

// watchdog enforces the total and idle limits of one exchange by cancelling its context with a cause.
type watchdog struct {
	cancel context.CancelCauseFunc
	idle   time.Duration
	totalT *time.Timer
	idleT  *time.Timer
}

// touch records progress: a byte moved, so the idle clock starts again.
func (w *watchdog) touch() {
	if w.idleT != nil {
		w.idleT.Reset(w.idle)
	}
}

// stop disarms both timers.
func (w *watchdog) stop() {
	if w.totalT != nil {
		w.totalT.Stop()
	}
	if w.idleT != nil {
		w.idleT.Stop()
	}
}

// touchBody resets the idle clock on every read.
type touchBody struct {
	io.ReadCloser
	wd *watchdog
}

func (b *touchBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.wd.touch()
	}
	return n, err
}
