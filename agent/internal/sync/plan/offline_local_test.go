package plan_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The network rule of a run (#275): the run's own verdict on the network replaces the gatherer's
// reading of the mode, and an item whose producer can work from material on a root is still planned
// when the node may not use the network.

func TestNetworkMayUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		net   plan.Network
		allow bool
		want  bool
	}{
		{"online, reachable", plan.Network{Mode: plan.ModeOnline, Online: true}, false, true},
		{"online, unknown mode treated as online", plan.Network{Online: true}, false, true},
		{"online mode, no internet", plan.Network{Mode: plan.ModeOnline}, false, false},
		{"online mode, no internet, allow flag does not conjure it", plan.Network{Mode: plan.ModeOnline}, true, false},
		{"offline mode, reachable, no flag", plan.Network{Mode: plan.ModeOffline, Online: true}, false, false},
		{"offline mode, reachable, allow_offline_mode", plan.Network{Mode: plan.ModeOffline, Online: true}, true, true},
		{"offline mode, no internet, allow_offline_mode", plan.Network{Mode: plan.ModeOffline}, true, false},
		{"survival never, even with the flag", plan.Network{Mode: plan.ModeSurvival, Online: true}, true, false},
	} {
		if got := tc.net.MayUse(tc.allow); got != tc.want {
			t.Errorf("%s: MayUse = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// localDocs is a repo_docs resolver that can also work from a local mirror: the planner half calls
// are counted, and the local half answers only when mirror is set. It fails the test on any call that
// would be a network call.
type localDocs struct {
	t        *testing.T
	mirror   bool
	resolves atomic.Int32
	locals   atomic.Int32
	roots    []string
}

func (l *localDocs) Resolve(context.Context, plan.CatalogItem) (plan.Resolved, error) {
	l.resolves.Add(1)
	return plan.Resolved{Version: "catalog-" + catVersion + "+inv", SizeBytes: 5}, nil
}

func (l *localDocs) ResolveLocal(_ context.Context, _ plan.CatalogItem, roots []string) (plan.Resolved, bool) {
	l.locals.Add(1)
	l.roots = roots
	if !l.mirror {
		return plan.Resolved{}, false
	}
	return plan.Resolved{Version: "catalog-" + catVersion, SizeBytes: 5}, true
}

func TestGatherOnlineOverride(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name        string
		net         plan.Network
		online      *bool
		wantOffline bool
		wantMode    string
	}{
		{"nil keeps the gatherer's reading: offline mode", plan.Network{Mode: plan.ModeOffline, Online: true}, nil, true, plan.ModeOffline},
		{"allow_offline_mode run: online wins over the offline mode", plan.Network{Mode: plan.ModeOffline, Online: true}, &yes, false, plan.ModeOnline},
		{"no internet wins over an online mode", plan.Network{Mode: plan.ModeOnline, Online: false}, &no, true, plan.ModeOnline},
		{"a run that may not use the network stays offline in survival", plan.Network{Mode: plan.ModeSurvival, Online: true}, &no, true, plan.ModeSurvival},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGatherRig(t)
			r.network = tc.net
			in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Online: tc.online})
			if err != nil || in.Offline != tc.wantOffline || in.Mode != tc.wantMode {
				t.Errorf("offline %v mode %q err %v, want offline %v mode %q", in.Offline, in.Mode, err, tc.wantOffline, tc.wantMode)
			}
		})
	}

	// The resolvers run in an allow_offline_mode run: the mode says offline, the run says online.
	t.Run("resolvers run when the run overrides the mode", func(t *testing.T) {
		var calls atomic.Int32
		r := newGatherRig(t)
		if err := r.res.Register("kiwix", plan.ResolverFunc(func(context.Context, plan.CatalogItem) (plan.Resolved, error) {
			calls.Add(1)
			return plan.Resolved{Version: "2026-10", SizeBytes: 7}, nil
		})); err != nil {
			t.Fatal(err)
		}
		r.network = plan.Network{Mode: plan.ModeOffline, Online: true}
		in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true})
		if err != nil || calls.Load() != 0 {
			t.Fatalf("without the override the offline mode must not resolve: calls %d err %v", calls.Load(), err)
		}
		in, err = r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true, Online: &yes})
		if err != nil || calls.Load() != 1 || in.Resolved["w-latest"].Version != "2026-10" {
			t.Fatalf("with the override the resolver must run: calls %d resolved %+v err %v", calls.Load(), in.Resolved["w-latest"], err)
		}
	})
}

