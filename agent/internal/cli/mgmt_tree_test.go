package cli

import (
	"strings"
	"testing"
)

// The hooks run once, and the real tree has every command of AICTL-06 in the groups the other
// issues share.
func TestLateHooksBuildTheRealTree(t *testing.T) {
	reg := mgRegistry()
	mgRegistry() // a second application changes nothing
	want := map[string]string{
		"ark":     "import,sync,verify",
		"catalog": "lint,refresh",
		"cloud":   "enroll,resend-key,unenroll",
		"profile": "set",
		"key":     "copy,rotate,show,snippets",
	}
	for group, subs := range want {
		g, ok := reg.Lookup(group)
		if !ok {
			t.Fatalf("no %s group", group)
		}
		have := map[string]bool{}
		for _, s := range g.Sub {
			have[s.Name] = true
		}
		for _, s := range strings.Split(subs, ",") {
			if !have[s] {
				t.Errorf("%s has no %q (has %s)", group, s, subNames(g))
			}
		}
	}
	for _, top := range []string{"mode", "ui-code"} {
		if _, ok := reg.Lookup(top); !ok {
			t.Errorf("no %s command", top)
		}
	}
	ark, _ := reg.Lookup("ark")
	drives := findSub(ark, "drives")
	if drives == nil || findSub(drives, "set-profile") == nil {
		t.Errorf("ark drives set-profile is missing")
	}
	// The marks are what help shows.
	for path, mark := range map[string]Marks{"key rotate": MarkAdmin, "cloud enroll": MarkAdmin, "profile set": MarkAdmin} {
		res, _, err := reg.resolve(strings.Fields(path))
		if err != nil || res.cmd.Marks != mark {
			t.Errorf("%s: marks %v, err %v", path, res.cmd.Marks, err)
		}
	}
}
