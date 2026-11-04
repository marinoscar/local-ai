package summary

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/setup/inventory"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// A fixture inventory with clearly fake values (RFC 5737 documentation addresses, example.test).
const fixtureInventory = `schema_version: 1
lab:
  owner_email: "admin@example.test"
  tailnet: "tailtest"
  route53_zone_id: "ZTESTZONE0001"
vps:
  public_ip: "192.0.2.10"
nodes:
  - id: ai-n4
    machine: test-box
    tailnet_ip: "198.51.100.4"
    serve_pattern: B
`

func baseInput() Input {
	return Input{
		NodeID: "ai-n4", Profile: "small-8gb", Backend: "cuda", AgentVersion: "0.4.0",
		Brain:        "qwen3.5-9b-q4km (loading in the background)",
		APIKeyMasked: "ab12…ef90",
		Content:      "sync running (186 GB planned)   check: aictl ark sync",
		Cloud:        "enrolled at https://ai.marin.cr",
		User:         "<user>", Machine: "test-box", Pattern: "B",
		Laptop: true, RebootRequired: true,
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenWithInventory(t *testing.T) {
	inv, err := inventory.Parse([]byte(fixtureInventory))
	if err != nil {
		t.Fatal(err)
	}
	in := baseInput()
	in.Inventory = inv
	out := Render(in)
	golden(t, "summary-ai-n4-inventory.txt", out)
	for _, real := range []string{"ZTESTZONE0001", "192.0.2.10", "198.51.100.4", "tailtest", "admin@example.test"} {
		if !strings.Contains(out, real) {
			t.Errorf("inventory value %q missing from the summary", real)
		}
	}
}

func TestGoldenPlaceholders(t *testing.T) {
	out := Render(baseInput())
	golden(t, "summary-ai-n4-placeholders.txt", out)
	for _, ph := range []string{"<ROUTE53_ZONE_ID>", "<VPS_PUBLIC_IP>", "<TAILNET>", "<TAILNET_IP_AI_N4>", "<ADMIN_EMAIL>"} {
		if !strings.Contains(out, ph) {
			t.Errorf("placeholder %s missing", ph)
		}
	}
}

func TestInventoryWithoutThisNodeKeepsNodePlaceholder(t *testing.T) {
	inv, _ := inventory.Parse([]byte(fixtureInventory))
	in := baseInput()
	in.NodeID = "ai-n5"
	in.Inventory = inv
	out := Render(in)
	if !strings.Contains(out, "<TAILNET_IP_AI_N5>") || !strings.Contains(out, "ZTESTZONE0001") {
		t.Errorf("want node placeholder and lab values:\n%s", out)
	}
}

func TestVariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		has    []string
		hasNot []string
	}{
		{"pattern A shows only A", func(i *Input) { i.Pattern = "A" },
			[]string{"Pattern A:  curl -i http://", "copy ai-n1.marin.cr.conf (Pattern A)"}, []string{"Pattern B:", "--resolve"}},
		{"no pattern shows both", func(i *Input) { i.Pattern = "" },
			[]string{"Pattern A:", "Pattern B:"}, nil},
		{"tailnet joined: no join text", func(i *Input) { i.TailnetJoined = true },
			nil, []string{"Join the tailnet", "tailscale up"}},
		{"no-tailscale: no join text", func(i *Input) { i.SkipTailscale = true },
			nil, []string{"Join the tailnet", "tailscale up"}},
		{"desktop: no laptop notes", func(i *Input) { i.Laptop = false },
			nil, []string{"BIOS", "LID TEST", "Ethernet"}},
		{"no reboot, no user", func(i *Input) { i.RebootRequired = false; i.User = "" },
			nil, []string{"REBOOT", "Log out and back in"}},
		{"cloud publishes", func(i *Input) { i.CloudPublishes = true },
			[]string{"The cloud will publish ai-n4.marin.cr automatically."}, []string{"VPS CHECKLIST", "Publish the node on the VPS"}},
	}
	for _, tc := range cases {
		in := baseInput()
		tc.mutate(&in)
		out := Render(in)
		for _, s := range tc.has {
			if !strings.Contains(out, s) {
				t.Errorf("%s: missing %q in\n%s", tc.name, s, out)
			}
		}
		for _, s := range tc.hasNot {
			if strings.Contains(out, s) {
				t.Errorf("%s: unexpected %q in\n%s", tc.name, s, out)
			}
		}
	}
}
