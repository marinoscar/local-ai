package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// fixedConfig is a ConfigSource with a given ui.language and agent port.
type fixedConfig struct {
	lang string
	port int
}

func (f fixedConfig) Get() config.Config {
	c := config.Defaults()
	c.UI.Language = f.lang
	if f.port != 0 {
		c.Ports.Agent = f.port
	}
	return c
}

func TestEnvelopeGolden(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	WriteError(rec, r, NewError("LAI-LLM-009", 502, map[string]any{"model": "model-x", "exit_code": 1, "rolled_back": true}).
		WithMessage("The model model-x failed to start. The previous model was started again.",
			"Run `aictl logs --unit llama` to see why, or choose a smaller model with `aictl models --loadable`."))
	want := `{"error":{"code":"LAI-LLM-009","message":"The model model-x failed to start. The previous model was started again.","fix":"Run ` + "`aictl logs --unit llama`" + ` to see why, or choose a smaller model with ` + "`aictl models --loadable`" + `.","details":{"exit_code":1,"model":"model-x","rolled_back":true}}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("envelope:\n got %s\nwant %s", got, want)
	}
	if rec.Code != 502 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestEnvelopeDefaultsAndDetails(t *testing.T) {
	t.Run("message and fix come from the code table, details is always an object", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), NewError(CodeNotFound, 404, nil))
		want := `{"error":{"code":"LAI-AGT-021","message":"The thing asked for does not exist (model, service, content, route).","fix":"Check the name with ` + "`aictl models` or `aictl status`" + `.","details":{}}}` + "\n"
		if rec.Body.String() != want {
			t.Fatalf("got %s", rec.Body.String())
		}
	})
	t.Run("an unknown code still gets a message and a fix", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), NewError("LAI-SYN-001", 422, nil))
		e := expectError(t, rec, 422, "LAI-SYN-001")
		if e.Error.Message == "" || e.Error.Fix == "" {
			t.Fatal("empty message")
		}
	})
	t.Run("the 503 starting error carries Retry-After 5", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), NewError(CodeStarting, 503, nil))
		if rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
	})
	t.Run("a 429 can carry its own Retry-After", func(t *testing.T) {
		rec := httptest.NewRecorder()
		e := NewError(CodeRateLimited, 429, nil)
		e.RetryAfter = 120
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), e)
		if rec.Header().Get("Retry-After") != "120" {
			t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
	})
	t.Run("the caller's details map is never modified", func(t *testing.T) {
		d := map[string]any{"a": 1}
		e := NewError(CodeInternal, 500, d)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x", nil).WithContext(context.WithValue(context.Background(), infoKey{}, &RequestInfo{ID: "req_abcdefgh2345", Lang: "en"}))
		WriteError(rec, r, e)
		if len(d) != 1 {
			t.Fatalf("details modified: %v", d)
		}
	})
	t.Run("every 500 carries request_id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/x", nil).WithContext(context.WithValue(context.Background(), infoKey{}, &RequestInfo{ID: "req_abcdefgh2345", Lang: "en"}))
		WriteError(rec, r, NewError(CodeInternal, 500, nil))
		e := expectError(t, rec, 500, CodeInternal)
		if e.Error.Details["request_id"] != "req_abcdefgh2345" {
			t.Fatalf("details = %v", e.Error.Details)
		}
	})
	t.Run("an out-of-range status becomes 500", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/x", nil), NewError(CodeInternal, 200, nil))
		if rec.Code != 500 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestErrorFrom(t *testing.T) {
	apiErr := NewError(CodeBusy, 409, nil)
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"api error passes through", apiErr, 409, CodeBusy},
		{"wrapped api error", fmt.Errorf("while loading: %w", apiErr), 409, CodeBusy},
		{"max bytes", &http.MaxBytesError{Limit: 5}, 413, CodeTooLarge},
		{"deadline", context.DeadlineExceeded, 504, CodeInternal},
		{"wrapped deadline", fmt.Errorf("upstream: %w", context.DeadlineExceeded), 504, CodeInternal},
		{"anything else", errors.New("disk exploded"), 500, CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := ErrorFrom(tc.err, "req_abcdefgh2345")
			if e.Status != tc.status || e.Code != tc.code {
				t.Fatalf("got %d %s", e.Status, e.Code)
			}
		})
	}
	if (&Error{Code: CodeBusy}).Error() != "LAI-AGT-022: Another operation is already running." {
		t.Fatalf("Error() = %q", (&Error{Code: CodeBusy}).Error())
	}
}

func TestSpanishLocalization(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()
	t.Run("Accept-Language es", func(t *testing.T) {
		rec := do(h, req{target: "/api/v1/nope", header: map[string]string{"Accept-Language": "es"}})
		e := expectError(t, rec, 404, CodeNotFound)
		if e.Error.Message != "Lo solicitado no existe (modelo, servicio, contenido o ruta)." || !strings.HasPrefix(e.Error.Fix, "Revise el nombre") {
			t.Fatalf("not Spanish: %+v", e.Error)
		}
	})
	t.Run("English by default, same code and keys", func(t *testing.T) {
		e := expectError(t, do(h, req{target: "/api/v1/nope"}), 404, CodeNotFound)
		if !strings.HasPrefix(e.Error.Message, "The thing asked for") {
			t.Fatalf("not English: %+v", e.Error)
		}
	})
	t.Run("ui.language es", func(t *testing.T) {
		s2, _ := newTestServer(t, func(o *Options) { o.Config = fixedConfig{lang: "es"} })
		e := expectError(t, do(s2.Handler(), req{target: "/api/v1/nope"}), 404, CodeNotFound)
		if !strings.HasPrefix(e.Error.Fix, "Revise") {
			t.Fatalf("not Spanish: %+v", e.Error)
		}
	})
	t.Run("a hot-reloaded ui.language applies to the next error", func(t *testing.T) {
		cfg := &swapConfig{lang: "auto"}
		s2, _ := newTestServer(t, func(o *Options) { o.Config = cfg })
		if e := expectError(t, do(s2.Handler(), req{target: "/api/v1/nope"}), 404, CodeNotFound); !strings.HasPrefix(e.Error.Message, "The thing") {
			t.Fatal("expected English first")
		}
		cfg.lang = "es"
		if e := expectError(t, do(s2.Handler(), req{target: "/api/v1/nope"}), 404, CodeNotFound); !strings.HasPrefix(e.Error.Message, "Lo solicitado") {
			t.Fatal("expected Spanish after the reload")
		}
	})
	t.Run("every code in the table has both languages and distinct text", func(t *testing.T) {
		for code, byLang := range messages {
			en, es := byLang[langEN], byLang[langES]
			if en.Message == "" || en.Fix == "" || es.Message == "" || es.Fix == "" {
				t.Errorf("%s: missing text", code)
			}
			if en == es {
				t.Errorf("%s: Spanish equals English", code)
			}
		}
	})
	t.Run("the Spanish refusal for a bad Host", func(t *testing.T) {
		e := expectError(t, do(h, req{target: "/api/v1/open", host: "evil:7070", header: map[string]string{"Accept-Language": "es-CR,es;q=0.9"}}), 403, CodeBadHost)
		if !strings.Contains(e.Error.Message, "dirección incorrecta") {
			t.Fatalf("%+v", e.Error)
		}
	})
}

type swapConfig struct{ lang string }

func (s *swapConfig) Get() config.Config {
	c := config.Defaults()
	c.UI.Language = s.lang
	return c
}

func TestResolveLang(t *testing.T) {
	tests := []struct {
		accept, ui, want string
	}{
		{"", "", "en"},
		{"es", "", "es"},
		{"es-CR", "", "es"},
		{"ES", "", "es"},
		{"en-US,en;q=0.9", "", "en"},
		{"es;q=0.5, en;q=0.9", "", "en"},
		{"en;q=0.5, es;q=0.9", "", "es"},
		{"fr, es;q=0.8", "", "es"},
		{"fr, de", "", "en"},
		{"es;q=0", "", "en"},
		{"*", "", "en"},
		{"es;q=oops", "", "en"},
		{"", "es", "es"},
		{"", "auto", "en"},
		{"", "en", "en"},
		{"en", "es", "es"},
		{"es", "en", "es"},
		{"en,es", "", "en"},
	}
	for _, tc := range tests {
		if got := resolveLang(tc.accept, tc.ui); got != tc.want {
			t.Errorf("resolveLang(%q, %q) = %s, want %s", tc.accept, tc.ui, got, tc.want)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name string `json:"name"`
		N    int    `json:"n"`
		In   struct {
			Flag bool `json:"flag"`
		} `json:"in"`
	}
	run := func(raw string, fn func(http.ResponseWriter, *http.Request, any) error) (body, error) {
		var b body
		r := httptest.NewRequest("POST", "/x", strings.NewReader(raw))
		err := fn(httptest.NewRecorder(), r, &b)
		return b, err
	}
	strict := func(w http.ResponseWriter, r *http.Request, dst any) error { return DecodeJSON(w, r, dst) }
	optional := func(w http.ResponseWriter, r *http.Request, dst any) error { return DecodeJSONOrEmpty(w, r, dst) }

	tests := []struct {
		name    string
		raw     string
		fn      func(http.ResponseWriter, *http.Request, any) error
		status  int // 0: success
		code    string
		reason  string
		field   string
		checkOK func(body) bool
	}{
		{name: "valid", raw: `{"name":"a","n":2}`, fn: strict, checkOK: func(b body) bool { return b.Name == "a" && b.N == 2 }},
		{name: "valid with trailing whitespace", raw: "{\"name\":\"a\"}\n \n", fn: strict, checkOK: func(b body) bool { return b.Name == "a" }},
		{name: "unknown field", raw: `{"name":"a","nmae":"b"}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "unknown_field", field: "nmae"},
		{name: "unknown nested field", raw: `{"in":{"flag":true,"extra":1}}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "unknown_field", field: "extra"},
		{name: "unknown field with quote", raw: `{"a\"b":1}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "unknown_field", field: `a"b`},
		{name: "wrong type", raw: `{"n":"two"}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "wrong_type", field: "n"},
		{name: "wrong nested type", raw: `{"in":{"flag":"yes"}}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "wrong_type", field: "in.flag"},
		{name: "malformed", raw: `{"name":`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "malformed_json"},
		{name: "not json", raw: `hello`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "malformed_json"},
		{name: "empty body", raw: ``, fn: strict, status: 400, code: CodeInvalidRequest, reason: "empty_body"},
		{name: "second value", raw: `{"name":"a"}{"name":"b"}`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "trailing_data"},
		{name: "garbage after the value", raw: `{"name":"a"} x`, fn: strict, status: 400, code: CodeInvalidRequest},
		{name: "array where object expected", raw: `[1]`, fn: strict, status: 400, code: CodeInvalidRequest, reason: "wrong_type"},
		{name: "optional: empty is fine", raw: ``, fn: optional, checkOK: func(b body) bool { return b.Name == "" }},
		{name: "optional: still strict", raw: `{"x":1}`, fn: optional, status: 400, code: CodeInvalidRequest, reason: "unknown_field", field: "x"},
		{name: "over the limit", raw: `{"name":"` + strings.Repeat("a", int(DefaultMaxBody)) + `"}`, fn: strict, status: 413, code: CodeTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := run(tc.raw, tc.fn)
			if tc.status == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.checkOK != nil && !tc.checkOK(got) {
					t.Fatalf("decoded %+v", got)
				}
				return
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v is not an *Error", err)
			}
			if e.Status != tc.status || e.Code != tc.code {
				t.Fatalf("got %d %s, want %d %s", e.Status, e.Code, tc.status, tc.code)
			}
			if tc.reason != "" && e.Details["reason"] != tc.reason {
				t.Fatalf("reason = %v, want %s", e.Details["reason"], tc.reason)
			}
			if tc.field != "" && e.Details["field"] != tc.field {
				t.Fatalf("field = %v, want %s", e.Details["field"], tc.field)
			}
		})
	}

	t.Run("a nil body is an empty body", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Body = nil
		var b body
		if err := DecodeJSON(httptest.NewRecorder(), r, &b); err == nil {
			t.Fatal("want empty_body")
		}
	})
	t.Run("the route limit applies", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"abcdefghij"}`))
		r = r.WithContext(context.WithValue(r.Context(), infoKey{}, &RequestInfo{MaxBody: 8}))
		var b body
		err := DecodeJSON(httptest.NewRecorder(), r, &b)
		var e *Error
		if !errors.As(err, &e) || e.Status != 413 || e.Details["limit"] != int64(8) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("through the server: unknown field names the field", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := do(s.Handler(), req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{"name":"a","bogus":1}`})
		e := expectError(t, rec, 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "bogus" {
			t.Fatalf("details = %v", e.Error.Details)
		}
	})
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, httptest.NewRequest("GET", "/x", nil), 201, map[string]any{"a": "<b>&"})
	if rec.Code != 201 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "{\"a\":\"<b>&\"}\n" {
		t.Fatalf("body %q (HTML must not be escaped)", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteJSON(rec, httptest.NewRequest("GET", "/x", nil), 200, map[string]any{"bad": make(chan int)})
	expectError(t, rec, 500, CodeInternal)

	rec = httptest.NewRecorder()
	SetOpID(rec, "op_7k2m9q4r8t1c")
	if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatal("op id header not set")
	}
}

