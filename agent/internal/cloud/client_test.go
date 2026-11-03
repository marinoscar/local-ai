package cloud

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestBackoffBoundsFollowTheSpecFormula(t *testing.T) {
	for failures, want := range map[int]time.Duration{1: 10 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second, 4: 80 * time.Second, 5: 160 * time.Second, 6: 300 * time.Second, 7: 300 * time.Second, 40: 300 * time.Second} {
		lo, hi := BackoffBounds(failures)
		if lo != BackoffMin || hi != want {
			t.Errorf("%d failures: [%v, %v], want [5s, %v]", failures, lo, hi, want)
		}
	}
	if lo, hi := BackoffBounds(0); lo != BackoffMin || hi != 10*time.Second {
		t.Errorf("0 failures counts as 1: [%v, %v]", lo, hi)
	}
}

func TestBackoffJitterStaysInsideTheWindowAndResets(t *testing.T) {
	bo := NewBackoff(nil)
	for i := 1; i <= 12; i++ {
		lo, hi := BackoffBounds(i)
		for n := 0; n < 200; n++ {
			bo2 := &Backoff{rnd: randomDuration, failures: i - 1}
			if d := bo2.Fail(0); d < lo || d > hi {
				t.Fatalf("failure %d: delay %v outside [%v, %v]", i, d, lo, hi)
			}
		}
		bo.Fail(0)
	}
	if bo.Failures() != 12 {
		t.Errorf("failures %d", bo.Failures())
	}
	bo.Reset()
	if bo.Failures() != 0 {
		t.Error("Reset")
	}
	// The window of the first failure again after a reset.
	if d := bo.Fail(0); d < BackoffMin || d > 10*time.Second {
		t.Errorf("after reset %v", d)
	}
}

func TestBackoffRetryAfterWinsOnlyWhenLonger(t *testing.T) {
	fixed := func(lo, hi time.Duration) time.Duration { return hi }
	bo := NewBackoff(fixed)
	if d := bo.Fail(2 * time.Second); d != 10*time.Second {
		t.Errorf("shorter Retry-After: %v", d)
	}
	if d := bo.Fail(90 * time.Second); d != 90*time.Second {
		t.Errorf("longer Retry-After: %v", d)
	}
}

func TestCheckURL(t *testing.T) {
	for url, devInsecure := range map[string]bool{
		"https://ai.example.test": false, "https://ai.example.test:8443": false, "https://ai.example.test/": false,
		"http://127.0.0.1:8080": true,
	} {
		if err := CheckURL(url, devInsecure); err != nil {
			t.Errorf("%s: %v", url, err)
		}
	}
	for _, tc := range []struct {
		url string
		dev bool
	}{
		{"http://ai.example.test", false}, {"http://ai.example.test", true}, {"http://127.0.0.1:8080", false}, {"http://127.0.0.1", true},
		{"http://localhost:8080", true}, {"ftp://ai.example.test", false}, {"ai.example.test", false}, {"https://", false},
		{"https://user" + ":pw@example.test", false}, {"https://ai.example.test/api", false}, {"https://ai.example.test?x=1", false},
		{"https://ai.example.test#frag", false}, {"", false},
	} {
		if err := CheckURL(tc.url, tc.dev); err == nil {
			t.Errorf("%q (dev %v) accepted", tc.url, tc.dev)
		}
	}
}

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return NewClient(ClientOptions{BaseURL: srv.URL + "/", HTTP: srv.Client(), UserAgent: "aiagent/test", Credential: func() string { return testCredential }})
}

