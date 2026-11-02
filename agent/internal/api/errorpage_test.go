package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestWantsHTML(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"*/*", false},
		{"application/json", false},
		{"text/event-stream", false},
		{"text/html", true},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", true},
		{"application/json, text/html;q=0.5", true},
		{"text/html;q=0", false},
		{"TEXT/HTML", true},
		{"text/htmlx", false},
		{"garbage;;;", false},
	}
	for _, tc := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		if got := WantsHTML(r); got != tc.want {
			t.Errorf("WantsHTML(%q) = %v, want %v", tc.accept, got, tc.want)
		}
	}
}

func pageReq(accept, lang string) *http.Request {
	r := httptest.NewRequest("GET", "/chat/", nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	if lang != "" {
		r.Header.Set("Accept-Language", lang)
	}
	return r
}

func TestWriteErrorPage(t *testing.T) {
	down := NewError(CodeChatDown, http.StatusBadGateway, map[string]any{"reason": "upstream_down"})

	t.Run("browser gets an English page", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("text/html", ""), down)
		if w.Code != 502 {
			t.Fatalf("status = %d", w.Code)
		}
		body := w.Body.String()
		for _, want := range []string{`lang="en"`, "The AI (llama) isn&#39;t answering", "LAI-SVC-102", "What to do", `href="/portal"`, "Back to the Portal"} {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q:\n%s", want, body)
			}
		}
		h := w.Header()
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q", got)
		}
		if h.Get("Content-Security-Policy") != HTMLContentSecurityPolicy || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "SAMEORIGIN" {
			t.Errorf("global HTML headers missing: %v", h)
		}
		if h.Get("Cache-Control") != "no-store" || h.Get("Content-Language") != "en" {
			t.Errorf("headers: %v", h)
		}
	})
	t.Run("browser in Spanish", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := pageReq("text/html", "es-CR,es;q=0.9,en;q=0.5")
		// The server's first middleware resolves the language from Accept-Language.
		r = r.WithContext(WithRequestInfo(r.Context(), &RequestInfo{Lang: resolveLang(r.Header.Get("Accept-Language"), "")}))
		WriteErrorPage(w, r, down)
		body := w.Body.String()
		for _, want := range []string{`lang="es"`, "La IA (llama) no responde", "Qué hacer", "Volver al Portal", "LAI-SVC-102"} {
			if !strings.Contains(body, want) {
				t.Errorf("página sin %q", want)
			}
		}
		if w.Header().Get("Content-Language") != "es" {
			t.Errorf("Content-Language = %q", w.Header().Get("Content-Language"))
		}
	})
	t.Run("ui language wins", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := pageReq("text/html", "en")
		r = r.WithContext(WithRequestInfo(r.Context(), &RequestInfo{ID: "req_abcdefghijkl", Lang: langES}))
		WriteErrorPage(w, r, down)
		if !strings.Contains(w.Body.String(), "La IA (llama) no responde") || !strings.Contains(w.Body.String(), "req_abcdefghijkl") {
			t.Fatalf("body: %s", w.Body.String())
		}
	})
	t.Run("API client gets the envelope", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("application/json", ""), down)
		e := expectError(t, w, 502, CodeChatDown)
		if e.Error.Message != "The AI (llama) isn't answering" || e.Error.Details["reason"] != "upstream_down" {
			t.Fatalf("envelope = %+v", e)
		}
	})
	t.Run("no Accept header is an API client", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("", ""), down)
		expectError(t, w, 502, CodeChatDown)
	})
	t.Run("library down is 503 with its code", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("text/html", ""), NewError(CodeLibraryDown, http.StatusServiceUnavailable, nil))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "LAI-SVC-002") || !strings.Contains(w.Body.String(), "The library service isn&#39;t running") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("message and fix of the error win", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("text/html", ""), down.WithMessage("Custom words.", "Custom fix."))
		if !strings.Contains(w.Body.String(), "Custom words.") || !strings.Contains(w.Body.String(), "Custom fix.") {
			t.Fatal(w.Body.String())
		}
	})
	t.Run("text is escaped", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("text/html", ""), down.WithMessage(`<script>alert(1)</script>`, `"><img src=x onerror=alert(2)>`))
		body := w.Body.String()
		if strings.Contains(body, "<script>") || strings.Contains(body, "<img") {
			t.Fatalf("unescaped markup: %s", body)
		}
	})
	t.Run("custom links, Retry-After, other headers", func(t *testing.T) {
		w := httptest.NewRecorder()
		e := NewError(CodeStarting, http.StatusServiceUnavailable, nil)
		e.Header = http.Header{"X-Test": []string{"1"}}
		WriteErrorPageLinks(w, pageReq("text/html", ""), e, []PageLink{{"/docs/", "Manuals"}})
		if w.Header().Get("Retry-After") != "5" || w.Header().Get("X-Test") != "1" {
			t.Fatalf("headers: %v", w.Header())
		}
		if !strings.Contains(w.Body.String(), `href="/docs/"`) || strings.Contains(w.Body.String(), `href="/portal"`) {
			t.Fatalf("links: %s", w.Body.String())
		}
	})
	t.Run("a plain error is a 500 page", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteErrorPage(w, pageReq("text/html", ""), http.ErrAbortHandler)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "LAI-AGT-099") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("the access log learns the code", func(t *testing.T) {
		info := &RequestInfo{}
		r := pageReq("text/html", "")
		r = r.WithContext(WithRequestInfo(r.Context(), info))
		WriteErrorPage(httptest.NewRecorder(), r, down)
		if info.ErrCode != CodeChatDown {
			t.Fatalf("ErrCode = %q", info.ErrCode)
		}
	})
}

// The page loads nothing: no scripts, no external URL, no web font (AGENTS.md rule 3).
func TestErrorTemplatesAreSelfContained(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)<script|<link|<img|<iframe|<object|@import|@font-face|url\(|src=|https?:|//[a-z]`)
	for _, name := range []string{"templates/error_en.html", "templates/error_es.html"} {
		b, err := fs.ReadFile(errorTemplates, name)
		if err != nil {
			t.Fatal(err)
		}
		if loc := forbidden.FindIndex(b); loc != nil {
			t.Errorf("%s loads or links something external: %q", name, b[loc[0]:loc[1]])
		}
	}
}

func TestCodeText(t *testing.T) {
	m, f := CodeText(CodeLibraryDown, "es")
	if m != "El servicio de la biblioteca no se está ejecutando" || f == "" {
		t.Fatalf("%q %q", m, f)
	}
	if m, f = CodeText("LAI-AGT-021", "en"); m == "" || f == "" {
		t.Fatal("empty text for a known code")
	}
	// A code the doctor table does not have (built at run time: the check forbids undefined literals).
	if m, _ = CodeText(strings.Join([]string{"LAI", "ZZZ", "999"}, "-"), "fr"); m != unknownText[langEN].Message {
		t.Fatalf("unknown code text = %q", m)
	}
}