func TestOpIDHeaderOnAnswer(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s.Handler(), req{method: "POST", target: "/api/v1/fail", header: map[string]string{"Content-Type": "application/json"}, body: `{}`})
	expectError(t, rec, 409, CodeBusy)
	if rec.Header().Get("X-LocalAI-Op-Id") != "op_7k2m9q4r8t1c" {
		t.Fatal("X-LocalAI-Op-Id missing")
	}
}

func TestHTMLHeaders(t *testing.T) {
	h := HTMLHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "SAMEORIGIN",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if strings.Contains(HTMLContentSecurityPolicy, "http") || strings.Contains(HTMLContentSecurityPolicy, "*") {
		t.Fatal("the CSP must not name any remote origin")
	}
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(s.Handler(), req{target: "/api/v1/health"})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if want := `{"ok":true,"version":"1.2.3-test","mode":"online","starting":true}` + "\n"; rec.Body.String() != want {
		t.Fatalf("body %q, want %q", rec.Body.String(), want)
	}
	s.SetStarting(false)
	if want := `{"ok":true,"version":"1.2.3-test","mode":"online","starting":false}` + "\n"; do(s.Handler(), req{target: "/api/v1/health"}).Body.String() != want {
		t.Fatal("starting flag not reflected")
	}

	t.Run("anonymous even with the default DenyAll", func(t *testing.T) {
		s2, _ := newTestServer(t, func(o *Options) { o.Auth = nil })
		if rec := do(s2.Handler(), req{target: "/api/v1/health"}); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("answers on the socket without a Host", func(t *testing.T) {
		rec := do(s.Handler(), req{target: "/api/v1/health", host: "whatever", socket: &PeerCred{UID: 1}})
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("the mode follows the source and defaults to offline", func(t *testing.T) {
		mode := "survival"
		s2, _ := newTestServer(t, func(o *Options) { o.Mode = func() string { return mode } })
		if !strings.Contains(do(s2.Handler(), req{target: "/api/v1/health"}).Body.String(), `"mode":"survival"`) {
			t.Fatal("mode not reported")
		}
		s3, _ := newTestServer(t, func(o *Options) { o.Mode = nil })
		if !strings.Contains(do(s3.Handler(), req{target: "/api/v1/health"}).Body.String(), `"mode":"offline"`) {
			t.Fatal("default mode")
		}
	})
	t.Run("POST is refused with Allow", func(t *testing.T) {
		rec := do(s.Handler(), req{method: "POST", target: "/api/v1/health", header: jsonHeaders(nil)})
		expectError(t, rec, 405, CodeInvalidRequest)
	})
}

func TestPortFollowsConfig(t *testing.T) {
	s, _ := newTestServer(t, func(o *Options) { o.Config = fixedConfig{port: 7171} })
	h := s.Handler()
	if rec := do(h, req{target: "/api/v1/open", host: "127.0.0.1:7171"}); rec.Code != 200 {
		t.Fatalf("configured port refused: %d", rec.Code)
	}
	expectError(t, do(h, req{target: "/api/v1/open", host: "127.0.0.1:7070"}), 403, CodeBadHost)
	if rec := do(h, req{target: "/api/v1/open", host: "localhost:7171", header: map[string]string{"Origin": "http://localhost:7171"}}); rec.Code != 200 {
		t.Fatalf("configured origin refused: %d", rec.Code)
	}
}

func TestRegistry(t *testing.T) {
	h := http.NotFoundHandler()
	good := Route{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h}
	tests := []struct {
		name  string
		route Route
		ok    bool
	}{
		{"valid", good, true},
		{"absolute outside the API", Route{Method: "GET", Pattern: "/portal", Auth: []Level{AuthNone}, Handler: h, Absolute: true}, true},
		{"absolute inside the API", Route{Method: "GET", Pattern: "/api/v1/x", Auth: []Level{AuthNone}, Handler: h, Absolute: true}, false},
		{"bad method", Route{Method: "TRACE", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h}, false},
		{"lower-case method", Route{Method: "get", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h}, false},
		{"pattern without slash", Route{Method: "GET", Pattern: "a", Auth: []Level{AuthOp}, Handler: h}, false},
		{"no auth", Route{Method: "GET", Pattern: "/a", Handler: h}, false},
		{"unknown auth", Route{Method: "GET", Pattern: "/a", Auth: []Level{"root"}, Handler: h}, false},
		{"no handler", Route{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}}, false},
		{"negative timeout", Route{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h, Timeout: -1}, false},
		{"negative limit", Route{Method: "POST", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h, MaxBody: -1}, false},
		{"the built-in health route", Route{Method: "GET", Pattern: "/health", Auth: []Level{AuthNone}, Handler: h}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := NewRegistry().Add(tc.route)
			if (err == nil) != tc.ok {
				t.Fatalf("Add error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	t.Run("a duplicate is refused and nothing is registered", func(t *testing.T) {
		r := NewRegistry()
		if err := r.Add(good); err != nil {
			t.Fatal(err)
		}
		if err := r.Add(Route{Method: "POST", Pattern: "/b", Auth: []Level{AuthOp}, Handler: h}, good); err == nil {
			t.Fatal("duplicate accepted")
		}
		if n := len(r.Routes()); n != 1 {
			t.Fatalf("%d routes, want 1 (all or none)", n)
		}
		if err := r.Add(Route{Method: "POST", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h}); err != nil {
			t.Fatalf("same path, other method must be allowed: %v", err)
		}
	})
	t.Run("a duplicate inside one call is refused", func(t *testing.T) {
		if err := NewRegistry().Add(good, good); err == nil {
			t.Fatal("duplicate accepted")
		}
	})
	t.Run("Routes is sorted and a copy", func(t *testing.T) {
		r := NewRegistry()
		_ = r.Add(Route{Method: "POST", Pattern: "/b", Auth: []Level{AuthOp}, Handler: h}, Route{Method: "PUT", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h}, Route{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}, Handler: h})
		got := r.Routes()
		var keys []string
		for _, rt := range got {
			keys = append(keys, rt.key())
		}
		if strings.Join(keys, ",") != "GET /api/v1/a,PUT /api/v1/a,POST /api/v1/b" {
			t.Fatalf("order: %v", keys)
		}
		got[0].Method = "DELETE"
		if r.Routes()[0].Method != "GET" {
			t.Fatal("Routes returned the registry's own slice")
		}
	})
	t.Run("the registry copies the auth levels", func(t *testing.T) {
		levels := []Level{AuthOp}
		r := NewRegistry()
		_ = r.Add(Route{Method: "GET", Pattern: "/a", Auth: levels, Handler: h})
		levels[0] = AuthNone
		if r.Routes()[0].Auth[0] != AuthOp {
			t.Fatal("registry shares the caller's slice")
		}
	})
	t.Run("api.Routes lists health and the default registry's routes", func(t *testing.T) {
		found := false
		for _, rt := range Routes() {
			if rt.Method == "GET" && rt.Path() == "/api/v1/health" {
				found = true
				if len(rt.Auth) != 1 || rt.Auth[0] != AuthNone || rt.Timeout.Seconds() != 2 {
					t.Fatalf("health route metadata: %+v", rt)
				}
			}
		}
		if !found {
			t.Fatal("GET /health missing from Routes()")
		}
	})
	t.Run("Register panics on a bad route", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		Register(Route{Method: "NOPE"})
	})
	t.Run("Server.Routes includes the built-in route", func(t *testing.T) {
		s, _ := newTestServer(t)
		n := 0
		for _, rt := range s.Routes() {
			if rt.Path() == "/api/v1/health" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("health listed %d times", n)
		}
	})
	t.Run("two overlapping patterns are an error from New, not a panic", func(t *testing.T) {
		reg := NewRegistry()
		_ = reg.Add(
			Route{Method: "GET", Pattern: "/x/{a}/b", Auth: []Level{AuthOp}, Handler: h},
			Route{Method: "GET", Pattern: "/x/b/{c}", Auth: []Level{AuthOp}, Handler: h},
		)
		if _, err := New(Options{Registry: reg}); err == nil {
			t.Fatal("conflicting patterns accepted")
		}
	})
}
