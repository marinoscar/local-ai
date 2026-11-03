package ready_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// TestItemStatus is the status derivation: what the planner would do with an item, what is on the
// root and what the journal says, to a status, a reason and a doctor code.
func TestItemStatus(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(w *world)
		id     string
		status ready.Status
		reason string
		code   string
	}{
		{
			name:  "the planned version is present",
			setup: func(w *world) { w.withModels(model("m", 1, 40)).have("m", "v1", 0) },
			id:    "m", status: ready.StatusVerified,
		},
		{
			name: "an older version is present and the update is pending",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).have("m", "v0", 0).target("m", "v1", 40)
			},
			id: "m", status: ready.StatusStale, reason: ready.ReasonUpdatePending,
		},
		{
			name: "an update left out for the budget keeps the old copy and says why",
			setup: func(w *world) {
				w.budget = 100
				w.withModels(model("a", 1, 60), model("m", 2, 60)).have("a", "v1", 0).have("m", "v0", 0).target("m", "v1", 60)
			},
			id: "m", status: ready.StatusStale, reason: ready.ReasonBudget, code: doctor.SYN041,
		},
		{
			name: "an update left out for disk space",
			setup: func(w *world) {
				w.free, w.minFree = 20, 10
				w.withModels(model("m", 2, 40)).have("m", "v0", 0).target("m", "v1", 40)
			},
			id: "m", status: ready.StatusStale, reason: ready.ReasonDisk, code: doctor.SYN040,
		},
		{
			name: "a kit item that does not fit for disk space is LAI-SYN-042",
			setup: func(w *world) {
				w.free, w.minFree = 20, 10
				w.withContent(kit("kit-a", 40)).have("kit-a", "v0", 0).target("kit-a", "v1", 40)
			},
			id: "kit-a", status: ready.StatusStale, reason: ready.ReasonDisk, code: doctor.SYN042,
		},
		{
			name: "a read-only root cannot install the update",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).have("m", "v0", 0).target("m", "v1", 40)
				w.readOnly = true
			},
			id: "m", status: ready.StatusStale, reason: ready.ReasonReadOnly,
		},
		{
			name:  "no copy and a download planned is missing",
			setup: func(w *world) { w.withModels(model("m", 1, 40)) },
			id:    "m", status: ready.StatusMissing, reason: ready.ReasonNotDownloaded,
		},
		{
			name:  "no copy and nothing to resolve it from is missing",
			setup: func(w *world) { w.withContent(kiwix("z", 2, 40)); w.offline = true },
			id:    "z", status: ready.StatusMissing, reason: ready.ReasonNotDownloaded,
		},
		{
			name: "no copy and a failed attempt is failed, with the journal's code",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).failedItem("m", journal.StateFailed, "v1", doctor.SYN031)
			},
			id: "m", status: ready.StatusFailed, reason: ready.ReasonDownloadFailed, code: doctor.SYN031,
		},
		{
			name: "no copy and a quarantined download is quarantined",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).failedItem("m", journal.StateQuarantined, "v1", doctor.SYN020)
			},
			id: "m", status: ready.StatusQuarantined, reason: ready.ReasonChecksum, code: doctor.SYN020,
		},
		{
			name: "no copy and left out for the budget is skipped",
			setup: func(w *world) {
				w.budget = 50
				w.withModels(model("a", 1, 40), model("b", 3, 40))
			},
			id: "b", status: ready.StatusSkipped, reason: ready.ReasonBudget, code: doctor.SYN041,
		},
		{
			name: "a kit item with no copy and no room is skipped with LAI-SYN-042",
			setup: func(w *world) {
				w.free, w.minFree = 20, 10
				w.withContent(kit("kit-a", 40))
			},
			id: "kit-a", status: ready.StatusSkipped, reason: ready.ReasonDisk, code: doctor.SYN042,
		},
		{
			name: "a read-only root with no copy: missing, not skipped",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40))
				w.readOnly = true
			},
			id: "m", status: ready.StatusMissing, reason: ready.ReasonReadOnly,
		},
		{
			name: "a copy and no answer from the resolver is kept as verified",
			setup: func(w *world) {
				w.withContent(kiwix("z", 2, 40)).have("z", "2026-07", 3*day)
				w.offline = true
				delete(w.resolved, "z")
			},
			id: "z", status: ready.StatusVerified,
		},
		{
			name: "a newer version differing within refresh_days is still the planned one",
			setup: func(w *world) {
				w.withContent(kiwix("z", 2, 40)).have("z", "2026-07", 3*day).target("z", "2026-10", 40)
			},
			id: "z", status: ready.StatusVerified,
		},
		{
			name: "a failed newer version makes the present copy stale even when the planner is offline",
			setup: func(w *world) {
				w.withContent(kiwix("z", 2, 40)).have("z", "2026-07", 124*day)
				w.offline = true
				delete(w.resolved, "z")
				w.failedItem("z", journal.StateFailed, "2026-10", doctor.SYN031)
			},
			id: "z", status: ready.StatusStale, reason: ready.ReasonUpdateFailed, code: doctor.SYN031,
		},
		{
			name: "a quarantined newer version makes the present copy stale",
			setup: func(w *world) {
				w.withContent(kiwix("z", 2, 40)).have("z", "2026-07", 4*day)
				w.offline = true
				delete(w.resolved, "z")
				w.failedItem("z", journal.StateQuarantined, "2026-10", doctor.SYN020)
			},
			id: "z", status: ready.StatusStale, reason: ready.ReasonChecksum, code: doctor.SYN020,
		},
		{
			name: "a failure of the version that is present does not make it stale",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).have("m", "v1", 0).failedItem("m", journal.StateFailed, "v1", doctor.SYN031)
			},
			id: "m", status: ready.StatusVerified,
		},
		{
			name: "a copy of an item that left the profile is an orphan",
			setup: func(w *world) {
				w.withModels(model("m", 1, 40)).have("m", "v1", 0).haveOrphan("gone", "kiwix", 5*day)
			},
			id: "gone", status: ready.StatusOrphan,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld()
			tt.setup(w)
			it := item(t, w.build(), tt.id)
			if it.Status != tt.status || it.Reason != tt.reason || it.Code != tt.code {
				t.Errorf("status/reason/code = %s/%q/%s, want %s/%q/%s", it.Status, it.Reason, it.Code, tt.status, tt.reason, tt.code)
			}
		})
	}
}

