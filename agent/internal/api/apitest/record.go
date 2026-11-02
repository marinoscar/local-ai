// Package apitest records the responses of handler tests and validates them against
// agent/api/openapi.yaml (agent.md section 4.14), so that a handler cannot return a field or a status
// the document does not describe.
//
// Every handler test passes the response it gets through Record (or RecordResult for a
// httptest.ResponseRecorder). When the test ends, the recorded status, headers and body are checked:
//
//   - the operation that serves the request must document the status;
//   - a JSON body must match the response schema for that status (so additionalProperties: false
//     catches an undocumented field);
//   - a status of 400 or more must match #/components/schemas/ErrorEnvelope, also for a request that
//     no operation serves (an unknown path, a wrong method);
//   - the response must carry X-Request-Id, and a 429 must carry Retry-After.
//
// The package does not import internal/api, so the tests of that package can use it.
package apitest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
)

// Record reads resp.Body (and puts an identical body back, so the test can still read it) and
// validates the response against the document when t finishes. resp.Request must be set, which the
// http.Client does. It returns resp for chaining.
func Record(t testing.TB, resp *http.Response) *http.Response {
	t.Helper()
	if resp.Request == nil {
		t.Fatal("apitest.Record: resp.Request is nil; use RecordResult for a httptest.ResponseRecorder")
	}
	var body []byte
	if resp.Body != nil {
		var err error
		body, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("apitest.Record: reading the body: %v", err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	method, target := resp.Request.Method, resp.Request.URL.RequestURI()
	status, header := resp.StatusCode, resp.Header.Clone()
	t.Cleanup(func() { validate(t, method, target, status, header, body) })
	return resp
}

// RecordStream is Record for a success response whose body never ends (server-sent events): the
// status and the headers are checked when t finishes and the body is not touched. An error response
// of a stream is an ordinary envelope and is recorded like any other.
func RecordStream(t testing.TB, resp *http.Response) *http.Response {
	t.Helper()
	if resp.StatusCode >= 400 {
		return Record(t, resp)
	}
	if resp.Request == nil {
		t.Fatal("apitest.RecordStream: resp.Request is nil")
	}
	method, target := resp.Request.Method, resp.Request.URL.RequestURI()
	status, header := resp.StatusCode, resp.Header.Clone()
	t.Cleanup(func() { validate(t, method, target, status, header, nil) })
	return resp
}

// RecordResult records the result of a httptest.ResponseRecorder served for req and returns the
// recorder's http.Response.
func RecordResult(t testing.TB, req *http.Request, rec *httptest.ResponseRecorder) *http.Response {
	t.Helper()
	resp := rec.Result()
	resp.Request = req
	return Record(t, resp)
}

func validate(t testing.TB, method, target string, status int, header http.Header, body []byte) {
	t.Helper()
	spec, err := apispec.Default()
	if err != nil {
		t.Errorf("apitest: %v", err)
		return
	}
	for _, p := range Check(spec, method, target, status, header, body) {
		t.Errorf("apitest: %s %s -> %d: %s", method, target, status, p)
	}
}

// Check returns what is wrong with one response, judged by spec; nil when it conforms. target is the
// request target including the base path and any query ("/api/v1/models?loadable=1"). It is what
// Record runs at the end of a test, and what the negative tests of this package call directly.
func Check(spec *apispec.Spec, method, target string, status int, header http.Header, body []byte) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	u, err := url.Parse(target)
	if err != nil {
		return []string{fmt.Sprintf("bad request target %q", target)}
	}
	path := u.Path
	op, found := apispec.Operation{}, false
	if rest, ok := strings.CutPrefix(path, spec.BasePath); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		op, found = spec.Match(method, rest)
	}

	if header.Get("X-Request-Id") == "" {
		add("the response has no X-Request-Id header")
	}
	if status == http.StatusTooManyRequests && header.Get("Retry-After") == "" {
		add("a 429 must carry Retry-After")
	}

	var doc map[string]any
	if found {
		var ok bool
		doc, ok = spec.Response(op, status)
		// 405 is answered by routing for any method a path does not serve (and for every OPTIONS), so
		// it belongs to no operation; it only has to be an envelope.
		if !ok && status != http.StatusMethodNotAllowed {
			add("status %d is not documented for %s", status, op.Key())
		}
	} else if status < 400 {
		add("no operation of openapi.yaml serves %s %s (a success status needs one)", method, path)
	}

	mediaType := ""
	if ct := header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil {
			add("bad Content-Type %q", ct)
		}
		mediaType = mt
	}

	if status >= 400 {
		checkEnvelope(spec, mediaType, body, add)
		return problems
	}
	if doc == nil {
		return problems
	}
	content, _ := doc["content"].(map[string]any)
	switch {
	case len(content) == 0:
		if len(bytes.TrimSpace(body)) != 0 {
			add("the response has a body but openapi.yaml documents none for status %d", status)
		}
	case mediaType == "":
		if len(body) > 0 {
			add("the response has a body and no Content-Type")
		}
	default:
		entry, ok := content[mediaType].(map[string]any)
		if !ok {
			add("Content-Type %s is not documented for status %d (documented: %s)", mediaType, status, keys(content))
			break
		}
		if mediaType != "application/json" {
			break
		}
		sch, ok := entry["schema"]
		if !ok {
			break
		}
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			add("the body is not valid JSON: %v", err)
			break
		}
		if err := spec.V.Validate(sch, value); err != nil {
			add("the body does not match the response schema:\n%v", err)
		}
	}
	return problems
}

// checkEnvelope validates an error response against #/components/schemas/ErrorEnvelope.
func checkEnvelope(spec *apispec.Spec, mediaType string, body []byte, add func(string, ...any)) {
	if mediaType != "application/json" {
		add("an error response must be application/json, got %q", mediaType)
		return
	}
	sch, err := spec.Schema("ErrorEnvelope")
	if err != nil {
		add("openapi.yaml has no ErrorEnvelope: %v", err)
		return
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		add("the error body is not valid JSON: %v", err)
		return
	}
	if err := spec.V.Validate(sch, value); err != nil {
		add("the error body is not an ErrorEnvelope:\n%v", err)
	}
}

func keys(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
