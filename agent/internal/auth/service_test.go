package auth

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/auth/fake"
)

var (
	_ PortalSessions = (*fake.Portal)(nil)
	_ GroupLookup    = (*fake.Groups)(nil)
	_ GroupLookup    = OSGroups{}
	_ Auditor        = (*recAudit)(nil)
)

func TestBearerToken(t *testing.T) {
	e := newEnv(t)
	otherTok := fakeToken()
	tests := []struct {
		name   string
		header string
		status int
		want   string // principal, when 200
	}{
		{"right", "Bearer " + e.token, 200, "operator:bearer"},
		{"scheme is case-insensitive", "bearer " + e.token, 200, "operator:bearer"},
		{"extra spaces around the token", "Bearer   " + e.token + "  ", 200, "operator:bearer"},
		{"wrong", "Bearer " + otherTok, 401, ""},
		{"empty", "Bearer ", 401, ""},
		{"no value", "Bearer", 401, ""},
		{"no prefix", "Bearer " + e.token[4:], 401, ""},
		{"another scheme", "Basic " + e.token, 401, ""},
		{"token without a scheme", e.token, 401, ""},
		{"truncated", "Bearer " + e.token[:len(e.token)-1], 401, ""},
		{"longer", "Bearer " + e.token + "A", 401, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(req{path: "/whoami", header: map[string]string{"Authorization": tt.header}})
			if tt.status == 200 {
				wantPrincipal(t, rec, tt.want)
				return
			}
			wantError(t, rec, 401, api.CodeUnauthenticated)
		})
	}
	t.Run("no header", func(t *testing.T) { wantError(t, e.do(req{path: "/whoami"}), 401, api.CodeUnauthenticated) })
}

func TestAStaleTokenDoesNotBreakAnOpenRoute(t *testing.T) {
	e := newEnv(t)
	wantPrincipal(t, e.do(req{path: "/open", header: bearer(fakeToken())}), "anonymous")
	wantPrincipal(t, e.do(req{path: "/open"}), "anonymous")
}

func TestRotatedTokenWorksAtOnceAndTheOldOneStops(t *testing.T) {
	e := newEnv(t)
	oldTok, newTok := e.token, GenerateToken()
	writeFile(t, e.tokPath, newTok+"\n", 0o640)
	bump(t, e.tokPath, time.Minute)
	wantPrincipal(t, e.do(req{path: "/whoami", header: bearer(newTok)}), "operator:bearer")
	wantError(t, e.do(req{path: "/whoami", header: bearer(oldTok)}), 401, api.CodeUnauthenticated)
}

func TestSessionCookieResolution(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	wantPrincipal(t, e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: cookie}}), "operator:session")

	t.Run("unknown cookie is anonymous", func(t *testing.T) {
		q := req{path: "/whoami", cookies: map[string]string{SessionCookie: randomToken(32)}}
		wantError(t, e.do(q), 401, api.CodeUnauthenticated)
		wantPrincipal(t, e.do(req{path: "/open", cookies: q.cookies}), "anonymous")
	})
	t.Run("idle expiry answers LAI-SEC-012", func(t *testing.T) {
		e.clock.Advance(12 * time.Hour)
		q := req{path: "/whoami", cookies: map[string]string{SessionCookie: cookie}}
		env := wantError(t, e.do(q), 401, api.CodeSessionExpired)
		if !strings.Contains(env.Error.Message, "expired") {
			t.Errorf("message %q", env.Error.Message)
		}
		// An open route still works for the holder of the dead cookie (so the login page can load).
		wantPrincipal(t, e.do(req{path: "/open", cookies: q.cookies}), "anonymous")
	})
	t.Run("absolute expiry answers LAI-SEC-012", func(t *testing.T) {
		c := e.login()
		for i := 0; i < 27; i++ {
			e.clock.Advance(6 * time.Hour)
			wantPrincipal(t, e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: c}}), "operator:session")
		}
		e.clock.Advance(6 * time.Hour)
		wantError(t, e.do(req{path: "/whoami", cookies: map[string]string{SessionCookie: c}}), 401, api.CodeSessionExpired)
	})
}

func TestCredentialPrecedence(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	e.portal.Add("portal-cookie", "ps_1")
	// bearer beats the session cookie
	wantPrincipal(t, e.do(req{path: "/whoami", header: bearer(e.token), cookies: map[string]string{SessionCookie: cookie}}), "operator:bearer")
	// a wrong bearer does not shadow a good cookie
	wantPrincipal(t, e.do(req{path: "/whoami", header: bearer(fakeToken()), cookies: map[string]string{SessionCookie: cookie}}), "operator:session")
	// the session cookie beats the Portal cookie
	wantPrincipal(t, e.do(req{path: "/either", cookies: map[string]string{SessionCookie: cookie, PortalCookie: "portal-cookie"}}), "operator:session")
	// an expired session does not shadow a good Portal cookie
	e.clock.Advance(13 * time.Hour)
	wantPrincipal(t, e.do(req{path: "/either", cookies: map[string]string{SessionCookie: cookie, PortalCookie: "portal-cookie"}}), "portal:portal")
}

