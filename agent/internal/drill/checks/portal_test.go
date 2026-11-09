package checks

import (
	"net/http"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

const shellHTML = "<!doctype html><html><head><title>Portal</title></head><body></body></html>"

// portalAgent serves the four routes of check 15; a nil handler leaves the route out (404).
type portalParts struct {
	portal, status, chat, props http.HandlerFunc
}

func goodPortal() portalParts {
	return portalParts{
		portal: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'")
			text(200, "text/html; charset=utf-8", shellHTML)(w, r)
		},
		status: jsonOf(200, portal.Status{SchemaVersion: portal.SchemaVersion, GeneratedAt: "2026-11-02T18:04:11Z", NodeID: "ai-test", Mode: "online"}),
		chat:   text(200, "text/html", "<!DOCTYPE html>\n<html><body>llama</body></html>"),
		props:  jsonOf(200, map[string]any{"build_info": "x"}),
	}
}

func (p portalParts) agent() *fakeAgent {
	a := newFakeAgent()
	if p.portal != nil {
		a.handle("/portal", p.portal)
	}
	if p.status != nil {
		a.handle(APIBase+"/portal/status", p.status)
	}
	if p.chat != nil {
		a.handle("/chat/", p.chat)
	}
	if p.props != nil {
		a.handle("/chat/props", p.props)
	}
	return a
}

func TestPortalCheck(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*portalParts)
		result drill.Result
		failed map[string]string
	}{
		{"everything works", func(*portalParts) {}, drill.ResultPass, nil},
		{"/portal answers 500", func(p *portalParts) { p.portal = status(500) }, drill.ResultFail, map[string]string{"portal": "http_500"}},
		{"/portal has no CSP header", func(p *portalParts) { p.portal = text(200, "text/html", shellHTML) }, drill.ResultFail, map[string]string{"portal": "no_csp"}},
		{"/portal is not a page", func(p *portalParts) {
			p.portal = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Security-Policy", "default-src 'self'")
				text(200, "text/plain", "hello")(w, r)
			}
		}, drill.ResultFail, map[string]string{"portal": "not_html"}},
		{"status answers 500", func(p *portalParts) { p.status = status(500) }, drill.ResultFail, map[string]string{"status": "http_500"}},
		{"status is damaged", func(p *portalParts) { p.status = text(200, "application/json", "{") }, drill.ResultFail, map[string]string{"status": "damaged"}},
		{"status has another schema", func(p *portalParts) {
			p.status = jsonOf(200, portal.Status{SchemaVersion: 9, GeneratedAt: "2026-11-02T18:04:11Z", NodeID: "n", Mode: "online"})
		}, drill.ResultFail, map[string]string{"status": "schema_version"}},
		{"status has no node", func(p *portalParts) {
			p.status = jsonOf(200, portal.Status{SchemaVersion: 1, GeneratedAt: "2026-11-02T18:04:11Z", Mode: "online"})
		}, drill.ResultFail, map[string]string{"status": "no_node_id"}},
		{"status has no mode", func(p *portalParts) {
			p.status = jsonOf(200, portal.Status{SchemaVersion: 1, GeneratedAt: "2026-11-02T18:04:11Z", NodeID: "n"})
		}, drill.ResultFail, map[string]string{"status": "no_mode"}},
		{"status has no time", func(p *portalParts) {
			p.status = jsonOf(200, portal.Status{SchemaVersion: 1, GeneratedAt: "yesterday", NodeID: "n", Mode: "online"})
		}, drill.ResultFail, map[string]string{"status": "generated_at"}},
		{"chat is locked (403)", func(p *portalParts) { p.chat = status(403) }, drill.ResultFail, map[string]string{"chat": "http_403"}},
		{"llama is down (502)", func(p *portalParts) { p.chat, p.props = status(502), status(502) }, drill.ResultFail, map[string]string{"chat": "http_502", "chat_props": "http_502"}},
		{"chat serves something that is not the WebUI", func(p *portalParts) { p.chat = text(200, "text/plain", "{}") }, drill.ResultFail, map[string]string{"chat": "not_html"}},
		{"props answers 401", func(p *portalParts) { p.props = status(401) }, drill.ResultFail, map[string]string{"chat_props": "http_401"}},
		{"nothing is mounted", func(p *portalParts) { *p = portalParts{} }, drill.ResultFail,
			map[string]string{"portal": "http_404", "status": "http_404", "chat": "http_404", "chat_props": "http_404"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodPortal()
			tc.mod(&p)
			out := run(t, NewPortalCheck(PortalDeps{HTTP: p.agent()}))
			if out.Result != tc.result {
				t.Fatalf("outcome = %+v, want %s", out, tc.result)
			}
			if tc.result == drill.ResultFail && out.Code != doctor.DRL050 {
				t.Errorf("code = %q", out.Code)
			}
			if tc.failed != nil {
				got, _ := out.Details["failed"].(map[string]any)
				if len(got) != len(tc.failed) {
					t.Fatalf("failed = %v, want %v", got, tc.failed)
				}
				for k, v := range tc.failed {
					if got[k] != v {
						t.Errorf("failed[%s] = %v, want %s", k, got[k], v)
					}
				}
			}
		})
	}
}

func TestPortalCheckUnreachableAndWiring(t *testing.T) {
	a := goodPortal().agent()
	a.take("/", true)
	if out := run(t, NewPortalCheck(PortalDeps{HTTP: a})); out.Result != drill.ResultFail || out.Code != doctor.DRL050 {
		t.Errorf("unreachable: %+v", out)
	}
	c := NewPortalCheck(PortalDeps{})
	if c.ID != "drl.portal" || !c.Critical || c.Order != 15 || c.FailCode != doctor.DRL050 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
}

// TestPortalCheckLeavesNoSession: the check asks for pages as the internal admin and the fake would see
// a cookie if the check sent or kept one.
func TestPortalCheckSendsNoCredentialsAndKeepsNoCookie(t *testing.T) {
	p := goodPortal()
	var seen []http.Header
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { seen = append(seen, r.Header.Clone()); h(w, r) }
	}
	p.portal, p.chat, p.props, p.status = wrap(p.portal), wrap(p.chat), wrap(p.props), wrap(p.status)
	run(t, NewPortalCheck(PortalDeps{HTTP: p.agent()}))
	if len(seen) != 4 {
		t.Fatalf("saw %d requests", len(seen))
	}
	for _, h := range seen {
		if h.Get("Authorization") != "" || h.Get("Cookie") != "" {
			t.Errorf("a credential was sent: %v", h)
		}
	}
}
