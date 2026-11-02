package docsrender

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// TestRealChromiumPrintsTheCardOnOnePage runs a real browser. It is opt-in (LOCALAI_TEST_CHROMIUM=1)
// because it needs Chromium in the fixed PATH and takes seconds; the manual test plan of AICTL-08
// and a CI image with Chromium run it. It proves the committed card, with long values, is one page.
func TestRealChromiumPrintsTheCardOnOnePage(t *testing.T) {
	if os.Getenv("LOCALAI_TEST_CHROMIUM") != "1" {
		t.Skip("set LOCALAI_TEST_CHROMIUM=1 to run a real Chromium")
	}
	bin := FindPrinter(execx.Lookup)
	if bin == "" {
		t.Skip("no chromium in the fixed PATH")
	}
	src := repoDocs(t)
	example, err := os.ReadFile(filepath.Join(src, "..", ExampleFile))
	if err != nil {
		t.Skip("inventory.example.yaml is not available")
	}
	local := placeholderQuotedRE.ReplaceAllString(string(example), `"`+strings.Repeat("long-name-", 3)+`$1"`)
	pairs, err := PairInventory(example, []byte(local))
	if err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(CardTemplateRel)))
	if err != nil {
		t.Fatal(err)
	}
	st := NodeState{NodeID: "ai-n3", EmergencyNumber: "911", CatalogVersion: "2026.11.02-1",
		Brains: []string{"Model Alpha 9B", "Model Beta 4B", "Model Gamma 30B", "Model Delta 1B"}}
	html, err := BuildCard(card, pairs, st, cardNow)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"card.html": html, "print.css": []byte(printCSS), "card.css": []byte(cardCSS)}
	pdfs, failed, err := PrintAll(context.Background(), PDFMaker{Runner: execx.Exec{}, Bin: bin}, t.TempDir(), []string{"card.html"}, files)
	if err != nil || len(failed) != 0 {
		t.Fatalf("print: %v %v", err, failed)
	}
	if n := CountPages(pdfs["card.pdf"]); n != 1 {
		t.Errorf("the card is %d pages, want 1", n)
	}
}
