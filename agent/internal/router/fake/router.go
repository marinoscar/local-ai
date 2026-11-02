// Package fake is a scripted llama-server router served over HTTP, for the tests of every package
// that talks to internal/router (the client, the watcher, the orchestrator, the doctor, the bench).
//
// It speaks the wire shapes of llama.cpp's router mode (tools/server/README.md, commit f3586e66b)
// and mimics its answers: 401 JSON for a wrong key on every route but /health, 404 for an unknown
// model, 400 for loading a running model or unloading a stopped one. A test scripts the state of the
// models (Set, Crash, FailLoad, Restart), the failure of the server (Down, Reset, Delay, Malformed,
// Redirect) and the SSE stream (Emit, DropStreams), and reads the request log (Requests) to assert
// what was called, with which method and path, and whether a key was sent and was right. The log
// never holds key material.
//
// The package imports nothing from internal/router, so router's own tests can use it.
package fake

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// Statuses of a model, as the router writes them.
const (
	Unloaded    = "unloaded"
	Loading     = "loading"
	Loaded      = "loaded"
	Sleeping    = "sleeping"
	Downloading = "downloading"
	Downloaded  = "downloaded"
	Incomplete  = "incomplete"
)

// Model is one scripted model.
type Model struct {
	ID               string
	Aliases, Tags    []string
	Source           string // preset (default), models_dir or cache
	Status           string // default unloaded
	Failed           bool
	ExitCode         int
	ContextLength    int64
	Memory           json.RawMessage // the memory object, or nil to omit it
	LoadOnStartup    bool            // loaded again by Restart
	InputModalities  []string
	OutputModalities []string

	// readyAt and failOnLoad drive a load in progress.
	readyAt    time.Time
	failOnLoad int
}

// Request is one entry of the request log. HasAuth tells whether an Authorization header was sent
// and KeyOK whether it carried the server's current key; the key itself is never recorded.
type Request struct {
	Method   string
	Path     string
	RawQuery string
	HasAuth  bool
	KeyOK    bool
	Body     string
}

// Router is the fake server. Create it with New and stop it with Close.
type Router struct {
	srv *httptest.Server

	mu      sync.Mutex
	key     string
	models  []*Model
	log     []Request
	now     func() time.Time
	loadFor time.Duration

	down        bool
	reset       bool
	redirect    bool
	delay       time.Duration
	modelsBody  *string
	forceStatus int
	forceBody   string
	sseStatus   int
	reloads     int

	streams map[*stream]struct{}
}

type stream struct {
	ch   chan []byte
	done chan struct{}
}

