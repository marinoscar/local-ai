package kit_test

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

// TestVerifyPrebuiltNameCapabilities pins the naming rule of rebuild-kit.md section 4.2 as the
// verifier reads it: the -sm suffix of a CUDA prebuilt lists the compute capabilities of the node
// that exported it, joined with "_" (kit.PrebuiltName), and the verifier finds the file when the list
// is the one of this computer (#263). A node with two different GPUs exports -sm75_86.
func TestVerifyPrebuiltNameCapabilities(t *testing.T) {
	tests := []struct {
		name string
		node string // the node's compute capabilities, as node.yaml lists them
		file string // the suffix of the prebuilt in the kit, after the architecture
		ok   bool
	}{
		{"one GPU, its capability", "75", "-sm75", true},
		{"one GPU, another capability", "75", "-sm86", false},
		{"one GPU, a list that includes it", "75", "-sm75_86", false},
		{"two GPUs, both capabilities", "75;86", "-sm75_86", true},
		{"two GPUs, the same set in another order", "75;86", "-sm86_75", true},
		{"two GPUs, only one of them", "75;86", "-sm75", false},
		{"two GPUs, a different pair", "75;86", "-sm75_89", false},
		{"two GPUs, three-digit capability in the list", "75;120", "-sm75_120", true},
		{"two GPUs, the list written with a semicolon", "75;86", "-sm75;86", false},
		{"two GPUs, an empty element", "75;86", "-sm75_", false},
		{"two GPUs, not a number", "75;86", "-sm75_x6", false},
		{"two GPUs, no suffix", "75;86", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := kittest.AiN3()
			n.CudaArch = tt.node
			k := kittest.New(t)
			k.Provision(n)
			k.Remove("prebuilt")
			name := "llama.cpp-" + n.Llama()[:9] + "-cuda-resolute-amd64" + tt.file + ".tar.gz"
			k.Item("prebuilt", "kit-prebuilt", map[string][]byte{name: []byte("prebuilt\n")})
			rep := run(t, k, facts(n))
			status, code := partStatus(t, rep, "prebuilt")
			if tt.ok {
				if status != kit.StatusOK || code != "" || len(findCode(rep, doctor.KIT040)) != 0 {
					t.Fatalf("%s: prebuilt part %s %s, findings %v", name, status, code, codes(rep))
				}
				return
			}
			if status != kit.StatusWarning || code != doctor.KIT040 {
				t.Fatalf("%s: prebuilt part %s %s, want a warning %s", name, status, code, doctor.KIT040)
			}
		})
	}
}
