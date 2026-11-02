package modes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestProbeClientIsDedicated(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example.test:3128")
	c := newProbeClient(3 * time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Error("the prober must not use proxy environment variables")
	}
	if !tr.DisableKeepAlives {
		t.Error("keep-alives must be off")
	}
	if c.Timeout != 3*time.Second || tr.TLSHandshakeTimeout != 3*time.Second || tr.ResponseHeaderTimeout != 3*time.Second {
		t.Errorf("timeouts: client %v tls %v header %v", c.Timeout, tr.TLSHandshakeTimeout, tr.ResponseHeaderTimeout)
	}
	if http.DefaultTransport.(*http.Transport) == tr {
		t.Error("the client must not share the default transport")
	}
}

func TestProbeClientReportsRedirectsInsteadOfFollowing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/start", nil)
	resp, err := newProbeClient(3 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits != 1 {
		t.Errorf("status %d after %d requests, want the 302 itself", resp.StatusCode, hits)
	}
}

func TestIsTimeout(t *testing.T) {
	to := &url.Error{Op: "Get", Err: context.DeadlineExceeded}
	if !isTimeout(to) {
		t.Error("deadline exceeded is a timeout")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer srv.Close()
	_, err := newProbeClient(50 * time.Millisecond).Get(srv.URL)
	if err == nil || !isTimeout(err) {
		t.Errorf("a client timeout is a timeout: %v", err)
	}
	if isTimeout(errors.New("connection refused")) || isTimeout(&net.OpError{Op: "dial", Err: errors.New("refused")}) {
		t.Error("a refusal is not a timeout")
	}
}
