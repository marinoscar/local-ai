package ready_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// TestStatusColour is every row of the status table of ark-sync.md section 4.12.2, in every column.
func TestStatusColour(t *testing.T) {
	type col struct {
		name     string
		tier     int
		survival bool
		kind     string
	}
	strict := []col{
		{"tier 1", 1, false, "kiwix"},
		{"survival model of tier 3", 3, true, "model"},
		{"kit of tier 3", 3, false, "kit"},
		{"docs of tier 2", 2, false, "docs"},
	}
	tier2 := []col{{"tier 2", 2, false, "kiwix"}}
	tier3 := []col{{"tier 3", 3, false, "maps"}}
	tests := []struct {
		status               ready.Status
		strict, tier2, tier3 ready.Colour
	}{
		{ready.StatusVerified, ready.Green, ready.Green, ready.Green},
		{ready.StatusStale, ready.Yellow, ready.Yellow, ready.Green},
		{ready.StatusMissing, ready.Red, ready.Yellow, ready.Yellow},
		{ready.StatusFailed, ready.Red, ready.Yellow, ready.Yellow},
		{ready.StatusQuarantined, ready.Red, ready.Yellow, ready.Yellow},
		{ready.StatusSkipped, ready.Red, ready.Yellow, ready.Green},
		{ready.StatusOrphan, ready.Green, ready.Green, ready.Green},
	}
	for _, tt := range tests {
		for _, group := range []struct {
			cols []col
			want ready.Colour
		}{{strict, tt.strict}, {tier2, tt.tier2}, {tier3, tt.tier3}} {
			for _, c := range group.cols {
				t.Run(string(tt.status)+"/"+c.name, func(t *testing.T) {
					if got := ready.StatusColour(tt.status, c.tier, c.survival, c.kind); got != group.want {
						t.Errorf("colour = %s, want %s", got, group.want)
					}
				})
			}
		}
	}
	if got := ready.StatusColour(ready.Status("bogus"), 3, false, "maps"); got != ready.Red {
		t.Errorf("an unknown status is %s, want red", got)
	}
	if got := ready.StatusColour(ready.StatusMissing, 0, false, "maps"); got != ready.Yellow {
		t.Errorf("an unknown tier is judged as tier 2: %s", got)
	}
}

// TestFreshness is the freshness table at both sides of every boundary.
func TestFreshness(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		closure bool
		age     int
		want    ready.Colour
	}{
		{"model at 180", "model", false, 180, ready.Green},
		{"model at 181", "model", false, 181, ready.Yellow},
		{"model never red by age", "model", false, 5000, ready.Yellow},
		{"kiwix at 180", "kiwix", false, 180, ready.Green},
		{"kiwix at 181", "kiwix", false, 181, ready.Yellow},
		{"maps at 365", "maps", false, 365, ready.Green},
		{"maps at 366", "maps", false, 366, ready.Yellow},
		{"closure at 60", "kit", true, 60, ready.Green},
		{"closure at 61", "kit", true, 61, ready.Yellow},
		{"other kit at 90", "kit", false, 90, ready.Green},
		{"other kit at 91", "kit", false, 91, ready.Yellow},
		{"unknown kind at 90", "other", false, 90, ready.Green},
		{"unknown kind at 91", "other", false, 91, ready.Yellow},
		{"fresh", "model", false, 0, ready.Green},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ready.Freshness(tt.kind, tt.closure, tt.age); got != tt.want {
				t.Errorf("Freshness(%s, %v, %d) = %s, want %s", tt.kind, tt.closure, tt.age, got, tt.want)
			}
		})
	}
}

// TestFreshnessInReport: the boundary through Build, so the age is the one the report computes.
func TestFreshnessInReport(t *testing.T) {
	for _, tt := range []struct {
		age  time.Duration
		want ready.Colour
	}{{180 * day, ready.Green}, {181 * day, ready.Yellow}} {
		w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", tt.age).synced(day)
		r := w.build()
		if it := item(t, r, "m"); it.Freshness != tt.want {
			t.Errorf("age %v: freshness %s, want %s", tt.age, it.Freshness, tt.want)
		}
		// Re-verified recently, so the only yellow is the age.
		w.reverified("m", day)
		r = w.build()
		if r.Overall != tt.want {
			t.Errorf("age %v: overall %s, want %s", tt.age, r.Overall, tt.want)
		}
	}
}

