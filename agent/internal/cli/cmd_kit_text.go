package cli

import (
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// The words of `aictl kit status` and `aictl kit verify` in English and Spanish (aictl.md section
// 4.23c). The library reports kinds, needs and names; every sentence an operator reads is composed
// here, and every problem carries its doctor code and the code's fix.

// kitReasons translate the reasons package kit puts in brackets after a file name.
var kitReasons = strings.NewReplacer(
	"(changed)", "(cambiado)", "(missing)", "(falta)", "(unreadable)", "(no se puede leer)",
	"(truncated)", "(truncado)", "(wrong size)", "(tamaño distinto)", "(not a regular file)", "(no es un archivo normal)",
	"(not recorded)", "(sin registro)", "(bad manifest)", "(registro dañado)", "(misplaced)", "(en el lugar equivocado)",
	"(git fsck failed)", "(git fsck falló)", "(SHA256SUMS missing)", "(falta SHA256SUMS)",
	"(SHA256SUMS does not match the manifest)", "(SHA256SUMS no coincide con el registro)",
	"(permission denied)", "(permiso denegado)",
	"(not listed in SHA256SUMS)", "(no está en SHA256SUMS)",
	"(no matching .deb in pool/)", "(no hay un .deb que coincida en pool/)",
	"(a .deb that PACKAGES.lock does not list)", "(un .deb que PACKAGES.lock no lista)")

// kitNames joins file names for a sentence (at most 12, the rest counted), with the reasons in the
// language of the output.
func kitNames(l Lang, names []string) string {
	list := append([]string(nil), names...)
	if l == LangES {
		for i, n := range list {
			list[i] = kitReasons.Replace(n)
		}
	}
	if len(list) <= 12 {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:12], ", ") + fmt.Sprintf(pick(l, " and %d more", " y %d más"), len(list)-12)
}

// kitRootName says which root a report is about.
func kitRootName(l Lang, id string, internal bool) string {
	if internal {
		return pick(l, "this computer's disk", "el disco de este equipo")
	}
	return pick(l, "the drive "+id, "el disco "+id)
}

// kitStateWord is a part's or a need's state in words.
func kitStateWord(l Lang, status string) string {
	switch status {
	case kit.StatusOK:
		return pick(l, "ok", "bien")
	case kit.StatusMissing:
		return pick(l, "missing", "falta")
	case kit.StatusDamaged:
		return pick(l, "damaged", "dañado")
	case kit.StatusInconsistent:
		return pick(l, "does not fit", "no encaja")
	case kit.StatusWarning:
		return pick(l, "warning", "aviso")
	case kit.StatusSkipped:
		return pick(l, "not checked", "sin comprobar")
	}
	return status
}

// kitColourWord is the overall colour of a report in words.
func kitColourWord(l Lang, colour string) string {
	switch colour {
	case kit.Green:
		return pick(l, "green", "verde")
	case kit.Yellow:
		return pick(l, "yellow", "amarillo")
	case kit.Red:
		return pick(l, "red", "rojo")
	}
	return colour
}

// kitNeedText names a requirement ("the llama.cpp source for f3586e66b").
func kitNeedText(l Lang, need, arg string) string {
	switch need {
	case kit.NeedLlamaSource:
		return pick(l, "the llama.cpp source for "+arg, "el código de llama.cpp de "+arg)
	case kit.NeedWhisperSource:
		return pick(l, "the whisper.cpp source for "+arg, "el código de whisper.cpp de "+arg)
	case kit.NeedAgentBinary:
		return pick(l, "the agent program for "+arg, "el programa del agente para "+arg)
	case kit.NeedAgentSource:
		return pick(l, "the agent source "+arg, "el código del agente "+arg)
	case kit.NeedGoToolchain:
		return pick(l, "the Go toolchain for "+arg, "el compilador Go para "+arg)
	case kit.NeedDebs:
		return pick(l, "the offline packages for "+arg, "los paquetes sin conexión para "+arg)
	case kit.NeedNvidia:
		return pick(l, "the NVIDIA driver packages for "+arg, "los paquetes del controlador NVIDIA para "+arg)
	case kit.NeedCuda:
		if arg != "" {
			return pick(l, "the CUDA toolkit "+arg, "el kit de CUDA "+arg)
		}
		return pick(l, "the CUDA toolkit", "el kit de CUDA")
	case kit.NeedKiwix:
		return pick(l, "kiwix-tools for "+arg, "kiwix-tools para "+arg)
	case kit.NeedPmtiles:
		return pick(l, "pmtiles for "+arg, "pmtiles para "+arg)
	case kit.NeedPiper:
		return pick(l, "piper for "+arg, "piper para "+arg)
	case kit.NeedPrebuilt:
		return pick(l, "a prebuilt llama.cpp for "+arg, "un llama.cpp precompilado para "+arg)
	}
	return need + " " + arg
}

// kitFixOf is the fix of a doctor code in the language of the output.
func kitFixOf(l Lang, code string) string {
	if _, fix, ok := doctor.Text(code, string(l)); ok {
		return fix
	}
	return ""
}

// kitProblem words a finding as a problem: one sentence that names what is wrong, the code, and the
// fix of the code. A red finding is an error, a yellow one a warning; a failed signature is critical.
func kitProblem(l Lang, f kit.Finding) out.Problem {
	sev := out.SevWarning
	switch {
	case f.Code == kit.CodeKitBadSignature:
		sev = out.SevCritical
	case f.Colour == kit.Red:
		sev = out.SevError
	}
	return out.Problem{Severity: sev, Code: f.Code, Message: kitFindingText(l, f), Fix: kitFixOf(l, f.Code)}
}

