package piper

import (
	"strings"
	"testing"
)

func TestCleanTable(t *testing.T) {
	cases := []struct{ name, lang, in, want string }{
		{"plain text is unchanged", "en", "Press firmly on the wound with a clean cloth.", "Press firmly on the wound with a clean cloth."},
		{"emphasis and underscores", "en", "This is **very** important and _really_ urgent", "This is very important and really urgent"},
		{"snake case reads as words", "en", "set the water_level now", "set the water level now"},
		{"heading ends the sentence", "en", "# First aid\nPress on the wound.", "First aid. Press on the wound."},
		{"deep heading", "en", "###### Small title\nBody", "Small title. Body"},
		{"inline code keeps its text", "en", "Run `aictl doctor` first", "Run aictl doctor first"},
		{"block quote", "en", "> Stay calm.\n> Breathe.", "Stay calm. Breathe."},
		{"dash bullets", "en", "- boil water\n- let it cool\n- drink", "boil water. let it cool. drink."},
		{"star and numbered bullets", "en", "* one\n* two\n1. three\n2) four", "one. two. three. four."},
		{"bullet keeps its own punctuation", "en", "- Wash your hands!\n- Dry them?", "Wash your hands! Dry them?"},
		{"paragraphs end the sentence", "en", "First paragraph\n\nSecond paragraph", "First paragraph. Second paragraph"},
		{"a wrapped paragraph is one sentence", "en", "line one\nline two", "line one line two"},
		{"url becomes link", "en", "See https://example.test/a/b?c=d for more.", "See link for more."},
		{"www url", "en", "Visit www.example.test today", "Visit link today"},
		{"url in Spanish", "es", "Vea https://example.test/ahora", "Vea enlace"},
		{"markdown link keeps its text", "en", "Read [the guide](https://example.test/guide) now", "Read the guide now"},
		{"image keeps its alt text", "en", "![a diagram](https://example.test/d.png) shows it", "a diagram shows it"},
		{"link with an empty text", "en", "[](https://example.test)", "link"},
		{"angle bracket url", "en", "Go to <https://example.test> now", "Go to link now"},
		{"two links in a row read once", "en", "https://example.test/a https://example.test/b", "link"},
		{"code block", "en", "Run this:\n```\nsudo rm -rf /\n```\nThen wait.", "Run this: (code omitted) Then wait."},
		{"code block with a language tag", "en", "```bash\nls\n```", "(code omitted)"},
		{"code block in Spanish", "es", "Ejecute:\n```\nls\n```", "Ejecute: (código omitido)"},
		{"tilde fence", "en", "~~~\ncode\n~~~\nafter", "(code omitted) after"},
		{"unterminated fence swallows the rest", "en", "before\n```\ncode that never ends", "before (code omitted)"},
		{"table cells", "en", "| Name | Dose |\n|------|------|\n| Salt | 1 tsp |", "Name, Dose. Salt, 1 tsp."},
		{"table separator with alignment", "en", "a | b\n:--- | ---:\nc | d", "a, b. c, d."},
		{"horizontal rule is dropped", "en", "above\n---\nbelow", "above below"},
		{"emoji are dropped", "en", "Stay safe \U0001F600\U0001F44D now \u2764\uFE0F", "Stay safe now"},
		{"flags and zwj sequences", "en", "ok \U0001F1E8\U0001F1F7 \U0001F468\u200D\U0001F469\u200D\U0001F467 done", "ok done"},
		{"keycap keeps the digit", "en", "press 1\uFE0F\u20E3 now", "press 1 now"},
		{"only emoji", "en", "\U0001F600\U0001F600", ""},
		{"control characters", "en", "a\x00b\x07c\u200bd\u202ee", "a b cde"}, // invisible format characters are dropped without splitting the word
		{"tabs and runs of spaces", "en", "a \t\t b    c", "a b c"},
		{"crlf and cr", "en", "one\r\ntwo\rthree", "one two three"},
		{"unicode line separators", "en", "one\u2028two", "one two"},
		{"Spanish punctuation and accents are kept", "es", "¿Cómo purifico el agua? ¡Hierva el agua durante un minuto!", "¿Cómo purifico el agua? ¡Hierva el agua durante un minuto!"},
		{"eñe is kept", "es", "El niño duerme", "El niño duerme"},
		{"private marks in the input are not placeholders", "en", "a\uE000b\uE001c", "abc"},
		{"empty", "en", "", ""},
		{"blanks only", "en", " \n\t\n ", ""},
		{"unknown language falls back to English words", "fr", "https://example.test", "link"},
		{"invalid utf-8 is dropped", "en", "a\xffb", "ab"},
	}
	for _, c := range cases {
		if got := Clean(c.in, c.lang); got != c.want {
			t.Errorf("%s:\n  Clean(%q, %q)\n  got  %q\n  want %q", c.name, c.in, c.lang, got, c.want)
		}
	}
}

func TestCleanIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"# Title\n- a\n- b\n\n```\ncode\n```\nSee https://example.test.",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		"¿Qué hora es? \U0001F600 **Ya**",
	} {
		once := Clean(in, "en")
		if twice := Clean(once, "en"); twice != once {
			t.Errorf("not idempotent for %q:\n  once  %q\n  twice %q", in, once, twice)
		}
	}
}

func TestCleanNeverLeavesMarkupOrMarks(t *testing.T) {
	in := "# T\n**b** _i_ `c` > q\n- x\n| a | b |\n|---|---|\nhttps://example.test\n```\nz\n```"
	got := Clean(in, "es")
	for _, bad := range []string{"*", "_", "`", "#", "|", "http", "\uE000", "\uE001"} {
		if strings.Contains(got, bad) {
			t.Errorf("cleaned text still contains %q: %q", bad, got)
		}
	}
}

func TestNeutralTextCarriesNoWordsOfAPlaceholder(t *testing.T) {
	// The language is detected on this text: a code block must not tip the detector.
	n := neutral("Hola amigo, ¿cómo está?\n```\nif not x and y or z:\n    for each in the list\n```\n")
	if strings.Contains(n, "if not") || strings.Contains(n, "for each") {
		t.Errorf("code leaked into the neutral text: %q", n)
	}
	if lang, ok := Detect(n); !ok || lang != LangES {
		t.Errorf("Detect = %q, %v; the code block must not make this English", lang, ok)
	}
}

func BenchmarkCleanLargeMarkdown(b *testing.B) {
	in := strings.Repeat("- **item** with a [link](https://example.test/x) and `code`\n", 800)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Clean(in, "en")
	}
}