// TestFreshnessFollowsCheckedCurrent: a recent confirmation makes an old copy fresh.
func TestFreshnessFollowsCheckedCurrent(t *testing.T) {
	w := newWorld().withContent(kiwix("z", 1, 40)).have("z", "v1", 400*day).reverified("z", day).checked("z", 2*day)
	r := w.build()
	it := item(t, r, "z")
	if it.Freshness != ready.Green || it.AgeDays == nil || *it.AgeDays != 2 {
		t.Errorf("item = %+v", it)
	}
}

// TestDriveAge: drive age 30 days is fine, 31 is yellow, 90 is yellow, 91 is red (LAI-ARK-008).
func TestDriveAge(t *testing.T) {
	tests := []struct {
		age      time.Duration
		want     ready.Colour
		problem  bool
		severity doctor.Severity
	}{
		{30 * day, ready.Green, false, ""},
		{31 * day, ready.Yellow, true, doctor.Warning},
		{90 * day, ready.Yellow, true, doctor.Warning},
		{91 * day, ready.Red, true, doctor.Error},
		{400 * day, ready.Red, true, doctor.Error},
	}
	for _, tt := range tests {
		t.Run(tt.age.String(), func(t *testing.T) {
			w := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", 0).synced(tt.age)
			r := w.build()
			if r.Overall != tt.want {
				t.Errorf("overall = %s, want %s", r.Overall, tt.want)
			}
			if got := r.HasProblem(doctor.ARK008); got != tt.problem {
				t.Fatalf("LAI-ARK-008 raised = %v, want %v (%v)", got, tt.problem, problemCodes(r))
			}
			if tt.problem && r.Problems[0].Severity != tt.severity {
				t.Errorf("severity = %s, want %s", r.Problems[0].Severity, tt.severity)
			}
		})
	}
}

// TestInternalSyncAge: the internal root is yellow after 14 days without a sync, but only when the
// node was online most of that time (LAI-SYN-004); the drive rule does not apply to it and the
// internal rule does not apply to a drive.
func TestInternalSyncAge(t *testing.T) {
	tests := []struct {
		name    string
		drive   bool
		age     time.Duration
		offline bool
		want    string
	}{
		{"14 days", false, 14 * day, false, ""},
		{"15 days online", false, 15 * day, false, doctor.SYN004},
		{"15 days offline most of the time", false, 15 * day, true, ""},
		{"200 days online is still yellow, never red by this rule", false, 200 * day, false, doctor.SYN004},
		{"a drive 15 days old is fine", true, 15 * day, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", 0).synced(tt.age)
			if tt.drive {
				w.drive()
			}
			w.in.OfflineMostly = tt.offline
			r := w.build()
			var got string
			for _, p := range r.Problems {
				if p.Code == doctor.SYN004 {
					got = p.Code
				}
			}
			if got != tt.want {
				t.Errorf("SYN-004 = %q, want %q (%v)", got, tt.want, problemCodes(r))
			}
			if tt.want != "" && r.Overall != ready.Yellow {
				t.Errorf("overall = %s", r.Overall)
			}
			if r.HasProblem(doctor.ARK008) {
				t.Errorf("the drive rule must not apply to %v", tt.drive)
			}
		})
	}
}

// TestSkippedRunDoesNotResetAge: a run that was skipped (no internet) is the last_sync but did not
// bring the root up to date, so the age rules still count from the last run that finished done.
func TestSkippedRunDoesNotResetAge(t *testing.T) {
	w := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", 0)
	w.in.LastSync = &ready.RunInfo{RunID: "run-skip", Trigger: "scheduled", FinishedAt: now.Add(-day), State: "skipped"}
	w.in.LastDoneAt = now.Add(-100 * day)
	r := w.build()
	if r.Overall != ready.Red || !r.HasProblem(doctor.ARK008) {
		t.Errorf("overall %s problems %v", r.Overall, problemCodes(r))
	}
	if r.LastSync == nil || r.LastSync.AgeDays != 1 || r.LastSync.State != "skipped" {
		t.Errorf("last_sync still shows the skipped run: %+v", r.LastSync)
	}
}

