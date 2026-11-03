package build

import (
	"slices"
	"strings"
	"testing"
)

// TestLlamaConfigureNamesTheCommit: a source tree from a tarball or `git archive` has no .git, and
// llama.cpp then reports "commit unknown" in `llama-server --version`, which the smoke test (llama-router.md
// section 4.10.3 step 3) refuses. The configure call therefore gives it the full commit, whatever the
// caller asked for (a short sha, the commit of node.yaml), and BUILD.json records the flag. Found by the
// real build of the llama-build workflow.
func TestLlamaConfigureNamesTheCommit(t *testing.T) {
	full := strings.Repeat("ab12cd3", 6)[:40]
	for _, tt := range []struct{ name, asked string }{
		{"the full sha", full}, {"a short sha", full[:7]}, {"nothing", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPlan("/opt/localai/llama.cpp", Inputs{Commit: tt.asked, Backend: BackendCPU, Jobs: 2}, full)
			want := "-DLLAMA_BUILD_COMMIT=" + full
			if !slices.Contains(p.CMakeFlags, want) || !slices.Contains(p.ConfigureArgs, want) {
				t.Errorf("flags %v, argv %v: no %s", p.CMakeFlags, p.ConfigureArgs, want)
			}
		})
	}
	// whisper.cpp has no such flag: its smoke test does not read a commit.
	p := newPlan(WhisperRecipe, "/opt/localai/whisper.cpp", "/var/lib/localai/build", Inputs{Backend: BackendCPU, Jobs: 2}, full)
	for _, f := range p.CMakeFlags {
		if strings.Contains(f, "BUILD_COMMIT") {
			t.Errorf("whisper flag %s", f)
		}
	}
}
