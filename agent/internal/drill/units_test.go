package drill

import (
	"os"
	"strings"
	"testing"
)

func TestEmbeddedUnitsPassTheLint(t *testing.T) {
	timer, service, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	if problems := LintUnits(timer, service); len(problems) != 0 {
		t.Fatalf("the embedded drill units have problems: %v", problems)
	}
	if !strings.Contains(string(service), "SuccessExitStatus=8\n") || strings.Contains(string(service), "SuccessExitStatus=3") {
		t.Fatal("the service must have SuccessExitStatus=8 and no SuccessExitStatus=3")
	}
}

func TestLintUnitsFindsWhatIsWrong(t *testing.T) {
	timer, service, err := Templates()
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
		name    string
		timer   []byte
		service []byte
		want    string
	}{
		{"agent not reachable counts as success", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=8 3"), "SuccessExitStatus must be exactly 8"},
		{"only 3", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=3"), "SuccessExitStatus must be exactly 8"},
		{"fail counts as success", timer, swap(service, "SuccessExitStatus=8", "SuccessExitStatus=8 9"), "SuccessExitStatus must be exactly 8"},
		{"no SuccessExitStatus", timer, swap(service, "SuccessExitStatus=8\n", ""), "exactly one SuccessExitStatus"},
		{"two SuccessExitStatus lines", timer, swap(service, "SuccessExitStatus=8\n", "SuccessExitStatus=8\nSuccessExitStatus=3\n"), "exactly one SuccessExitStatus"},
		{"wrong command", timer, swap(service, "drill --scheduled --wait", "drill --wait"), "aictl drill --scheduled --wait"},
		{"not oneshot", timer, swap(service, "Type=oneshot", "Type=simple"), "oneshot"},
		{"does not require the agent", timer, swap(service, "Requires=localai-agent.service", ""), "require localai-agent.service"},
		{"timer not persistent", swap(timer, "Persistent=true", "Persistent=false"), service, "Persistent=true"},
		{"timer on another day", swap(timer, "Sun *-*-01..07", "Mon *-*-*"), service, "OnCalendar"},
		{"timer starts another unit", swap(timer, "Unit=localai-drill.service", "Unit=localai-sync.service"), service, "Unit=localai-drill.service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := LintUnits(tc.timer, tc.service)
			found := false
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("problems = %v, want one mentioning %q", problems, tc.want)
			}
		})
	}
}

// TestUnitsMatchTheSpec: specs and code must never disagree. Every directive line of the unit files
// is a line of the code blocks in drill.md section 3.3.
func TestUnitsMatchTheSpec(t *testing.T) {
	spec, err := os.ReadFile("../../../docs/specs/drill.md")
	if err != nil {
		t.Skipf("drill.md not found: %v", err)
	}
	timer, service, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(string(spec), "\n") {
		lines[strings.TrimRight(l, " ")] = true
	}
	for name, body := range map[string][]byte{TimerUnit: timer, ServiceUnit: service} {
		for _, l := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
			if l != "" && !lines[l] {
				t.Errorf("%s: line %q is not in docs/specs/drill.md", name, l)
			}
		}
	}
}
