// Package schema is the small JSON Schema validator behind the OpenAPI conformance tests
// (agent.md section 4.14). It supports a subset of JSON Schema 2020-12 and nothing else:
//
//	type (one name or a list), properties, required, enum, const, items, additionalProperties
//	(false, true or a schema), oneOf, $ref (local, "#/..."), format (date-time and binary),
//	pattern, minimum, maximum, minLength, maxLength, minItems, maxItems
//
// plus the annotations description, title, examples, default, deprecated, readOnly, writeOnly,
// $comment and any "x-" keyword, which are accepted and ignored. CheckSchema rejects every other
// keyword, so nobody can rely in openapi.yaml on a validation this package does not perform.
//
// Instances are JSON-decoded values: map[string]any, []any, string, bool, nil and numbers (float64
// from encoding/json, or any Go integer or float type).
package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// validating keywords this package implements.
var validating = map[string]bool{
	"type": true, "properties": true, "required": true, "enum": true, "const": true, "items": true,
	"additionalProperties": true, "oneOf": true, "$ref": true, "format": true, "pattern": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true, "minItems": true, "maxItems": true,
}

// annotations are accepted and ignored.
var annotations = map[string]bool{
	"description": true, "title": true, "examples": true, "default": true, "deprecated": true,
	"readOnly": true, "writeOnly": true, "$comment": true,
}

// formats this package knows: date-time is validated, binary only marks a byte stream.
var formats = map[string]bool{"date-time": true, "binary": true}

var typeNames = map[string]bool{
	"null": true, "boolean": true, "object": true, "array": true, "number": true, "integer": true, "string": true,
}

// Validator validates instances against schemas that live in one document, which is where local
// $ref pointers resolve (for OpenAPI: the whole openapi.yaml, so "#/components/schemas/X").
type Validator struct {
	doc      any
	patterns map[string]*regexp.Regexp
}

// New returns a Validator whose $ref pointers resolve inside doc.
func New(doc any) *Validator {
	return &Validator{doc: doc, patterns: map[string]*regexp.Regexp{}}
}

// Resolve follows a local reference such as "#/components/schemas/Health".
func (v *Validator) Resolve(ref string) (any, error) {
	if ref == "#" {
		return v.doc, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("$ref %q: only local references (\"#/...\") are supported", ref)
	}
	cur := v.doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, fmt.Errorf("$ref %q: %q not found", ref, part)
			}
			cur = next
		case []any:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("$ref %q: bad index %q", ref, part)
			}
			cur = node[idx]
		default:
			return nil, fmt.Errorf("$ref %q: cannot descend into %q", ref, part)
		}
	}
	return cur, nil
}

// ResolveNode follows a chain of {"$ref": ...} objects and returns the object they end at. A node
// without $ref is returned as is. The siblings of a $ref are ignored (descriptions only).
func (v *Validator) ResolveNode(node any) (any, error) {
	for i := 0; i < 32; i++ {
		m, ok := node.(map[string]any)
		if !ok {
			return node, nil
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return node, nil
		}
		next, err := v.Resolve(ref)
		if err != nil {
			return nil, err
		}
		node = next
	}
	return nil, errors.New("$ref chain too long (a cycle?)")
}

// CheckSchema reports every use of an unsupported keyword, a malformed supported one, an unknown
// format and every unresolvable local $ref in schema and the schemas nested in it. It does not
// follow a $ref (the referenced schema is checked where it lives).
func (v *Validator) CheckSchema(schema any) error {
	var errs []error
	v.check("$", schema, &errs)
	return errors.Join(errs...)
}

func (v *Validator) check(path string, schema any, errs *[]error) {
	switch s := schema.(type) {
	case bool:
		return
	case map[string]any:
		v.checkObject(path, s, errs)
	default:
		*errs = append(*errs, fmt.Errorf("%s: a schema must be an object or a boolean, got %T", path, schema))
	}
}

