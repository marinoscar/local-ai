package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Route names, as Script and the request log use them.
const (
	RouteRegister   = "register"
	RouteHeartbeat  = "heartbeat"
	RouteDeregister = "deregister"
	RouteConfig     = "config"
	RouteState      = "state"
	RouteKey        = "key"
	RouteCatalog    = "catalog"
	RouteClaim      = "claim"
	RouteRenew      = "renew"
	RouteResult     = "result"
	RouteFailure    = "failure"
)

// Reply is a scripted answer. Status 0 means 200. A nil Body writes no body. Drop closes the
// connection without answering (a network failure for the client).
type Reply struct {
	Status int
	Body   any
	Header map[string]string
	Drop   bool
}

// Err builds the error answer of the cloud: EAB's error body with the Local AI code and reason in
// details (agent.md section 4.8.2: "reads our code from details.code on errors").
func Err(status int, code, reason string) Reply {
	return Reply{Status: status, Body: map[string]any{"error": map[string]any{
		"message": http.StatusText(status), "details": map[string]any{"code": code, "reason": reason},
	}}}
}

// Request is one request the server received.
type Request struct {
	Route      string
	Method     string
	Path       string
	Header     http.Header
	Body       []byte
	Credential string
}

// JSON decodes the request body.
func (r Request) JSON(v any) error { return json.Unmarshal(r.Body, v) }

// Node is a registered worker node as the server holds it.
type Node struct {
	ID            string
	Name          string
	Hostname      string
	EligibleTypes []string
	Concurrency   int
	Capabilities  json.RawMessage
	LastHeartbeat time.Time
	Status        string
	Deregistered  bool
}

// Handshake configures GET .../localai/config.
type Handshake struct {
	// Missing answers 404 (an older cloud without the Local AI node routes).
	Missing            bool
	APIVersion         int
	Features           []string
	CommandPlane       string
	HeartbeatIntervalS int
	StateMinIntervalS  int
	StateMaxIntervalS  int
	ClaimWaitMaxMs     int
	// KeyReported overrides the answer; nil reports whether a key is stored.
	KeyReported *bool
	// ServerTime overrides the clock the answer shows.
	ServerTime func() time.Time
}

// Server is the fake cloud: a TLS test server (use Client() as the http client of the code under test).
type Server struct {
	*httptest.Server

	mu      sync.Mutex
	changed chan struct{}

	// Now is the server's clock; default time.Now.
	Now func() time.Time
	// ServerFeatures is what register and heartbeat answer as serverFeatures.
	ServerFeatures []string
	// Handshake configures the config route.
	Handshake Handshake
	// StateEndpoint: the state route exists (default true); false answers 404.
	StateEndpoint bool
	// KeyEndpoint: the key route exists (default true).
	KeyEndpoint bool
	// RejectExtra answers 400 mentioning "extra" to a heartbeat whose vitals carry extra.
	RejectExtra bool
	// RejectVitals answers 400 (not mentioning extra) to a heartbeat that carries vitals.
	RejectVitals bool
	// Disabled answers 403 to claim (an operator disabled the node).
	Disabled bool
	// Catalog is served by the catalog route; nil answers 404 LAI-CLD-013.
	Catalog *Bundle

	creds    map[string]bool // accepted credentials; empty accepts any
	revoked  map[string]bool
	nodes    map[string]*Node // by name
	byID     map[string]*Node
	nextID   int
	script   map[string][]Reply
	forever  map[string]Reply
	reqs     []Request
	lastSeq  map[string]int64
	states   []json.RawMessage
	keys     map[string]string // node id -> key
	keyCount int
	cmds     commandState
	// holdClaims makes a claim without a job wait (a long poll); see HoldClaims.
	holdClaims bool
}

// Bundle is a catalog the fake serves.
type Bundle struct {
	Version    string
	ApprovedAt string
	Files      []BundleFile
}

// BundleFile is one file of a Bundle; SHA256 is computed when empty.
type BundleFile struct {
	Path    string
	Content string
	SHA256  string
}

