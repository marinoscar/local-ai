package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// testKey is built at run time: no key-shaped literal sits in the source.
func testKey() string {
	s := sha256.Sum256([]byte("host test key"))
	return hex.EncodeToString(s[:])
}

func writeKeyFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(testKey()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// routerServer is a router on a loopback port: /health is open, everything else wants the key.
func routerServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://elsewhere.example.test/", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testKey() {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"model-one","aliases":["legacy-name"]},{"id":"other"}]}`))
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[]}`))
		case "/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"model-one","status":{"value":"loaded"}},{"id":"broken","status":{"value":"unloaded","failed":true,"exit_code":1}}]}`))
		case "/props":
			w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096},"total_slots":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeSpeaksTheRouterContract(t *testing.T) {
	var calls []string
	srv := routerServer(t, &calls)
	ctx := context.Background()
	p := &Probe{BaseURL: srv.URL, KeyFiles: []string{filepath.Join(t.TempDir(), "missing"), writeKeyFile(t, "api-key")}}
	if err := p.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	// Without the key: 401. With it: 200 and the model with its aliases.
	if st, _, err := p.V1Models(ctx, false); err != nil || st != 401 {
		t.Fatalf("without key: %d %v", st, err)
	}
	st, models, err := p.V1Models(ctx, true)
	if err != nil || st != 200 || len(models) != 2 || models[0].ID != "model-one" || models[0].Aliases[0] != "legacy-name" {
		t.Fatalf("with key: %d %+v %v", st, models, err)
	}
	if st, err := p.Chat(ctx, "legacy-name", 8); err != nil || st != 200 {
		t.Fatalf("Chat: %d %v", st, err)
	}
	ms, err := p.Models(ctx)
	if err != nil || len(ms) != 2 || ms[0].Status != "loaded" || !ms[1].Failed {
		t.Fatalf("Models: %+v %v", ms, err)
	}
	if n, err := p.NCtx(ctx, "model-one"); err != nil || n != 4096 {
		t.Fatalf("NCtx: %d %v", n, err)
	}
	// The public URL is asked the same way, through the URL builder.
	p.PublicURL = func(host string) string { return srv.URL + "/v1/models?host=" + host }
	if st, err := p.Public(ctx, "node.example.test"); err != nil || st != 200 {
		t.Fatalf("Public: %d %v", st, err)
	}
}

func TestProbeNeverFollowsARedirectAndNeverLeaksTheKey(t *testing.T) {
	var calls []string
	srv := routerServer(t, &calls)
	p := &Probe{BaseURL: srv.URL, KeyFiles: []string{writeKeyFile(t, "api-key")}}
	st, _, err := p.do(context.Background(), p.loopbackClient(5*time.Second), http.MethodGet, srv.URL+"/redirect", nil, true)
	if err != nil || st != http.StatusFound {
		t.Fatalf("redirect: %d %v (it must be reported, not followed)", st, err)
	}
	for _, c := range calls {
		if strings.Contains(c, "elsewhere") {
			t.Fatal("the redirect was followed")
		}
	}
	// A request to a non-loopback address is refused before anything is sent, and the error has no key.
	bad := &Probe{BaseURL: "http://192.0.2.1:8080", KeyFiles: p.KeyFiles}
	_, _, err = bad.do(context.Background(), bad.loopbackClient(time.Second), http.MethodGet, bad.base()+"/v1/models", nil, true)
	if err == nil || !strings.Contains(err.Error(), "not a loopback address") || strings.Contains(err.Error(), testKey()) {
		t.Fatalf("err = %v", err)
	}
	// A key that cannot be read is an error without any key material in it.
	none := &Probe{BaseURL: srv.URL, KeyFiles: []string{filepath.Join(t.TempDir(), "missing")}}
	if _, _, err := none.V1Models(context.Background(), true); err == nil || strings.Contains(err.Error(), testKey()) {
		t.Fatalf("err = %v", err)
	}
	// An answer that is not JSON is an error that does not echo the body.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>" + testKey())) }))
	defer junk.Close()
	jp := &Probe{BaseURL: junk.URL, KeyFiles: p.KeyFiles}
	if _, _, err := jp.V1Models(context.Background(), true); err == nil || strings.Contains(err.Error(), testKey()) {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeKeyFilesAreTriedInOrder(t *testing.T) {
	good := writeKeyFile(t, "api-key")
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, []byte("  \n"), 0o600)
	p := &Probe{KeyFiles: []string{empty, filepath.Join(t.TempDir(), "missing"), good}}
	k, err := keyChain{p}.Key()
	if err != nil || k != testKey() {
		t.Fatalf("%q %v", k, err)
	}
	if _, err := (keyChain{&Probe{}}).Key(); err == nil {
		t.Fatal("no key files, no key")
	}
}

func TestOSFS(t *testing.T) {
	dir := t.TempDir()
	var f OSFS
	if err := f.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a", "b", "f")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "a", "link")
	if err := f.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(file, link); err == nil {
		t.Fatal("a second symlink must fail")
	}
	if tgt, err := f.Readlink(link); err != nil || tgt != file {
		t.Fatalf("%q %v", tgt, err)
	}
	r, err := f.OpenRead(link) // follows the link
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	d1, err1 := f.Dev(file)
	d2, err2 := f.Dev(link)
	if err1 != nil || err2 != nil || d1 != d2 {
		t.Fatalf("devices %d %d %v %v", d1, d2, err1, err2)
	}
	if _, err := f.Dev(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("Dev of a missing file")
	}
	free, err := f.FreeBytes(dir)
	if err != nil || free == 0 {
		t.Fatalf("free %d %v", free, err)
	}
	if _, err := f.FreeBytes(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("FreeBytes of a missing path")
	}
}

func TestClockSleepEndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Clock{}).Sleep(ctx, time.Hour); err == nil {
		t.Fatal("a cancelled sleep must return the context's error")
	}
	if err := (Clock{}).Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if (Clock{}).Now().IsZero() {
		t.Fatal("Now")
	}
}

