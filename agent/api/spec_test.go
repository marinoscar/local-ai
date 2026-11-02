package apispec

import "testing"

func TestDefaultParses(t *testing.T) {
	s, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if s.BasePath != "/api/v1" {
		t.Fatalf("base path %q", s.BasePath)
	}
	if len(s.Operations()) < 50 {
		t.Fatalf("only %d operations", len(s.Operations()))
	}
	if string(YAML()) != string(embedded) || len(YAML()) == 0 {
		t.Fatal("YAML must return the embedded document")
	}
}

func TestParseTurnsYAMLIntoJSONValues(t *testing.T) {
	s, err := Parse([]byte("a: 2026-11-02T15:04:05Z\nb: 3\nc: [1, 2]\nservers: [{url: /x/}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Doc["a"].(string); !ok || got != "2026-11-02T15:04:05Z" {
		t.Fatalf("an unquoted timestamp must become a string, got %#v", s.Doc["a"])
	}
	if got, ok := s.Doc["b"].(float64); !ok || got != 3 {
		t.Fatalf("numbers are float64, got %#v", s.Doc["b"])
	}
	if s.BasePath != "/x" {
		t.Fatalf("base path %q", s.BasePath)
	}
	if _, err := Parse([]byte("a: [")); err == nil {
		t.Fatal("bad YAML must fail")
	}
}

func TestMatch(t *testing.T) {
	s, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         string // operation key, empty for no match
	}{
		{"GET", "/health", "GET /health"},
		{"HEAD", "/health", "GET /health"},
		{"GET", "/health/", "GET /health"},
		{"POST", "/health", ""},
		{"GET", "/models", "GET /models"},
		{"GET", "/models/qwen", "GET /models/{id}"},
		{"DELETE", "/models/qwen", "DELETE /models/{id}"},
		{"PUT", "/models/default", "PUT /models/default"},
		{"GET", "/models/default", "GET /models/{id}"},
		{"POST", "/models/qwen/load", "POST /models/{id}/load"},
		{"POST", "/models/qwen/nope", ""},
		{"POST", "/ark/drives/ark-1/eject", "POST /ark/drives/{root_id}/eject"},
		{"PUT", "/ark/drives/ark-1/profile", "PUT /ark/drives/{root_id}/profile"},
		{"POST", "/services/llama/restart", "POST /services/{name}/{action}"},
		{"GET", "/nope", ""},
		{"GET", "/", ""},
	} {
		op, ok := s.Match(tc.method, tc.path)
		got := ""
		if ok {
			got = op.Key()
		}
		if got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestResponseResolvesRefs(t *testing.T) {
	s, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := s.Match("GET", "/health")
	if !ok {
		t.Fatal("no GET /health")
	}
	if _, ok := s.Response(op, 200); !ok {
		t.Error("200 is documented")
	}
	r, ok := s.Response(op, 403)
	if !ok || r["description"] == nil {
		t.Errorf("403 is a $ref to components.responses.Forbidden: %v %v", r, ok)
	}
	if _, ok := s.Response(op, 418); ok {
		t.Error("418 is not documented")
	}
}
