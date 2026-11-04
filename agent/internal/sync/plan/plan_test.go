package plan_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

func TestBudget(t *testing.T) {
	cases := []struct {
		name   string
		budget int64
		want   map[string]plan.Action // item id -> action
		reason map[string]plan.Reason
		used   int64
	}{
		{name: "exact fit", budget: 100, want: map[string]plan.Action{"a": plan.ActionDownload, "b": plan.ActionDownload}, used: 100},
		{name: "off by one byte", budget: 99, want: map[string]plan.Action{"a": plan.ActionDownload, "b": plan.ActionSkip},
			reason: map[string]plan.Reason{"b": plan.ReasonBudget}, used: 40},
		{name: "nothing fits", budget: 30, want: map[string]plan.Action{"a": plan.ActionSkip, "b": plan.ActionSkip},
			reason: map[string]plan.Reason{"a": plan.ReasonBudget, "b": plan.ReasonBudget}, used: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("a", 2, 40), model("b", 2, 60)}, nil)
			s.in.Profile.Storage.BudgetBytes = tc.budget
			s.resolveAll()
			p := plan.Plan(s.build())
			for id, want := range tc.want {
				got := row(t, p, id)
				if got.Action != want || got.Reason != tc.reason[id] {
					t.Errorf("%s: %s/%q, want %s/%q", id, got.Action, got.Reason, want, tc.reason[id])
				}
			}
			if p.PlannedBytes != tc.used || p.BudgetBytes != tc.budget {
				t.Errorf("planned %d of %d, want %d of %d", p.PlannedBytes, p.BudgetBytes, tc.used, tc.budget)
			}
		})
	}
}

func TestPriorityOrder(t *testing.T) {
	cases := []struct {
		name    string
		models  []catalog.Model
		content []catalog.ContentItem
		survive []string
		want    []string
	}{
		{name: "tier ascending", models: []catalog.Model{model("m3", 3, 1), model("m1", 1, 1), model("m2", 2, 1)}, want: []string{"m1", "m2", "m3"}},
		{name: "list position breaks ties", models: []catalog.Model{model("x", 2, 1), model("y", 2, 1), model("z", 2, 1)}, want: []string{"x", "y", "z"}},
		{name: "survival models first within a tier", models: []catalog.Model{model("plain", 1, 1), model("surv", 1, 1)}, survive: []string{"surv"},
			want: []string{"surv", "plain"}},
		{name: "survival does not beat a lower tier", models: []catalog.Model{model("plain", 1, 1), model("surv", 2, 1)}, survive: []string{"surv"},
			want: []string{"plain", "surv"}},
		{name: "kit and docs first whatever the tier", models: []catalog.Model{model("m1", 1, 1)},
			content: []catalog.ContentItem{zim("wiki", 1, tier(1)), docs(1), kit("kit-x", 1)}, want: []string{"docs-bundle", "kit-x", "m1", "wiki"}},
		{name: "missing tier counts as 2", models: []catalog.Model{model("m3", 3, 1)},
			content: []catalog.ContentItem{zim("wiki", 1, tier(0)), zim("wiki1", 1, tier(1))}, want: []string{"wiki1", "wiki", "m3"}},
		{name: "models before content of the same tier", models: []catalog.Model{model("m", 2, 1)}, content: []catalog.ContentItem{zim("wiki", 1)},
			want: []string{"m", "wiki"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(tc.models, tc.content)
			s.survive = tc.survive
			s.resolveAll()
			p := plan.Plan(s.build())
			if got := ids(p); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("order = %v, want %v", got, tc.want)
			}
			for i, it := range p.Items {
				if it.Pos != i+1 {
					t.Errorf("%s has pos %d, want %d", it.ID, it.Pos, i+1)
				}
			}
		})
	}
}

func TestLowerTiersAreDroppedFirst(t *testing.T) {
	// 3 models of 40 into a budget of 100: the later and higher-numbered tier loses.
	s := newScenario([]catalog.Model{model("late-t1", 1, 40), model("early-t3", 3, 40), model("mid-t2", 2, 40)}, nil)
	s.resolveAll()
	p := plan.Plan(s.build())
	if row(t, p, "early-t3").Action != plan.ActionSkip || row(t, p, "late-t1").Action != plan.ActionDownload || row(t, p, "mid-t2").Action != plan.ActionDownload {
		t.Fatalf("plan = %+v", p.Items)
	}
	if !p.HasFinding(plan.CodeBudgetFull) {
		t.Errorf("findings = %+v, want LAI-SYN-041", p.Findings)
	}
}

func TestKitAndDocsAreNeverDropped(t *testing.T) {
	s := newScenario([]catalog.Model{model("m1", 1, 30)}, []catalog.ContentItem{kit("kit-big", 90), docs(20)})
	s.resolveAll()
	p := plan.Plan(s.build())
	for _, id := range []string{"kit-big", "docs-bundle"} {
		if got := row(t, p, id); got.Action != plan.ActionDownload || !got.Tier.Pinned {
			t.Errorf("%s: %+v, want a pinned-class download", id, got)
		}
	}
	// The model does not fit next to them and is the one that is dropped.
	if got := row(t, p, "m1"); got.Action != plan.ActionSkip || got.Reason != plan.ReasonBudget {
		t.Errorf("m1: %+v", got)
	}
	var f plan.Finding
	for _, x := range p.Findings {
		if x.Code == plan.CodeKitDoesNotFit {
			f = x
		}
	}
	if f.Code == "" || f.Severity != doctor.Error || !reflect.DeepEqual(f.Items, []string{"docs-bundle"}) {
		t.Errorf("LAI-SYN-042 finding = %+v (the item that crossed the budget is the second kit-class item)", f)
	}
}

