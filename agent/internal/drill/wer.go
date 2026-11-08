package drill

import (
	"strings"
	"unicode"
)

// The normalised word error rate of the speech checks (drill.md sections 4.2 checks 10 and 12, and 9.1).
// Both texts are normalised the same way before they are compared, so a transcript that differs from
// the reference only in casing, punctuation or accents is a perfect match, and a result is comparable
// from month to month:
//
//	lower case, accents folded (a-acute to a, n-tilde to n, u-diaeresis to u), apostrophes dropped,
//	every other character that is not a letter or a digit is a word break.
//
// WER is the word-level Levenshtein distance (substitutions, insertions and deletions each cost 1)
// divided by the number of words of the reference, so it can exceed 1 when the hypothesis is much
// longer than the reference.

// accentFold maps the accented Latin letters of English and Spanish (and the neighbouring Romance
// languages) to their base letter. It is a table, not Unicode decomposition, because the agent vendors
// its dependencies and golang.org/x/text is not one of them.
var accentFold = map[rune]rune{
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a',
	'ç': 'c', 'ć': 'c', 'č': 'c',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ę': 'e', 'ě': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i',
	'ñ': 'n', 'ń': 'n', 'ň': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o', 'ō': 'o',
	'š': 's', 'ś': 's',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u', 'ů': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
}

// NormalizeWords returns the words of s after the normalisation above.
func NormalizeWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		r = unicode.ToLower(r)
		if f, ok := accentFold[r]; ok {
			r = f
		}
		switch {
		case r == '\'' || r == '’' || r == '‘' || r == '`':
			// "don't" and "dont" are the same word.
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return words
}

// NormalizeText is the normalised text: the words of s joined by single spaces.
func NormalizeText(s string) string { return strings.Join(NormalizeWords(s), " ") }

// WER is the normalised word error rate of hyp against ref. An empty reference gives 0 for an empty
// hypothesis and 1 for any other.
func WER(ref, hyp string) float64 {
	r, h := NormalizeWords(ref), NormalizeWords(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	return float64(editDistance(r, h)) / float64(len(r))
}

// editDistance is the word-level Levenshtein distance, in two rows of memory.
func editDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
