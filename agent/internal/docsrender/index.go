package docsrender

import (
	"bytes"
	"encoding/json"
	"html"
	"time"
)

// IndexEntry is one document of the index.
type IndexEntry struct {
	Slug    string
	Title   string
	TitleES string
	Kind    string
	// HTMLFile and PDFFile are file names inside the output folder; PDFFile is "" when no PDF was made.
	HTMLFile string
	PDFFile  string
}

// jsonDoc is one element of index.json "docs" (agent.md 3.8.29).
type jsonDoc struct {
	Slug    string  `json:"slug"`
	Title   string  `json:"title"`
	TitleES *string `json:"title_es"`
	Kind    string  `json:"kind"`
	HTML    string  `json:"html"`
	PDF     *string `json:"pdf"`
}

type jsonIndex struct {
	Root       string    `json:"root"`
	RenderedAt string    `json:"rendered_at"`
	Docs       []jsonDoc `json:"docs"`
}

// IndexJSON is index.json for the output folder root, as the agent reads it (GET /docs/index).
func IndexJSON(root string, now time.Time, entries []IndexEntry) ([]byte, error) {
	idx := jsonIndex{Root: root, RenderedAt: now.UTC().Format(time.RFC3339), Docs: []jsonDoc{}}
	for _, e := range entries {
		d := jsonDoc{Slug: e.Slug, Title: e.Title, Kind: e.Kind, HTML: "/docs/" + e.HTMLFile}
		if e.TitleES != "" {
			v := e.TitleES
			d.TitleES = &v
		}
		if e.PDFFile != "" {
			v := "/docs/" + e.PDFFile
			d.PDF = &v
		}
		idx.Docs = append(idx.Docs, d)
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var indexSections = []struct{ kind, en, es string }{
	{KindCard, "Survival card", "Tarjeta de supervivencia"},
	{KindRunbook, "Runbooks (step by step)", "Guías paso a paso"},
	{KindSpec, "Specifications", "Especificaciones"},
	{KindArchitecture, "Architecture", "Arquitectura"},
	{KindDecision, "Decisions", "Decisiones"},
	{KindOther, "Other", "Otros"},
}

// IndexHTML is index.html: every document, grouped, in English with the Spanish title when there is one.
func IndexHTML(entries []IndexEntry) []byte {
	var b bytes.Buffer
	b.WriteString("<h1>Manuals / Manuales</h1>\n")
	b.WriteString("<p>These pages work without the internet. / Estas páginas funcionan sin internet.</p>\n")
	for _, s := range indexSections {
		var rows []IndexEntry
		for _, e := range entries {
			if e.Kind == s.kind {
				rows = append(rows, e)
			}
		}
		if len(rows) == 0 {
			continue
		}
		b.WriteString("<h2>" + html.EscapeString(s.en) + " / " + html.EscapeString(s.es) + "</h2>\n<ul class=\"manuals\">\n")
		for _, e := range rows {
			b.WriteString("<li><a href=\"" + html.EscapeString(e.HTMLFile) + "\">" + html.EscapeString(e.Title) + "</a>")
			if e.TitleES != "" {
				b.WriteString(" <span class=\"es\">" + html.EscapeString(e.TitleES) + "</span>")
			}
			if e.PDFFile != "" {
				b.WriteString(" <a href=\"" + html.EscapeString(e.PDFFile) + "\">PDF</a>")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	return pageHTML("Manuals / Manuales", "en", "", b.String(), pageOpts{Main: true}, "print.css")
}
