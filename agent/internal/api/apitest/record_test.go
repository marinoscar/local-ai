package apitest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apispec "github.com/marinoscar/local-ai/agent/api"
)

func mustSpec(t *testing.T) *apispec.Spec {
	t.Helper()
	s, err := apispec.Default()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

const envelope = `{"error":{"code":"LAI-AGT-021","message":"Not found.","fix":"Check the address.","details":{}}}`

// TestCheck is the negative and positive matrix of the recorder: what it accepts and what it refuses.
func TestCheck(t *testing.T) {
	spec := mustSpec(t)
	jsonH := []string{"X-Request-Id", "req_abcdefghijkl", "Content-Type", "application/json; charset=utf-8"}
	tests := []struct {
		name    string
		method  string
		target  string
		status  int
		header  http.Header
		body    string
		wantErr string // substring of the joined problems; empty means conforming
	}{
		{"health conforms", "GET", "/api/v1/health", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"online","starting":false}`, ""},
		{"health with a query conforms", "GET", "/api/v1/health?x=1", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"online","starting":false}`, ""},
		{"HEAD is served by GET", "HEAD", "/api/v1/health", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"online","starting":false}`, ""},
		{"field not in the schema fails", "GET", "/api/v1/health", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"online","starting":false,"extra":1}`, `property "extra" is not allowed`},
		{"missing required field fails", "GET", "/api/v1/health", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"online"}`, `required property "starting" is missing`},
		{"wrong type fails", "GET", "/api/v1/health", 200, hdr(jsonH...), `{"ok":"yes","version":"0.1.0","mode":"online","starting":false}`, "$.ok: expected type boolean"},
		{"value outside an enum fails", "GET", "/api/v1/health", 200, hdr(jsonH...), `{"ok":true,"version":"0.1.0","mode":"sleepy","starting":false}`, "$.mode"},
		{"body that is not JSON fails", "GET", "/api/v1/health", 200, hdr(jsonH...), `ok`, "not valid JSON"},
		{"undocumented status fails", "GET", "/api/v1/health", 201, hdr(jsonH...), `{}`, "status 201 is not documented for GET /health"},
		{"missing request id fails", "GET", "/api/v1/health", 200, hdr("Content-Type", "application/json"), `{"ok":true,"version":"0.1.0","mode":"online","starting":false}`, "no X-Request-Id"},
		{"documented error conforms", "GET", "/api/v1/health", 500, hdr(jsonH...), envelope, ""},
		{"error that is not an envelope fails", "GET", "/api/v1/health", 500, hdr(jsonH...), `{"error":"boom"}`, "not an ErrorEnvelope"},
		{"error with an extra key fails", "GET", "/api/v1/health", 500, hdr(jsonH...), `{"error":{"code":"LAI-AGT-099","message":"m","fix":"f","details":{},"extra":1}}`, `property "extra" is not allowed`},
		{"error with a code that is not a doctor code fails", "GET", "/api/v1/health", 500, hdr(jsonH...), `{"error":{"code":"E_BOOM","message":"m","fix":"f","details":{}}}`, "does not match the pattern"},
		{"error without details fails", "GET", "/api/v1/health", 500, hdr(jsonH...), `{"error":{"code":"LAI-AGT-099","message":"m","fix":"f"}}`, `required property "details" is missing`},
		{"error as text fails", "GET", "/api/v1/health", 500, hdr("X-Request-Id", "req_abcdefghijkl", "Content-Type", "text/plain"), `boom`, "must be application/json"},
		{"unknown path 404 as envelope conforms", "GET", "/api/v1/nope", 404, hdr(jsonH...), envelope, ""},
		{"405 of an existing path conforms as envelope", "DELETE", "/api/v1/health", 405, hdr(jsonH...), envelope, ""},
		{"405 on a documented path needs no documentation", "DELETE", "/api/v1/models/default", 405, hdr(jsonH...), envelope, ""},
		{"405 that is not an envelope fails", "DELETE", "/api/v1/models/default", 405, hdr("X-Request-Id", "req_abcdefghijkl", "Content-Type", "text/plain"), `no`, "must be application/json"},
		{"success on an unknown path fails", "GET", "/api/v1/nope", 200, hdr(jsonH...), `{}`, "no operation of openapi.yaml serves GET /api/v1/nope"},
		{"path outside the base path fails", "GET", "/health", 200, hdr(jsonH...), `{}`, "no operation of openapi.yaml serves"},
		{"429 without Retry-After fails", "POST", "/api/v1/pin/verify", 429, hdr(jsonH...), envelope, "429 must carry Retry-After"},
		{"429 with Retry-After conforms", "POST", "/api/v1/pin/verify", 429, hdr(append(jsonH, "Retry-After", "30")...), envelope, ""},
		{"204 without a body conforms", "DELETE", "/api/v1/session", 204, hdr("X-Request-Id", "req_abcdefghijkl"), ``, ""},
		{"204 with a body fails", "DELETE", "/api/v1/session", 204, hdr(jsonH...), `{}`, "documents none"},
		{"path parameter resolves", "GET", "/api/v1/models/qwen3.5-9b-q4km", 404, hdr(jsonH...), envelope, ""},
		{"literal segment beats parameter", "PUT", "/api/v1/models/default", 200, hdr(jsonH...), `{"default":"a","previous":null,"ini_regenerated":true}`, ""},
		{"nested parameters resolve", "POST", "/api/v1/ark/drives/ark-3f9c0a12/eject", 202, hdr(jsonH...), `{"state":"ejecting"}`, ""},
		{"schema with oneOf accepts the first shape", "POST", "/api/v1/mode", 200, hdr(jsonH...), `{"op_id":"op_1","from":"online","mode":"offline","warnings":[]}`, ""},
		{"schema with oneOf accepts the second shape", "POST", "/api/v1/mode", 200, hdr(jsonH...), `{"mode":"offline","already":true}`, ""},
		{"schema with oneOf rejects a mix", "POST", "/api/v1/mode", 200, hdr(jsonH...), `{"mode":"offline","already":true,"warnings":[]}`, "must match exactly one"},
		{"date that is not RFC 3339 fails", "POST", "/api/v1/ui-code", 200, hdr(jsonH...), `{"code":"123456","expires_at":"tomorrow"}`, "RFC 3339"},
		{"content type that is not documented fails", "GET", "/api/v1/status", 200, hdr("X-Request-Id", "req_abcdefghijkl", "Content-Type", "text/html"), `<p>`, "Content-Type text/html is not documented"},
		{"audio is accepted without schema checks", "POST", "/api/v1/speech/tts", 200, hdr("X-Request-Id", "req_abcdefghijkl", "Content-Type", "audio/wav"), "RIFF....", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(Check(spec, tc.method, tc.target, tc.status, tc.header, []byte(tc.body)), "\n")
			switch {
			case tc.wantErr == "" && got != "":
				t.Fatalf("want a conforming response, got:\n%s", got)
			case tc.wantErr != "" && got == "":
				t.Fatalf("want a problem containing %q, got none", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(got, tc.wantErr):
				t.Fatalf("want a problem containing %q, got:\n%s", tc.wantErr, got)
			}
		})
	}
}

// TestRecordStreamChecksHeadersOnly proves that a stream's body is never read: the reader here would
// block forever.
func TestRecordStreamChecksHeadersOnly(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	h := http.Header{}
	h.Set("X-Request-Id", "req_abcdefghijkl")
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	resp := &http.Response{StatusCode: 200, Header: h, Body: pr, Request: httptest.NewRequest("GET", "/api/v1/events", nil)}
	if got := RecordStream(t, resp); got != resp {
		t.Fatal("RecordStream returns its argument")
	}
	if problems := Check(mustSpec(t), "GET", "/api/v1/events", 200, h, nil); len(problems) != 0 {
		t.Fatalf("a documented stream must conform, got %v", problems)
	}
	h2 := h.Clone()
	h2.Set("Content-Type", "application/json")
	if problems := Check(mustSpec(t), "GET", "/api/v1/events", 200, h2, nil); len(problems) == 0 {
		t.Fatal("a stream answered as JSON must fail")
	}
}

// TestRecordPassesAConformingResponse runs Record for real: a conforming response leaves t green and the
// body readable afterwards.
func TestRecordPassesAConformingResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req_abcdefghijkl")
	rec.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = rec.WriteString(`{"ok":true,"version":"0.1.0","mode":"online","starting":false}`)
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	resp := RecordResult(t, req, rec)
	b, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(b), `"starting":false`) {
		t.Fatalf("the body must stay readable after Record, got %q (%v)", b, err)
	}
}
