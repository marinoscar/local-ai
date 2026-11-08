package doctor

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCodeTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Codes() {
		if !CodePattern.MatchString(c.ID) {
			t.Errorf("%q does not match the code pattern", c.ID)
		}
		if seen[c.ID] {
			t.Errorf("%s is defined twice", c.ID)
		}
		seen[c.ID] = true
		if !ValidArea(Area(c.ID)) {
			t.Errorf("%s: area %q is not in Areas()", c.ID, Area(c.ID))
		}
		if !c.Severity.Valid() && c.Severity != Retired {
			t.Errorf("%s: severity %q", c.ID, c.Severity)
		}
		for _, f := range []struct {
			name string
			text string
		}{
			{"title en", c.Title.EN}, {"title es", c.Title.ES}, {"fix en", c.Fix.EN}, {"fix es", c.Fix.ES},
		} {
			if strings.TrimSpace(f.text) == "" {
				t.Errorf("%s: %s is empty", c.ID, f.name)
			}
			if f.text != strings.TrimSpace(f.text) {
				t.Errorf("%s: %s has surrounding whitespace", c.ID, f.name)
			}
		}
		// Message at most 120 characters (doctor-codes.md section 1.5).
		for _, f := range []struct{ name, text string }{{"title en", c.Title.EN}, {"title es", c.Title.ES}} {
			if n := utf8.RuneCountInString(f.text); n > 120 {
				t.Errorf("%s: %s is %d characters, the limit is 120", c.ID, f.name, n)
			}
		}
		if c.Title.EN == c.Title.ES {
			t.Errorf("%s: the Spanish title equals the English one", c.ID)
		}
		if c.Fix.EN == c.Fix.ES {
			t.Errorf("%s: the Spanish fix equals the English one", c.ID)
		}
		// The Spanish text keeps every command the English one names: backticked spans are
		// not translated.
		if missing := missingSpans(c.Fix.EN, c.Fix.ES); len(missing) > 0 {
			t.Errorf("%s: the Spanish fix lost these commands: %v", c.ID, missing)
		}
	}
	if len(seen) < 374 {
		t.Errorf("the table has %d codes, the registry had 374 when it was copied", len(seen))
	}
}

var backticks = regexp.MustCompile("`[^`]+`")

// missingSpans returns the spans of en that are not in es, ignoring spans with <placeholders>,
// whose placeholder is translated (`aictl load <model>` is `aictl load <modelo>`).
func missingSpans(en, es string) []string {
	var out []string
	for _, s := range backticks.FindAllString(en, -1) {
		if strings.Contains(s, "<") || strings.Contains(s, "…") {
			continue
		}
		if !strings.Contains(es, s) {
			out = append(out, s)
		}
	}
	return out
}

func TestEveryRegistryCodeIsInTheTable(t *testing.T) {
	data, err := os.ReadFile("../../../docs/specs/doctor-codes.md")
	if err != nil {
		t.Skipf("registry not found (%v)", err)
	}
	row := regexp.MustCompile(`(?m)^\| (LAI-[A-Z]+-[0-9]{3}) \| ([^|]+?) \|`)
	registry := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(string(data), -1) {
		sev := strings.Fields(m[2])[0]
		registry[m[1]] = sev
	}
	if len(registry) < 374 {
		t.Fatalf("parsed %d registry rows, want at least 374", len(registry))
	}
	for code, sev := range registry {
		c, ok := Lookup(code)
		if !ok {
			t.Errorf("%s is in the registry but not in the code table", code)
			continue
		}
		if string(c.Severity) != sev {
			t.Errorf("%s: table severity %q, registry %q", code, c.Severity, sev)
		}
	}
	for _, c := range Codes() {
		if _, ok := registry[c.ID]; !ok {
			t.Errorf("%s is in the code table but not in the registry", c.ID)
		}
	}
}

