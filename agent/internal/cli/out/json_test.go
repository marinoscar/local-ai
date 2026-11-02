package out

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestJSONDocument(t *testing.T) {
	var buf bytes.Buffer
	j := NewJSON(&buf)
	if j.Wrote() {
		t.Fatal("Wrote before any write")
	}
	if err := j.Document(map[string]any{"b": 1, "a": "<x>&"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "{\"a\":\"<x>&\",\"b\":1}\n" {
		t.Errorf("document = %q (compact, sorted keys, not HTML-escaped, one newline)", got)
	}
	if !j.Wrote() {
		t.Error("Wrote after a document")
	}
	before := buf.Len()
	if err := j.Document(1); !errors.Is(err, ErrSecondDocument) {
		t.Errorf("second document err = %v", err)
	}
	if err := j.Line(1); !errors.Is(err, ErrSecondDocument) {
		t.Errorf("line after document err = %v", err)
	}
	if err := j.Raw([]byte(`{"x":1}`)); !errors.Is(err, ErrSecondDocument) {
		t.Errorf("raw after document err = %v", err)
	}
	if err := j.Error(Problem{Code: "LAI-AGT-099"}); !errors.Is(err, ErrSecondDocument) {
		t.Errorf("error after document err = %v", err)
	}
	if buf.Len() != before {
		t.Errorf("a refused write changed stdout: %q", buf.String())
	}
}

func TestJSONRaw(t *testing.T) {
	var buf bytes.Buffer
	j := NewJSON(&buf)
	body := "  {\n  \"models\": [1, 2]\n}\n\n"
	if err := j.Raw([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "{\n  \"models\": [1, 2]\n}\n"; got != want {
		t.Errorf("raw = %q, want the agent's bytes %q", got, want)
	}
	var buf2 bytes.Buffer
	j2 := NewJSON(&buf2)
	if err := j2.Raw([]byte("not json")); !errors.Is(err, ErrNotJSON) {
		t.Errorf("err = %v", err)
	}
	if buf2.Len() != 0 || j2.Wrote() {
		t.Error("an invalid body must write nothing")
	}
}

func TestJSONLines(t *testing.T) {
	var buf bytes.Buffer
	j := NewJSON(&buf)
	for i := 0; i < 3; i++ {
		if err := j.Line(map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Document(1); !errors.Is(err, ErrSecondDocument) {
		t.Errorf("document after lines err = %v", err)
	}
	// A failure in a stream is one more line.
	if err := j.Error(Problem{Code: "LAI-CLI-001", Message: "gone."}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Errorf("not JSON: %q", l)
		}
	}
	if !strings.HasPrefix(lines[3], `{"error":{"code":"LAI-CLI-001"`) {
		t.Errorf("last line = %q", lines[3])
	}
}

func TestEnvelopeShape(t *testing.T) {
	b, err := Envelope(Problem{Code: "LAI-CLI-008", Message: "No terminal.", Fix: "Use `sudo`."})
	if err != nil {
		t.Fatal(err)
	}
	// Field order is the agent's, details is always an object.
	want := `{"error":{"code":"LAI-CLI-008","message":"No terminal.","fix":"Use ` + "`sudo`" + `.","details":{}}}` + "\n"
	if string(b) != want {
		t.Errorf("envelope = %s want %s", b, want)
	}
	b, _ = Envelope(Problem{Code: "X", Details: map[string]any{"remaining_attempts": 2}})
	if !strings.Contains(string(b), `"details":{"remaining_attempts":2}`) {
		t.Errorf("details missing: %s", b)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestJSONWriteError(t *testing.T) {
	j := NewJSON(failWriter{})
	if err := j.Document(1); err == nil {
		t.Error("a write error must be returned")
	}
}
