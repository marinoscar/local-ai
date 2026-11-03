package piper

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

func voicesOf(t *testing.T, cat StaticCatalog, ids ...string) Voices {
	t.Helper()
	return Voices{Catalog: cat, Copies: installedVoices(t, ids...)}
}

func TestSelectOrder(t *testing.T) {
	both := []string{"piper-en-medium", "piper-es-medium"}
	enOnly := []string{"piper-en-medium"}
	esOnly := []string{"piper-es-medium"}
	cases := []struct {
		name         string
		installed    []string
		in           Choice
		cfg          Config
		wantID       string
		wantRule     int
		wantFallback bool
		wantErr      error
	}{
		{"1 the requested voice wins over the text", both, Choice{Voice: "piper-es-medium", Language: "en", Text: "the and is of you"}, DefaultConfig(), "piper-es-medium", 1, false, nil},
		{"1 the requested voice wins over the language", both, Choice{Voice: "piper-en-medium", Language: "es"}, DefaultConfig(), "piper-en-medium", 1, false, nil},
		{"2 explicit english", both, Choice{Language: "en", Text: "hola amigo ¿cómo estás?"}, DefaultConfig(), "piper-en-medium", 2, false, nil},
		{"2 explicit spanish", both, Choice{Language: "es", Text: "the and is of you"}, DefaultConfig(), "piper-es-medium", 2, false, nil},
		{"3 auto detects spanish", both, Choice{Language: "auto", Text: "Hierva el agua durante un minuto, por favor."}, DefaultConfig(), "piper-es-medium", 3, false, nil},
		{"3 empty language is auto", both, Choice{Text: "Press firmly on the wound with the cloth."}, DefaultConfig(), "piper-en-medium", 3, false, nil},
		{"4 detection falls through to the portal language", both, Choice{Text: "Hello", UILanguage: "es"}, DefaultConfig(), "piper-es-medium", 4, false, nil},
		{"4 portal language in english", both, Choice{Text: "Hola", UILanguage: "en"}, DefaultConfig(), "piper-en-medium", 4, false, nil},
		{"5 any voice, with the fallback flag", enOnly, Choice{Language: "es"}, Config{Enabled: true, DefaultVoice: DefaultVoices{EN: "auto", ES: "auto"}}, "piper-en-medium", 5, true, nil},
		{"5 any voice when nothing else says", esOnly, Choice{Text: "Hello"}, DefaultConfig(), "piper-es-medium", 5, true, nil},
		{"auto detected language has no voice, fallback to any", enOnly, Choice{Text: "Hierva el agua durante un minuto, por favor."}, DefaultConfig(), "piper-en-medium", 5, true, nil},
		{"4 explicit language not served by the portal language voice is a fallback", esOnly, Choice{Language: "en", UILanguage: "es"}, DefaultConfig(), "piper-es-medium", 4, true, nil},
		{"6 no voice installed", nil, Choice{Language: "en"}, DefaultConfig(), "", 0, false, errNoVoice},
		{"an uninstalled requested voice falls through", enOnly, Choice{Voice: "piper-es-medium", Text: "Press firmly on the wound with the cloth."}, DefaultConfig(), "piper-en-medium", 3, false, nil},
		{"an unknown requested voice is refused", both, Choice{Voice: "piper-fr-medium"}, DefaultConfig(), "", 0, false, errUnknownVoice},
		{"configured default voice of the language", both, Choice{Language: "en"}, Config{Enabled: true, DefaultVoice: DefaultVoices{EN: "piper-en-medium", ES: "auto"}}, "piper-en-medium", 2, false, nil},
	}
	for _, c := range cases {
		v := voicesOf(t, catalogVoices(), c.installed...)
		got, err := v.Select(c.in, c.cfg)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.Voice.ID != c.wantID || got.Rule != c.wantRule || got.Fallback != c.wantFallback {
			t.Errorf("%s: got voice %s rule %d fallback %v, want %s rule %d fallback %v", c.name, got.Voice.ID, got.Rule, got.Fallback, c.wantID, c.wantRule, c.wantFallback)
		}
	}
}

func TestSelectPrefersTheConfiguredDefaultAndThenCatalogOrder(t *testing.T) {
	cat := StaticCatalog{
		{ID: "piper-en-low", Language: "en", DisplayName: "English, low"},
		{ID: "piper-en-medium", Language: "en", DisplayName: "English, medium"},
	}
	v := voicesOf(t, cat, "piper-en-low", "piper-en-medium")
	got, err := v.Select(Choice{Language: "en"}, DefaultConfig())
	if err != nil || got.Voice.ID != "piper-en-low" {
		t.Errorf("auto default = %v, %v; the first voice of the language in catalog order is expected", got.Voice.ID, err)
	}
	cfg := DefaultConfig()
	cfg.DefaultVoice.EN = "piper-en-medium"
	if got, _ := v.Select(Choice{Language: "en"}, cfg); got.Voice.ID != "piper-en-medium" {
		t.Errorf("configured default = %v", got.Voice.ID)
	}
	// A configured default that is not installed is ignored.
	cfg.DefaultVoice.EN = "piper-en-high"
	if got, _ := v.Select(Choice{Language: "en"}, cfg); got.Voice.ID != "piper-en-low" {
		t.Errorf("an unknown configured default must fall back to the first voice, got %v", got.Voice.ID)
	}
	// A configured default of the wrong language is ignored too.
	cfg.DefaultVoice.ES = "piper-en-medium"
	if _, err := v.Select(Choice{Language: "es"}, cfg); err != nil {
		t.Errorf("es with only english voices must fall back, got %v", err)
	}
}