// TestItemFields: the fields of an item that has a copy, one that has none, and an orphan.
func TestItemFields(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40), model("n", 2, 30)).withContent(kiwix("z", 3, 50))
	w.survive = []string{"m"}
	w.have("m", "v1", 2*day).checked("m", day)
	w.haveOrphan("old", "kiwix", 400*day)
	r := w.build()

	m := item(t, r, "m")
	if m.Kind != "model" || m.Tier != 1 || !m.Survival || m.Version != "v1" || m.Trust != "pinned" {
		t.Errorf("m = %+v", m)
	}
	if m.VerifiedAt != "2026-10-31T15:00:00Z" || m.LastReverifiedAt != "2026-10-31T15:00:00Z" || m.CheckedCurrentAt != "2026-11-01T15:00:00Z" {
		t.Errorf("m times = %s %s %s", m.VerifiedAt, m.LastReverifiedAt, m.CheckedCurrentAt)
	}
	if m.AgeDays == nil || *m.AgeDays != 1 {
		t.Errorf("age is counted from checked_current_at: %v", m.AgeDays)
	}
	if m.Bytes == nil || *m.Bytes != 40 || m.Freshness != ready.Green {
		t.Errorf("m bytes/freshness = %v/%s", m.Bytes, m.Freshness)
	}

	n := item(t, r, "n")
	if n.Status != ready.StatusMissing || n.Bytes == nil || *n.Bytes != 30 || n.Version != "" || n.Freshness != "" || n.AgeDays != nil || n.Survival {
		t.Errorf("n = %+v: an item with no copy shows the size it should have and no age", n)
	}

	z := item(t, r, "z")
	if z.Tier != 3 || z.Status != ready.StatusMissing {
		t.Errorf("z = %+v", z)
	}

	o := item(t, r, "old")
	if o.Status != ready.StatusOrphan || o.Freshness != "" || o.AgeDays != nil || o.Version != "v1" || o.Tier != 2 {
		t.Errorf("an orphan has no freshness and no age, and a tier of 2 when the catalog does not know it: %+v", o)
	}

	if got := []string{r.Items[0].ID, r.Items[1].ID, r.Items[2].ID, r.Items[3].ID}; !reflect.DeepEqual(got, []string{"m", "n", "z", "old"}) {
		t.Errorf("order = %v: the planner's priority order, then orphans", got)
	}
	want := ready.Counts{Verified: 1, Missing: 2, Orphan: 1}
	if r.Counts != want {
		t.Errorf("counts = %+v, want %+v", r.Counts, want)
	}
}

