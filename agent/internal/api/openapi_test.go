package api

// Conformance of agent/api/openapi.yaml (agent.md section 4.14): the document is valid and uses only
// validation the in-repo validator performs; it describes exactly the routes of the spec table of
// agent.md section 3.8 and of api.Routes(); every operation carries its auth level, its error statuses
// and the headers of section 3.7; the doctor codes it names exist; the Go wire types mirror its schemas;
// and the recorded responses of handler tests (apitest.Record) match it.
//
// Not-yet-implemented operations carry `x-localai-status: planned` and the owning issue in
// `x-localai-issue`. That marker is the allowlist of the route bijection: the test skips such an
// operation on the mux side and FAILS when one of them is registered, so the PR that implements a route
// has to delete the two markers. Nothing else in this file changes when an issue lands.

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	apispec "github.com/marinoscar/local-ai/agent/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
)

func loadSpec(t *testing.T) *apispec.Spec {
	t.Helper()
	s, err := apispec.Default()
	if err != nil {
		t.Fatalf("agent/api/openapi.yaml does not parse: %v", err)
	}
	return s
}

// readSpecDoc reads docs/specs/<name>. The tests need the repository's docs; a source tree without
// them (a bare module copy) skips instead of failing.
func readSpecDoc(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "docs", "specs")
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s not found: the docs are not part of this source tree", dir)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------------------------------
// The document itself

func TestOpenAPIDocument(t *testing.T) {
	s := loadSpec(t)
	if v, _ := s.Doc["openapi"].(string); !strings.HasPrefix(v, "3.1") {
		t.Fatalf("openapi must be 3.1.x, got %q", v)
	}
	info, _ := s.Doc["info"].(map[string]any)
	if title, _ := info["title"].(string); title == "" {
		t.Fatal("info.title is empty")
	}
	if s.BasePath != BasePath {
		t.Fatalf("servers[0].url is %q, the base path of the API is %q", s.BasePath, BasePath)
	}
	tags := map[string]bool{}
	list, _ := s.Doc["tags"].([]any)
	for _, tag := range list {
		tags[tag.(map[string]any)["name"].(string)] = true
	}
	ids := map[string]string{}
	for _, op := range s.Operations() {
		id, _ := op.Raw["operationId"].(string)
		if id == "" {
			t.Errorf("%s: no operationId", op.Key())
		} else if other, dup := ids[id]; dup {
			t.Errorf("%s: operationId %q is also used by %s", op.Key(), id, other)
		}
		ids[id] = op.Key()
		opTags, _ := op.Raw["tags"].([]any)
		if len(opTags) == 0 {
			t.Errorf("%s: no tags", op.Key())
		}
		for _, tag := range opTags {
			if !tags[tag.(string)] {
				t.Errorf("%s: tag %q is not declared in the top-level tags", op.Key(), tag)
			}
		}
		if sum, _ := op.Raw["summary"].(string); sum == "" {
			t.Errorf("%s: no summary", op.Key())
		}
	}
}

// walk calls fn for every object of the document. It does not descend into the keys listed in skip.
func walk(node any, path string, skip map[string]bool, fn func(path string, m map[string]any)) {
	switch n := node.(type) {
	case map[string]any:
		fn(path, n)
		for k, v := range n {
			if skip[k] {
				continue
			}
			walk(v, path+"/"+k, skip, fn)
		}
	case []any:
		for i, v := range n {
			walk(v, path+"/"+strconv.Itoa(i), skip, fn)
		}
	}
}

// schemaLocations returns every schema of the document by its location: the named ones of
// components.schemas and the inline ones of parameters, headers and media types.
func schemaLocations(s *apispec.Spec) map[string]any {
	out := map[string]any{}
	comps, _ := s.Doc["components"].(map[string]any)
	named, _ := comps["schemas"].(map[string]any)
	for name, sch := range named {
		out["#/components/schemas/"+name] = sch
	}
	walk(s.Doc, "#", map[string]bool{"schemas": true, "schema": true, "examples": true, "example": true}, func(path string, m map[string]any) {
		if sch, ok := m["schema"]; ok {
			out[path+"/schema"] = sch
		}
	})
	return out
}

func TestOpenAPISchemasUseSupportedKeywords(t *testing.T) {
	s := loadSpec(t)
	locs := schemaLocations(s)
	if len(locs) < 100 {
		t.Fatalf("found only %d schemas: the walker is broken", len(locs))
	}
	for loc, sch := range locs {
		if err := s.V.CheckSchema(sch); err != nil {
			t.Errorf("%s uses validation the in-repo validator does not do:\n%v", loc, err)
		}
	}
}

func TestOpenAPIRefsResolve(t *testing.T) {
	s := loadSpec(t)
	n := 0
	walk(s.Doc, "#", map[string]bool{"examples": true, "example": true}, func(path string, m map[string]any) {
		ref, ok := m["$ref"].(string)
		if !ok {
			return
		}
		n++
		if _, err := s.V.Resolve(ref); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	})
	if n < 200 {
		t.Fatalf("found only %d $ref: the walker is broken", n)
	}
}

