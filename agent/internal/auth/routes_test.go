package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/auth/fake"
)

var origin = map[string]string{"Origin": testOrigin}

// mintCode asks for a login code the way aictl does: over the socket as an operator.
func (e *env) mintCode() (code, expires string) {
	e.t.Helper()
	rec := e.do(asOperator(req{method: "POST", path: "/ui-code"}))
	if rec.Code != http.StatusOK {
		e.t.Fatalf("POST /ui-code = %d %s", rec.Code, rec.Body.String())
	}
	var out struct{ Code, ExpiresAt string }
	var raw map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		e.t.Fatal(err)
	}
	out.Code, out.ExpiresAt = raw["code"], raw["expires_at"]
	return out.Code, out.ExpiresAt
}

func (e *env) redeem(code string) *httptest.ResponseRecorder {
	return e.do(req{method: "POST", path: "/session/code", body: `{"code": "` + code + `"}`})
}

func TestRoutesAreRegisteredWithTheSpecAuthLevels(t *testing.T) {
	want := map[string][]api.Level{
		"POST /api/v1/session":      {api.AuthOp},
		"POST /api/v1/session/code": {api.AuthNone},
		"DELETE /api/v1/session":    {api.AuthOp},
		"POST /api/v1/ui-code":      {api.AuthAdminStar},
		"POST /api/v1/pin/verify":   {api.AuthOp},
	}
	reg := api.NewRegistry()
	if err := Register(reg, New(Config{Groups: fake.NewGroups().Add(OperatorsGroup, 990)})); err != nil {
		t.Fatal(err)
	}
	got := reg.Routes()
	if len(got) != len(want) {
		t.Fatalf("%d routes registered, want %d", len(got), len(want))
	}
	for _, rt := range got {
		levels, ok := want[rt.Method+" "+rt.Path()]
		if !ok || len(rt.Auth) != 1 || rt.Auth[0] != levels[0] {
			t.Errorf("%s %s: auth %v, want %v", rt.Method, rt.Path(), rt.Auth, levels)
		}
		if rt.Timeout != 5*time.Second || !rt.Mutating() {
			t.Errorf("%s %s: timeout %v", rt.Method, rt.Path(), rt.Timeout)
		}
	}
	if err := Register(reg, New(Config{Groups: fake.NewGroups().Add(OperatorsGroup, 990)})); err == nil {
		t.Error("registering twice must fail")
	}
}

// ---- POST /session, DELETE /session ----

func TestCreateSessionFromTheBearerToken(t *testing.T) {
	e := newEnv(t)
	rec := e.do(req{method: "POST", path: "/session", header: bearer(e.token)})
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	raw := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(raw, "localai_session=") || !strings.HasSuffix(raw, "; Path=/; HttpOnly; SameSite=Strict; Max-Age=604800") {
		t.Fatalf("Set-Cookie %q", strings.Replace(raw, sessionCookie(t, rec).Value, "<value>", 1))
	}
	c := sessionCookie(t, rec)
	if len(c.Value) != 43 || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 604800 || c.Path != "/" {
		t.Errorf("cookie %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a response that sets a credential must be no-store")
	}
	wantPrincipal(t, e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: c.Value}}), "operator:session")

	evs := e.audit.all()
	if len(evs) != 1 || evs[0].Action != "session.create" || evs[0].Result != audit.ResultOK {
		t.Fatalf("audit %+v", evs)
	}
	if a := evs[0].Actor; a.Kind != audit.KindOperator || a.Via != "bearer" || a.Fingerprint != TokenFingerprint(e.token) {
		t.Errorf("actor %+v", a)
	}
	if evs[0].ReqID == "" {
		t.Error("the audit event carries the request id")
	}
}