func TestPortalHook(t *testing.T) {
	e := newEnv(t)
	e.portal.Add("known", "ps_1")
	portalCookie := map[string]string{PortalCookie: "known"}

	t.Run("a known cookie on a route the hook allows is the portal principal", func(t *testing.T) {
		wantPrincipal(t, e.do(req{path: "/either", cookies: portalCookie}), "portal:portal")
	})
	t.Run("the principal carries the Portal session id", func(t *testing.T) {
		r := req{path: "/either", cookies: portalCookie}.build()
		p, err := e.svc.Authenticate(r, &api.Route{Auth: []api.Level{api.AuthPortal}})
		if err != nil || p.Kind != api.KindPortal || p.Credential != api.CredPortal || p.PortalSessionID != "ps_1" {
			t.Fatalf("%+v, %v", p, err)
		}
	})
	t.Run("a route the hook disallows answers 401 LAI-SVC-110", func(t *testing.T) {
		e.portal.Allow = func(r *http.Request) bool { return r.URL.Path != "/api/v1/either" }
		defer func() { e.portal.Allow = nil }()
		wantError(t, e.do(req{path: "/either", cookies: portalCookie}), 401, api.CodePortalSessionEnded)
	})
	t.Run("a route that does not list portal answers 401 LAI-SVC-110", func(t *testing.T) {
		wantError(t, e.do(req{path: "/whoami", cookies: portalCookie}), 401, api.CodePortalSessionEnded)
		wantError(t, e.do(req{path: "/admin-only", cookies: portalCookie}), 401, api.CodePortalSessionEnded)
	})
	t.Run("an unknown cookie is anonymous", func(t *testing.T) {
		unknown := map[string]string{PortalCookie: "nope"}
		wantError(t, e.do(req{path: "/either", cookies: unknown}), 401, api.CodeUnauthenticated)
		wantPrincipal(t, e.do(req{path: "/open", cookies: unknown}), "anonymous")
	})
	t.Run("on an open route a Portal cookie is harmless", func(t *testing.T) {
		e.portal.Allow = func(*http.Request) bool { return false }
		defer func() { e.portal.Allow = nil }()
		wantPrincipal(t, e.do(req{path: "/open", cookies: portalCookie}), "anonymous")
	})
	t.Run("a Portal mutation needs a real Origin (the server's CSRF rule sees the credential)", func(t *testing.T) {
		wantError(t, e.do(req{method: "POST", path: "/mutate", cookies: portalCookie}), 403, api.CodeBadOrigin)
		wantError(t, e.do(req{method: "POST", path: "/mutate", cookies: portalCookie, header: map[string]string{"Sec-Fetch-Site": "same-origin"}}), 403, api.CodeBadOrigin)
		wantPrincipal(t, e.do(req{method: "POST", path: "/mutate", cookies: portalCookie, header: map[string]string{"Origin": testOrigin}}), "portal:portal")
	})
	t.Run("no hook configured: the cookie is ignored", func(t *testing.T) {
		svc := New(Config{TokenPath: e.tokPath, PINPath: e.pinPath, Clock: e.clock, Groups: e.groups})
		r := req{path: "/open", cookies: portalCookie}.build()
		p, err := svc.Authenticate(r, &api.Route{Auth: []api.Level{api.AuthPortal, api.AuthOp}})
		if p.Kind != api.KindAnonymous || err == nil {
			t.Fatalf("%+v, %v", p, err)
		}
	})
	t.Run("dropped sessions stop resolving", func(t *testing.T) {
		e.portal.DropAll()
		wantError(t, e.do(req{path: "/either", cookies: portalCookie}), 401, api.CodeUnauthenticated)
	})
}

