package i18n

import (
	"reflect"
	"strings"
	"testing"
)

func TestTablesHaveTheSameKeysAndNoBlanks(t *testing.T) {
	en, es := Keys(EN), Keys(ES)
	if len(en) == 0 {
		t.Fatal("the English table is empty or unreadable")
	}
	if !reflect.DeepEqual(en, es) {
		t.Fatalf("English keys %v, Spanish keys %v", en, es)
	}
	for _, k := range en {
		for _, l := range []Lang{EN, ES} {
			if s := T(l, k); strings.TrimSpace(s) == "" || s == k {
				t.Errorf("%s/%s has no sentence", l, k)
			}
		}
	}
}

func TestTPicksTheLanguageAndFallsBackToTheKey(t *testing.T) {
	for _, tc := range []struct {
		lang      Lang
		key, want string
	}{
		{EN, "drill.notify.pass", "Monthly check finished: everything works offline."},
		{ES, "drill.notify.pass", "Revisión mensual terminada: todo funciona sin internet."},
		{EN, "drill.notify.problems", "Monthly check found problems. Open Status."},
		{ES, "drill.notify.problems", "La revisión mensual encontró problemas. Abra Estado."},
		{"fr", "drill.notify.pass", "Monthly check finished: everything works offline."},
		{EN, "no.such.key", "no.such.key"},
	} {
		if got := T(tc.lang, tc.key); got != tc.want {
			t.Errorf("T(%q, %q) = %q, want %q", tc.lang, tc.key, got, tc.want)
		}
	}
}
