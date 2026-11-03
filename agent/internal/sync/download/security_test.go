package download

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// tlsServer is an https test server that serves data and records the Authorization header.
type tlsServer struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
	hits int
}

func newTLSServer(t *testing.T, h http.HandlerFunc) *tlsServer {
	t.Helper()
	s := &tlsServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits++
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tlsServer) authHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

func (s *tlsServer) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// tlsClient trusts the test servers' certificate and reaches any host name at 127.0.0.1 (the port
// is kept), so "example.test"-style hosts can be redirected to without DNS.
func tlsClient(t *testing.T, trust *tlsServer, authHosts ...string) *HTTPClient {
	t.Helper()
	pool := x509.NewCertPool()
	if trust != nil {
		pool.AddCert(trust.Certificate())
	}
	return NewHTTPClient(ClientConfig{
		Version: "9.9.9", RootCAs: pool, AuthHosts: authHosts, Token: func() string { return testToken },
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, _ := net.SplitHostPort(addr)
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	})
}

func serveData(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", epoch, strings.NewReader(string(data)))
	}
}

func TestTLSVerificationIsAlwaysOn(t *testing.T) {
	data := genData(1000, 40)
	srv := newTLSServer(t, serveData(data))
	e := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, nil) }) // nobody trusts the test certificate
	res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), e.cp)
	var fe *Error
	if !errors.As(err, &fe) || fe.Kind != KindPermanent || fe.Code != CodeRefused {
		t.Fatalf("err = %v, want a permanent refusal", err)
	}
	if srv.hitCount() != 0 || res.Attempts != 1 || len(e.clk.delays()) != 0 {
		t.Errorf("hits %d, attempts %d, waits %v: a certificate failure is not retried", srv.hitCount(), res.Attempts, e.clk.delays())
	}
	// With the certificate trusted the same download works.
	e2 := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, srv) })
	mustFetch(t, e2, e2.job(srv.URL+"/f", data))
}

func TestAuthorizationOnlyToTheAuthHostOverHTTPS(t *testing.T) {
	data := genData(1000, 41)
	srv := newTLSServer(t, serveData(data))
	want := "Bearer " + testToken
	for _, tc := range []struct {
		name      string
		auth      bool
		authHosts []string
		wantAuth  string
	}{
		{"the auth host with Auth set", true, []string{"127.0.0.1"}, want},
		{"Auth not set", false, []string{"127.0.0.1"}, ""},
		{"a host that is not the auth host", true, []string{"huggingface.co"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(srv.authHeaders())
			e := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, srv, tc.authHosts...) })
			j := e.job(srv.URL+"/f", data)
			j.Auth = tc.auth
			mustFetch(t, e, j)
			if got := srv.authHeaders()[before]; got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
		})
	}
	t.Run("never over plain http", func(t *testing.T) {
		e := newEnv(t, nil) // the auth host list holds 127.0.0.1, the server is http
		plain := newTestServer(t, data)
		j := e.job(plain.URL+"/f", data)
		j.Auth = true
		mustFetch(t, e, j)
		if got := plain.requests()[0].Auth; got != "" {
			t.Errorf("the token was sent over http: %q", got)
		}
	})
}

func TestRedirects(t *testing.T) {
	data := genData(5000, 42)
	t.Run("cross-host redirect drops Authorization", func(t *testing.T) {
		dest := newTLSServer(t, serveData(data))
		_, destPort, _ := net.SplitHostPort(dest.Listener.Addr().String())
		src := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com:"+destPort+"/cdn/f", http.StatusFound)
		})
		e := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, dest, "127.0.0.1") })
		j := e.job(src.URL+"/f", data)
		j.Auth = true
		mustFetch(t, e, j)
		if got := src.authHeaders(); len(got) != 1 || got[0] != "Bearer "+testToken {
			t.Errorf("the first host saw %q", got)
		}
		if got := dest.authHeaders(); len(got) != 1 || got[0] != "" {
			t.Errorf("the redirect target saw Authorization %q: it must be dropped on a cross-host redirect", got)
		}
	})
	t.Run("same-host redirect keeps it", func(t *testing.T) {
		dest := newTLSServer(t, serveData(data))
		src := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, dest.URL+"/f2", http.StatusFound) // 127.0.0.1 again, another port
		})
		e := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, dest, "127.0.0.1") })
		j := e.job(src.URL+"/f", data)
		j.Auth = true
		mustFetch(t, e, j)
		if got := dest.authHeaders(); got[0] != "Bearer "+testToken {
			t.Errorf("a redirect inside the auth host lost the token: %q", got)
		}
	})
	t.Run("redirect to http is refused", func(t *testing.T) {
		plain := newTestServer(t, data)
		src := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/f", http.StatusFound)
		})
		e := newEnv(t, func(o *Options) { o.HTTP = tlsClient(t, src, "127.0.0.1") })
		res, err := e.dl.Fetch(context.Background(), e.job(src.URL+"/f", data), e.cp)
		var fe *Error
		if !errors.As(err, &fe) || fe.Kind != KindPermanent || fe.Code != CodeRefused || res.Attempts != 1 {
			t.Fatalf("err = %v, Result %+v", err, res)
		}
		if plain.count() != 0 {
			t.Error("the http target was contacted")
		}
	})
	t.Run("more than ten redirects", func(t *testing.T) {
		var src *tlsServer
		src = newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, src.URL+r.URL.Path+"x", http.StatusFound)
		})
		e := newEnv(t, func(o *Options) {
			o.HTTP = tlsClient(t, src, "127.0.0.1")
			o.Backoff = Backoff{Steps: []time.Duration{time.Millisecond}}
		})
		_, err := e.dl.Fetch(context.Background(), e.job(src.URL+"/f", data), e.cp)
		if !errors.Is(err, ErrExhausted) {
			t.Fatalf("err = %v", err)
		}
		if hits := src.hitCount(); hits != 2*MaxRedirects { // ten hops per attempt, two attempts
			t.Errorf("hits = %d, want %d", hits, 2*MaxRedirects)
		}
	})
}

