package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

type tailCall struct {
	unit  string
	lines int
	since time.Time
}

type fakeTailer struct {
	calls []tailCall
	tail  services.JournalTail
	err   error
}

func (f *fakeTailer) Tail(_ context.Context, unit string, lines int, since time.Time) (services.JournalTail, error) {
	f.calls = append(f.calls, tailCall{unit, lines, since})
	if f.err != nil {
		return services.JournalTail{}, f.err
	}
	t := f.tail
	t.Unit = unit
	return t, nil
}

func logsHandler(t *testing.T, j LogTailer) http.Handler {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterLogs(reg, LogsDeps{Journal: j}); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t, func(o *Options) { o.Registry = reg })
	return s.Handler()
}

func getLogs(h http.Handler, query string) *httptest.ResponseRecorder {
	return do(h, req{target: "/api/v1/logs" + query, header: map[string]string{"X-Test-Principal": "bearer"}})
}

func TestLogsResponseShape(t *testing.T) {
	f := &fakeTailer{tail: services.JournalTail{Lines: []services.JournalLine{
		{At: time.Date(2026, 11, 2, 8, 0, 12, 500_000_000, time.UTC), Priority: 6, Message: "llama_server: model loaded"},
		{At: time.Date(2026, 11, 2, 8, 0, 13, 0, time.FixedZone("x", 3600)), Priority: 4, Message: "slot 0: context shift"},
	}}}
	rec := getLogs(logsHandler(t, f), "?unit=llama&lines=5")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status %d, Content-Type %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	want := `{"unit":"localai-llama.service","lines":[` +
		`{"at":"2026-11-02T08:00:12Z","priority":6,"message":"llama_server: model loaded"},` +
		`{"at":"2026-11-02T07:00:13Z","priority":4,"message":"slot 0: context shift"}],"truncated":false}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body:\n got %s\nwant %s", got, want)
	}
	if len(f.calls) != 1 || f.calls[0].unit != services.UnitLlama || f.calls[0].lines != 5 || !f.calls[0].since.IsZero() {
		t.Errorf("tail calls = %+v", f.calls)
	}
}

func TestLogsEmptyAndTruncated(t *testing.T) {
	f := &fakeTailer{tail: services.JournalTail{Lines: nil, Truncated: true}}
	rec := getLogs(logsHandler(t, f), "?unit=kiwix")
	if got, want := rec.Body.String(), `{"unit":"localai-kiwix.service","lines":[],"truncated":true}`+"\n"; got != want {
		t.Errorf("body:\n got %s\nwant %s", got, want)
	}
}

func TestLogsUnitMappingAndDefaults(t *testing.T) {
	want := map[string]string{
		"agent": "localai-agent.service", "llama": "localai-llama.service", "kiwix": "localai-kiwix.service",
		"whisper": "localai-whisper.service", "sync": "localai-sync.service", "drill": "localai-drill.service",
	}
	for short, unit := range want {
		f := &fakeTailer{}
		rec := getLogs(logsHandler(t, f), "?unit="+short)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", short, rec.Code, rec.Body)
		}
		if len(f.calls) != 1 || f.calls[0].unit != unit || f.calls[0].lines != 200 {
			t.Errorf("%s: calls = %+v, want %s with the default 200 lines", short, f.calls, unit)
		}
	}
}

func TestLogsLinesBoundaries(t *testing.T) {
	h := logsHandler(t, &fakeTailer{})
	for _, ok := range []string{"1", "2", "200", "1999", "2000"} {
		if rec := getLogs(h, "?unit=llama&lines="+ok); rec.Code != 200 {
			t.Errorf("lines=%s: status %d: %s", ok, rec.Code, rec.Body)
		}
	}
	tests := []struct {
		lines  string
		reason string
	}{
		{"0", "out_of_range"}, {"2001", "out_of_range"}, {"-1", "out_of_range"}, {"100000", "out_of_range"},
		{"", "not_an_integer"}, {"abc", "not_an_integer"}, {"1.5", "not_an_integer"}, {"1e3", "not_an_integer"},
		{"99999999999999999999", "not_an_integer"},
	}
	for _, tc := range tests {
		e := expectError(t, getLogs(h, "?unit=llama&lines="+tc.lines), 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "lines" || e.Error.Details["reason"] != tc.reason {
			t.Errorf("lines=%q: details = %v", tc.lines, e.Error.Details)
		}
	}
}

func TestLogsUnitValidation(t *testing.T) {
	f := &fakeTailer{}
	h := logsHandler(t, f)
	e := expectError(t, getLogs(h, ""), 400, CodeInvalidRequest)
	if e.Error.Details["field"] != "unit" || e.Error.Details["reason"] != "missing" {
		t.Errorf("details = %v", e.Error.Details)
	}
	for _, unit := range []string{"sshd", "localai-llama.service", "LLAMA", "llama%3Bls", "..%2F..%2Fetc", "tailscaled", "-f", "agent%20-f", "piper", "maps"} {
		e := expectError(t, getLogs(h, "?unit="+unit), 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "unit" || e.Error.Details["reason"] != "unknown_unit" {
			t.Errorf("unit=%q: details = %v", unit, e.Error.Details)
		}
		if allowed, _ := e.Error.Details["allowed"].([]any); len(allowed) != 6 {
			t.Errorf("unit=%q: allowed = %v", unit, e.Error.Details["allowed"])
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("the journal was read for refused requests: %+v", f.calls)
	}
}

func TestLogsSince(t *testing.T) {
	f := &fakeTailer{}
	h := logsHandler(t, f)
	for _, ok := range []string{"2026-11-02T08:00:00Z", "2026-11-02T08:00:00.123Z", "2026-11-02T02:00:00-06:00"} {
		f.calls = nil
		rec := getLogs(h, "?unit=llama&since="+strings.ReplaceAll(ok, "+", "%2B"))
		if rec.Code != 200 {
			t.Fatalf("since=%s: status %d: %s", ok, rec.Code, rec.Body)
		}
		if len(f.calls) != 1 || f.calls[0].since.IsZero() {
			t.Errorf("since=%s: calls = %+v", ok, f.calls)
		}
	}
	// The same instant, written in two zones, reaches the journal as the same time.
	f.calls = nil
	getLogs(h, "?unit=llama&since=2026-11-02T08:00:00Z")
	getLogs(h, "?unit=llama&since=2026-11-02T02:00:00-06:00")
	if len(f.calls) != 2 || !f.calls[0].since.Equal(f.calls[1].since) {
		t.Errorf("calls = %+v", f.calls)
	}
	for _, bad := range []string{"", "yesterday", "2026-11-02", "2026-11-02 08:00:00", "1793606405", "2026-11-02T08:00:00", "2026-13-02T08:00:00Z", "2026-11-02T08:00:00Z%3Brm"} {
		e := expectError(t, getLogs(h, "?unit=llama&since="+strings.ReplaceAll(bad, " ", "%20")), 400, CodeInvalidRequest)
		if e.Error.Details["field"] != "since" || e.Error.Details["reason"] != "not_rfc3339" {
			t.Errorf("since=%q: details = %v", bad, e.Error.Details)
		}
	}
}

func TestLogsAuth(t *testing.T) {
	h := logsHandler(t, &fakeTailer{})
	expectError(t, do(h, req{target: "/api/v1/logs?unit=llama"}), 401, CodeUnauthenticated)
	expectError(t, do(h, req{target: "/api/v1/logs?unit=llama", header: map[string]string{"X-Test-Principal": "portal"}}), 401, CodePortalSessionEnded)
	if rec := do(h, req{target: "/api/v1/logs?unit=llama", header: map[string]string{"X-Test-Principal": "socket"}}); rec.Code != 200 {
		t.Errorf("a socket operator was refused: %d", rec.Code)
	}
}

func TestLogsJournalFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
		reason string
	}{
		{"timeout", services.ErrTimeout, 504, CodeInternal, "journalctl_timeout"},
		{"journalctl exits non-zero", &services.JournalError{Unit: services.UnitLlama, ExitCode: 1}, 500, CodeInternal, "journalctl_failed"},
		{"lines out of range from the reader", services.ErrTailRange, 400, CodeInvalidRequest, "out_of_range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := expectError(t, getLogs(logsHandler(t, &fakeTailer{err: tc.err}), "?unit=llama"), tc.status, tc.code)
			if e.Error.Details["reason"] != tc.reason {
				t.Errorf("details = %v", e.Error.Details)
			}
			if tc.status == 500 && e.Error.Details["request_id"] == nil {
				t.Errorf("a 500 carries the request id: %v", e.Error.Details)
			}
		})
	}
	// Anything else is a bug: 500 with the internal code.
	expectError(t, getLogs(logsHandler(t, &fakeTailer{err: errors.New("boom")}), "?unit=llama"), 500, CodeInternal)
}

// End to end with the real journal reader over a fake journalctl: the credentials in the fixture never
// reach the response, and the response is still one valid JSON document with every line in it.
func TestLogsScrubsCredentialsEndToEnd(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "journal-llama.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	token := "lat_" + strings.Repeat("Qx7", 10)
	out := []byte(strings.ReplaceAll(string(raw), "@@TOKEN@@", token))
	argv := []string{"-u", services.UnitLlama, "-n", "200", "-o", "json", "--no-pager"}
	runner := execfake.New().Expect(execfake.Key("/usr/bin/journalctl", argv...), execx.Result{Stdout: out}, nil)
	h := logsHandler(t, services.NewJournal(runner, "/usr/bin/journalctl"))

	rec := getLogs(h, "?unit=llama")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), "Qx7Qx7Qx7") {
		t.Fatalf("the token reached the response: %s", rec.Body)
	}
	var body struct {
		Unit      string `json:"unit"`
		Truncated bool   `json:"truncated"`
		Lines     []struct {
			At       string `json:"at"`
			Priority int    `json:"priority"`
			Message  string `json:"message"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, rec.Body)
	}
	if body.Unit != services.UnitLlama || len(body.Lines) != 8 || body.Truncated {
		t.Fatalf("body = %+v", body)
	}
	if body.Lines[1].At != "2026-11-02T08:00:12Z" || body.Lines[1].Message != "llama_server: model loaded" || body.Lines[1].Priority != 6 {
		t.Errorf("line 1 = %+v", body.Lines[1])
	}
	if !strings.Contains(body.Lines[5].Message, "lat_…") {
		t.Errorf("line 5 = %q", body.Lines[5].Message)
	}
}

