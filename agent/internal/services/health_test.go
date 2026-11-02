package services_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/services"
)

func TestProbesGolden(t *testing.T) {
	got := services.Probes(services.Ports{Llama: 8080, Kiwix: 8888, Whisper: 8081})
	want := []services.Probe{
		{Service: "llama", URL: "http://127.0.0.1:8080/health", Timeout: 2 * time.Second, Interval: 15 * time.Second},
		{Service: "kiwix", URL: "http://127.0.0.1:8888/library/catalog/v2/entries?count=1", Timeout: 3 * time.Second, Interval: 30 * time.Second},
		{Service: "whisper", URL: "http://127.0.0.1:8081/", Timeout: 3 * time.Second, Interval: 30 * time.Second},
	}
	if len(got) != len(want) {
		t.Fatalf("%d probes", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("probe %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func probeFor(t *testing.T, srv *httptest.Server, path string, timeout time.Duration) services.Probe {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return services.Probe{Service: "llama", URL: "http://" + u.Host + path, Timeout: timeout, Interval: time.Second}
}

func TestHTTPProberAnswers(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/loading", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading model", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/health", http.StatusFound) })
	mux.HandleFunc("/created", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 1<<20))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := services.NewHTTPProber()
	ctx := context.Background()

	tests := []struct {
		name    string
		path    string
		timeout time.Duration
		wantErr bool
	}{
		{"200 is healthy", "/health?count=1", time.Second, false},
		{"a big 200 body is fine", "/big", time.Second, false},
		{"503 while the model loads is not healthy", "/loading", time.Second, true},
		{"a redirect is not followed", "/moved", time.Second, true},
		{"201 is not a health answer", "/created", time.Second, true},
		{"unknown path is 404", "/nope", time.Second, true},
		{"an answer after the timeout is not healthy", "/slow", 80 * time.Millisecond, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Check(ctx, probeFor(t, srv, tc.path, tc.timeout))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
	if gotMethod != http.MethodGet || gotPath != "/health" || gotQuery != "count=1" {
		t.Errorf("request = %s %s?%s", gotMethod, gotPath, gotQuery)
	}
}

func TestHTTPProberNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	pr := services.Probe{Service: "kiwix", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/", Timeout: time.Second, Interval: time.Second}
	if err := services.NewHTTPProber().Check(context.Background(), pr); err == nil {
		t.Fatal("a closed port reported healthy")
	}
	// A cancelled context ends the probe too.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := services.NewHTTPProber().Check(ctx, pr); err == nil {
		t.Fatal("a cancelled probe reported healthy")
	}
	bad := services.Probe{Service: "x", URL: "http://[::1", Timeout: time.Second}
	if err := services.NewHTTPProber().Check(context.Background(), bad); err == nil {
		t.Fatal("a malformed URL reported healthy")
	}
}
