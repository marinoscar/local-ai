package router

import (
	"bytes"
	"sort"
	"strings"
)

// Section names of a parsed preset file: GlobalSection holds the keys before the first section
// (version), DefaultsSection is [*].
const (
	GlobalSection   = ""
	DefaultsSection = "*"
)

// keyLoadOnStartup is the one key whose change never needs the router to re-read the file: it is
// read at router start only.
const keyLoadOnStartup = "load-on-startup"

// Sections is a parsed preset file: section name to key to value.
type Sections map[string]map[string]string

// ParseINI reads a preset file the way the reload rule needs it: sections, keys and values, with
// comment lines (; or #) and blank lines skipped. It never fails: a line that is neither a header
// nor key = value is ignored, so a hand-edited file still gives the sections the router would see.
// A key given twice keeps its last value.
func ParseINI(b []byte) Sections {
	out := Sections{GlobalSection: {}}
	cur := GlobalSection
	for _, line := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		switch {
		case s == "" || s[0] == ';' || s[0] == '#':
		case s[0] == '[' && s[len(s)-1] == ']':
			cur = strings.TrimSpace(s[1 : len(s)-1])
			if out[cur] == nil {
				out[cur] = map[string]string{}
			}
		default:
			if i := strings.IndexByte(s, '='); i > 0 {
				out[cur][strings.TrimSpace(s[:i])] = strings.TrimSpace(s[i+1:])
			}
		}
	}
	return out
}

// IDs returns the model section names (everything except the global keys and [*]), sorted.
func (s Sections) IDs() []string {
	var ids []string
	for name := range s {
		if name != GlobalSection && name != DefaultsSection {
			ids = append(ids, name)
		}
	}
	sort.Strings(ids)
	return ids
}

// differs lists the keys whose value differs between two sections (a key present in only one
// counts), without load-on-startup, and reports whether load-on-startup differs.
func differs(a, b map[string]string) (keys []string, startup bool) {
	for k, va := range a {
		if vb, ok := b[k]; !ok || vb != va {
			if k == keyLoadOnStartup {
				startup = true
			} else {
				keys = append(keys, k)
			}
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			if k == keyLoadOnStartup {
				startup = true
			} else {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys, startup
}

// Why a reload decision came out as it did (ReloadDecision.Reason).
const (
	// ReasonNoChange: the router already has this file.
	ReasonNoChange = "no_change"
	// ReasonStartupOnly: only load-on-startup differs (a default change): the file is right for
	// the next router start and nothing else needs the router to re-read it.
	ReasonStartupOnly = "startup_only"
	// ReasonSections: sections were added, removed or changed for models that are not running.
	ReasonSections = "sections"
	// ReasonRunningChanged: a running model's preset changed; the reload unloads it.
	ReasonRunningChanged = "running_changed"
	// ReasonRunningDropped: a running model's section is gone; the reload unloads it.
	ReasonRunningDropped = "running_dropped"
	// ReasonResync: the router lists other presets than the file has (set by Generate, not by
	// DecideReload).
	ReasonResync = "resync"
)

// ReloadDecision is the answer of the reload-safety rule (llama-router.md section 4.2 step 8).
type ReloadDecision struct {
	// Reload is true when the router must re-read the file (GET /models?reload=1).
	Reload bool
	// Reason is one of the Reason* constants.
	Reason string
	// Restore are the running models whose preset changed. The reload unloads them, so the caller
	// takes the model-operations lock first, emits model.unloaded {preset_changed} for each and
	// loads each again afterwards. Sorted.
	Restore []string
	// Dropped are the running models whose section is gone. The reload unloads them and they are
	// not loaded again (they have no preset); the caller emits model.unloaded {preset_changed}.
	Dropped []string
	// Kept are the running models whose section is unchanged. They must survive the reload; the
	// caller checks it, because load-on-startup differences the router never read may still count
	// as a change on its side.
	Kept []string
	// Added and Removed are the model sections that appeared or disappeared. Sorted.
	Added, Removed []string
}

// DecideReload is the pure reload-safety rule. oldSections is what the router last read (not
// necessarily what is on disk: see Generator), newSections the file just generated, and running
// the ids of the models whose router status is loaded, loading or sleeping.
//
//   - Only load-on-startup differs, or nothing differs: no reload. The file is right for the next
//     router start, and a default change never unloads a running model.
//   - A running model's preset differs in any other key (the global defaults count for every
//     model): reload, and the model is in Restore.
//   - A running model's section was removed: reload; it is in Dropped.
//   - Anything else that differs (sections added, removed or changed for models that are not
//     running): reload, which disturbs nobody.
func DecideReload(oldSections, newSections Sections, running []string) ReloadDecision {
	if oldSections == nil {
		oldSections = Sections{}
	}
	if newSections == nil {
		newSections = Sections{}
	}
	var d ReloadDecision
	run := make(map[string]bool, len(running))
	for _, id := range running {
		run[id] = true
	}

	// The global keys and [*] apply to every model: a change there is a change of every preset.
	globalKeys, _ := differs(oldSections[GlobalSection], newSections[GlobalSection])
	defKeys, _ := differs(oldSections[DefaultsSection], newSections[DefaultsSection])
	globalChange := len(globalKeys)+len(defKeys) > 0

	oldIDs, newIDs := oldSections.IDs(), newSections.IDs()
	for _, id := range newIDs {
		if _, ok := oldSections[id]; !ok {
			d.Added = append(d.Added, id)
		}
	}
	for _, id := range oldIDs {
		if _, ok := newSections[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}

	anyChange := globalChange || len(d.Added)+len(d.Removed) > 0
	startupOnly := false
	if _, s := differs(oldSections[DefaultsSection], newSections[DefaultsSection]); s {
		startupOnly = true
	}
	for _, id := range newIDs {
		old, existed := oldSections[id]
		if !existed {
			continue
		}
		keys, startup := differs(old, newSections[id])
		if startup {
			startupOnly = true
		}
		if len(keys) > 0 || globalChange {
			anyChange = true
			if run[id] {
				d.Restore = append(d.Restore, id)
			}
		} else if run[id] {
			d.Kept = append(d.Kept, id)
		}
	}
	for _, id := range d.Removed {
		if run[id] {
			d.Dropped = append(d.Dropped, id)
		}
	}
	// A running model the router has but the old file did not (a hand edit, a lost reload) cannot
	// be proven unchanged: it is treated as changed.
	for _, id := range d.Added {
		if run[id] {
			d.Restore = append(d.Restore, id)
		}
	}
	sort.Strings(d.Restore)
	sort.Strings(d.Dropped)
	sort.Strings(d.Kept)

	switch {
	case len(d.Restore) > 0:
		d.Reload, d.Reason = true, ReasonRunningChanged
	case len(d.Dropped) > 0:
		d.Reload, d.Reason = true, ReasonRunningDropped
	case anyChange:
		d.Reload, d.Reason = true, ReasonSections
	case startupOnly:
		d.Reason = ReasonStartupOnly
	default:
		d.Reason = ReasonNoChange
	}
	return d
}