// TestAgeWithoutChecked: age counts from verified_at when no run ever confirmed the item, and a
// negative age is zero.
func TestAgeWithoutChecked(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40), model("n", 1, 40))
	w.have("m", "v1", 10*day).have("n", "v1", -2*day)
	r := w.build()
	if a := item(t, r, "m").AgeDays; a == nil || *a != 10 {
		t.Errorf("age = %v, want 10", a)
	}
	if a := item(t, r, "n").AgeDays; a == nil || *a != 0 {
		t.Errorf("a verified_at in the future gives age %v, want 0", a)
	}
}

// TestRootBlock: the facts of the root block.
func TestRootBlock(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40), model("n", 2, 30)).have("m", "v1", day).haveOrphan("o", "kiwix", day)
	w.budget, w.total, w.free = 500, 2000, 1500
	r := w.build()
	got := r.Root
	if got.ID != "internal" || got.Kind != "internal" || got.Path != "/srv/localai" || got.Profile != "test-profile" ||
		got.FSTotalBytes != 2000 || got.FSFreeBytes != 1500 || got.BudgetBytes != 500 || got.UsedBytes != 50 || got.ReadOnly {
		t.Errorf("root = %+v", got)
	}
	if got.CatalogVersion == nil || *got.CatalogVersion != catVersion {
		t.Errorf("catalog_version = %v", got.CatalogVersion)
	}
	if r.SchemaVersion != 1 || r.GeneratedAt != "2026-11-02T15:00:00Z" || r.GeneratedBy.NodeID != "ai-n3" || r.GeneratedBy.AgentVersion != "0.4.0" {
		t.Errorf("header = %d %s %+v", r.SchemaVersion, r.GeneratedAt, r.GeneratedBy)
	}

	in := w.input()
	in.CatalogVersion = ""
	if cv := ready.Build(in).Root.CatalogVersion; cv != nil {
		t.Errorf("no catalog gives null, got %v", *cv)
	}
}

// TestLastSyncAndFullReverify: last_sync from the run record, last_full_reverify_at the oldest
// re-verification of any copy.
func TestLastSyncAndFullReverify(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40), model("n", 1, 40))
	if r := w.build(); r.LastSync != nil || r.LastFullReverifyAt != nil {
		t.Errorf("no run and no copy: last_sync %v, last_full_reverify_at %v", r.LastSync, r.LastFullReverifyAt)
	}
	w.have("m", "v1", 3*day).have("n", "v1", day).reverified("m", 20*day)
	w.in.LastSync = &ready.RunInfo{RunID: "run-x", Trigger: "manual", FinishedAt: now.Add(-36 * time.Hour), State: "failed"}
	r := w.build()
	if r.LastSync == nil || r.LastSync.RunID != "run-x" || r.LastSync.Trigger != "manual" || r.LastSync.State != "failed" ||
		r.LastSync.AgeDays != 1 || r.LastSync.FinishedAt != "2026-11-01T03:00:00Z" {
		t.Errorf("last_sync = %+v", r.LastSync)
	}
	if r.LastFullReverifyAt == nil || *r.LastFullReverifyAt != "2026-10-13T15:00:00Z" {
		t.Errorf("last_full_reverify_at = %v, want the oldest (m, 20 d)", r.LastFullReverifyAt)
	}
}