func TestGatherLocalResolverOffline(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name       string
		online     *bool
		mirror     bool
		wantLocal  bool   // marked in LocalBuild
		wantCalls  int32  // network-side Resolve calls
		wantLocals int32  // ResolveLocal calls
		wantVer    string // Resolved["docs-bundle"].Version
	}{
		{"offline with a mirror: the local answer", &no, true, true, 0, 1, "catalog-" + catVersion},
		{"offline without a mirror: the catalog pin, not buildable", &no, false, false, 0, 1, "catalog-" + catVersion},
		{"online: the resolver answers, the local half is never asked", &yes, true, false, 1, 0, "catalog-" + catVersion + "+inv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGatherRig(t)
			ld := &localDocs{t: t, mirror: tc.mirror}
			if err := r.res.Register("repo_docs", ld); err != nil {
				t.Fatal(err)
			}
			in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Resolve: true, Online: tc.online})
			if err != nil {
				t.Fatal(err)
			}
			if got := in.LocalBuild["docs-bundle"]; got != tc.wantLocal {
				t.Errorf("LocalBuild = %v, want %v", got, tc.wantLocal)
			}
			if ld.resolves.Load() != tc.wantCalls || ld.locals.Load() != tc.wantLocals {
				t.Errorf("network resolves %d (want %d), local resolves %d (want %d)", ld.resolves.Load(), tc.wantCalls, ld.locals.Load(), tc.wantLocals)
			}
			if got := in.Resolved["docs-bundle"].Version; got != tc.wantVer {
				t.Errorf("version = %q, want %q", got, tc.wantVer)
			}
			if tc.wantLocals > 0 && len(ld.roots) != 2 {
				t.Errorf("the local half was given the roots %v, want the internal root and the drive", ld.roots)
			}
		})
	}

	t.Run("without Resolve the local half is not asked", func(t *testing.T) {
		r := newGatherRig(t)
		ld := &localDocs{t: t, mirror: true}
		if err := r.res.Register("repo_docs", ld); err != nil {
			t.Fatal(err)
		}
		in, err := r.gatherer().Gather(context.Background(), ark.InternalID, plan.GatherOptions{Online: &no})
		if err != nil || ld.locals.Load() != 0 || len(in.LocalBuild) != 0 {
			t.Errorf("locals %d LocalBuild %v err %v", ld.locals.Load(), in.LocalBuild, err)
		}
	})
}