func TestKitDoesNotFitOnDisk(t *testing.T) {
	s := newScenario(nil, []catalog.ContentItem{kit("kit-big", 50)})
	s.in.Root.FreeBytes = 52 // 52 - 50 < min_free 5
	s.resolveAll()
	p := plan.Plan(s.build())
	if got := row(t, p, "kit-big"); got.Action != plan.ActionSkip || got.Reason != plan.ReasonDisk {
		t.Fatalf("kit-big = %+v", got)
	}
	if !p.HasFinding(plan.CodeKitDoesNotFit) || !p.HasFinding(plan.CodeDiskFull) {
		t.Errorf("findings = %+v, want LAI-SYN-042 and LAI-SYN-040", p.Findings)
	}
}

func TestSurvivalModelSkippedIsAnError(t *testing.T) {
	s := newScenario([]catalog.Model{model("big", 1, 70), model("surv", 1, 70)}, []catalog.ContentItem{zim("wiki", 10)})
	s.survive = []string{"surv"}
	s.resolveAll()
	p := plan.Plan(s.build())
	// surv is ranked before big, so big is the one skipped; make survival the casualty instead.
	if row(t, p, "surv").Action != plan.ActionDownload || row(t, p, "big").Action != plan.ActionSkip {
		t.Fatalf("plan = %+v", p.Items)
	}
	for _, f := range p.Findings {
		if f.Code == plan.CodeBudgetFull && f.Severity == doctor.Error {
			t.Errorf("a non-survival skip must be a warning, got %+v", f)
		}
	}

	s = newScenario([]catalog.Model{model("surv", 3, 70)}, []catalog.ContentItem{zim("wiki", 80, tier(1))})
	s.survive = []string{"surv"}
	s.resolveAll()
	p = plan.Plan(s.build())
	if row(t, p, "surv").Reason != plan.ReasonBudget {
		t.Fatalf("plan = %+v", p.Items)
	}
	var got plan.Finding
	for _, f := range p.Findings {
		if f.Code == plan.CodeBudgetFull {
			got = f
		}
	}
	if got.Severity != doctor.Error || !reflect.DeepEqual(got.Items, []string{"surv"}) {
		t.Errorf("finding = %+v, want an error naming surv", got)
	}
}