// TestOpenAPIExamplesValidate validates every example against its schema (it catches typos in the document):
// the named examples of every media type, and the examples list of a named schema.
func TestOpenAPIExamplesValidate(t *testing.T) {
	s := loadSpec(t)
	n := 0
	walk(s.Doc, "#", map[string]bool{"schemas": true, "schema": true, "examples": true, "example": true}, func(path string, m map[string]any) {
		sch, hasSchema := m["schema"]
		examples, hasExamples := m["examples"].(map[string]any)
		if !hasSchema || !hasExamples {
			return
		}
		for name, ex := range examples {
			em, ok := ex.(map[string]any)
			if !ok {
				t.Errorf("%s/examples/%s: not an Example object", path, name)
				continue
			}
			value, ok := em["value"]
			if !ok {
				t.Errorf("%s/examples/%s: no value", path, name)
				continue
			}
			n++
			if err := s.V.Validate(sch, value); err != nil {
				t.Errorf("%s/examples/%s does not match its schema:\n%v", path, name, err)
			}
		}
	})
	comps, _ := s.Doc["components"].(map[string]any)
	named, _ := comps["schemas"].(map[string]any)
	for name, sch := range named {
		list, _ := sch.(map[string]any)["examples"].([]any)
		for i, ex := range list {
			n++
			if err := s.V.Validate(sch, ex); err != nil {
				t.Errorf("schema %s examples[%d] does not match it:\n%v", name, i, err)
			}
		}
	}
	if n < 40 {
		t.Fatalf("validated only %d examples: the walker is broken", n)
	}
}

// ---------------------------------------------------------------------------------------------------
// Operations: auth levels, error statuses, headers

var issueKey = regexp.MustCompile(`^[A-Z]+-[0-9]+$`)

func TestOpenAPIOperationsConform(t *testing.T) {
	s := loadSpec(t)
	ops := s.Operations()
	if len(ops) < 50 {
		t.Fatalf("only %d operations", len(ops))
	}
	for _, op := range ops {
		t.Run(op.Key(), func(t *testing.T) { checkOperation(t, s, op) })
	}
}

