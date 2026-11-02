// Package api is the agent's HTTP server (agent.md sections 3.7 to 3.11): one http.Handler served
// on TCP 127.0.0.1:<ports.agent> and on the unix socket /run/localai/agent.sock, behind the
// middleware chain of section 3.9.1, with the JSON error envelope, strict JSON decoding, the route
// registry later issues plug their handlers into, and GET /health.
//
// The server is an app.Worker (Name, Start, Stop). It owns no domain logic: routes arrive through a
// Registry, and authentication through the Authenticator seam (AGENT-06).
//
// Offline first: the package uses the standard library only, binds loopback only, and loads nothing
// from the network, so the Portal and POST /mode/survival work with the cable pulled.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// ConfigSource is what the server reads from the config store (*config.Store implements it): the
// agent port, and ui.language for the language of error messages.
type ConfigSource interface{ Get() config.Config }

// Options configures a Server. Every field is optional.
type Options struct {
	// Config supplies ports.agent (restart-only, read once) and ui.language (read per error, so a
	// hot reload applies). Nil: Port or the canonical port 7070, English.
	Config ConfigSource
	// Port overrides ports.agent. Zero means use Config, then config.DefaultAgentPort.
	Port int
	// TCPAddr is the address to bind, default 127.0.0.1:<port>. It must be a loopback address.
	// Tests pass 127.0.0.1:0; the Host and Origin checks then follow the port actually bound.
	TCPAddr string
	// SocketPath is the unix socket, default DefaultSocketPath. SocketGroup owns it, default
	// DefaultSocketGroup.
	SocketPath  string
	SocketGroup string
	// Version is reported by GET /health (app.Version; the api package cannot import app).
	Version string
	// Mode returns the current mode name for GET /health, from memory (state store snapshot).
	Mode func() string
	// Auth authenticates requests (steps 5 to 7). Nil means DenyAll: only routes at level none work.
	Auth Authenticator
	// Registry holds the routes. Nil means DefaultRegistry.
	Registry *Registry
	// Logger receives the access log and the lifecycle messages; the server adds component=api.
	// Nil: slog.Default.
	Logger *slog.Logger
	// Hooks replace the operating-system calls of the socket setup, for tests.
	Hooks Hooks
}

// Server is the agent's HTTP server. Create it with New; it implements app.Worker.
type Server struct {
	opts Options
	log  *slog.Logger
	auth Authenticator
	reg  *Registry

	port      atomic.Int32 // the port Host and Origin must name: ports.agent, or the port actually bound
	mux       *http.ServeMux
	methodMux map[string]*http.ServeMux // one mux per method, to tell 405 from 404
	handler   http.Handler

	mutationsOpen atomic.Bool // the mutating-route gate; closed until startup step 12
	starting      atomic.Bool // the /health starting flag; true until startup step 17
	stopping      atomic.Bool
	stopCh        chan struct{}
	stopOnce      sync.Once

	mu      sync.Mutex
	started bool
	tcpLn   net.Listener
	sockLn  *unixSocket
	servers []*http.Server
	tcpErr  *BindError
	sockErr *BindError
}

// New builds the server: it validates the routes, builds the mux and the middleware chain. It does
// not listen; Start does. The gate starts closed and the starting flag set, as at process start.
func New(opts Options) (*Server, error) {
	s := &Server{opts: opts, stopCh: make(chan struct{})}
	s.log = opts.Logger
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "api")
	if s.opts.Version == "" {
		s.opts.Version = "0.0.0-dev"
	}
	s.auth = opts.Auth
	if s.auth == nil {
		s.auth = DenyAll{}
	}
	s.reg = opts.Registry
	if s.reg == nil {
		s.reg = DefaultRegistry
	}
	if s.opts.SocketPath == "" {
		s.opts.SocketPath = DefaultSocketPath
	}
	if s.opts.SocketGroup == "" {
		s.opts.SocketGroup = DefaultSocketGroup
	}
	port := opts.Port
	if port == 0 && opts.Config != nil {
		port = opts.Config.Get().Ports.Agent
	}
	if port == 0 {
		port = config.DefaultAgentPort
	}
	s.port.Store(int32(port))
	s.starting.Store(true)
	if err := s.buildRoutes(); err != nil {
		return nil, err
	}
	var h http.Handler = s.mux
	h = s.matchGuard(h)  // step 4
	h = s.originCheck(h) // step 3
	h = s.hostCheck(h)   // step 2
	h = s.observe(h)     // access log, panic recovery
	h = s.requestID(h)   // step 1
	s.handler = h
	return s, nil
}