// TestSyncAgeWithoutRunRecord: with no run record the newest evidence on the root stands in; with
// nothing on the root there is no age to judge.
func TestSyncAgeWithoutRunRecord(t *testing.T) {
	w := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", 45*day)
	r := w.build()
	if !r.HasProblem(doctor.ARK008) || r.Overall != ready.Yellow && r.Overall != ready.Red {
		t.Errorf("problems %v overall %s", problemCodes(r), r.Overall)
	}
	// checked_current_at is newer evidence than verified_at.
	w.checked("m", 3*day)
	if r = w.build(); r.HasProblem(doctor.ARK008) {
		t.Errorf("a recent checked_current_at is evidence of a recent sync: %v", problemCodes(r))
	}
	// An empty root has no date: only the status rules speak.
	empty := newWorld().drive().withModels(model("m", 1, 40))
	if r = empty.build(); r.HasProblem(doctor.ARK008) {
		t.Errorf("an empty root has no age: %v", problemCodes(r))
	}
	// A last_sync that ended done is the reference.
	done := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", 200*day)
	done.in.LastSync = &ready.RunInfo{RunID: "r", Trigger: "manual", FinishedAt: now.Add(-day), State: "done"}
	if r = done.build(); r.HasProblem(doctor.ARK008) {
		t.Errorf("a done run a day ago: %v", problemCodes(r))
	}
}

// TestNotRechecked: any copy not re-verified in 2 x reverify_days is yellow (LAI-SYN-025), naming
// the most overdue one.
func TestNotRechecked(t *testing.T) {
	tests := []struct {
		name   string
		days   int
		age    time.Duration
		item   string
		raised bool
	}{
		{"at the limit (60 d)", 0, 60 * day, "", false},
		{"one day over", 0, 61 * day, "m", true},
		{"a custom reverify_days of 10: 20 d is the limit", 10, 20 * day, "", false},
		{"a custom reverify_days of 10: 21 d is over", 10, 21 * day, "m", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withModels(model("m", 1, 40), model("n", 1, 40)).have("m", "v1", 0).have("n", "v1", 0).reverified("m", tt.age).reverified("n", day)
			w.in.ReverifyDays = tt.days
			w.synced(day)
			r := w.build()
			if got := r.HasProblem(doctor.SYN025); got != tt.raised {
				t.Fatalf("raised = %v, want %v (%v)", got, tt.raised, problemCodes(r))
			}
			if tt.raised {
				for _, p := range r.Problems {
					if p.Code == doctor.SYN025 && p.Item != tt.item {
						t.Errorf("item = %s, want %s", p.Item, tt.item)
					}
				}
				if r.Overall != ready.Yellow {
					t.Errorf("overall = %s", r.Overall)
				}
			}
		})
	}
	// The most overdue of several is the one named, ties by id.
	w := newWorld().withModels(model("a", 1, 40), model("b", 1, 40), model("c", 1, 40)).synced(day)
	w.have("a", "v1", 0).have("b", "v1", 0).have("c", "v1", 0).reverified("a", 70*day).reverified("b", 90*day).reverified("c", 90*day)
	r := w.build()
	for _, p := range r.Problems {
		if p.Code == doctor.SYN025 && p.Item != "b" {
			t.Errorf("item = %s, want b", p.Item)
		}
	}
}

// TestFreeSpace: free space below the root's minimum is yellow (LAI-SYN-040); equal is fine.
func TestFreeSpace(t *testing.T) {
	for _, tt := range []struct {
		name          string
		free, minFree int64
		want          bool
	}{{"above", 11, 10, false}, {"equal", 10, 10, false}, {"below", 9, 10, true}, {"no minimum", 0, 0, false}} {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", 0).synced(day)
			w.free, w.minFree = tt.free, tt.minFree
			r := w.build()
			if got := r.HasProblem(doctor.SYN040); got != tt.want {
				t.Errorf("raised = %v, want %v", got, tt.want)
			}
			if tt.want && r.Overall != ready.Yellow {
				t.Errorf("overall = %s", r.Overall)
			}
		})
	}
}

// TestUnclean: an unclean drive is red whatever its content looks like (LAI-ARK-004).
func TestUnclean(t *testing.T) {
	w := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", 0).synced(day)
	in := w.input()
	if ready.Build(in).Overall != ready.Green {
		t.Fatal("the fixture is meant to be green without the unclean state")
	}
	in.Root.State = ark.StateUnclean
	r := ready.Build(in)
	if r.Overall != ready.Red || !r.HasProblem(doctor.ARK004) {
		t.Errorf("overall %s problems %v", r.Overall, problemCodes(r))
	}
	if p := r.Problems[0]; p.Severity != doctor.Error {
		t.Errorf("severity = %s", p.Severity)
	}
}

