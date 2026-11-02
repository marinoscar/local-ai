package docsrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var placeholderQuotedRE = regexp.MustCompile(`"<([A-Z][A-Z0-9_]{1,63})>"`)

// The committed survival card template must render for every node of the committed example inventory,
// with no placeholder left and no secret placeholder in it (survival-portal.md 4.12). The local
// inventory here is the example with every placeholder replaced by an invented value.
func TestRealSurvivalCardRendersForEveryNode(t *testing.T) {
	src := repoDocs(t)
	example, err := os.ReadFile(filepath.Join(src, "..", ExampleFile))
	if err != nil {
		t.Skip("inventory.example.yaml is not available")
	}
	local := placeholderQuotedRE.ReplaceAllString(string(example), `"fake-$1"`)
	pairs, err := PairInventory(example, []byte(local))
	if err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(CardTemplateRel)))
	if err != nil {
		t.Fatal(err)
	}
	if secret := SecretPlaceholders(card, pairs); len(secret) != 0 {
		t.Fatalf("the committed card template holds secret placeholders: %v", secret)
	}
	for _, node := range []string{"ai-n1", "ai-n2", "ai-n3"} {
		st := NodeState{NodeID: node, EmergencyNumber: "911", CatalogVersion: "2026.11.02-1", Brains: []string{"Model A"}}
		html, err := BuildCard(card, pairs, st, cardNow)
		if err != nil {
			t.Fatalf("%s: %v", node, err)
		}
		s := string(html)
		if strings.Contains(s, "&lt;") {
			t.Errorf("%s: a placeholder is left on the card", node)
		}
		if !strings.Contains(s, "fake-DESKTOP_USER") || !strings.Contains(s, "Computer / Computadora <strong>"+node+"</strong>") {
			t.Errorf("%s: node id or desktop user missing", node)
		}
		assertSelfContained(t, node, html)
	}
}