// buildRoutes registers the built-in routes and the registry's on the mux. A pattern that
// net/http rejects (a conflict between two patterns) is returned as an error.
func (s *Server) buildRoutes() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("api: bad route table: %v", r)
		}
	}()
	s.mux = http.NewServeMux()
	s.methodMux = map[string]*http.ServeMux{}
	add := func(rt Route) {
		pattern := rt.Path()
		// An absolute "/" route is the SPA catch-all (agent.md section 3.11): it must never answer
		// under /api/, where an unknown path is 404 LAI-AGT-021 and a known path with another
		// method is 405, exactly as when no catch-all is registered.
		catchAll := rt.Absolute && rt.Pattern == "/"
		s.mux.Handle(rt.Method+" "+pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if catchAll && (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) {
				s.unmatched(w, r)
				return
			}
			s.serveRoute(&rt, w, r)
		}))
		m := s.methodMux[rt.Method]
		if m == nil {
			m = http.NewServeMux()
			s.methodMux[rt.Method] = m
		}
		m.Handle(pattern, http.NotFoundHandler()) // only ever matched, never served
	}
	for _, rt := range BuiltinRoutes() {
		rt.Handler = s.healthHandler()
		add(rt)
	}
	for _, rt := range s.reg.Routes() {
		add(rt)
	}
	// Everything else: 405 or 404 as an envelope, never ServeMux's plain text.
	s.mux.Handle("/", http.HandlerFunc(s.unmatched))
	return nil
}

// Routes returns every route this server serves: the built-in ones and the registry's.
func (s *Server) Routes() []Route { return allRoutes(s.reg) }

// Handler returns the full middleware chain. A request is TCP unless its context says otherwise
// (WithTransport), so the TCP-only checks fail closed.
func (s *Server) Handler() http.Handler { return s.handler }

// Name implements app.Worker.
func (s *Server) Name() string { return "api" }

func (s *Server) agentPort() int { return int(s.port.Load()) }

func (s *Server) uiLanguage() string {
	if s.opts.Config == nil {
		return ""
	}
	return s.opts.Config.Get().UI.Language
}

// MutationsOpen reports whether mutating routes are served: false before SetMutationsOpen(true)
// (startup step 12) and from the moment Stop begins.
func (s *Server) MutationsOpen() bool { return s.mutationsOpen.Load() && !s.stopping.Load() }

// SetMutationsOpen opens or closes the mutating-route gate. While closed, a mutating route
// answers 503 LAI-AGT-024 with Retry-After: 5, except routes with GateExempt. AGENT-20 opens it
// when startup step 12 (llama reconciled) ends, success or failure.
func (s *Server) SetMutationsOpen(open bool) { s.mutationsOpen.Store(open) }

// Starting reports the /health starting flag.
func (s *Server) Starting() bool { return s.starting.Load() }

// SetStarting sets the /health starting flag. It is true from process start; AGENT-20 clears it at
// startup step 17. It is independent of the gate: between steps 12 and 17 mutations are accepted
// while starting is still true (agent.md section 3.8.1).
func (s *Server) SetStarting(starting bool) { s.starting.Store(starting) }

