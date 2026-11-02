package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// MaxFileBytes caps the size of any catalog file the loader reads (the cloud caps the whole
// catalog at 2 MB, cloud-app.md 4.6). A larger file is a layout problem, not a memory problem.
const MaxFileBytes = 4 << 20

// Catalog is a loaded, lint-clean catalog. It is read-only: callers must not modify the slices.
type Catalog struct {
	Version  string
	Models   []Model
	Content  []ContentItem
	Profiles []Profile

	models   map[string]int
	content  map[string]int
	profiles map[string]int
}

// Empty is the catalog the agent runs with when nothing loads (agent.md 4.1 step 8).
func Empty() *Catalog { return newCatalog("", nil, nil, nil) }

func newCatalog(version string, m []Model, c []ContentItem, p []Profile) *Catalog {
	cat := &Catalog{Version: version, Models: m, Content: c, Profiles: p,
		models: map[string]int{}, content: map[string]int{}, profiles: map[string]int{}}
	for i := range m {
		cat.models[m[i].ID] = i
	}
	for i := range c {
		cat.content[c[i].ID] = i
	}
	for i := range p {
		cat.profiles[p[i].ID] = i
	}
	return cat
}

// Model returns the model with the given id.
func (c *Catalog) Model(id string) (*Model, bool) {
	i, ok := c.models[id]
	if !ok {
		return nil, false
	}
	return &c.Models[i], true
}

// ContentItem returns the ark content item with the given id.
func (c *Catalog) ContentItem(id string) (*ContentItem, bool) {
	i, ok := c.content[id]
	if !ok {
		return nil, false
	}
	return &c.Content[i], true
}

// Profile returns the profile with the given id.
func (c *Catalog) Profile(id string) (*Profile, bool) {
	i, ok := c.profiles[id]
	if !ok {
		return nil, false
	}
	return &c.Profiles[i], true
}

// LintOptions configures Lint. The zero value is the offline lint.
type LintOptions struct {
	// Online also HEAD-checks every URL and Hugging Face file (online.go). It runs only when the
	// offline rules pass.
	Online bool
	// Context bounds the online checks (default: background).
	Context context.Context
	// Client is the HTTP client of the online checks (default: a client with a 30 s timeout).
	Client *http.Client
	// HFEndpoint and GitHubEndpoint override the hosts of the online checks (tests).
	HFEndpoint     string
	GitHubEndpoint string
	// Concurrency bounds parallel HEAD requests (default 4).
	Concurrency int
}

// LoadError is returned by Load when the catalog cannot be used. Its doctor code is
// LAI-CAT-001 (catalog file can't be parsed); Report says what is wrong.
type LoadError struct{ Report Report }

func (e *LoadError) Error() string {
	n := len(e.Report.Problems)
	if n == 0 {
		return CodeUnparseable + ": catalog cannot be loaded"
	}
	p := e.Report.Problems[0]
	return fmt.Sprintf("%s: catalog is invalid (%d problem(s)); first: %s  %s  %s", CodeUnparseable, n, p.Location(), p.Code, p.Message)
}

// Code is the doctor code of the error.
func (e *LoadError) Code() string { return CodeUnparseable }

// Load reads dir and returns the catalog, or a *LoadError. It applies the same rules as Lint, so a
// catalog that does not lint never loads: the agent reads the catalog only through this function.
func Load(dir string) (*Catalog, error) {
	rep, cat := lint(os.DirFS(dir))
	if !rep.Valid() || cat == nil {
		return nil, &LoadError{Report: rep}
	}
	return cat, nil
}

// Lint validates the catalog in dir against every rule of catalog.md sections 2 to 4 and 6.
// It is pure (no network) unless opts.Online is set.
func Lint(dir string, opts LintOptions) Report {
	rep, cat := lint(os.DirFS(dir))
	if opts.Online && rep.Valid() && cat != nil {
		rep.Problems = normalize(append(rep.Problems, checkOnline(cat, opts)...))
	}
	return rep
}

// lintState collects problems and the line index of every file.
type lintState struct {
	problems []Problem
	lines    map[string]map[string]int
}

func (s *lintState) add(file, code, p, format string, args ...any) {
	s.addAt(file, code, p, fmt.Sprintf(format, args...), 0)
}

func (s *lintState) addAt(file, code, p, msg string, line int) {
	if line == 0 {
		if idx := s.lines[file]; idx != nil {
			line = lineFor(idx, p)
		}
	}
	s.problems = append(s.problems, Problem{Code: code, File: file, Path: p, Message: oneLine(msg), Line: line})
}

// readFile reads rel from fsys with the size cap; a problem is recorded (LAI-CAT-019) on failure.
func (s *lintState) readFile(fsys fs.FS, rel string) ([]byte, bool) {
	info, err := fs.Stat(fsys, rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.add(rel, CodeLayout, "", "required file is missing")
		return nil, false
	case err != nil:
		s.add(rel, CodeLayout, "", "cannot read the file: %v", err)
		return nil, false
	case !info.Mode().IsRegular():
		s.add(rel, CodeLayout, "", "must be a regular file")
		return nil, false
	case info.Size() > MaxFileBytes:
		s.add(rel, CodeLayout, "", "file is larger than %d bytes", MaxFileBytes)
		return nil, false
	}
	data, err := fs.ReadFile(fsys, rel)
	if err != nil {
		s.add(rel, CodeLayout, "", "cannot read the file: %v", err)
		return nil, false
	}
	return data, true
}