func checkOperation(t *testing.T, s *apispec.Spec, op apispec.Operation) {
	t.Helper()
	fail := func(format string, args ...any) { t.Errorf(format, args...) }

	// x-localai-auth: one valid level, or a list of distinct valid levels without none.
	auth := op.Auth()
	if len(auth) == 0 {
		fail("x-localai-auth is missing or malformed")
	}
	seen := map[string]bool{}
	for _, a := range auth {
		if !Level(a).Valid() {
			fail("x-localai-auth: %q is not none, op, portal, admin or admin*", a)
		}
		if seen[a] {
			fail("x-localai-auth: %q twice", a)
		}
		seen[a] = true
	}
	if seen["none"] && len(auth) > 1 {
		fail("x-localai-auth: none cannot be combined with another level")
	}
	if timeout, ok := op.Raw["x-localai-timeout"].(string); !ok || timeout == "" {
		fail("x-localai-timeout is missing")
	}
	if note, present := op.Raw["x-localai-auth-note"]; present {
		if str, ok := note.(string); !ok || str == "" {
			fail("x-localai-auth-note must be a non-empty string")
		}
	}
	if ge, present := op.Raw["x-localai-gate-exempt"]; present && ge != true {
		fail("x-localai-gate-exempt must be true when present")
	}

	// planned operations name their issue; implemented ones do not.
	switch st := op.Raw["x-localai-status"]; {
	case st == nil:
		if _, has := op.Raw["x-localai-issue"]; has {
			fail("x-localai-issue without x-localai-status: planned")
		}
	case st == "planned":
		if issue, _ := op.Raw["x-localai-issue"].(string); !issueKey.MatchString(issue) {
			fail("a planned operation needs x-localai-issue: <ISSUE-KEY>, got %q", issue)
		}
	default:
		fail("x-localai-status must be planned or absent, got %v", st)
	}

	// path parameters are declared and required.
	item, _ := s.Doc["paths"].(map[string]any)[op.Path].(map[string]any)
	declared := map[string]bool{}
	for _, list := range [][]any{asList(item["parameters"]), asList(op.Raw["parameters"])} {
		for _, p := range list {
			node, err := s.V.ResolveNode(p)
			if err != nil {
				fail("parameter: %v", err)
				continue
			}
			pm, _ := node.(map[string]any)
			if pm["in"] == "path" {
				declared[pm["name"].(string)] = true
				if pm["required"] != true {
					fail("path parameter %v must be required", pm["name"])
				}
			}
		}
	}
	for _, seg := range strings.Split(op.Path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && !declared[seg[1:len(seg)-1]] {
			fail("path parameter %s is not declared", seg)
		}
	}

	// request body media types.
	if rb, ok := op.Raw["requestBody"].(map[string]any); ok {
		if !op.Mutating() {
			fail("a GET has no request body")
		}
		content, _ := rb["content"].(map[string]any)
		if len(content) == 0 {
			fail("requestBody has no content")
		}
		for mt, entry := range content {
			if mt != "application/json" && mt != "multipart/form-data" {
				fail("requestBody media type %s is neither application/json nor multipart/form-data", mt)
			}
			if _, ok := entry.(map[string]any)["schema"]; !ok {
				fail("requestBody %s has no schema", mt)
			}
		}
	}

	// responses.
	responses, _ := op.Raw["responses"].(map[string]any)
	has2xx := false
	statuses := map[int]bool{}
	for code, node := range responses {
		status, err := strconv.Atoi(code)
		if err != nil || status < 100 || status > 599 {
			fail("response key %q is not an HTTP status", code)
			continue
		}
		statuses[status] = true
		resp, err := s.V.ResolveNode(node)
		if err != nil {
			fail("response %d: %v", status, err)
			continue
		}
		rm, _ := resp.(map[string]any)
		headers, _ := rm["headers"].(map[string]any)
		if _, ok := headers["X-Request-Id"]; !ok {
			fail("response %d does not declare X-Request-Id", status)
		}
		content, _ := rm["content"].(map[string]any)
		switch {
		case status >= 200 && status < 300:
			has2xx = true
			if op.Mutating() {
				if _, ok := headers["X-LocalAI-Op-Id"]; !ok {
					fail("response %d of a mutating operation does not declare X-LocalAI-Op-Id", status)
				}
			}
			for mt, entry := range content {
				if _, ok := entry.(map[string]any)["schema"]; !ok {
					fail("response %d %s has no schema", status, mt)
				}
			}
		case status >= 400:
			if len(content) != 1 {
				fail("error response %d must have exactly one media type, application/json", status)
			}
			entry, _ := content["application/json"].(map[string]any)
			sch, _ := entry["schema"].(map[string]any)
			if sch["$ref"] != "#/components/schemas/ErrorEnvelope" {
				fail("error response %d must use #/components/schemas/ErrorEnvelope, got %v", status, sch)
			}
		}
		if status == http.StatusTooManyRequests {
			if _, ok := headers["Retry-After"]; !ok {
				fail("response 429 does not declare Retry-After")
			}
		}
	}
	if !has2xx {
		fail("no 2xx response")
	}

	// the error statuses every operation of its kind can answer (agent.md section 3.7 status map).
	want := []int{http.StatusForbidden, http.StatusInternalServerError} // Host, Origin and peer checks; a bug
	if !seen["none"] {
		want = append(want, http.StatusUnauthorized)
	}
	if op.Mutating() {
		want = append(want, http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType)
		if op.Raw["x-localai-gate-exempt"] != true {
			want = append(want, http.StatusServiceUnavailable) // the mutating-route gate
		}
	}
	for _, st := range want {
		if !statuses[st] {
			fail("the %d response is not documented", st)
		}
	}
	if op.Raw["x-localai-gate-exempt"] == true && statuses[http.StatusServiceUnavailable] {
		fail("a gate-exempt route is never held back by the gate: no 503")
	}
	if (op.Raw["x-localai-gate-exempt"] == true) && op.Key() != "POST /mode/survival" {
		fail("only POST /mode/survival is gate-exempt (agent.md section 4.1 step 10)")
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// TestOpenAPIStarting503HasRetryAfter pins the shared 503 and 429 responses: Retry-After is declared.
func TestOpenAPISharedResponses(t *testing.T) {
	s := loadSpec(t)
	for _, name := range []string{"Starting", "RateLimited"} {
		node, err := s.V.Resolve("#/components/responses/" + name)
		if err != nil {
			t.Fatal(err)
		}
		headers, _ := node.(map[string]any)["headers"].(map[string]any)
		if _, ok := headers["Retry-After"]; !ok {
			t.Errorf("components.responses.%s does not declare Retry-After", name)
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// Doctor codes

var codeRe = regexp.MustCompile(`LAI-[A-Z]{2,3}-[0-9]{3}`)

func TestOpenAPIDoctorCodesExist(t *testing.T) {
	registry := readSpecDoc(t, "doctor-codes.md")
	known := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\| *(LAI-[A-Z]{2,3}-[0-9]{3}) *\|`).FindAllStringSubmatch(registry, -1) {
		known[m[1]] = true
	}
	if len(known) < 100 {
		t.Fatalf("parsed only %d codes of doctor-codes.md: the table format changed", len(known))
	}
	// Every code the document spells, in descriptions, examples and patterns alike, must be in the registry.
	used := map[string]bool{}
	for _, c := range codeRe.FindAllString(string(apispec.YAML()), -1) {
		used[c] = true
	}
	if len(used) < 30 {
		t.Fatalf("found only %d codes in openapi.yaml", len(used))
	}
	for c := range used {
		if !known[c] {
			t.Errorf("openapi.yaml names %s, which is not in docs/specs/doctor-codes.md", c)
		}
	}
	// The codes the package emits are documented in the document too, where a route can answer them.
	for _, c := range []string{CodeInvalidRequest, CodeNotFound, CodeBusy, CodeNotInMode, CodeStarting, CodeTooLarge, CodeInternal, CodeUnauthenticated, CodeAdminRequired, CodeRateLimited, CodeBadHost, CodeBadOrigin, CodePeerNotOperator, CodePortalSessionEnded, CodeRouterDown, CodeUpstreamTimeout} {
		if !used[c] {
			t.Errorf("the code %s is emitted by internal/api but never named in openapi.yaml", c)
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// The spec table of agent.md section 3.8

// tableRoute is one route of the table: method, path relative to /api/v1, auth levels.
type tableRoute struct {
	Method, Path string
	Auth         []string
}

func (r tableRoute) key() string { return r.Method + " " + r.Path }

var (
	methodPrefix = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) +(/\S*)$`)
	braceAlt     = regexp.MustCompile(`\{([a-z_]+(?:\|[a-z_]+)+)\}`)
	authLevels   = map[string]bool{"none": true, "op": true, "portal": true, "admin": true, "admin*": true}
)

// stripParens drops every balanced (...) group: notes inside parentheses are not part of the value.
func stripParens(s string) string {
	depth := 0
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func splitList(s string, seps ...string) []string {
	parts := []string{s}
	for _, sep := range seps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseAuthItem(s string) ([]string, error) {
	var levels []string
	for _, f := range strings.FieldsFunc(stripParens(s), func(r rune) bool { return r == ',' || r == ' ' || r == '`' }) {
		if f == "or" {
			continue
		}
		if !authLevels[f] {
			return nil, fmt.Errorf("unknown auth word %q in %q", f, s)
		}
		levels = append(levels, f)
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("no auth level in %q", s)
	}
	return levels, nil
}

// expandRow turns one row of the table into routes. The table is written for people: a method column
// like "POST/GET/DELETE" or "GET ; POST", a path column like "/models ; /models/{id}" or
// "/cloud ; POST /cloud/enroll ; DELETE /cloud", a brace group "{eject|init|mount}", notes in parentheses.
func expandRow(methodCol, pathCol, authCol string) ([]tableRoute, error) {
	methods := splitList(stripParens(methodCol), ";", "/")
	items := splitList(stripParens(pathCol), ";")
	auths := splitList(stripParens(authCol), ";")
	if len(methods) == 0 || len(items) == 0 || len(auths) == 0 {
		return nil, errors.New("empty column")
	}
	type pair struct {
		method, path string
		auth         int // index into auths, or -1 for "all"
	}
	var pairs []pair
	switch {
	case len(methods) == 1:
		for i, it := range items {
			method, path := methods[0], it
			if m := methodPrefix.FindStringSubmatch(it); m != nil {
				method, path = m[1], m[2]
			}
			a := -1
			if len(auths) == len(items) {
				a = i
			}
			pairs = append(pairs, pair{method, path, a})
		}
	case len(methods) == len(items):
		for i, it := range items {
			a := -1
			if len(auths) == len(items) {
				a = i
			}
			pairs = append(pairs, pair{methods[i], it, a})
		}
	case len(items) == 1:
		for i, m := range methods {
			a := -1
			if len(auths) == len(methods) {
				a = i
			}
			pairs = append(pairs, pair{m, items[0], a})
		}
	default:
		return nil, fmt.Errorf("cannot pair %d methods with %d paths", len(methods), len(items))
	}
	var out []tableRoute
	for _, p := range pairs {
		var authText string
		switch {
		case p.auth >= 0:
			authText = auths[p.auth]
		case len(auths) == 1:
			authText = auths[0]
		default:
			return nil, fmt.Errorf("cannot pair %d auth values with %d routes", len(auths), len(pairs))
		}
		levels, err := parseAuthItem(authText)
		if err != nil {
			return nil, err
		}
		paths := []string{p.path}
		if m := braceAlt.FindStringSubmatch(p.path); m != nil {
			paths = nil
			for _, alt := range strings.Split(m[1], "|") {
				paths = append(paths, strings.Replace(p.path, m[0], alt, 1))
			}
		}
		for _, path := range paths {
			if !strings.HasPrefix(path, "/") {
				return nil, fmt.Errorf("path %q does not start with /", path)
			}
			out = append(out, tableRoute{Method: p.method, Path: path, Auth: levels})
		}
	}
	return out, nil
}

// parseSpecTable reads the route table of agent.md section 3.8 ("### 3.8 Route reference").
func parseSpecTable(md string) ([]tableRoute, error) {
	_, after, ok := strings.Cut(md, "### 3.8 Route reference")
	if !ok {
		return nil, errors.New(`no "### 3.8 Route reference" heading`)
	}
	var routes []tableRoute
	seen := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(after, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		inTable = true
		cells := strings.Split(strings.ReplaceAll(line, `\|`, "\x00"), "|")
		if len(cells) < 5 {
			return nil, fmt.Errorf("short table row %q", line)
		}
		cells = cells[1 : len(cells)-1]
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "\x00", "|"))
		}
		if cells[0] == "Method" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		rs, err := expandRow(cells[0], cells[1], cells[2])
		if err != nil {
			return nil, fmt.Errorf("row %q: %w", line, err)
		}
		for _, r := range rs {
			if seen[r.key()] {
				return nil, fmt.Errorf("route %s is in the table twice", r.key())
			}
			seen[r.key()] = true
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		return nil, errors.New("the route table is empty")
	}
	return routes, nil
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

// diffTable compares the spec table with the document: the same routes, the same auth levels.
func diffTable(table []tableRoute, ops []apispec.Operation) []string {
	var problems []string
	inDoc := map[string]apispec.Operation{}
	for _, op := range ops {
		inDoc[op.Key()] = op
	}
	inTable := map[string]bool{}
	for _, r := range table {
		inTable[r.key()] = true
		op, ok := inDoc[r.key()]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is in the table of agent.md section 3.8 but not in openapi.yaml", r.key()))
			continue
		}
		if !sameSet(r.Auth, op.Auth()) {
			problems = append(problems, fmt.Sprintf("%s: the table says auth %v, openapi.yaml x-localai-auth says %v", r.key(), r.Auth, op.Auth()))
		}
	}
	for key := range inDoc {
		if !inTable[key] {
			problems = append(problems, fmt.Sprintf("%s is in openapi.yaml but not in the table of agent.md section 3.8", key))
		}
	}
	sort.Strings(problems)
	return problems
}

func TestSpecTableParses(t *testing.T) {
	const fixture = `
### 3.8 Route reference

| Method | Path | Auth | Timeout | Section |
|---|---|---|---|---|
| GET | /health | none | 2 s | 3.8.1 |
| POST | /session | op (bearer only) | 5 s | 3.8.2 |
| GET | /models ; /models/{id} | op | 5 s | 3.8.7 |
| POST | /models/{id}/load | op, or portal (restricted: ` + "`loadable`" + ` models only, [x](y.md) §3.3) | 90 s | 3.8.8 |
| POST/GET/DELETE | /ark/sync | op | 10 s | 3.8.18 |
| POST | /ark/drives/{root_id}/{eject\|init\|mount} ; PUT /ark/drives/{root_id}/profile ; POST /ark/prune | op ; admin ; admin | 60 s | 3.8.20a |
| GET ; POST | /catalog ; /catalog/refresh | op | 5 s / 120 s | 3.8.21 |
| POST | /speech/stt ; /speech/tts ; GET /speech/voices | op or portal | 120 s | 3.8.25 |
| GET | /cloud ; POST /cloud/enroll ; DELETE /cloud | op ; admin ; admin | 5 s | 3.8.26 |
| POST ; GET ; DELETE | /drill (` + "`GET ?run_id=`" + `) | op | 10 s | 3.8.28 |
| GET ; PUT | /profile | op ; admin | 5 s / 30 s | 3.8.30 |

Text after the table is ignored.
| POST | /not-in-the-table | op | 5 s | 9 |
`
	got, err := parseSpecTable(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	auth := map[string][]string{}
	for _, r := range got {
		keys = append(keys, r.key())
		auth[r.key()] = r.Auth
	}
	want := []string{
		"GET /health", "POST /session", "GET /models", "GET /models/{id}", "POST /models/{id}/load",
		"POST /ark/sync", "GET /ark/sync", "DELETE /ark/sync",
		"POST /ark/drives/{root_id}/eject", "POST /ark/drives/{root_id}/init", "POST /ark/drives/{root_id}/mount",
		"PUT /ark/drives/{root_id}/profile", "POST /ark/prune",
		"GET /catalog", "POST /catalog/refresh",
		"POST /speech/stt", "POST /speech/tts", "GET /speech/voices",
		"GET /cloud", "POST /cloud/enroll", "DELETE /cloud",
		"POST /drill", "GET /drill", "DELETE /drill",
		"GET /profile", "PUT /profile",
	}
	if !sameSet(keys, want) {
		t.Fatalf("routes:\n got %v\nwant %v", keys, want)
	}
	for key, levels := range map[string][]string{
		"GET /health":                       {"none"},
		"POST /session":                     {"op"},
		"POST /models/{id}/load":            {"op", "portal"},
		"POST /ark/drives/{root_id}/mount":  {"op"},
		"PUT /ark/drives/{root_id}/profile": {"admin"},
		"POST /ark/prune":                   {"admin"},
		"POST /speech/stt":                  {"op", "portal"},
		"GET /speech/voices":                {"op", "portal"},
		"GET /cloud":                        {"op"},
		"POST /cloud/enroll":                {"admin"},
		"DELETE /cloud":                     {"admin"},
		"GET /profile":                      {"op"},
		"PUT /profile":                      {"admin"},
		"DELETE /drill":                     {"op"},
	} {
		if !sameSet(auth[key], levels) {
			t.Errorf("%s: auth %v, want %v", key, auth[key], levels)
		}
	}

	for name, bad := range map[string]string{
		"no heading":     "| Method | Path |\n",
		"unknown auth":   "### 3.8 Route reference\n| Method | Path | Auth | T | S |\n|---|---|---|---|---|\n| GET | /x | root | 1 s | 1 |\n",
		"duplicate":      "### 3.8 Route reference\n| Method | Path | Auth | T | S |\n|---|---|---|---|---|\n| GET | /x | op | 1 s | 1 |\n| GET | /x | op | 1 s | 1 |\n",
		"unpairable":     "### 3.8 Route reference\n| Method | Path | Auth | T | S |\n|---|---|---|---|---|\n| GET ; POST | /a ; /b ; /c | op | 1 s | 1 |\n",
		"no leading /":   "### 3.8 Route reference\n| Method | Path | Auth | T | S |\n|---|---|---|---|---|\n| GET | x | op | 1 s | 1 |\n",
		"empty table":    "### 3.8 Route reference\n\nnothing\n",
		"auth mismatch ": "### 3.8 Route reference\n| Method | Path | Auth | T | S |\n|---|---|---|---|---|\n| GET | /a ; /b | op ; admin ; admin | 1 s | 1 |\n",
	} {
		if _, err := parseSpecTable(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestOpenAPIMatchesSpecTable is the first side of the bijection: the table of agent.md section 3.8, the
// paths and methods of openapi.yaml and their auth levels are the same set.
func TestOpenAPIMatchesSpecTable(t *testing.T) {
	s := loadSpec(t)
	table, err := parseSpecTable(readSpecDoc(t, "agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(table) < 50 {
		t.Fatalf("parsed only %d routes of the table", len(table))
	}
	for _, p := range diffTable(table, s.Operations()) {
		t.Error(p)
	}
}

func TestDiffTableNegative(t *testing.T) {
	s := loadSpec(t)
	ops := s.Operations()
	table, err := parseSpecTable(readSpecDoc(t, "agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	// A route only in the table.
	extra := append(append([]tableRoute(nil), table...), tableRoute{Method: "GET", Path: "/brand-new", Auth: []string{"op"}})
	if got := strings.Join(diffTable(extra, ops), "\n"); !strings.Contains(got, "GET /brand-new is in the table") {
		t.Errorf("a route only in the table must fail, got %q", got)
	}
	// A route only in the document.
	if got := strings.Join(diffTable(table[1:], ops), "\n"); !strings.Contains(got, "is in openapi.yaml but not in the table") {
		t.Errorf("a route only in the document must fail, got %q", got)
	}
	// An auth level that differs.
	changed := append([]tableRoute(nil), table...)
	changed[0].Auth = []string{"admin"}
	if got := strings.Join(diffTable(changed, ops), "\n"); !strings.Contains(got, "the table says auth") {
		t.Errorf("an auth mismatch must fail, got %q", got)
	}
}

// ---------------------------------------------------------------------------------------------------
// Registered routes

var timeoutRe = regexp.MustCompile(`^([0-9]+) (s|min)$`)

// documentedTimeout reads x-localai-timeout: "5 s", "15 min" and "none" are exact; a formula or a
// conditional ("20 min if wait, else 5 s") is informational and not compared.
func documentedTimeout(op apispec.Operation) (time.Duration, bool) {
	text, _ := op.Raw["x-localai-timeout"].(string)
	if text == "none" {
		return 0, true
	}
	m := timeoutRe.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "min" {
		return time.Duration(n) * time.Minute, true
	}
	return time.Duration(n) * time.Second, true
}

func requestMediaTypes(op apispec.Operation) []string {
	rb, ok := op.Raw["requestBody"].(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for mt := range rb["content"].(map[string]any) {
		out = append(out, mt)
	}
	return out
}

// compareRegistered is the second side of the bijection: the registered routes (api.Routes()) against
// the operations of the document.
//
//   - a registered route must be in the document, with the same auth levels, timeout (when the document
//     gives an exact one), gate exemption and request media types;
//   - a route in the document must be registered, unless it is marked x-localai-status: planned;
//   - a planned operation must NOT be registered: the PR that implements it removes the marker.
//
// Absolute routes (the SPA, /portal, /chat/, /library/, /maps/, /docs/) are not part of the document.
func compareRegistered(ops []apispec.Operation, routes []Route) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	inDoc := map[string]apispec.Operation{}
	for _, op := range ops {
		inDoc[op.Key()] = op
	}
	registered := map[string]bool{}
	for _, rt := range routes {
		if rt.Absolute {
			continue
		}
		key := rt.Method + " " + rt.Pattern
		registered[key] = true
		op, ok := inDoc[key]
		if !ok {
			add("%s is registered on the mux but not in openapi.yaml: describe it there (and in agent.md section 3.8)", key)
			continue
		}
		if op.Planned() {
			add("%s is registered but openapi.yaml still marks it x-localai-status: planned: remove x-localai-status and x-localai-issue", key)
		}
		var levels []string
		for _, l := range rt.Auth {
			levels = append(levels, string(l))
		}
		if !sameSet(levels, op.Auth()) {
			add("%s: the route requires auth %v, openapi.yaml x-localai-auth says %v", key, levels, op.Auth())
		}
		if want, exact := documentedTimeout(op); exact && want != rt.Timeout {
			add("%s: the route's timeout is %v, openapi.yaml x-localai-timeout says %v", key, rt.Timeout, want)
		}
		if exempt := op.Raw["x-localai-gate-exempt"] == true; exempt != rt.GateExempt {
			add("%s: GateExempt is %v on the route, x-localai-gate-exempt is %v in openapi.yaml", key, rt.GateExempt, exempt)
		}
		if docTypes := requestMediaTypes(op); docTypes != nil && rt.Mutating() {
			routeTypes := rt.MediaTypes
			if routeTypes == nil {
				routeTypes = []string{JSONMediaType}
			}
			if !sameSet(routeTypes, docTypes) {
				add("%s: the route accepts %v, the request body of openapi.yaml lists %v", key, routeTypes, docTypes)
			}
		}
	}
	for key, op := range inDoc {
		if !registered[key] && !op.Planned() {
			add("%s is in openapi.yaml but not registered on the mux: implement it, or mark it x-localai-status: planned with x-localai-issue", key)
		}
	}
	sort.Strings(problems)
	return problems
}

func TestOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	s := loadSpec(t)
	for _, p := range compareRegistered(s.Operations(), Routes()) {
		t.Error(p)
	}
}

// TestPlannedOperations lists the allowlist and checks it is what it claims to be: planned operations
// are not registered (also covered by the comparison above, with a message of its own), every
// implemented operation of the document is registered, and GET /health is among them.
func TestPlannedOperations(t *testing.T) {
	s := loadSpec(t)
	registered := map[string]bool{}
	for _, rt := range Routes() {
		if !rt.Absolute {
			registered[rt.Method+" "+rt.Pattern] = true
		}
	}
	byIssue := map[string][]string{}
	implemented := 0
	for _, op := range s.Operations() {
		if !op.Planned() {
			implemented++
			if !registered[op.Key()] {
				t.Errorf("%s is not planned and not registered", op.Key())
			}
			continue
		}
		if registered[op.Key()] {
			t.Errorf("%s is planned but registered: the allowlist must shrink (remove x-localai-status and x-localai-issue)", op.Key())
		}
		issue, _ := op.Raw["x-localai-issue"].(string)
		byIssue[issue] = append(byIssue[issue], op.Key())
	}
	if !registered["GET /health"] {
		t.Error("GET /health must be registered")
	}
	var issues []string
	for issue := range byIssue {
		issues = append(issues, issue)
	}
	sort.Strings(issues)
	for _, issue := range issues {
		t.Logf("planned, lands with %s: %s", issue, strings.Join(byIssue[issue], ", "))
	}
	t.Logf("%d operation(s) implemented, %d planned", implemented, len(s.Operations())-implemented)
}

// TestCompareRegisteredNegative gives the comparison the two negative fixtures of the issue (a route on the
// mux only; a route in the document only) and the other ways the two sides can disagree.
func TestCompareRegisteredNegative(t *testing.T) {
	const mini = `
openapi: 3.1.0
servers: [{url: /api/v1}]
paths:
  /a:
    get: {x-localai-auth: op, x-localai-timeout: "5 s"}
  /b:
    post: {x-localai-auth: none, x-localai-timeout: "5 s", x-localai-gate-exempt: true, requestBody: {content: {application/json: {schema: {}}}}}
  /c:
    put: {x-localai-auth: [op, portal], x-localai-timeout: "20 min if wait, else 5 s", x-localai-status: planned, x-localai-issue: AGENT-99}
  /up:
    post: {x-localai-auth: op, x-localai-timeout: "none", requestBody: {content: {multipart/form-data: {schema: {}}}}}
`
	spec, err := apispec.Parse([]byte(mini))
	if err != nil {
		t.Fatal(err)
	}
	ops := spec.Operations()
	good := []Route{
		{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}, Timeout: 5 * time.Second},
		{Method: "POST", Pattern: "/b", Auth: []Level{AuthNone}, Timeout: 5 * time.Second, GateExempt: true},
		{Method: "POST", Pattern: "/up", Auth: []Level{AuthOp}, MediaTypes: []string{"multipart/form-data"}},
	}
	if got := compareRegistered(ops, good); len(got) != 0 {
		t.Fatalf("a matching registry (with /c planned and absent, and an absolute route) must pass, got %v", got)
	}
	if got := compareRegistered(ops, append(append([]Route(nil), good...), Route{Method: "GET", Pattern: "/spa", Absolute: true})); len(got) != 0 {
		t.Fatalf("an absolute route is not part of the document, got %v", got)
	}

	tests := []struct {
		name   string
		routes []Route
		want   string
	}{
		{"route on the mux only", append(append([]Route(nil), good...), Route{Method: "GET", Pattern: "/mux-only", Auth: []Level{AuthOp}}), "GET /mux-only is registered on the mux but not in openapi.yaml"},
		{"same path, other method, on the mux only", append(append([]Route(nil), good...), Route{Method: "DELETE", Pattern: "/a", Auth: []Level{AuthOp}}), "DELETE /a is registered on the mux but not in openapi.yaml"},
		{"route in the document only", good[1:], "GET /a is in openapi.yaml but not registered on the mux"},
		{"planned route that is registered", append(append([]Route(nil), good...), Route{Method: "PUT", Pattern: "/c", Auth: []Level{AuthOp, AuthPortal}}), "PUT /c is registered but openapi.yaml still marks it x-localai-status: planned"},
		{"auth level differs", []Route{{Method: "GET", Pattern: "/a", Auth: []Level{AuthAdmin}, Timeout: 5 * time.Second}, good[1], good[2]}, "GET /a: the route requires auth [admin], openapi.yaml x-localai-auth says [op]"},
		{"auth list differs", []Route{good[0], {Method: "POST", Pattern: "/b", Auth: []Level{AuthNone, AuthPortal}, Timeout: 5 * time.Second, GateExempt: true}, good[2]}, "POST /b: the route requires auth"},
		{"timeout differs", []Route{{Method: "GET", Pattern: "/a", Auth: []Level{AuthOp}, Timeout: 7 * time.Second}, good[1], good[2]}, "the route's timeout is 7s"},
		{"gate exemption differs", []Route{good[0], {Method: "POST", Pattern: "/b", Auth: []Level{AuthNone}, Timeout: 5 * time.Second}, good[2]}, "GateExempt is false on the route, x-localai-gate-exempt is true"},
		{"media types differ", []Route{good[0], good[1], {Method: "POST", Pattern: "/up", Auth: []Level{AuthOp}}}, "the route accepts [application/json], the request body of openapi.yaml lists [multipart/form-data]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(compareRegistered(ops, tc.routes), "\n")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("want a problem containing %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRegisteredRoutesCarryAuthAndHandlers(t *testing.T) {
	// A guard for the comparison itself: Routes() lists the built-in route, with metadata only.
	var found bool
	for _, rt := range Routes() {
		if rt.Method == "GET" && rt.Pattern == "/health" {
			found = true
			if !sameSet([]string{string(rt.Auth[0])}, []string{"none"}) {
				t.Errorf("GET /health auth %v", rt.Auth)
			}
		}
	}
	if !found {
		t.Fatal("api.Routes() does not list GET /health")
	}
}

// ---------------------------------------------------------------------------------------------------
// Go wire types

// goJSONFields returns the JSON property names of a struct type and the ones that are always present.
func goJSONFields(typ reflect.Type) (all, always map[string]bool) {
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

func compareStruct(t *testing.T, s *apispec.Spec, path string, typ reflect.Type, node any) {
	t.Helper()
	resolved, err := s.V.ResolveNode(node)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	sch, _ := resolved.(map[string]any)
	props, _ := sch["properties"].(map[string]any)
	all, always := goJSONFields(typ)
	for name := range props {
		if !all[name] {
			t.Errorf("%s: the schema has property %q, the Go type %s has no such json field", path, name, typ)
		}
	}
	for name := range all {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: the Go type %s has json field %q, the schema has no such property", path, typ, name)
		}
	}
	for _, r := range asList(sch["required"]) {
		if name := r.(string); !always[name] {
			t.Errorf("%s: %q is required by the schema but the Go field is omitempty or missing", path, name)
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Type.Kind() == reflect.Struct {
			if sub, ok := props[name]; ok {
				compareStruct(t, s, path+"."+name, f.Type, sub)
			}
		}
	}
}

// TestTypesMirrorSchemas fails when a Go wire type of types.go and the schema of the same name differ:
// a field on one side only, or a required property the Go field may omit.
func TestTypesMirrorSchemas(t *testing.T) {
	s := loadSpec(t)
	for name, typ := range map[string]reflect.Type{
		"Health":        reflect.TypeOf(Health{}),
		"ErrorEnvelope": reflect.TypeOf(ErrorEnvelope{}),
	} {
		node, err := s.Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		compareStruct(t, s, name, typ, node)
	}
}

// ---------------------------------------------------------------------------------------------------
// Recorded responses of the handlers of this package

// doRecorded serves q on h and records the response against openapi.yaml (apitest.Record's job is to
// run at the end of the test): every handler test of a documented route goes through this or
// apitest.Record, so a body or status the document does not describe fails the test.
func doRecorded(t *testing.T, h http.Handler, q req) *httptest.ResponseRecorder {
	t.Helper()
	r := q.build()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	apitest.RecordResult(t, r, rec)
	return rec
}

// TestRecordedHealthAndErrorsConform records GET /health and one response per error of the status map of
// agent.md section 3.7, produced by the real server and middleware chain; the health body must match
// schema Health and every error must be an ErrorEnvelope.
func TestRecordedHealthAndErrorsConform(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.Handler()

	t.Run("health", func(t *testing.T) {
		rec := doRecorded(t, h, req{target: "/api/v1/health"})
		if rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("health while starting", func(t *testing.T) {
		s2, _ := newTestServer(t)
		s2.SetStarting(true)
		if rec := doRecorded(t, s2.Handler(), req{target: "/api/v1/health"}); rec.Code != 200 {
			t.Fatalf("status %d", rec.Code)
		}
	})

	errors := []struct {
		name string
		q    req
		code int
	}{
		{"unknown route", req{target: "/api/v1/nope"}, 404},
		{"path not in clean form", req{target: "/api/v1//ping", header: map[string]string{"X-Test-Principal": "bearer"}}, 404},
		{"wrong method", req{method: "DELETE", target: "/api/v1/ping", header: map[string]string{"X-Test-Principal": "bearer"}}, 405},
		{"options", req{method: "OPTIONS", target: "/api/v1/ping"}, 405},
		{"anonymous on an operator route", req{target: "/api/v1/ping"}, 401},
		{"operator on an admin route", req{method: "DELETE", target: "/api/v1/thing", header: jsonHeaders(nil)}, 403},
		{"bad host", req{target: "/api/v1/health", host: "evil.example:7070"}, 403},
		{"bad origin", req{target: "/api/v1/ping", header: map[string]string{"Origin": "https://evil.example", "X-Test-Principal": "bearer"}}, 403},
		{"wrong content type", req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(map[string]string{"Content-Type": "text/plain"}), body: `{}`}, 415},
		{"unknown json field", req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{"name":"a","bogus":1}`}, 400},
		{"malformed json", req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{`}, 400},
		{"body over the limit", req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{"name":"` + strings.Repeat("a", int(DefaultMaxBody)) + `"}`}, 413},
		{"conflict", req{method: "POST", target: "/api/v1/fail", header: jsonHeaders(nil), body: `{}`}, 409},
		{"handler bug", req{method: "POST", target: "/api/v1/boom", header: jsonHeaders(nil), body: `{}`}, 500},
		{"handler past its deadline", req{method: "POST", target: "/api/v1/slow", header: jsonHeaders(nil), body: `{}`}, 500},
	}
	for _, tc := range errors {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRecorded(t, h, tc.q)
			if rec.Code != tc.code && !(tc.code == 500 && rec.Code == 504) {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.code, rec.Body)
			}
		})
	}
	t.Run("mutating gate closed", func(t *testing.T) {
		s2, _ := newTestServer(t)
		s2.SetMutationsOpen(false)
		rec := doRecorded(t, s2.Handler(), req{method: "POST", target: "/api/v1/echo", header: jsonHeaders(nil), body: `{}`})
		if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" {
			t.Fatalf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
}