// BundleSHA256 is the strong ETag of the bundle: the sha256 over the files' paths and hashes.
func (b *Bundle) BundleSHA256() string {
	h := sha256.New()
	for _, f := range b.Files {
		h.Write([]byte(f.Path + "\n" + f.hash() + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (f BundleFile) hash() string {
	if f.SHA256 != "" {
		return f.SHA256
	}
	sum := sha256.Sum256([]byte(f.Content))
	return hex.EncodeToString(sum[:])
}

// New starts the server and registers its cleanup.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		changed: make(chan struct{}), Now: time.Now, ServerFeatures: []string{"node_host_vitals"}, StateEndpoint: true, KeyEndpoint: true,
		Handshake: Handshake{APIVersion: 1, Features: []string{"state.v1", "commands.v1", "catalog.v1", "key.v1"}, CommandPlane: "localai",
			HeartbeatIntervalS: 15, StateMinIntervalS: 5, StateMaxIntervalS: 60, ClaimWaitMaxMs: 25000},
		creds: map[string]bool{}, revoked: map[string]bool{}, nodes: map[string]*Node{}, byID: map[string]*Node{},
		script: map[string][]Reply{}, forever: map[string]Reply{}, lastSeq: map[string]int64{}, keys: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/nodes/register", s.route(RouteRegister, s.register))
	mux.HandleFunc("POST /api/nodes/{id}/heartbeat", s.route(RouteHeartbeat, s.heartbeat))
	mux.HandleFunc("POST /api/nodes/{id}/deregister", s.route(RouteDeregister, s.deregister))
	mux.HandleFunc("GET /api/nodes/{id}/localai/config", s.route(RouteConfig, s.config))
	mux.HandleFunc("POST /api/nodes/{id}/localai/state", s.route(RouteState, s.state))
	mux.HandleFunc("POST /api/nodes/{id}/localai/key", s.route(RouteKey, s.key))
	mux.HandleFunc("GET /api/nodes/{id}/localai/catalog", s.route(RouteCatalog, s.catalog))
	s.cmdRoutes(mux)
	s.Server = httptest.NewTLSServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// Accept restricts the accepted credentials to cred (call it for each); without any call every
// credential is accepted.
func (s *Server) Accept(cred string) {
	s.mu.Lock()
	s.creds[cred] = true
	s.mu.Unlock()
}

// Revoke makes every route answer 401 to cred.
func (s *Server) Revoke(cred string) {
	s.mu.Lock()
	s.revoked[cred] = true
	s.mu.Unlock()
}

// Unrevoke undoes Revoke.
func (s *Server) Unrevoke(cred string) {
	s.mu.Lock()
	delete(s.revoked, cred)
	s.mu.Unlock()
}

// Script queues replies for a route; each request takes the next one, then the default behaviour
// resumes.
func (s *Server) Script(route string, replies ...Reply) {
	s.mu.Lock()
	s.script[route] = append(s.script[route], replies...)
	s.mu.Unlock()
}

// Forever makes a route answer r to every request (a cloud that stays down).
func (s *Server) Forever(route string, r Reply) {
	s.mu.Lock()
	s.forever[route] = r
	s.mu.Unlock()
}

// Clear removes the scripted and the standing replies of a route.
func (s *Server) Clear(route string) {
	s.mu.Lock()
	delete(s.script, route)
	delete(s.forever, route)
	s.mu.Unlock()
}

// Locked runs fn with the server's lock held, for tests that change its fields while it serves.
func (s *Server) Locked(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// RequestsFor returns the requests of one route.
func (s *Server) RequestsFor(route string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Route == route {
			out = append(out, r)
		}
	}
	return out
}

// Count is the number of requests of a route.
func (s *Server) Count(route string) int { return len(s.RequestsFor(route)) }

// WaitCount blocks until route has received at least n requests, or ctx ends.
func (s *Server) WaitCount(ctx context.Context, route string, n int) error {
	for {
		s.mu.Lock()
		cnt := 0
		for _, r := range s.reqs {
			if r.Route == route {
				cnt++
			}
		}
		ch := s.changed
		s.mu.Unlock()
		if cnt >= n {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("waiting for %d %s requests (got %d): %w", n, route, cnt, ctx.Err())
		}
	}
}

// NodeByName returns the registered node, false when there is none.
func (s *Server) NodeByName(name string) (Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[name]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

// DeleteNode removes a node row, as an admin or the prune job does: its id answers 404 from then on.
func (s *Server) DeleteNode(name string) {
	s.mu.Lock()
	if n, ok := s.nodes[name]; ok {
		delete(s.byID, n.ID)
		delete(s.nodes, name)
	}
	s.mu.Unlock()
}

// SetLastHeartbeat sets the last heartbeat time of a node row (to stage another machine using the id).
func (s *Server) SetLastHeartbeat(name, hostname string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[name]; ok {
		n.LastHeartbeat, n.Hostname = at, hostname
	}
}

// States returns the state snapshots accepted so far, as sent.
func (s *Server) States() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.states...)
}

// StoredKey returns the llama key the cloud holds for a node id (a test double of the credential
// store) and how many times a key was accepted.
func (s *Server) StoredKey(nodeID string) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[nodeID], s.keyCount
}

// ---------------------------------------------------------------------------------------------------
// Handlers

type handler func(w http.ResponseWriter, r *http.Request, body []byte) (status int, body2 any)

// route wraps a handler: it authenticates, records the request, applies scripted replies and writes
// the answer.
func (s *Server) route(name string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		unauthorized := cred == "" || s.revoked[cred] || (len(s.creds) > 0 && !s.creds[cred])
		var scripted *Reply
		if q := s.script[name]; len(q) > 0 {
			rp := q[0]
			s.script[name] = q[1:]
			scripted = &rp
		} else if rp, ok := s.forever[name]; ok {
			scripted = &rp
		}
		s.mu.Unlock()

		var reply Reply
		switch {
		case scripted != nil:
			reply = *scripted
		case unauthorized:
			reply = Reply{Status: 401, Body: map[string]any{"error": map[string]any{"message": "Unauthorized"}}}
		default:
			status, body := h(w, r, raw)
			reply = Reply{Status: status, Body: body}
		}
		// The request is logged once its handler has run and before the answer goes out: a test that
		// sees the client's call return, or the request in the log, also sees everything the handler did.
		s.mu.Lock()
		s.reqs = append(s.reqs, Request{Route: name, Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: raw, Credential: cred})
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
		s.write(w, reply)
	}
}

func (s *Server) write(w http.ResponseWriter, r Reply) {
	if r.Drop {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	for k, v := range r.Header {
		w.Header().Set(k, v)
	}
	status := r.Status
	if status == 0 {
		status = 200
	}
	if r.Body == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(r.Body)
}

func envelope(data any) map[string]any {
	return map[string]any{"data": data, "meta": map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339)}}
}

