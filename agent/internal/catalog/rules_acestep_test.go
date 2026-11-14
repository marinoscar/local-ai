package catalog

import (
	"os"
	"strings"
	"testing"
)

// The E11 rules: an engine acestep model (LAI-CAT-021) and the workloads of a profile (LAI-CAT-020,
// LAI-CAT-018). The acestep example lives in testdata/acestep/model.yaml; every case appends it to the
// valid fixture and adds it to the cuda-12gb profile.

func acestepEntry(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/acestep/model.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const acestepID = "acestep-v15-turbo"

// withAcestep appends the example entry (after edit, applied to its text) and includes it in the
// cuda-12gb profile.
func withAcestep(edit func(string) string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		entry := acestepEntry(t)
		if edit != nil {
			entry = edit(entry)
		}
		writeFile(t, dir, fM, readFile(t, dir, fM)+entry)
		p := readFile(t, dir, pCu)
		p = strings.Replace(p, "whisper-small, piper-en-medium]", "whisper-small, piper-en-medium, "+acestepID+"]", 1)
		writeFile(t, dir, pCu, p)
	}
}

func replacing(old, new string) func(string) string {
	return func(s string) string { return strings.Replace(s, old, new, 1) }
}

const workloadsYAML = `workloads:
  coding:
    label: {en: Coding, es: "Programación"}
    models: [model-alpha-9b-q4]
  music:
    label: {en: Music, es: "Música"}
    models: [` + acestepID + `]
`

func TestAcestepAndWorkloadsAllowed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"the acestep example is valid", withAcestep(nil)},
		{"a profile with workloads, one of them music", func(t *testing.T, dir string) {
			withAcestep(nil)(t, dir)
			appendTo(pCu, workloadsYAML)(t, dir)
		}},
		{"a file at the top of the model folder", withAcestep(replacing(`name: "acestep-v15-turbo/config.json"`, `name: "config.json"`))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := validDir(t)
			c.setup(t, dir)
			rep := Lint(dir, LintOptions{})
			if !rep.Valid() {
				t.Fatalf("expected valid, got:\n%s", problemsText(rep))
			}
			cat, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			m, ok := cat.Model(acestepID)
			if !ok || m.Engine != EngineAcestep || len(m.Files) != 3 {
				t.Fatalf("acestep model not loaded: %+v", m)
			}
		})
	}
}

func TestAcestepAndWorkloadRules(t *testing.T) {
	const mp = "models[6]"
	cases := []struct {
		name  string
		setup func(*testing.T, string)
		want  []want
	}{
		{"021 a file of another kind", withAcestep(replacing(`{kind: acestep, name: "acestep-v15-turbo/config.json"`, `{kind: weights, name: "acestep-v15-turbo/config.json"`)),
			[]want{w(CodeAcestepLayout, fM, mp+".files[1].kind")}},
		{"021 two sub-directory levels", withAcestep(replacing(`"vae/diffusion_pytorch_model.safetensors"`, `"vae/deep/diffusion_pytorch_model.safetensors"`)),
			[]want{w(CodeAcestepLayout, fM, mp+".files[2].name")}},
		{"021 no music role", withAcestep(replacing("roles: [music]", "roles: [chat]")),
			[]want{w(CodeAcestepLayout, fM, mp+".roles")}},
		{"021 no graphics memory requirement", withAcestep(replacing("min_vram_mib: 8000", "min_vram_mib: 0")),
			[]want{w(CodeAcestepLayout, fM, mp+".requirements.min_vram_mib")}},
		{"021 a backend other than cuda", withAcestep(replacing("backends: [cuda]", "backends: [cuda, vulkan]")),
			[]want{w(CodeAcestepLayout, fM, mp+".requirements.backends[1]")}},
		{"021 a llama block", withAcestep(replacing("    requirements:", "    llama: {ctx: 4096}\n    requirements:")),
			[]want{w(CodeAcestepLayout, fM, mp+".llama")}},
		{"021 kind acestep on a llama model", func(t *testing.T, dir string) {
			applyEdits(t, dir, []edit{{fM, `{kind: draft, name: "Model-Beta-1B-Q4_K_M.gguf"`, `{kind: acestep, name: "Model-Beta-1B-Q4_K_M.gguf"`}})
		}, []want{w(CodeAcestepLayout, fM, "models[1].files"), w(CodeModelFiles, fM, "models[1].llama.speculative")}},
		{"020 workload with an unknown model", func(t *testing.T, dir string) {
			appendTo(pCu, "workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: [no-such-model]\n")(t, dir)
		}, []want{w(CodeWorkloadModel, pCu, "workloads.chat.models[0]")}},
		{"018 workload model not in include.models", func(t *testing.T, dir string) {
			appendTo(pCp, "workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: [model-beta-27b-q4]\n")(t, dir)
		}, []want{w(CodeProfile, pCp, "workloads.chat.models[0]")}},
		{"018 bad workload name", func(t *testing.T, dir string) {
			appendTo(pCu, "workloads:\n  Chat_1:\n    label: {en: Chat, es: Charla}\n    models: [model-alpha-9b-q4]\n")(t, dir)
		}, []want{w(CodeProfile, pCu, "workloads.Chat_1")}},
		{"018 a model twice", func(t *testing.T, dir string) {
			appendTo(pCu, "workloads:\n  chat:\n    label: {en: Chat, es: Charla}\n    models: [model-alpha-9b-q4, model-alpha-9b-q4]\n")(t, dir)
		}, []want{w(CodeProfile, pCu, "workloads.chat.models[1]")}},
		{"010 workload without a Spanish label", func(t *testing.T, dir string) {
			appendTo(pCu, "workloads:\n  chat:\n    label: {en: Chat}\n    models: [model-alpha-9b-q4]\n")(t, dir)
		}, []want{w(CodeSchema, pCu, "workloads.chat.label.es")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := validDir(t)
			c.setup(t, dir)
			rep := Lint(dir, LintOptions{})
			if len(rep.Problems) != len(c.want) {
				t.Fatalf("want %d problem(s), got %d:\n%s", len(c.want), len(rep.Problems), problemsText(rep))
			}
			for _, wnt := range c.want {
				if !has(rep, wnt.code, wnt.file, wnt.path) {
					t.Errorf("missing %s %s:%s in:\n%s", wnt.code, wnt.file, wnt.path, problemsText(rep))
				}
			}
		})
	}
}
