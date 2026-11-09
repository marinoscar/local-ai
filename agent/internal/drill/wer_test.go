package drill

import (
	"math"
	"testing"
)

func TestNormalizeWords(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     []string
	}{
		{"casing and punctuation", "The Quick, BROWN fox -- jumps!", []string{"the", "quick", "brown", "fox", "jumps"}},
		{"accents are folded", "Murciélago hindú comía; PIÑA über", []string{"murcielago", "hindu", "comia", "pina", "uber"}},
		{"apostrophes join", "Don't stop; it’s here", []string{"dont", "stop", "its", "here"}},
		{"digits are words", "route 66, exit 7b", []string{"route", "66", "exit", "7b"}},
		{"spaces and newlines collapse", "  a \n\t b  ", []string{"a", "b"}},
		{"nothing but punctuation", "... ?!", nil},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeWords(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("NormalizeWords(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("NormalizeWords(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestWER(t *testing.T) {
	const en = "The quick brown fox jumps over the lazy dog."
	const es = "El veloz murciélago hindú comía feliz cardillo y kiwi."
	for _, tc := range []struct {
		name, ref, hyp string
		want           float64
	}{
		{"identical", en, en, 0},
		{"casing and punctuation only", en, "the QUICK brown fox, jumps over the lazy dog", 0},
		{"accents folded both ways", es, "el veloz murcielago hindu comia feliz cardillo y kiwi", 0},
		{"accents added by the recogniser", "el avion aterrizo", "El avión aterrizó.", 0},
		{"one substitution in nine", en, "The quick brown fox jumps over the lazy cat.", 1.0 / 9},
		{"one deletion in nine", en, "The quick brown fox jumps over the dog.", 1.0 / 9},
		{"one insertion in nine", en, "The quick brown fox jumps right over the lazy dog.", 1.0 / 9},
		{"tiny model on the Spanish sample", es, "El veloz murcea la bovindu comía feliz cardiño y kiwi.", 4.0 / 9},
		{"nothing heard", en, "", 1},
		{"all wrong, longer", "a b", "x y z w", 2},
		{"empty reference, empty hypothesis", "", "...", 0},
		{"empty reference, words heard", "", "hello", 1},
		{"word order matters", "one two three four", "one three two four", 2.0 / 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WER(tc.ref, tc.hyp); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("WER(%q, %q) = %v, want %v", tc.ref, tc.hyp, got, tc.want)
			}
		})
	}
}

// TestSamplesAreWhatTheSpecSays: the embedded recordings are 16 kHz mono 16-bit WAVs of about four
// seconds, and the reference text is the sentence of drill.md section 3.2.
func TestSamplesAreWhatTheSpecSays(t *testing.T) {
	for _, tc := range []struct {
		s    Sample
		lang string
		text string
	}{
		{EnglishSample(), "en", "The quick brown fox jumps over the lazy dog."},
		{SpanishSample(), "es", "El veloz murciélago hindú comía feliz cardillo y kiwi."},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			if tc.s.Language != tc.lang || tc.s.Text != tc.text {
				t.Errorf("sample = %q %q", tc.s.Language, tc.s.Text)
			}
			b := tc.s.WAV
			if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
				t.Fatalf("not a WAV (%d bytes)", len(b))
			}
			le32 := func(o int) int { return int(b[o]) | int(b[o+1])<<8 | int(b[o+2])<<16 | int(b[o+3])<<24 }
			le16 := func(o int) int { return int(b[o]) | int(b[o+1])<<8 }
			if le16(20) != 1 || le16(22) != 1 || le32(24) != 16000 || le16(34) != 16 {
				t.Errorf("format = tag %d, %d channels, %d Hz, %d bits; want PCM mono 16000 16", le16(20), le16(22), le32(24), le16(34))
			}
			seconds := float64(len(b)-44) / (16000 * 2)
			if seconds < 3 || seconds > 6 {
				t.Errorf("length = %.2f s, want about 4 s", seconds)
			}
		})
	}
	// The returned bytes are a copy.
	a := EnglishSample()
	a.WAV[0] = 'X'
	if EnglishSample().WAV[0] != 'R' {
		t.Error("the embedded recording can be changed through the returned slice")
	}
}
