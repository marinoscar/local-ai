package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture reads a file of agent/testdata/router.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "router", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func byID(t *testing.T, ms []ModelStatus, id string) ModelStatus {
	t.Helper()
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %q not in the list", id)
	return ModelStatus{}
}

func TestParseModelsAllStatuses(t *testing.T) {
	ms, err := ParseModels(fixture(t, "models_all_states.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 8 {
		t.Fatalf("got %d models, want 8", len(ms))
	}
	cases := []struct {
		id      string
		status  Status
		running bool
		failed  bool
		exit    int
	}{
		{"model-unloaded-q4km", StatusUnloaded, false, false, 0},
		{"model-loading-q4km", StatusLoading, true, false, 0},
		{"model-loaded-q4km", StatusLoaded, true, false, 0},
		{"model-failed-q4km", StatusUnloaded, false, true, 1},
		{"model-sleeping-q4km", StatusSleeping, true, false, 0},
		{"org-example/model-downloading-GGUF:Q4_K_M", StatusDownloading, false, false, 0},
		{"org-example/model-incomplete-GGUF:Q4_K_M", StatusIncomplete, false, false, 0},
		{"model-downloaded-q4km", StatusDownloaded, false, false, 0},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			m := byID(t, ms, c.id)
			if m.Status != c.status || !m.Status.Known() {
				t.Errorf("status = %q (known %v), want %q", m.Status, m.Status.Known(), c.status)
			}
			if m.Status.Running() != c.running {
				t.Errorf("Running() = %v, want %v", m.Status.Running(), c.running)
			}
			if m.Failed != c.failed || m.ExitCode != c.exit {
				t.Errorf("failed/exit = %v/%d, want %v/%d", m.Failed, m.ExitCode, c.failed, c.exit)
			}
		})
	}

	un := byID(t, ms, "model-unloaded-q4km")
	if len(un.Aliases) != 1 || un.Aliases[0] != "legacy-unloaded" || len(un.Tags) != 1 || un.Tags[0] != "chat" {
		t.Errorf("aliases/tags = %v/%v", un.Aliases, un.Tags)
	}
	if !strings.Contains(un.Preset, "[model-unloaded-q4km]") || un.Source != SourcePreset {
		t.Errorf("preset/source = %q/%q", un.Preset, un.Source)
	}
	if len(un.InputModalities) != 2 || un.OutputModalities[0] != "text" {
		t.Errorf("modalities = %v/%v", un.InputModalities, un.OutputModalities)
	}

	loaded := byID(t, ms, "model-loaded-q4km")
	if !loaded.Loaded() || loaded.ContextLength != 262144 || len(loaded.Args) != 3 {
		t.Errorf("loaded entry = %+v", loaded)
	}
	// effective: model + context + compute of the "device" entry only (the host entry is RAM).
	eff := loaded.EffectiveMiB()
	if eff == nil || *eff != 5556 {
		t.Fatalf("EffectiveMiB = %v, want 5556", eff)
	}
	if got := loaded.Memory.Max.DeviceMiB(); got != 21428 {
		t.Errorf("max DeviceMiB = %d, want 21428", got)
	}
	if (*MemoryEstimate)(nil).DeviceMiB() != 0 {
		t.Error("a nil estimate must be 0")
	}
	if loaded.Memory.Status != "measured" || loaded.Memory.Max.NCtx != 131072 {
		t.Errorf("memory = %+v", loaded.Memory)
	}
	if byID(t, ms, "model-unloaded-q4km").EffectiveMiB() != nil {
		t.Error("a model without memory must have no EffectiveMiB")
	}

	dl := byID(t, ms, "org-example/model-downloading-GGUF:Q4_K_M")
	if d := dl.Downloads["https://models.example.test/model.gguf"]; d.Done != 195963406 || d.Total != 219307424 {
		t.Errorf("downloads = %+v", dl.Downloads)
	}
	if dl.Source != SourceCache {
		t.Errorf("source = %q", dl.Source)
	}
	if byID(t, ms, "model-downloaded-q4km").Source != SourceModelsDir {
		t.Error("models_dir source lost")
	}
}