func TestLogsTruncatedByTheCap(t *testing.T) {
	line := `{"__REALTIME_TIMESTAMP":"1793606405000000","PRIORITY":"6","MESSAGE":"one"}` + "\n"
	out := []byte(line + line + `{"__REALTIME_TIMESTAMP":"17936064`)
	argv := []string{"-u", services.UnitWhisper, "-n", "2000", "-o", "json", "--no-pager"}
	runner := execfake.New().Expect(execfake.Key("/usr/bin/journalctl", argv...), execx.Result{Stdout: out, Truncated: true}, nil)
	rec := getLogs(logsHandler(t, services.NewJournal(runner, "/usr/bin/journalctl")), "?unit=whisper&lines=2000")
	var body struct {
		Lines     []json.RawMessage `json:"lines"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("status %d: %v: %s", rec.Code, err, rec.Body)
	}
	if !body.Truncated || len(body.Lines) != 2 {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestLogsRegistration(t *testing.T) {
	if err := RegisterLogs(NewRegistry(), LogsDeps{}); err == nil {
		t.Error("registered without a journal reader")
	}
	reg := NewRegistry()
	if err := RegisterLogs(reg, LogsDeps{Journal: &fakeTailer{}}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterLogs(reg, LogsDeps{Journal: &fakeTailer{}}); err == nil {
		t.Error("registered twice")
	}
	rs := reg.Routes()
	if len(rs) != 1 || rs[0].Method != "GET" || rs[0].Pattern != "/logs" || rs[0].Timeout != 10*time.Second || len(rs[0].Auth) != 1 || rs[0].Auth[0] != AuthOp {
		t.Errorf("routes = %+v", rs)
	}
}