// TestKitFindings: the kit verify report, by code and by root kind.
func TestKitFindings(t *testing.T) {
	tests := []struct {
		name     string
		drive    bool
		findings []ready.KitFinding
		want     ready.Colour
		codes    []string
	}{
		{"none", false, nil, ready.Green, nil},
		{"040 on the internal root is red", false, []ready.KitFinding{{Code: doctor.KIT040}}, ready.Red, []string{doctor.KIT040}},
		{"040 on a drive is yellow", true, []ready.KitFinding{{Code: doctor.KIT040}}, ready.Yellow, []string{doctor.KIT040}},
		{"041 is yellow", true, []ready.KitFinding{{Code: doctor.KIT041, Item: "kit-debs-resolute-amd64"}}, ready.Yellow, []string{doctor.KIT041}},
		{"042 on the internal root is red", false, []ready.KitFinding{{Code: doctor.KIT042}}, ready.Red, []string{doctor.KIT042}},
		{"042 on a drive is yellow", true, []ready.KitFinding{{Code: doctor.KIT042}}, ready.Yellow, []string{doctor.KIT042}},
		{"043 is yellow", false, []ready.KitFinding{{Code: doctor.KIT043}}, ready.Yellow, []string{doctor.KIT043}},
		{"the worst wins and red comes first", false, []ready.KitFinding{{Code: doctor.KIT043}, {Code: doctor.KIT042}}, ready.Red, []string{doctor.KIT042, doctor.KIT043}},
		{"a code that is not a kit finding is ignored", false, []ready.KitFinding{{Code: doctor.SYN031}}, ready.Green, nil},
		{"a repeated finding is reported once", false, []ready.KitFinding{{Code: doctor.KIT043}, {Code: doctor.KIT043}}, ready.Yellow, []string{doctor.KIT043}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withContent(kit("kit-a", 40)).have("kit-a", "v1", day).synced(day)
			if tt.drive {
				w.drive()
			}
			w.in.Kit = ready.KitReport{Findings: tt.findings}
			r := w.build()
			if r.Overall != tt.want {
				t.Errorf("overall = %s, want %s", r.Overall, tt.want)
			}
			if got := problemCodes(r); !reflect.DeepEqual(got, tt.codes) && !(len(got) == 0 && len(tt.codes) == 0) {
				t.Errorf("problems = %v, want %v", got, tt.codes)
			}
		})
	}
}

// TestKitAgeAndClosure: the kit rows combine age with the report.
func TestKitAgeAndClosure(t *testing.T) {
	w := newWorld().withContent(closure("resolute", "amd64", 40), kit("kit-llama", 40)).synced(day)
	w.have("kit-debs-resolute-amd64", "v1", 61*day).reverified("kit-debs-resolute-amd64", day)
	w.have("kit-llama", "v1", 61*day).reverified("kit-llama", day)
	r := w.build()
	if item(t, r, "kit-debs-resolute-amd64").Freshness != ready.Yellow {
		t.Errorf("a closure at 61 days is yellow")
	}
	if item(t, r, "kit-llama").Freshness != ready.Green {
		t.Errorf("another kit item at 61 days is green")
	}
	if r.Overall != ready.Yellow {
		t.Errorf("overall = %s", r.Overall)
	}
}

