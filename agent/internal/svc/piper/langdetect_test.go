package piper

import "testing"

func TestDetectTable(t *testing.T) {
	cases := []struct {
		name, text string
		want       string // "" means no decision (falls through)
	}{
		{"english sentence", "Press firmly on the wound with a clean cloth and keep it there.", LangEN},
		{"english instruction", "You should boil the water and let it cool before you drink it.", LangEN},
		{"spanish sentence", "Hierva el agua durante un minuto antes de que usted la beba.", LangES},
		{"spanish question", "¿Cómo purifico el agua que hay en la casa?", LangES},
		{"spanish with stop words only", "El agua de la casa es para los niños y las niñas", LangES},
		{"mixed, spanish wins on evidence", "The water is clean, pero el agua del río no está limpia ni es potable", LangES},
		{"mixed, english wins on evidence", "Please wash your hands and then call el doctor about the wound", LangEN},
		{"short english", "Hello", ""},
		{"short two words", "Stop now", ""},
		{"one stop word", "the water", ""},
		{"two hits is not enough", "the and", ""},
		{"three hits is enough", "the and is", LangEN},
		{"accents only", "¿Cómo?", LangES},
		{"single eñe", "mañana", LangES},
		{"single inverted mark", "¡Hola", LangES},
		{"upper case accents", "ÁÉ", LangES},
		{"tie between languages", "the el", ""},
		{"digits and symbols only", "123 456 !!!", ""},
		{"empty", "", ""},
		{"ambiguous words score for nobody", "no me he come son sin mi a", ""},
		{"accented stop word folds", "Es más fácil y lo hace muy bien", LangES},
	}
	for _, c := range cases {
		got, ok := Detect(c.text)
		if c.want == "" {
			if ok {
				t.Errorf("%s: Detect(%q) = %q, want no decision (score %+v)", c.name, c.text, got, ScoreText(c.text))
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: Detect(%q) = %q, %v, want %q (score %+v)", c.name, c.text, got, ok, c.want, ScoreText(c.text))
		}
	}
}

func TestScoreWeights(t *testing.T) {
	// Each Spanish-only character counts 3; each stop word 1.
	if sc := ScoreText("ñ"); sc.ES != 3 || sc.EN != 0 {
		t.Errorf("ñ: %+v", sc)
	}
	if sc := ScoreText("ñññ"); sc.ES != 9 {
		t.Errorf("ñññ: %+v", sc)
	}
	if sc := ScoreText("the and"); sc.EN != 2 || sc.ES != 0 {
		t.Errorf("the and: %+v", sc)
	}
	// Words are matched whole, not as prefixes: "there" is a stop word, "thermos" is not; "los" is
	// Spanish, "lost" is not.
	if sc := ScoreText("thermos lost"); sc.EN != 0 || sc.ES != 0 {
		t.Errorf("whole words only: %+v", sc)
	}
}

func TestStopWordListsAreSizedAndDisjoint(t *testing.T) {
	if len(stopEN) < 50 || len(stopES) < 50 {
		t.Errorf("the lists hold %d and %d words, about 60 each are expected", len(stopEN), len(stopES))
	}
	for w := range stopEN {
		if stopES[w] {
			t.Errorf("%q is in both lists", w)
		}
		for _, r := range w {
			if r > 127 {
				t.Errorf("%q is not ASCII", w)
			}
		}
	}
	for w := range stopES {
		for _, r := range w {
			if r > 127 {
				t.Errorf("%q is not ASCII: the lists are written without accents", w)
			}
		}
	}
}

func FuzzDetect(f *testing.F) {
	for _, s := range []string{"", "the and is", "¿Cómo?", "\xff\xfe", "ÑÑÑ"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		lang, ok := Detect(s)
		if ok && lang != LangEN && lang != LangES {
			t.Fatalf("Detect(%q) = %q", s, lang)
		}
	})
}
