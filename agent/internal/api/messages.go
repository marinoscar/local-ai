package api

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// text is the one-sentence message and the one-sentence fix of a code in one language.
type text struct{ Message, Fix string }

// Languages the envelope is localized into. Codes and field names never change (agent.md 3.7).
const (
	langEN = doctor.LangEN
	langES = doctor.LangES
)

// unknownText is what a code outside the doctor table gets when its handler gave no message.
var unknownText = map[string]text{
	langEN: {"The request failed.", "Run `aictl doctor` to see what is wrong."},
	langES: {"La solicitud falló.", "Ejecute `aictl doctor` para ver qué falla."},
}

// lookup returns the message and fix of code in lang from the doctor code table
// (internal/doctor/codes.go), falling back to English, then to the generic text for a code the
// table does not know.
func lookup(code, lang string) text {
	if title, fix, ok := doctor.Text(code, lang); ok {
		return text{title, fix}
	}
	if t, ok := unknownText[lang]; ok {
		return t
	}
	return unknownText[langEN]
}