// TestUnverifiedAptRepositoriesAreYellow: a kit closure whose manifest names repositories taken without
// a checked vendor signature (source.unverified_repos) is never green, with LAI-SYN-063, and it stays
// "verified" (survival capability and status are untouched); one without the field is green.
func TestUnverifiedAptRepositoriesAreYellow(t *testing.T) {
	const id = "kit-debs-resolute-amd64"
	tests := []struct {
		name  string
		repos any
		want  ready.Colour
		codes []string
	}{
		{"no field", nil, ready.Green, nil},
		{"an empty list", []string{}, ready.Green, nil},
		{"one repository", []string{"nvidia-cuda"}, ready.Yellow, []string{doctor.SYN063}},
		{"two repositories", []string{"nvidia-cuda", "tailscale"}, ready.Yellow, []string{doctor.SYN063}},
		{"a field of the wrong shape is ignored", "nvidia-cuda", ready.Green, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withContent(closure("resolute", "amd64", 40)).synced(day)
			w.have(id, "v1", day)
			if tt.repos != nil {
				m := w.present[id]
				var err error
				if m.Source, err = m.Source.With(manifest.SourceUnverifiedRepos, tt.repos); err != nil {
					t.Fatal(err)
				}
				w.present[id] = m
			}
			r := w.build()
			it := item(t, r, id)
			if it.Status != ready.StatusVerified {
				t.Errorf("status = %s: an unverified repository must not unverify the copy", it.Status)
			}
			if r.Overall != tt.want {
				t.Errorf("overall = %s, want %s", r.Overall, tt.want)
			}
			if got := problemCodes(r); !reflect.DeepEqual(got, tt.codes) && !(len(got) == 0 && len(tt.codes) == 0) {
				t.Errorf("problems = %v, want %v", got, tt.codes)
			}
			if len(tt.codes) == 1 && (r.Problems[0].Item != id || it.Code != doctor.SYN063) {
				t.Errorf("problem = %+v, item code %q", r.Problems[0], it.Code)
			}
		})
	}
}

// TestDocsRow is the docs row: current and complete is green; an older catalog, placeholders or a
// missing card PDF are yellow with their codes; missing pages are red; unknown facts add nothing.
func TestDocsRow(t *testing.T) {
	tests := []struct {
		name     string
		facts    ready.DocsFacts
		oldCat   bool
		want     ready.Colour
		code     string
		freshnes ready.Colour
	}{
		{"current and complete", ready.DocsFacts{Known: true}, false, ready.Green, "", ready.Green},
		{"facts unknown", ready.DocsFacts{}, false, ready.Green, "", ready.Green},
		{"unknown facts do not hide an older catalog", ready.DocsFacts{}, true, ready.Yellow, "", ready.Yellow},
		{"older catalog", ready.DocsFacts{Known: true}, true, ready.Yellow, "", ready.Yellow},
		{"placeholders", ready.DocsFacts{Known: true, Placeholders: true}, false, ready.Yellow, doctor.SYN062, ready.Yellow},
		{"no survival-card PDF", ready.DocsFacts{Known: true, CardPDFMissing: true}, false, ready.Yellow, doctor.CLI014, ready.Yellow},
		{"placeholders and no card: the code of the first", ready.DocsFacts{Known: true, Placeholders: true, CardPDFMissing: true}, false, ready.Yellow, doctor.SYN062, ready.Yellow},
		{"missing pages", ready.DocsFacts{Known: true, HTMLMissing: true}, false, ready.Red, doctor.SYN052, ready.Red},
		{"missing pages beat everything", ready.DocsFacts{Known: true, HTMLMissing: true, Placeholders: true}, true, ready.Red, doctor.SYN052, ready.Red},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld().withContent(docs(40)).have("docs-bundle", "v1", day).synced(day)
			if tt.oldCat {
				m := w.present["docs-bundle"]
				m.CatalogVersion = "2026.01.01-1"
				w.present["docs-bundle"] = m
			}
			w.in.Docs = tt.facts
			r := w.build()
			it := item(t, r, "docs-bundle")
			if r.Overall != tt.want || it.Code != tt.code || it.Freshness != tt.freshnes {
				t.Errorf("overall %s code %q freshness %s, want %s %q %s", r.Overall, it.Code, it.Freshness, tt.want, tt.code, tt.freshnes)
			}
			if tt.code != "" && !r.HasProblem(tt.code) {
				t.Errorf("problems = %v, want %s", problemCodes(r), tt.code)
			}
		})
	}
}