// readYAML reads and strictly decodes one YAML file. nil means the file could not be used at all.
func readYAML[T any](s *lintState, fsys fs.FS, rel string, schemaVersion func(*T) int) *parsedFile[T] {
	data, ok := s.readFile(fsys, rel)
	if !ok {
		return nil
	}
	pf := decodeStrict[T](data, func(p, msg string, line int) { s.addAt(rel, CodeSchema, p, msg, line) })
	if pf == nil {
		return nil
	}
	s.lines[rel] = pf.Lines
	if _, present := pf.Lines["schema_version"]; present && !pf.Dirty[""] {
		if v := schemaVersion(pf.Value); v != SchemaVersion {
			s.add(rel, CodeSchema, "schema_version", "schema_version must be %d, got %d", SchemaVersion, v)
			pf.Dirty[""] = true
		}
	}
	return pf
}

var versionRe = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})-([1-9][0-9]{0,8})$`)

// ValidVersion reports whether v is a CATALOG_VERSION: YYYY.MM.DD-N, a real calendar date, N >= 1.
func ValidVersion(v string) bool {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	_, err := time.Parse("2006.01.02", m[1]+"."+m[2]+"."+m[3])
	return err == nil
}

// CompareVersions compares two valid versions field by field as numbers (catalog.md section 1):
// -1, 0 or 1. An invalid version sorts before every valid one.
func CompareVersions(a, b string) int {
	ka, okA := versionKey(a)
	kb, okB := versionKey(b)
	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range ka {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionKey(v string) ([4]int, bool) {
	var k [4]int
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return k, false
	}
	for i := 0; i < 4; i++ {
		n := 0
		for _, c := range m[i+1] {
			n = n*10 + int(c-'0')
		}
		k[i] = n
	}
	return k, true
}

// readVersion checks CATALOG_VERSION (single line, YYYY.MM.DD-N).
func (s *lintState) readVersion(fsys fs.FS) string {
	data, ok := s.readFile(fsys, FileVersion)
	if !ok {
		return ""
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if !ValidVersion(v) {
		s.add(FileVersion, CodeLayout, "", "CATALOG_VERSION must be one line YYYY.MM.DD-N (a real date, N >= 1), got %q", truncate(v, 40))
		return ""
	}
	return v
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// readProfiles reads profiles/*.yaml in lexical order.
func (s *lintState) readProfiles(fsys fs.FS) []profileDoc {
	entries, err := fs.ReadDir(fsys, DirProfiles)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.add(DirProfiles, CodeLayout, "", "the profiles/ directory is missing")
		} else {
			s.add(DirProfiles, CodeLayout, "", "cannot read the profiles/ directory: %v", err)
		}
		return nil
	}
	var docs []profileDoc
	for _, e := range entries {
		name := e.Name()
		rel := path.Join(DirProfiles, name)
		switch {
		case strings.HasPrefix(name, "."):
			continue
		case e.IsDir():
			s.add(rel, CodeLayout, "", "unexpected directory inside profiles/")
			continue
		case !strings.HasSuffix(name, ".yaml"):
			s.add(rel, CodeLayout, "", "profile files must be named <id>.yaml")
			continue
		}
		pf := readYAML(s, fsys, rel, func(p *Profile) int { return p.SchemaVersion })
		docs = append(docs, profileDoc{rel: rel, stem: strings.TrimSuffix(name, ".yaml"), pf: pf})
	}
	if len(docs) == 0 {
		s.add(DirProfiles, CodeLayout, "", "profiles/ has no profile files")
	}
	return docs
}

type profileDoc struct {
	rel  string
	stem string
	pf   *parsedFile[Profile]
}

// lint runs every rule over fsys and returns the report plus, when valid, the loaded catalog.
func lint(fsys fs.FS) (Report, *Catalog) {
	s := &lintState{lines: map[string]map[string]int{}}
	version := s.readVersion(fsys)
	models := readYAML(s, fsys, FileModels, func(f *ModelsFile) int { return f.SchemaVersion })
	ark := readYAML(s, fsys, FileArk, func(f *ArkFile) int { return f.SchemaVersion })
	profiles := s.readProfiles(fsys)

	ids := map[string]string{} // id -> "file:path" of its first definition (models and ark share the space)
	if models != nil {
		s.lintModels(models, ids)
	}
	if ark != nil {
		s.lintArk(ark, ids)
	}
	s.lintProfiles(profiles, models, ark)

	rep := Report{Problems: normalize(s.problems), Version: version}
	var m []Model
	var c []ContentItem
	if models != nil {
		m = models.Value.Models
		rep.Models = len(m)
	}
	if ark != nil {
		c = ark.Value.Content
		rep.Content = len(c)
	}
	var p []Profile
	for _, d := range profiles {
		if d.pf != nil {
			p = append(p, *d.pf.Value)
		}
	}
	rep.Profiles = len(p)
	if !rep.Valid() {
		return rep, nil
	}
	sort.SliceStable(p, func(i, j int) bool { return p[i].ID < p[j].ID })
	return rep, newCatalog(version, m, c, p)
}
