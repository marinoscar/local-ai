package router

import (
	"errors"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Code is a doctor code (doctor-codes.md) the fit rules can raise.
type Code string

// Doctor codes of the fit rules (llama-router.md section 4.3, agent.md section 3.8.7).
//
// The constants alias those of internal/doctor/codes.go, the one file allowed to spell a "LAI-..."
// literal (scripts/check-doctor-codes.sh). The English and Spanish texts below are the router's own
// plain-language reasons, built from the situation (model names, sizes); the generic title and fix
// of each code are in the doctor table (doctor.Text).
const (
	// CodeNotFit: the model can never load on this node (backend, graphics memory or RAM).
	CodeNotFit Code = doctor.CAT005
	// CodeFilesMissing: the model files are missing or not verified.
	CodeFilesMissing Code = doctor.LLM008
	// CodeNoRoom: not enough room to load without unloading others (and unload_others is false).
	CodeNoRoom Code = doctor.LLM011
	// CodeRouterDown: the router does not answer. Never produced here; the API layer sets it as the
	// loadable_reason when it cannot reach the router (agent.md section 3.8.7).
	CodeRouterDown Code = doctor.LLM004
	// CodeKeyRejected: the router answers 401 to this node's own key (the key file and the unit's
	// credential disagree). Raised by the watcher.
	CodeKeyRejected Code = doctor.LLM005
	// CodeModelFailed: a model failed to start or stopped unexpectedly (the router reports
	// failed: true). Raised by the watcher.
	CodeModelFailed Code = doctor.LLM009
	// CodeCacheModels: ids that are not in the preset (LLAMA_CACHE models) are visible. Raised by
	// the watcher.
	CodeCacheModels Code = doctor.LLM024
	// CodeTooManyLoaded: more models are loaded than models_max allows. Raised by the watcher.
	CodeTooManyLoaded Code = doctor.LLM028
	// CodeOthersVRAM: other programs hold a lot of graphics memory; the load goes ahead anyway.
	CodeOthersVRAM Code = doctor.HW005
	// CodeRAMLow: other programs hold most of the RAM; the load goes ahead anyway.
	CodeRAMLow Code = doctor.HW006
)

// Reason is a code with its plain-language message, the shape of loadable_reason in GET /models
// (agent.md section 3.8.7): {"code": "...", "message": "..."}. MessageES is the Spanish text; it
// is not on the wire, use Text to pick a language.
type Reason struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	MessageES string `json:"-"`
}

// Text returns the message in the given language ("es" and region variants such as "es-CR" give
// Spanish; anything else English).
func (r Reason) Text(lang string) string {
	l := strings.ToLower(lang)
	if (l == "es" || strings.HasPrefix(l, "es-") || strings.HasPrefix(l, "es_")) && r.MessageES != "" {
		return r.MessageES
	}
	return r.Message
}

// FormatGB formats a size in MiB as decimal gigabytes with one decimal ("11.0"), the unit of every
// user-facing size (aictl.md section 3.3): MiB * 1048576 / 1e9.
func FormatGB(mib int64) string { return gb(mib, ".") }

// gb formats MiB as decimal GB with the given decimal separator.
func gb(mib int64, sep string) string {
	s := fmt.Sprintf("%.1f", float64(mib)*1048576/1e9)
	if sep != "." {
		s = strings.Replace(s, ".", sep, 1)
	}
	return s
}

// sized builds a Reason from an English and a Spanish format whose arguments are sizes in MiB,
// rendered as GB with a point (English) or a comma (Spanish).
func sized(code Code, en, es string, mibs ...int64) Reason {
	enArgs := make([]any, len(mibs))
	esArgs := make([]any, len(mibs))
	for i, v := range mibs {
		enArgs[i] = gb(v, ".")
		esArgs[i] = gb(v, ",")
	}
	return Reason{Code: code, Message: fmt.Sprintf(en, enArgs...), MessageES: fmt.Sprintf(es, esArgs...)}
}

func reasonBackend(backend string) Reason {
	return Reason{
		Code:      CodeNotFit,
		Message:   fmt.Sprintf("This model does not run on %s.", backend),
		MessageES: fmt.Sprintf("Este modelo no funciona con %s.", backend),
	}
}

func reasonGraphics(need, total int64) Reason {
	return sized(CodeNotFit,
		"Needs %s GB of graphics memory; this computer has %s GB.",
		"Necesita %s GB de memoria gráfica; este equipo tiene %s GB.",
		need, total)
}

func reasonMemoryReserve(need, reserve, total int64) Reason {
	return sized(CodeNotFit,
		"Needs %s GB of memory, and the system keeps %s GB for itself; this computer has %s GB.",
		"Necesita %s GB de memoria, y el sistema reserva %s GB para sí mismo; este equipo tiene %s GB.",
		need, reserve, total)
}

func reasonMemory(need, total int64) Reason {
	return sized(CodeNotFit,
		"Needs %s GB of memory; this computer has %s GB.",
		"Necesita %s GB de memoria; este equipo tiene %s GB.",
		need, total)
}

func reasonFilesMissing() Reason {
	return Reason{
		Code:      CodeFilesMissing,
		Message:   "The files of this model are missing or not verified on this computer.",
		MessageES: "Los archivos de este modelo faltan o no están verificados en este equipo.",
	}
}

func reasonNoRoom() Reason {
	return Reason{
		Code:      CodeNoRoom,
		Message:   "There is not enough room to load this model without unloading another one.",
		MessageES: "No hay espacio suficiente para cargar este modelo sin descargar otro.",
	}
}

func reasonOthersVRAM() Reason {
	return Reason{
		Code:      CodeOthersVRAM,
		Message:   "Other programs are using a lot of graphics memory; this model may run slower.",
		MessageES: "Otros programas usan mucha memoria gráfica; este modelo puede funcionar más lento.",
	}
}

func reasonRAMLow() Reason {
	return Reason{
		Code:      CodeRAMLow,
		Message:   "Other programs are using most of the memory; this model may run slower.",
		MessageES: "Otros programas usan casi toda la memoria; este modelo puede funcionar más lento.",
	}
}

// CodeOf maps an error of the client to the doctor code a caller raises or returns for it: router
// down or not answering (LAI-LLM-004), or our key rejected (LAI-LLM-005). Any other error gives "".
func CodeOf(err error) Code {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return CodeKeyRejected
	case errors.Is(err, ErrDown):
		return CodeRouterDown
	}
	return ""
}