func TestCreateSessionRefusals(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	e.audit.events = nil
	tests := []struct {
		name   string
		q      req
		status int
		code   string
		reason string
	}{
		{"no credentials", req{method: "POST", path: "/session"}, 401, api.CodeUnauthenticated, ""},
		{"wrong token", req{method: "POST", path: "/session", header: bearer(fakeToken())}, 401, api.CodeUnauthenticated, ""},
		{"a session cookie does not mint another", req{method: "POST", path: "/session", cookies: map[string]string{SessionCookie: cookie}, header: origin}, 401, api.CodeUnauthenticated, "bearer_required"},
		{"a socket peer needs no cookie", asOperator(req{method: "POST", path: "/session"}), 401, api.CodeUnauthenticated, "bearer_required"},
		{"unknown field", req{method: "POST", path: "/session", header: bearer(e.token), body: `{"x": 1}`}, 400, api.CodeInvalidRequest, "unknown_field"},
		{"not json", req{method: "POST", path: "/session", header: bearer(e.token), body: `nope`}, 400, api.CodeInvalidRequest, "malformed_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := wantError(t, e.do(tt.q), tt.status, tt.code)
			if tt.reason != "" && env.Error.Details["reason"] != tt.reason {
				t.Errorf("details %v, want reason %s", env.Error.Details, tt.reason)
			}
		})
	}
	if len(e.audit.all()) != 0 {
		t.Errorf("a refused request creates no session and no audit record: %+v", e.audit.all())
	}
	t.Run("an empty body is fine", func(t *testing.T) {
		q := req{method: "POST", path: "/session", header: bearer(e.token)}
		r := q.build()
		r.Body = http.NoBody
		r.ContentLength = 0
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestDeleteSession(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	jar := map[string]string{SessionCookie: cookie}

	t.Run("needs the session cookie, not the bearer", func(t *testing.T) {
		env := wantError(t, e.do(req{method: "DELETE", path: "/session", header: bearer(e.token)}), 401, api.CodeUnauthenticated)
		if env.Error.Details["reason"] != "session_cookie_required" {
			t.Errorf("details %v", env.Error.Details)
		}
	})
	t.Run("a cookie request needs an Origin (CSRF)", func(t *testing.T) {
		wantError(t, e.do(req{method: "DELETE", path: "/session", cookies: jar}), 403, api.CodeBadOrigin)
		wantPrincipal(t, e.do(req{path: "/whoami", cookies: jar}), "operator:session") // still alive
	})
	t.Run("ends the session and clears the cookie", func(t *testing.T) {
		rec := e.do(req{method: "DELETE", path: "/session", cookies: jar, header: origin})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
		c := sessionCookie(t, rec)
		if c.Value != "" || c.MaxAge != -1 || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
			t.Errorf("cookie %q / %+v must be cleared with Max-Age=0", rec.Header().Get("Set-Cookie"), c)
		}
		wantError(t, e.do(req{path: "/whoami", cookies: jar}), 401, api.CodeUnauthenticated)
		// Deleting again: the cookie is dead, so this is an ordinary 401.
		wantError(t, e.do(req{method: "DELETE", path: "/session", cookies: jar, header: origin}), 401, api.CodeUnauthenticated)
	})
}

// ---- POST /ui-code and POST /session/code ----

func TestMintUICode(t *testing.T) {
	e := newEnv(t)
	rec := e.do(asOperator(req{method: "POST", path: "/ui-code"}))
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !isDigits(out["code"], 6, 6) {
		t.Fatalf("body %v", out)
	}
	if want := t0.Add(5 * time.Minute).Format(time.RFC3339); out["expires_at"] != want || !strings.HasSuffix(out["expires_at"], "Z") {
		t.Errorf("expires_at %q, want %q", out["expires_at"], want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a minted code must be no-store")
	}
	evs := e.audit.all()
	if len(evs) != 1 || evs[0].Action != "ui_code.mint" || evs[0].Actor.Kind != audit.KindOperator || evs[0].Actor.Via != "socket" || *evs[0].Actor.UID != operatorA {
		t.Fatalf("audit %+v", evs)
	}
	mustNotContain(t, "audit", dump(evs), out["code"])
	mustNotContain(t, "log", e.logs.String(), out["code"])
}

func TestMintUICodeAuthLevels(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	t.Run("root over the socket", func(t *testing.T) {
		if rec := e.do(req{method: "POST", path: "/ui-code", peer: peer(0)}); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("an operator over the socket (admin*)", func(t *testing.T) {
		if rec := e.do(asOperator(req{method: "POST", path: "/ui-code"})); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("a bearer operator needs the PIN", func(t *testing.T) {
		env := wantError(t, e.do(req{method: "POST", path: "/ui-code", header: bearer(e.token)}), 403, api.CodeAdminRequired)
		if env.Error.Details["pin_required"] != true {
			t.Errorf("details %v", env.Error.Details)
		}
	})
	t.Run("a bearer operator with a PIN grant", func(t *testing.T) {
		grant := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))
		h := bearer(e.token)
		h[GrantHeader] = grant
		rec := e.do(req{method: "POST", path: "/ui-code", header: h})
		if rec.Code != 200 {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		last := e.audit.all()[len(e.audit.all())-1]
		if last.Action != "ui_code.mint" || last.Actor.Kind != audit.KindAdmin || last.Actor.Via != "pin" {
			t.Errorf("audit actor %+v", last.Actor)
		}
	})
	t.Run("anonymous", func(t *testing.T) {
		wantError(t, e.do(req{method: "POST", path: "/ui-code"}), 401, api.CodeUnauthenticated)
	})
	t.Run("a stranger on the socket", func(t *testing.T) {
		wantError(t, e.do(req{method: "POST", path: "/ui-code", peer: peer(stranger)}), 403, api.CodePeerNotOperator)
	})
	t.Run("a body is refused", func(t *testing.T) {
		wantError(t, e.do(asOperator(req{method: "POST", path: "/ui-code", body: `{"x":1}`})), 400, api.CodeInvalidRequest)
	})
}

func TestLoginWithACode(t *testing.T) {
	e := newEnv(t)
	code, _ := e.mintCode()
	rec := e.redeem(code)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookie(t, rec)
	if len(c.Value) != 43 || !c.HttpOnly || c.MaxAge != 604800 {
		t.Errorf("cookie %+v", c)
	}
	wantPrincipal(t, e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: c.Value}}), "operator:session")

	t.Run("single use", func(t *testing.T) {
		wantError(t, e.redeem(code), 401, api.CodeBadLoginCode)
	})
	t.Run("audit", func(t *testing.T) {
		got := e.audit.actions()
		if len(got) != 2 || got[0] != "ui_code.mint" || got[1] != "session.create" {
			t.Fatalf("actions %v", got)
		}
		ev := e.audit.all()[1]
		if ev.Actor.Kind != audit.KindAnonymous || ev.Actor.Via != "ui_code" || ev.Target["method"] != "ui_code" {
			t.Errorf("event %+v", ev)
		}
		mustNotContain(t, "audit", dump(e.audit.all()), code, c.Value)
		mustNotContain(t, "log", e.logs.String(), code, c.Value)
	})
}

func TestUICodeExpires(t *testing.T) {
	for _, tt := range []struct {
		after time.Duration
		ok    bool
	}{{4*time.Minute + 59*time.Second, true}, {5 * time.Minute, false}} {
		e := newEnv(t)
		code, _ := e.mintCode()
		e.clock.Advance(tt.after)
		rec := e.redeem(code)
		if tt.ok && rec.Code != 204 {
			t.Errorf("after %v: status %d", tt.after, rec.Code)
		}
		if !tt.ok {
			wantError(t, rec, 401, api.CodeBadLoginCode)
		}
	}
}

func TestFourthCodeInvalidatesTheOldestOverHTTP(t *testing.T) {
	e := newEnv(t)
	var codes []string
	for i := 0; i < 4; i++ {
		c, _ := e.mintCode()
		codes = append(codes, c)
	}
	wantError(t, e.redeem(codes[0]), 401, api.CodeBadLoginCode)
	for _, c := range codes[1:] {
		if rec := e.redeem(c); rec.Code != 204 {
			t.Errorf("a live code failed: %d", rec.Code)
		}
	}
}

func TestFiveWrongCodesLockLoginForEveryone(t *testing.T) {
	e := newEnv(t)
	code, _ := e.mintCode()
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		wantError(t, e.redeem(wrong), 401, api.CodeBadLoginCode)
		e.clock.Advance(10 * time.Second)
	}
	// The window is full: a 6th wrong code, and the right one, are both 429.
	for _, c := range []string{wrong, code} {
		rec := e.redeem(c)
		env := wantError(t, rec, 429, api.CodeRateLimited)
		secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
		if err != nil || secs != 250 { // 5 min after the first failure, 50 s have passed
			t.Errorf("Retry-After %q, want 250", rec.Header().Get("Retry-After"))
		}
		if env.Error.Details["retry_after_s"] != float64(250) || env.Error.Details["locked"] != false {
			t.Errorf("details %v", env.Error.Details)
		}
	}
	// A bearer-authenticated login is not behind the limiter.
	if rec := e.do(req{method: "POST", path: "/session", header: bearer(e.token)}); rec.Code != 204 {
		t.Errorf("POST /session while codes are locked = %d", rec.Code)
	}
	e.clock.Advance(250 * time.Second) // the first failure is 5 minutes old; the code (also 5 minutes old) has expired
	fresh, _ := e.mintCode()
	if rec := e.redeem(fresh); rec.Code != 204 {
		t.Fatalf("a fresh code works once the window frees: %d", rec.Code)
	}
}

func TestMalformedCodesAreNotGuessesAndAreNotCounted(t *testing.T) {
	e := newEnv(t)
	code, _ := e.mintCode()
	for i := 0; i < 20; i++ {
		for _, bad := range []string{"", "1234", "12345a", "1234567", " 12345"} {
			wantError(t, e.redeem(bad), 400, api.CodeInvalidRequest)
		}
	}
	wantError(t, e.do(req{method: "POST", path: "/session/code", body: `{}`}), 400, api.CodeInvalidRequest)
	wantError(t, e.do(req{method: "POST", path: "/session/code", body: `{"code": 482913}`}), 400, api.CodeInvalidRequest)
	wantError(t, e.do(req{method: "POST", path: "/session/code", body: `{"code": "482913", "x": 1}`}), 400, api.CodeInvalidRequest)
	if rec := e.redeem(code); rec.Code != 204 {
		t.Fatalf("a malformed flood must not lock the login: %d", rec.Code)
	}
}

func TestCodeLoginNeedsNoCredentialsAndIgnoresAStaleOne(t *testing.T) {
	e := newEnv(t)
	code, _ := e.mintCode()
	rec := e.do(req{method: "POST", path: "/session/code", body: `{"code": "` + code + `"}`,
		header: bearer(fakeToken()), cookies: map[string]string{SessionCookie: randomToken(32)}})
	if rec.Code != 204 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// ---- POST /pin/verify ----

func TestVerifyPINSuccess(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	rec := e.verifyPIN(req{header: bearer(e.token)}, "4821")
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !strings.HasPrefix(out["grant"], "pg_") || len(out["grant"]) != 46 {
		t.Fatalf("body %v", out)
	}
	if want := t0.Add(5 * time.Minute).Format(time.RFC3339); out["expires_at"] != want {
		t.Errorf("expires_at %q, want %q", out["expires_at"], want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a grant must be no-store")
	}
	evs := e.audit.all()
	if len(evs) != 1 || evs[0].Action != "pin.verify.ok" || evs[0].Result != audit.ResultOK || evs[0].Actor.Via != "bearer" {
		t.Fatalf("audit %+v", evs)
	}
	mustNotContain(t, "audit", dump(evs), "4821", out["grant"])
	mustNotLeak(t, "log", e.logs.String(), "4821", out["grant"])
}

func TestVerifyPINFailureModes(t *testing.T) {
	e := newEnv(t)
	t.Run("no PIN set", func(t *testing.T) {
		env := wantError(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"), 409, api.CodeNoPIN)
		if !strings.Contains(env.Error.Fix, "aictl pin set") {
			t.Errorf("fix %q", env.Error.Fix)
		}
	})
	e.setPIN("4821")
	t.Run("wrong PIN counts down", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			env := wantError(t, e.verifyPIN(req{header: bearer(e.token)}, "0000"), 401, api.CodeWrongPIN)
			if env.Error.Details["remaining_attempts"] != float64(4-i) {
				t.Fatalf("attempt %d: details %v", i+1, env.Error.Details)
			}
		}
		evs := e.audit.all()
		if len(evs) != 5 {
			t.Fatalf("%d audit events, want 5", len(evs))
		}
		for i, ev := range evs {
			if ev.Action != "pin.verify.fail" || ev.Result != audit.ResultFail || ev.Code != api.CodeWrongPIN || ev.Target["remaining_attempts"] != strconv.Itoa(4-i) {
				t.Errorf("event %d: %+v", i, ev)
			}
		}
	})
	t.Run("429 with Retry-After, even for the right PIN", func(t *testing.T) {
		before := len(e.audit.all())
		rec := e.verifyPIN(req{header: bearer(e.token)}, "4821")
		wantError(t, rec, 429, api.CodeRateLimited)
		if got := rec.Header().Get("Retry-After"); got != "300" {
			t.Errorf("Retry-After %q, want 300", got)
		}
		if len(e.audit.all()) != before {
			t.Error("refusals while the window is full are not audited one by one")
		}
		e.clock.Advance(5 * time.Minute)
		if rec := e.verifyPIN(req{header: bearer(e.token)}, "4821"); rec.Code != 200 {
			t.Errorf("after the window: %d", rec.Code)
		}
	})
}

func TestVerifyPINBodyValidation(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	h := bearer(e.token)
	for name, body := range map[string]string{
		"no pin":      `{}`,
		"null":        `{"pin": null}`,
		"a number":    `{"pin": 4821}`,
		"unknown key": `{"pin": "4821", "x": 1}`,
		"not json":    `4821`,
		"empty":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			q := req{method: "POST", path: "/pin/verify", header: h, body: body}
			r := q.build()
			if body == "" {
				r.Body, r.ContentLength = http.NoBody, 0
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, r)
			wantError(t, rec, 400, api.CodeInvalidRequest)
		})
	}
	t.Run("a malformed PIN string is a wrong PIN, not a 400", func(t *testing.T) {
		for _, pin := range []string{"", "abc", "12", "123456789", "4821 "} {
			wantError(t, e.verifyPIN(req{header: h}, pin), 401, api.CodeWrongPIN)
			e.clock.Advance(6 * time.Minute)
		}
	})
	t.Run("bad body errors are not guesses", func(t *testing.T) {
		if rec := e.verifyPIN(req{header: h}, "4821"); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
}

func TestVerifyPINRequiresAnOperator(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	wantError(t, e.verifyPIN(req{}, "4821"), 401, api.CodeUnauthenticated)
	wantError(t, e.verifyPIN(req{header: bearer(fakeToken())}, "4821"), 401, api.CodeUnauthenticated)
	wantError(t, e.verifyPIN(req{peer: peer(stranger)}, "4821"), 403, api.CodePeerNotOperator)
	e.portal.Add("pc", "ps")
	wantError(t, e.verifyPIN(req{cookies: map[string]string{PortalCookie: "pc"}, header: origin}, "4821"), 401, api.CodePortalSessionEnded)
	if e.svc.PIN().limiter.consecutive != 0 {
		t.Error("an unauthenticated caller must not even reach the PIN limiter")
	}
	for _, rec := range []*httptest.ResponseRecorder{
		e.verifyPIN(asOperator(req{}), "4821"),
		e.verifyPIN(req{peer: peer(0)}, "4821"),
		e.verifyPIN(req{cookies: map[string]string{SessionCookie: e.login()}, header: origin}, "4821"),
	} {
		if rec.Code != 200 {
			t.Errorf("an operator, root or session caller must get a grant, got %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestPINLockoutOverHTTP(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	var last *httptest.ResponseRecorder
	for round := 0; round < 4; round++ {
		for i := 0; i < 5; i++ {
			last = e.verifyPIN(req{header: bearer(e.token)}, "0000")
		}
		e.clock.Advance(5*time.Minute + time.Second)
	}
	env := wantError(t, last, 429, api.CodeRateLimited)
	if last.Header().Get("Retry-After") != "3600" || env.Error.Details["locked"] != true {
		t.Fatalf("20th failure: Retry-After %q details %v", last.Header().Get("Retry-After"), env.Error.Details)
	}
	if got := e.alertList(); len(got) != 1 || got[0] != "LAI-SEC-004/critical" {
		t.Fatalf("alerts %v", got)
	}
	// The right PIN is refused for an hour; only the request that began the lockout is audited as denied.
	rec := e.verifyPIN(req{header: bearer(e.token)}, "4821")
	wantError(t, rec, 429, api.CodeRateLimited)
	if secs, _ := strconv.Atoi(rec.Header().Get("Retry-After")); secs != 3600-301 {
		t.Errorf("Retry-After %d", secs)
	}
	var denied int
	for _, ev := range e.audit.all() {
		if ev.Result == audit.ResultDenied {
			denied++
			if ev.Action != "pin.verify.fail" || ev.Code != api.CodeRateLimited {
				t.Errorf("denied event %+v", ev)
			}
		}
	}
	if denied != 1 {
		t.Errorf("%d denied events, want 1", denied)
	}
	e.clock.Advance(time.Hour)
	if rec := e.verifyPIN(req{header: bearer(e.token)}, "4821"); rec.Code != 200 {
		t.Fatalf("after the lockout: %d", rec.Code)
	}
}

func TestNewPINFileAppliesAtOnce(t *testing.T) {
	e := newEnv(t)
	e.setPIN("1111")
	if rec := e.verifyPIN(req{header: bearer(e.token)}, "1111"); rec.Code != 200 {
		t.Fatal("setup")
	}
	e.setPIN("2222") // `sudo aictl pin set`
	wantError(t, e.verifyPIN(req{header: bearer(e.token)}, "1111"), 401, api.CodeWrongPIN)
	e.clock.Advance(6 * time.Minute)
	if rec := e.verifyPIN(req{header: bearer(e.token)}, "2222"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

// ---- secrets never leak ----

func TestNoSecretReachesTheLogOrTheAuditRecords(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	pinHash, _ := os.ReadFile(e.pinPath)

	cookie := e.login()
	code, _ := e.mintCode()
	codeCookie := sessionCookie(t, e.redeem(code)).Value
	grant := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))
	e.verifyPIN(req{header: bearer(e.token)}, "7777")
	h := bearer(e.token)
	h[GrantHeader] = grant
	e.do(req{path: "/admin-only", header: h})
	e.do(req{path: "/whoami", header: bearer(fakeToken())})
	e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: cookie}})
	e.redeem("000000")

	secrets := []string{e.token, e.token[4:], cookie, codeCookie, code, grant, "4821", "7777", strings.TrimSpace(string(pinHash)), fakeToken()}
	mustNotContain(t, "log", e.logs.String(), secrets...)
	mustNotContain(t, "audit", dump(e.audit.all()), secrets...)
	if !strings.Contains(e.logs.String(), `"component":"auth"`) {
		t.Error("auth log lines carry component=auth")
	}
}

func TestRealAuditLogGetsCleanLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	alog, err := audit.Open(path, audit.WithoutRotation(), audit.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer alog.Close()
	e := newEnv(t)
	e.svc.audit = alog
	e.setPIN("4821")

	cookie := e.login()
	code, _ := e.mintCode()
	grant := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))
	e.verifyPIN(req{header: bearer(e.token)}, "9999")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, "audit file", string(data), e.token, e.token[4:], cookie, code, grant, "4821", "9999")
	var actions []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec struct {
			Action string `json:"action"`
			Result string `json:"result"`
			Code   *string
			Actor  struct {
				Kind        string  `json:"kind"`
				Via         *string `json:"via"`
				Fingerprint *string `json:"fingerprint"`
			} `json:"actor"`
			ReqID *string `json:"req_id"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad line %q", line)
		}
		actions = append(actions, rec.Action+":"+rec.Result)
		if rec.ReqID == nil || !strings.HasPrefix(*rec.ReqID, "req_") {
			t.Errorf("%s: req_id %v", rec.Action, rec.ReqID)
		}
		if rec.Action == "pin.verify.fail" && (rec.Code == nil || *rec.Code != "LAI-SEC-003") {
			t.Errorf("fail event code %v", rec.Code)
		}
		if rec.Actor.Via != nil && *rec.Actor.Via == "bearer" && (rec.Actor.Fingerprint == nil || !strings.HasPrefix(*rec.Actor.Fingerprint, "lat_…")) {
			t.Errorf("bearer actor without a fingerprint: %+v", rec.Actor)
		}
	}
	want := "session.create:ok ui_code.mint:ok pin.verify.ok:ok pin.verify.fail:fail"
	if got := strings.Join(actions, " "); got != want {
		t.Errorf("actions %q, want %q", got, want)
	}
}

func TestAuditFailureDoesNotFailARequest(t *testing.T) {
	e := newEnv(t)
	e.audit.err = errTest
	if rec := e.do(req{method: "POST", path: "/session", header: bearer(e.token)}); rec.Code != 204 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestActorOf(t *testing.T) {
	uid := func(n int) *int { return &n }
	tests := []struct {
		name string
		p    api.Principal
		want audit.Actor
	}{
		{"bearer", api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer", TokenFingerprint: "lat_…abcd"},
			audit.Actor{Kind: audit.KindOperator, Via: "bearer", Fingerprint: "lat_…abcd"}},
		{"session", api.Principal{Kind: api.KindOperator, Credential: api.CredSession, Via: "session", SessionID: "s"},
			audit.Actor{Kind: audit.KindOperator, Via: "session"}},
		{"socket operator", api.Principal{Kind: api.KindOperator, Credential: api.CredSocket, Via: "uid=1000", UID: 1000},
			audit.Actor{Kind: audit.KindOperator, Via: "socket", UID: uid(1000)}},
		{"socket root", api.Principal{Kind: api.KindAdmin, Credential: api.CredSocket, Via: "uid=0"},
			audit.Actor{Kind: audit.KindAdmin, Via: "socket", UID: uid(0)}},
		{"lifted by a grant", api.Principal{Kind: api.KindAdmin, Credential: api.CredBearer, Via: "pin", TokenFingerprint: "lat_…abcd"},
			audit.Actor{Kind: audit.KindAdmin, Via: "pin", Fingerprint: "lat_…abcd"}},
		{"portal", api.Principal{Kind: api.KindPortal, Credential: api.CredPortal, Via: "portal"}, audit.Portal()},
		{"anonymous", api.Anonymous(), audit.Anonymous()},
	}
	for _, tt := range tests {
		got := actorOf(tt.p)
		if got.Kind != tt.want.Kind || got.Via != tt.want.Via || got.Fingerprint != tt.want.Fingerprint || (got.UID == nil) != (tt.want.UID == nil) || (got.UID != nil && *got.UID != *tt.want.UID) {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// dump renders audit events for a secret scan.
func dump(evs []audit.Event) string {
	b, _ := json.Marshal(evs)
	return string(b)
}
