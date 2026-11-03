package ark

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// START-HERE.html, the page at the top level of every ark drive (ark-sync.md section 4.15, SYNC-14).
//
// It is the entry point to the rebuild guide on a bare machine, so it is a single self-contained file:
// inline CSS, no scripts, no external addresses and no images. Every link is relative to the page, so it
// works from any mount point and in any browser, and a link is written only when its target exists on
// the drive (a dead link on the one page a stranger opens is worse than a line that says "not here
// yet"). The text is bilingual, English first and Spanish under it.

//go:embed starthere.html.tmpl
var startHereTemplate string

// StartHereName is the file name of the page, at the drive's top level.
const StartHereName = "START-HERE.html"

// The relative targets of the page. The rendered manuals are flat: `<root>/docs/<slug>.html`
// (agent.md section 3.8.29).
const (
	startHereRebuildGuide = "localai/docs/06-rebuild-a-node-offline.html"
	startHereDocsIndex    = "localai/docs/index.html"
	startHereSurvival     = "localai/docs/01-survival-mode.html"
	startHereReady        = "localai/READY.json"
	startHereKiwixTools   = "localai/kit/tools/kiwix-tools"
	startHereKiwixLibrary = "localai/kiwix/library.xml"
	startHereDocsDir      = "localai/docs"
)

// StartHereItem is one row of the table of what the drive holds, taken from READY.json.
type StartHereItem struct {
	// ID is the catalog id; Name is its display name ("" shows the id).
	ID, Name string
	Version  string
	// Bytes is the size of the copy on the drive; zero shows "-".
	Bytes int64
	// Status is the READY.json item status: verified, stale, missing, skipped, failed, quarantined or
	// orphan.
	Status string
}

// StartHereInput is what the page says. It is the readiness report of the drive as it was when the page
// was written, reduced to what a person needs.
type StartHereInput struct {
	// Overall is the READY.json colour: green, yellow or red. Anything else shows "unknown".
	Overall         string
	SurvivalCapable bool
	// LastSync is when the last sync that finished ok ended; zero shows "never".
	LastSync       time.Time
	CatalogVersion string
	// WrittenBy is the node that wrote the report (READY.json generated_by.node_id).
	WrittenBy   string
	GeneratedAt time.Time
	Items       []StartHereItem
}

// StartHereCard is a survival card found on the drive.
type StartHereCard struct {
	NodeID string
	// Href is relative to the page: localai/docs/survival-card-<node-id>.pdf, or .html when no PDF was
	// rendered.
	Href string
}

// StartHereLinks says which targets exist on the drive.
type StartHereLinks struct {
	RebuildGuide, DocsIndex, Survival, Ready bool
	// KiwixTools and KiwixLibrary are not links (the page prints commands) but facts the page states.
	KiwixTools, KiwixLibrary bool
	Cards                    []StartHereCard
}

var cardName = regexp.MustCompile(`^survival-card-([a-z0-9][a-z0-9._-]*)\.(pdf|html)$`)

// ProbeStartHereLinks looks at the drive (mount is its top level) and reports which targets exist. A
// symbolic link does not count: the drive is not trusted and a link could point out of it.
func ProbeStartHereLinks(fsys FS, mount string) StartHereLinks {
	has := func(rel string) bool {
		fi, err := fsys.Lstat(filepath.Join(mount, filepath.FromSlash(rel)))
		return err == nil && fi.Mode().IsRegular()
	}
	l := StartHereLinks{
		RebuildGuide: has(startHereRebuildGuide), DocsIndex: has(startHereDocsIndex), Survival: has(startHereSurvival),
		Ready: has(startHereReady), KiwixLibrary: has(startHereKiwixLibrary),
	}
	if fi, err := fsys.Lstat(filepath.Join(mount, filepath.FromSlash(startHereKiwixTools))); err == nil && fi.IsDir() {
		if ents, err := fsys.ReadDir(filepath.Join(mount, filepath.FromSlash(startHereKiwixTools))); err == nil {
			for _, e := range ents {
				if e.Type().IsRegular() {
					l.KiwixTools = true
					break
				}
			}
		}
	}
	ents, err := fsys.ReadDir(filepath.Join(mount, filepath.FromSlash(startHereDocsDir)))
	if err != nil {
		return l
	}
	cards := map[string]StartHereCard{}
	for _, e := range ents {
		m := cardName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		if cur, seen := cards[m[1]]; seen && strings.HasSuffix(cur.Href, ".pdf") {
			continue // the PDF wins over the HTML of the same card
		}
		cards[m[1]] = StartHereCard{NodeID: m[1], Href: startHereDocsDir + "/" + e.Name()}
	}
	for _, c := range cards {
		l.Cards = append(l.Cards, c)
	}
	sort.Slice(l.Cards, func(i, j int) bool { return l.Cards[i].NodeID < l.Cards[j].NodeID })
	return l
}

