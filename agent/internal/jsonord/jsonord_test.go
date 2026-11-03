package jsonord_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/jsonord"
)

func TestObjectOrderAndEscaping(t *testing.T) {
	extra := map[string]json.RawMessage{"zeta": json.RawMessage(`1`), "alpha": json.RawMessage(`{"k":[1,2]}`)}
	got, err := jsonord.Object([]jsonord.KV{{Key: "b", Val: "x&y<z>"}, {Key: "a", Val: 2}}, extra)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"b":"x&y<z>","a":2,"alpha":{"k":[1,2]},"zeta":1}`
	if string(got) != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestObjectMarshalError(t *testing.T) {
	_, err := jsonord.Object([]jsonord.KV{{Key: "bad", Val: make(chan int)}}, nil)
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("want an error naming the key, got %v", err)
	}
}

func TestIndent(t *testing.T) {
	got, err := jsonord.Indent([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\n  \"a\": 1\n}\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := jsonord.Indent([]byte(`{`)); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestDecode(t *testing.T) {
	var name string
	var n int
	extra, err := jsonord.Decode([]byte(`{"name":"a","n":3,"future":{"x":1}}`), map[string]any{"name": &name, "n": &n, "absent": new(string)})
	if err != nil {
		t.Fatal(err)
	}
	if name != "a" || n != 3 {
		t.Fatalf("name=%q n=%d", name, n)
	}
	if len(extra) != 1 || string(extra["future"]) != `{"x":1}` {
		t.Fatalf("extra = %v", extra)
	}

	extra, err = jsonord.Decode([]byte(`{"name":"a"}`), map[string]any{"name": &name})
	if err != nil || extra != nil {
		t.Fatalf("no unknown keys must give nil extra, got %v %v", extra, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	var n int
	tests := []struct{ name, in, want string }{
		{"wrong type names the key", `{"n":"x"}`, "n:"},
		{"array", `[1]`, ""},
		{"truncated", `{"n":`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jsonord.Decode([]byte(tc.in), map[string]any{"n": &n})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestStamp(t *testing.T) {
	zone := time.FixedZone("x", -6*3600)
	at := time.Date(2026, 11, 2, 21, 41, 10, 987, zone)
	raw, err := json.Marshal(jsonord.Stamp(at))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"2026-11-03T03:41:10Z"` {
		t.Fatalf("got %s", raw)
	}
	var back time.Time
	if err := json.Unmarshal([]byte(`"2026-11-02T21:41:10-06:00"`), (*jsonord.Stamp)(&back)); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(at.Truncate(time.Second)) || back.Location() != time.UTC {
		t.Fatalf("got %v", back)
	}
	keep := back
	if err := json.Unmarshal([]byte(`null`), (*jsonord.Stamp)(&back)); err != nil || !back.Equal(keep) {
		t.Fatalf("null must be a no-op: %v %v", back, err)
	}
	for _, bad := range []string{`"yesterday"`, `5`} {
		if err := json.Unmarshal([]byte(bad), (*jsonord.Stamp)(&back)); err == nil {
			t.Fatalf("%s must fail", bad)
		}
	}
}
