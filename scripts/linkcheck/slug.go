package main

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	reImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reRefLink    = regexp.MustCompile(`\[([^\]]*)\]\[[^\]]*\]`)
	reHTMLTag    = regexp.MustCompile(`<[^>]*>`)
	reCodeSpan   = regexp.MustCompile("`+([^`]*)`+")
	reUnderEmph  = regexp.MustCompile(`(^|[^\pL\pN_])_+([^_]+?)_+($|[^\pL\pN_])`)
	reATXClosing = regexp.MustCompile(`\s+#+\s*$`)
)

// headingText reduces the raw markdown of a heading to the text GitHub renders,
// which is what its anchor slug is computed from.
func headingText(raw string) string {
	s := strings.TrimSpace(raw)
	s = reATXClosing.ReplaceAllString(s, "")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reRefLink.ReplaceAllString(s, "$1")
	s = reCodeSpan.ReplaceAllString(s, "$1")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "*", "")
	// Emphasis with underscores only counts at word boundaries (snake_case keeps its underscores).
	for i := 0; i < 3; i++ {
		n := reUnderEmph.ReplaceAllString(s, "$1$2$3")
		if n == s {
			break
		}
		s = n
	}
	return s
}

// slugify implements GitHub's heading anchor rule: lowercase, drop everything except
// letters, digits, combining marks, '_' and '-', and turn each space into '-'
// (no collapsing: "A & B" becomes "a--b"; a leading emoji leaves a leading '-').
func slugify(raw string) string {
	var b strings.Builder
	for _, r := range headingText(raw) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
