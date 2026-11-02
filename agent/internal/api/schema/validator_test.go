package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// parse decodes JSON into the generic form the validator works on.
func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test JSON %q: %v", s, err)
	}
	return v
}

// TestKeywords covers every supported keyword with at least one passing and one failing instance.
func TestKeywords(t *testing.T) {
	doc := parse(t, `{"components": {"schemas": {
		"Name": {"type": "string", "minLength": 2},
		"Node": {"type": "object", "required": ["name"], "additionalProperties": false, "properties": {"name": {"$ref": "#/components/schemas/Name"}}}
	}}}`)
	v := New(doc)
	tests := []struct {
		name     string
		schema   string
		instance string
		wantErr  string // substring; empty means valid
	}{
		// type
		{"type string ok", `{"type":"string"}`, `"a"`, ""},
		{"type string fails on number", `{"type":"string"}`, `1`, "expected type string, got integer"},
		{"type integer ok", `{"type":"integer"}`, `3`, ""},
		{"type integer accepts 3.0", `{"type":"integer"}`, `3.0`, ""},
		{"type integer fails on 3.5", `{"type":"integer"}`, `3.5`, "expected type integer, got number"},
		{"type number ok", `{"type":"number"}`, `3.5`, ""},
		{"type boolean ok", `{"type":"boolean"}`, `true`, ""},
		{"type boolean fails on string", `{"type":"boolean"}`, `"true"`, "expected type boolean"},
		{"type null ok", `{"type":"null"}`, `null`, ""},
		{"type null fails on 0", `{"type":"null"}`, `0`, "expected type null"},
		{"type object fails on array", `{"type":"object"}`, `[]`, "expected type object, got array"},
		{"type array fails on object", `{"type":"array"}`, `{}`, "expected type array, got object"},
		{"type list ok", `{"type":["string","null"]}`, `null`, ""},
		{"type list fails", `{"type":["string","null"]}`, `1`, "expected type string or null"},
		// properties, required
		{"properties ok", `{"type":"object","properties":{"a":{"type":"integer"}}}`, `{"a":1}`, ""},
		{"properties fails", `{"type":"object","properties":{"a":{"type":"integer"}}}`, `{"a":"x"}`, "$.a: expected type integer"},
		{"required ok", `{"required":["a"]}`, `{"a":null}`, ""},
		{"required fails", `{"required":["a","b"]}`, `{"a":1}`, `required property "b" is missing`},
		// additionalProperties
		{"additionalProperties absent allows extras", `{"type":"object","properties":{"a":{}}}`, `{"a":1,"b":2}`, ""},
		{"additionalProperties true allows extras", `{"additionalProperties":true,"properties":{"a":{}}}`, `{"a":1,"b":2}`, ""},
		{"additionalProperties false rejects extras", `{"additionalProperties":false,"properties":{"a":{}}}`, `{"a":1,"b":2}`, `property "b" is not allowed`},
		{"additionalProperties schema ok", `{"additionalProperties":{"type":"integer"}}`, `{"a":1,"b":2}`, ""},
		{"additionalProperties schema fails", `{"additionalProperties":{"type":"integer"}}`, `{"a":1,"b":"x"}`, "$.b: expected type integer"},
		// enum, const
		{"enum ok", `{"enum":["a","b"]}`, `"b"`, ""},
		{"enum fails", `{"enum":["a","b"]}`, `"c"`, "is not one of the allowed values"},
		{"enum compares numbers by value", `{"enum":[1,2]}`, `1.0`, ""},
		{"enum null ok", `{"enum":[null,"a"]}`, `null`, ""},
		{"const ok", `{"const":true}`, `true`, ""},
		{"const fails", `{"const":true}`, `false`, "is not the constant"},
		// items
		{"items ok", `{"type":"array","items":{"type":"string"}}`, `["a","b"]`, ""},
		{"items fails", `{"type":"array","items":{"type":"string"}}`, `["a",2]`, "$[1]: expected type string"},
		{"items empty ok", `{"type":"array","items":{"type":"string"}}`, `[]`, ""},
		{"minItems ok", `{"minItems":1}`, `[1]`, ""},
		{"minItems fails", `{"minItems":1}`, `[]`, "fewer than minItems"},
		{"maxItems fails", `{"maxItems":1}`, `[1,2]`, "more than maxItems"},
		// oneOf
		{"oneOf ok", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `1`, ""},
		{"oneOf fails when none match", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `true`, "matches 0"},
		{"oneOf fails when two match", `{"oneOf":[{"type":"number"},{"type":"integer"}]}`, `1`, "matches 2"},
		{"oneOf with null", `{"oneOf":[{"type":"null"},{"type":"object","required":["a"]}]}`, `null`, ""},
		// $ref
		{"ref ok", `{"$ref":"#/components/schemas/Node"}`, `{"name":"ab"}`, ""},
		{"ref follows nested ref and fails", `{"$ref":"#/components/schemas/Node"}`, `{"name":"a"}`, "$.name: is shorter than minLength 2"},
		{"ref applies sibling additionalProperties", `{"$ref":"#/components/schemas/Node"}`, `{"name":"ab","extra":1}`, `property "extra" is not allowed`},
		{"ref to missing schema is a violation", `{"$ref":"#/components/schemas/Nope"}`, `1`, `"Nope" not found`},
		// format
		{"date-time ok", `{"type":"string","format":"date-time"}`, `"2026-11-02T15:04:05Z"`, ""},
		{"date-time with offset ok", `{"type":"string","format":"date-time"}`, `"2026-11-02T15:04:05.250+02:00"`, ""},
		{"date-time fails without zone", `{"type":"string","format":"date-time"}`, `"2026-11-02T15:04:05"`, "not an RFC 3339 date-time"},
		{"date-time fails on a date", `{"type":"string","format":"date-time"}`, `"2026-11-02"`, "not an RFC 3339 date-time"},
		{"binary accepts a string", `{"type":"string","format":"binary"}`, `"x"`, ""},
		// pattern, length, bounds
		{"pattern ok", `{"pattern":"^LAI-[A-Z]{2,3}-[0-9]{3}$"}`, `"LAI-AGT-020"`, ""},
		{"pattern fails", `{"pattern":"^LAI-[A-Z]{2,3}-[0-9]{3}$"}`, `"LAI-agt-20"`, "does not match the pattern"},
		{"pattern ignores non-strings", `{"pattern":"^a$"}`, `1`, ""},
		{"minLength fails", `{"minLength":3}`, `"ab"`, "shorter than minLength"},
		{"maxLength counts characters", `{"maxLength":2}`, `"éé"`, ""},
		{"maxLength fails", `{"maxLength":2}`, `"abc"`, "longer than maxLength"},
		{"minimum ok", `{"minimum":1}`, `1`, ""},
		{"minimum fails", `{"minimum":1}`, `0`, "below the minimum"},
		{"maximum fails", `{"maximum":10}`, `11`, "above the maximum"},
		// annotations are ignored
		{"annotations ignored", `{"type":"string","description":"d","title":"t","examples":[1],"default":2,"deprecated":true,"$comment":"c","x-localai-note":"n"}`, `"a"`, ""},
		// boolean schemas
		{"true schema", `true`, `1`, ""},
		{"false schema", `false`, `1`, "no value is allowed here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := parse(t, tc.schema)
			err := v.Validate(s, parse(t, tc.instance))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want valid, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestGoValues checks that Go-typed numbers (from a YAML decode) validate like JSON numbers.
func TestGoValues(t *testing.T) {
	v := New(nil)
	if err := v.Validate(map[string]any{"type": "integer", "minimum": 1}, 3); err != nil {
		t.Fatalf("int: %v", err)
	}
	if err := v.Validate(map[string]any{"type": "number"}, int64(3)); err != nil {
		t.Fatalf("int64: %v", err)
	}
	if err := v.Validate(map[string]any{"type": "string"}, struct{}{}); err == nil {
		t.Fatal("a struct must not match any JSON type")
	}
}

func TestErrorsAreAllReportedWithPaths(t *testing.T) {
	v := New(nil)
	s := parse(t, `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"b":{"type":"integer"}}}`)
	err := v.Validate(s, parse(t, `{"b":"x","c":1}`))
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{`required property "a" is missing`, "$.b: expected type integer", `property "c" is not allowed`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

// TestCheckSchema proves that an unsupported keyword, a bad ref or format fails, so openapi.yaml cannot
// lean on validation this package does not do.
func TestCheckSchema(t *testing.T) {
	doc := parse(t, `{"components": {"schemas": {"A": {"type": "string"}}}}`)
	v := New(doc)
	tests := []struct {
		name    string
		schema  string
		wantErr string
	}{
		{"all supported keywords", `{"type":["object","null"],"properties":{"a":{"$ref":"#/components/schemas/A"},"b":{"type":"array","items":{"type":"string","format":"date-time"}}},"required":["a"],"enum":[null],"const":null,"additionalProperties":{"type":"integer"},"oneOf":[{"type":"null"},{"type":"object"}],"pattern":"^a","minimum":0,"maximum":1,"minLength":0,"maxLength":1,"minItems":0,"maxItems":1,"description":"d","x-ext":1}`, ""},
		{"allOf", `{"allOf":[{"type":"string"}]}`, `unsupported keyword "allOf"`},
		{"anyOf", `{"anyOf":[{"type":"string"}]}`, `unsupported keyword "anyOf"`},
		{"not", `{"not":{}}`, `unsupported keyword "not"`},
		{"patternProperties", `{"patternProperties":{"^a":{}}}`, `unsupported keyword "patternProperties"`},
		{"minProperties", `{"minProperties":1}`, `unsupported keyword "minProperties"`},
		{"uniqueItems", `{"uniqueItems":true}`, `unsupported keyword "uniqueItems"`},
		{"nested unsupported keyword", `{"properties":{"a":{"items":{"multipleOf":2}}}}`, `$.properties.a.items: unsupported keyword "multipleOf"`},
		{"unknown type", `{"type":"float"}`, `unknown type "float"`},
		{"empty type list", `{"type":[]}`, "type must be a name"},
		{"missing ref", `{"$ref":"#/components/schemas/Nope"}`, `"Nope" not found`},
		{"remote ref", `{"$ref":"https://example.test/s.json"}`, "only local references"},
		{"unsupported format", `{"type":"string","format":"uuid"}`, "unsupported format"},
		{"bad pattern", `{"pattern":"("}`, "pattern"},
		{"required not a list", `{"required":"a"}`, "required must be a list"},
		{"enum empty", `{"enum":[]}`, "enum must be a non-empty list"},
		{"oneOf empty", `{"oneOf":[]}`, "oneOf must be a non-empty list"},
		{"non-numeric bound", `{"minimum":"x"}`, "minimum must be a number"},
		{"schema is a string", `"x"`, "a schema must be an object or a boolean"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := v.CheckSchema(parse(t, tc.schema))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("want a supported schema, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	doc := parse(t, `{"a/b": {"c~d": [10, 20]}, "x": {"$ref": "#/y"}, "y": {"$ref": "#/z"}, "z": {"ok": true}, "loop": {"$ref": "#/loop"}}`)
	v := New(doc)
	got, err := v.Resolve("#/a~1b/c~0d/1")
	if err != nil || got != float64(20) {
		t.Fatalf("pointer escapes and list index: %v %v", got, err)
	}
	if _, err := v.Resolve("#/nope"); err == nil {
		t.Fatal("a missing pointer must fail")
	}
	if _, err := v.Resolve("#/a~1b/c~0d/9"); err == nil {
		t.Fatal("an index out of range must fail")
	}
	node, err := v.ResolveNode(map[string]any{"$ref": "#/x"})
	if err != nil || node.(map[string]any)["ok"] != true {
		t.Fatalf("ResolveNode follows a chain: %v %v", node, err)
	}
	if _, err := v.ResolveNode(map[string]any{"$ref": "#/loop"}); err == nil {
		t.Fatal("a $ref cycle must fail")
	}
}
