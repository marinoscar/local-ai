package i18n

import (
	"regexp"
	"strings"
	"testing"
)

var verbs = regexp.MustCompile(`%[a-z]`)

// Every key exists in both languages, is not empty and uses the same formatting verbs in the same
// order, so a translation can never change what a call site passes.
func TestTablesAgree(t *testing.T) {
	if len(en) != len(es) {
		t.Fatalf("en has %d keys, es has %d", len(en), len(es))
	}
	for _, k := range Keys() {
		e, eok := en[k]
		s, sok := es[k]
		if !eok || !sok || strings.TrimSpace(e) == "" || strings.TrimSpace(s) == "" {
			t.Errorf("key %q: en=%q es=%q", k, e, s)
			continue
		}
		if strings.Join(verbs.FindAllString(e, -1), "") != strings.Join(verbs.FindAllString(s, -1), "") {
			t.Errorf("key %q: formatting verbs differ: %q vs %q", k, e, s)
		}
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		lang string
		want Lang
	}{
		{"unset", "", EN},
		{"C", "C", EN},
		{"English UTF-8", "en_US.UTF-8", EN},
		{"Spanish Costa Rica", "es_CR.UTF-8", ES},
		{"Spanish plain", "es", ES},
		{"upper case", "ES_ES", ES},
		{"not Spanish but contains es", "pt_BR.UTF-8", EN},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(func(k string) string {
				if k == "LANG" {
					return tc.lang
				}
				return ""
			})
			if got != tc.want {
				t.Errorf("Detect(LANG=%q) = %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
	if Detect(nil) != EN {
		t.Error("a nil getenv must give English")
	}
}

func TestTAndPlural(t *testing.T) {
	if got := T(EN, KeyFixLabel); got != "Fix:" {
		t.Errorf("en fix label = %q", got)
	}
	if got := T(ES, KeyFixLabel); got != "Solución:" {
		t.Errorf("es fix label = %q", got)
	}
	if got := T(ES, KeyTriesLeft, 2); got != "Intentos restantes: 2." {
		t.Errorf("formatted = %q", got)
	}
	if got := T(EN, "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key = %q, want the key", got)
	}
	for _, tc := range []struct {
		l    Lang
		n    int64
		want string
	}{{EN, 1, "hour"}, {EN, 2, "hours"}, {EN, 0, "hours"}, {ES, 1, "hora"}, {ES, 5, "horas"}} {
		if got := Plural(tc.l, KeyUnitHour, tc.n); got != tc.want {
			t.Errorf("Plural(%s,%d) = %q, want %q", tc.l, tc.n, got, tc.want)
		}
	}
}