type startHereRow struct {
	Name, Version, Size             string
	StatusClass, StatusEN, StatusES string
}

type startHereView struct {
	OverallClass, OverallEN, OverallES string
	SurvivalEN, SurvivalES             string
	LastSync, Catalog, WrittenBy       string
	Generated                          string
	Links                              StartHereLinks
	Rows                               []startHereRow
}

var startHereTmpl = template.Must(template.New(StartHereName).Parse(startHereTemplate))

var overallWords = map[string][2]string{
	"green":  {"READY", "LISTO"},
	"yellow": {"USABLE, NEEDS ATTENTION", "UTILIZABLE, NECESITA ATENCIÓN"},
	"red":    {"NOT READY", "NO ESTÁ LISTO"},
}

var statusWords = map[string][2]string{
	"verified":    {"verified", "verificado"},
	"stale":       {"older version", "versión anterior"},
	"missing":     {"missing", "falta"},
	"skipped":     {"left out", "omitido"},
	"failed":      {"failed", "falló"},
	"quarantined": {"damaged, set aside", "dañado, apartado"},
	"orphan":      {"extra, not in the plan", "extra, fuera del plan"},
}

// RenderStartHere renders the page. The same input always gives the same bytes.
func RenderStartHere(in StartHereInput, links StartHereLinks) ([]byte, error) {
	v := startHereView{Links: links, Catalog: in.CatalogVersion, WrittenBy: in.WrittenBy}
	if w, ok := overallWords[in.Overall]; ok {
		v.OverallClass, v.OverallEN, v.OverallES = in.Overall, w[0], w[1]
	} else {
		v.OverallClass, v.OverallEN, v.OverallES = "unknown", "UNKNOWN", "DESCONOCIDO"
	}
	if in.SurvivalCapable {
		v.SurvivalEN, v.SurvivalES = "yes", "sí"
	} else {
		v.SurvivalEN, v.SurvivalES = "no", "no"
	}
	if !in.LastSync.IsZero() {
		v.LastSync = in.LastSync.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	if !in.GeneratedAt.IsZero() {
		v.Generated = in.GeneratedAt.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	for _, it := range in.Items {
		w, ok := statusWords[it.Status]
		if !ok {
			w = [2]string{it.Status, it.Status}
		}
		name := it.Name
		if name == "" {
			name = it.ID
		}
		row := startHereRow{Name: name, Version: it.Version, StatusClass: it.Status, StatusEN: w[0], StatusES: w[1]}
		if !ok {
			row.StatusClass = ""
		}
		if it.Bytes > 0 {
			row.Size = startHereSize(it.Bytes)
		}
		v.Rows = append(v.Rows, row)
	}
	var buf bytes.Buffer
	if err := startHereTmpl.Execute(&buf, v); err != nil {
		return nil, fmt.Errorf("render %s: %w", StartHereName, err)
	}
	return buf.Bytes(), nil
}

// startHereSize words a size in decimal units, like the rest of the agent ("12.0 GB").
func startHereSize(n int64) string {
	const k = 1000.0
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < k || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= k
	}
	return ""
}

// WriteStartHere probes the drive, renders the page and writes `<mount>/START-HERE.html` (mode 0644,
// temporary file and rename). A drive that cannot be written returns the error; the caller logs it, the
// page is a courtesy and never fails a run.
func WriteStartHere(fsys FS, mount string, in StartHereInput) error {
	page, err := RenderStartHere(in, ProbeStartHereLinks(fsys, mount))
	if err != nil {
		return err
	}
	path := filepath.Join(mount, StartHereName)
	if fi, err := fsys.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", StartHereName)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return config.WriteFileAtomicFS(fsys, path, page, 0o644, -1, -1)
}
