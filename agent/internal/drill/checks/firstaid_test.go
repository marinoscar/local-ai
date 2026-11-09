package checks

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/portal"
)

func statusWith(links ...portal.FirstAid) portal.Status {
	return portal.Status{SchemaVersion: portal.SchemaVersion, NodeID: "ai-test", Mode: "online", FirstAid: links}
}

func jsonOf(code int, v any) http.HandlerFunc {
	b, _ := json.Marshal(v)
	return text(code, "application/json", string(b))
}

func TestFirstAidLinks(t *testing.T) {
	a1 := portal.FirstAid{Key: "bleeding", Title: "Bleeding", URL: "/library/content/med/A/Bleeding", Available: true}
	a2 := portal.FirstAid{Key: "burns", Title: "Burns", URL: "/library/content/med/A/Burns", Available: true}
	gone := portal.FirstAid{Key: "water", Title: "Water", URL: "/library/content/x/A/Water", Available: false}
	cases := []struct {
		name   string
		status http.HandlerFunc
		pages  map[string]http.HandlerFunc
		result drill.Result
		code   string
	}{
		{"every link is available and answers 200", jsonOf(200, statusWith(a1, a2)),
			map[string]http.HandlerFunc{a1.URL: status(200), a2.URL: status(200)}, drill.ResultPass, ""},
		{"a link the catalog lists is not available", jsonOf(200, statusWith(a1, gone)),
			map[string]http.HandlerFunc{a1.URL: status(200)}, drill.ResultWarn, doctor.SVC109},
		{"an available link answers 404", jsonOf(200, statusWith(a1, a2)),
			map[string]http.HandlerFunc{a1.URL: status(200), a2.URL: status(404)}, drill.ResultWarn, doctor.SVC109},
		{"an available link answers 503 (kiwix died since the probe)", jsonOf(200, statusWith(a1)),
			map[string]http.HandlerFunc{a1.URL: status(503)}, drill.ResultWarn, doctor.SVC109},
		{"no links in the catalog", jsonOf(200, statusWith()), nil, drill.ResultSkip, ""},
		{"the status route fails", status(500), nil, drill.ResultWarn, doctor.SVC109},
		{"the status body is damaged", text(200, "application/json", "{"), nil, drill.ResultWarn, doctor.SVC109},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent()
			a.handle(APIBase+"/portal/status", tc.status)
			for p, h := range tc.pages {
				a.handle(p, h)
			}
			out := run(t, NewFirstAidCheck(FirstAidDeps{HTTP: a}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
		})
	}
}

func TestFirstAidNeverFailsAndIsNotCritical(t *testing.T) {
	c := NewFirstAidCheck(FirstAidDeps{HTTP: newFakeAgent()})
	if c.ID != "drl.portal.firstaid" || c.Critical || c.Order != 9 {
		t.Errorf("check = %+v", c)
	}
	// A dead agent route is a warn: the table has no fail column for this check.
	a := newFakeAgent()
	a.take("/api", true)
	if out := run(t, NewFirstAidCheck(FirstAidDeps{HTTP: a})); out.Result != drill.ResultWarn {
		t.Errorf("unreachable: %+v", out)
	}
	if out := run(t, NewFirstAidCheck(FirstAidDeps{})); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
}