// TestOverallAggregation: overall is the worst colour of any item or root rule, and problems are
// listed worst first.
func TestOverallAggregation(t *testing.T) {
	w := newWorld().withModels(model("a", 1, 40), model("z", 2, 40)).withContent(kiwix("y", 3, 40)).synced(day)
	w.have("a", "v1", day).have("z", "v1", day).have("y", "v1", day)
	if r := w.build(); r.Overall != ready.Green {
		t.Fatalf("fixture overall = %s, want green", r.Overall)
	}
	// One yellow item (stale tier 2) makes it yellow.
	w.have("z", "v0", day).target("z", "v1", 40)
	w.in.Docs = ready.DocsFacts{}
	if r := w.build(); r.Overall != ready.Yellow {
		t.Errorf("a stale tier-2 item: overall = %s, want yellow", r.Overall)
	}
	// A stale tier-3 item alone is green.
	w2 := newWorld().withModels(model("a", 1, 40), model("y", 3, 40)).synced(day)
	w2.have("a", "v1", day).have("y", "v0", day).target("y", "v1", 40)
	if r := w2.build(); item(t, r, "y").Status != ready.StatusStale || r.Overall != ready.Green {
		t.Errorf("a stale tier-3 item: overall = %s, want green", r.Overall)
	}
	// A red item makes it red even with yellow rules around, and red problems come first.
	w3 := newWorld().withModels(model("a", 1, 40)).withContent(kiwix("z", 2, 40)).synced(day)
	w3.have("z", "v1", day).failedItem("a", "failed", "v1", doctor.SYN031)
	w3.free, w3.minFree = 3, 10
	r := w3.build()
	if r.Overall != ready.Red {
		t.Errorf("a missing tier-1 item: overall = %s, want red", r.Overall)
	}
	if len(r.Problems) < 2 || r.Problems[0].Code != doctor.SYN031 {
		t.Errorf("problems = %v: the red one first", problemCodes(r))
	}
}

// TestMissingTier1WithoutCode: a missing tier-1 item with nothing to explain it is red and raises no
// problem of its own (the item carries the status; there is no doctor code for "not downloaded yet").
func TestMissingTier1WithoutCode(t *testing.T) {
	w := newWorld().withModels(model("a", 1, 40)).synced(day)
	r := w.build()
	if r.Overall != ready.Red || len(r.Problems) != 0 {
		t.Errorf("overall %s problems %v", r.Overall, problemCodes(r))
	}
}

// TestRecreatedInternalRootIsRed: a recreated empty internal root comes out red through the status
// rule alone (every tier-1 item is missing), as the spec says; there is no extra root rule.
func TestRecreatedInternalRootIsRed(t *testing.T) {
	w := newWorld().withModels(model("a", 1, 40), model("b", 2, 40)).withContent(kit("kit-a", 40), docs(40))
	r := w.build()
	if r.Overall != ready.Red || r.Counts.Missing != 4 {
		t.Errorf("overall %s counts %+v", r.Overall, r.Counts)
	}
	if r.SurvivalCapable {
		t.Error("an empty root is not survival capable")
	}
}

// TestClockWrong: when the clock is earlier than a time the node has already seen, every freshness
// is omitted, the age rules are off, the report says so, and overall comes from the status alone.
func TestClockWrong(t *testing.T) {
	build := func(verifiedAhead time.Duration, elsewhere time.Duration) (ready.Report, *world) {
		w := newWorld().drive().withModels(model("m", 1, 40)).withContent(kiwix("z", 2, 40)).have("m", "v1", -verifiedAhead).have("z", "v1", 500*day)
		w.reverified("z", 500*day)
		w.in.LastDoneAt = now.Add(-500 * day)
		if elsewhere != 0 {
			w.in.NewestVerifiedElsewhere = now.Add(-elsewhere)
		}
		return w.build(), w
	}

	// Verified in the future on this root.
	r, _ := build(time.Hour, 0)
	if !r.ClockWrong {
		t.Fatal("clock_wrong must be true")
	}
	for _, it := range r.Items {
		if it.Freshness != "" {
			t.Errorf("%s keeps freshness %s", it.ID, it.Freshness)
		}
		if it.AgeDays == nil {
			t.Errorf("%s lost age_days", it.ID)
		}
	}
	for _, code := range []string{doctor.ARK008, doctor.SYN025, doctor.SYN004} {
		if r.HasProblem(code) {
			t.Errorf("age rule %s applied with a wrong clock", code)
		}
	}
	if r.Overall != ready.Green {
		t.Errorf("overall = %s, want green (status only)", r.Overall)
	}
	if a := item(t, r, "m").AgeDays; *a != 0 {
		t.Errorf("a negative age clamps to 0, got %d", *a)
	}

	// A future verified_at on another root is enough.
	r, _ = build(0, -time.Hour)
	if !r.ClockWrong {
		t.Error("a root elsewhere that is ahead of the clock makes the clock wrong")
	}

	// Equal is not wrong, and the same data with a sane clock does apply the age rules.
	r, _ = build(0, 0)
	if r.ClockWrong {
		t.Error("verified exactly now is not a wrong clock")
	}
	if !r.HasProblem(doctor.ARK008) || !r.HasProblem(doctor.SYN025) || item(t, r, "z").Freshness != ready.Yellow {
		t.Errorf("with a sane clock: problems %v", problemCodes(r))
	}

	// Free space and an unclean root are not age rules and still apply.
	w := newWorld().drive().withModels(model("m", 1, 40)).have("m", "v1", -time.Hour)
	w.free, w.minFree = 1, 10
	in := w.input()
	in.Root.State = ark.StateUnclean
	r = ready.Build(in)
	if !r.ClockWrong || !r.HasProblem(doctor.SYN040) || !r.HasProblem(doctor.ARK004) || r.Overall != ready.Red {
		t.Errorf("wrong clock: problems %v overall %s", problemCodes(r), r.Overall)
	}
}

