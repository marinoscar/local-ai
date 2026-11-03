package kit_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

// Every need of a computer, present (the healthy tests) and missing (here): taking exactly the files of
// one need away leaves exactly that need missing, with LAI-KIT-040 and the part and item that hold it.
func TestVerifyEachNeedMissing(t *testing.T) {
	n := kittest.AiN3()
	llama, whisper := n.Llama(), n.Whisper()
	tests := []struct {
		name   string
		remove []string
		need   string
		part   string
		item   string
	}{
		{"llama.cpp source", []string{"git/llama.cpp.git", "src/llama.cpp-" + llama + ".tar.gz"}, kit.NeedLlamaSource, "src", "kit-llama-cpp"},
		{"whisper.cpp source", []string{"git/whisper.cpp.git", "src/whisper.cpp-" + whisper + ".tar.gz"}, kit.NeedWhisperSource, "src", "kit-whisper-cpp"},
		{"agent program", []string{"bin"}, kit.NeedAgentBinary, "bin", "kit-agent-binaries"},
		{"Go toolchain", []string{"toolchains"}, kit.NeedGoToolchain, "toolchains", "kit-go-toolchain"},
		{"offline packages", []string{"debs"}, kit.NeedDebs, "debs", "kit-debs-resolute-amd64"},
		{"NVIDIA driver packages", []string{"nvidia"}, kit.NeedNvidia, "nvidia", "kit-nvidia-resolute-amd64-595-open"},
		{"CUDA toolkit", []string{"cuda"}, kit.NeedCuda, "cuda", ""},
		{"kiwix-tools", []string{"tools/kiwix-tools"}, kit.NeedKiwix, "tools", "kit-kiwix-tools"},
		{"pmtiles", []string{"tools/pmtiles"}, kit.NeedPmtiles, "tools", "kit-pmtiles"},
		{"piper", []string{"tools/piper"}, kit.NeedPiper, "tools", "kit-piper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kittest.New(t)
			k.Provision(n)
			for _, r := range tt.remove {
				k.Remove(r)
			}
			rep := run(t, k, facts(n))
			var missing []string
			for _, nd := range rep.Node.Needs {
				if nd.Status != kit.StatusOK && nd.Status != kit.StatusWarning {
					missing = append(missing, nd.Need+":"+nd.Status)
				}
			}
			if strings.Join(missing, ",") != tt.need+":"+kit.StatusMissing {
				t.Fatalf("needs not met: %v (findings %v)", missing, codes(rep))
			}
			got := findCode(rep, doctor.KIT040)
			if len(got) != 1 || got[0].Need != tt.need || got[0].Part != tt.part || got[0].Item != tt.item || rep.Damaged() {
				t.Fatalf("findings %v: %+v", codes(rep), got)
			}
		})
	}
}

// Taking the whole of src/ away leaves the three needs that live there missing, and only those.
func TestVerifyWithoutSrcLacksSourcesAndTheAgentSource(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Remove("src")
	// the mirrors still record the tarballs, which are gone: damaged, and the needs say missing
	rep := run(t, k, facts(n))
	var need []string
	for _, nd := range rep.Node.Needs {
		if nd.Status == kit.StatusMissing {
			need = append(need, nd.Need)
		}
	}
	sort.Strings(need)
	want := []string{kit.NeedAgentSource, kit.NeedLlamaSource, kit.NeedWhisperSource}
	sort.Strings(want)
	if strings.Join(need, ",") != strings.Join(want, ",") {
		t.Fatalf("missing needs %v, want %v (%v)", need, want, codes(rep))
	}
}

// A CPU node needs neither the NVIDIA closure nor a CUDA toolkit, and a node of another architecture
// needs the files of its own.
func TestVerifyNeedsFollowTheBackendAndTheArchitecture(t *testing.T) {
	cpu := kittest.AiN1()
	cpu.Backend = "cpu"
	k := kittest.New(t)
	k.Provision(cpu)
	rep := run(t, k, facts(cpu))
	for _, nd := range rep.Node.Needs {
		if nd.Need == kit.NeedNvidia || nd.Need == kit.NeedCuda {
			t.Errorf("a cpu node needs %s", nd.Need)
		}
	}
	arm := kittest.AiN1()
	arm.Arch = "arm64"
	k2 := kittest.New(t)
	k2.Provision(arm)
	if rep := run(t, k2, facts(arm)); rep.Overall != kit.Green || len(rep.Findings) != 0 {
		t.Fatalf("arm64: %v", codes(rep))
	}
	// An amd64 kit does not serve an arm64 node.
	k3 := kittest.New(t)
	k3.Provision(kittest.AiN1())
	rep = run(t, k3, facts(arm))
	var lacking []string
	for _, f := range findCode(rep, doctor.KIT040) {
		lacking = append(lacking, f.Need)
	}
	sort.Strings(lacking)
	if len(lacking) < 4 || rep.Overall != kit.Red {
		t.Fatalf("overall %s, lacking %v", rep.Overall, lacking)
	}
}
