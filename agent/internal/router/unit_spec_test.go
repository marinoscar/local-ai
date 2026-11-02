package router

import (
	"os"
	"strings"
	"testing"
)

// specBlock returns the first ```ini block that follows the heading in docs/specs/llama-router.md.
func specBlock(t *testing.T, heading string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/specs/llama-router.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, heading)
	if i < 0 {
		t.Fatalf("heading %q not in the spec", heading)
	}
	s = s[i:]
	start := strings.Index(s, "```ini\n")
	if start < 0 {
		t.Fatalf("no ini block after %q", heading)
	}
	s = s[start+len("```ini\n"):]
	return s[:strings.Index(s, "```")]
}

// TestUnitMatchesTheSpec: the examples of sections 3.2 and 3.4 are what the generator renders for
// ai-n3 (the <value> placeholders of 3.2 filled with models_max 1 and margin 512). Specs and code
// must never disagree on main.
func TestUnitMatchesTheSpec(t *testing.T) {
	spec := specBlock(t, "### 3.2 `localai-llama.service` (router mode)")
	spec = strings.NewReplacer("<1>", "1", "<512>", "512").Replace(spec)
	if got := mustRender(t, UnitInputs{Router: routerBlock(1, 512)}); got != spec {
		t.Fatalf("the router unit differs from section 3.2.\n--- got ---\n%s\n--- spec ---\n%s", got, spec)
	}
	single := specBlock(t, "### 3.4 `localai-llama.service` (`single` fallback mode)")
	got := mustRender(t, UnitInputs{Mode: LlamaModeSingle, Single: singleN3()})
	if !strings.Contains(got, strings.TrimSuffix(single, "\n")+"\n") {
		t.Fatalf("the single ExecStart differs from section 3.4.\n--- got ---\n%s\n--- spec ---\n%s", got, single)
	}
}
