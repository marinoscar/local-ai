package cli

import "strings"

// The plain-language names of the drill's checks (drill.md section 4.2), in English and Spanish. A check
// id the table does not know (a newer agent) is shown as it is, so nothing is ever hidden.

type drillLabel struct {
	// id is an exact check id, or a prefix ending in "." when arg is set: the part after it is shown in
	// brackets (a model id) or turned into a language name.
	id     string
	en, es string
	arg    drillArg
}

type drillArg int

const (
	argNone drillArg = iota
	// argModel: the rest of the id is a model id, shown as "(<id>)".
	argModel
	// argLang: the rest of the id is a language code, shown as "(English)".
	argLang
)

var drillLabels = []drillLabel{
	{id: "drl.fence", en: "The internet is blocked during the test", es: "Internet está bloqueado durante la prueba"},
	{id: "drl.config.offline_lint", en: "No setting needs the internet", es: "Ningún ajuste necesita internet"},
	{id: "drl.files.manifest", en: "The stored content is complete", es: "El contenido guardado está completo"},
	{id: "drl.files.hash", en: "The stored files match their checksums", es: "Los archivos guardados coinciden con sus sumas"},
	{id: "drl.llm.answer.", en: "Brain answered", es: "El cerebro respondió", arg: argModel},
	{id: "drl.vlm.photo.", en: "Photo brain read a test image", es: "El cerebro de fotos leyó una imagen de prueba", arg: argModel},
	{id: "drl.llm.fit", en: "The survival brains fit in memory", es: "Los cerebros de supervivencia caben en la memoria"},
	{id: "drl.kiwix.search", en: "Library search worked", es: "La búsqueda en la biblioteca funcionó"},
	{id: "drl.portal.firstaid", en: "The first-aid pages open", es: "Las páginas de primeros auxilios abren"},
	{id: "drl.stt.", en: "Speech to text", es: "Voz a texto", arg: argLang},
	{id: "drl.tts.", en: "Text to speech", es: "Texto a voz", arg: argLang},
	{id: "drl.speech.roundtrip", en: "Speech round trip (speak, then listen)", es: "Ida y vuelta de voz (hablar y escuchar)"},
	{id: "drl.maps.tile", en: "Map tiles were served", es: "Se sirvieron los mosaicos del mapa"},
	{id: "drl.maps.search", en: "Place search worked", es: "La búsqueda de lugares funcionó"},
	{id: "drl.portal", en: "The Portal and the chat page open", es: "El Portal y la página del chat abren"},
	{id: "drl.docs", en: "The manuals are in place", es: "Los manuales están en su lugar"},
	{id: "drl.kit.present", en: "The rebuild kit is present", es: "El kit de reconstrucción está presente"},
	{id: "drl.kit.rebuild_dry_run", en: "llama.cpp can be rebuilt offline", es: "llama.cpp se puede recompilar sin conexión"},
	{id: "drl.services", en: "The services are healthy", es: "Los servicios están sanos"},
	{id: "drl.power", en: "The laptop power settings", es: "Los ajustes de energía del portátil"},
	{id: "drl.clock", en: "The clock looks right", es: "El reloj parece correcto"},
	{id: "drl.restore", en: "The normal state was put back", es: "Se restauró el estado habitual"},
}

// drillCheckLabel is the plain name of a check id.
func drillCheckLabel(l Lang, id string) string {
	for _, d := range drillLabels {
		name := pick(l, d.en, d.es)
		if d.arg == argNone {
			if id == d.id {
				return name
			}
			continue
		}
		if rest, ok := strings.CutPrefix(id, d.id); ok && rest != "" {
			if d.arg == argLang {
				return name + " (" + drillLangName(l, rest) + ")"
			}
			return name + " (" + rest + ")"
		}
	}
	return id
}

func drillLangName(l Lang, code string) string {
	switch code {
	case "en":
		return pick(l, "English", "inglés")
	case "es":
		return pick(l, "Spanish", "español")
	}
	return code
}