// Stopping returns a channel closed when Stop begins, for long-lived handlers (SSE) that must say
// goodbye and return so shutdown can finish.
func (s *Server) Stopping() <-chan struct{} { return s.stopCh }

// TCPAddr returns the address the TCP listener is bound to, or nil when it is not.
func (s *Server) TCPAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tcpLn == nil {
		return nil
	}
	return s.tcpLn.Addr()
}

// SocketPath returns the path of the unix socket, whether or not it is bound.
func (s *Server) SocketPath() string { return s.opts.SocketPath }

// BindErrors returns the bind errors of the two listeners, nil for one that started. AGENT-11
// raises LAI-AGT-006 and LAI-AGT-005 from them.
func (s *Server) BindErrors() (tcp, socket *BindError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tcpErr, s.sockErr
}

// Start binds both listeners and serves (agent.md section 4.1 step 10). The socket never depends on
// the TCP listener: when port 7070 is taken the socket still serves, so aictl keeps working.
// Start returns nil if at least one listener is up (see BindErrors for the other) and a
// *ListenError only when both failed, which is fatal for the agent. The context is not used for
// request handling; Stop ends the server.
func (s *Server) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("api: server already started")
	}
	s.started = true

	addr := s.opts.TCPAddr
	if addr == "" {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(s.agentPort()))
	}
	if ln, berr := listenTCP(addr); berr != nil {
		s.tcpErr = berr
		s.log.Error("tcp listener failed", "code", berr.Code, "error", berr.Err.Error(), "addr", addr)
	} else {
		s.tcpLn = ln
		s.port.Store(int32(ln.Addr().(*net.TCPAddr).Port)) // equal to ports.agent unless a test asked for port 0
		s.serve(ln, TransportTCP)
	}
	if ln, berr := listenSocket(s.opts.SocketPath, s.opts.SocketGroup, s.opts.Hooks); berr != nil {
		s.sockErr = berr
		s.log.Error("socket listener failed", "code", berr.Code, "error", berr.Err.Error(), "path", s.opts.SocketPath)
	} else {
		s.sockLn = ln
		s.serve(ln, TransportSocket)
	}
	if s.tcpErr != nil && s.sockErr != nil {
		return &ListenError{TCP: s.tcpErr, Socket: s.sockErr}
	}
	return nil
}

// serve starts one http.Server over ln. Both share the same handler; ConnContext records the
// transport and, for the socket, the peer credentials of each connection.
func (s *Server) serve(ln net.Listener, t Transport) {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if t != TransportSocket {
				return WithTransport(ctx, t, nil)
			}
			cred, err := peerCred(c)
			if err != nil {
				s.log.Warn("cannot read socket peer credentials", "error", err.Error())
				return WithTransport(ctx, t, nil) // no peer: the auth hook refuses the request
			}
			return WithTransport(ctx, t, &cred)
		},
	}
	s.servers = append(s.servers, srv)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("listener stopped", "transport", string(t), "error", err.Error())
		}
	}()
	s.log.Info("listening", "transport", string(t), "addr", ln.Addr().String())
}

// Stop implements app.Worker: it closes the gate (new mutating requests get 503), signals Stopping,
// stops accepting, lets in-flight requests finish until ctx ends, then closes what is left and
// removes the socket file (agent.md section 4.12 step 7). It is safe to call twice.
func (s *Server) Stop(ctx context.Context) error {
	s.stopping.Store(true)
	s.stopOnce.Do(func() { close(s.stopCh) })

	s.mu.Lock()
	servers := s.servers
	s.servers = nil
	s.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, len(servers))
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Shutdown(ctx); err != nil {
				_ = srv.Close() // the budget is spent: cut the stragglers
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	s.mu.Lock()
	if s.sockLn != nil {
		_ = s.sockLn.Close() // already closed by Shutdown; makes sure the file is gone
	}
	s.mu.Unlock()
	return errors.Join(errs...)
}
