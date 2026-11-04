package portal

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// maxIndexBytes bounds docs/index.json: it sits on a drive that may be damaged.
const maxIndexBytes = 4 << 20

// FileDocs is the DocsView over the first content root that has docs/index.json.
type FileDocs struct {
	// Roots returns the content roots, internal first; called per build, so a drive that is mounted or
	// ejected is noticed.
	Roots func() []string
	// NoInventory reports that the documents were rendered without the lab inventory; nil means false.
	NoInventory func() bool
}

var _ DocsView = FileDocs{}

type docsFile struct {
	Docs []struct {
		Slug    string  `json:"slug"`
		Title   string  `json:"title"`
		TitleES *string `json:"title_es"`
		HTML    string  `json:"html"`
	} `json:"docs"`
}

// Docs implements DocsView. An unreadable or absent index is "no documents".
func (f FileDocs) Docs() DocsInfo {
	var info DocsInfo
	if f.Roots == nil {
		return info
	}
	for _, root := range f.Roots() {
		fh, err := os.Open(filepath.Join(root, "docs", "index.json"))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(fh, maxIndexBytes))
		_ = fh.Close()
		if err != nil {
			continue
		}
		var idx docsFile
		if json.Unmarshal(data, &idx) != nil {
			continue
		}
		for _, d := range idx.Docs {
			doc := Doc{Slug: d.Slug, Title: d.Title, HTML: d.HTML}
			if d.TitleES != nil {
				doc.TitleES = *d.TitleES
			}
			info.Docs = append(info.Docs, doc)
		}
		break
	}
	if f.NoInventory != nil {
		info.NoInventory = f.NoInventory()
	}
	return info
}