func TestSocketPeer(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		peer   *api.PeerCred
		noPeer bool
		path   string
		status int
		want   string
		code   string
	}{
		{"root is admin", peer(0), false, "/whoami", 200, "admin:uid=0", ""},
		{"root passes an admin route with no PIN", peer(0), false, "/admin-only", 200, "admin:uid=0", ""},
		{"root passes admin*", peer(0), false, "/admin-star", 200, "admin:uid=0", ""},
		{"an operator is operator", peer(operatorA), false, "/whoami", 200, "operator:uid=1000", ""},
		{"an operator satisfies admin*", peer(operatorA), false, "/admin-star", 200, "operator:uid=1000", ""},
		{"an operator needs the PIN for admin", peer(operatorA), false, "/admin-only", 403, "", api.CodeAdminRequired},
		{"a stranger is refused", peer(stranger), false, "/whoami", 403, "", api.CodePeerNotOperator},
		{"a stranger is refused on an open route too", peer(stranger), false, "/open", 403, "", api.CodePeerNotOperator},
		{"an unreadable peer is refused", nil, true, "/open", 403, "", api.CodePeerNotOperator},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(req{path: tt.path, peer: tt.peer, noPeer: tt.noPeer})
			if tt.status == 200 {
				wantPrincipal(t, rec, tt.want)
				return
			}
			wantError(t, rec, tt.status, tt.code)
		})
	}
	t.Run("an operator on admin gets pin_required", func(t *testing.T) {
		env := wantError(t, e.do(req{path: "/admin-only", peer: peer(operatorA)}), 403, api.CodeAdminRequired)
		if env.Error.Details["pin_required"] != true {
			t.Errorf("details %v", env.Error.Details)
		}
	})
	t.Run("credentials sent over the socket do not rescue a stranger", func(t *testing.T) {
		wantError(t, e.do(req{path: "/whoami", peer: peer(stranger), header: bearer(e.token)}), 403, api.CodePeerNotOperator)
	})
	t.Run("the peer decides on the socket, not a bearer token", func(t *testing.T) {
		wantPrincipal(t, e.do(req{path: "/whoami", peer: peer(operatorA), header: bearer(fakeToken())}), "operator:uid=1000")
	})
	t.Run("the principal records the peer uid", func(t *testing.T) {
		p, err := e.svc.Authenticate(req{path: "/whoami", peer: peer(operatorA)}.build().WithContext(
			api.WithRequestInfo(api.WithTransport(t.Context(), api.TransportSocket, peer(operatorA)), &api.RequestInfo{Transport: api.TransportSocket, Peer: peer(operatorA)})),
			&api.Route{Auth: []api.Level{api.AuthOp}})
		if err != nil || p.UID != operatorA || p.Credential != api.CredSocket {
			t.Fatalf("%+v, %v", p, err)
		}
	})
}

func TestGroupLookupIsCachedAndFailsClosed(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		wantPrincipal(t, e.do(req{path: "/whoami", peer: peer(operatorA)}), "operator:uid=1000")
	}
	if e.groups.Calls != 1 {
		t.Fatalf("%d lookups for 5 requests, want 1 (cached)", e.groups.Calls)
	}
	e.groups.Remove(OperatorsGroup, operatorA)
	wantPrincipal(t, e.do(req{path: "/whoami", peer: peer(operatorA)}), "operator:uid=1000") // still cached
	e.clock.Advance(GroupCacheTTL)
	wantError(t, e.do(req{path: "/whoami", peer: peer(operatorA)}), 403, api.CodePeerNotOperator)

	e.groups.Err = errTest
	wantError(t, e.do(req{path: "/whoami", peer: peer(operatorB)}), 403, api.CodePeerNotOperator)
	if !strings.Contains(e.logs.String(), "cannot look up the groups") {
		t.Error("a lookup failure must be logged")
	}
}

var errTest = errors.New("lookup failed")

func TestTCPOperatorNeedsAPINForAdminRoutes(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/admin-only", "/admin-star"} {
		env := wantError(t, e.do(req{path: path, header: bearer(e.token)}), 403, api.CodeAdminRequired)
		if env.Error.Details["pin_required"] != true {
			t.Errorf("%s: details %v", path, env.Error.Details)
		}
		if !strings.Contains(env.Error.Fix, "PIN") {
			t.Errorf("%s: fix %q must say to enter the PIN", path, env.Error.Fix)
		}
	}
}

func TestPINGrantLiftsAnOperatorToAdmin(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	grant := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))

	h := bearer(e.token)
	h[GrantHeader] = grant
	wantPrincipal(t, e.do(req{path: "/admin-only", header: h}), "admin:pin")
	wantPrincipal(t, e.do(req{path: "/admin-star", header: h}), "admin:pin")
	wantPrincipal(t, e.do(req{path: "/admin-only", header: h}), "admin:pin") // reusable

	t.Run("it expires after five minutes", func(t *testing.T) {
		e.clock.Advance(5 * time.Minute)
		env := wantError(t, e.do(req{path: "/admin-only", header: h}), 403, api.CodeAdminRequired)
		if env.Error.Details["reason"] != "grant_expired" {
			t.Errorf("details %v", env.Error.Details)
		}
		// On an op route the dead grant is ignored.
		wantPrincipal(t, e.do(req{path: "/whoami", header: h}), "operator:bearer")
	})
}