func TestParseModelsReadmeSample(t *testing.T) {
	ms, err := ParseModels(fixture(t, "models_readme_sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "org-example/small-it-GGUF:Q4_K_M" || ms[0].Status != StatusLoaded {
		t.Fatalf("got %+v", ms)
	}
	if ms[0].Aliases != nil || ms[0].Memory != nil || ms[0].Source != "" {
		t.Errorf("absent fields must stay empty: %+v", ms[0])
	}
}

func TestParseModelsToleratesTheUnknown(t *testing.T) {
	body := `{"data":[{"id":"m","status":{"value":"hibernating","future":true},"new_field":[1,2]}],"extra":1}`
	ms, err := ParseModels([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].Status != "hibernating" || ms[0].Status.Known() || ms[0].Status.Running() || ms[0].Status.Resident() {
		t.Errorf("an unknown status must be kept and treated as not running: %+v", ms[0])
	}
	// A loading progress object (not a download map) is ignored, not an error.
	body = `{"data":[{"id":"m","status":{"value":"loading","progress":{"stages":["a"],"current":"a","value":0.1}}}]}`
	ms, err = ParseModels([]byte(body))
	if err != nil || ms[0].Downloads != nil {
		t.Errorf("loading progress: err=%v downloads=%v", err, ms[0].Downloads)
	}
	if ms, err = ParseModels([]byte(`{"data":[]}`)); err != nil || len(ms) != 0 {
		t.Errorf("empty list: %v %v", ms, err)
	}
}

func TestParseModelsMalformed(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty body", ``},
		{"not json", `<html>502</html>`},
		{"truncated", `{"data":[{"id":"a","status":{"value":"loa`},
		{"array at top", `[{"id":"a"}]`},
		{"null", `null`},
		{"no data", `{"object":"list"}`},
		{"data not an array", `{"data":{"id":"a"}}`},
		{"data null", `{"data":null}`},
		{"entry not an object", `{"data":["a"]}`},
		{"no id", `{"data":[{"status":{"value":"loaded"}}]}`},
		{"empty id", `{"data":[{"id":"","status":{"value":"loaded"}}]}`},
		{"id with newline", "{\"data\":[{\"id\":\"a\\nb\",\"status\":{\"value\":\"loaded\"}}]}"},
		{"id not a string", `{"data":[{"id":7,"status":{"value":"loaded"}}]}`},
		{"no status", `{"data":[{"id":"a"}]}`},
		{"status not an object", `{"data":[{"id":"a","status":"loaded"}]}`},
		{"no status value", `{"data":[{"id":"a","status":{"args":[]}}]}`},
		{"empty status value", `{"data":[{"id":"a","status":{"value":""}}]}`},
		{"aliases a string", `{"data":[{"id":"a","aliases":"x","status":{"value":"loaded"}}]}`},
		{"failed a string", `{"data":[{"id":"a","status":{"value":"unloaded","failed":"yes"}}]}`},
		{"duplicate id", `{"data":[{"id":"a","status":{"value":"loaded"}},{"id":"a","status":{"value":"unloaded"}}]}`},
		{"negative memory", `{"data":[{"id":"a","status":{"value":"loaded"},"memory":{"effective":{"devices":[{"kind":"device","model":-1}]}}}]}`},
		{"trailing data", `{"data":[]} {"data":[]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms, err := ParseModels([]byte(c.body))
			if !errors.Is(err, ErrMalformed) || ms != nil {
				t.Errorf("got %v, %v; want ErrMalformed", ms, err)
			}
		})
	}
}

func TestParseProps(t *testing.T) {
	p, err := ParseProps(fixture(t, "props.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.NCtx != 65536 || p.TotalSlots != 1 || p.BuildInfo != "b1234-abcdef0" || p.Sleeping || !strings.HasSuffix(p.ModelPath, ".gguf") {
		t.Errorf("props = %+v", p)
	}
	if p, err = ParseProps([]byte(`{}`)); err != nil || p.NCtx != 0 {
		t.Errorf("empty props: %+v %v", p, err)
	}
	for _, bad := range []string{``, `[]`, `nope`, `{"total_slots":"one"}`, `{} {}`} {
		if _, err := ParseProps([]byte(bad)); !errors.Is(err, ErrMalformed) {
			t.Errorf("ParseProps(%q) = %v, want ErrMalformed", bad, err)
		}
	}
}

func TestValidID(t *testing.T) {
	good := []string{"m", "org-example/model-GGUF:Q4_K_M", "qwen3.5-9b-q4km", strings.Repeat("a", maxIDLen)}
	bad := []string{"", strings.Repeat("a", maxIDLen+1), "a\x00b", "a\nb", "a\x7fb", "\xff\xfe"}
	for _, id := range good {
		if !validID(id) {
			t.Errorf("validID(%q) = false", id)
		}
	}
	for _, id := range bad {
		if validID(id) {
			t.Errorf("validID(%q) = true", id)
		}
	}
}
