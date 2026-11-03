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

// specUnitBlocks returns the two ini blocks of ark-sync.md section 3.7 (timer first): the units are
// those texts byte for byte (specs and code must never disagree).
func specUnitBlocks(t *testing.T) (timer, service []byte) {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "specs", "ark-sync.md"))
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
			t.Skipf("the spec is not readable here: %v", err)
		}
		t.Fatal(err)
	}
	start := bytes.Index(spec, []byte("### 3.7 systemd units"))
	if start < 0 {
		t.Fatal("section 3.7 not found in the spec")
	}
	rest := spec[start:]
	if end := bytes.Index(rest[5:], []byte("\n## ")); end >= 0 {
		rest = rest[:end+5]
	}
	var blocks [][]byte
	const open = "```ini\n"
	for {
		i := bytes.Index(rest, []byte(open))
		if i < 0 {
			break
		}
		rest = rest[i+len(open):]
		j := bytes.Index(rest, []byte("```"))
		if j < 0 {
			t.Fatal("an ini block of section 3.7 is not closed")
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j+3:]
	}
	if len(blocks) != 2 {
		t.Fatalf("section 3.7 has %d ini blocks, want 2 (timer, service)", len(blocks))
	}
	return blocks[0], blocks[1]
}

func TestSyncUnitsMatchTheSpec(t *testing.T) {
	timer, service, err := SyncTemplates()
	if err != nil {
		t.Fatal(err)
	}
	wantTimer, wantService := specUnitBlocks(t)
	if !bytes.Equal(timer, wantTimer) {
		t.Errorf("localai-sync.timer differs from ark-sync.md section 3.7:\n--- unit\n%s\n--- spec\n%s", timer, wantTimer)
	}
	if !bytes.Equal(service, wantService) {
		t.Errorf("localai-sync.service differs from ark-sync.md section 3.7:\n--- unit\n%s\n--- spec\n%s", service, wantService)
	}
}

func TestEmbeddedSyncUnitsPassTheLint(t *testing.T) {
	timer, service, err := SyncTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if problems := LintSync(timer, service); len(problems) != 0 {
		t.Fatalf("the embedded sync units have problems: %v", problems)
	}
	if !strings.Contains(string(service), "SuccessExitStatus=8\n") || strings.Contains(string(service), "SuccessExitStatus=3") {
		t.Fatal("the service must have SuccessExitStatus=8 and no SuccessExitStatus=3")
	}
}

// The drill service orders itself after the sync service, so the drill never starts in the middle of
// the nightly sync window: the pair must keep the name the drill refers to.
func TestSyncServiceNameIsWhatTheDrillOrdersAfter(t *testing.T) {
	drill, err := Drill.ReadFile("localai-drill.service")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(drill), "After=localai-agent.service "+SyncServiceUnit) {
		t.Fatalf("the drill service no longer orders itself after %s", SyncServiceUnit)
	}
}

func TestLintSyncFindsWhatIsWrong(t *testing.T) {
	timer, service, err := SyncTemplates()
	if err != nil {
		t.Fatal(err)
	}
	swap := func(b []byte, old, new string) []byte {
		if !strings.Contains(string(b), old) {
			t.Fatalf("fixture has no %q", old)
		}
		return []byte(strings.Replace(string(b), old, new, 1))
	}
	for _, tc := range []struct {
		name           string
		timer, service []byte
		want           string
	}{
		{"agent not reachable counts as success", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=8 3"), "SuccessExitStatus must be exactly 8"},
		{"only 3", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=3"), "SuccessExitStatus must be exactly 8"},
		{"errors count as success", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=8 9"), "SuccessExitStatus must be exactly 8"},
		{"no SuccessExitStatus", timer, swap(service, "SuccessExitStatus=8\n", ""), "exactly one SuccessExitStatus"},
		{"two SuccessExitStatus lines", timer, swap(service, "SuccessExitStatus=8\n", "SuccessExitStatus=8\nSuccessExitStatus=3\n"), "exactly one SuccessExitStatus"},
		{"not scheduled", timer, swap(service, "ark sync --scheduled --wait", "ark sync --wait"), "ark sync --scheduled --wait"},
		{"not oneshot", timer, swap(service, "Type=oneshot", "Type=simple"), "oneshot"},
		{"does not require the agent", timer, swap(service, "Requires=localai-agent.service", ""), "Requires=localai-agent.service"},
		{"kills the client at once", timer, swap(service, "KillMode=mixed", "KillMode=control-group"), "KillMode=mixed"},
		{"timer not persistent", swap(timer, "Persistent=true", "Persistent=false"), service, "Persistent=true"},
		{"timer at another time", swap(timer, "03:15:00", "05:00:00"), service, "OnCalendar"},
		{"timer without the random delay", swap(timer, "RandomizedDelaySec=45min\n", ""), service, "RandomizedDelaySec"},
		{"timer starts another unit", swap(timer, "Unit=localai-sync.service", "Unit=localai-drill.service"), service, "Unit=localai-sync.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := LintSync(tc.timer, tc.service)
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Fatalf("problems = %v, want one mentioning %q", problems, tc.want)
		})
	}
}
