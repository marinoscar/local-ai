package piper

import (
	"regexp"
	"strings"
	"unicode"
)

// Placeholders of the cleanup. The neutral pass writes these private-use marks; Localize turns them
// into words, so that the language of the text can be decided on text that has no placeholder in it
// (a code block must not tip the detector) and the words follow the language of the voice.
const (
	linkMark = ''
	codeMark = ''
)

// Words the marks become (companion-services.md section 4.3.3).
var placeholderWords = map[string]struct{ link, code string }{
	"en": {"link", "(code omitted)"},
	"es": {"enlace", "(código omitido)"},
}

var (
	urlRe       = regexp.MustCompile(`(?i)(?:\b(?:https?|ftp)://|\bwww\.)[^\s<>()\[\]]+`)
	imageRe     = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	linkRe      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	headingRe   = regexp.MustCompile(`^#{1,6}\s+`)
	quoteRe     = regexp.MustCompile(`^(?:>\s?)+`)
	bulletRe    = regexp.MustCompile(`^(?:[-*+\x{2022}\x{2013}]|\d{1,3}[.)])\s+`)
	tableSepRe  = regexp.MustCompile(`^\|?[\s:|-]*-[\s:|-]*\|?$`)
	angleMarkRe = regexp.MustCompile("<([" + string(linkMark) + "])>")
)

// Clean turns text (possibly Markdown, possibly produced by a model) into what is read aloud:
//
//   - Markdown markup is removed: *, _, ~, backticks, heading marks, > quotes, table pipes (cells
//     become ", " separated) and the table separator row;
//   - URLs and the target of Markdown links become "link" (or "enlace"); the text of a link stays;
//   - fenced code blocks become "(code omitted)" (or "(código omitido)");
//   - list bullets, headings, quotes and table rows each end in a sentence break;
//   - emoji, control and invisible characters are dropped, and white space is collapsed.
//
// lang picks the words of the placeholders: "es" for Spanish, anything else English. Accents and the
// Spanish ¿ and ¡ are kept. The function is pure and never fails; an empty result means there was
// nothing to say.
func Clean(text, lang string) string { return Localize(neutral(text), lang) }

// Localize replaces the placeholder marks of a neutral text with the words of lang.
func Localize(s, lang string) string {
	w, ok := placeholderWords[lang]
	if !ok {
		w = placeholderWords["en"]
	}
	if !strings.ContainsRune(s, linkMark) && !strings.ContainsRune(s, codeMark) {
		return s
	}
	s = strings.NewReplacer(string(linkMark), w.link, string(codeMark), w.code).Replace(s)
	// Two placeholders in a row (two links in a list, two code blocks) read once.
	for _, x := range []string{w.link, w.code} {
		for strings.Contains(s, x+" "+x) {
			s = strings.ReplaceAll(s, x+" "+x, x)
		}
	}
	return s
}

// neutral does the whole cleanup except the choice of the placeholder words.
func neutral(text string) string {
	text = strings.ToValidUTF8(text, "")
	// The marks are ours alone: text that carries them would fake a link or a code block.
	text = strings.NewReplacer(string(linkMark), "", string(codeMark), "").Replace(text)
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n").Replace(text)

	var out strings.Builder
	sentenceEnd := func() { // end the sentence being built, when it is not ended already
		s := out.String()
		if s != "" && !endsSentence(s) {
			out.WriteByte('.')
		}
	}
	add := func(line string, asSentence bool) {
		line = tidy(line)
		if line == "" {
			return
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(line)
		if asSentence {
			sentenceEnd()
		}
	}

	inFence, fenceMark := false, ""
	for _, raw := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(raw)
		if inFence {
			if strings.HasPrefix(trim, fenceMark) && strings.Trim(trim, fenceMark[:1]) == "" {
				inFence = false
			}
			continue
		}
		if m := fenceOpen(trim); m != "" {
			inFence, fenceMark = true, m
			add(string(codeMark), false)
			continue
		}
		if trim == "" {
			sentenceEnd() // a blank line ends the paragraph
			continue
		}
		if tableSepRe.MatchString(trim) && strings.ContainsAny(trim, "|-") && strings.Contains(trim, "-") {
			continue
		}
		line, sentence := trim, false
		if q := quoteRe.FindString(line); q != "" {
			line, sentence = strings.TrimSpace(line[len(q):]), true
		}
		if h := headingRe.FindString(line); h != "" {
			line, sentence = line[len(h):], true
		}
		if b := bulletRe.FindString(line); b != "" {
			line, sentence = line[len(b):], true
		}
		if strings.Contains(line, "|") {
			line, sentence = tableRow(line), true
		}
		add(inline(line), sentence)
	}
	return out.String()
}

// fenceOpen returns the fence marker (``` or ~~~, any length of three or more) that opens a code
// block on this line, or "".
func fenceOpen(trim string) string {
	for _, c := range []string{"`", "~"} {
		n := len(trim) - len(strings.TrimLeft(trim, c))
		if n >= 3 && !strings.Contains(trim[n:], c) {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// tableRow turns "| a | b |" into "a, b".
func tableRow(line string) string {
	var cells []string
	for _, c := range strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|") {
		if c = strings.TrimSpace(c); c != "" {
			cells = append(cells, c)
		}
	}
	return strings.Join(cells, ", ")
}

// inline removes the markup inside one line.
func inline(s string) string {
	s = imageRe.ReplaceAllString(s, "$1")
	// A link keeps its text; the target is dropped. A text that is itself a URL becomes the mark.
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		if strings.TrimSpace(sub[1]) == "" {
			return string(linkMark)
		}
		return sub[1]
	})
	s = urlRe.ReplaceAllString(s, string(linkMark))
	s = angleMarkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("*", "", "`", "", "~", "", "_", " ").Replace(s)
	return s
}

// tidy drops emoji and control characters and collapses white space.
func tidy(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case isEmoji(r), unicode.Is(unicode.Cf, r):
		case unicode.IsControl(r), unicode.IsSpace(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// isEmoji reports whether r is an emoji, a pictograph or a part of an emoji sequence (variation
// selector, zero width joiner, keycap, tag). The ranges are the Unicode blocks the pictographs live
// in; nothing here touches letters, digits, accents or the punctuation of Spanish.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // symbols, pictographs, flags, skin tones, supplemental
		return true
	case r >= 0x2300 && r <= 0x23FF: // technical (watch, hourglass, media controls)
		return true
	case r >= 0x2600 && r <= 0x27BF: // miscellaneous symbols and dingbats
		return true
	case r >= 0x2900 && r <= 0x297F, r >= 0x2B00 && r <= 0x2BFF: // arrows, stars, squares
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r == 0x200D, r == 0x20E3, r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return false
}

// endsSentence reports whether s already ends with a sentence break.
func endsSentence(s string) bool {
	s = strings.TrimRight(s, " )]\"'»”’")
	if s == "" {
		return true
	}
	switch s[len(s)-1] {
	case '.', '!', '?', ';', ':':
		return true
	}
	return strings.HasSuffix(s, "…")
}
