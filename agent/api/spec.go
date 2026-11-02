// Package apispec embeds agent/api/openapi.yaml, the hand-written wire contract of the agent REST API
// (agent.md section 4.14), and answers the questions the conformance tests and the handler-test
// recorder ask of it: which operations exist, which one serves a request, what a response may look like.
//
// The directory is named api because the file is agent/api/openapi.yaml (agent.md section 3.2); the
// package is apispec so that it does not read like internal/api.
package apispec

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/api/schema"
)

//go:embed openapi.yaml
var embedded []byte

// YAML returns the bytes of the embedded openapi.yaml.
func YAML() []byte { return append([]byte(nil), embedded...) }

// Methods are the HTTP methods an OpenAPI path item may define that the agent uses.
var Methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// Spec is a parsed OpenAPI document.
type Spec struct {
	// Doc is the whole document as generic JSON values (YAML numbers become float64).
	Doc map[string]any
	// V validates against schemas of Doc and resolves its $ref pointers.
	V *schema.Validator
	// BasePath is the path of servers[0].url, "/api/v1".
	BasePath string
}

// Operation is one method on one path.
type Operation struct {
	Method string // upper case
	Path   string // the OpenAPI path template, relative to BasePath: "/models/{id}/load"
	Raw    map[string]any
}

// Key is "METHOD path".
func (o Operation) Key() string { return o.Method + " " + o.Path }

// Planned reports whether the operation carries x-localai-status: planned.
func (o Operation) Planned() bool { return o.Raw["x-localai-status"] == "planned" }

// Auth returns the levels of x-localai-auth (one level or a list); nil when it is missing or malformed.
func (o Operation) Auth() []string {
	switch a := o.Raw["x-localai-auth"].(type) {
	case string:
		return []string{a}
	case []any:
		out := make([]string, 0, len(a))
		for _, e := range a {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// Mutating reports whether the operation changes state: every method except GET.
func (o Operation) Mutating() bool { return o.Method != http.MethodGet && o.Method != http.MethodHead }

// Parse parses an OpenAPI document. YAML is converted to plain JSON values so the validator and the
// examples see what a client would see (an unquoted YAML timestamp becomes a string, not a time.Time).
func Parse(data []byte) (*Spec, error) {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	enc, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("openapi.yaml is not JSON-compatible: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(enc, &doc); err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	s := &Spec{Doc: doc, V: schema.New(doc), BasePath: "/api/v1"}
	if servers, ok := doc["servers"].([]any); ok && len(servers) > 0 {
		if first, ok := servers[0].(map[string]any); ok {
			if u, ok := first["url"].(string); ok {
				s.BasePath = strings.TrimRight(u, "/")
			}
		}
	}
	return s, nil
}

var (
	defaultOnce sync.Once
	defaultSpec *Spec
	defaultErr  error
)

// Default returns the parsed embedded document (parsed once).
func Default() (*Spec, error) {
	defaultOnce.Do(func() { defaultSpec, defaultErr = Parse(embedded) })
	return defaultSpec, defaultErr
}

// Operations lists every operation of the document, sorted by path then method.
func (s *Spec) Operations() []Operation {
	paths, _ := s.Doc["paths"].(map[string]any)
	var ops []Operation
	for p, item := range paths {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, method := range Methods {
			if raw, ok := m[strings.ToLower(method)].(map[string]any); ok {
				ops = append(ops, Operation{Method: method, Path: p, Raw: raw})
			}
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

// Match finds the operation that serves method and path, where path is relative to BasePath
// ("/models/qwen/load"). Like net/http's mux, a literal segment beats a {parameter}. HEAD is GET.
func (s *Spec) Match(method, path string) (Operation, bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	segs := splitPath(path)
	var best Operation
	bestScore, found := -1, false
	for _, op := range s.Operations() {
		if op.Method != method {
			continue
		}
		tmpl := splitPath(op.Path)
		if len(tmpl) != len(segs) {
			continue
		}
		score, ok := 0, true
		for i, t := range tmpl {
			switch {
			case strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}"):
			case t == segs[i]:
				score += 1 << (len(tmpl) - i) // earlier literals weigh more, as in the mux
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok && score > bestScore {
			best, bestScore, found = op, score, true
		}
	}
	return best, found
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Response returns the documented response of op for status, with a $ref resolved; ok is false when
// the status is not documented.
func (s *Spec) Response(op Operation, status int) (map[string]any, bool) {
	responses, _ := op.Raw["responses"].(map[string]any)
	node, ok := responses[strconv.Itoa(status)]
	if !ok {
		return nil, false
	}
	resolved, err := s.V.ResolveNode(node)
	if err != nil {
		return nil, false
	}
	m, ok := resolved.(map[string]any)
	return m, ok
}

// Schema returns the schema of #/components/schemas/name.
func (s *Spec) Schema(name string) (any, error) {
	return s.V.Resolve("#/components/schemas/" + name)
}
