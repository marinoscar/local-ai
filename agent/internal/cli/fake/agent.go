// Package fake is a stand-in for the agent for the aictl tests: the same handler answers on a unix
// socket in a temporary directory and on an httptest TCP server, records every request (which
// transport it came on, the Host and Authorization headers, the body), and answers with the agent's
// error envelope. It imports "testing", so only test code may import it.
package fake

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// Request is one request the fake saw.
type Request struct {
	Via    string // "socket" or "tcp"
	Method string
	Path   string // relative to /api/v1, with the query
	Host   string
	Header http.Header
	Body   string
}

// Options configure New.
type Options struct {
	// Version is what GET /health reports (default "0.0.0-dev", the CLI's default).
	Version string
	// NoSocket and NoTCP leave that listener out, so the transport is down.
	NoSocket, NoTCP bool
}

// Agent is the fake. Its handlers may be set and replaced while it runs.
type Agent struct {
	// SocketPath is the unix socket path (a path that does not exist when NoSocket is set).
	SocketPath string
	// URL is the TCP base URL, http://127.0.0.1:<port> (an unused loopback port when NoTCP is set).
	URL string

	mu     sync.Mutex
	reqs   []Request
	routes map[string]http.HandlerFunc
	health struct {
		version, mode string
		starting      bool
	}
	sockLn net.Listener
	tcp    *httptest.Server
	dir    string
}

// New starts the fake and registers its cleanup with t.
func New(t testing.TB, o Options) *Agent {
	t.Helper()
	a := &Agent{routes: map[string]http.HandlerFunc{}}
	a.health.version, a.health.mode = o.Version, "online"
	if a.health.version == "" {
		a.health.version = "0.0.0-dev"
	}
	// Unix socket paths are limited to about 100 bytes; t.TempDir can be longer.
	dir, err := os.MkdirTemp("", "aictl")
	if err != nil {
		t.Fatal(err)
	}
	a.dir = dir
	t.Cleanup(func() { a.Close(); os.RemoveAll(dir) })
	a.SocketPath = filepath.Join(dir, "agent.sock")
	if !o.NoSocket {
		ln, err := net.Listen("unix", a.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		a.sockLn = ln
		go (&http.Server{Handler: a.handler("socket")}).Serve(ln)
	}
	if !o.NoTCP {
		a.tcp = httptest.NewServer(a.handler("tcp"))
		a.URL = a.tcp.URL
	} else {
		// A port that is almost surely closed: bind it and release it.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		a.URL = "http://" + ln.Addr().String()
		ln.Close()
	}
	return a
}

// Close stops both listeners. It is safe to call twice.
func (a *Agent) Close() {
	a.CloseSocket()
	a.mu.Lock()
	tcp := a.tcp
	a.tcp = nil
	a.mu.Unlock()
	if tcp != nil {
		tcp.Close()
	}
}

// CloseSocket stops the unix listener only (the agent is then reachable over TCP alone).
func (a *Agent) CloseSocket() {
	a.mu.Lock()
	ln := a.sockLn
	a.sockLn = nil
	a.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// SetHealth sets what GET /health answers.
func (a *Agent) SetHealth(version, mode string, starting bool) {
	a.mu.Lock()
	a.health.version, a.health.mode, a.health.starting = version, mode, starting
	a.mu.Unlock()
}

// Handle sets the handler of method and path (relative to /api/v1).
func (a *Agent) Handle(method, path string, h http.HandlerFunc) {
	a.mu.Lock()
	a.routes[method+" "+path] = h
	a.mu.Unlock()
}

// Respond answers method and path with status and a JSON body.
func (a *Agent) Respond(method, path string, status int, body string) {
	a.Handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

// RespondError answers method and path with status and the agent's error envelope.
func (a *Agent) RespondError(method, path string, status int, code, message, fix string, details map[string]any) {
	a.Handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		WriteEnvelope(w, status, code, message, fix, details)
	})
}

// WriteEnvelope writes the agent's error envelope (agent.md section 3.7).
func WriteEnvelope(w http.ResponseWriter, status int, code, message, fix string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "fix": fix, "details": details,
	}})
}

// Requests returns a copy of the requests seen so far.
func (a *Agent) Requests() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Request(nil), a.reqs...)
}

// Count returns how many requests for path (relative to /api/v1) were seen.
func (a *Agent) Count(path string) int {
	n := 0
	for _, r := range a.Requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

func (a *Agent) handler(via string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body)) // handlers may read the body again
		rel := strings.TrimPrefix(r.URL.RequestURI(), api.BasePath)
		a.mu.Lock()
		a.reqs = append(a.reqs, Request{Via: via, Method: r.Method, Path: rel, Host: r.Host, Header: r.Header.Clone(), Body: string(body)})
		h := a.routes[r.Method+" "+strings.TrimPrefix(r.URL.Path, api.BasePath)]
		health := a.health
		a.mu.Unlock()

		w.Header().Set("X-Request-Id", "req_ABCDEFGHJKLM")
		switch {
		case h != nil:
			h(w, r)
		case r.Method == http.MethodGet && r.URL.Path == api.BasePath+"/health":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(api.Health{OK: true, Version: health.version, Mode: health.mode, Starting: health.starting})
		default:
			WriteEnvelope(w, http.StatusNotFound, api.CodeNotFound, "Not found.", "Check the path.", nil)
		}
	})
}
