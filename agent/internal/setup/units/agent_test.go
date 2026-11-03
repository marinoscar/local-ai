package units

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The spec block of agent.md section 3.6, extracted: the unit is that text byte for byte (specs and
// code must never disagree).
func specUnitBlock(t *testing.T) []byte {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "specs", "agent.md"))
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
			t.Skipf("the spec is not readable here: %v", err)
		}
		t.Fatal(err)
	}
	head := []byte("### 3.6 `localai-agent.service`")
	start := bytes.Index(spec, head)
	if start < 0 {
		t.Fatal("section 3.6 not found in the spec")
	}
	const open = "```ini\n"
	rest := spec[start:]
	i := bytes.Index(rest, []byte(open))
	if i < 0 {
		t.Fatal("the unit block of section 3.6 was not found")
	}
	block := rest[i+len(open):]
	j := bytes.Index(block, []byte("```"))
	if j < 0 {
		t.Fatal("the unit block of section 3.6 is not closed")
	}
	return block[:j]
}

func TestAgentUnitMatchesTheSpec(t *testing.T) {
	got, err := AgentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if want := specUnitBlock(t); !bytes.Equal(got, want) {
		t.Errorf("localai-agent.service differs from agent.md section 3.6:\n--- unit\n%s\n--- spec\n%s", got, want)
	}
}

// What the rest of the system relies on, checked on the bytes and not only against the spec text.
func TestAgentUnitProperties(t *testing.T) {
	b, err := AgentTemplate()
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	lines := map[string]bool{}
	for _, l := range strings.Split(unit, "\n") {
		lines[l] = true
	}
	for _, want := range []string{
		"Type=notify", "NotifyAccess=main", "WatchdogSec=60", "TimeoutStopSec=30", "Restart=always",
		"ExecStart=/opt/localai/bin/aiagent serve", "RuntimeDirectory=localai",
	} {
		if !lines[want] {
			t.Errorf("the unit lacks %q", want)
		}
	}
	// The agent must start with no network at all, and inference must not depend on the agent:
	// no ordering-with-requirement on the network or tailscale, and no BindsTo or PartOf either way.
	for _, bad := range []string{"Requires=", "Wants=", "BindsTo=", "PartOf=", "network-online"} {
		for _, l := range strings.Split(unit, "\n") {
			if strings.HasPrefix(l, "#") {
				continue
			}
			if strings.Contains(l, bad) {
				t.Errorf("the unit must not contain %q: %s", bad, l)
			}
		}
	}
	// WatchdogSec=60 against the 20 s pinger leaves two missed pings of slack.
	if !strings.Contains(unit, "After=local-fs.target network.target tailscaled.service") {
		t.Error("the unit must order itself after local-fs, network and tailscaled without requiring them")
	}
}
