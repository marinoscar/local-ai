package router

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// iniOf builds the text of a preset file from section bodies, to feed ParseINI.
func iniOf(def string, sections ...string) Sections {
	var b strings.Builder
	b.WriteString("version = 1\n\n[*]\nstop-timeout = 10\nload-on-startup = false\n")
	for _, s := range sections {
		id, body, _ := strings.Cut(s, "|")
		fmt.Fprintf(&b, "\n[%s]\n", id)
		for _, kv := range strings.Split(body, ",") {
			if kv != "" {
				k, v, _ := strings.Cut(kv, "=")
				fmt.Fprintf(&b, "%s = %s\n", k, v)
			}
		}
		if id == def {
			b.WriteString("load-on-startup = true\n")
		}
	}
	return ParseINI([]byte(b.String()))
}

func TestDecideReload(t *testing.T) {
	const (
		a = "a-model|c=1,tags=chat"
		b = "b-model|c=2,tags=chat"
	)
	cases := []struct {
		name       string
		oldDef     string
		old        []string
		newDef     string
		new        []string
		running    []string
		reload     bool
		reason     string
		restore    []string
		dropped    []string
		kept       []string
		added, rem []string
	}{
		{name: "nothing differs", old: []string{a, b}, new: []string{a, b}, running: []string{"a-model"},
			reason: ReasonNoChange, kept: []string{"a-model"}},
		{name: "nothing differs, nothing running", old: []string{a}, new: []string{a}, reason: ReasonNoChange},
		{name: "default change only, the old default is running", oldDef: "a-model", old: []string{a, b}, newDef: "b-model", new: []string{a, b},
			running: []string{"a-model"}, reason: ReasonStartupOnly, kept: []string{"a-model"}},
		{name: "default change only, the new default is running", oldDef: "a-model", old: []string{a, b}, newDef: "b-model", new: []string{a, b},
			running: []string{"b-model"}, reason: ReasonStartupOnly, kept: []string{"b-model"}},
		{name: "default change only, nothing running", oldDef: "a-model", old: []string{a, b}, newDef: "b-model", new: []string{a, b},
			reason: ReasonStartupOnly},
		{name: "default appears where there was none", old: []string{a}, newDef: "a-model", new: []string{a},
			running: []string{"a-model"}, reason: ReasonStartupOnly, kept: []string{"a-model"}},
		{name: "flag change of a running model", old: []string{a, b}, new: []string{"a-model|c=9,tags=chat", b},
			running: []string{"a-model", "b-model"}, reload: true, reason: ReasonRunningChanged,
			restore: []string{"a-model"}, kept: []string{"b-model"}},
		{name: "flag change and default change of a running model", oldDef: "a-model", old: []string{a},
			newDef: "b-model", new: []string{"a-model|c=9,tags=chat", b},
			running: []string{"a-model"}, reload: true, reason: ReasonRunningChanged, restore: []string{"a-model"}, added: []string{"b-model"}},
		{name: "a key added to a running model", old: []string{a}, new: []string{"a-model|c=1,tags=chat,alias=x"},
			running: []string{"a-model"}, reload: true, reason: ReasonRunningChanged, restore: []string{"a-model"}},
		{name: "a key removed from a running model", old: []string{a}, new: []string{"a-model|c=1"},
			running: []string{"a-model"}, reload: true, reason: ReasonRunningChanged, restore: []string{"a-model"}},
		{name: "flag change of a model that is not running", old: []string{a, b}, new: []string{"a-model|c=9,tags=chat", b},
			running: []string{"b-model"}, reload: true, reason: ReasonSections, kept: []string{"b-model"}},
		{name: "section added, nothing running", old: []string{a}, new: []string{a, b}, reload: true,
			reason: ReasonSections, added: []string{"b-model"}},
		{name: "section added while another model runs", old: []string{a}, new: []string{a, b}, running: []string{"a-model"},
			reload: true, reason: ReasonSections, kept: []string{"a-model"}, added: []string{"b-model"}},
		{name: "section removed for a model that is not running", old: []string{a, b}, new: []string{a}, running: []string{"a-model"},
			reload: true, reason: ReasonSections, kept: []string{"a-model"}, rem: []string{"b-model"}},
		{name: "section removed for a running model", old: []string{a, b}, new: []string{a}, running: []string{"b-model"},
			reload: true, reason: ReasonRunningDropped, dropped: []string{"b-model"}, kept: []string{"a-model"}[:0], rem: []string{"b-model"}},
		{name: "added and removed together, neither running", old: []string{a}, new: []string{b}, reload: true,
			reason: ReasonSections, added: []string{"b-model"}, rem: []string{"a-model"}},
		{name: "a running model the old file did not have is treated as changed", old: []string{a}, new: []string{a, b},
			running: []string{"b-model"}, reload: true, reason: ReasonRunningChanged, restore: []string{"b-model"}, added: []string{"b-model"}},
		{name: "empty to something", new: []string{a}, reload: true, reason: ReasonSections, added: []string{"a-model"}},
		{name: "everything removed", old: []string{a, b}, running: []string{"a-model", "b-model"}, reload: true,
			reason: ReasonRunningDropped, dropped: []string{"a-model", "b-model"}, rem: []string{"a-model", "b-model"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var oldS, newS Sections
			if tc.old != nil {
				oldS = iniOf(tc.oldDef, tc.old...)
			}
			if tc.new != nil {
				newS = iniOf(tc.newDef, tc.new...)
			}
			d := DecideReload(oldS, newS, tc.running)
			if d.Reload != tc.reload || d.Reason != tc.reason {
				t.Fatalf("Reload=%v Reason=%q, want %v %q (%+v)", d.Reload, d.Reason, tc.reload, tc.reason, d)
			}
			equalStrings(t, d.Restore, tc.restore)
			equalStrings(t, d.Dropped, tc.dropped)
			equalStrings(t, d.Added, tc.added)
			equalStrings(t, d.Removed, tc.rem)
			if tc.kept != nil || len(d.Kept) > 0 {
				equalStrings(t, d.Kept, tc.kept)
			}
		})
	}
}