// TestProfileUnknown: with no profile there is nothing to judge the root against, so it is red and
// says why; nothing is claimed.
func TestProfileUnknown(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", day)
	in := w.input()
	in.Profile, in.ProfileUnknown = nil, true
	in.Plan.Items, in.Plan.BudgetBytes = nil, 0
	r := ready.Build(in)
	if r.Overall != ready.Red || r.SurvivalCapable || len(r.Items) != 0 || r.Counts != (ready.Counts{}) {
		t.Errorf("report = %s survival %v items %d counts %+v", r.Overall, r.SurvivalCapable, len(r.Items), r.Counts)
	}
	if got := problemCodes(r); !reflect.DeepEqual(got, []string{doctor.CAT003}) {
		t.Errorf("problems = %v", got)
	}
	if r.Root.UsedBytes != 40 {
		t.Errorf("used_bytes = %d: what is on the disk is still reported", r.Root.UsedBytes)
	}
}

// TestProblemTitles: a problem carries the doctor code, the code's severity and the English title of
// the doctor table; In re-titles it in Spanish and leaves the report alone.
func TestProblemTitles(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40))
	r := w.build()
	if len(r.Problems) != 0 {
		// A missing tier-1 model has no code of its own: it is red through the status rule, with no
		// problem to explain it. That is checked in the colour tests.
		t.Fatalf("problems = %v", r.Problems)
	}
	w.have("m", "v1", day)
	w.free, w.minFree = 3, 10
	r = w.build()
	if len(r.Problems) != 1 || r.Problems[0].Code != doctor.SYN040 {
		t.Fatalf("problems = %+v", r.Problems)
	}
	p := r.Problems[0]
	enTitle, _, _ := doctor.Text(doctor.SYN040, "en")
	esTitle, _, _ := doctor.Text(doctor.SYN040, "es")
	if p.Title != enTitle || p.Severity != doctor.Warning {
		t.Errorf("problem = %+v", p)
	}
	es := r.In("es")
	if es.Problems[0].Title != esTitle || r.Problems[0].Title != enTitle {
		t.Errorf("In(es) = %q, original = %q", es.Problems[0].Title, r.Problems[0].Title)
	}
	if got := r.In("fr").Problems[0].Title; got != enTitle {
		t.Errorf("an unknown language gives English, got %q", got)
	}
}

// TestUnknownKindItem: an item the catalog does not know falls back to the plan's kind and tier 2.
func TestUnknownKindItem(t *testing.T) {
	w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", day)
	in := w.input()
	in.Catalog = nil
	r := ready.Build(in)
	it := item(t, r, "m")
	if it.Kind != "model" || it.Tier != 2 {
		t.Errorf("item without a catalog = %+v", it)
	}
}

func TestRootKindJudgedLikeInternal(t *testing.T) {
	// A root of a kind the code does not know is judged like the internal root, as the planner does.
	w := newWorld().withModels(model("m", 1, 40)).have("m", "v1", 20*day)
	w.in.LastDoneAt = now.Add(-20 * day)
	in := w.input()
	in.Root.Kind = ark.RootKind("other")
	r := ready.Build(in)
	if !r.HasProblem(doctor.SYN004) {
		t.Errorf("problems = %v", problemCodes(r))
	}
}

var _ = catalog.Empty
var _ = manifest.KindModel