func (s *Server) node(r *http.Request) (*Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.byID[r.PathValue("id")]
	return n, ok
}

var errNoNode = map[string]any{"error": map[string]any{"message": "Not Found"}}

func (s *Server) register(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
	var b struct {
		Name          string          `json:"name"`
		Hostname      string          `json:"hostname"`
		Platform      string          `json:"platform"`
		CLIVersion    string          `json:"cliVersion"`
		EligibleTypes []string        `json:"eligibleTypes"`
		Concurrency   int             `json:"concurrency"`
		Capabilities  json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Name == "" || b.EligibleTypes == nil || b.Concurrency < 1 {
		return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"issues": []any{map[string]any{"path": []any{"name"}}}}}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, existed := s.nodes[b.Name]
	prevBeat, prevHost := time.Time{}, ""
	if !existed {
		s.nextID++
		n = &Node{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", s.nextID), Name: b.Name}
		s.nodes[b.Name], s.byID[n.ID] = n, n
	} else {
		prevBeat, prevHost = n.LastHeartbeat, n.Hostname
	}
	n.Hostname, n.EligibleTypes, n.Concurrency, n.Capabilities, n.Deregistered = b.Hostname, b.EligibleTypes, b.Concurrency, b.Capabilities, false
	node := map[string]any{"id": n.ID, "name": n.Name, "hostname": prevHost}
	if !prevBeat.IsZero() {
		node["lastHeartbeatAt"] = prevBeat.UTC().Format(time.RFC3339)
	}
	if prevHost == "" {
		node["hostname"] = b.Hostname
	}
	return 200, envelope(map[string]any{"node": node, "reattached": existed, "serverFeatures": s.ServerFeatures})
}

func (s *Server) heartbeat(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
	n, ok := s.node(r)
	if !ok {
		return 404, errNoNode
	}
	var b struct {
		Status string `json:"status"`
		Vitals *struct {
			Extra map[string]float64 `json:"extra"`
		} `json:"vitals"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Status == "" {
		return 400, map[string]any{"error": map[string]any{"message": "Bad Request"}}
	}
	if b.Vitals != nil {
		s.mu.Lock()
		rejectExtra, rejectVitals := s.RejectExtra, s.RejectVitals
		s.mu.Unlock()
		if rejectExtra && b.Vitals.Extra != nil {
			return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"issues": []any{map[string]any{"path": []any{"vitals", "extra"}, "message": "Unrecognized key"}}}}}
		}
		if rejectVitals {
			return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"issues": []any{map[string]any{"path": []any{"vitals", "cpuPercent"}}}}}}
		}
		if len(b.Vitals.Extra) > 16 {
			return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"reason": "vitals.extra has more than 16 keys"}}}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n.Status = b.Status
	n.LastHeartbeat = s.Now()
	return 200, envelope(map[string]any{"serverFeatures": s.ServerFeatures})
}

func (s *Server) deregister(_ http.ResponseWriter, r *http.Request, _ []byte) (int, any) {
	n, ok := s.node(r)
	if !ok {
		return 404, errNoNode
	}
	s.mu.Lock()
	n.Deregistered = true
	s.mu.Unlock()
	return 200, envelope(map[string]any{"status": "offline"})
}

func (s *Server) config(_ http.ResponseWriter, r *http.Request, _ []byte) (int, any) {
	n, ok := s.node(r)
	if !ok {
		return 404, errNoNode
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.Handshake
	if h.Missing {
		return 404, errNoNode
	}
	reported := s.keys[n.ID] != ""
	if h.KeyReported != nil {
		reported = *h.KeyReported
	}
	now := s.Now()
	if h.ServerTime != nil {
		now = h.ServerTime()
	}
	return 200, envelope(map[string]any{
		"nodeId": n.Name, "apiVersion": h.APIVersion, "features": h.Features, "commandPlane": h.CommandPlane,
		"heartbeatIntervalS": h.HeartbeatIntervalS, "stateMinIntervalS": h.StateMinIntervalS, "stateMaxIntervalS": h.StateMaxIntervalS,
		"claimWaitMaxMs": h.ClaimWaitMaxMs, "keyReported": reported, "serverTime": now.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

func (s *Server) state(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
	n, ok := s.node(r)
	if !ok {
		return 404, errNoNode
	}
	s.mu.Lock()
	enabled := s.StateEndpoint
	s.mu.Unlock()
	if !enabled {
		return 404, errNoNode
	}
	if len(raw) > 256<<10 {
		return 413, map[string]any{"error": map[string]any{"message": "Payload Too Large"}}
	}
	var b struct {
		SchemaVersion int    `json:"schema_version"`
		NodeID        string `json:"node_id"`
		BootID        string `json:"boot_id"`
		Seq           *int64 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.SchemaVersion != 1 || b.BootID == "" || b.Seq == nil {
		return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"code": doctor.CLD008, "issues": []any{map[string]any{"path": []any{"seq"}}}}}}
	}
	if b.NodeID != n.Name {
		return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"code": doctor.CLD004}}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, seen := s.lastSeq[b.BootID]; seen && *b.Seq <= last {
		return 409, map[string]any{"error": map[string]any{"message": "Conflict", "details": map[string]any{"code": doctor.CLD008, "reason": "stale_seq"}}}
	}
	s.lastSeq[b.BootID] = *b.Seq
	s.states = append(s.states, append(json.RawMessage(nil), raw...))
	return 200, envelope(map[string]any{"accepted": true, "seq": *b.Seq})
}

var (
	hexKeyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fpRE     = regexp.MustCompile(`^[0-9a-f]{4}…[0-9a-f]{4}$`)
)

func (s *Server) key(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
	n, ok := s.node(r)
	if !ok {
		return 404, errNoNode
	}
	s.mu.Lock()
	enabled := s.KeyEndpoint
	s.mu.Unlock()
	if !enabled {
		return 404, errNoNode
	}
	var b struct {
		Key         string `json:"key"`
		Fingerprint string `json:"fingerprint"`
		RotatedAt   string `json:"rotated_at"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || !hexKeyRE.MatchString(b.Key) || !fpRE.MatchString(b.Fingerprint) || b.RotatedAt == "" ||
		b.Fingerprint != b.Key[:4]+"…"+b.Key[60:] {
		return 400, map[string]any{"error": map[string]any{"message": "Bad Request"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[n.ID] = b.Key
	s.keyCount++
	return 200, envelope(map[string]any{"keyHint": b.Key[:4] + "…" + b.Key[60:], "storedAt": s.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, _ []byte) (int, any) {
	if _, ok := s.node(r); !ok {
		return 404, errNoNode
	}
	s.mu.Lock()
	b := s.Catalog
	s.mu.Unlock()
	if b == nil {
		return 404, map[string]any{"error": map[string]any{"message": "Not Found", "details": map[string]any{"code": doctor.CLD013, "reason": "no_approved_catalog"}}}
	}
	sha := b.BundleSHA256()
	if r.Header.Get("If-None-Match") == `"`+sha+`"` {
		return 304, nil
	}
	files := make([]map[string]any, 0, len(b.Files))
	for _, f := range b.Files {
		files = append(files, map[string]any{"path": f.Path, "sha256": f.hash(), "content": f.Content})
	}
	w.Header().Set("ETag", `"`+sha+`"`)
	return 200, envelope(map[string]any{"version": b.Version, "bundleSha256": sha, "approvedAt": b.ApprovedAt, "files": files})
}