func (v *Validator) checkObject(path string, s map[string]any, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	for _, k := range sortedKeys(s) {
		if !validating[k] && !annotations[k] && !strings.HasPrefix(k, "x-") {
			fail("unsupported keyword %q (supported: %s)", k, supportedList())
		}
	}
	if t, ok := s["type"]; ok {
		for _, name := range typeList(t) {
			if !typeNames[name] {
				fail("unknown type %q", name)
			}
		}
		if len(typeList(t)) == 0 {
			fail("type must be a name or a list of names")
		}
	}
	if p, ok := s["properties"]; ok {
		props, isMap := p.(map[string]any)
		if !isMap {
			fail("properties must be an object")
		}
		for _, name := range sortedKeys(props) {
			v.check(path+".properties."+name, props[name], errs)
		}
	}
	if r, ok := s["required"]; ok {
		list, isList := r.([]any)
		if !isList {
			fail("required must be a list of names")
		}
		for _, n := range list {
			if _, isStr := n.(string); !isStr {
				fail("required must hold strings, got %T", n)
			}
		}
	}
	if e, ok := s["enum"]; ok {
		if list, isList := e.([]any); !isList || len(list) == 0 {
			fail("enum must be a non-empty list")
		}
	}
	if it, ok := s["items"]; ok {
		v.check(path+".items", it, errs)
	}
	if ap, ok := s["additionalProperties"]; ok {
		v.check(path+".additionalProperties", ap, errs)
	}
	if o, ok := s["oneOf"]; ok {
		list, isList := o.([]any)
		if !isList || len(list) == 0 {
			fail("oneOf must be a non-empty list of schemas")
		}
		for i, sub := range list {
			v.check(fmt.Sprintf("%s.oneOf[%d]", path, i), sub, errs)
		}
	}
	if r, ok := s["$ref"]; ok {
		ref, isStr := r.(string)
		if !isStr {
			fail("$ref must be a string")
		} else if _, err := v.Resolve(ref); err != nil {
			fail("%v", err)
		}
	}
	if f, ok := s["format"]; ok {
		if name, isStr := f.(string); !isStr || !formats[name] {
			fail("unsupported format %v (supported: date-time, binary)", f)
		}
	}
	if p, ok := s["pattern"]; ok {
		pat, isStr := p.(string)
		if !isStr {
			fail("pattern must be a string")
		} else if _, err := regexp.Compile(pat); err != nil {
			fail("pattern %q: %v", pat, err)
		}
	}
	for _, k := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
		if n, ok := s[k]; ok {
			if _, isNum := number(n); !isNum {
				fail("%s must be a number", k)
			}
		}
	}
}

func supportedList() string {
	names := make([]string, 0, len(validating))
	for k := range validating {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Validate checks value against schema and returns every violation, each prefixed with its JSON
// path ("$.error.code: ..."), or nil. Run CheckSchema first: Validate ignores a keyword it does not
// know and reports an unresolvable $ref as a violation.
func (v *Validator) Validate(schema any, value any) error {
	var errs []error
	v.validate("$", schema, value, &errs)
	return errors.Join(errs...)
}

func (v *Validator) validate(path string, schema any, value any, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	switch s := schema.(type) {
	case bool:
		if !s {
			fail("no value is allowed here")
		}
		return
	case map[string]any:
		v.validateObject(path, s, value, errs)
	default:
		fail("a schema must be an object or a boolean, got %T", schema)
	}
}

func (v *Validator) validateObject(path string, s map[string]any, value any, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	if ref, ok := s["$ref"].(string); ok {
		target, err := v.Resolve(ref)
		if err != nil {
			fail("%v", err)
		} else {
			v.validate(path, target, value, errs)
		}
	}
	if t, ok := s["type"]; ok {
		names := typeList(t)
		if !matchesType(names, value) {
			fail("expected type %s, got %s", strings.Join(names, " or "), kind(value))
			return // the keywords below assume the type
		}
	}
	if e, ok := s["enum"].([]any); ok {
		found := false
		for _, want := range e {
			if equal(want, value) {
				found = true
				break
			}
		}
		if !found {
			fail("%s is not one of the allowed values %s", show(value), show(e))
		}
	}
	if c, ok := s["const"]; ok && !equal(c, value) {
		fail("%s is not the constant %s", show(value), show(c))
	}
	if o, ok := s["oneOf"].([]any); ok {
		matches := 0
		for _, sub := range o {
			var subErrs []error
			v.validate(path, sub, value, &subErrs)
			if len(subErrs) == 0 {
				matches++
			}
		}
		if matches != 1 {
			fail("must match exactly one of %d schemas, matches %d", len(o), matches)
		}
	}
	switch val := value.(type) {
	case map[string]any:
		v.validateMap(path, s, val, errs)
	case []any:
		v.validateList(path, s, val, errs)
	case string:
		v.validateString(path, s, val, errs)
	default:
		if n, isNum := number(value); isNum {
			if lim, ok := number(s["minimum"]); ok && n < lim {
				fail("%v is below the minimum %v", n, lim)
			}
			if lim, ok := number(s["maximum"]); ok && n > lim {
				fail("%v is above the maximum %v", n, lim)
			}
		}
	}
}

func (v *Validator) validateMap(path string, s map[string]any, obj map[string]any, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	props, _ := s["properties"].(map[string]any)
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, isStr := r.(string); isStr {
				if _, present := obj[name]; !present {
					fail("required property %q is missing", name)
				}
			}
		}
	}
	for _, name := range sortedKeys(obj) {
		if sub, known := props[name]; known {
			v.validate(path+"."+name, sub, obj[name], errs)
			continue
		}
		switch ap := s["additionalProperties"].(type) {
		case bool:
			if !ap {
				fail("property %q is not allowed (additionalProperties is false)", name)
			}
		case map[string]any:
			v.validate(path+"."+name, ap, obj[name], errs)
		}
	}
}