func TestGrantIsBoundToThePrincipal(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	cookieA := e.login()

	grantA := grantFrom(t, e.verifyPIN(req{cookies: map[string]string{SessionCookie: cookieA}, header: map[string]string{"Origin": testOrigin}}, "4821"))

	t.Run("works for session A", func(t *testing.T) {
		wantPrincipal(t, e.do(req{path: "/admin-only", cookies: map[string]string{SessionCookie: cookieA}, header: map[string]string{GrantHeader: grantA}}), "admin:pin")
	})
	t.Run("a grant from session A is rejected for bearer B", func(t *testing.T) {
		env := wantError(t, e.do(req{path: "/admin-only", header: map[string]string{"Authorization": "Bearer " + e.token, GrantHeader: grantA}}), 403, api.CodeAdminRequired)
		if env.Error.Details["reason"] != "grant_expired" {
			t.Errorf("a foreign grant looks like an expired one: %v", env.Error.Details)
		}
	})
	t.Run("a grant from session A is rejected for session B", func(t *testing.T) {
		cookieB := e.login()
		wantError(t, e.do(req{path: "/admin-only", cookies: map[string]string{SessionCookie: cookieB}, header: map[string]string{GrantHeader: grantA}}), 403, api.CodeAdminRequired)
	})
	t.Run("a grant from a bearer is rejected for a session", func(t *testing.T) {
		grantBearer := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))
		wantError(t, e.do(req{path: "/admin-only", cookies: map[string]string{SessionCookie: cookieA}, header: map[string]string{GrantHeader: grantBearer}}), 403, api.CodeAdminRequired)
	})
	t.Run("a grant from socket uid A is rejected for socket uid B", func(t *testing.T) {
		grantUID := grantFrom(t, e.verifyPIN(req{peer: peer(operatorA)}, "4821"))
		wantPrincipal(t, e.do(req{path: "/admin-only", peer: peer(operatorA), header: map[string]string{GrantHeader: grantUID}}), "admin:pin")
		wantError(t, e.do(req{path: "/admin-only", peer: peer(operatorB), header: map[string]string{GrantHeader: grantUID}}), 403, api.CodeAdminRequired)
	})
	t.Run("a grant does not make an anonymous caller anyone", func(t *testing.T) {
		wantError(t, e.do(req{path: "/admin-only", header: map[string]string{GrantHeader: grantA}}), 401, api.CodeUnauthenticated)
	})
	t.Run("junk in the grant header is just an invalid grant", func(t *testing.T) {
		for _, junk := range []string{"x", "pg_", "pg_" + strings.Repeat("A", 43), strings.Repeat("é", 300)} {
			wantError(t, e.do(req{path: "/admin-only", header: map[string]string{"Authorization": "Bearer " + e.token, GrantHeader: junk}}), 403, api.CodeAdminRequired)
		}
	})
}

func TestEndingASessionRevokesItsGrants(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	cookie := e.login()
	origin := map[string]string{"Origin": testOrigin}
	grant := grantFrom(t, e.verifyPIN(req{cookies: map[string]string{SessionCookie: cookie}, header: origin}, "4821"))
	if e.svc.Grants().Len() != 1 {
		t.Fatal("expected one grant")
	}
	if rec := e.do(req{method: "DELETE", path: "/session", cookies: map[string]string{SessionCookie: cookie}, header: origin}); rec.Code != 204 {
		t.Fatalf("DELETE /session = %d", rec.Code)
	}
	if e.svc.Grants().Len() != 0 {
		t.Error("the session's grants must go with it")
	}
	_ = grant
}

func TestRotatingTheTokenRevokesBearerGrants(t *testing.T) {
	e := newEnv(t)
	e.setPIN("4821")
	grant := grantFrom(t, e.verifyPIN(req{header: bearer(e.token)}, "4821"))
	// A new token that ends in the same four characters, so the fingerprint (the binding) is equal.
	newTok := TokenPrefix + strings.Repeat("B", 39) + e.token[len(e.token)-4:]
	writeFile(t, e.tokPath, newTok+"\n", 0o640)
	bump(t, e.tokPath, time.Minute)
	if TokenFingerprint(newTok) != TokenFingerprint(e.token) {
		t.Fatal("test setup: fingerprints must collide")
	}
	h := bearer(newTok)
	h[GrantHeader] = grant
	wantError(t, e.do(req{path: "/admin-only", header: h}), 403, api.CodeAdminRequired)
}

func TestAuthenticateWithoutARouteIsSafe(t *testing.T) {
	e := newEnv(t)
	r := req{path: "/whoami", header: bearer(e.token)}.build()
	if p, err := e.svc.Authenticate(r, nil); err != nil || p.Kind != api.KindOperator {
		t.Fatalf("%+v, %v", p, err)
	}
}
