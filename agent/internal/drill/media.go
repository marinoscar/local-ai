package drill

import (
	_ "embed" // the test recordings are part of the binary
	"strings"
)

// The test recordings of the speech checks (drill.md section 3.2). They are fixed, embedded in the agent
// and self-made (media/README.md): no user data is used or sent, and the node needs no file on disk.

//go:embed media/en.wav
var mediaEnWAV []byte

//go:embed media/es.wav
var mediaEsWAV []byte

//go:embed media/en.txt
var mediaEnTXT string

//go:embed media/es.txt
var mediaEsTXT string

// Sample is a recording and the text it says.
type Sample struct {
	// Language is "en" or "es".
	Language string
	// WAV is the recording: 16 kHz, mono, 16-bit PCM.
	WAV []byte
	// Text is what it says, the reference of the word error rate.
	Text string
}

// EnglishSample returns the English recording ("The quick brown fox jumps over the lazy dog."). The
// returned bytes are a copy; callers may not change the embedded data.
func EnglishSample() Sample {
	return Sample{Language: "en", WAV: append([]byte(nil), mediaEnWAV...), Text: strings.TrimSpace(mediaEnTXT)}
}

// SpanishSample returns the Spanish recording ("El veloz murcielago hindu comia feliz cardillo y kiwi.").
func SpanishSample() Sample {
	return Sample{Language: "es", WAV: append([]byte(nil), mediaEsWAV...), Text: strings.TrimSpace(mediaEsTXT)}
}
