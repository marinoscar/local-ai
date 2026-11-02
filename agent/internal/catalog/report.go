package catalog

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Problem is one lint finding. File is relative to the catalog directory ("models.yaml",
// "profiles/small-8gb.yaml"); Path is the YAML path ("models[3].source.revision", empty for the
// whole file). Line is the 1-based line in File when known; it is for callers that want it and is
// deliberately not part of the output format or of the --json contract.
type Problem struct {
	Code    string `json:"code"`
	File    string `json:"file"`
	Path    string `json:"path"`
	Message string `json:"message"`
	Line    int    `json:"-"`
}

// Location is "<file>:<yaml-path>", the first field of a human lint line.
func (p Problem) Location() string { return p.File + ":" + p.Path }

// Report is the result of Lint. Valid means no problems. Version and the counts are filled when
// the files could be read (for the CLI summary line); they are not part of the JSON form.
type Report struct {
	Problems []Problem
	Version  string
	Models   int
	Content  int
	Profiles int
}

// Valid reports whether the catalog passed every rule.
func (r Report) Valid() bool { return len(r.Problems) == 0 }

// MarshalJSON renders the stable machine interface the cloud depends on (aictl.md 4.11.1,
// cloud-app.md 4.6): {"valid":bool,"problems":[{"code","file","path","message"}]}. The problems
// array is never null.
func (r Report) MarshalJSON() ([]byte, error) {
	probs := r.Problems
	if probs == nil {
		probs = []Problem{}
	}
	return json.Marshal(struct {
		Valid    bool      `json:"valid"`
		Problems []Problem `json:"problems"`
	}{r.Valid(), probs})
}

// FormatLines renders the human form, one line per problem:
//
//	<file>:<yaml-path>  LAI-CAT-0NN  <message>
//
// (two spaces between fields). dir is the directory as the user typed it ("catalog/"), prefixed to
// the file so a CI log line points at a repository path. A problem about a whole file has an empty
// yaml-path, so its line starts "<file>:  LAI-CAT-0NN".
func (r Report) FormatLines(dir string) []string {
	out := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		out = append(out, fmt.Sprintf("%s:%s  %s  %s", joinDir(dir, p.File), p.Path, p.Code, p.Message))
	}
	return out
}

func joinDir(dir, file string) string {
	if dir == "" {
		return file
	}
	return path.Join(filepath.ToSlash(dir), file)
}

// fileRank orders files in the report: CATALOG_VERSION, models, ark, profiles.
func fileRank(f string) int {
	switch f {
	case FileVersion:
		return 0
	case FileModels:
		return 1
	case FileArk:
		return 2
	default:
		return 3
	}
}

// normalize sorts problems by file, then line (stable, so generation order breaks ties), and drops
// exact duplicates.
func normalize(ps []Problem) []Problem {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if ra, rb := fileRank(a.File), fileRank(b.File); ra != rb {
			return ra < rb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	seen := map[Problem]bool{}
	out := ps[:0]
	for _, p := range ps {
		k := p
		k.Line = 0
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}

// oneLine keeps a message on one line: the output format is line-oriented.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