// TestDecideReloadGlobalChange: a change of [*] or of the version applies to every model, so a
// running model must be restored, except for load-on-startup of [*], which is startup only.
func TestDecideReloadGlobalChange(t *testing.T) {
	text := func(stop, lab, version string) Sections {
		return ParseINI([]byte("version = " + version + "\n\n[*]\nstop-timeout = " + stop + "\nload-on-startup = " + lab +
			"\n\n[a-model]\nc = 1\n\n[b-model]\nc = 2\n"))
	}
	ref := text("10", "false", "1")
	running := []string{"a-model"}

	d := DecideReload(ref, text("30", "false", "1"), running)
	if !d.Reload || d.Reason != ReasonRunningChanged {
		t.Fatalf("stop-timeout change: %+v", d)
	}
	equalStrings(t, d.Restore, []string{"a-model"})

	d = DecideReload(ref, text("10", "false", "2"), running)
	if !d.Reload || d.Reason != ReasonRunningChanged {
		t.Fatalf("version change: %+v", d)
	}

	d = DecideReload(ref, text("10", "true", "1"), running)
	if d.Reload || d.Reason != ReasonStartupOnly {
		t.Fatalf("[*] load-on-startup change: %+v", d)
	}

	d = DecideReload(ref, text("30", "false", "1"), nil)
	if !d.Reload || d.Reason != ReasonSections || len(d.Restore) != 0 {
		t.Fatalf("global change, nothing running: %+v", d)
	}
}

func TestDecideReloadNilSections(t *testing.T) {
	d := DecideReload(nil, nil, []string{"a-model"})
	if d.Reload || d.Reason != ReasonNoChange {
		t.Fatalf("nil, nil: %+v", d)
	}
}

