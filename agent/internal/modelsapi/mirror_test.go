package modelsapi_test

import (
	"reflect"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
)

// jsonFields returns the JSON property names of a struct type (embedded structs flattened) and the
// ones always present.
func jsonFields(typ reflect.Type, all, always map[string]bool) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			jsonFields(f.Type, all, always)
			continue
		}
		if name == "" {
			name = f.Name
		}
		all[name] = true
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
			always[name] = true
		}
	}
}

func structOf(t reflect.Type) (reflect.Type, bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t, t.Kind() == reflect.Struct
}

// compare checks one object schema against one Go struct, and recurses into the nested objects that
// are Go structs of this package.
func compare(t *testing.T, s *apispec.Spec, path string, typ reflect.Type, node any) {
	t.Helper()
	resolved, err := s.V.ResolveNode(node)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	sch, _ := resolved.(map[string]any)
	props, _ := sch["properties"].(map[string]any)
	all, always := map[string]bool{}, map[string]bool{}
	jsonFields(typ, all, always)
	// An open object (additionalProperties: true) may carry more than the schema lists (the OpenCode
	// snippet); every other property must exist on both sides.
	open := sch["additionalProperties"] == true
	for name := range props {
		if !all[name] && !open {
			t.Errorf("%s: the schema has property %q, the Go type %s has no such json field", path, name, typ)
		}
	}
	for name := range all {
		if _, ok := props[name]; !ok && !open {
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
		st, isStruct := structOf(f.Type)
		if !isStruct || st.PkgPath() != typ.PkgPath() {
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

// TestTypesMirrorSchemas fails when a wire type of this package and the schema of the same name
// differ: a field on one side only, or a required property the Go field may omit (agent.md section
// 4.14). ModelRow, the part of GET /models/{id} that statusapi owns, is checked there; here the
// detail adds files and preset.
func TestTypesMirrorSchemas(t *testing.T) {
	s, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]reflect.Type{
		"ModelList":           reflect.TypeOf(modelsapi.ModelList{}),
		"DownloadAccepted":    reflect.TypeOf(modelsapi.DownloadAccepted{}),
		"DeleteModelResponse": reflect.TypeOf(modelsapi.DeleteResult{}),
		"Key":                 reflect.TypeOf(modelsapi.KeyBody{}),
		"KeyRotated":          reflect.TypeOf(modelsapi.KeyRotated{}),
	} {
		node, err := s.Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		compare(t, s, name, typ, node)
	}

	// The detail is a Model: the row's fields plus files and preset, flattened.
	node, err := s.Schema("Model")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.V.ResolveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	props := resolved.(map[string]any)["properties"].(map[string]any)
	all, always := map[string]bool{}, map[string]bool{}
	jsonFields(reflect.TypeOf(modelsapi.ModelDetail{}), all, always)
	for name := range props {
		if !all[name] {
			t.Errorf("Model: the schema has property %q, ModelDetail has no such json field", name)
		}
	}
	for name := range all {
		if _, ok := props[name]; !ok {
			t.Errorf("Model: ModelDetail has json field %q, the schema has no such property", name)
		}
	}
	for _, name := range []string{"files", "preset"} {
		if !always[name] {
			t.Errorf("ModelDetail.%s may be omitted; GET /models/{id} always sends it", name)
		}
	}
}