func kitFindingText(l Lang, f kit.Finding) string {
	nodes := strings.Join(f.Nodes, ", ")
	switch f.Kind {
	case kit.KindDamaged:
		return fmt.Sprintf(pick(l,
			"These files of the rebuild kit are damaged or do not match their record: %s.",
			"Estos archivos del kit de reconstrucción están dañados o no coinciden con su registro: %s."), kitNames(l, f.Names))
	case kit.KindBadManifest:
		return fmt.Sprintf(pick(l,
			"The record of this part of the kit (MANIFEST.json) is damaged or in the wrong place: %s.",
			"El registro de esta parte del kit (MANIFEST.json) está dañado o en el lugar equivocado: %s."), kitNames(l, f.Names))
	case kit.KindBadSignature:
		switch {
		case len(f.Names) > 0:
			return fmt.Sprintf(pick(l,
				"These agent files in the kit do not match their signed checksums: %s.",
				"Estos archivos del agente en el kit no coinciden con sus sumas firmadas: %s."), kitNames(l, f.Names))
		case strings.Contains(f.Detail, "missing"):
			return pick(l, "The signed checksums of the agent files are missing from the kit.",
				"Faltan en el kit las sumas firmadas de los archivos del agente.")
		}
		return pick(l, "The signature of the agent files in the kit did not verify.",
			"La firma de los archivos del agente en el kit no se pudo verificar.")
	case kit.KindMissing:
		what := kitNeedText(l, f.Need, f.Arg)
		if len(f.Nodes) > 0 && f.Code == kit.CodeKitMissingLab {
			return fmt.Sprintf(pick(l, "The drive has no %s for %s.", "El disco no tiene %s para %s."), what, nodes)
		}
		return fmt.Sprintf(pick(l, "The kit has no %s for this computer.", "El kit no tiene %s para este equipo."), what)
	case kit.KindWrongRelease:
		return fmt.Sprintf(pick(l,
			"This computer needs packages for %s, but the kit only has: %s.",
			"Este equipo necesita paquetes para %s, pero el kit solo tiene: %s."), f.Want, strings.Join(f.Have, ", "))
	case kit.KindCudaTooNew:
		have := ""
		if len(f.Have) > 0 {
			have = f.Have[0]
		}
		return fmt.Sprintf(pick(l,
			"The kit's CUDA toolkit is version %s, but this computer's graphics driver supports only CUDA %s.",
			"El kit de CUDA es la versión %s, pero el controlador gráfico de este equipo solo admite CUDA %s."), have, f.Want)
	case kit.KindPrebuiltMissing:
		return fmt.Sprintf(pick(l,
			"The kit has no prebuilt llama.cpp for this computer's current build (%s). This is only a warning.",
			"El kit no tiene un llama.cpp precompilado para la compilación actual de este equipo (%s). Es solo un aviso."), f.Arg)
	case kit.KindPinsDamaged:
		return pick(l, "The kit's list of pinned versions (catalog/pins.json) is damaged or newer than this program.",
			"La lista de versiones fijadas del kit (catalog/pins.json) está dañada o es más nueva que este programa.")
	case kit.KindBuildCheck:
		return f.Detail
	case kit.KindUnverifiedRepos:
		return fmt.Sprintf(pick(l,
			"The offline packages for %s include packages from vendor repositories whose signature was not checked: %s.",
			"Los paquetes sin conexión de %s incluyen paquetes de repositorios de proveedores cuya firma no se comprobó: %s."), f.Item, strings.Join(f.Names, ", "))
	}
	return f.Detail
}

// kitNoteText words a note; ok is false for a note that is not worth a line.
func kitNoteText(l Lang, n kit.Note) (msg, code string) {
	names := strings.Join(n.Names, ", ")
	switch n.Kind {
	case kit.NoteUnchecked:
		return pick(l, "The signature of the agent files could not be checked: this program has no release key built in.",
			"No se pudo comprobar la firma de los archivos del agente: este programa no lleva una clave de publicación."), ""
	case kit.NoteSkipped:
		if names == "" {
			return pick(l, "What this computer needs was not checked: its details are not known.",
				"No se comprobó lo que este equipo necesita: no se conocen sus datos."), ""
		}
		return fmt.Sprintf(pick(l, "Not checked, because it is not known for this computer: %s.",
			"Sin comprobar, porque no se conoce en este equipo: %s."), names), ""
	case kit.NoteIndexStale:
		return pick(l, "The kit's index does not match what is on the disk. The kit itself is fine; `aictl ark sync` rewrites the index.",
			"El índice del kit no coincide con lo que hay en el disco. El kit está bien; `aictl ark sync` reescribe el índice."), n.Code
	case kit.NoteStalePins:
		return fmt.Sprintf(pick(l, "Not required, because no computer has reported them for 180 days: %s.",
			"No se exigen, porque ningún equipo las informa desde hace 180 días: %s."), names), ""
	case kit.NoteNoPins:
		return pick(l, "The kit has no readable list of pinned versions, so what the other computers need was not checked.",
			"El kit no tiene una lista legible de versiones fijadas, así que no se comprobó lo que necesitan los otros equipos."), ""
	case kit.NoteNoGit:
		return pick(l, "git is not available, so the git mirrors were checked by their structure only.",
			"git no está disponible, así que los espejos de git se comprobaron solo por su estructura."), ""
	}
	return n.Detail, n.Code
}

// kitSize is a size in the units of the other commands.
func kitSize(n int64) string { return out.Size(n) }
