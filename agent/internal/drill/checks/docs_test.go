package checks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

const (
	testNode    = "ai-test"
	testCatalog = "2026.11.01-1"
)

func cardHTML(extra string) []byte {
	return []byte("<html><body><h1>Survival card ai-test</h1><p>Call 911</p>" + extra + "</body></html>")
}

// docsFiles is a complete docs item for testNode: the three manuals, the card with its PDF, and the index
// that lists them.
func docsFiles() map[string][]byte {
	idx := `{"root":"/docs","rendered_at":"2026-11-01T00:00:00Z","docs":[` +
		`{"slug":"survival-card","kind":"card","html":"/docs/survival-card-ai-test.html","pdf":"/docs/survival-card-ai-test.pdf"},` +
		`{"slug":"01-survival-mode","kind":"runbook","html":"/docs/01-survival-mode.html","pdf":null},` +
		`{"slug":"06-rebuild-a-node-offline","kind":"runbook","html":"/docs/06-rebuild-a-node-offline.html","pdf":null},` +
		`{"slug":"02-extra","kind":"runbook","html":"/docs/02-extra.html","pdf":null}]}`
	return map[string][]byte{
		"index.html":                     []byte("<html>Manuals</html>"),
		"index.json":                     []byte(idx),
		"01-survival-mode.html":          []byte("<html>Survival mode</html>"),
		"06-rebuild-a-node-offline.html": []byte("<html>Rebuild</html>"),
		"02-extra.html":                  []byte("<html>Extra</html>"),
		"survival-card-ai-test.html":     cardHTML(""),
		"survival-card-ai-test.pdf":      []byte("%PDF-1.4 card"),
	}
}

func (r *contentRig) installDocs(files map[string][]byte, catalog string) string {
	r.t.Helper()
	return r.install(ark.InternalID, itemSpec{ref: refDocs, tier: 1, files: files, catalog: catalog})
}

func (r *contentRig) docsCheck(catalog string) drill.Check {
	return NewDocsCheck(DocsDeps{
		Roots: r.rootList, FS: config.OSFS{}, NodeID: func() string { return testNode },
		CatalogVersion: func() string { return catalog },
	})
}

func TestDocsCheck(t *testing.T) {
	cases := []struct {
		name    string
		files   func(map[string][]byte)
		after   func(t *testing.T, dir string)
		catalog string // the catalog the agent runs with
		result  drill.Result
		code    string
		missing string // a file that must be named in details.missing
		problem string // a problem that must be named in details.problems
	}{
		{name: "complete and current", result: drill.ResultPass},
		{name: "index.html deleted", after: rm("index.html"), result: drill.ResultFail, code: doctor.DRL060, missing: "index.html"},
		{name: "06-rebuild-a-node-offline.html deleted", after: rm("06-rebuild-a-node-offline.html"), result: drill.ResultFail, code: doctor.DRL060, missing: "06-rebuild-a-node-offline.html"},
		{name: "01-survival-mode.html deleted", after: rm("01-survival-mode.html"), result: drill.ResultFail, code: doctor.DRL060, missing: "01-survival-mode.html"},
		{name: "this node's card deleted", after: rm("survival-card-ai-test.html"), result: drill.ResultFail, code: doctor.DRL060, missing: "survival-card-ai-test.html"},
		{name: "a page the index lists is gone", after: rm("02-extra.html"), result: drill.ResultFail, code: doctor.DRL060, missing: "02-extra.html"},
		{name: "a page is empty", files: func(f map[string][]byte) { f["index.html"] = nil }, result: drill.ResultFail, code: doctor.DRL060, missing: "index.html"},
		{name: "index.json deleted", after: rm("index.json"), result: drill.ResultFail, code: doctor.DRL060, missing: "index.json"},
		{name: "index.json damaged", files: func(f map[string][]byte) { f["index.json"] = []byte("{") }, result: drill.ResultFail, code: doctor.DRL060, missing: "index.json (damaged)"},
		{name: "only the card PDF deleted", after: rm("survival-card-ai-test.pdf"), result: drill.ResultWarn, code: doctor.CLI014, problem: "card_pdf_missing"},
		{name: "rendered for an older catalog", catalog: "2026.12.01-1", result: drill.ResultWarn, code: doctor.SYN062, problem: "older_catalog"},
		{name: "placeholders left in the card", files: func(f map[string][]byte) {
			f["survival-card-ai-test.html"] = cardHTML("<p>Tailnet: &lt;TAILNET&gt; IP &lt;TAILNET_IP_AI_N1&gt;</p>")
		}, result: drill.ResultWarn, code: doctor.SYN062, problem: "placeholders"},
		{name: "older catalog and no PDF: the manuals problem comes first", catalog: "2026.12.01-1", after: rm("survival-card-ai-test.pdf"), result: drill.ResultWarn, code: doctor.SYN062, problem: "card_pdf_missing"},
		{name: "a missing page beats every warning", catalog: "2026.12.01-1", after: func(t *testing.T, dir string) {
			rm("survival-card-ai-test.pdf")(t, dir)
			rm("index.html")(t, dir)
		}, result: drill.ResultFail, code: doctor.DRL060, missing: "index.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newContentRig(t)
			files := docsFiles()
			if tc.files != nil {
				tc.files(files)
			}
			dir := r.installDocs(files, testCatalog)
			if tc.after != nil {
				tc.after(t, dir)
			}
			catalog := tc.catalog
			if catalog == "" {
				catalog = testCatalog
			}
			out := run(t, r.docsCheck(catalog))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if tc.missing != "" {
				if got := detailList(t, out.Details, "missing"); !contains(got, tc.missing) {
					t.Errorf("missing = %v, want %s", got, tc.missing)
				}
			}
			if tc.problem != "" {
				if got := detailList(t, out.Details, "problems"); !contains(got, tc.problem) {
					t.Errorf("problems = %v, want %s", got, tc.problem)
				}
			}
		})
	}
}