func TestCheckRedirectRules(t *testing.T) {
	mk := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u, Header: http.Header{"Authorization": {"Bearer x"}}}
	}
	via := []*http.Request{mk("https://huggingface.co/a")}
	for _, tc := range []struct {
		name     string
		to       string
		wantErr  error
		wantAuth bool
	}{
		{"same host", "https://huggingface.co/b", nil, true},
		{"same host other case", "https://HuggingFace.co/b", nil, true},
		{"subdomain (Go would keep the header)", "https://cdn.huggingface.co/b", nil, false},
		{"other host", "https://cdn.example.test/b", nil, false},
		{"http", "http://huggingface.co/b", ErrRedirectHTTP, true},
	} {
		req := mk(tc.to)
		err := checkRedirect(req, via)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if tc.wantErr == nil && (req.Header.Get("Authorization") != "") != tc.wantAuth {
			t.Errorf("%s: Authorization kept = %v, want %v", tc.name, req.Header.Get("Authorization") != "", tc.wantAuth)
		}
	}
	long := make([]*http.Request, MaxRedirects)
	for i := range long {
		long[i] = via[0]
	}
	if err := checkRedirect(mk("https://huggingface.co/z"), long); err == nil {
		t.Error("the eleventh redirect must be refused")
	}
}

// No secret reaches a log line or an error: not the token, not a signed URL's query, not a full
// digest.
func TestNoSecretsInLogsOrErrors(t *testing.T) {
	data := genData(testSize, 43)
	flipped := append([]byte(nil), data...)
	flipped[7] ^= 1
	const query = "?X-Amz-Signature=SIGSECRET123&token=" + testToken
	var all []string
	collect := func(e *env, errs ...error) {
		all = append(all, e.logs.String())
		for _, err := range errs {
			if err != nil {
				all = append(all, err.Error())
			}
		}
	}

	// success after a transient failure
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	srv.setHandler(statusHandler([]int{503}, nil))
	j := e.job(srv.URL+"/f"+query, data)
	j.Auth = true
	_, err := e.dl.Fetch(context.Background(), j, e.cp)
	if err != nil {
		t.Fatal(err)
	}
	collect(e)

	// permanent failure
	e = newEnv(t, nil)
	srv2 := newTestServer(t, data)
	srv2.setHandler(statusHandler([]int{404}, nil))
	_, err = e.dl.Fetch(context.Background(), e.job(srv2.URL+"/f"+query, data), e.cp)
	collect(e, err)

	// checksum mismatch
	e = newEnv(t, nil)
	srv3 := newTestServer(t, flipped)
	_, err = e.dl.Fetch(context.Background(), e.job(srv3.URL+"/f"+query, data), e.cp)
	collect(e, err)
	if err == nil {
		t.Fatal("expected a mismatch")
	}

	// connection refused (the URL is in the client's own error)
	e = newEnv(t, func(o *Options) { o.Backoff = Backoff{Steps: []time.Duration{time.Millisecond}} })
	srv4 := newTestServer(t, data)
	dead := srv4.URL
	srv4.Close()
	_, err = e.dl.Fetch(context.Background(), e.job(dead+"/f"+query, data), e.cp)
	collect(e, err)
	if err == nil {
		t.Fatal("expected a connection error")
	}

	// redirect to http: the client's error repeats the target URL
	e = newEnv(t, nil)
	srv5 := newTestServer(t, data)
	tls := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv5.URL+"/f"+query, http.StatusFound)
	})
	e.opts.HTTP = tlsClient(t, tls, "127.0.0.1")
	e.rebuild()
	_, err = e.dl.Fetch(context.Background(), e.job(tls.URL+"/f"+query, data), e.cp)
	collect(e, err)

	full := []string{testToken, "SIGSECRET123", "X-Amz-Signature", sum(flipped), sum(data)}
	for _, text := range all {
		for _, secret := range full {
			if strings.Contains(text, secret) {
				t.Errorf("%q leaked into:\n%s", secret, text)
			}
		}
	}
	if !strings.Contains(strings.Join(all, "\n"), ShortHash(sum(flipped))) {
		t.Error("the mismatch must be logged with the digest as first4...last2")
	}
}
