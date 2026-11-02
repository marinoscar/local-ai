package catalog

import (
	"errors"
	"testing"
)

func TestValidFixtureLintsClean(t *testing.T) {
	rep := Lint(validDir(t), LintOptions{})
	if !rep.Valid() {
		t.Fatalf("the valid fixture has problems:\n%s", problemsText(rep))
	}
	if rep.Version != "2026.11.02-1" || rep.Models != 6 || rep.Content != 12 || rep.Profiles != 5 {
		t.Errorf("report counts: %+v", rep)
	}
}

func TestLoadAndLookups(t *testing.T) {
	cat, err := Load(validDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if cat.Version != "2026.11.02-1" {
		t.Errorf("version %q", cat.Version)
	}
	m, ok := cat.Model("model-beta-27b-q4")
	if !ok || m.Llama == nil || !m.Llama.NGPULayers.All || m.Llama.Speculative.DraftNGL.N != 99 {
		t.Fatalf("model lookup: %+v", m)
	}
	if len(m.Summaries) != 0 {
		t.Errorf("model-beta has no translations, got %v", m.Summaries)
	}
	alpha, _ := cat.Model("model-alpha-9b-q4")
	if alpha.Summaries["summary_es"] == "" {
		t.Errorf("summary_es not captured: %v", alpha.Summaries)
	}
	if _, ok := cat.Model("nope"); ok {
		t.Error("unknown model found")
	}
	c, ok := cat.ContentItem("medical-alpha-en")
	if !ok || len(c.PortalLinks) != 2 || c.PortalLinks[0].ESItem != "wiki-alpha-es" {
		t.Fatalf("content lookup: %+v", c)
	}
	if _, ok := cat.ContentItem("model-alpha-9b-q4"); ok {
		t.Error("a model id must not resolve as content")
	}
	p, ok := cat.Profile("ark-drive-full")
	if !ok || p.Match != nil || p.Storage.Target != "ark_drive" {
		t.Fatalf("profile lookup: %+v", p)
	}
	if got := len(cat.Profiles); got != 5 {
		t.Errorf("profiles: %d", got)
	}
	kit, _ := cat.ContentItem("kit-llama-cpp")
	if kit.Tier() != 1 {
		t.Errorf("kit tier %d", kit.Tier())
	}
	docs, _ := cat.ContentItem("docs-bundle")
	if docs.Tier() != 1 {
		t.Errorf("docs tier %d", docs.Tier())
	}
	iso, _ := cat.ContentItem("kit-ubuntu-iso-26.04")
	if iso.Tier() != 1 {
		t.Errorf("kit without tier must default to 1, got %d", iso.Tier())
	}
}

func TestLoadRejectsInvalidCatalog(t *testing.T) {
	dir := validDir(t)
	applyEdits(t, dir, []edit{{"models.yaml", "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	cat, err := Load(dir)
	if cat != nil || err == nil {
		t.Fatalf("Load accepted an invalid catalog: %v %v", cat, err)
	}
	var le *LoadError
	if !errors.As(err, &le) || le.Code() != CodeUnparseable || len(le.Report.Problems) != 1 {
		t.Fatalf("error %v", err)
	}
}

func TestLoadMissingDirectory(t *testing.T) {
	_, err := Load(t.TempDir() + "/nope")
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("want LoadError, got %v", err)
	}
	for _, f := range []string{FileVersion, FileModels, FileArk, DirProfiles} {
		if !has(le.Report, CodeLayout, f, "") {
			t.Errorf("no layout problem for %s:\n%s", f, problemsText(le.Report))
		}
	}
}

func TestEmptyCatalog(t *testing.T) {
	e := Empty()
	if e.Version != "" || len(e.Models) != 0 {
		t.Errorf("%+v", e)
	}
	if _, ok := e.Model("x"); ok {
		t.Error("empty catalog resolved a model")
	}
}

func TestVersions(t *testing.T) {
	valid := []string{"2026.11.02-1", "2026.02.28-12", "2024.02.29-3"}
	invalid := []string{"", "2026.11.02", "2026.11.02-0", "2026.11.02-01", "2026.13.02-1", "2026.02.30-1", "2025.02.29-1", "26.11.02-1", "2026.11.02-1 ", "v2026.11.02-1", "2026.11.02-1234567890"}
	for _, v := range valid {
		if !ValidVersion(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range invalid {
		if ValidVersion(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
	cmp := []struct {
		a, b string
		want int
	}{
		{"2026.11.02-1", "2026.11.02-1", 0},
		{"2026.11.02-2", "2026.11.02-10", -1}, // numeric, not lexical
		{"2026.11.03-1", "2026.11.02-9", 1},
		{"2025.12.31-1", "2026.01.01-1", -1},
		{"garbage", "2026.11.02-1", -1},
		{"2026.11.02-1", "garbage", 1},
	}
	for _, c := range cmp {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