func TestKitFilteringByCodenameAndArch(t *testing.T) {
	content := []catalog.ContentItem{debs("resolute", "amd64", 5), debs("resolute", "arm64", 5), debs("noble", "amd64", 5), kit("kit-x", 1)}
	cases := []struct {
		name    string
		node    plan.NodeInfo
		kind    ark.RootKind
		want    []string
		dropped []string
	}{
		{name: "internal keeps only this node's", node: plan.NodeInfo{Codename: "resolute", Arch: "amd64"}, kind: ark.KindInternal,
			want: []string{"kit-debs-resolute-amd64", "kit-x"}, dropped: []string{"kit-debs-resolute-arm64", "kit-debs-noble-amd64"}},
		{name: "a drive keeps them all", node: plan.NodeInfo{Codename: "resolute", Arch: "amd64"}, kind: ark.KindArkDrive,
			want: []string{"kit-debs-resolute-amd64", "kit-debs-resolute-arm64", "kit-debs-noble-amd64", "kit-x"}},
		{name: "an unknown node filters nothing", node: plan.NodeInfo{}, kind: ark.KindInternal,
			want: []string{"kit-debs-resolute-amd64", "kit-debs-resolute-arm64", "kit-debs-noble-amd64", "kit-x"}},
		{name: "unknown arch still filters the codename", node: plan.NodeInfo{Codename: "noble"}, kind: ark.KindInternal,
			want: []string{"kit-debs-noble-amd64", "kit-x"}, dropped: []string{"kit-debs-resolute-amd64", "kit-debs-resolute-arm64"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(nil, content)
			s.in.Node = tc.node
			s.in.Root.Kind = tc.kind
			s.in.Root.TotalBytes = 100000
			s.in.Root.FreeBytes = 100000
			s.resolveAll()
			p := plan.Plan(s.build())
			got := ids(p)
			if !sameSet(got, tc.want) {
				t.Errorf("items = %v, want %v", got, tc.want)
			}
			if !sameSet(p.NotForNode, tc.dropped) {
				t.Errorf("not_for_node = %v, want %v", p.NotForNode, tc.dropped)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestOverrides(t *testing.T) {
	models := []catalog.Model{model("m1", 1, 10), model("m2", 1, 10), model("m3", 1, 10)}
	content := []catalog.ContentItem{zim("w1", 10), zim("w2", 10), zim("w3", 10)}
	build := func(ov plan.ProfileOverrides) plan.RootPlan {
		s := newScenario(models, content)
		// The profile lists only m1, m2, w1 and w2; m3 and w3 are known to the catalog but not included.
		s.in.Profile.Include = catalog.Include{Models: []string{"m1", "m2"}, Content: []string{"w1", "w2"}}
		s.in.Overrides = &ov
		s.resolveAll()
		return plan.Plan(s.build())
	}
	t.Run("add appends after the list, models then content", func(t *testing.T) {
		p := build(plan.ProfileOverrides{AddModels: []string{"m3"}, AddContent: []string{"w3"}})
		if got, want := ids(p), []string{"m1", "m2", "m3", "w1", "w2", "w3"}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
	t.Run("adding an id already there changes nothing", func(t *testing.T) {
		p := build(plan.ProfileOverrides{AddModels: []string{"m1"}})
		if got, want := ids(p), []string{"m1", "m2", "w1", "w2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
	t.Run("remove wins over include and add", func(t *testing.T) {
		p := build(plan.ProfileOverrides{AddModels: []string{"m3"}, RemoveModels: []string{"m1", "m3"}, RemoveContent: []string{"w2"}})
		if got, want := ids(p), []string{"m2", "w1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
	t.Run("a removed item that is present becomes an orphan, never a deletion", func(t *testing.T) {
		s := newScenario(models, content)
		s.in.Overrides = &plan.ProfileOverrides{RemoveModels: []string{"m1"}}
		s.resolveAll()
		s.have("m1", manifest.KindModel, "v1", 10, days)
		p := plan.Plan(s.build())
		if got := row(t, p, "m1"); got.Action != plan.ActionOrphan {
			t.Errorf("m1 = %+v, want an orphan", got)
		}
	})
	t.Run("budget override replaces the profile budget", func(t *testing.T) {
		b := int64(25)
		p := build(plan.ProfileOverrides{BudgetBytes: &b})
		if p.BudgetBytes != 25 || row(t, p, "w2").Action != plan.ActionSkip {
			t.Errorf("budget %d, w2 %+v", p.BudgetBytes, row(t, p, "w2"))
		}
	})
	t.Run("a budget override that is not positive is ignored", func(t *testing.T) {
		b := int64(0)
		if p := build(plan.ProfileOverrides{BudgetBytes: &b}); p.BudgetBytes != 100 {
			t.Errorf("budget = %d", p.BudgetBytes)
		}
	})
}

func TestUnknownIDsAreDroppedWithCAT004(t *testing.T) {
	s := newScenario([]catalog.Model{model("m1", 1, 10)}, []catalog.ContentItem{zim("w1", 10)})
	s.in.Profile.Include.Models = append(s.in.Profile.Include.Models, "ghost-model")
	s.in.Overrides = &plan.ProfileOverrides{AddContent: []string{"ghost-content"}, RemoveModels: []string{"never-listed"}}
	s.resolveAll()
	p := plan.Plan(s.build())
	if got, want := ids(p), []string{"m1", "w1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	var f plan.Finding
	for _, x := range p.Findings {
		if x.Code == plan.CodeUnknownID {
			f = x
		}
	}
	if !sameSet(f.Items, []string{"ghost-model", "ghost-content"}) {
		t.Errorf("LAI-CAT-004 items = %v (a removal of an unknown id is a no-op, not a finding)", f.Items)
	}
}

func TestModelsThatCannotRunHereAreKept(t *testing.T) {
	m := model("huge", 3, 10)
	m.Requirements = catalog.Requirements{Backends: []string{"cuda"}, MinVRAMMiB: 1 << 30}
	s := newScenario([]catalog.Model{m}, nil)
	s.in.Node = plan.NodeInfo{Backend: "cpu", VRAMMiB: 0}
	s.resolveAll()
	if got := row(t, plan.Plan(s.build()), "huge"); got.Action != plan.ActionDownload {
		t.Fatalf("huge = %+v: a model that cannot load is still stored", got)
	}
}

func TestPresentAndRefresh(t *testing.T) {
	cases := []struct {
		name       string
		item       catalog.ContentItem
		haveVer    string
		age        time.Duration
		target     string
		wantAction plan.Action
		wantReason plan.Reason
	}{
		{name: "same version is kept", item: zim("w", 10, refresh(30)), haveVer: "v1", age: 100 * days, target: "v1", wantAction: plan.ActionKeep},
		{name: "refresh_days not due", item: zim("w", 10, refresh(30)), haveVer: "v0", age: 29 * days, target: "v1", wantAction: plan.ActionKeep, wantReason: plan.ReasonRefreshDays},
		{name: "exactly refresh_days old is not yet due", item: zim("w", 10, refresh(30)), haveVer: "v0", age: 30 * days, target: "v1", wantAction: plan.ActionKeep, wantReason: plan.ReasonRefreshDays},
		{name: "older than refresh_days is due", item: zim("w", 10, refresh(30)), haveVer: "v0", age: 31 * days, target: "v1", wantAction: plan.ActionUpdate, wantReason: plan.ReasonRefreshDue},
		{name: "no refresh_days and not pinned never refreshes", item: zim("w", 10), haveVer: "v0", age: 999 * days, target: "v1", wantAction: plan.ActionKeep, wantReason: plan.ReasonRefreshDays},
		{name: "a pinned change is always applied", item: kit("k", 10), haveVer: "v0", age: 1 * days, target: "v1", wantAction: plan.ActionUpdate, wantReason: plan.ReasonCatalogPin},
		{name: "pinned kiwix is applied too", item: zim("w", 10, source(catalog.ContentSource{Type: "kiwix", VersionPolicy: "pinned"}), refresh(90)), haveVer: "v0", age: 1 * days, target: "v1",
			wantAction: plan.ActionUpdate, wantReason: plan.ReasonCatalogPin},
		{name: "missing is downloaded", item: zim("w", 10), haveVer: "", target: "v1", wantAction: plan.ActionDownload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(nil, []catalog.ContentItem{tc.item})
			s.resolve(tc.item.ID, tc.target, 10)
			if tc.haveVer != "" {
				s.have(tc.item.ID, tc.item.Kind, tc.haveVer, 10, tc.age)
			}
			got := row(t, plan.Plan(s.build()), tc.item.ID)
			if got.Action != tc.wantAction || got.Reason != tc.wantReason {
				t.Errorf("%+v, want %s/%q", got, tc.wantAction, tc.wantReason)
			}
		})
	}

	t.Run("a model with a new revision is updated at once", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolve("m", "v2", 10)
		s.have("m", manifest.KindModel, "v1", 10, 1*days)
		got := row(t, plan.Plan(s.build()), "m")
		if got.Action != plan.ActionUpdate || got.Reason != plan.ReasonCatalogPin || got.FromVersion != "v1" || got.ToVersion != "v2" {
			t.Errorf("m = %+v", got)
		}
	})

	t.Run("the age is reported in whole days", func(t *testing.T) {
		s := newScenario(nil, []catalog.ContentItem{zim("w", 10, refresh(30))})
		s.resolve("w", "v1", 10)
		s.have("w", "kiwix", "v0", 10, 31*days+5*time.Hour)
		if got := row(t, plan.Plan(s.build()), "w"); got.AgeDays != 31 || got.RefreshDays != 30 {
			t.Errorf("age %d refresh %d", got.AgeDays, got.RefreshDays)
		}
	})
}

func TestUpdateThatDoesNotFitKeepsThePresentVersion(t *testing.T) {
	s := newScenario([]catalog.Model{model("a", 1, 40), model("b", 2, 30)}, nil)
	s.resolve("a", "v1", 40)
	s.resolve("b", "v2", 80) // grew from 30 to 80
	s.have("a", manifest.KindModel, "v1", 40, days)
	s.have("b", manifest.KindModel, "v1", 30, days)
	p := plan.Plan(s.build())
	if got := row(t, p, "b"); got.Action != plan.ActionKeep || got.Reason != plan.ReasonBudget || got.FromVersion != "v1" {
		t.Fatalf("b = %+v", got)
	}
	if p.PlannedBytes != 70 {
		t.Errorf("planned = %d, want 70 (the present copy still counts)", p.PlannedBytes)
	}
}

func TestUnresolved(t *testing.T) {
	cases := []struct {
		name       string
		present    bool
		readOnly   bool
		wantAction plan.Action
		wantReason plan.Reason
	}{
		{name: "present is kept", present: true, wantAction: plan.ActionKeep, wantReason: plan.ReasonUnresolved},
		{name: "absent is skipped (needs the network)", wantAction: plan.ActionSkip, wantReason: plan.ReasonNeedsNetwork},
		{name: "present on a read-only root is still kept", present: true, readOnly: true, wantAction: plan.ActionKeep, wantReason: plan.ReasonUnresolved},
		{name: "absent on a read-only root is read_only", readOnly: true, wantAction: plan.ActionSkip, wantReason: plan.ReasonReadOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(nil, []catalog.ContentItem{zim("w", 10)})
			s.in.Offline = true
			s.in.Root.ReadOnly = tc.readOnly
			if tc.present {
				s.have("w", "kiwix", "v1", 10, days)
			}
			p := plan.Plan(s.build())
			got := row(t, p, "w")
			if got.Action != tc.wantAction || got.Reason != tc.wantReason {
				t.Errorf("%+v, want %s/%q", got, tc.wantAction, tc.wantReason)
			}
			if !p.Offline {
				t.Error("the plan must say it was made offline")
			}
		})
	}
}

func TestModesThatForbidTheNetwork(t *testing.T) {
	for _, mode := range []string{plan.ModeOffline, plan.ModeSurvival} {
		t.Run(mode, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
			s.resolve("m", "v1", 10) // known from the catalog pin, but there is no local copy
			s.in.Mode = mode
			p := plan.Plan(s.build())
			if got := row(t, p, "m"); got.Action != plan.ActionSkip || got.Reason != plan.ReasonNeedsNetwork || got.Source != "" {
				t.Errorf("m = %+v, want skip/needs_network", got)
			}
			if !p.Offline || p.Mode != mode {
				t.Errorf("offline %v mode %q", p.Offline, p.Mode)
			}
		})
	}
	t.Run("online uses the network", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolve("m", "v1", 10)
		s.in.Mode = plan.ModeOnline
		if got := row(t, plan.Plan(s.build()), "m"); got.Source != plan.SourceNetwork {
			t.Errorf("m = %+v", got)
		}
	})
}

func TestLocalCopySourcing(t *testing.T) {
	other := func(version string) map[string][]plan.RootCopy {
		return map[string][]plan.RootCopy{"m": {{RootID: "ark-3f9c0a12", Manifest: manifest.Manifest{ItemID: "m", Version: version, TotalBytes: 10}}}}
	}
	cases := []struct {
		name       string
		others     map[string][]plan.RootCopy
		offline    bool
		wantAction plan.Action
		wantSource plan.Source
		wantFrom   string
	}{
		{name: "a copy of the target version elsewhere", others: other("v1"), wantAction: plan.ActionDownload, wantSource: plan.SourceLocalCopy, wantFrom: "ark-3f9c0a12"},
		{name: "a copy of another version is not used", others: other("v0"), wantAction: plan.ActionDownload, wantSource: plan.SourceNetwork},
		{name: "no copy elsewhere", wantAction: plan.ActionDownload, wantSource: plan.SourceNetwork},
		{name: "offline with a local copy still works", others: other("v1"), offline: true, wantAction: plan.ActionDownload, wantSource: plan.SourceLocalCopy, wantFrom: "ark-3f9c0a12"},
		{name: "offline without one is unresolved", offline: true, wantAction: plan.ActionSkip},
		{name: "the root's own id is never a source", others: map[string][]plan.RootCopy{"m": {{RootID: "internal", Manifest: manifest.Manifest{Version: "v1"}}}}, wantAction: plan.ActionDownload, wantSource: plan.SourceNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
			s.resolve("m", "v1", 10)
			s.in.OtherRoots = tc.others
			s.in.Offline = tc.offline
			got := row(t, plan.Plan(s.build()), "m")
			if got.Action != tc.wantAction || got.Source != tc.wantSource || got.FromRoot != tc.wantFrom {
				t.Errorf("m = %+v, want %s/%s from %q", got, tc.wantAction, tc.wantSource, tc.wantFrom)
			}
		})
	}
}

func TestPlanAllSequencesSharedDownloads(t *testing.T) {
	mk := func(rootID string, kind ark.RootKind) plan.PlanInput {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolve("m", "v1", 10)
		s.in.Root.ID, s.in.Root.Kind = rootID, kind
		s.in.Root.TotalBytes, s.in.Root.FreeBytes = 1_000_000_000_000, 1_000_000_000_000
		return s.build()
	}
	plans := plan.PlanAll([]plan.PlanInput{mk("internal", ark.KindInternal), mk("ark-3f9c0a12", ark.KindArkDrive), mk("ark-11111111", ark.KindArkDrive)})
	first, second, third := row(t, plans[0], "m"), row(t, plans[1], "m"), row(t, plans[2], "m")
	if first.Source != plan.SourceNetwork || first.After != "" {
		t.Errorf("first root = %+v, want the one network download", first)
	}
	for i, got := range []plan.ItemPlan{second, third} {
		if got.Source != plan.SourceLocalCopy || got.FromRoot != "internal" || got.After != "internal" {
			t.Errorf("root %d = %+v, want a local copy after internal", i+1, got)
		}
	}
	if tot := plans[1].Totals(); tot.NetworkBytes != 0 || tot.LocalBytes != 10 {
		t.Errorf("totals = %+v", tot)
	}

	t.Run("different versions are not shared", func(t *testing.T) {
		a, b := mk("internal", ark.KindInternal), mk("ark-3f9c0a12", ark.KindArkDrive)
		b.Resolved = map[string]plan.Resolved{"m": {Version: "v2", SizeBytes: 10}}
		ps := plan.PlanAll([]plan.PlanInput{a, b})
		if got := row(t, ps[1], "m"); got.Source != plan.SourceNetwork {
			t.Errorf("m = %+v", got)
		}
	})
}

func TestOrphans(t *testing.T) {
	cases := []struct {
		name       string
		orphanSize int64
		wantFlag   bool
	}{
		{name: "exactly 5 percent of the budget is not reported", orphanSize: 5},
		{name: "one byte over 5 percent is reported", orphanSize: 6, wantFlag: true},
		{name: "none", orphanSize: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
			s.resolveAll()
			if tc.orphanSize > 0 {
				s.have("old-model", manifest.KindModel, "v9", tc.orphanSize, days)
			}
			p := plan.Plan(s.build())
			if p.HasFinding(plan.CodeOrphans) != tc.wantFlag {
				t.Errorf("LAI-SYN-051 raised = %v, want %v", p.HasFinding(plan.CodeOrphans), tc.wantFlag)
			}
			if tc.orphanSize > 0 {
				got := row(t, p, "old-model")
				if got.Action != plan.ActionOrphan || got.Pos != 0 || got.Tier != nil || got.FromVersion != "v9" || got.FinalBytes != tc.orphanSize {
					t.Errorf("orphan = %+v", got)
				}
				// An orphan never counts against the budget.
				if p.PlannedBytes != 10 {
					t.Errorf("planned = %d, want 10", p.PlannedBytes)
				}
			}
		})
	}

	t.Run("orphans are listed last, by id", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolveAll()
		s.have("zeta", "kiwix", "v1", 1, days)
		s.have("alpha", "kiwix", "v1", 1, days)
		if got, want := ids(plan.Plan(s.build())), []string{"m", "alpha", "zeta"}; !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	})
}

func TestSizingAndNeed(t *testing.T) {
	t.Run("resolver size wins over the catalog size", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolve("m", "v1", 25)
		if got := row(t, plan.Plan(s.build()), "m"); got.FinalBytes != 25 || got.NeedBytes != 25 {
			t.Errorf("m = %+v", got)
		}
	})
	t.Run("an unknown resolver size falls back to the catalog", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
		s.resolve("m", "v1", 0)
		if got := row(t, plan.Plan(s.build()), "m"); got.FinalBytes != 10 {
			t.Errorf("m = %+v", got)
		}
	})
	t.Run("then to the present copy", func(t *testing.T) {
		s := newScenario(nil, []catalog.ContentItem{zim("w", 0)})
		s.resolve("w", "v2", 0)
		s.have("w", "kiwix", "v1", 33, 100*days)
		s.in.Profile.Storage.BudgetBytes = 100
		if got := row(t, plan.Plan(s.build()), "w"); got.FinalBytes != 33 {
			t.Errorf("w = %+v", got)
		}
	})
	t.Run("staged bytes of the same version are not needed again", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
		s.resolve("m", "v1", 40)
		s.in.Partial["m"] = plan.PartState{Version: "v1", PartBytes: 15, StagedBytes: 5}
		got := row(t, plan.Plan(s.build()), "m")
		if got.FinalBytes != 40 || got.NeedBytes != 20 {
			t.Errorf("m = %+v, want final 40 need 20", got)
		}
	})
	t.Run("staged bytes of another version are ignored", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
		s.resolve("m", "v2", 40)
		s.in.Partial["m"] = plan.PartState{Version: "v1", PartBytes: 15}
		if got := row(t, plan.Plan(s.build()), "m"); got.NeedBytes != 40 {
			t.Errorf("m = %+v", got)
		}
	})
	t.Run("more staged than needed never goes negative", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
		s.resolve("m", "v1", 40)
		s.in.Partial["m"] = plan.PartState{Version: "v1", StagedBytes: 90}
		if got := row(t, plan.Plan(s.build()), "m"); got.NeedBytes != 0 {
			t.Errorf("m = %+v", got)
		}
	})
	t.Run("a keep needs nothing", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
		s.resolve("m", "v1", 40)
		s.have("m", manifest.KindModel, "v1", 40, days)
		if got := row(t, plan.Plan(s.build()), "m"); got.NeedBytes != 0 || got.FinalBytes != 40 {
			t.Errorf("m = %+v", got)
		}
	})
}

