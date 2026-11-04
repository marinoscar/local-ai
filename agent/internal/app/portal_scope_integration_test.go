//go:build integration

package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// portalPathFor fills the path parameters of a route pattern with a harmless value.
func portalPathFor(pattern string) string {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '{' {
			j := strings.IndexByte(pattern[i:], '}')
			name := pattern[i+1 : i+j]
			name = strings.TrimSuffix(name, "...")
			out.WriteString("x")
			i += j
			continue
		}
		out.WriteByte(pattern[i])
	}
	return out.String()
}

// The Portal session can reach the routes that list "portal" and the routes at level none, and
// nothing else (survival-portal.md section 3.3): every other route of the daemon's route table answers
// 401 LAI-SVC-110. The table is the real one, so a new route is covered the day it is added.
func TestIntegrationPortalSessionScopeOverTheRouteTable(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	if rp := r.doAuth("POST", "/mode/survival", map[string]any{}, false); rp.status != http.StatusAccepted && rp.status != http.StatusOK {
		t.Fatalf("POST /mode/survival = %d %s", rp.status, rp.body)
	}
	r.waitMode(state.ModeSurvival)

	cookie, err := d.portalStore().Issue()
	if err != nil {
		t.Fatal(err)
	}
	host := d.Components().API.TCPAddr().String()
	cl := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var checked, allowed int
	for _, rt := range d.Components().API.Routes() {
		level := map[api.Level]bool{}
		for _, l := range rt.Auth {
			level[l] = true
		}
		req, err := http.NewRequest(rt.Method, "http://"+host+rt.Path(), nil)
		if err != nil {
			t.Fatal(err)
		}
		// rt.Path() may hold {params}: use a real path.
		req.URL.Path = portalPathFor(rt.Path())
		req.URL.RawPath = ""
		req.AddCookie(&http.Cookie{Name: "lai_portal", Value: cookie})
		req.Header.Set("Origin", "http://"+host)
		req.Header.Set("Content-Type", "application/json")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", rt.Method, rt.Path(), err)
		}
		body := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		body.Write(buf[:n])
		resp.Body.Close()
		isPortalEnded := resp.StatusCode == http.StatusUnauthorized && strings.Contains(body.String(), doctor.SVC110)
		switch {
		case level[api.AuthNone] || level[api.AuthPortal]:
			allowed++
			if isPortalEnded {
				t.Errorf("%s %s is open to a Portal session but answered LAI-SVC-110", rt.Method, rt.Path())
			}
		default:
			checked++
			if !isPortalEnded {
				t.Errorf("%s %s (auth %v) answered %d %.80s with a Portal session, want 401 LAI-SVC-110", rt.Method, rt.Path(), rt.Auth, resp.StatusCode, body.String())
			}
		}
	}
	if checked < 20 || allowed < 5 {
		t.Errorf("only %d refused and %d allowed routes were checked: is the route table empty?", checked, allowed)
	}
}