// TestDecideReloadProperty: over seeded random old/new files and running sets, the rule never
// calls for a reload that only a default change needs, never restores a model that is not
// running, and every running model with a changed preset is restored or dropped.
func TestDecideReloadProperty(t *testing.T) {
	ids := []string{"m-a", "m-b", "m-c", "m-d", "m-e"}
	for seed := int64(1); seed <= 400; seed++ {
		rng := rand.New(rand.NewSource(seed))
		gen := func() (Sections, string) {
			var secs []string
			var present []string
			for _, id := range ids {
				if rng.Intn(3) == 0 {
					continue
				}
				secs = append(secs, fmt.Sprintf("%s|c=%d,tags=chat", id, 1+rng.Intn(3)))
				present = append(present, id)
			}
			def := ""
			if len(present) > 0 && rng.Intn(2) == 0 {
				def = present[rng.Intn(len(present))]
			}
			return iniOf(def, secs...), def
		}
		oldS, _ := gen()
		var newS Sections
		if rng.Intn(3) == 0 { // often: the same file with another default
			_, def := gen()
			var secs []string
			for _, id := range oldS.IDs() {
				secs = append(secs, id+"|c="+oldS[id]["c"]+",tags=chat")
			}
			if len(secs) == 0 {
				def = ""
			} else if def == "" || !strings.Contains(strings.Join(secs, " "), def+"|") {
				def = oldS.IDs()[rng.Intn(len(oldS.IDs()))]
			}
			newS = iniOf(def, secs...)
		} else {
			newS, _ = gen()
		}
		var running []string
		for _, id := range ids {
			if rng.Intn(2) == 0 {
				running = append(running, id)
			}
		}
		d := DecideReload(oldS, newS, running)
		run := map[string]bool{}
		for _, id := range running {
			run[id] = true
		}
		label := fmt.Sprintf("seed %d: old %v new %v running %v -> %+v", seed, oldS, newS, running, d)

		for _, id := range d.Restore {
			if !run[id] {
				t.Fatalf("%s: restored a model that is not running", label)
			}
			if _, ok := newS[id]; !ok {
				t.Fatalf("%s: restored a model that has no section", label)
			}
		}
		for _, id := range d.Dropped {
			if !run[id] {
				t.Fatalf("%s: dropped a model that is not running", label)
			}
			if _, ok := newS[id]; ok {
				t.Fatalf("%s: dropped a model that still has a section", label)
			}
		}
		for _, id := range running {
			o, hadOld := oldS[id]
			n, hasNew := newS[id]
			changed := !hadOld || (hasNew && o["c"] != n["c"])
			switch {
			case hasNew && changed:
				if !contains(d.Restore, id) {
					t.Fatalf("%s: %s changed and runs but is not restored", label, id)
				}
			case !hasNew && hadOld:
				if !contains(d.Dropped, id) {
					t.Fatalf("%s: %s lost its section and runs but is not dropped", label, id)
				}
			case hasNew && !changed:
				if !contains(d.Kept, id) {
					t.Fatalf("%s: %s is unchanged and runs but is not kept", label, id)
				}
			}
		}
		// The sets a reload disturbs are disjoint.
		all := append(append(append([]string(nil), d.Restore...), d.Dropped...), d.Kept...)
		sort.Strings(all)
		for i := 1; i < len(all); i++ {
			if all[i] == all[i-1] {
				t.Fatalf("%s: %s is in two of Restore, Dropped and Kept", label, all[i])
			}
		}
		// A reload is requested iff the files differ in more than load-on-startup.
		differ := len(d.Added)+len(d.Removed) > 0
		for _, id := range newS.IDs() {
			if o, ok := oldS[id]; ok && o["c"] != newS[id]["c"] {
				differ = true
			}
		}
		if d.Reload != differ {
			t.Fatalf("%s: Reload=%v but the files differ=%v", label, d.Reload, differ)
		}
	}
}

func TestParseINI(t *testing.T) {
	in := "; comment\n# other\nversion = 1\n\n[*]\nstop-timeout = 10\n\n[a]\nc = 1\nc = 2\nweird line\n [b] \nmodel = /x = y\n"
	s := ParseINI([]byte(in))
	if s[GlobalSection]["version"] != "1" || s[DefaultsSection]["stop-timeout"] != "10" {
		t.Fatalf("globals: %v", s)
	}
	if s["a"]["c"] != "2" || len(s["a"]) != 1 {
		t.Fatalf("[a]: %v", s["a"])
	}
	if s["b"]["model"] != "/x = y" {
		t.Fatalf("[b]: %v", s["b"])
	}
	equalStrings(t, s.IDs(), []string{"a", "b"})
	if got := ParseINI(nil); len(got) != 1 {
		t.Fatalf("empty input: %v", got)
	}
}
