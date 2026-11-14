package config_test

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// node.yaml workloads{} and ports.music (E11, agent.md section 3.4).

const musicWorkload = `workloads:
  music:
    label: {en: Music, es: "Música"}
    models: [gemma-4b]
`

func TestWorkloadsParseAndDefaultMusicPort(t *testing.T) {
	cfg, probs := config.Load([]byte(minimal+musicWorkload), catalogHooks())
	if len(probs) != 0 {
		t.Fatalf("problems: %+v", probs)
	}
	w, ok := cfg.Workloads["music"]
	if !ok || w.Label.ES != "Música" || len(w.Models) != 1 || w.Models[0] != "gemma-4b" {
		t.Fatalf("workloads = %+v", cfg.Workloads)
	}
	if cfg.Ports.Music != config.DefaultMusicPort {
		t.Errorf("ports.music = %d, want %d", cfg.Ports.Music, config.DefaultMusicPort)
	}
	c := cfg.Clone()
	c.Workloads["music"].Models[0] = "changed"
	if cfg.Workloads["music"].Models[0] != "gemma-4b" {
		t.Error("Clone shares the workload model list")
	}
}

func TestWorkloadValidation(t *testing.T) {
	cases := []struct {
		name, yaml, path, code string
	}{
		{"unknown model", "workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: [no-such]\n", "workloads.chat.models[0]", config.CodeWorkloadModel},
		{"bad name", "workloads:\n  Chat:\n    label: {en: Chat, es: Charla}\n    models: [gemma-4b]\n", "workloads.Chat", ""},
		{"no Spanish label", "workloads:\n  chat:\n    label: {en: Chat}\n    models: [gemma-4b]\n", "workloads.chat.label", ""},
		{"no models", "workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: []\n", "workloads.chat.models", ""},
		{"music port duplicates whisper", "ports:\n  whisper: 8081\n  music: 8081\n", "ports.music", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, probs := config.Load([]byte(minimal+c.yaml), catalogHooks())
			p, ok := findProblem(probs, c.path)
			if !ok {
				t.Fatalf("no problem at %q: %+v", c.path, probs)
			}
			if p.Severity != config.SeverityError || p.Code != c.code {
				t.Errorf("problem = %+v, want an error with code %q", p, c.code)
			}
		})
	}
	// Without a catalog the model ids are not judged.
	if _, probs := config.Load([]byte(minimal+"workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: [no-such]\n"), nil); len(probs) != 0 {
		t.Errorf("without a catalog: %+v", probs)
	}
}
