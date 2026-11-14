package cloud

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

func TestDecodeParams(t *testing.T) {
	type p struct {
		Name  *string  `json:"name"`
		N     *int     `json:"n"`
		On    *bool    `json:"on"`
		List  []string `json:"list"`
		Plain string   `json:"-"`
	}
	tests := []struct {
		name, raw     string
		wantPath, why string // empty both: valid
	}{
		{"empty object", `{}`, "", ""},
		{"nothing at all", ``, "", ""},
		{"null counts as an empty object", `null`, "", ""},
		{"every field", `{"name":"a","n":3,"on":true,"list":["x"]}`, "", ""},
		{"unknown field", `{"other":1}`, "other", "unknown field"},
		{"a field hidden from json is unknown", `{"Plain":"x"}`, "Plain", "unknown field"},
		{"a key in another case", `{"Name":"a"}`, "Name", "unknown field"},
		{"a string where an integer goes", `{"n":"3"}`, "n", "must be an integer"},
		{"a float where an integer goes", `{"n":3.5}`, "n", "must be an integer"},
		{"an integer where a string goes", `{"name":3}`, "name", "must be a string"},
		{"a string where a bool goes", `{"on":"true"}`, "on", "must be true or false"},
		{"an object where an array goes", `{"list":{}}`, "list", "must be an array"},
		{"a number in a string array", `{"list":["a",1]}`, "list", "must be a string"},
		{"null for a field", `{"n":null}`, "n", "must not be null"},
		{"an array", `[]`, "", "must be an object"},
		{"a string", `"x"`, "", "must be an object"},
		{"not JSON", `{"n":`, "", "is not valid JSON"},
		{"data after the object", `{"n":1} {"n":2}`, "", "is not valid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := DecodeParams(json.RawMessage(tc.raw), &p{})
			if tc.why == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			var de *DecodeError
			if !errors.As(err, &de) || de.Path != tc.wantPath || de.Problem != tc.why {
				t.Errorf("error = %#v, want path %q problem %q", err, tc.wantPath, tc.why)
			}
		})
	}
	t.Run("values are filled in", func(t *testing.T) {
		var v p
		if err := DecodeParams(json.RawMessage(`{"name":"a","n":3,"on":false,"list":["x","y"]}`), &v); err != nil {
			t.Fatal(err)
		}
		if *v.Name != "a" || *v.N != 3 || *v.On || len(v.List) != 2 {
			t.Errorf("v = %+v", v)
		}
	})
	t.Run("the size is bounded", func(t *testing.T) {
		big := `{"name":"` + strings.Repeat("a", MaxParamsBytes) + `"}`
		var de *DecodeError
		if err := DecodeParams(json.RawMessage(big), &p{}); !errors.As(err, &de) || !strings.Contains(de.Problem, "64 KiB") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("the target must be a pointer to a struct", func(t *testing.T) {
		var de *DecodeError
		if err := DecodeParams(json.RawMessage(`{}`), p{}); !errors.As(err, &de) {
			t.Errorf("error = %v", err)
		}
		var m map[string]any
		if err := DecodeParams(json.RawMessage(`{}`), &m); !errors.As(err, &de) {
			t.Errorf("error = %v", err)
		}
	})
}

// TypeLimits is the lease and hard cap table of cloud-app.md section 3.7 for exactly the 17 types the
// config accepts.
func TestTypeLimitsMatchTheSpecTable(t *testing.T) {
	want := map[string][2]time.Duration{
		TypeStatusRefresh:    {30 * time.Second, 300 * time.Second},
		TypeModelLoad:        {60 * time.Second, 600 * time.Second},
		TypeModelUnload:      {30 * time.Second, 300 * time.Second},
		TypeModelSetDefault:  {30 * time.Second, 300 * time.Second},
		TypeModelDownload:    {30 * time.Second, 300 * time.Second},
		TypeModelDelete:      {60 * time.Second, 600 * time.Second},
		TypeArkSync:          {30 * time.Second, 300 * time.Second},
		TypeArkVerify:        {30 * time.Second, 300 * time.Second},
		TypeModeOffline:      {30 * time.Second, 300 * time.Second},
		TypeKeyRotate:        {60 * time.Second, 600 * time.Second},
		TypeServiceRestart:   {60 * time.Second, 600 * time.Second},
		TypeLogsTail:         {30 * time.Second, 300 * time.Second},
		TypeCatalogRefresh:   {60 * time.Second, 600 * time.Second},
		TypeAgentUpdate:      {300 * time.Second, 900 * time.Second},
		TypeEnsureRun:        {30 * time.Second, 300 * time.Second},
		TypeWorkloadActivate: {60 * time.Second, 900 * time.Second},
		TypeMusicGenerate:    {60 * time.Second, 3600 * time.Second},
	}
	names := config.CloudJobNames()
	if len(names) != 17 || len(want) != 17 {
		t.Fatalf("%d config names, %d expected", len(names), len(want))
	}
	for _, n := range names {
		lim, ok := TypeLimits(n)
		w, listed := want[n]
		if !ok || !listed || lim.Lease != w[0] || lim.HardCap != w[1] {
			t.Errorf("%s: limits %+v (known %v), want lease %v cap %v", n, lim, ok, w[0], w[1])
		}
	}
	if _, ok := TypeLimits("shell.exec"); ok {
		t.Error("an unknown type has limits")
	}
}

func TestFailureTextAndCodes(t *testing.T) {
	if got := failureText("LAI-AGT-021", "  gone  "); got != "LAI-AGT-021: gone" {
		t.Errorf("failureText = %q", got)
	}
	if got := failureText("LAI-AGT-021", strings.Repeat("x", 5000)); len([]rune(got)) != MaxFailureChars {
		t.Errorf("length = %d", len([]rune(got)))
	}
}

func TestInflightStore(t *testing.T) {
	dir := t.TempDir()
	s := InflightStore{Path: filepath.Join(dir, "cloud", "inflight.json")}
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	entries := []InflightEntry{
		{JobID: "a", Type: "localai.model.load", Plane: "localai", ClaimToken: "tok", LeaseExpiresAt: at, StartedAt: at.Add(-time.Second)},
		{JobID: "b", Type: "localai.logs.tail", Plane: "eab", LeaseExpiresAt: at, StartedAt: at},
	}
	if got, err := s.Load(); err != nil || got != nil {
		t.Fatalf("Load of a missing file = %v %v", got, err)
	}
	if err := s.Save(entries); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(s.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v %v, want 0600", fi.Mode().Perm(), err)
	}
	got, err := s.Load()
	if err != nil || len(got) != 2 || got[0] != entries[0] || got[1] != entries[1] {
		t.Fatalf("Load = %+v %v", got, err)
	}
	raw, _ := os.ReadFile(s.Path)
	if strings.Contains(string(raw), `"claim_token": ""`) || !strings.Contains(string(raw), `"claim_token": "tok"`) {
		t.Errorf("a missing token must be omitted, a present one stored: %s", raw)
	}
	// No entries removes the file, and removing a missing file is fine.
	if err := s.Save(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file survived Save(nil): %v", err)
	}
	if err := s.Save(nil); err != nil {
		t.Errorf("Save(nil) of a missing file = %v", err)
	}
	for name, body := range map[string]string{"not json": "{", "other version": `{"version":2,"jobs":[]}`, "no version": `{"jobs":[]}`} {
		if err := os.WriteFile(s.Path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); !errors.Is(err, ErrInflightCorrupt) {
			t.Errorf("%s: Load = %v, want ErrInflightCorrupt", name, err)
		}
	}
}