// TestActionsMatchTheRegistry: every Action of the table is one of the closed set, and the table
// carries exactly the actions of the registry's action table (doctor-codes.md section 1.7).
func TestActionsMatchTheRegistry(t *testing.T) {
	for _, c := range Codes() {
		if c.Action != "" && !c.Action.Valid() {
			t.Errorf("%s: action %q is not one of %v", c.ID, c.Action, Actions())
		}
		if c.Action != "" && c.Severity == Retired {
			t.Errorf("%s: a retired code has no action", c.ID)
		}
	}
	if ActionOf(SVC001) != ActionArkImport || ActionOf(AGT001) != "" || ActionOf(unknownCode("SVC")) != "" {
		t.Error("ActionOf")
	}
	data, err := os.ReadFile("../../../docs/specs/doctor-codes.md")
	if err != nil {
		t.Skipf("registry not found (%v)", err)
	}
	s := string(data)
	start := strings.Index(s, "### 1.7 ")
	if start < 0 {
		t.Fatal("the registry has no section 1.7 (actions)")
	}
	sect := s[start+1:]
	if end := strings.Index(sect, "\n#"); end >= 0 {
		sect = sect[:end]
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z][a-z.:]*)` \\|.*\\|([^|]*)\\|$")
	code := regexp.MustCompile(`LAI-[A-Z]+-[0-9]{3}`)
	want := map[string]Action{}
	listed := map[Action]bool{}
	for _, m := range row.FindAllStringSubmatch(sect, -1) {
		a := Action(m[1])
		listed[a] = true
		if !a.Valid() {
			t.Errorf("the registry lists action %q, which is not one of %v", a, Actions())
		}
		for _, c := range code.FindAllString(m[2], -1) {
			want[c] = a
		}
	}
	for _, a := range Actions() {
		if !listed[a] {
			t.Errorf("action %q is not in the registry's action table", a)
		}
	}
	for _, c := range Codes() {
		if c.Action != want[c.ID] {
			t.Errorf("%s: table action %q, registry %q", c.ID, c.Action, want[c.ID])
		}
	}
}

func TestLookupAndText(t *testing.T) {
	c, ok := Lookup(HW012)
	if !ok || c.ID != HW012 || c.Severity != Warning {
		t.Fatalf("Lookup(HW012) = %+v, %v", c, ok)
	}
	if _, ok := Lookup(unknownCode("HW")); ok {
		t.Error("an unknown code was found")
	}
	tests := []struct {
		name, code, lang string
		title, fix       string
		ok               bool
	}{
		{"english", HW012, "en", "Running on battery", "Plug in the charger. A laptop server stops when the battery runs out", true},
		{"spanish", HW012, "es", "Funcionando con batería", "Conecte el cargador. Un portátil servidor se apaga cuando se agota la batería", true},
		{"other language falls back to english", HW012, "fr", "Running on battery", "Plug in the charger. A laptop server stops when the battery runs out", true},
		{"empty language is english", HW012, "", "Running on battery", "Plug in the charger. A laptop server stops when the battery runs out", true},
		{"unknown code", unknownCode("HW"), "en", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, fix, ok := Text(tc.code, tc.lang)
			if ok != tc.ok || title != tc.title || fix != tc.fix {
				t.Errorf("Text = %q, %q, %v", title, fix, ok)
			}
		})
	}
}

func TestEmittable(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{AGT099, true},
		{SEC009, true},
		{KIT022, false}, // retired
		{unknownCode("AGT"), false},
		{"", false},
	}
	for _, tc := range tests {
		if got := Emittable(tc.code); got != tc.want {
			t.Errorf("Emittable(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if c, _ := Lookup(KIT022); c.Severity != Retired {
		t.Errorf("KIT022 severity = %q, want retired", c.Severity)
	}
}

func TestSeverityOrder(t *testing.T) {
	order := []Severity{Info, Warning, Error, Critical}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Errorf("%s must rank below %s", order[i-1], order[i])
		}
	}
	if Retired.Valid() || Severity("fatal").Valid() || Severity("").Valid() {
		t.Error("retired and unknown severities are not valid for a result")
	}
}

func TestAreaHelpers(t *testing.T) {
	if Area(HW012) != "HW" || Area("nope") != "" || Area("X-HW-012") != "" {
		t.Error("Area")
	}
	if !ValidArea("LLM") || ValidArea("llm") || ValidArea("XYZ") {
		t.Error("ValidArea")
	}
	for _, a := range Areas() {
		found := false
		for _, c := range Codes() {
			if Area(c.ID) == a {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("area %s has no code in the table", a)
		}
	}
}

// unknownCode returns a well-formed code of area that is not in the table. The tests build it
// instead of spelling it, because scripts/check-doctor-codes.sh rejects a code literal that is not
// in the registry anywhere under agent/.
func unknownCode(area string) string { return "LAI-" + area + "-998" }

func TestCodesReturnsACopy(t *testing.T) {
	a := Codes()
	a[0].Title.EN = "changed"
	if b := Codes(); b[0].Title.EN == "changed" {
		t.Error("Codes leaked the table")
	}
}
