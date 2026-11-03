package ready_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

func TestReadyPercent(t *testing.T) {
	tests := []struct {
		name   string
		counts ready.Counts
		pct    int
		ok     bool
	}{
		{"nothing wanted", ready.Counts{Orphan: 4}, 0, false},
		{"all verified", ready.Counts{Verified: 5}, 100, true},
		{"a few missing", ready.Counts{Verified: 24, Stale: 1, Skipped: 3}, 85, true},
		{"orphans are not wanted", ready.Counts{Verified: 1, Missing: 1, Orphan: 10}, 50, true},
		{"none verified", ready.Counts{Failed: 2, Quarantined: 1}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, ok := ready.Report{Counts: tt.counts}.ReadyPercent()
			if pct != tt.pct || ok != tt.ok {
				t.Errorf("ReadyPercent = %d %v, want %d %v", pct, ok, tt.pct, tt.ok)
			}
		})
	}
}

// TestStatusView: what /status shows of the ark: one row per root with the colour, the percent and the
// time of the report, whether a drive is mounted, the last success and the gauges.
func TestStatusView(t *testing.T) {
	done := now.Add(-3 * day)
	r := newRig(t, func(d *ready.Deps) {
		d.Runs = fakeRuns{done: map[string]time.Time{"internal": now.Add(-day), driveID: done}}
		d.SyncState = func() string { return "running" }
	})
	r.driveW.without("map-b")
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	info := r.svc.Ark()
	if len(info.Roots) != 2 || info.Roots[0].Kind != "internal" || info.Roots[1].Kind != "drive" || !info.DriveMounted {
		t.Fatalf("roots = %+v drive %v", info.Roots, info.DriveMounted)
	}
	if info.Roots[0].Status != "green" || info.Roots[0].ReadyPct == nil || *info.Roots[0].ReadyPct != 100 || !info.Roots[0].UpdatedAt.Equal(now) {
		t.Errorf("internal = %+v", info.Roots[0])
	}
	if info.Roots[1].Path != r.roots[1].Path || *info.Roots[1].ReadyPct != 90 {
		t.Errorf("drive = %+v", info.Roots[1])
	}
	if info.SyncState != "running" || !info.LastSuccessAt.Equal(now.Add(-day)) {
		t.Errorf("sync = %q %v", info.SyncState, info.LastSuccessAt)
	}
	names := map[string]int{}
	for _, m := range info.Metrics {
		names[m.Name]++
	}
	if len(info.Metrics) != 6 || names[ready.MetricRootOverall] != 2 || names[ready.MetricRootFreeBytes] != 2 || names[ready.MetricReverifyAgeDaysMax] != 2 {
		t.Errorf("metrics = %+v", info.Metrics)
	}

	empty := newRig(t, func(d *ready.Deps) { d.Roots = func() []ark.Root { return nil } })
	if info := empty.svc.Ark(); len(info.Roots) != 0 || info.DriveMounted || info.SyncState != "" {
		t.Errorf("an empty node = %+v", info)
	}
}

func TestMetricsOf(t *testing.T) {
	w := survivalWorld()
	w.have("brain", "v1", 12*day)
	w.reverified("brain", 12*day)
	w.free = 105 * gb
	rep := w.build()
	got := ready.MetricsOf([]ready.Report{rep}, now)
	want := []ready.Metric{
		{Name: ready.MetricRootOverall, Labels: map[string]string{"root": "internal"}, Value: 0},
		{Name: ready.MetricRootFreeBytes, Labels: map[string]string{"root": "internal"}, Value: float64(105 * gb)},
		{Name: ready.MetricReverifyAgeDaysMax, Labels: map[string]string{"root": "internal"}, Value: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("metrics = %+v, want %+v", got, want)
	}
	red := ready.Report{Overall: ready.Red, Root: ready.RootBlock{ID: "ark-3f9c0a12"}}
	got = ready.MetricsOf([]ready.Report{red}, now)
	if got[0].Value != 2 || got[2].Value != 0 {
		t.Errorf("a red report with no item: %+v", got)
	}
	if len(ready.MetricsOf(nil, now)) != 0 {
		t.Error("no report, no metric")
	}
}

// TestContentState: what the catalog's content rows show: present and verified, and the best status
// when the roots disagree.
func TestContentState(t *testing.T) {
	r := newRig(t)
	r.intW.without("wiki-es")
	r.driveW.without("map-b")
	for i := range r.driveW.content {
		if r.driveW.content[i].ID == "wiki" {
			r.driveW.content[i].Source.VersionPolicy = "pinned"
		}
	}
	r.driveW.have("wiki", "v0", day).target("wiki", "v1", 20)
	r.driveW.haveOrphan("old-book", "kiwix", day)
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id      string
		present bool
		valid   bool
		status  string
	}{
		{"brain", true, true, "verified"},
		{"wiki", true, true, "verified"}, // verified on the internal root, stale on the drive: the best wins
		{"wiki-es", true, true, "verified"},
		{"map-b", true, true, "verified"},
		{"old-book", true, false, "orphan"},
		{"never-heard-of-it", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := r.svc.ContentState(tt.id)
			if got.Present != tt.present || got.Verified != tt.valid || got.Status != tt.status {
				t.Errorf("ContentState = %+v, want present %v verified %v status %q", got, tt.present, tt.valid, tt.status)
			}
		})
	}

	// Only the drive has it, and it is stale there.
	r.intW.without("wiki")
	_ = r.svc.Regenerate(context.Background(), ready.TriggerRun, "internal")
	if got := r.svc.ContentState("wiki"); !got.Present || got.Verified || got.Status != "stale" {
		t.Errorf("stale only: %+v", got)
	}
	// Missing everywhere: the status says so, and it is not present.
	r.driveW.without("wiki")
	_ = r.svc.Regenerate(context.Background(), ready.TriggerRun)
	if got := r.svc.ContentState("wiki"); got.Present || got.Verified || got.Status != "missing" {
		t.Errorf("missing: %+v", got)
	}
}