func TestVoicesAreVerifiedByTheIndexAndTheirFiles(t *testing.T) {
	dir := t.TempDir()
	good := voiceCopy(t, dir, "piper-en-medium", 22050)

	noConfig := voiceCopy(t, dir, "piper-es-medium", 22050)
	noConfig.Manifest.Files = noConfig.Manifest.Files[:1]
	noConfig.Files = noConfig.Files[:1]

	outside := voiceCopy(t, dir, "piper-en-low", 22050)
	outside.Files[0] = filepath.Join(dir, "elsewhere.onnx")

	relative := voiceCopy(t, dir, "piper-en-high", 22050)
	relative.Files[1] = "piper-en-high.onnx.json"

	cat := StaticCatalog{
		{ID: "piper-en-medium", Language: "en"}, {ID: "piper-es-medium", Language: "es"},
		{ID: "piper-en-low", Language: "en"}, {ID: "piper-en-high", Language: "en"}, {ID: "piper-en-missing", Language: "en"},
	}
	v := Voices{Catalog: cat, Copies: fakeCopies{
		"piper-en-medium": good, "piper-es-medium": noConfig, "piper-en-low": outside, "piper-en-high": relative,
	}}
	var ids []string
	for _, vo := range v.All() {
		ids = append(ids, vo.ID)
	}
	if len(ids) != 1 || ids[0] != "piper-en-medium" {
		t.Errorf("verified voices = %v, want only piper-en-medium", ids)
	}
	if !v.Known("piper-en-missing") || v.Known("piper-xx") {
		t.Error("Known must follow the catalog, not the disk")
	}
	if vo := v.All()[0]; vo.Model != good.Files[0] || vo.Config != good.Files[1] {
		t.Errorf("paths = %q %q", vo.Model, vo.Config)
	}
}

func TestVoicesWithoutSourcesAreEmpty(t *testing.T) {
	if got := (Voices{}).All(); got != nil {
		t.Errorf("All = %v", got)
	}
	if (Voices{}).Known("x") {
		t.Error("Known on an empty catalog")
	}
}

func TestDefaultsOnePerLanguage(t *testing.T) {
	cat := StaticCatalog{
		{ID: "piper-en-low", Language: "en"}, {ID: "piper-en-medium", Language: "en"}, {ID: "piper-es-medium", Language: "es"},
	}
	v := voicesOf(t, cat, "piper-en-low", "piper-en-medium", "piper-es-medium")
	d := Defaults(v.All(), DefaultConfig())
	if d["en"] != "piper-en-low" || d["es"] != "piper-es-medium" || len(d) != 2 {
		t.Errorf("defaults = %v", d)
	}
	cfg := DefaultConfig()
	cfg.DefaultVoice.EN = "piper-en-medium"
	if d := Defaults(v.All(), cfg); d["en"] != "piper-en-medium" {
		t.Errorf("configured default = %v", d)
	}
}

func TestCatalogVoicesOrdersProfileFirst(t *testing.T) {
	c := &catalog.Catalog{Models: []catalog.Model{
		{ID: "llama-x", Engine: "llama", Languages: []string{"en"}},
		{ID: "piper-es-medium", DisplayName: "Spanish", Engine: "piper", Languages: []string{"es_MX"}},
		{ID: "piper-en-low", DisplayName: "English low", Engine: "piper", Languages: []string{"en-US"}},
		{ID: "piper-en-medium", DisplayName: "English medium", Engine: "piper", Languages: []string{"en"}},
		{ID: "piper-nolang", Engine: "piper"},
	}}
	got := CatalogVoices(c, []string{"piper-en-medium", "llama-x", "unknown", "piper-es-medium", "piper-en-medium"})
	var ids, langs []string
	for _, v := range got {
		ids = append(ids, v.ID)
		langs = append(langs, v.Language)
	}
	want := []string{"piper-en-medium", "piper-es-medium", "piper-en-low"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids = %v, want %v", ids, want)
			break
		}
	}
	if langs[1] != "es" || langs[2] != "en" {
		t.Errorf("languages = %v: region subtags must be dropped", langs)
	}
	if CatalogVoices(nil, nil) != nil {
		t.Error("a nil catalog has no voices")
	}
}

func TestInsideRefusesEscapes(t *testing.T) {
	d := "/ark/speech/piper/v"
	for p, want := range map[string]bool{
		d + "/a.onnx": true, d + "/sub/a.onnx": true,
		d: false, "/ark/speech/piper/other/a.onnx": false, d + "/../a": false, "a.onnx": false, d + "/./a": false,
	} {
		if got := inside(d, p); got != want {
			t.Errorf("inside(%q) = %v, want %v", p, got, want)
		}
	}
}
