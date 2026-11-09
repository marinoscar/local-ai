package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
)

// This file wires the content and service checks of the drill (DRILL-04, drill.md section 4.2 checks 3,
// 4, 8 to 16 and 19): it builds the ports of internal/drill/checks from what the daemon has and
// registers the checks in the engine's registry. The checks themselves are in that package; nothing
// here decides a result.

// registerContentDrillChecks registers DRILL-04's checks in reg. A piece the daemon could not build
// (no sync stack, no supervisor) leaves its port nil, and the checks that need it record a skip.
func (d *Daemon) registerContentDrillChecks(reg *drill.Registry) error {
	return checks.RegisterContent(reg, d.contentDrillDeps())
}

func (d *Daemon) contentDrillDeps() checks.ContentDeps {
	c := d.c
	deps := checks.ContentDeps{
		HTTP: agentLoopback{handler: d.apiHandler},
		FS:   ark.OSFS{},
		NodeID: func() string {
			return c.Config.Get().Node.ID
		},
		CatalogVersion: func() string { return c.Catalog.Get().Catalog.Version },
		ModelEngine:    d.modelEngine,
		Books:          d.libraryBooks,
		NodeKit:        d.nodeKitItems,
		ReverifyDays:   func() int { return d.syncSettings().ReverifyDays },
	}
	if c.ArkRoots != nil {
		deps.Roots = c.ArkRoots.List
	} else if c.ArkMonitor != nil {
		deps.Roots = func() []ark.Root { return []ark.Root{c.ArkMonitor.Root()} }
	}
	if c.Ready != nil {
		deps.Ready = c.Ready
	}
	if d.ark != nil && d.ark.verifier != nil {
		deps.Verifier = d.ark.verifier
	}
	if c.Health != nil {
		deps.Health = c.Health
	}
	return deps
}

// apiHandler is the agent's own HTTP handler, once the server is built.
func (d *Daemon) apiHandler() http.Handler {
	if d.c.API == nil {
		return nil
	}
	return d.c.API.Handler()
}

// modelEngine is the engine ("llama", "whisper", "piper") of a catalog model id; "" when unknown.
func (d *Daemon) modelEngine(id string) string {
	for _, m := range d.c.Catalog.Get().Catalog.Models {
		if m.ID == id {
			return m.Engine
		}
	}
	return ""
}

// libraryBooks lists the books the kiwix library serves with the survival tier of their catalog item.
func (d *Daemon) libraryBooks(context.Context) ([]checks.LibraryBook, error) {
	books, known := d.kiwixStatus.Snapshot()
	if !known {
		return nil, errors.New("the library has not been built since the agent started")
	}
	tiers := map[string]int{}
	for _, it := range d.c.Catalog.Get().Catalog.Content {
		tiers[it.ID] = it.SurvivalTier
	}
	out := make([]checks.LibraryBook, 0, len(books))
	for _, b := range books {
		tier := tiers[b.ContentID]
		if tier == 0 {
			tier = 3
		}
		out = append(out, checks.LibraryBook{Name: kiwix.UndatedName(b.Name), ItemID: b.ContentID, Tier: tier})
	}
	return out, nil
}

// nodeKitItems are the catalog ids of the kit parts this computer needs (rebuild-kit.md section 4.4),
// read from a quick kit verification of the internal root: nothing is hashed and nothing is written.
func (d *Daemon) nodeKitItems(ctx context.Context) []string {
	if d.ark == nil || d.ark.facts == nil || d.c.ArkMonitor == nil {
		return nil
	}
	root := d.c.ArkMonitor.Root()
	rep, err := (kit.Verifier{FS: kit.OSFS{}}).Verify(ctx, root.Path, kit.Options{
		Mode: kit.ModeQuick, Now: d.env.Clock.Now(), ReverifyDays: d.syncSettings().ReverifyDays,
		RootID: root.ID, Internal: true, Node: d.ark.facts.kitNode(),
	})
	if err != nil || rep.Node == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range rep.Node.Needs {
		if n.Item != "" && !seen[n.Item] {
			seen[n.Item] = true
			out = append(out, n.Item)
		}
	}
	sort.Strings(out)
	return out
}

// agentLoopback is checks.AgentHTTP: a request goes through the agent's own handler, in process, with
// the context of a socket peer of uid 0 (the internal admin of drill.md section 6). No token exists
// inside the agent and none is made; no socket, session or cookie is opened, so nothing is left behind
// and the network fence has nothing to refuse.
type agentLoopback struct {
	handler func() http.Handler
}

// Do implements checks.AgentHTTP.
func (a agentLoopback) Do(req *http.Request) (*http.Response, error) {
	h := a.handler()
	if h == nil {
		return nil, errors.New("the agent's API is not running")
	}
	ctx := api.WithTransport(req.Context(), api.TransportSocket, &api.PeerCred{UID: 0, GID: 0, PID: os.Getpid()})
	r := req.Clone(ctx)
	r.RequestURI = req.URL.RequestURI()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	rec := &loopbackWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				rec.mu.Lock()
				rec.status, rec.panicked = http.StatusInternalServerError, true
				rec.mu.Unlock()
			}
		}()
		h.ServeHTTP(rec, r)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return rec.response(req), nil
}

// loopbackWriter is the http.ResponseWriter the loopback handler writes into. It is not net/http/httptest,
// which registers a command-line flag in the daemon.
type loopbackWriter struct {
	mu       sync.Mutex
	header   http.Header
	status   int
	body     bytes.Buffer
	panicked bool
}

func (w *loopbackWriter) Header() http.Header { return w.header }

func (w *loopbackWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = code
	}
}

func (w *loopbackWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(b)
}

// Flush implements http.Flusher: the buffer is read when the handler returns.
func (w *loopbackWriter) Flush() {}

func (w *loopbackWriter) response(req *http.Request) *http.Response {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status: http.StatusText(status), StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.header.Clone(), Body: io.NopCloser(bytes.NewReader(w.body.Bytes())),
		ContentLength: int64(w.body.Len()), Request: req,
	}
}
