package router

import (
	"encoding/json"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func model(id, engine string, vram, ram int64, backends ...string) catalog.Model {
	return catalog.Model{ID: id, Engine: engine, Requirements: req(vram, ram, backends...)}
}

var notFit = string(CodeNotFit)

func TestLoadableAllLabNodes(t *testing.T) {
	models := []catalog.Model{
		model("llm-small", "llama", 6400, 8192),
		model("llm-big", "llama", 22000, 24000),
		model("llm-cuda-only", "llama", 4000, 4096, hardware.BackendCUDA),
		model("stt", "whisper", 0, 1500),
		model("tts", "piper", 0, 300),
	}
	present := map[string]bool{"llm-small": true, "llm-big": true, "llm-cuda-only": true, "stt": true, "tts": true}
	tests := []struct {
		name string
		snap hardware.Snapshot
		want map[string]string // id -> "" (loadable) or the reason code
	}{
		{"ai-n3 8 GB cuda", snapAiN3(), map[string]string{
			"llm-small": "", "llm-big": notFit, "llm-cuda-only": "", "stt": "", "tts": ""}},
		{"ai-n1 unified vulkan", snapAiN1(), map[string]string{
			"llm-small": "", "llm-big": "", "llm-cuda-only": notFit, "stt": "", "tts": ""}},
		{"cpu only", snapCPUOnly(), map[string]string{
			"llm-small": "", "llm-big": notFit, "llm-cuda-only": notFit, "stt": "", "tts": ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := NewEnv(tc.snap, "", routerBlock(1, 512), 0)
			got := env.LoadableAll(models, present)
			if len(got) != len(models) {
				t.Fatalf("%d results for %d models", len(got), len(models))
			}
			for i, l := range got {
				if l.ID != models[i].ID {
					t.Fatalf("order: %s at %d", l.ID, i)
				}
				code := ""
				if l.Reason != nil {
					code = string(l.Reason.Code)
				}
				if code != tc.want[l.ID] || l.Loadable != (code == "") {
					t.Errorf("%s: loadable=%v reason=%v, want code %q", l.ID, l.Loadable, l.Reason, tc.want[l.ID])
				}
			}
		})
	}
}

func TestLoadableMessage(t *testing.T) {
	// The issue's example: a model that needs 11.0 GB of graphics memory on an 8 GB node.
	env := NewEnv(snapAiN3(), "", routerBlock(1, 512), 0)
	m := model("qwen-big", "llama", 10490, 12000)
	ok, why := env.Loadable(&m, true)
	if ok || why == nil {
		t.Fatal("expected not loadable")
	}
	if why.Code != CodeNotFit || why.Message != "Needs 11.0 GB of graphics memory; this computer has 8.6 GB." {
		// 8192 MiB is 8.6 GB decimal; the 8.0 GB of the spec example is a rounded illustration.
		t.Fatalf("%+v", why)
	}
	raw, _ := json.Marshal(Loadability{ID: m.ID, Loadable: ok, Reason: why})
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["loadable"] != false || back["loadable_reason"].(map[string]any)["code"] != string(CodeNotFit) {
		t.Fatalf("%s", raw)
	}
	loadableModel := model("qwen-small", "llama", 4000, 4096)
	raw, _ = json.Marshal(env.LoadableAll([]catalog.Model{loadableModel}, map[string]bool{"qwen-small": true}))
	if string(raw) != `[{"id":"qwen-small","loadable":true,"loadable_reason":null}]` {
		t.Fatalf("%s", raw)
	}
}

func TestLoadableNonLlamaIgnoresVRAM(t *testing.T) {
	env := NewEnv(snapAiN3(), "", routerBlock(1, 512), 0)
	stt := model("stt", "whisper", 99999, 1500) // a stray min_vram_mib is ignored for a non-llama engine
	if ok, why := env.Loadable(&stt, true); !ok {
		t.Fatalf("whisper judged on VRAM: %+v", why)
	}
	llm := model("llm", "llama", 99999, 1500)
	if ok, _ := env.Loadable(&llm, true); ok {
		t.Fatal("llama model judged without VRAM")
	}
	// Backend and RAM still apply to every engine.
	tts := model("tts", "piper", 0, 999999)
	if ok, why := env.Loadable(&tts, true); ok || why.Code != CodeNotFit {
		t.Fatalf("piper RAM: %v %+v", ok, why)
	}
	cpuOnly := model("tts2", "piper", 0, 100, hardware.BackendCPU)
	if ok, why := env.Loadable(&cpuOnly, true); ok || why.Code != CodeNotFit {
		t.Fatalf("piper backend: %v %+v", ok, why)
	}
}

func TestLoadablePresence(t *testing.T) {
	env := NewEnv(snapAiN3(), "", routerBlock(1, 512), 0)
	m := model("llm-small", "llama", 6400, 8192)
	ok, why := env.Loadable(&m, false)
	if ok || why.Code != CodeFilesMissing {
		t.Fatalf("absent files: %v %+v", ok, why)
	}
	// A nil presence map means nothing is present.
	got := env.LoadableAll([]catalog.Model{m}, nil)
	if got[0].Loadable || got[0].Reason.Code != CodeFilesMissing {
		t.Fatalf("%+v", got[0])
	}
	// A model that does not fit is reported as not fitting even when its files are missing: the
	// fit rules come first.
	big := model("llm-big", "llama", 22000, 24000)
	if _, why := env.Loadable(&big, false); why.Code != CodeNotFit {
		t.Fatalf("%+v", why)
	}
}

func TestLoadableUnifiedMemoryReserve(t *testing.T) {
	// ai-n1: 124928 MiB of RAM, 4096 reserved: a model needing 122000 MiB of RAM does not load
	// although the GTT (62464 MiB) is not the limit for a small VRAM need.
	env := NewEnv(snapAiN1(), "", routerBlock(3, 1024), 0)
	fits := model("fits", "llama", 40000, 120000)
	over := model("over", "llama", 40000, 122000)
	if ok, why := env.Loadable(&fits, true); !ok {
		t.Fatalf("%+v", why)
	}
	ok, why := env.Loadable(&over, true)
	if ok || why.Code != CodeNotFit {
		t.Fatalf("%v %+v", ok, why)
	}
	want := "Needs 127.9 GB of memory, and the system keeps 4.3 GB for itself; this computer has 131.0 GB."
	if why.Message != want {
		t.Fatalf("%q", why.Message)
	}
	// Missing hardware data: no snapshot at all is a cpu box with 0 RAM, so nothing loads.
	empty := NewEnv(hardware.Snapshot{}, "", routerBlock(1, 0), 0)
	if ok, _ := empty.Loadable(&fits, true); ok {
		t.Fatal("loadable on an unknown machine")
	}
}
