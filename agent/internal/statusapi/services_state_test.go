package statusapi_test

import (
	"encoding/json"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// status.services carries the health state of every service the view lists, and a service without a
// unit (piper, maps) has "unit": null (companion-services.md section 4.6).
func TestStatusServicesCarryStateAndNullUnit(t *testing.T) {
	r := newRig(t, func(r *rig, d *statusapi.Deps) {
		r.svc.list = []services.Status{
			{Name: "llama", Unit: services.UnitLlama, Active: "active", Sub: "running", Healthy: true, Health: services.HealthOK},
			{Name: "kiwix", Unit: services.UnitKiwix, Active: "inactive", Sub: "dead", Health: "missing_content"},
			{Name: "piper", Health: "degraded"},
		}
		d.Services = r.svc
	})
	rec := r.get("/status", "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Services []map[string]any `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Services) != 3 {
		t.Fatalf("services %v", body.Services)
	}
	want := []struct {
		name  string
		unit  any
		state string
	}{{"llama", services.UnitLlama, "ok"}, {"kiwix", services.UnitKiwix, "missing_content"}, {"piper", nil, "degraded"}}
	for i, w := range want {
		row := body.Services[i]
		if row["name"] != w.name || row["unit"] != w.unit || row["state"] != w.state {
			t.Errorf("row %d = %v, want %+v", i, row, w)
		}
	}
	if _, ok := body.Services[2]["unit"]; !ok {
		t.Error("piper has no unit key: it must be null")
	}
}
