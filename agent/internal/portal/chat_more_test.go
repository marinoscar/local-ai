package portal_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

// The enabling rule of survival-portal.md section 4.4: {survival, online} x {no auth, full session,
// Portal session}.
func TestChatEnableMatrix(t *testing.T) {
	for _, mode := range []string{"survival", "normal"} {
		for _, pr := range []struct {
			name string
			kind api.PrincipalKind
		}{{"none", api.KindAnonymous}, {"operator", api.KindOperator}, {"admin", api.KindAdmin}, {"portal", api.KindPortal}} {
			want := 200
			if mode != "survival" && pr.kind == api.KindAnonymous {
				want = 403
			}
			t.Run(mode+"/"+pr.name, func(t *testing.T) {
				up := newLlama(t)
				h := chatHandler(t, up, mode, &fakeOps{}, "GET")
				rec := serveChat(h, chatCase{method: "GET", path: "/chat/props", principal: pr.kind, headers: map[string]string{"Accept": "text/html"}})
				if rec.Code != want {
					t.Fatalf("status %d, want %d", rec.Code, want)
				}
				if want == 403 {
					for _, s := range []string{"LAI-SVC-101", "/portal", "Log in"} {
						if !strings.Contains(rec.Body.String(), s) {
							t.Errorf("locked page lacks %q", s)
						}
					}
					if up.count() != 0 {
						t.Error("a locked request reached llama")
					}
				}
			})
		}
	}
}

func TestChatPagesFollowTheUILanguage(t *testing.T) {
	up := newLlama(t)
	h := chatHandler(t, up, "normal", &fakeOps{}, "GET")
	u := "http://node.test/chat/"
	req := httptest.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "text/html")
	req = req.WithContext(api.WithRequestInfo(req.Context(), &api.RequestInfo{Transport: api.TransportTCP, Lang: "es"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "Iniciar modo supervivencia") || !strings.Contains(rec.Body.String(), "LAI-SVC-101") {
		t.Fatalf("locked page: %d %q", rec.Code, rec.Body.String())
	}

	port := up.port()
	up.srv.Close()
	routes := portal.ChatRoutes(portal.ChatOptions{Port: port, Mode: func() string { return "survival" }})
	req = httptest.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "text/html")
	req = req.WithContext(api.WithRequestInfo(req.Context(), &api.RequestInfo{Transport: api.TransportTCP, Lang: "es"}))
	rec = httptest.NewRecorder()
	routes[0].Handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 502 || !strings.Contains(body, "LAI-SVC-102") || !strings.Contains(body, "/portal/brain") || !strings.Contains(body, "/portal/status") || !strings.Contains(body, "Cambiar de cerebro") {
		t.Fatalf("down page: %d %q", rec.Code, body)
	}
}

func TestChatHostAndForwardedHeaders(t *testing.T) {
	var gotHost, gotFwd, gotForwarded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotFwd, gotForwarded = r.Host, r.Header.Get("X-Forwarded-For"), r.Header.Get("Forwarded")
	}))
	defer srv.Close()
	up := &llamaFake{srv: srv}
	h := chatHandler(t, up, "survival", &fakeOps{}, "GET")
	rec := serveChat(h, chatCase{method: "GET", path: "/chat/props", headers: map[string]string{"X-Forwarded-For": "203.0.113.9", "Forwarded": "for=203.0.113.9"}})
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if gotHost != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("upstream Host = %q, want the upstream's own", gotHost)
	}
	if gotFwd != "" || gotForwarded != "" {
		t.Errorf("client forwarding headers reached llama: %q %q", gotFwd, gotForwarded)
	}
}

func TestChatOriginOnEveryWrite(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, o := range []struct {
			origin string
			want   int
		}{{"", 403}, {"http://evil.test", 403}, {"null", 403}, {"http://node.test", 403 + 0}, {"localai://app", 403 + 0}} {
			t.Run(m+" "+o.origin, func(t *testing.T) {
				up := newLlama(t)
				h := chatHandler(t, up, "survival", &fakeOps{}, m)
				hdr := map[string]string{}
				if o.origin != "" {
					hdr["Origin"] = o.origin
				}
				// /v1/chat/completions is the only write that is also allowed by the path table, and only POST.
				rec := serveChat(h, chatCase{method: m, path: "/chat/v1/chat/completions", body: "{}", headers: hdr})
				good := o.origin == "http://node.test" || o.origin == "localai://app"
				switch {
				case !good && rec.Code != 403:
					t.Fatalf("a bad origin got %d", rec.Code)
				case !good && up.count() != 0:
					t.Fatal("a bad origin reached llama")
				case good && m == "POST" && rec.Code != 200:
					t.Fatalf("a good origin got %d", rec.Code)
				case good && m != "POST" && !strings.Contains(rec.Body.String(), "LAI-SVC-115"):
					t.Fatalf("%s with a good origin should be refused by the path table, got %d %q", m, rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestChatBodyLimit(t *testing.T) {
	up := newLlama(t)
	h := chatHandler(t, up, "survival", &fakeOps{}, "POST")
	big := strings.Repeat("a", int(portal.ChatBodyLimit)+1)
	rec := serveChat(h, chatCase{method: "POST", path: "/chat/v1/chat/completions", body: big, headers: map[string]string{"Origin": "http://node.test"}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
}

// A token stream reaches the browser as it is produced: the first chunk is readable while llama is
// still writing.
func TestChatStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	defer close(release)
	up := &llamaFake{srv: srv}
	h := chatHandler(t, up, "survival", &fakeOps{}, "POST")
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(api.WithRequestInfo(r.Context(), &api.RequestInfo{Transport: api.TransportTCP, Lang: "en", MaxBody: portal.ChatBodyLimit}))
		h.ServeHTTP(w, r)
	}))
	defer front.Close()
	req, _ := http.NewRequest("POST", front.URL+"/chat/v1/chat/completions", bytes.NewReader([]byte("{}")))
	req.Header.Set("Origin", front.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, "first") {
			t.Fatalf("first chunk %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk was held back until llama finished")
	}
}

// Key rotation: the Key function is asked per request, so a new key is used from the next request.
func TestChatPicksUpARotatedKey(t *testing.T) {
	up := newLlama(t)
	var key atomic.Value
	key.Store("key-one")
	routes := portal.ChatRoutes(portal.ChatOptions{
		Port: up.port(), Key: func() (string, error) { return key.Load().(string), nil },
		Mode: func() string { return "survival" },
	})
	for _, k := range []string{"key-one", "key-two"} {
		key.Store(k)
		rec := serveChat(routes[0].Handler, chatCase{method: "GET", path: "/chat/props"})
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	}
	if len(up.reqs) != 2 || up.reqs[0].auth != "Bearer key-one" || up.reqs[1].auth != "Bearer key-two" {
		t.Fatalf("upstream auth = %+v", up.reqs)
	}
}
