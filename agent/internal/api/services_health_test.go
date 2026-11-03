package api

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

type staticHealth struct{ recs []health.Record }

func (s *staticHealth) Records(context.Context) []health.Record { return s.recs }

// The example of companion-services.md section 4.6 (and the issue SVC-01) served through the route:
// the supervisor's records replace the watcher's rows of kiwix and whisper, piper and maps are added,
// the order is llama, kiwix, whisper, piper, maps, and every answer validates against openapi.yaml.
func TestServicesListWithHealthRecords(t *testing.T) {
	at := time.Date(2026, 11, 2, 3, 0, 12, 0, time.UTC)
	check := at.Add(11*time.Hour + 5*time.Minute)
	exit := at.Add(-time.Hour)
	hs := &staticHealth{recs: []health.Record{
		{Name: "kiwix", Unit: services.UnitKiwix, State: health.StateOK, Active: "active", Sub: "running", Since: at, Healthy: true, Enabled: true,
			LastExit: &services.LastExit{Code: 0, At: exit}, LastCheck: check, Detail: "7 books", Problems: []health.Problem{},
			Books: []health.Book{{ContentID: "book-alpha", Name: "alpha_en_all", Root: "internal"}}},
		{Name: "whisper", Unit: services.UnitWhisper, State: health.StateOK, Active: "active", Sub: "running", Healthy: true, Model: "whisper-small", Device: "cpu",
			Problems: []health.Problem{{Code: doctor.SVC013, Severity: doctor.Info}}},
		{Name: "piper", State: health.StateOK, Healthy: true, Voices: []string{"piper-en-medium", "piper-es-medium"}, Problems: []health.Problem{}},
		{Name: "maps", State: health.StateDegraded, Maps: []string{"map-alpha"}, Problems: []health.Problem{{Code: doctor.SVC032, Severity: doctor.Warning}}},
	}}
	r := newSvcRig(t, func(d *ServicesDeps) { d.Health = hs })
	r.status.rows = []services.Status{
		{Name: "llama", Unit: services.UnitLlama, Active: "active", Sub: "running", Since: at, Healthy: true, Health: services.HealthOK, Enabled: true},
		{Name: "kiwix", Unit: services.UnitKiwix, Active: "inactive", Sub: "dead", Health: services.HealthDown},
	}
	rec := r.get("/services", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	want := `{"services":[
	  {"name":"llama","unit":"localai-llama.service","active":"active","sub":"running","since":"2026-11-02T03:00:12Z","restarts":0,"healthy":true,"state":"ok","enabled":true,"last_exit":null},
	  {"name":"kiwix","unit":"localai-kiwix.service","state":"ok","active":"active","sub":"running","since":"2026-11-02T03:00:12Z","restarts":0,"healthy":true,"enabled":true,
	   "last_exit":{"code":0,"at":"2026-11-02T02:00:12Z"},"last_check":"2026-11-02T14:05:12Z","detail":"7 books",
	   "books":[{"content_id":"book-alpha","name":"alpha_en_all","root":"internal"}],"problems":[]},
	  {"name":"whisper","unit":"localai-whisper.service","state":"ok","active":"active","sub":"running","since":null,"restarts":0,"healthy":true,"enabled":false,"last_exit":null,
	   "model":"whisper-small","device":"cpu","problems":[{"code":"LAI-SVC-013","severity":"info"}]},
	  {"name":"piper","unit":null,"state":"ok","healthy":true,"voices":["piper-en-medium","piper-es-medium"],"problems":[]},
	  {"name":"maps","unit":null,"state":"degraded","healthy":false,"maps":["map-alpha"],"problems":[{"code":"LAI-SVC-032","severity":"warning"}]}
	]}`
	var gotV, wantV any
	if err := json.Unmarshal(rec.Body.Bytes(), &gotV); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("body:\n got %s\nwant %s", rec.Body.String(), want)
	}
}

func TestServicesListHealthOnly(t *testing.T) {
	// Without a watcher row the health records still make the list: the answer is never missing a
	// service the supervisor knows, and a service without books has none of the optional keys.
	r := newSvcRig(t, func(d *ServicesDeps) {
		d.Health = &staticHealth{recs: []health.Record{
			{Name: "maps", State: health.StateMissingContent, Problems: []health.Problem{{Code: doctor.SVC030, Severity: doctor.Warning}}},
			{Name: "kiwix", Unit: services.UnitKiwix, State: health.StateDown, Active: "failed", Sub: "failed", Problems: []health.Problem{{Code: doctor.SVC051, Severity: doctor.Warning}, {Code: doctor.SVC002, Severity: doctor.Error}}},
		}}
	})
	rec := r.get("/services", nil)
	var body struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Services) != 2 || body.Services[0]["name"] != "kiwix" || body.Services[1]["name"] != "maps" {
		t.Fatalf("services %v", body.Services)
	}
	maps := body.Services[1]
	for _, k := range []string{"books", "voices", "maps", "model", "device", "detail", "last_check", "active", "since"} {
		if _, ok := maps[k]; ok {
			t.Errorf("maps row has %q: %v", k, maps)
		}
	}
	if maps["state"] != "missing_content" || maps["unit"] != nil {
		t.Errorf("maps row %v", maps)
	}
	probs := body.Services[0]["problems"].([]any)
	if len(probs) != 2 || probs[0].(map[string]any)["code"] != doctor.SVC051 {
		t.Errorf("kiwix problems %v", probs)
	}
}
