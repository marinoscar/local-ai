// Package places is the offline place index of the maps (companion-services.md section 4.4.4): it
// reads the GeoNames per-country dumps straight from their zip files, keeps the rows worth finding in
// an emergency, and answers name search and nearest-of-kind queries from memory.
package places

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// foldTable maps the accented letters of Latin-1 and Latin Extended-A to their plain form. It is
// built in code (no golang.org/x/text); a rune with no entry is kept as it is.
var foldTable = buildFold()

func buildFold() map[rune]string {
	m := map[rune]string{}
	add := func(plain string, accented string) {
		for _, r := range accented {
			m[r] = plain
		}
	}
	// Latin-1 supplement.
	add("a", "àáâãäåÀÁÂÃÄÅ")
	add("ae", "æÆ")
	add("c", "çÇ")
	add("e", "èéêëÈÉÊË")
	add("i", "ìíîïÌÍÎÏ")
	add("d", "ðÐ")
	add("n", "ñÑ")
	add("o", "òóôõöøÒÓÔÕÖØ")
	add("u", "ùúûüÙÚÛÜ")
	add("y", "ýÿÝ")
	add("th", "þÞ")
	add("ss", "ß")
	// Latin Extended-A.
	add("a", "āăąĀĂĄ")
	add("c", "ćĉċčĆĈĊČ")
	add("d", "ďđĎĐ")
	add("e", "ēĕėęěĒĔĖĘĚ")
	add("g", "ĝğġģĜĞĠĢ")
	add("h", "ĥħĤĦ")
	add("i", "ĩīĭįıĨĪĬĮİ")
	add("ij", "ĳĲ")
	add("j", "ĵĴ")
	add("k", "ķĶ")
	add("l", "ĺļľŀłĹĻĽĿŁ")
	add("n", "ńņňŉŋŃŅŇŊ")
	add("o", "ōŏőŌŎŐ")
	add("oe", "œŒ")
	add("r", "ŕŗřŔŖŘ")
	add("s", "śŝşšŚŜŞŠ")
	add("t", "ţťŧŢŤŦ")
	add("u", "ũūŭůűųŨŪŬŮŰŲ")
	add("w", "ŵŴ")
	add("y", "ŷŶŸ")
	add("z", "źżžŹŻŽ")
	return m
}

// Fold normalises a name or a query for matching: lowercase, accents removed through the built-in
// table, every run of spaces and punctuation collapsed to one space, and no space at either end.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true // true at the start so a leading separator writes nothing
	for _, r := range s {
		if r == utf8.RuneError {
			continue
		}
		if p, ok := foldTable[r]; ok {
			b.WriteString(p)
			space = false
			continue
		}
		r = unicode.ToLower(r)
		if p, ok := foldTable[r]; ok { // a capital the table lists only in lower case
			b.WriteString(p)
			space = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}