func (v *Validator) validateList(path string, s map[string]any, list []any, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	if lim, ok := number(s["minItems"]); ok && float64(len(list)) < lim {
		fail("has %d items, fewer than minItems %v", len(list), lim)
	}
	if lim, ok := number(s["maxItems"]); ok && float64(len(list)) > lim {
		fail("has %d items, more than maxItems %v", len(list), lim)
	}
	if items, ok := s["items"]; ok {
		for i, el := range list {
			v.validate(fmt.Sprintf("%s[%d]", path, i), items, el, errs)
		}
	}
}

func (v *Validator) validateString(path string, s map[string]any, str string, errs *[]error) {
	fail := func(format string, args ...any) { *errs = append(*errs, fmt.Errorf(path+": "+format, args...)) }
	n := float64(utf8.RuneCountInString(str))
	if lim, ok := number(s["minLength"]); ok && n < lim {
		fail("is shorter than minLength %v", lim)
	}
	if lim, ok := number(s["maxLength"]); ok && n > lim {
		fail("is longer than maxLength %v", lim)
	}
	if p, ok := s["pattern"].(string); ok {
		re, err := v.regexp(p)
		if err != nil {
			fail("pattern %q: %v", p, err)
		} else if !re.MatchString(str) {
			fail("%q does not match the pattern %q", str, p)
		}
	}
	if f, ok := s["format"].(string); ok && f == "date-time" {
		if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
			fail("%q is not an RFC 3339 date-time", str)
		}
	}
}

func (v *Validator) regexp(p string) (*regexp.Regexp, error) {
	if re, ok := v.patterns[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	v.patterns[p] = re
	return re, nil
}

// typeList turns a type keyword (a name or a list of names) into a list; empty when malformed.
func typeList(t any) []string {
	switch tt := t.(type) {
	case string:
		return []string{tt}
	case []any:
		out := make([]string, 0, len(tt))
		for _, e := range tt {
			name, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, name)
		}
		return out
	}
	return nil
}

func matchesType(names []string, value any) bool {
	for _, n := range names {
		switch n {
		case "null":
			if value == nil {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "number":
			if _, ok := number(value); ok {
				return true
			}
		case "integer":
			if f, ok := number(value); ok && f == math.Trunc(f) && !math.IsInf(f, 0) {
				return true
			}
		}
	}
	return false
}

// number returns v as a float64 when it is a numeric value.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	if f, ok := number(v); ok {
		if f == math.Trunc(f) {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

// equal compares two JSON values; numbers compare by value whatever their Go type.
func equal(a, b any) bool {
	if fa, ok := number(a); ok {
		fb, ok := number(b)
		return ok && fa == fb
	}
	switch ta := a.(type) {
	case map[string]any:
		tb, ok := b.(map[string]any)
		if !ok || len(ta) != len(tb) {
			return false
		}
		for k, va := range ta {
			vb, present := tb[k]
			if !present || !equal(va, vb) {
				return false
			}
		}
		return true
	case []any:
		tb, ok := b.([]any)
		if !ok || len(ta) != len(tb) {
			return false
		}
		for i := range ta {
			if !equal(ta[i], tb[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(b) > 80 {
		return string(b[:77]) + "..."
	}
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
