package throttle

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ProxyOptions tunes StartProxyWith. The zero value is what StartProxy uses.
type ProxyOptions struct {
	// Dial connects to the target; default a net.Dialer with DialTimeout. Tests replace it so no
	// real host is needed.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// DialTimeout bounds the default dial; default 15 s.
	DialTimeout time.Duration
	// MaxConns is the most simultaneous tunnels; more are answered 503. Default 64.
	MaxConns int
	// Logger receives refusals (host only, never a path or header); default slog.Default.
	Logger *slog.Logger
}

// proxyChunk is the size of one relayed read: small enough that the bucket's rate is smooth.
const proxyChunk = 32 << 10

// StartProxy starts the throttling CONNECT proxy of ark-sync.md section 4.7 on 127.0.0.1 with an
// ephemeral port and returns its address ("127.0.0.1:41234"). The caller hands
// "http://"+addr to the child as HTTPS_PROXY (for git: -c http.proxy=...). It
//
//   - accepts only CONNECT host:443 where host is in allowHosts (compared in lower case, with or
//     without ":443"); every other request, method, port or host is answered 403;
//   - relays the bytes from the target to the client through the bucket, so the child shares the
//     limit with the in-process downloads;
//   - dies with the subprocess: stop closes the listener and every tunnel, and so does ctx.
//
// With an unlimited bucket (nil, Unlimited, or a TokenBucket whose rate is 0) no proxy is started:
// addr is "" and stop does nothing, and the child runs direct. stop may be called more than once.
func StartProxy(ctx context.Context, b Bucket, allowHosts []string) (addr string, stop func(), err error) {
	return StartProxyWith(ctx, b, allowHosts, ProxyOptions{})
}

// StartProxyWith is StartProxy with options.
func StartProxyWith(ctx context.Context, b Bucket, allowHosts []string, opts ProxyOptions) (string, func(), error) {
	if b == nil {
		return "", func() {}, nil
	}
	if u, ok := b.(interface{ Unlimited() bool }); ok && u.Unlimited() {
		return "", func() {}, nil
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = 64
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Dial == nil {
		d := &net.Dialer{Timeout: opts.DialTimeout}
		opts.Dial = d.DialContext
	}
	allow := map[string]bool{}
	for _, h := range allowHosts {
		if h = normHost(h); h != "" {
			allow[h] = true
		}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &proxy{b: b, allow: allow, opts: opts, ctx: pctx, slots: make(chan struct{}, opts.MaxConns), conns: map[net.Conn]struct{}{}}
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = p.srv.Close()
			p.closeConns()
		})
	}
	go func() { _ = p.srv.Serve(ln) }()
	go func() { <-pctx.Done(); stop() }()
	return ln.Addr().String(), stop, nil
}

func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ":443")
	return strings.TrimSuffix(h, ".")
}

type proxy struct {
	b     Bucket
	allow map[string]bool
	opts  ProxyOptions
	ctx   context.Context
	srv   *http.Server
	slots chan struct{}

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func (p *proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

func (p *proxy) closeConns() {
	p.mu.Lock()
	p.closed = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *proxy) refuse(w http.ResponseWriter, host, why string) {
	p.opts.Logger.Warn("throttle proxy refused a request", "component", "sync", "host", host, "reason", why)
	http.Error(w, "forbidden", http.StatusForbidden)
}

// ServeHTTP implements http.Handler.
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.refuse(w, "", "method")
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		p.refuse(w, "", "port")
		return
	}
	host = normHost(host)
	if !p.allow[host] {
		p.refuse(w, host, "host not allowed")
		return
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	target, err := p.opts.Dial(p.ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = target.Close()
		http.Error(w, "unsupported", http.StatusInternalServerError)
		return
	}
	client, brw, err := hj.Hijack()
	if err != nil {
		_ = target.Close()
		return
	}
	if !p.track(client) || !p.track(target) {
		_ = client.Close()
		_ = target.Close()
		return
	}
	defer p.untrack(client)
	defer p.untrack(target)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = target.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // client -> target: requests are small, not throttled
		defer wg.Done()
		if n := brw.Reader.Buffered(); n > 0 {
			buf, _ := brw.Reader.Peek(n)
			_, _ = target.Write(buf)
		}
		_, _ = io.Copy(target, client)
		_ = target.Close()
		_ = client.Close()
	}()
	go func() { // target -> client: the downloads, drawn from the bucket
		defer wg.Done()
		p.relay(client, target)
		_ = target.Close()
		_ = client.Close()
	}()
	wg.Wait()
}

func (p *proxy) relay(dst io.Writer, src io.Reader) {
	buf := make([]byte, proxyChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if p.b.Wait(p.ctx, n) != nil {
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