// fakeManager is the part of services.Manager the agent adapter needs.
type fakeManager struct {
	services.Manager
	st    services.UnitState
	calls []string
}

func (m *fakeManager) State(_ context.Context, u string) (services.UnitState, error) {
	m.calls = append(m.calls, "state "+u)
	return m.st, nil
}
func (m *fakeManager) Stop(_ context.Context, u string) error {
	m.calls = append(m.calls, "stop "+u)
	return nil
}
func (m *fakeManager) Start(_ context.Context, u string) error {
	m.calls = append(m.calls, "start "+u)
	return nil
}

func TestAgentAdapterOnlyTouchesTheAgentUnit(t *testing.T) {
	m := &fakeManager{st: services.UnitState{ActiveState: services.ActiveActive}}
	a := Agent{M: m}
	ctx := context.Background()
	if on, err := a.Active(ctx); err != nil || !on {
		t.Fatalf("%v %v", on, err)
	}
	m.st.ActiveState = services.ActiveInactive
	if on, _ := a.Active(ctx); on {
		t.Fatal("inactive is not active")
	}
	_ = a.Stop(ctx)
	_ = a.Start(ctx)
	for _, c := range m.calls {
		if !strings.HasSuffix(c, services.UnitAgent) {
			t.Fatalf("the adapter touched another unit: %v", m.calls)
		}
	}
}

func TestConfigRecordKeepsWhatIsThereAndComments(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.yaml")
	src := "schema_version: 1\nnode:\n  id: ai-n3  # the node\nprofile: small-8gb\nllama:\n  backend: cuda\n  extra_env:\n    GGML_KEEP: \"1\"\n  legacy_aliases:\n    model-one: [old-name]\n"
	if err := os.WriteFile(p, []byte(src), 0o640); err != nil {
		t.Fatal(err)
	}
	store := config.Open(config.Options{Path: p})
	c := Config{Store: store}
	err := c.Record(context.Background(), migrate.ConfigChange{
		LegacyAliases: map[string][]string{"model-one": {"old-name", "legacy-name"}},
		ExtraEnv:      map[string]string{"GGML_KEEP": "9", "GGML_NEW": "2"},
		Commit:        "f3586e66b",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := c.Get().Llama
	if a := got.LegacyAliases["model-one"]; len(a) != 2 || a[0] != "old-name" || a[1] != "legacy-name" {
		t.Errorf("aliases = %v", a)
	}
	if got.ExtraEnv["GGML_KEEP"] != "1" || got.ExtraEnv["GGML_NEW"] != "2" {
		t.Errorf("extra_env = %v (an existing value wins, a new one is added)", got.ExtraEnv)
	}
	if got.Commit != "f3586e66b" {
		t.Errorf("commit = %q", got.Commit)
	}
	text, _ := os.ReadFile(p)
	if !strings.Contains(string(text), "# the node") {
		t.Errorf("the comment is gone:\n%s", text)
	}
}
