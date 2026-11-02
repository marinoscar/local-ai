package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api/static"
)

// DocsContentSecurityPolicy is the CSP of the HTML pages under /docs/ (agent.md section 3.11.3).
// It replaces the global one: the rendered manuals load their CSS, images and nothing else from
// the agent's own origin, and run no script. aictl docs render must therefore write its CSS as a
// file the pages link to, not as an inline <style>.
const DocsContentSecurityPolicy = "default-src 'self'; img-src 'self' data:"

// maxDocsIndexBytes bounds docs/index.json: the file is written by aictl, but it sits on a drive.
const maxDocsIndexBytes = 4 << 20

// DocsOptions configures the /docs/ routes.
type DocsOptions struct {
	// Roots returns the content roots, internal root first (/srv/localai, then the ark drive's
	// localai folder when mounted). It is called per request, so a drive that is mounted or ejected
	// is noticed without a restart. Nil or empty: no documents.
	Roots func() []string
	// Logger receives warnings (an unreadable index.json). Nil: slog.Default.
	Logger *slog.Logger
}

// DocEntry is one rendered document of GET /api/v1/docs/index (agent.md section 3.8.29).
type DocEntry struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	TitleES string `json:"title_es"`
	HTML    string `json:"html"`
	PDF     string `json:"pdf,omitempty"`
}

// DocsIndex is the body of GET /api/v1/docs/index.
type DocsIndex struct {
	// Root is the docs folder the list comes from ("<root>/docs"), null when no root has an index.
	Root *string `json:"root"`
	// RenderedAt is the time aictl docs render wrote the index, null when unknown.
	RenderedAt *string `json:"rendered_at"`
	// Docs is never null: an empty list when there is nothing to show.
	Docs []DocEntry `json:"docs"`
}

type docs struct {
	opts DocsOptions
	log  *slog.Logger
}

// DocsRoutes returns the routes of the rendered manuals: GET /docs/ (level none, a subtree: files
// under the first content root that has docs/index.json, index.html for a folder) and GET
// /api/v1/docs/index (level op, 5 s). Add them with Register or Registry.Add.
//
// Path safety is internal/api/static's: no traversal or encoded traversal, no dotfile, and a symbolic
// link is followed only if its target stays inside the content root. Every refusal is the same 404
// as a missing file. HTML pages get DocsContentSecurityPolicy plus the other global HTML headers;
// other files get X-Content-Type-Options: nosniff.
func DocsRoutes(opts DocsOptions) []Route {
	d := &docs{opts: opts, log: opts.Logger}
	if d.log == nil {
		d.log = slog.Default()
	}
	d.log = d.log.With("component", "api")
	return []Route{
		{Method: http.MethodGet, Pattern: "/docs/", Auth: []Level{AuthNone}, Absolute: true, Handler: HandlerFunc(d.serveFile)},
		{Method: http.MethodGet, Pattern: "/docs/index", Auth: []Level{AuthOp}, Timeout: 5 * time.Second, Handler: HandlerFunc(d.index)},
	}
}

// found is the docs folder of the first content root that has docs/index.json.
type found struct {
	dir   string // real path of <root>/docs, what files are served from
	shown string // <root>/docs as configured, what the index reports
	index string // real path of docs/index.json
}

// find picks the first content root whose docs/index.json is a regular file inside the root.
func (d *docs) find() (found, bool) {
	if d.opts.Roots == nil {
		return found{}, false
	}
	for _, root := range d.opts.Roots() {
		idx, err := static.SafeJoin(root, "docs/index.json")
		if err != nil {
			continue
		}
		if fi, err := os.Stat(idx); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		dir, err := static.SafeJoin(root, "docs")
		if err != nil {
			continue
		}
		return found{dir: dir, shown: filepath.Join(root, "docs"), index: idx}, true
	}
	return found{}, false
}

// serveFile serves GET /docs/<rel>. A browser that asks for a page that is not there gets the
// bilingual error page; any other client gets the envelope.
func (d *docs) serveFile(w http.ResponseWriter, r *http.Request) error {
	f, ok := d.find()
	if !ok {
		WriteErrorPage(w, r, NewError(CodeNotFound, http.StatusNotFound, map[string]any{"reason": "docs_not_rendered"}))
		return nil
	}
	rel := strings.TrimPrefix(r.URL.Path, "/docs/")
	p, serr := static.Resolve(f.dir, rel, "index.html")
	if serr != nil {
		WriteErrorPage(w, r, StaticError(serr))
		return nil
	}
	if static.IsHTML(p) {
		SetHTMLHeaders(w.Header())
		w.Header().Set("Content-Security-Policy", DocsContentSecurityPolicy)
	}
	if err := static.ServeFile(w, r, p); err != nil {
		WriteErrorPage(w, r, StaticError(err)) // nothing was written yet
	}
	return nil
}

// index serves GET /api/v1/docs/index.
func (d *docs) index(w http.ResponseWriter, r *http.Request) error {
	out := DocsIndex{Docs: []DocEntry{}}
	if f, ok := d.find(); ok {
		out.Root = &f.shown
		idx, err := readDocsIndex(f.index)
		if err != nil {
			d.log.Warn("docs index unreadable", "path", f.index, "error", err.Error())
		} else {
			out.Docs = idx.Docs
			if idx.RenderedAt != "" {
				out.RenderedAt = &idx.RenderedAt
			}
		}
	}
	WriteJSON(w, r, http.StatusOK, out)
	return nil
}

type rawDocsIndex struct {
	RenderedAt string     `json:"rendered_at"`
	Docs       []DocEntry `json:"docs"`
}

// readDocsIndex parses docs/index.json. Entries without a slug or without a /docs/ link are
// dropped, and a pdf link that is not a /docs/ path is blanked, so a hand-edited index cannot make
// the Portal link outside the mount.
func readDocsIndex(path string) (rawDocsIndex, error) {
	var out rawDocsIndex
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxDocsIndexBytes+1))
	if err != nil {
		return out, err
	}
	if len(b) > maxDocsIndexBytes {
		return out, errors.New("index.json is too large")
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return rawDocsIndex{}, err
	}
	kept := make([]DocEntry, 0, len(out.Docs))
	for _, e := range out.Docs {
		if e.Slug == "" || !docsLink(e.HTML) {
			continue
		}
		if e.PDF != "" && !docsLink(e.PDF) {
			e.PDF = ""
		}
		kept = append(kept, e)
	}
	out.Docs = kept
	return out, nil
}

// docsLink reports whether s is a link under /docs/ with no parent-directory segment.
func docsLink(s string) bool {
	return strings.HasPrefix(s, "/docs/") && len(s) > len("/docs/") && !strings.Contains(s, "..") && !strings.ContainsAny(s, "\\\x00")
}
