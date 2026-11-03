package piper

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// File kinds of a piper model in the catalog and in the item manifest (catalog.md section 2.1:
// engine piper needs exactly one of each).
const (
	FileKindONNX   = "piper_onnx"
	FileKindConfig = "piper_config"
)

// CatalogVoice is a voice the catalog knows (models.yaml, engine piper). It says nothing about the
// disk: whether the voice is installed is the ark index's answer.
type CatalogVoice struct {
	// ID is the catalog model id, for example piper-en-medium.
	ID string
	// Language is the primary language subtag, "en" or "es" ("en-US" is read as "en").
	Language    string
	DisplayName string
}

// VoiceCatalog is the catalog's piper voices, in the order the selection prefers: the voices of the
// node's profile first (in the profile's order), then the other voices of the catalog (by id). The
// app wiring implements it over the loaded catalog and the effective plan (see CatalogVoices); the
// answer may change when the catalog or the profile does, so it is asked for on every request.
type VoiceCatalog interface {
	PiperVoices() []CatalogVoice
}

// StaticCatalog is a fixed VoiceCatalog.
type StaticCatalog []CatalogVoice

// PiperVoices implements VoiceCatalog.
func (s StaticCatalog) PiperVoices() []CatalogVoice { return s }

// CatalogVoices lists the piper models of c, the ones named by planned (the effective plan's model
// ids: the profile's include list with the node's overrides) first in that order, the rest by id. A
// model with no language is skipped: it cannot be chosen for one.
func CatalogVoices(c *catalog.Catalog, planned []string) []CatalogVoice {
	if c == nil {
		return nil
	}
	voice := func(m *catalog.Model) (CatalogVoice, bool) {
		if m == nil || m.Engine != ark.EnginePiper || len(m.Languages) == 0 {
			return CatalogVoice{}, false
		}
		lang := primaryLanguage(m.Languages[0])
		if lang == "" {
			return CatalogVoice{}, false
		}
		return CatalogVoice{ID: m.ID, Language: lang, DisplayName: m.DisplayName}, true
	}
	seen := map[string]bool{}
	var out []CatalogVoice
	for _, id := range planned {
		if m, ok := c.Model(id); ok && !seen[id] {
			if v, ok := voice(m); ok {
				seen[id] = true
				out = append(out, v)
			}
		}
	}
	var rest []CatalogVoice
	for i := range c.Models {
		m := &c.Models[i]
		if seen[m.ID] {
			continue
		}
		if v, ok := voice(m); ok {
			rest = append(rest, v)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	return append(out, rest...)
}

// primaryLanguage reads "en", "en-US" or "es_MX" as "en" or "es".
func primaryLanguage(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// Voice is a catalog voice with a verified copy on disk, ready to be run.
type Voice struct {
	CatalogVoice
	// Model and Config are the absolute paths of the .onnx and the .onnx.json of the best copy.
	Model, Config string
}

// Voices answers which voices are installed. A voice is verified when the ark index has a verified
// copy of its item whose manifest lists both files, and both paths lie inside the copy's directory.
type Voices struct {
	Catalog VoiceCatalog
	Copies  ark.CopyFinder
}

// All returns the catalog's voices that are installed, in the order of the catalog.
func (v Voices) All() []Voice {
	if v.Catalog == nil || v.Copies == nil {
		return nil
	}
	var out []Voice
	seen := map[string]bool{}
	for _, cv := range v.Catalog.PiperVoices() {
		if seen[cv.ID] {
			continue
		}
		seen[cv.ID] = true
		if vo, err := v.resolve(cv); err == nil {
			out = append(out, vo)
		}
	}
	return out
}

// Known reports whether id is a piper voice of the catalog (installed or not).
func (v Voices) Known(id string) bool {
	if v.Catalog == nil {
		return false
	}
	for _, cv := range v.Catalog.PiperVoices() {
		if cv.ID == id {
			return true
		}
	}
	return false
}

// ErrNoFiles: the best copy exists but lacks one of the two files, or one lies outside the copy.
var ErrNoFiles = errors.New("piper: the voice copy has no usable model or config file")

func (v Voices) resolve(cv CatalogVoice) (Voice, error) {
	cp, err := v.Copies.BestCopy(cv.ID)
	if err != nil {
		return Voice{}, err
	}
	if len(cp.Files) != len(cp.Manifest.Files) {
		return Voice{}, ErrNoFiles
	}
	out := Voice{CatalogVoice: cv}
	for i, f := range cp.Manifest.Files {
		p := cp.Files[i]
		switch f.Kind {
		case FileKindONNX:
			out.Model = p
		case FileKindConfig:
			out.Config = p
		default:
			continue
		}
		if !inside(cp.Dir, p) {
			return Voice{}, fmt.Errorf("%w: %s", ErrNoFiles, f.Path)
		}
	}
	if out.Model == "" || out.Config == "" {
		return Voice{}, ErrNoFiles
	}
	return out, nil
}

// inside reports whether p is an absolute path below dir.
func inside(dir, p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// Defaults picks the default voice of each language among the installed voices: the one
// node.yaml's piper.default_voice names when it is installed and of that language, else the first
// voice of the language in catalog order (profile voices first).
func Defaults(all []Voice, cfg Config) map[string]string {
	out := map[string]string{}
	for _, lang := range []string{LangEN, LangES} {
		if v, ok := configured(all, lang, cfg.DefaultVoiceFor(lang)); ok {
			out[lang] = v.ID
		}
	}
	for _, v := range all {
		if _, ok := out[v.Language]; !ok {
			out[v.Language] = v.ID
		}
	}
	return out
}

func configured(all []Voice, lang, id string) (Voice, bool) {
	if id == "" || id == AutoVoice {
		return Voice{}, false
	}
	for _, v := range all {
		if v.ID == id && v.Language == lang {
			return v, true
		}
	}
	return Voice{}, false
}

// Selection is the outcome of the voice rules.
type Selection struct {
	Voice Voice
	// Fallback is true when the voice is not what was asked for: the node has no voice for the
	// requested language (rules 4 and 5 of section 4.3.4). The response carries
	// X-LocalAI-Fallback: true and the log line the info code LAI-SVC-021.
	Fallback bool
	// Rule is the number (1..5) of the rule that chose the voice.
	Rule int
}

// Selection errors. Both are *selectError so the handler maps them to a status and a doctor code.
var (
	errUnknownVoice = errors.New("unknown voice")
	errNoVoice      = errors.New("no voice installed")
)

// Choice is the input of Select.
type Choice struct {
	// Voice is the requested voice id, "" for none.
	Voice string
	// Language is "en", "es" or "auto" ("" is auto).
	Language string
	// Text is the text to speak, as the language detector should see it.
	Text string
	// UILanguage is the Portal language (X-LocalAI-UI-Language), already reduced to "en" or "es", or "".
	UILanguage string
}

// Select applies companion-services.md section 4.3.4: the first rule that yields an installed voice
// wins.
//
//  1. the requested voice;
//  2. the requested language: node.yaml's default voice for it, else the first voice of it;
//  3. language auto: the language the text is written in (Detect), same choice as rule 2;
//  4. the Portal language (X-LocalAI-UI-Language);
//  5. any installed voice, Fallback true;
//  6. none: errNoVoice (LAI-SVC-022).
//
// A requested voice that is not a voice of the catalog is errUnknownVoice. One that is, but not
// installed, falls through to the rules below like any other that yields no voice. Fallback is also
// set when rule 4 answered a request for an explicit language it could not honour, since the header
// means "the requested language had no voice".
func (v Voices) Select(in Choice, cfg Config) (Selection, error) {
	all := v.All()
	if in.Voice != "" && !v.Known(in.Voice) {
		return Selection{}, fmt.Errorf("%w: %q", errUnknownVoice, in.Voice)
	}
	// Rule 1.
	if in.Voice != "" {
		for _, vo := range all {
			if vo.ID == in.Voice {
				return Selection{Voice: vo, Rule: 1}, nil
			}
		}
	}
	pick := func(lang string) (Voice, bool) {
		if vo, ok := configured(all, lang, cfg.DefaultVoiceFor(lang)); ok {
			return vo, true
		}
		for _, vo := range all {
			if vo.Language == lang {
				return vo, true
			}
		}
		return Voice{}, false
	}
	explicit := in.Language == LangEN || in.Language == LangES
	// Rule 2.
	if explicit {
		if vo, ok := pick(in.Language); ok {
			return Selection{Voice: vo, Rule: 2}, nil
		}
	} else if lang, ok := Detect(in.Text); ok { // Rule 3.
		if vo, ok := pick(lang); ok {
			return Selection{Voice: vo, Rule: 3}, nil
		}
	}
	// Rule 4.
	if in.UILanguage != "" {
		if vo, ok := pick(in.UILanguage); ok {
			return Selection{Voice: vo, Rule: 4, Fallback: explicit}, nil
		}
	}
	// Rule 5.
	if len(all) > 0 {
		return Selection{Voice: all[0], Rule: 5, Fallback: true}, nil
	}
	return Selection{}, errNoVoice
}
