package router

import (
	"encoding/json"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func TestFormatGB(t *testing.T) {
	cases := []struct {
		mib  int64
		want string
	}{
		{0, "0.0"},
		{1, "0.0"},
		{477, "0.5"},         // 0.500 GB
		{7630, "8.0"},        // the 8.0 GB of the API example
		{8192, "8.6"},        // 8 GiB is 8.6 GB in decimal units
		{10486, "11.0"},      // the 11.0 GB of the API example
		{11264, "11.8"},      // 11 GiB
		{14336, "15.0"},      // 14 GiB
		{62464, "65.5"},      // 61 GiB
		{2048, "2.1"},        // the R_res
		{4096, "4.3"},        // the unified R_res
		{9536743, "10000.0"}, // large values stay plain
	}
	for _, c := range cases {
		if got := FormatGB(c.mib); got != c.want {
			t.Errorf("FormatGB(%d) = %q, want %q", c.mib, got, c.want)
		}
	}
}

func TestReasonText(t *testing.T) {
	r := reasonGraphics(10486, 7630)
	if r.Message != "Needs 11.0 GB of graphics memory; this computer has 8.0 GB." {
		t.Errorf("English message = %q", r.Message)
	}
	for _, lang := range []string{"es", "ES", "es-CR", "es_MX"} {
		if got := r.Text(lang); got != "Necesita 11,0 GB de memoria gráfica; este equipo tiene 8,0 GB." {
			t.Errorf("Text(%q) = %q", lang, got)
		}
	}
	for _, lang := range []string{"", "en", "en-US", "fr"} {
		if got := r.Text(lang); got != r.Message {
			t.Errorf("Text(%q) = %q, want English", lang, got)
		}
	}
	if (Reason{Code: CodeNotFit, Message: "x"}).Text("es") != "x" {
		t.Error("a reason without Spanish text falls back to English")
	}
}

// The API shape of loadable_reason (agent.md section 3.8.7) is {"code", "message"} only.
func TestReasonJSONShape(t *testing.T) {
	r := reasonGraphics(10486, 7630)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"LAI-CAT-005","message":"Needs 11.0 GB of graphics memory; this computer has 8.0 GB."}`
	if string(b) != want {
		t.Errorf("json = %s\nwant   %s", b, want)
	}
}

func TestNewEnv(t *testing.T) {
	t.Run("ai-n3 cuda", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		want := Env{Backend: "cuda", GTotalMiB: 8192, GOthersMiB: 300, RTotalMiB: 14336, RAvailMiB: 11000, MarginMiB: 512, ModelsMax: 1}
		if e != want {
			t.Errorf("env = %+v\nwant %+v", e, want)
		}
	})
	t.Run("ai-n1 unified uses GTT and the larger reserve", func(t *testing.T) {
		e := NewEnv(snapAiN1(), "", routerBlock(3, 1024), 0)
		if !e.Unified || e.GTotalMiB != 62464 || e.RAMReserve() != 4096 || e.MarginMiB != 1024 || e.ModelsMax != 3 {
			t.Errorf("env = %+v", e)
		}
	})
	t.Run("cpu has no graphics memory", func(t *testing.T) {
		e := NewEnv(snapCPUOnly(), "", routerBlock(1, 0), 0)
		if e.Backend != "cpu" || e.GTotalMiB != 0 || e.GOthersMiB != 0 || e.Unified || e.RAMReserve() != 2048 {
			t.Errorf("env = %+v", e)
		}
	})
	t.Run("empty snapshot is a cpu node", func(t *testing.T) {
		e := NewEnv(hardware.Snapshot{}, "", routerBlock(1, 0), 0)
		if e.Backend != "cpu" {
			t.Errorf("backend = %q", e.Backend)
		}
	})
	t.Run("explicit backend wins over the detected one", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "cpu", routerBlock(1, 0), 0)
		if e.Backend != "cpu" || e.GTotalMiB != 0 {
			t.Errorf("env = %+v", e)
		}
	})
	t.Run("several CUDA GPUs add up", func(t *testing.T) {
		s := snapAiN3()
		second := s.GPUs[0]
		second.VRAMTotalMiB = 12288
		second.VRAMUsedByOthersMiB = i64p(200)
		s.GPUs = append(s.GPUs, second)
		e := NewEnv(s, "cuda", routerBlock(1, 0), 0)
		if e.GTotalMiB != 8192+12288 || e.GOthersMiB != 500 {
			t.Errorf("G_total=%d G_others=%d", e.GTotalMiB, e.GOthersMiB)
		}
	})
	t.Run("hybrid laptop: cuda sees NVIDIA only, vulkan the AMD GPU", func(t *testing.T) {
		s := snapAiN3()
		s.GPUs = append(s.GPUs, hardware.GPU{
			Vendor: hardware.VendorAMD, Backend: hardware.BackendVulkan, VRAMTotalMiB: 512,
		})
		if e := NewEnv(s, "cuda", routerBlock(1, 0), 0); e.GTotalMiB != 8192 {
			t.Errorf("cuda G_total = %d", e.GTotalMiB)
		}
		if e := NewEnv(s, "vulkan", routerBlock(1, 0), 0); e.GTotalMiB != 512 {
			t.Errorf("vulkan G_total = %d", e.GTotalMiB)
		}
	})
	t.Run("unmeasurable others count as zero, negatives are clamped", func(t *testing.T) {
		s := snapAiN2()
		s.GPUs[0].VRAMUsedByOthersMiB = nil
		s.RAM.AvailableMiB = -5
		e := NewEnv(s, "", routerBlock(1, 0), -10)
		if e.GOthersMiB != 0 || e.RAvailMiB != 0 || e.WhisperReserveMiB != 0 {
			t.Errorf("env = %+v", e)
		}
	})
	t.Run("whisper reserve is passed through", func(t *testing.T) {
		if e := NewEnv(snapAiN2(), "", routerBlock(1, 0), 1500); e.WhisperReserveMiB != 1500 {
			t.Errorf("W_res = %d", e.WhisperReserveMiB)
		}
	})
}

func TestStaticFitRules(t *testing.T) {
	n3 := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)     // cuda, G 8192, M 512, R 14336
	n1 := NewEnv(snapAiN1(), "", routerBlock(3, 0), 0)     // vulkan unified, G 62464, R 124928
	cpu := NewEnv(snapCPUOnly(), "", routerBlock(1, 0), 0) // cpu, R 16384

	// A vulkan node with a dedicated card and little RAM: rule 3 must not apply, rule 5 does.
	dedicated := Env{Backend: "vulkan", GTotalMiB: 24576, RTotalMiB: 8192, RAvailMiB: 6000, MarginMiB: 512, ModelsMax: 1}
	// A unified node with little RAM, so rule 3 can fail alone.
	smallUnified := Env{Backend: "vulkan", Unified: true, GTotalMiB: 61440, RTotalMiB: 8192, RAvailMiB: 6000, MarginMiB: 512, ModelsMax: 1}

	type tc struct {
		name     string
		env      Env
		m        Candidate
		ok       bool
		code     Code
		message  string
		messageE string // Spanish, when checked
	}
	cases := []tc{
		// Passing cases, including every boundary.
		{name: "ai-n3 9B fits", env: n3, m: cand("chat-9b", 7000, 8000), ok: true},
		{name: "ai-n3 exactly G_total minus M fits", env: n3, m: cand("edge", 8192-512, 1000), ok: true},
		{name: "ai-n3 RAM exactly at the cpu-style cap still fits on cuda", env: n3, m: cand("edge", 100, 14336), ok: true},
		{name: "cpu exactly R_total minus 2048 fits", env: cpu, m: cand("edge", 0, 16384-2048, "cpu"), ok: true},
		{name: "unified exactly R_total minus 4096 fits", env: smallUnified, m: cand("edge", 1000, 8192-4096), ok: true},
		{name: "cpu backend ignores min_vram_mib", env: cpu, m: cand("cpu-ok", 50000, 4000), ok: true},
		{name: "ai-n1 big model fits in GTT", env: n1, m: cand("moe-35b", 24000, 8000), ok: true},

		// Rule 1: backend.
		{name: "rule 1: model is cuda-only, node is vulkan", env: n1, m: cand("cuda-only", 1000, 1000, "cuda"),
			code: CodeNotFit, message: "This model does not run on vulkan.", messageE: "Este modelo no funciona con vulkan."},
		{name: "rule 1: model is cuda/vulkan, node is cpu", env: cpu, m: cand("gpu-only", 1000, 1000, "cuda", "vulkan"),
			code: CodeNotFit, message: "This model does not run on cpu."},
		{name: "rule 1: empty backend list matches nothing", env: n3, m: Candidate{ID: "x", Present: true},
			code: CodeNotFit, message: "This model does not run on cuda."},

		// Rule 2: graphics memory (GPU backends).
		{name: "rule 2: 35B on ai-n3", env: n3, m: cand("moe-35b", 11264, 4000),
			code: CodeNotFit, message: "Needs 11.8 GB of graphics memory; this computer has 8.6 GB.",
			messageE: "Necesita 11,8 GB de memoria gráfica; este equipo tiene 8,6 GB."},
		{name: "rule 2: one MiB past G_total minus M", env: n3, m: cand("edge", 8192-512+1, 1000),
			code: CodeNotFit, message: "Needs 8.1 GB of graphics memory; this computer has 8.6 GB."},
		{name: "rule 2: margin comes from the profile", env: NewEnv(snapAiN3(), "", routerBlock(1, 2048), 0), m: cand("edge", 8192-512, 1000),
			code: CodeNotFit, message: "Needs 8.1 GB of graphics memory; this computer has 8.6 GB."},
		{name: "rule 2: G_others is ignored by the static rule", env: Env{Backend: "cuda", GTotalMiB: 8192, GOthersMiB: 6000, RTotalMiB: 14336, MarginMiB: 512, ModelsMax: 1}, m: cand("chat-9b", 7000, 8000), ok: true},
		{name: "rule 2: unified node compares against GTT, not the UMA slice", env: n1, m: cand("too-big", 62464-512+1, 1000),
			code: CodeNotFit},

		// Rule 3: unified memory also needs the RAM reserve of 4096 MiB.
		{name: "rule 3: unified, one MiB past R_total minus 4096", env: smallUnified, m: cand("edge", 1000, 8192-4096+1),
			code: CodeNotFit, message: "Needs 4.3 GB of memory, and the system keeps 4.3 GB for itself; this computer has 8.6 GB.",
			messageE: "Necesita 4,3 GB de memoria, y el sistema reserva 4,3 GB para sí mismo; este equipo tiene 8,6 GB."},
		{name: "rule 3 does not apply to a dedicated vulkan card (a 2048 reserve is not enforced either)", env: dedicated, m: cand("edge", 1000, 8192-2048+1), ok: true},

		// Rule 4: CPU backend needs the RAM reserve of 2048 MiB.
		{name: "rule 4: cpu, one MiB past R_total minus 2048", env: cpu, m: cand("edge", 0, 16384-2048+1, "cpu"),
			code: CodeNotFit, message: "Needs 15.0 GB of memory, and the system keeps 2.1 GB for itself; this computer has 17.2 GB.",
			messageE: "Necesita 15,0 GB de memoria, y el sistema reserva 2,1 GB para sí mismo; este equipo tiene 17,2 GB."},

		// Rule 5: every backend, RAM must fit in the machine.
		{name: "rule 5: cuda node, RAM need above R_total", env: n3, m: cand("ram-hog", 1000, 16384),
			code: CodeNotFit, message: "Needs 17.2 GB of memory; this computer has 15.0 GB.",
			messageE: "Necesita 17,2 GB de memoria; este equipo tiene 15,0 GB."},
		{name: "rule 5: one MiB past R_total on cuda", env: n3, m: cand("edge", 1000, 14336+1), code: CodeNotFit},
		{name: "rule 5: dedicated vulkan card with little RAM", env: dedicated, m: cand("ram-hog", 1000, 8192+1),
			code: CodeNotFit},

		// Rule 6: files.
		{name: "rule 6: fits but not present", env: n3, m: Candidate{ID: "chat-9b", Requirements: req(7000, 8000), Present: false},
			code: CodeFilesMissing, message: "The files of this model are missing or not verified on this computer.",
			messageE: "Los archivos de este modelo faltan o no están verificados en este equipo."},

		// Order: the first failing rule wins.
		{name: "order: rule 1 beats rule 2", env: n3, m: Candidate{ID: "x", Requirements: req(20000, 1000, "vulkan"), Present: true},
			code: CodeNotFit, message: "This model does not run on cuda."},
		{name: "order: rule 2 beats rule 5", env: n3, m: cand("x", 20000, 20000),
			code: CodeNotFit, message: "Needs 21.0 GB of graphics memory; this computer has 8.6 GB."},
		{name: "order: rule 3 beats rule 5", env: smallUnified, m: cand("x", 1000, 8192),
			code: CodeNotFit, message: "Needs 8.6 GB of memory, and the system keeps 4.3 GB for itself; this computer has 8.6 GB."},
		{name: "order: a misfit beats missing files", env: n3, m: Candidate{ID: "x", Requirements: req(20000, 1000), Present: false},
			code: CodeNotFit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := c.env.StaticFit(c.m)
			if c.ok {
				if !ok || reason != nil {
					t.Fatalf("StaticFit = %v, %+v; want ok", ok, reason)
				}
				return
			}
			if ok || reason == nil {
				t.Fatalf("StaticFit = %v, %v; want a failure with code %s", ok, reason, c.code)
			}
			if reason.Code != c.code {
				t.Errorf("code = %s, want %s (message %q)", reason.Code, c.code, reason.Message)
			}
			if c.message != "" && reason.Message != c.message {
				t.Errorf("message = %q\nwant      %q", reason.Message, c.message)
			}
			if c.messageE != "" && reason.MessageES != c.messageE {
				t.Errorf("Spanish = %q\nwant     %q", reason.MessageES, c.messageE)
			}
			if reason.MessageES == "" {
				t.Error("every reason carries a Spanish message")
			}
		})
	}
}

// Each rule is the only one failing in its own case: a model that passes every other rule.
func TestStaticFitEachRuleFailsAlone(t *testing.T) {
	envs := map[string]Env{
		"cuda":    NewEnv(snapAiN3(), "", routerBlock(1, 0), 0),
		"unified": NewEnv(snapAiN1(), "", routerBlock(3, 0), 0),
		"cpu":     NewEnv(snapCPUOnly(), "", routerBlock(1, 0), 0),
	}
	for name, e := range envs {
		good := Candidate{ID: "ok", Requirements: req(100, 100), Present: true}
		if ok, r := e.StaticFit(good); !ok {
			t.Fatalf("%s: baseline model rejected: %+v", name, r)
		}
		// Present is the only thing wrong.
		noFiles := good
		noFiles.Present = false
		if ok, r := e.StaticFit(noFiles); ok || r.Code != CodeFilesMissing {
			t.Errorf("%s: missing files: ok=%v reason=%+v", name, ok, r)
		}
		// The backend is the only thing wrong.
		other := good
		other.Requirements.Backends = []string{"rocm"}
		if ok, r := e.StaticFit(other); ok || r.Code != CodeNotFit {
			t.Errorf("%s: backend: ok=%v reason=%+v", name, ok, r)
		}
	}
}

func TestStaticFitLabNodes(t *testing.T) {
	// The brief's lab: ai-n3 cannot run a model needing 11264 MiB of graphics memory; ai-n2 (12 GB)
	// and ai-n1 can.
	big := cand("moe-35b", 11264, 4000)
	n3 := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
	n2 := NewEnv(snapAiN2(), "", routerBlock(2, 0), 0)
	n1 := NewEnv(snapAiN1(), "", routerBlock(3, 0), 0)
	if ok, r := n3.StaticFit(big); ok || r.Code != CodeNotFit {
		t.Errorf("ai-n3: ok=%v reason=%+v", ok, r)
	}
	if ok, r := n2.StaticFit(big); !ok {
		t.Errorf("ai-n2: %+v", r)
	}
	if ok, r := n1.StaticFit(big); !ok {
		t.Errorf("ai-n1: %+v", r)
	}
	// A CPU-only box runs only models listing cpu; a 9B with 8 GB of RAM fits, a 35B at 20 GB does not.
	cpu := NewEnv(snapCPUOnly(), "", routerBlock(1, 0), 0)
	if ok, r := cpu.StaticFit(cand("chat-9b", 0, 8000, "cuda", "vulkan", "cpu")); !ok {
		t.Errorf("cpu 9B: %+v", r)
	}
	if ok, r := cpu.StaticFit(cand("moe-35b", 0, 20000, "cpu")); ok || r.Code != CodeNotFit {
		t.Errorf("cpu 35B: ok=%v reason=%+v", ok, r)
	}
}
