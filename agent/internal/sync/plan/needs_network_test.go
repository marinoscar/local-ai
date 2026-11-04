package plan_test

import (
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// An item that only the network can give is skipped with needs_network when the run may not use it;
// online, and for an import from a drive, the reason stays unresolved.
func TestSkipReasonNeedsNetwork(t *testing.T) {
	cases := []struct {
		name     string
		offline  bool
		imp      bool
		resolved bool // the target version is known (a catalog pin) but there is no local copy
		want     plan.Reason
		note     map[string]string
	}{
		{name: "offline, version unknown", offline: true, want: plan.ReasonNeedsNetwork, note: map[string]string{"en": "needs the network", "es": "necesita la red"}},
		{name: "offline, version pinned, no local copy", offline: true, resolved: true, want: plan.ReasonNeedsNetwork, note: map[string]string{"en": "needs the network", "es": "necesita la red"}},
		{name: "online, version unknown", want: plan.ReasonUnresolved, note: map[string]string{"en": "unresolved", "es": "sin resolver"}},
		{name: "import, version unknown", offline: true, imp: true, want: plan.ReasonUnresolved},
		{name: "import, version pinned, not on the drive", offline: true, imp: true, resolved: true, want: plan.ReasonUnresolved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
			if tc.resolved {
				s.resolve("m", "v1", 10)
			}
			s.in.Offline = tc.offline
			s.in.Import = tc.imp
			p := plan.Plan(s.build())
			got := row(t, p, "m")
			if got.Action != plan.ActionSkip || got.Reason != tc.want {
				t.Fatalf("m = %+v, want skip/%s", got, tc.want)
			}
			for lang, w := range tc.note {
				if txt := plan.Render(p, lang); !strings.Contains(txt, w) {
					t.Errorf("%s text lacks %q:\n%s", lang, w, txt)
				}
			}
		})
	}
}