func rm(name string) func(*testing.T, string) {
	return func(t *testing.T, dir string) { mustRemove(t, filepath.Join(dir, name)) }
}

func TestDocsPlaceholderNamesNotValues(t *testing.T) {
	r := newContentRig(t)
	files := docsFiles()
	files["survival-card-ai-test.html"] = cardHTML("<p>&lt;VPS_PUBLIC_IP&gt;</p><p>&lt;VPS_PUBLIC_IP&gt;</p><p>&lt;ROUTE53_ZONE_ID&gt;</p>")
	r.installDocs(files, testCatalog)
	out := run(t, r.docsCheck(testCatalog))
	got := detailList(t, out.Details, "placeholders")
	if len(got) != 2 || got[0] != "ROUTE53_ZONE_ID" || got[1] != "VPS_PUBLIC_IP" {
		t.Errorf("placeholders = %v, want the two names once each, sorted", got)
	}
}

func TestDocsDriveIsNotRequiredAndInternalRootIs(t *testing.T) {
	r := newContentRig(t)
	r.installDocs(docsFiles(), testCatalog)
	r.unplug()
	if out := run(t, r.docsCheck(testCatalog)); out.Result != drill.ResultPass {
		t.Fatalf("no drive: %+v", out)
	}
	r.roots[0].State = ark.StateNotMounted
	if out := run(t, r.docsCheck(testCatalog)); out.Result != drill.ResultFail || out.Code != doctor.DRL060 {
		t.Fatalf("internal root not mounted: %+v", out)
	}
}

func TestDocsWiring(t *testing.T) {
	c := NewDocsCheck(DocsDeps{})
	if c.ID != "drl.docs" || !c.Critical || c.Order != 16 || c.FailCode != doctor.DRL060 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
	r := newContentRig(t)
	noNode := NewDocsCheck(DocsDeps{Roots: r.rootList, FS: config.OSFS{}, NodeID: func() string { return "" }})
	if out := run(t, noNode); out.Result != drill.ResultFail {
		t.Errorf("no node id: %+v", out)
	}
	// An unknown catalog version skips the comparison.
	r.installDocs(docsFiles(), "old")
	same := NewDocsCheck(DocsDeps{Roots: r.rootList, FS: config.OSFS{}, NodeID: func() string { return testNode }})
	if out := run(t, same); out.Result != drill.ResultPass {
		t.Errorf("no catalog to compare: %+v", out)
	}
	_ = context.Background
}