// New starts a fake router that accepts key as the bearer credential.
func New(key string) *Router {
	r := &Router{key: key, now: time.Now, streams: map[*stream]struct{}{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	return r
}

// URL is the base URL of the server (http://127.0.0.1:<port>).
func (r *Router) URL() string { return r.srv.URL }

// Close stops the server and ends every open stream.
func (r *Router) Close() {
	r.DropStreams()
	r.srv.CloseClientConnections()
	r.srv.Close()
}

// SetKey changes the key the server accepts (a rotation).
func (r *Router) SetKey(key string) { r.with(func() { r.key = key }) }

// SetClock replaces the clock that decides when a load in progress is done.
func (r *Router) SetClock(now func() time.Time) { r.with(func() { r.now = now }) }

// SetLoadTime sets how long a model stays "loading" after a load request (default 0: loaded at the
// next read).
func (r *Router) SetLoadTime(d time.Duration) { r.with(func() { r.loadFor = d }) }

func (r *Router) with(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

// Add registers a model (a copy), replacing one with the same id.
func (r *Router) Add(m Model) {
	if m.Status == "" {
		m.Status = Unloaded
	}
	if m.Source == "" {
		m.Source = "preset"
	}
	r.with(func() {
		for i, x := range r.models {
			if x.ID == m.ID {
				r.models[i] = &m
				return
			}
		}
		r.models = append(r.models, &m)
	})
}

// Remove drops a model from the list.
func (r *Router) Remove(id string) {
	r.with(func() {
		for i, x := range r.models {
			if x.ID == id {
				r.models = append(r.models[:i], r.models[i+1:]...)
				return
			}
		}
	})
}

// Set sets the status of a model and clears its failure.
func (r *Router) Set(id, status string) {
	r.with(func() {
		if m := r.find(id); m != nil {
			m.Status, m.Failed, m.ExitCode, m.readyAt = status, false, 0, time.Time{}
		}
	})
}

// Crash makes a model stop the way a dead child does: unloaded, failed, with the exit code.
func (r *Router) Crash(id string, exitCode int) {
	r.with(func() {
		if m := r.find(id); m != nil {
			m.Status, m.Failed, m.ExitCode, m.readyAt = Unloaded, true, exitCode, time.Time{}
		}
	})
}

// FailLoad makes the next load of a model end in a crash with this exit code.
func (r *Router) FailLoad(id string, exitCode int) {
	r.with(func() {
		if m := r.find(id); m != nil {
			m.failOnLoad = exitCode
		}
	})
}

// SetMemory sets the memory object of a model (raw JSON); nil removes it.
func (r *Router) SetMemory(id string, raw string) {
	r.with(func() {
		if m := r.find(id); m != nil {
			if raw == "" {
				m.Memory = nil
			} else {
				m.Memory = json.RawMessage(raw)
			}
		}
	})
}

// Restart simulates a llama restart: every child is gone, and the models flagged LoadOnStartup are
// loading again. Open streams are cut.
func (r *Router) Restart() {
	r.with(func() {
		for _, m := range r.models {
			m.Status, m.Failed, m.ExitCode, m.readyAt = Unloaded, false, 0, time.Time{}
			if m.LoadOnStartup {
				m.Status, m.readyAt = Loading, r.now().Add(r.loadFor)
			}
		}
	})
	r.DropStreams()
}

// Status returns the current status of a model ("" for an unknown id), resolving a finished load.
func (r *Router) Status(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.find(id); m != nil {
		r.settle(m)
		return m.Status
	}
	return ""
}

func (r *Router) find(id string) *Model {
	for _, m := range r.models {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// settle finishes a load whose time has come.
func (r *Router) settle(m *Model) {
	// readyAt is zero for a load a test holds open with Set(id, Loading).
	if m.Status != Loading || m.readyAt.IsZero() || r.now().Before(m.readyAt) {
		return
	}
	if m.failOnLoad != 0 {
		m.Status, m.Failed, m.ExitCode, m.failOnLoad = Unloaded, true, m.failOnLoad, 0
		return
	}
	m.Status, m.Failed, m.ExitCode = Loaded, false, 0
}

// Down makes every route (health included) answer 503 "Loading model"; Down(false) ends it.
func (r *Router) Down(on bool) { r.with(func() { r.down = on }) }

// Reset makes the server close the connection without answering, like a router that just died.
func (r *Router) Reset(on bool) { r.with(func() { r.reset = on }) }

// Redirect makes every route answer 302 to another host.
func (r *Router) Redirect(on bool) { r.with(func() { r.redirect = on }) }

// Delay makes every route wait d before answering (the request's own timeout ends the wait).
func (r *Router) Delay(d time.Duration) { r.with(func() { r.delay = d }) }

// Malformed replaces the body of GET /models with raw (status 200); Malformed("") restores it.
func (r *Router) Malformed(raw string) {
	r.with(func() {
		if raw == "" {
			r.modelsBody = nil
			return
		}
		r.modelsBody = &raw
	})
}

// Force makes every authenticated route answer with this status and body (not /health);
// Force(0, "") ends it.
func (r *Router) Force(status int, body string) {
	r.with(func() { r.forceStatus, r.forceBody = status, body })
}

// SSEStatus makes GET /models/sse answer with this status instead of a stream (0: a stream).
func (r *Router) SSEStatus(status int) { r.with(func() { r.sseStatus = status }) }

// Requests returns a copy of the request log.
func (r *Router) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.log...)
}

// Calls counts the logged requests with this method and path.
func (r *Router) Calls(method, path string) int {
	n := 0
	for _, q := range r.Requests() {
		if q.Method == method && q.Path == path {
			n++
		}
	}
	return n
}

// Reloads is the number of GET /models?reload=1 calls served.
func (r *Router) Reloads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

// Emit sends an SSE event {"model","event","data"} to every open stream. data may be nil.
func (r *Router) Emit(model, event string, data any) {
	obj := map[string]any{"model": model, "event": event}
	if data != nil {
		obj["data"] = data
	}
	b, _ := json.Marshal(obj)
	r.EmitRaw("data: " + string(b) + "\n\n")
}

// EmitRaw writes raw text to every open stream (comments, bad frames).
func (r *Router) EmitRaw(text string) {
	r.mu.Lock()
	ss := make([]*stream, 0, len(r.streams))
	for s := range r.streams {
		ss = append(ss, s)
	}
	r.mu.Unlock()
	for _, s := range ss {
		select {
		case s.ch <- []byte(text):
		case <-s.done:
		case <-time.After(time.Second):
		}
	}
}

// Streams is the number of open SSE streams.
func (r *Router) Streams() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

// DropStreams closes every open SSE stream from the server side.
func (r *Router) DropStreams() {
	r.mu.Lock()
	ss := make([]*stream, 0, len(r.streams))
	for s := range r.streams {
		ss = append(ss, s)
	}
	r.mu.Unlock()
	for _, s := range ss {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
}

// serve is the handler.
func (r *Router) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	r.mu.Lock()
	auth := req.Header.Get("Authorization")
	rec := Request{Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery, HasAuth: auth != "", Body: string(body)}
	rec.KeyOK = auth == "Bearer "+r.key
	r.log = append(r.log, rec)
	reset, down, redirect, delay := r.reset, r.down, r.redirect, r.delay
	r.mu.Unlock()

	if reset {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-req.Context().Done():
			return
		}
	}
	if redirect {
		http.Redirect(w, req, "http://203.0.113.9/elsewhere", http.StatusFound)
		return
	}
	if down {
		writeErr(w, 503, "Loading model", "unavailable_error")
		return
	}
	public := req.URL.Path == "/health" || req.URL.Path == "/v1/health"
	if !public && !rec.KeyOK {
		writeErr(w, 401, "Invalid API Key", "authentication_error")
		return
	}
	if public {
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	}
	r.mu.Lock()
	fs, fb := r.forceStatus, r.forceBody
	r.mu.Unlock()
	if fs != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fs)
		_, _ = io.WriteString(w, fb)
		return
	}
	switch {
	case req.Method == http.MethodGet && (req.URL.Path == "/models" || req.URL.Path == "/v1/models"):
		r.serveModels(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/models/load":
		r.serveAct(w, body, true)
	case req.Method == http.MethodPost && req.URL.Path == "/models/unload":
		r.serveAct(w, body, false)
	case req.Method == http.MethodGet && req.URL.Path == "/models/sse":
		r.serveSSE(w, req)
	case req.Method == http.MethodGet && req.URL.Path == "/props":
		r.serveProps(w, req)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/chat/completions":
		r.serveChat(w, body)
	default:
		// POST /models (download) and DELETE /models land here too: the fake does nothing with them,
		// the request log is how a test proves they were never sent.
		writeJSON(w, 200, map[string]any{"success": true})
	}
}

func (r *Router) serveModels(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	if req.URL.Query().Get("reload") != "" {
		r.reloads++
	}
	raw := r.modelsBody
	data := make([]any, 0, len(r.models))
	for _, m := range r.models {
		r.settle(m)
		data = append(data, m.json())
	}
	r.mu.Unlock()
	if raw != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, *raw)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (m *Model) json() map[string]any {
	st := map[string]any{"value": m.Status, "args": []string{}}
	if m.Status == Loaded || m.Status == Loading || m.Status == Sleeping {
		st["args"] = []string{"llama-server", "-c", "4096"}
	}
	if m.Failed {
		st["failed"], st["exit_code"] = true, m.ExitCode
	}
	in, out := m.InputModalities, m.OutputModalities
	if in == nil {
		in = []string{"text"}
	}
	if out == nil {
		out = []string{"text"}
	}
	obj := map[string]any{
		"id": m.ID, "aliases": nz(m.Aliases), "tags": nz(m.Tags), "object": "model", "owned_by": "llamacpp",
		"created": 1793630645, "status": st, "source": m.Source, "can_remove": m.Source == "cache",
		"architecture": map[string]any{"input_modalities": in, "output_modalities": out},
	}
	if m.ContextLength > 0 {
		obj["context_length"] = m.ContextLength
	}
	if m.Memory != nil {
		obj["memory"] = m.Memory
	}
	return obj
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *Router) serveAct(w http.ResponseWriter, body []byte, load bool) {
	var in struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &in) != nil {
		writeErr(w, 400, "invalid JSON body", "invalid_request_error")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.find(in.Model)
	switch {
	case m == nil && load:
		writeErr(w, 404, "model is not found", "not_found_error")
	case m == nil:
		writeErr(w, 400, "model is not found", "invalid_request_error")
	default:
		r.settle(m)
		running := m.Status == Loaded || m.Status == Loading || m.Status == Sleeping
		switch {
		case load && running:
			writeErr(w, 400, "model is already running", "invalid_request_error")
		case !load && !running:
			writeErr(w, 400, "model is not running", "invalid_request_error")
		case load:
			m.Status, m.Failed, m.ExitCode, m.readyAt = Loading, false, 0, r.now().Add(r.loadFor)
			writeJSON(w, 200, map[string]any{"success": true})
		default:
			m.Status, m.Failed, m.ExitCode, m.readyAt = Unloaded, false, 0, time.Time{}
			writeJSON(w, 200, map[string]any{"success": true})
		}
	}
}

func (r *Router) serveProps(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("model")
	if id == "" {
		writeErr(w, 400, "model name is missing from the request", "invalid_request_error")
		return
	}
	if st := r.Status(id); st != Loaded && st != Sleeping {
		writeErr(w, 400, "model is not loaded", "invalid_request_error")
		return
	}
	writeJSON(w, 200, map[string]any{
		"default_generation_settings": map[string]any{"n_ctx": 4096},
		"total_slots":                 1, "model_path": "/var/lib/localai/models/" + id + ".gguf",
		"build_info": "b1234-abcdef0", "is_sleeping": r.Status(id) == Sleeping,
	})
}

func (r *Router) serveChat(w http.ResponseWriter, body []byte) {
	var in struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &in)
	if r.Status(in.Model) != Loaded {
		writeErr(w, 400, "model is not loaded", "invalid_request_error")
		return
	}
	writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "OK"}}}})
}

func (r *Router) serveSSE(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	code := r.sseStatus
	r.mu.Unlock()
	if code != 0 {
		writeErr(w, code, "no such route", "not_found_error")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		return
	}
	s := &stream{ch: make(chan []byte, 64), done: make(chan struct{})}
	r.mu.Lock()
	r.streams[s] = struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.streams, s)
		r.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl.Flush()
	for {
		select {
		case b := <-s.ch:
			_, _ = w.Write(b)
			fl.Flush()
		case <-s.done:
			return
		case <-req.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, typ string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "type": typ}})
}

// String describes the request, without the body, for test failure messages.
func (q Request) String() string {
	s := q.Method + " " + q.Path
	if q.RawQuery != "" {
		s += "?" + q.RawQuery
	}
	return s
}
