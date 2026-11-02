package statusapi_test

import (
	"reflect"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// jsonFields returns the JSON property names of a struct type and the ones always present.
func jsonFields(typ reflect.Type) (all, always map[string]bool) {
	all, always = map[string]bool{}, map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = typ.Field(i).Name
		}
		all[name] = true
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
			always[name] = true
		}
	}
	return all, always
}

// elemStruct returns the struct a field decodes into (through a pointer or a slice), or nil.
func elemStruct(t reflect.Type) (reflect.Type, bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t, t.Kind() == reflect.Struct
}

func compare(t *testing.T, s *apispec.Spec, path string, typ reflect.Type, node any) {
	t.Helper()
	resolved, err := s.V.ResolveNode(node)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	sch, _ := resolved.(map[string]any)
	// A nullable object is a oneOf of null and the object: compare with the object.
	if branches, ok := sch["oneOf"].([]any); ok {
		for _, b := range branches {
			if rb, err := s.V.ResolveNode(b); err == nil {
				if m, ok := rb.(map[string]any); ok && m["type"] != "null" {
					sch = m
				}
			}
		}
	}
	props, _ := sch["properties"].(map[string]any)
	all, always := jsonFields(typ)
	// An open object (additionalProperties: true) is completed by other owners; GET /models/{id} adds
	// files and preset to a model row. A schema-only property is fine there, a Go-only one never is.
	open := sch["additionalProperties"] == true
	getOnly := map[string]bool{"files": true, "preset": true}
	for name := range props {
		if !all[name] && !open && !(typ.Name() == "ModelRow" && getOnly[name]) {
			t.Errorf("%s: the schema has property %q, the Go type %s has no such json field", path, name, typ)
		}
	}
	for name := range all {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: the Go type %s has json field %q, the schema has no such property", path, typ, name)
		}
	}
	if req, ok := sch["required"].([]any); ok {
		for _, r := range req {
			if name := r.(string); !always[name] {
				t.Errorf("%s: %q is required by the schema but the Go field may be omitted", path, name)
			}
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		sub, ok := props[name]
		if !ok {
			continue
		}
		st, isStruct := elemStruct(f.Type)
		if !isStruct || st.PkgPath() != typ.PkgPath() && !strings.HasSuffix(st.PkgPath(), "internal/catalog") {
			continue
		}
		if f.Type.Kind() == reflect.Slice {
			rs, err := s.V.ResolveNode(sub)
			if err != nil {
				t.Errorf("%s.%s: %v", path, name, err)
				continue
			}
			items := rs.(map[string]any)["items"]
			if items == nil {
				continue
			}
			sub = items
		}
		compare(t, s, path+"."+name, st, sub)
	}
}

// TestTypesMirrorSchemas fails when a wire type of this package and the schema of the same name differ:
// a field on one side only, or a required property the Go field may omit (agent.md section 4.14).
func TestTypesMirrorSchemas(t *testing.T) {
	s, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]reflect.Type{
		"Status":         reflect.TypeOf(statusapi.Status{}),
		"Profile":        reflect.TypeOf(statusapi.ProfileBody{}),
		"Catalog":        reflect.TypeOf(statusapi.CatalogBody{}),
		"CatalogRefresh": reflect.TypeOf(statusapi.CatalogRefreshBody{}),
		"Model":          reflect.TypeOf(statusapi.ModelRow{}),
	} {
		node, err := s.Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		compare(t, s, name, typ, node)
	}
}
