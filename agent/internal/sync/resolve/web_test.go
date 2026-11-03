package resolve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The hermetic rig of the web resolver tests: TLS servers on the loopback stand in for Hugging Face,
// GitHub and the file origins; one client trusts their (shared) test certificate; the clock is fixed
// and a wait for a rate limit is recorded, never slept.

// testToken is a Hugging Face token the tests look for in places it must never be.
const testToken = "hf_test_token_value_0123456789"

type webRig struct {
	t      *testing.T
	w      *Web
	logs   *bytes.Buffer
	online bool

	mu     sync.Mutex
	sleeps []time.Duration
}

func newWebRig(t *testing.T) *webRig {
	t.Helper()
	r := &webRig{t: t, logs: &bytes.Buffer{}, online: true}
	r.w = &Web{
		Env: &Env{
			Online: func() bool { return r.online },
			Now:    func() time.Time { return fixedNow },
			Logger: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
		HFToken:        func() string { return testToken },
		CatalogVersion: func() string { return "2026.11.02-1" },
		Sleep: func(_ context.Context, d time.Duration) error {
			r.mu.Lock()
			r.sleeps = append(r.sleeps, d)
			r.mu.Unlock()
			return nil
		},
	}
	return r
}

func (r *webRig) slept() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.sleeps...)
}

// serve starts a TLS server and makes the rig's client trust it (they all share one certificate).
func (r *webRig) serve(h http.Handler) *httptest.Server {
	r.t.Helper()
	srv := httptest.NewTLSServer(h)
	r.t.Cleanup(srv.Close)
	if r.w.HTTP == nil {
		r.w.HTTP = srv.Client()
	}
	return srv
}

// downloader builds a real downloader that trusts the test certificate.
func (r *webRig) downloader(srv *httptest.Server) *download.Downloader {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return download.New(download.Options{HTTP: download.NewHTTPClient(download.ClientConfig{RootCAs: pool}), Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
}

func sum256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// codeIs fails the test unless err carries the doctor code, and reports whether the engine would call
// it permanent.
func codeIs(t *testing.T, err error, code string, permanentWant bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", code)
	}
	if got := CodeOf(err); got != code {
		t.Fatalf("code = %q (%v), want %s", got, err, code)
	}
	if got := isPermanent(err); got != permanentWant {
		t.Fatalf("permanent = %v for %v, want %v", got, err, permanentWant)
	}
}

func noSecret(t *testing.T, where string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, testToken) {
			t.Fatalf("the Hugging Face token leaked into %s", where)
		}
	}
}

func urlContent(id, kind string, src catalog.ContentSource) plan.CatalogItem {
	return contentItem(id, kind, src)
}

// The engine half turns a resolver's error into the shape the machine reads: the doctor code, and
// permanent only for the conditions a retry cannot change. A cancelled context passes through.
func TestToResolveErrorShapes(t *testing.T) {
	perm := toResolveError(failPerm(CodeSourceDisagrees, nil, "x"))
	var re *item.ResolveError
	if !errors.As(perm, &re) || re.Code != doctor.SYN021 || !re.Permanent {
		t.Errorf("021 = %#v", perm)
	}
	trans := toResolveError(fail(CodeHFLogin, nil, "x"))
	if !errors.As(trans, &re) || re.Code != doctor.SYN034 || re.Permanent {
		t.Errorf("034 = %#v", trans)
	}
	if err := toResolveError(errors.New("plain")); !errors.As(err, &re) || re.Code != doctor.SYN033 || re.Permanent {
		t.Errorf("plain = %#v", err)
	}
	if err := toResolveError(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel = %v", err)
	}
	if err := toResolveError(&download.Error{Kind: download.KindDisk, Code: download.CodeDisk}); !errors.Is(err, download.ErrDisk) {
		t.Errorf("disk = %v", err)
	}
}

// Offline (or in survival mode) no resolver touches the network, whatever it is asked.
func TestWebResolversFailClosedOffline(t *testing.T) {
	r := newWebRig(t)
	calls := 0
	srv := r.serve(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	r.w.HFBase, r.w.GitHubAPI = srv.URL, srv.URL
	r.online = false
	rev := strings.Repeat("a", 40)
	model := plan.ModelItem(&catalog.Model{ID: "m1", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Repo: "org-alpha/m", Revision: rev},
		Files: []catalog.ModelFile{{Kind: "weights", Name: "a.gguf", SHA256: strings.Repeat("b", 64), SizeBytes: 3}}})
	gh := urlContent("assets", "maps", catalog.ContentSource{Type: "github_release", Repo: "org-alpha/assets", Tag: "v1", Asset: "a.tgz", SHA256: strings.Repeat("c", 64)})
	u := urlContent("tool", "kit", catalog.ContentSource{Type: "url", URL: "https://data.example.test/t.tgz", SHA256: strings.Repeat("d", 64)})
	for name, call := range map[string]func() error{
		"hf":       func() error { _, err := (&HuggingFace{W: r.w}).Lookup(context.Background(), model); return err },
		"github":   func() error { _, err := (&GitHubRelease{W: r.w}).Lookup(context.Background(), gh); return err },
		"url":      func() error { _, err := (&URLSource{W: r.w}).Lookup(context.Background(), u); return err },
		"url-plan": func() error { _, err := (&URLSource{W: r.w}).Resolve(context.Background(), u); return err },
	} {
		if err := call(); CodeOf(err) != doctor.SYN002 || !errors.Is(err, ErrOffline) {
			t.Errorf("%s offline: %v", name, err)
		}
	}
	if calls != 0 {
		t.Errorf("%d requests were made while offline", calls)
	}
}

func truePtr() *bool { b := true; return &b }