func TestMinFree(t *testing.T) {
	trash := func(eligible bool, sizes ...int64) []plan.TrashEntry {
		var out []plan.TrashEntry
		for i, sz := range sizes {
			out = append(out, plan.TrashEntry{ItemID: "old", Version: string(rune('a' + i)), Bytes: sz, Eligible: eligible, TrashedAt: now.Add(time.Duration(i) * time.Hour)})
		}
		return out
	}
	cases := []struct {
		name       string
		free       int64
		trash      []plan.TrashEntry
		wantAction plan.Action
		wantReason plan.Reason
		wantPurge  int64
		wantFloor  int64
	}{
		{name: "enough room", free: 100, wantAction: plan.ActionDownload, wantFloor: 60},
		{name: "exactly min_free left is allowed", free: 45, wantAction: plan.ActionDownload, wantFloor: 5},
		{name: "one byte short is blocked", free: 44, wantAction: plan.ActionSkip, wantReason: plan.ReasonDisk, wantFloor: 44},
		{name: "eligible trash makes room", free: 30, trash: trash(true, 20), wantAction: plan.ActionDownload, wantPurge: 20, wantFloor: 10},
		{name: "only as much trash as needed is purged, oldest first", free: 30, trash: trash(true, 12, 50), wantAction: plan.ActionDownload, wantPurge: 62, wantFloor: 52},
		{name: "trash that does not cover it is not enough", free: 20, trash: trash(true, 10), wantAction: plan.ActionSkip, wantReason: plan.ReasonDisk, wantFloor: 20},
		{name: "ineligible trash (replacement not verified) is never counted", free: 30, trash: trash(false, 500), wantAction: plan.ActionSkip, wantReason: plan.ReasonDisk, wantFloor: 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
			s.resolve("m", "v1", 40)
			s.in.Root.FreeBytes = tc.free
			s.in.Trash = tc.trash
			p := plan.Plan(s.build())
			got := row(t, p, "m")
			if got.Action != tc.wantAction || got.Reason != tc.wantReason || got.PurgeBytes != tc.wantPurge {
				t.Errorf("m = %+v, want %s/%q purge %d", got, tc.wantAction, tc.wantReason, tc.wantPurge)
			}
			if p.FreeFloorBytes != tc.wantFloor {
				t.Errorf("free floor = %d, want %d", p.FreeFloorBytes, tc.wantFloor)
			}
			if tc.wantAction == plan.ActionSkip && !p.HasFinding(plan.CodeDiskFull) {
				t.Error("a disk skip must raise LAI-SYN-040")
			}
		})
	}

	t.Run("downloads are checked one after the other against the shrinking free space", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("a", 1, 30), model("b", 1, 30), model("c", 1, 30)}, nil)
		s.in.Profile.Storage.BudgetBytes = 1000
		s.resolveAll()
		s.in.Root.FreeBytes = 80 // a: 80-30=50 ok; b: 50-30=20 ok; c: 20-30 < 5 blocked
		p := plan.Plan(s.build())
		if row(t, p, "a").Action != plan.ActionDownload || row(t, p, "b").Action != plan.ActionDownload {
			t.Fatalf("plan = %+v", p.Items)
		}
		if got := row(t, p, "c"); got.Action != plan.ActionSkip || got.Reason != plan.ReasonDisk {
			t.Errorf("c = %+v", got)
		}
	})

	t.Run("a smaller item still fits after a bigger one is blocked", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("big", 1, 90), model("small", 2, 10)}, nil)
		s.in.Profile.Storage.BudgetBytes = 1000
		s.resolveAll()
		s.in.Root.FreeBytes = 50
		p := plan.Plan(s.build())
		if row(t, p, "big").Reason != plan.ReasonDisk || row(t, p, "small").Action != plan.ActionDownload {
			t.Errorf("plan = %+v", p.Items)
		}
	})

	t.Run("a blocked update keeps the old copy in the steady state", func(t *testing.T) {
		s := newScenario([]catalog.Model{model("m", 1, 40)}, nil)
		s.resolve("m", "v2", 40)
		s.have("m", manifest.KindModel, "v1", 35, days)
		s.in.Root.FreeBytes = 44
		p := plan.Plan(s.build())
		if got := row(t, p, "m"); got.Action != plan.ActionSkip || got.Reason != plan.ReasonDisk || got.FromVersion != "v1" {
			t.Errorf("m = %+v", got)
		}
		if p.PlannedBytes != 35 {
			t.Errorf("planned = %d, want 35", p.PlannedBytes)
		}
	})
}

