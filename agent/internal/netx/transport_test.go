package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransportRefusesAndAttributesTheCaller(t *testing.T) {
	f := NewFence()
	tr := f.NewTransport("catalog.online", nil)
	client := &http.Client{Transport: Via(tr, "catalog.online")}
	l := f.Acquire()
	defer l.Release()

	req, _ := http.NewRequest(http.MethodGet, "https://huggingface.co/", nil)
	_, err := client.Do(req)
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("err = %v, want a fence refusal through the http client", err)
	}
	// An explicit caller on the request wins over the transport's name.
	req2, _ := http.NewRequestWithContext(WithCaller(context.Background(), "cloud"), http.MethodGet, "https://cloud.example.test/", nil)
	if _, err := client.Do(req2); !errors.Is(err, ErrFenced) {
		t.Fatal(err)
	}
	rep := l.Release()
	if rep.Blocked != 2 {
		t.Fatalf("blocked = %d, want 2", rep.Blocked)
	}
	got := map[string]string{}
	for _, d := range rep.Dials {
		got[d.Addr] = d.Caller
	}
	if got["huggingface.co:443"] != "catalog.online" || got["cloud.example.test:443"] != "cloud" {
		t.Errorf("callers = %v", got)
	}
}

func TestTransportServesLoopbackDuringTheFence(t *testing.T) {
	f := NewFence()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") }))
	defer srv.Close()
	client := &http.Client{Transport: f.NewTransport("t", nil)}
	l := f.Acquire()
	defer l.Release()
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback request refused: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "pong" {
		t.Errorf("body = %q", body)
	}
	if r := l.Release(); r.Blocked != 0 {
		t.Errorf("blocked = %d", r.Blocked)
	}
}

func TestTransportIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.test:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example.test:3128")
	if Shared().Proxy != nil || NewTransport("x", nil).Proxy != nil {
		t.Fatal("netx transports must not read the proxy environment")
	}
}

func TestSharedIsOneTransportAndUsesTheDefaultFence(t *testing.T) {
	first, second := Shared(), Shared()
	if first != second {
		t.Fatal("Shared must return one transport")
	}
	l := Default.Acquire()
	defer l.Release()
	_, err := Client("tester").Get("https://huggingface.co/")
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("err = %v", err)
	}
	_, derr := DialContext(context.Background(), "tcp", "huggingface.co:443")
	if !errors.Is(derr, ErrFenced) {
		t.Fatalf("DialContext err = %v", derr)
	}
	_, derr = DialFor("gps", 0)(context.Background(), "tcp", "8.8.8.8:2947")
	if !errors.Is(derr, ErrFenced) {
		t.Fatalf("DialFor err = %v", derr)
	}
	rep := l.Release()
	if rep.Blocked != 3 {
		t.Fatalf("blocked = %d: %+v", rep.Blocked, rep)
	}
	found := false
	for _, d := range rep.Dials {
		if d.Caller == "tester" && d.Addr == "huggingface.co:443" {
			found = true
		}
	}
	if !found {
		t.Errorf("the Client's caller was not recorded: %+v", rep.Dials)
	}
}

func TestUnixTransportIsNeverFenced(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "unix-ok") })}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	l := Default.Acquire()
	defer l.Release()
	c := &http.Client{Transport: UnixTransport(sock)}
	resp, err := c.Get("http://agent/api/v1/health")
	if err != nil {
		t.Fatalf("unix request failed: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "unix-ok" {
		t.Errorf("body = %q", b)
	}
	if r := l.Release(); r.Blocked != 0 {
		t.Errorf("blocked = %d", r.Blocked)
	}
}

func TestDeadProxyEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HTTP_PROXY=http://corp.example.test:3128", "https_proxy=http://corp.example.test:3128",
		"ALL_PROXY=socks5://x", "no_proxy=localhost", "FTP_PROXY=y", "HOME=/root", "LANG=C", "PROXYLESS=1", "malformed",
	}
	got := DeadProxyEnv(in)
	want := []string{"PATH=/usr/bin", "HOME=/root", "LANG=C", "PROXYLESS=1", "malformed",
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("env = %v\nwant  %v", got, want)
	}
	if len(in) != 10 {
		t.Error("DeadProxyEnv modified its input")
	}
	// Nothing in: still the two dead entries.
	if g := DeadProxyEnv(nil); len(g) != 2 {
		t.Errorf("env = %v", g)
	}
}

func TestAcquireClosesIdleConnectionsSoNoPooledConnectionOutlivesTheFence(t *testing.T) {
	f := NewFence()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") }))
	defer srv.Close()
	dials := 0
	inner := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials++
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	client := &http.Client{Transport: f.NewTransport("t", inner)}
	get := func() {
		t.Helper()
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	get()
	get()
	if dials != 1 {
		t.Fatalf("dials = %d before the fence, want 1 (the connection is pooled)", dials)
	}
	l := f.Acquire()
	get()
	if dials != 2 {
		t.Errorf("dials = %d, want 2: the fence must close pooled connections so every request dials again", dials)
	}
	l.Release()
}