func TestClientSendsTheCredentialAndUnwrapsTheEnvelope(t *testing.T) {
	var gotAuth, gotUA, gotCT, gotMethod string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotCT, gotMethod = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), r.Method
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"data":{"echo":`+string(body)+`},"meta":{"timestamp":"x"}}`)
	}))
	rep, err := c.Call(context.Background(), "POST", "/x", map[string]int{"a": 1}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Echo map[string]int `json:"echo"`
	}
	if err := rep.Decode(&out); err != nil || out.Echo["a"] != 1 {
		t.Fatalf("decode %v %v", out, err)
	}
	if gotAuth != "Bearer "+testCredential || gotUA != "aiagent/test" || gotCT != "application/json" || gotMethod != "POST" {
		t.Errorf("headers %q %q %q %q", gotAuth, gotUA, gotCT, gotMethod)
	}
	// A body without an envelope is returned as it is.
	c2 := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"node":{"id":"n"}}`) }))
	rep, err = c2.Call(context.Background(), "GET", "/x", nil, time.Second)
	if err != nil || !strings.Contains(string(rep.Data), `"node"`) {
		t.Fatalf("plain body: %v %s", err, rep.Data)
	}
}

func TestClientErrorShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		header  map[string]string
		code    string
		reason  string
		paths   []string
		extra   bool
		retryOK time.Duration
	}{
		"nested details":   {409, `{"error":{"message":"Conflict","details":{"code":"LAI-CLD-008","reason":"stale_seq"}}}`, nil, "LAI-CLD-008", "stale_seq", nil, false, 0},
		"top level":        {400, `{"message":"bad","details":{"code":"LAI-CLD-004"}}`, nil, "LAI-CLD-004", "", nil, false, 0},
		"code at the top":  {404, `{"code":"LAI-CLD-013","message":"none"}`, nil, "LAI-CLD-013", "", nil, false, 0},
		"zod paths":        {400, `{"error":{"details":{"issues":[{"path":["vitals","extra"],"message":"Unrecognized key"},{"path":["models",0,"id"]}]}}}`, nil, "", "", []string{"vitals.extra", "models.0.id"}, true, 0},
		"retry after":      {429, `{}`, map[string]string{"Retry-After": "7"}, "", "", nil, false, 7 * time.Second},
		"not json":         {502, `<html>bad gateway</html>`, nil, "", "", nil, false, 0},
		"empty":            {500, ``, nil, "", "", nil, false, 0},
		"retry after date": {503, `{}`, map[string]string{"Retry-After": time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)}, "", "", nil, false, -1},
	} {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			_, err := c.Call(context.Background(), "GET", "/x", nil, time.Second)
			ae := asAPIError(err)
			if ae == nil || ae.Status != tc.status || ae.Code != tc.code || ae.Reason != tc.reason {
				t.Fatalf("err = %#v", err)
			}
			if strings.Join(ae.Paths, ",") != strings.Join(tc.paths, ",") {
				t.Errorf("paths %v", ae.Paths)
			}
			if ae.Mentions("extra") != tc.extra {
				t.Errorf("Mentions(extra) = %v", ae.Mentions("extra"))
			}
			if tc.retryOK > 0 && ae.RetryAfter != tc.retryOK || tc.retryOK < 0 && ae.RetryAfter < time.Hour {
				t.Errorf("RetryAfter %v", ae.RetryAfter)
			}
		})
	}
}

func TestClientScrubsSecretsOutOfServerMessages(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key `+testKey+` and `+testCredential+`"}}`)
	}))
	_, err := c.Call(context.Background(), "GET", "/x", nil, time.Second)
	ae := asAPIError(err)
	if ae == nil || strings.Contains(ae.Message, testKey) || strings.Contains(ae.Message, testCredential) || ae.Message == "" {
		t.Fatalf("message %q", ae.Message)
	}
}

func TestClientNeverFollowsARedirectWithTheCredential(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target was contacted")
	}))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	// The production http client, made to trust the test certificate.
	hc := NewHTTPClient()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	hc.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
	c := NewClient(ClientOptions{BaseURL: srv.URL, HTTP: hc, Credential: func() string { return testCredential }})
	_, err := c.Call(context.Background(), "GET", "/x", nil, time.Second)
	if ae := asAPIError(err); ae == nil || ae.Status != http.StatusTemporaryRedirect {
		t.Fatalf("a redirect must surface as an error answer, got %v", err)
	}
	// TLS verification is on: the production client, with no certificate added, refuses a test server.
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer other.Close()
	c2 := NewClient(ClientOptions{BaseURL: other.URL, HTTP: NewHTTPClient()})
	if _, err := c2.Call(context.Background(), "GET", "/x", nil, time.Second); err == nil {
		t.Fatal("a certificate signed by an unknown authority was accepted")
	}
}

