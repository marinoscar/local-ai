package checks

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeAgent is the AgentHTTP of the tests: the agent's routes as a ServeMux, served in process the way
// the daemon's own Doer does. A route can be taken down (as a stopped service is), and every request is
// logged.
type fakeAgent struct {
	mux *http.ServeMux

	mu   sync.Mutex
	log  []string
	down map[string]bool // path prefix -> the connection is refused
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{mux: http.NewServeMux(), down: map[string]bool{}}
}

func (a *fakeAgent) handle(pattern string, h http.HandlerFunc) { a.mux.HandleFunc(pattern, h) }

func (a *fakeAgent) take(prefix string, down bool) {
	a.mu.Lock()
	a.down[prefix] = down
	a.mu.Unlock()
}

func (a *fakeAgent) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.log...)
}

func (a *fakeAgent) Do(req *http.Request) (*http.Response, error) {
	a.mu.Lock()
	a.log = append(a.log, req.Method+" "+req.URL.RequestURI())
	for p, d := range a.down {
		if d && len(req.URL.Path) >= len(p) && req.URL.Path[:len(p)] == p {
			a.mu.Unlock()
			return nil, errors.New("connection refused")
		}
	}
	a.mu.Unlock()
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec.Result(), nil
}

// text answers with a body and a content type.
func text(status int, ctype, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
