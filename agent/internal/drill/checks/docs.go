package checks

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Doctor codes of the docs check.
const (
	// CodeDocsMissing: the manuals are missing (LAI-DRL-060).
	CodeDocsMissing = doctor.DRL060
	// CodeDocsPlaceholders: the manuals were rendered for an older catalog or without the real lab
	// values (LAI-SYN-062).
	CodeDocsPlaceholders = doctor.SYN062
	// CodeCardPDFMissing: the survival card has no PDF because no Chromium is installed (LAI-CLI-014).
	CodeCardPDFMissing = doctor.CLI014
)

// docsDir is the docs item directory below a root, and the pages check 16 requires in it. They are the
// flat paths of docs/index.json (agent.md section 3.8.29).
const docsDir = "docs"

var requiredDocs = []string{"index.html", "01-survival-mode.html", "06-rebuild-a-node-offline.html"}

// cardName is the file name of the survival card of a node, without extension (docsrender.CardFileName).
func cardName(nodeID string) string { return "survival-card-" + nodeID }

// DocsDeps are what check 16 needs.
type DocsDeps struct {
	// Roots lists the mounted roots; the check reads the internal one.
	Roots func() []ark.Root
	FS    config.FS
	// NodeID names this node's survival card.
	NodeID func() string
	// CatalogVersion is the version of the catalog the agent runs with; the manuals must have been
	// rendered for it. Nil or "" skips that comparison.
	CatalogVersion func() string
}

// unfilledRE matches a placeholder the render left in a page: <NAME> escaped as HTML text.
var unfilledRE = regexp.MustCompile(`&lt;([A-Z][A-Z0-9_]{1,63})&gt;`)

// maxCardBytes bounds the survival card that is scanned for placeholders.
const maxCardBytes = 2 << 20

// NewDocsCheck is check 16, drl.docs (critical): the manuals a person reads when there is nothing else
// are on the internal root, rendered for the current catalog.
//
//	pass: index.html, 01-survival-mode.html, 06-rebuild-a-node-offline.html, this node's
//	      survival-card-<node-id>.html and its .pdf are there, rendered for this catalog with the real
//	      lab values.
//	warn: rendered for an older catalog, or with placeholders left in the card (LAI-SYN-062); or the
//	      card has no PDF because there is no Chromium (LAI-CLI-014).
//	fail: an HTML page the index lists, or one of the four above, is missing (LAI-DRL-060).
func NewDocsCheck(d DocsDeps) drill.Check {
	return drill.Check{
		ID: "drl.docs", Area: "docs", Critical: true, Order: orderDocs, FailCode: CodeDocsMissing,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return runDocs(ctx, d) },
	}
}

func runDocs(ctx context.Context, d DocsDeps) drill.Outcome {
	if d.Roots == nil || d.FS == nil {
		return contentSkip("not_available", nil)
	}
	var root *ark.Root
	for _, r := range readableRoots(d.Roots()) {
		if r.Kind == ark.KindInternal {
			r := r
			root = &r
			break
		}
	}
	if root == nil {
		return contentFail(CodeDocsMissing, map[string]any{"reason": "internal_root_unavailable"})
	}
	node := ""
	if d.NodeID != nil {
		node = d.NodeID()
	}
	if node == "" {
		return contentFail(CodeDocsMissing, map[string]any{"reason": "node_id_unknown"})
	}
	dir := filepath.Join(root.Path, docsDir)

	present := func(name string) bool {
		fi, err := d.FS.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
	}
	var missing []string
	need := append(append([]string(nil), requiredDocs...), cardName(node)+".html")
	for _, name := range need {
		if !present(name) {
			missing = append(missing, name)
		}
	}
	// Every page the index lists must be there too: a manual that is listed and not served is the
	// first thing a person finds.
	listed, indexErr := indexedPages(d.FS, dir)
	switch {
	case errors.Is(indexErr, fs.ErrNotExist):
		missing = append(missing, "index.json")
	case indexErr != nil:
		missing = append(missing, "index.json (damaged)")
	default:
		for _, name := range listed {
			if !hasString(need, name) && !present(name) {
				missing = append(missing, name)
			}
		}
	}
	details := map[string]any{}
	if len(missing) > 0 {
		sort.Strings(missing)
		details["missing"] = missing
		return contentFail(CodeDocsMissing, details)
	}

	// Complete. Now how good is it?
	var warn []string
	code := ""
	if d.CatalogVersion != nil {
		if want := d.CatalogVersion(); want != "" {
			if m, err := manifest.Read(d.FS, manifest.Path(dir)); err == nil && m.CatalogVersion != "" && m.CatalogVersion != want {
				warn = append(warn, "older_catalog")
				details["rendered_for"], details["catalog"] = m.CatalogVersion, want
				code = CodeDocsPlaceholders
			}
		}
	}
	if names := cardPlaceholders(d.FS, filepath.Join(dir, cardName(node)+".html")); len(names) > 0 {
		warn = append(warn, "placeholders")
		details["placeholders"] = names
		code = CodeDocsPlaceholders
	}
	if !present(cardName(node) + ".pdf") {
		warn = append(warn, "card_pdf_missing")
		if code == "" {
			code = CodeCardPDFMissing
		}
	}
	if len(warn) > 0 {
		details["problems"] = warn
		return contentWarn(code, details)
	}
	return contentPass(map[string]any{"pages": len(need) + len(listed)})
}

// indexedPages returns the HTML pages docs/index.json lists, as file names inside docs/.
func indexedPages(fsys config.FS, dir string) ([]string, error) {
	b, err := fsys.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx struct {
		Docs []struct {
			HTML string `json:"html"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(b, &idx); err != nil || len(idx.Docs) == 0 {
		if err == nil {
			err = errors.New("the index lists no manual")
		}
		return nil, err
	}
	var out []string
	for _, d := range idx.Docs {
		name := path.Base(strings.TrimPrefix(d.HTML, "/docs/"))
		if d.HTML != "" && name != "." && name != "/" && strings.HasSuffix(name, ".html") {
			out = append(out, name)
		}
	}
	return out, nil
}

// cardPlaceholders returns the names of the placeholders left in the survival card, never a value.
func cardPlaceholders(fsys config.FS, file string) []string {
	if fi, err := fsys.Stat(file); err != nil || fi.Size() > maxCardBytes {
		return nil
	}
	b, err := fsys.ReadFile(file)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range unfilledRE.FindAllStringSubmatch(string(b), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func hasString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
