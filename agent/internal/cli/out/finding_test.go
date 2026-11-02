package out

import (
	"strings"
	"testing"
)

// Finding is Problem on stdout, for commands whose result is a list of problems (doctor, the alerts
// of status): same layout, hidden by --json, info and warning hidden by --quiet.
func TestFinding(t *testing.T) {
	pr := Problem{Severity: SevWarning, Code: "LAI-HW-012", Message: "Running on battery.",
		Fix: "Plug in the charger. A laptop server stops when the battery runs out."}
	want := "! Running on battery.  [LAI-HW-012]\n" +
		"  → Fix: Plug in the charger. A laptop server stops when the battery runs out.\n"

	t.Run("same layout as Problem, on stdout", func(t *testing.T) {
		p, so, se := newPrinter(variant{"en", false, true}, nil)
		p.Finding(pr)
		if so.String() != want || se.Len() != 0 {
			t.Errorf("stdout %q stderr %q", so.String(), se.String())
		}
		p2, _, se2 := newPrinter(variant{"en", false, true}, nil)
		p2.Problem(pr)
		if se2.String() != want {
			t.Errorf("Problem layout changed: %q", se2.String())
		}
	})
	t.Run("colour follows the printer", func(t *testing.T) {
		p, so, _ := newPrinter(variant{"en", true, true}, nil)
		p.Finding(pr)
		if !strings.HasPrefix(so.String(), "\x1b[33m!\x1b[0m Running on battery.") {
			t.Errorf("stdout %q", so.String())
		}
	})
	t.Run("--json writes nothing", func(t *testing.T) {
		p, so, se := newPrinter(variant{"en", false, true}, func(o *Options) { o.JSON = true })
		p.Finding(pr)
		p.Finding(Problem{Severity: SevError, Message: "Bad."})
		if so.Len() != 0 || se.Len() != 0 {
			t.Errorf("stdout %q stderr %q", so.String(), se.String())
		}
	})
	t.Run("--quiet keeps errors only", func(t *testing.T) {
		p, so, _ := newPrinter(variant{"en", false, true}, func(o *Options) { o.Quiet = true })
		p.Finding(pr)
		p.Finding(Problem{Severity: SevInfo, Message: "Note."})
		if so.Len() != 0 {
			t.Errorf("warning and info must be hidden: %q", so.String())
		}
		p.Finding(Problem{Severity: SevCritical, Code: "LAI-AGT-004", Message: "Cannot write."})
		p.Finding(Problem{Severity: SevError, Message: "Bad."})
		if !strings.Contains(so.String(), "Cannot write.") || !strings.Contains(so.String(), "Bad.") {
			t.Errorf("errors must stay: %q", so.String())
		}
	})
	t.Run("-v adds details", func(t *testing.T) {
		p, so, _ := newPrinter(variant{"en", false, true}, func(o *Options) { o.Verbose = true })
		pr := pr
		pr.Details = map[string]any{"battery_percent": 64}
		p.Finding(pr)
		if !strings.Contains(so.String(), `details: {"battery_percent":64}`) {
			t.Errorf("stdout %q", so.String())
		}
	})
}
