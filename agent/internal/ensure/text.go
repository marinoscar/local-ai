package ensure

import (
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The words people read (ADR-0009: state first, plain language, English and Spanish). The doctor codes carry
// the fix of a failure; this file words the state, the steps and the single next thing to do.

// Lang is "en" or "es"; anything else reads as English.
func spanish(lang string) bool { return strings.HasPrefix(strings.ToLower(lang), "es") }

func pick(lang, en, es string) string {
	if spanish(lang) {
		return es
	}
	return en
}

// Title is the name of a step for a person.
func Title(id, lang string) string {
	switch id {
	case IDPackages:
		return pick(lang, "System packages for the AI engine", "Paquetes del sistema para el motor de IA")
	case IDKiwix:
		return pick(lang, "Library tools (kiwix-tools)", "Herramientas de la biblioteca (kiwix-tools)")
	case IDPmtiles:
		return pick(lang, "Map tools (pmtiles)", "Herramientas de mapas (pmtiles)")
	case IDPiper:
		return pick(lang, "Voice tools (piper)", "Herramientas de voz (piper)")
	case IDWhisperRestore:
		return pick(lang, "Speech-to-text program (ready-made)", "Programa de voz a texto (ya compilado)")
	case IDWhisperBuild:
		return pick(lang, "Speech-to-text program (build from source)", "Programa de voz a texto (compilar desde el código)")
	case IDRestartKiwix:
		return pick(lang, "Restart the library service", "Reiniciar el servicio de biblioteca")
	}
	return id
}

// short is the name of a step inside a sentence.
func short(id, lang string) string {
	switch id {
	case IDPackages:
		return pick(lang, "the system packages", "los paquetes del sistema")
	case IDKiwix:
		return "kiwix-tools"
	case IDPmtiles:
		return "pmtiles"
	case IDPiper:
		return "piper"
	case IDWhisperRestore, IDWhisperBuild:
		return pick(lang, "the speech-to-text program", "el programa de voz a texto")
	case IDRestartKiwix:
		return pick(lang, "the library service", "el servicio de biblioteca")
	}
	return id
}

// detailOf is the one sentence under a step.
func detailOf(s Step, lang string) string {
	name := short(s.ID, lang)
	switch s.Status {
	case StatusOK:
		if s.Version != "" {
			return fmt.Sprintf(pick(lang, "Installed (%s).", "Instalado (%s)."), s.Version)
		}
		return pick(lang, "Installed.", "Instalado.")
	case StatusRunning:
		return pick(lang, "Installing now.", "Instalando ahora.")
	case StatusDone:
		if s.Version != "" {
			return fmt.Sprintf(pick(lang, "Installed from the kit (%s).", "Instalado desde el kit (%s)."), s.Version)
		}
		return pick(lang, "Installed from the kit.", "Instalado desde el kit.")
	case StatusCancelled:
		return pick(lang, "Stopped before it started.", "Detenido antes de empezar.")
	case StatusFailed:
		if _, fix, ok := doctor.Text(s.Code, lang); ok {
			return fmt.Sprintf(pick(lang, "It did not install. %s", "No se instaló. %s"), fix)
		}
		return pick(lang, "It did not install. Try again; the message in the log says why.", "No se instaló. Intente otra vez; el mensaje del registro dice por qué.")
	case StatusBlocked:
		switch s.Reason {
		case ReasonKitDamaged:
			return fmt.Sprintf(pick(lang,
				"The kit's copy of %s is damaged, so nothing was installed. Sync the AI-ARK drive again.",
				"La copia de %s del kit está dañada, así que no se instaló nada. Sincronice otra vez la unidad AI-ARK."), name)
		case ReasonNotInKit:
			return fmt.Sprintf(pick(lang,
				"The kit does not carry %s for this computer: %s. Sync the AI-ARK drive with internet.",
				"El kit no incluye %s para esta computadora: %s. Sincronice la unidad AI-ARK con internet."), name, strings.Join(s.Packages, ", "))
		}
		return fmt.Sprintf(pick(lang,
			"The kit does not have %s yet. Connect the AI-ARK drive or sync with internet.",
			"El kit todavía no tiene %s. Conecte la unidad AI-ARK o sincronice con internet."), name)
	case StatusNeedsConfirm:
		switch s.ID {
		case IDWhisperBuild:
			return pick(lang, "Building it takes a long time, so it starts only when you confirm.",
				"Compilarlo tarda mucho, así que empieza solo cuando usted lo confirme.")
		case IDRestartKiwix:
			return pick(lang, "The library is unavailable for a few seconds. It restarts only when you confirm.",
				"La biblioteca no estará disponible unos segundos. Se reinicia solo cuando usted lo confirme.")
		}
	case StatusPending, StatusWaiting:
		switch {
		case s.ID == IDPackages:
			return fmt.Sprintf(pick(lang, "Install %d packages from the kit.", "Instalar %d paquetes desde el kit."), len(s.Packages))
		case s.From != "" && s.Version != "":
			return fmt.Sprintf(pick(lang, "Update from %s to %s, from the kit.", "Actualizar de %s a %s, desde el kit."), s.From, s.Version)
		case s.Version != "":
			return fmt.Sprintf(pick(lang, "Install version %s from the kit.", "Instalar la versión %s desde el kit."), s.Version)
		}
		return pick(lang, "Install it from the kit.", "Instalarlo desde el kit.")
	}
	return ""
}

// messageFor is the state line: "Complete", "Installing what's missing (2 of 4)", "Needs you: ...".
func messageFor(state State, steps []Step, p Progress, lang string) string {
	switch state {
	case StateComplete:
		return pick(lang, "Complete: this computer has everything it needs.", "Completo: esta computadora tiene todo lo que necesita.")
	case StateInstalling:
		n := p.Done + 1
		if n > p.Total {
			n = p.Total
		}
		return fmt.Sprintf(pick(lang, "Installing what's missing (%d of %d)", "Instalando lo que falta (%d de %d)"), n, p.Total)
	case StateOff:
		return fmt.Sprintf(pick(lang,
			"Automatic install is off: %d things are missing. Press Install what's missing, or run `aictl ensure`.",
			"La instalación automática está apagada: faltan %d cosas. Pulse Instalar lo que falta, o ejecute `aictl ensure`."), countWork(steps))
	}
	return pick(lang, "Needs you: ", "Necesita su ayuda: ") + needsYouReason(steps, lang)
}

func countWork(steps []Step) int {
	n := 0
	for _, s := range steps {
		if s.Status != StatusOK && s.Status != StatusDone {
			n++
		}
	}
	return n
}

// first returns the first step with one of the statuses.
func first(steps []Step, statuses ...Status) (Step, bool) {
	for _, st := range statuses {
		for _, s := range steps {
			if s.Status == st {
				return s, true
			}
		}
	}
	return Step{}, false
}

// needsYouReason is the one thing a person must do, by priority: a failure, a confirmation, a missing kit
// item, then the button.
func needsYouReason(steps []Step, lang string) string {
	if s, ok := first(steps, StatusFailed); ok {
		return fmt.Sprintf(pick(lang, "%s could not be installed.", "No se pudo instalar %s."), short(s.ID, lang))
	}
	if s, ok := first(steps, StatusNeedsConfirm); ok {
		if s.ID == IDRestartKiwix {
			return pick(lang, "restart the library service to use the new tools.", "reinicie el servicio de biblioteca para usar las herramientas nuevas.")
		}
		return pick(lang, "building the speech-to-text program takes a long time and needs your OK.", "compilar el programa de voz a texto tarda mucho y necesita su aprobación.")
	}
	if s, ok := first(steps, StatusBlocked); ok {
		return fmt.Sprintf(pick(lang, "the kit does not have %s yet. Sync the AI-ARK drive.", "el kit todavía no tiene %s. Sincronice la unidad AI-ARK."), short(s.ID, lang))
	}
	n := 0
	for _, s := range steps {
		if s.Status == StatusPending || s.Status == StatusWaiting {
			n++
		}
	}
	if n == 1 {
		return pick(lang, "1 thing is ready to install. Press Install what's missing.", "1 cosa está lista para instalar. Pulse Instalar lo que falta.")
	}
	return fmt.Sprintf(pick(lang, "%d things are ready to install. Press Install what's missing.", "%d cosas están listas para instalar. Pulse Instalar lo que falta."), n)
}

// nextFor is the single next action, by the same priority.
func nextFor(state State, steps []Step, lang string) NextAction {
	switch state {
	case StateComplete:
		return NextAction{Kind: NextNone, Text: pick(lang, "Nothing to do.", "No hay nada que hacer.")}
	case StateInstalling:
		return NextAction{Kind: NextWait, Text: pick(lang, "Wait: it keeps going by itself. You can cancel it.", "Espere: sigue solo. Puede cancelarlo.")}
	}
	if s, ok := first(steps, StatusFailed); ok {
		text := pick(lang, "Press Install what's missing to try again.", "Pulse Instalar lo que falta para intentar otra vez.")
		if _, fix, ok := doctor.Text(s.Code, lang); ok {
			text = fix
		}
		return NextAction{Kind: NextFix, Text: text, Step: s.ID}
	}
	if s, ok := first(steps, StatusNeedsConfirm); ok {
		return NextAction{Kind: NextConfirm, Step: s.ID, Text: pick(lang, "Confirm it when you are ready.", "Confírmelo cuando esté listo.")}
	}
	if s, ok := first(steps, StatusPending, StatusWaiting); ok {
		return NextAction{Kind: NextPressButton, Step: s.ID, Text: pick(lang, "Press Install what's missing.", "Pulse Instalar lo que falta.")}
	}
	if s, ok := first(steps, StatusBlocked); ok {
		return NextAction{Kind: NextSync, Step: s.ID, Text: pick(lang,
			"Connect the AI-ARK drive that has the kit, or sync with internet (`aictl ark sync`).",
			"Conecte la unidad AI-ARK que tiene el kit, o sincronice con internet (`aictl ark sync`).")}
	}
	return NextAction{Kind: NextNone, Text: pick(lang, "Nothing to do.", "No hay nada que hacer.")}
}
