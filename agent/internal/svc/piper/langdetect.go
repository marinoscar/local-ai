package piper

import (
	"strings"
	"unicode"
)

// Languages of the Survival Portal and of the voices of the initial catalog.
const (
	LangEN = "en"
	LangES = "es"
)

// MinDetectScore is the least score a language needs to win: fewer hits fall through to the next
// rule of the voice selection (companion-services.md section 4.3.4).
const MinDetectScore = 3

// spanishCharWeight is what one of the Spanish-only characters adds: a single ñ or ¿ is enough
// evidence on its own.
const spanishCharWeight = 3

// The stop-word lists. They are ASCII on purpose: an accented word already scores through its
// accent. A word that means something in both languages (no, me, he, a, son, come, sin, mi) is in
// neither list, so a short text does not lean to one side by accident.
var (
	stopEN = wordSet(`the and is are was were be been of to in on at by for from with about into
		you your it its that this these those they them their we our she his her what which who how
		when where why can could will would should do does did have has had not but or if than then
		there here all any some my an as so just also more most very`)
	stopES = wordSet(`el la los las lo le les de del al que y o es en un una unos unas usted ustedes
		por para con se su sus pero si muy hay este esta esto ese esa eso estos estas cuando donde
		como cual quien porque aunque tambien ya mas ni ser fue estan esta tiene tienen debe
		puede pueden hacer agua nosotros ellos ella uno dos tres`)
)

// The Spanish list is written without accents ("mas", "tambien", "esta"): the matcher folds the
// accents of the text before looking a word up, see fold.

func wordSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// spanishChars are the characters that only Spanish text among the two languages carries.
const spanishChars = "ñáéíóúü¿¡"

// Score is the evidence for each language in a text.
type Score struct{ EN, ES int }

// ScoreText counts, for each language, the words of its stop list and, for Spanish, the
// Spanish-only characters (weight 3 each).
func ScoreText(text string) Score {
	var sc Score
	for _, r := range text {
		if strings.ContainsRune(spanishChars, unicode.ToLower(r)) {
			sc.ES += spanishCharWeight
		}
	}
	for _, w := range words(text) {
		if stopEN[w] {
			sc.EN++
		}
		if stopES[fold(w)] || stopES[w] {
			sc.ES++
		}
	}
	return sc
}

// Detect picks the language of text: "en" or "es" when one scores at least MinDetectScore and
// strictly more than the other; otherwise ok is false (a tie, or too little evidence: "Hello").
func Detect(text string) (lang string, ok bool) {
	sc := ScoreText(text)
	switch {
	case sc.ES > sc.EN && sc.ES >= MinDetectScore:
		return LangES, true
	case sc.EN > sc.ES && sc.EN >= MinDetectScore:
		return LangEN, true
	}
	return "", false
}

// words splits text into lower-case words (letters, and apostrophes inside a word).
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
}

// fold removes the accents of the Spanish vowels so that "más" matches the entry "mas".
func fold(w string) string {
	return strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u").Replace(w)
}