func TestClientTransportFailuresAreNetErrorsAndTimeoutsEnd(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	_, err := c.Call(context.Background(), "GET", "/x", nil, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
	dead := NewClient(ClientOptions{BaseURL: "https://127.0.0.1:1", Credential: func() string { return testCredential }})
	_, err = dead.Call(context.Background(), "GET", "/x", nil, time.Second)
	var ne *NetError
	if !errors.As(err, &ne) || asAPIError(err) != nil {
		t.Fatalf("connection refused: %#v", err)
	}
}

func TestClientRefusesAnOversizeAnswer(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxReplyBytes+10)))
	}))
	if _, err := c.Call(context.Background(), "GET", "/x", nil, 5*time.Second); err == nil {
		t.Fatal("an answer over the limit was accepted")
	}
}

// ---------------------------------------------------------------------------------------------------
// Files

func TestCredentialStoreIs0600AndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-credential")
	s := CredentialStore{Path: path}
	if _, err := s.Read(); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("missing file: %v", err)
	}
	if err := s.Write(testCredential); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	got, err := s.Read()
	if err != nil || got != testCredential {
		t.Fatalf("read %q %v", got, err)
	}
	// A looser mode is tightened when the file is read.
	_ = os.Chmod(path, 0o644)
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode after read %v", fi.Mode())
	}
	// A symlink is refused: chmod would follow it.
	link := filepath.Join(dir, "linked")
	_ = os.Symlink(path, link)
	if _, err := (CredentialStore{Path: link}).Read(); err == nil {
		t.Error("a symlinked credential was read")
	}
	// An empty file is no credential.
	_ = os.WriteFile(path, []byte("  \n"), 0o600)
	if _, err := s.Read(); !errors.Is(err, ErrNoCredential) {
		t.Errorf("blank file: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
}

func TestPartialCredentialWriteLeavesTheOldFileAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud-credential")
	if err := (CredentialStore{Path: path}).Write(testCredential); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"rename", "sync"} {
		t.Run(op, func(t *testing.T) {
			fs := cfake.New(config.OSFS{}).FailOn(op, 1)
			err := CredentialStore{Path: path, FS: fs}.Write("nod_" + strings.Repeat("Q", 24))
			if err == nil {
				t.Fatal("the failing write reported success")
			}
			got, rerr := (CredentialStore{Path: path}).Read()
			if rerr != nil || got != testCredential {
				t.Fatalf("the old credential was damaged: %q %v", got, rerr)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Errorf("leftover files: %v", entries)
			}
		})
	}
}

func TestLinkStoreRoundTripAndDamagedFile(t *testing.T) {
	dir := t.TempDir()
	s := LinkStore{Path: filepath.Join(dir, "cloud", "link.json")}
	if _, ok := s.Load(); ok {
		t.Fatal("a missing file loaded")
	}
	rec := LinkRecord{URL: "https://ai.example.test", ServerNodeID: "00000000-0000-4000-8000-000000000001", Name: "ai-n3", RegisteredAt: t0,
		ServerFeatures: []string{"node_host_vitals"}, Handshake: &HandshakeRecord{Features: []string{"state.v1"}, CommandPlane: "localai", HeartbeatIntervalS: 15, CheckedAt: t0}}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(s.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	got, ok := s.Load()
	if !ok || got.ServerNodeID != rec.ServerNodeID || got.Handshake == nil || got.Handshake.CommandPlane != "localai" || !got.RegisteredAt.Equal(t0) {
		t.Fatalf("loaded %+v %v", got, ok)
	}
	// A damaged file is no record, never an error for the link.
	_ = os.WriteFile(s.Path, []byte("{broken"), 0o600)
	if _, ok := s.Load(); ok {
		t.Error("a damaged link.json loaded")
	}
	_ = os.WriteFile(s.Path, []byte(`{"url":"x"}`), 0o600)
	if _, ok := s.Load(); ok {
		t.Error("a link.json without a server id loaded")
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
}