// TestClockWrongOverallFromStatus: with a wrong clock a missing tier-1 item still makes it red.
func TestClockWrongOverallFromStatus(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40), model("n", 1, 40)).have("m", "v1", -time.Hour)
	r := w.build()
	if !r.ClockWrong || r.Overall != ready.Red {
		t.Errorf("clock_wrong %v overall %s", r.ClockWrong, r.Overall)
	}
}

// TestMostlyOnline: more than half of the window in online mode, with no history meaning online.
func TestMostlyOnline(t *testing.T) {
	from := now.Add(-20 * day)
	at := func(d time.Duration, mode string) ready.ModeChange {
		return ready.ModeChange{At: now.Add(-d), Mode: mode}
	}
	tests := []struct {
		name    string
		changes []ready.ModeChange
		want    bool
	}{
		{"no history", nil, true},
		{"offline for the last 12 of 20 days", []ready.ModeChange{at(12*day, "offline")}, false},
		{"offline for the last 10 of 20 days is exactly half, not most", []ready.ModeChange{at(10*day, "offline")}, false},
		{"offline for the last 9 of 20 days", []ready.ModeChange{at(9*day, "offline")}, true},
		{"offline from before the window, back online 11 days ago", []ready.ModeChange{at(30*day, "offline"), at(11*day, "online")}, true},
		{"offline from before the window and still", []ready.ModeChange{at(30*day, "offline")}, false},
		{"survival counts as not online", []ready.ModeChange{at(15*day, "survival")}, false},
		{"back and forth ends online", []ready.ModeChange{at(17*day, "offline"), at(11*day, "online"), at(5*day, "offline"), at(2*day, "online")}, true},
		{"a change after the window is ignored", []ready.ModeChange{{At: now.Add(day), Mode: "offline"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ready.MostlyOnline(tt.changes, from, now); got != tt.want {
				t.Errorf("MostlyOnline = %v, want %v", got, tt.want)
			}
		})
	}
	if !ready.MostlyOnline([]ready.ModeChange{{At: now, Mode: "offline"}}, now, now) {
		t.Error("an empty window is online")
	}
}

// TestGaugeAndColour: the metric values and the helpers.
func TestGaugeAndColour(t *testing.T) {
	for c, want := range map[ready.Colour]int{ready.Green: 0, ready.Yellow: 1, ready.Red: 2, "": 2} {
		if got := c.Gauge(); got != want {
			t.Errorf("%q.Gauge() = %d, want %d", c, got, want)
		}
	}
	if ready.Worse(ready.Yellow, ready.Green) != ready.Yellow || ready.Worse(ready.Green, ready.Red) != ready.Red || ready.Worse(ready.Red, ready.Yellow) != ready.Red {
		t.Error("Worse")
	}
	if ready.Colour("blue").Valid() || !ready.Red.Valid() {
		t.Error("Valid")
	}
	it := ready.Item{Status: ready.StatusVerified, Tier: 1, Kind: "kiwix", Freshness: ready.Yellow}
	if ready.ItemColour(it, false) != ready.Yellow {
		t.Error("a verified item with a yellow freshness is yellow")
	}
	it = ready.Item{Status: ready.StatusMissing, Tier: 3, Kind: "maps"}
	if ready.ItemColour(it, true) != ready.Red || ready.ItemColour(it, false) != ready.Yellow {
		t.Error("the survival flag moves a tier-3 model to the strict column")
	}
}
