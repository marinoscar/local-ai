package api

import (
	"net/http"
	"testing"
)

// A refusal by the Authenticator still records who the caller was, so the access log (and audit)
// know the principal of a 403.
func TestRefusedRequestKeepsItsPrincipalInTheAccessLog(t *testing.T) {
	refuse := AuthFunc(func(*http.Request, *Route) (Principal, error) {
		return Principal{Kind: KindOperator, Credential: CredBearer, Via: "bearer"},
			NewError(CodeAdminRequired, http.StatusForbidden, map[string]any{"pin_required": true})
	})
	s, logs := newTestServer(t, func(o *Options) { o.Auth = refuse })
	rec := do(s.Handler(), req{target: "/api/v1/ping"})
	e := expectError(t, rec, http.StatusForbidden, CodeAdminRequired)
	if e.Error.Details["pin_required"] != true {
		t.Errorf("details: %v", e.Error.Details)
	}
	var found bool
	for _, l := range logs.lines(t) {
		if l["path"] == "/api/v1/ping" {
			found = true
			if l["principal"] != "operator:bearer" {
				t.Errorf("principal in the access log = %v, want operator:bearer", l["principal"])
			}
		}
	}
	if !found {
		t.Fatal("no access log line for the request")
	}
}

func TestPrincipalCarriesTheIdentityFieldsHandlersNeed(t *testing.T) {
	p := Principal{Kind: KindOperator, Credential: CredSocket, Via: "uid=1000", UID: 1000, TokenFingerprint: "lat_…9xQz", SessionID: "s", PortalSessionID: "p"}
	if p.UID != 1000 || p.String() != "operator:uid=1000" {
		t.Fatalf("%+v", p)
	}
}

func TestSecurityCodesHaveEnvelopeText(t *testing.T) {
	for _, code := range []string{CodeWrongPIN, CodeNoPIN, CodeBadLoginCode, CodeSessionExpired, CodeRateLimited, CodePeerNotOperator} {
		for _, lang := range []string{langEN, langES} {
			if tx := lookup(code, lang); tx == unknownText[lang] {
				t.Errorf("%s has no %s text", code, lang)
			}
		}
	}
}