// TestIndexDocs: what the docs index says about the manuals.
func TestIndexDocs(t *testing.T) {
	tests := []struct {
		name  string
		index string // "" means no file
		want  ready.DocsFacts
	}{
		{"no index", "", ready.DocsFacts{Known: true, HTMLMissing: true}},
		{"not JSON", "{", ready.DocsFacts{Known: true, HTMLMissing: true}},
		{"no documents", `{"docs": []}`, ready.DocsFacts{Known: true, HTMLMissing: true}},
		{"complete", `{"docs": [{"kind": "card", "html": "/docs/survival-card-ai-n3.html", "pdf": "/docs/survival-card-ai-n3.pdf"}, {"kind": "runbook", "html": "/docs/01.html", "pdf": null}]}`, ready.DocsFacts{Known: true}},
		{"card without a PDF", `{"docs": [{"kind": "card", "html": "/docs/c.html", "pdf": null}]}`, ready.DocsFacts{Known: true, CardPDFMissing: true}},
		{"card with an empty PDF", `{"docs": [{"kind": "card", "html": "/docs/c.html", "pdf": ""}]}`, ready.DocsFacts{Known: true, CardPDFMissing: true}},
		{"no card at all", `{"docs": [{"kind": "runbook", "html": "/docs/01.html"}]}`, ready.DocsFacts{Known: true, CardPDFMissing: true}},
		{"a page without HTML", `{"docs": [{"kind": "card", "html": "", "pdf": "/docs/c.pdf"}]}`, ready.DocsFacts{Known: true, HTMLMissing: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.index != "" {
				if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "docs", "index.json"), []byte(tt.index), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := ready.IndexDocs{FS: config.OSFS{}}.Docs(ark.Root{Path: dir})
			if got != tt.want {
				t.Errorf("facts = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestDocsProbeReachesTheReport: with the default probe, a docs item whose index lacks the card PDF is
// yellow with LAI-CLI-014; a root with no index at all is red.
func TestDocsProbeReachesTheReport(t *testing.T) {
	r := newRig(t, func(d *ready.Deps) { d.Docs = nil })
	r.intW.withContent(docs(10)).have("docs-bundle", "v1", day)
	root := r.rootPath("internal")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	index := `{"docs": [{"kind": "card", "html": "/docs/c.html", "pdf": null}]}`
	if err := os.WriteFile(filepath.Join(root, "docs", "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Regenerate(context.Background(), ready.TriggerManual, "internal"); err != nil {
		t.Fatal(err)
	}
	rep, _ := r.svc.Report("internal")
	if it := item(t, rep, "docs-bundle"); it.Code != "LAI-CLI-014" || it.Freshness != ready.Yellow {
		t.Errorf("docs item = %s %s", it.Code, it.Freshness)
	}
	if err := os.Remove(filepath.Join(root, "docs", "index.json")); err != nil {
		t.Fatal(err)
	}
	_ = r.svc.Regenerate(context.Background(), ready.TriggerRun, "internal")
	rep, _ = r.svc.Report("internal")
	if it := item(t, rep, "docs-bundle"); it.Freshness != ready.Red || rep.Overall != ready.Red {
		t.Errorf("docs item without an index = %s overall %s", it.Freshness, rep.Overall)
	}
}