func TestPeak(t *testing.T) {
	s := newScenario([]catalog.Model{model("new", 1, 40), model("upd", 1, 30), model("same", 2, 10)}, nil)
	s.in.Profile.Storage.BudgetBytes = 1000
	s.resolve("new", "v1", 40)
	s.resolve("upd", "v2", 30)
	s.resolve("same", "v1", 10)
	s.have("upd", manifest.KindModel, "v1", 25, days)
	s.have("same", manifest.KindModel, "v1", 10, days)
	s.have("orphan-x", "kiwix", "v1", 7, days)
	s.in.Trash = []plan.TrashEntry{{ItemID: "old", Version: "v0", Bytes: 6, Eligible: true, TrashedAt: now}}
	s.in.Partial["new"] = plan.PartState{Version: "v1", PartBytes: 4}
	s.in.Root.FreeBytes = 500
	p := plan.Plan(s.build())

	// On disk now: 25 + 10 + 7 (copies, orphan included) + 6 (trash) + 4 (staging) = 52.
	// Still to write: new 40-4 = 36 and upd 30 = 66.
	if p.PeakBytes != 52+66 {
		t.Errorf("peak = %d, want %d", p.PeakBytes, 52+66)
	}
	if p.FreeFloorBytes != 500-66 {
		t.Errorf("free floor = %d, want %d", p.FreeFloorBytes, 500-66)
	}
	if p.TrashBytes != 6 {
		t.Errorf("trash = %d", p.TrashBytes)
	}
	if p.PlannedBytes != 40+30+10 {
		t.Errorf("planned = %d", p.PlannedBytes)
	}
	if tot := p.Totals(); tot.Items != 2 || tot.NetworkBytes != 66 || tot.LocalBytes != 0 {
		t.Errorf("totals = %+v", tot)
	}
}