// Offline, an item the producer can build locally is a download with source local_build; an item
// that needs the network is skipped as unresolved; a local copy still wins over a local build.
func TestPlanOfflineLocalBuild(t *testing.T) {
	mk := func() *scenario {
		s := newScenario(nil, []catalog.ContentItem{docs(5), zim("w-latest", 10)})
		s.in.Offline = true
		s.in.Mode = plan.ModeOnline
		s.resolve("docs-bundle", "catalog-"+catVersion, 5)
		return s
	}
	t.Run("marked: planned as a local build", func(t *testing.T) {
		s := mk()
		s.in.LocalBuild = map[string]bool{"docs-bundle": true}
		p := plan.Plan(s.build())
		d := row(t, p, "docs-bundle")
		if d.Action != plan.ActionDownload || d.Source != plan.SourceLocalBuild || d.FromRoot != "" || d.ToVersion != "catalog-"+catVersion {
			t.Errorf("docs-bundle = %+v", d)
		}
		if w := row(t, p, "w-latest"); w.Action != plan.ActionSkip || w.Reason != plan.ReasonUnresolved {
			t.Errorf("w-latest needs the network offline: %+v", w)
		}
		if tot := p.Totals(); tot.NetworkBytes != 0 || tot.LocalBytes != 5 {
			t.Errorf("totals = %+v: a local build is not from the internet", tot)
		}
		if !p.Offline {
			t.Error("the plan must say it was made offline")
		}
		if txt := plan.Render(p, "en"); !strings.Contains(txt, "built here from local files") {
			t.Errorf("the human plan does not say it is a local build:\n%s", txt)
		}
	})
	t.Run("not marked: skipped as before", func(t *testing.T) {
		p := plan.Plan(mk().build())
		if d := row(t, p, "docs-bundle"); d.Action != plan.ActionSkip || d.Reason != plan.ReasonUnresolved {
			t.Errorf("docs-bundle = %+v", d)
		}
	})
	t.Run("marked but online: an ordinary network download", func(t *testing.T) {
		s := mk()
		s.in.Offline = false
		s.in.LocalBuild = map[string]bool{"docs-bundle": true}
		if d := row(t, plan.Plan(s.build()), "docs-bundle"); d.Source != plan.SourceNetwork {
			t.Errorf("docs-bundle source = %q, want network", d.Source)
		}
	})
	t.Run("a verified copy on another root is preferred", func(t *testing.T) {
		s := mk()
		s.in.LocalBuild = map[string]bool{"docs-bundle": true}
		s.in.OtherRoots = map[string][]plan.RootCopy{"docs-bundle": {{RootID: "ark-3f9c0a12", Manifest: manifest.Manifest{ItemID: "docs-bundle", ItemKind: manifest.KindDocs, Version: "catalog-" + catVersion, TotalBytes: 5, DownloadedAt: now}}}}
		if d := row(t, plan.Plan(s.build()), "docs-bundle"); d.Source != plan.SourceLocalCopy || d.FromRoot != "ark-3f9c0a12" {
			t.Errorf("docs-bundle = %+v", d)
		}
	})
}

// A dry run follows the rule the engine follows: the offline mode plans offline, and plans online
// when the request carries allow_offline_mode and the internet is reachable; survival and a node with
// no internet never plan online.
func TestDryRunNetworkRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		net         plan.Network
		allow       bool
		wantOffline bool
		wantMode    string
	}{
		{"offline mode", plan.Network{Mode: plan.ModeOffline, Online: true}, false, true, plan.ModeOffline},
		{"offline mode, allowed", plan.Network{Mode: plan.ModeOffline, Online: true}, true, false, plan.ModeOnline},
		{"survival, allowed", plan.Network{Mode: plan.ModeSurvival, Online: true}, true, true, plan.ModeSurvival},
		{"no internet, allowed", plan.Network{Mode: plan.ModeOffline}, true, true, plan.ModeOffline},
		{"online", plan.Network{Mode: plan.ModeOnline, Online: true}, false, false, plan.ModeOnline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGatherRig(t)
			r.network = tc.net
			var resolves atomic.Int32
			if err := r.res.Register("kiwix", plan.ResolverFunc(func(context.Context, plan.CatalogItem) (plan.Resolved, error) {
				resolves.Add(1)
				return plan.Resolved{Version: "2026-10", SizeBytes: 7}, nil
			})); err != nil {
				t.Fatal(err)
			}
			got, err := plan.NewDryRun(r.gatherer()).DryRun(context.Background(), api.ArkSyncRequest{DryRun: true, Roots: []string{"internal"}, AllowOfflineMode: tc.allow})
			if err != nil {
				t.Fatal(err)
			}
			p := got.([]plan.RootPlan)[0]
			if p.Offline != tc.wantOffline || p.Mode != tc.wantMode {
				t.Errorf("offline %v mode %q, want offline %v mode %q", p.Offline, p.Mode, tc.wantOffline, tc.wantMode)
			}
			if (resolves.Load() > 0) == tc.wantOffline {
				t.Errorf("resolver calls %d with offline %v", resolves.Load(), tc.wantOffline)
			}
			// The JSON of the plan is what an operator reads: the same plan twice is the same bytes.
			again, _ := plan.NewDryRun(r.gatherer()).DryRun(context.Background(), api.ArkSyncRequest{DryRun: true, Roots: []string{"internal"}, AllowOfflineMode: tc.allow})
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(again)
			if string(a) != string(b) {
				t.Error("two dry runs of one state differ")
			}
		})
	}
}
