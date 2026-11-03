package cli

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The bodies the model and service tests answer with are valid answers of the agent's OpenAPI document
// (agent/api/openapi.yaml), including the two routes that are still planned (download and delete, which
// land with AGENT-15): the commands are built against the documented contract, not against shapes
// this package invented.

func responseSchema(t *testing.T, doc map[string]any, path, method, status string) any {
	t.Helper()
	cur := any(doc)
	for _, k := range []string{"paths", path, method, "responses", status, "content", "application/json", "schema"} {
		m, ok := cur.(map[string]any)
		if !ok || m[k] == nil {
			t.Fatalf("openapi.yaml has no %s %s %s (stopped at %q)", method, path, status, k)
		}
		cur = m[k]
	}
	return cur
}

func TestModelBodiesMatchTheOpenAPIDocument(t *testing.T) {
	v, doc := loadOpenAPI(t)
	cases := []struct {
		name, path, method, status, body string
	}{
		{"load 200", "/models/{id}/load", "post", "200",
			`{"id":"` + qwenID + `","status":"loaded","already_loaded":false,"load_ms":7100,"unloaded":["` + smallID + `"],"op_id":"` + loadOpID + `"}`},
		{"load 200 already loaded", "/models/{id}/load", "post", "200",
			`{"id":"` + qwenID + `","status":"loaded","already_loaded":true,"load_ms":0,"unloaded":[],"op_id":"` + loadOpID + `"}`},
		{"load 202", "/models/{id}/load", "post", "202", `{"id":"` + qwenID + `","status":"loading","op_id":"` + loadOpID + `"}`},
		{"unload 200", "/models/{id}/unload", "post", "200", `{"id":"` + smallID + `","status":"unloaded","already_unloaded":false,"op_id":"op_1"}`},
		{"unload 200 restarted", "/models/{id}/unload", "post", "200",
			`{"id":"` + smallID + `","status":"unloaded","already_unloaded":false,"op_id":"op_1","restarted":true,"restored":["` + qwenID + `"]}`},
		{"default 200", "/models/default", "put", "200", `{"default":"` + qwenID + `","previous":"` + smallID + `","ini_regenerated":true}`},
		{"download 202", "/models/{id}/download", "post", "202", `{"id":"` + smallID + `","sync_run_id":"` + arkRunID + `","queued":true}`},
		{"delete 200", "/models/{id}", "delete", "200", delBody},
		{"restart 200", "/services/{name}/{action}", "post", "200", llamaRestarted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var val any
			if err := yaml.Unmarshal([]byte(tc.body), &val); err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(responseSchema(t, doc, tc.path, tc.method, tc.status), val); err != nil {
				t.Errorf("not a valid answer of %s %s:\n%v", tc.method, tc.path, strings.ReplaceAll(err.Error(), "\n", "\n  "))
			}
		})
	}
}