func TestReadOnlyRoot(t *testing.T) {
	s := newScenario([]catalog.Model{model("have", 1, 10), model("old", 1, 10), model("new", 1, 10)}, nil)
	s.resolve("have", "v1", 10)
	s.resolve("old", "v2", 10)
	s.resolve("new", "v1", 10)
	s.have("have", manifest.KindModel, "v1", 10, days)
	s.have("old", manifest.KindModel, "v1", 10, days)
	s.in.Root.ReadOnly = true
	p := plan.Plan(s.build())
	if got := row(t, p, "have"); got.Action != plan.ActionKeep {
		t.Errorf("have = %+v", got)
	}
	for _, id := range []string{"old", "new"} {
		if got := row(t, p, id); got.Action != plan.ActionSkip || got.Reason != plan.ReasonReadOnly {
			t.Errorf("%s = %+v, want skip/read_only", id, got)
		}
	}
	if !p.ReadOnly || p.Totals().Items != 0 {
		t.Errorf("read_only %v totals %+v", p.ReadOnly, p.Totals())
	}
}

func TestDriveRoot(t *testing.T) {
	driveInput := func(total int64) *scenario {
		s := newScenario([]catalog.Model{model("a", 1, 40), model("b", 2, 40), model("c", 3, 40)}, nil)
		s.in.Root = plan.RootInfo{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/media/x/localai", TotalBytes: total, FreeBytes: total}
		s.in.Profile.Storage.BudgetBytes = 1 // a drive ignores the profile's figures
		s.resolveAll()
		return s
	}
	t.Run("min_free is 2 percent, at least 5 GB; the budget is the rest", func(t *testing.T) {
		const gb = int64(1_000_000_000)
		for _, tc := range []struct{ total, minFree int64 }{{100 * gb, 5 * gb}, {2000 * gb, 40 * gb}, {250 * gb, 5 * gb}, {300 * gb, 6 * gb}} {
			p := plan.Plan(driveInput(tc.total).build())
			if p.MinFreeBytes != tc.minFree || p.BudgetBytes != tc.total-tc.minFree {
				t.Errorf("total %d: min_free %d budget %d, want %d and %d", tc.total, p.MinFreeBytes, p.BudgetBytes, tc.minFree, tc.total-tc.minFree)
			}
		}
	})
	t.Run("a drive too small for everything skips the lower tiers with LAI-ARK-011", func(t *testing.T) {
		s := driveInput(1)
		s.in.Root.TotalBytes = 5_000_000_000 + 90 // budget = 90: a and b fit, c does not
		s.in.Root.FreeBytes = s.in.Root.TotalBytes
		p := plan.Plan(s.build())
		if p.BudgetBytes != 90 {
			t.Fatalf("budget = %d", p.BudgetBytes)
		}
		if row(t, p, "a").Action != plan.ActionDownload || row(t, p, "b").Action != plan.ActionDownload || row(t, p, "c").Reason != plan.ReasonBudget {
			t.Fatalf("plan = %+v", p.Items)
		}
		if !p.HasFinding(plan.CodeDriveTooSmall) || p.HasFinding(plan.CodeBudgetFull) {
			t.Errorf("findings = %+v, want LAI-ARK-011 and no LAI-SYN-041 on a drive", p.Findings)
		}
	})
	t.Run("an override budget applies to a drive too", func(t *testing.T) {
		s := driveInput(2000_000_000_000)
		b := int64(50)
		s.in.Overrides = &plan.ProfileOverrides{BudgetBytes: &b}
		if p := plan.Plan(s.build()); p.BudgetBytes != 50 {
			t.Errorf("budget = %d", p.BudgetBytes)
		}
	})
}

func TestNoProfileMeansEverythingIsAnOrphan(t *testing.T) {
	s := newScenario([]catalog.Model{model("m", 1, 10)}, nil)
	s.have("m", manifest.KindModel, "v1", 10, days)
	in := s.build()
	in.Profile = nil
	p := plan.Plan(in)
	if len(p.Items) != 1 || p.Items[0].Action != plan.ActionOrphan {
		t.Fatalf("plan = %+v", p.Items)
	}
	// And a nil catalog does not panic either.
	in.Catalog = nil
	_ = plan.Plan(in)
}

// TestNeverProposesDeletion is the promise of AGENTS.md rule 3 as a property: whatever the input, a
// verified copy is never dropped from the plan, and no action means "delete".
func TestNeverProposesDeletion(t *testing.T) {
	allowed := map[plan.Action]bool{plan.ActionKeep: true, plan.ActionDownload: true, plan.ActionUpdate: true, plan.ActionSkip: true, plan.ActionOrphan: true}
	for i := 0; i < 64; i++ {
		s := newScenario([]catalog.Model{model("m1", 1, 30), model("m2", 2, 30), model("m3", 3, 30)}, []catalog.ContentItem{zim("w1", 30), kit("k1", 30), docs(10)})
		s.in.Profile.Storage.BudgetBytes = int64(10 + (i%7)*20)
		s.in.Offline = i&1 != 0
		s.in.Root.ReadOnly = i&2 != 0
		s.in.Root.FreeBytes = int64(20 + (i%5)*40)
		for j, id := range []string{"m1", "m2", "m3", "w1", "k1", "docs-bundle", "stray"} {
			if (i>>uint(j%5))&1 == 1 || id == "stray" {
				s.have(id, "model", "v0", 25, time.Duration(i)*days)
			}
			if i%3 != 0 {
				s.resolve(id, "v1", 30)
			}
		}
		delete(s.in.Resolved, "stray")
		p := plan.Plan(s.build())
		for id := range s.in.Present {
			it, ok := p.Item(id)
			if !ok {
				t.Fatalf("case %d: verified copy %s vanished from the plan", i, id)
			}
			if !allowed[it.Action] {
				t.Fatalf("case %d: %s has action %q", i, id, it.Action)
			}
			if it.Action == plan.ActionSkip && it.FromVersion == "" {
				t.Errorf("case %d: %s is skipped but its present copy is not reported", i, id)
			}
		}
		for _, it := range p.Items {
			if !allowed[it.Action] {
				t.Fatalf("case %d: action %q is not in the vocabulary", i, it.Action)
			}
		}
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	build := func() plan.PlanInput {
		s := newScenario([]catalog.Model{model("a", 1, 10), model("b", 2, 20), model("c", 2, 20)}, []catalog.ContentItem{zim("w", 30), kit("k", 5), docs(1)})
		s.survive = []string{"b"}
		s.resolveAll()
		for _, id := range []string{"o3", "o1", "o2", "w"} {
			s.have(id, "kiwix", "v0", 1, days)
		}
		s.in.Trash = []plan.TrashEntry{
			{ItemID: "t2", Version: "x", Bytes: 1, Eligible: true, TrashedAt: now},
			{ItemID: "t1", Version: "x", Bytes: 1, Eligible: true, TrashedAt: now},
		}
		return s.build()
	}
	first, err := plan.Plan(build()).MarshalIndented()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, _ := plan.Plan(build()).MarshalIndented()
		if string(got) != string(first) {
			t.Fatalf("run %d differs:\n%s\n--- vs ---\n%s", i, got, first)
		}
	}
}

func TestFromCatalog(t *testing.T) {
	cases := []struct {
		name    string
		item    plan.CatalogItem
		ok      bool
		version string
		size    int64
	}{
		{name: "model revision", item: plan.ModelItem(&catalog.Model{ID: "m", Engine: "llama", Source: catalog.ModelSource{Type: "huggingface", Revision: "0123456789abcdef0123"},
			Files: []catalog.ModelFile{{SizeBytes: 7}, {SizeBytes: 3}}}), ok: true, version: "0123456789ab", size: 10},
		{name: "pinned kiwix file", item: plan.ContentItem(&catalog.ContentItem{ID: "w", Kind: "kiwix", ExpectedSizeBytes: 5,
			Source: catalog.ContentSource{Type: "kiwix", VersionPolicy: "pinned", Pinned: &catalog.KiwixPinned{File: "book_2026-09.zim"}}}), ok: true, version: "2026-09", size: 5},
		{name: "latest kiwix needs a resolver", item: plan.ContentItem(&catalog.ContentItem{ID: "w", Kind: "kiwix", Source: latestZIM})},
		{name: "pinned url", item: plan.ContentItem(&catalog.ContentItem{ID: "u", Kind: "kit", Source: catalog.ContentSource{Type: "url", SHA256: "abcdefghijklmnop"}}), ok: true, version: "abcdefghijkl"},
		{name: "latest url needs a resolver", item: plan.ContentItem(&catalog.ContentItem{ID: "u", Kind: "maps", Source: catalog.ContentSource{Type: "url", VersionPolicy: "latest", SHA256: "abc"}})},
		{name: "release tag", item: plan.ContentItem(&catalog.ContentItem{ID: "r", Kind: "kit", Source: catalog.ContentSource{Type: "github_release", Tag: "v1.2"}}), ok: true, version: "v1.2"},
		{name: "docs follow the catalog version", item: plan.ContentItem(&catalog.ContentItem{ID: "docs-bundle", Kind: "docs", Source: catalog.ContentSource{Type: "repo_docs"}}), ok: true, version: "catalog-" + catVersion},
		{name: "git mirror needs a resolver", item: plan.ContentItem(&catalog.ContentItem{ID: "g", Kind: "kit", Source: catalog.ContentSource{Type: "git_mirror"}})},
		{name: "apt closure needs a resolver", item: plan.ContentItem(&catalog.ContentItem{ID: "a", Kind: "kit", Source: catalog.ContentSource{Type: "apt_closure"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := plan.FromCatalog(tc.item, catVersion)
			if ok != tc.ok || got.Version != tc.version || got.SizeBytes != tc.size {
				t.Errorf("FromCatalog = %+v, %v; want %q size %d, %v", got, ok, tc.version, tc.size, tc.ok)
			}
		})
	}
}
