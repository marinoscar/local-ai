package cli

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/api/schema"
)

// The fixture bodies the golden tests use are valid answers of the agent's OpenAPI document
// (agent/api/openapi.yaml), so the golden output is rendered from shapes the agent can really send,
// not from shapes this package invented.

func loadOpenAPI(t *testing.T) (*schema.Validator, map[string]any) {
	t.Helper()
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return schema.New(doc), doc
}

// okSchema returns the schema of the 200 answer of GET path.
func okSchema(t *testing.T, doc map[string]any, path string) any {
	t.Helper()
	cur := any(doc)
	for _, k := range []string{"paths", path, "get", "responses", "200", "content", "application/json", "schema"} {
		m, ok := cur.(map[string]any)
		if !ok || m[k] == nil {
			t.Fatalf("openapi.yaml has no %s (stopped at %q)", path, k)
		}
		cur = m[k]
	}
	return cur
}

func TestFixturesMatchTheOpenAPIDocument(t *testing.T) {
	v, doc := loadOpenAPI(t)
	cases := []struct{ fixture, path string }{
		{"status.online", "/status"},
		{"status.survival", "/status"},
		{"models", "/models"},
		{"model.qwen", "/models/{id}"},
		{"services", "/services"},
		{"logs", "/logs"},
		{"doctor.mixed", "/doctor"},
		{"doctor.warn", "/doctor"},
		{"doctor.ok", "/doctor"},
		{"ark", "/ark"},
		{"ark.sync", "/ark/sync"},
		{"drives", "/ark/drives"},
		{"cloud", "/cloud"},
		{"cloud.notenrolled", "/cloud"},
		{"profile", "/profile"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			var val any
			if err := yaml.Unmarshal([]byte(roBody(t, tc.fixture)), &val); err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(okSchema(t, doc, tc.path), val); err != nil {
				t.Errorf("%s is not a valid %s answer:\n%v", tc.fixture, tc.path, strings.ReplaceAll(err.Error(), "\n", "\n  "))
			}
		})
	}
}
