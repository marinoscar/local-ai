package cli

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes aictl itself raises (aictl.md section 8, doctor-codes.md section 2).
//
// The constants alias those of agent/internal/doctor/codes.go, the one file allowed to spell a code
// literal (scripts/check-doctor-codes.sh).
//
// The en/es strings below are this package's own table. internal/doctor holds text for every code
// (doctor.Text), so the table could become a lookup into it in a later change.
const (
	// CodeUnreachable: the agent cannot be reached (socket and TCP both failed, or it is still starting).
	CodeUnreachable = doctor.CLI001
	// CodePermission: the socket refused this user and there is no usable token.
	CodePermission = doctor.CLI002
	// CodeVersionSkew: aictl and the agent report different versions (a warning).
	CodeVersionSkew = doctor.CLI003
	// CodeTimeout: the command's overall deadline passed.
	CodeTimeout = doctor.CLI004
	// CodeUsage: unknown command, unknown option or an invalid option value.
	CodeUsage = doctor.CLI005
	// CodeNeedRoot: the command must run as root (RequireRoot).
	CodeNeedRoot = doctor.CLI006
	// CodeBadResponse: the agent sent an answer aictl cannot decode.
	CodeBadResponse = doctor.CLI009
	// CodeInterrupted: the user pressed Ctrl-C while waiting.
	CodeInterrupted = doctor.CLI010
)

// Severities of a code, as in the doctor registry.
const (
	sevError   = "error"
	sevWarning = "warning"
	sevInfo    = "info"
)

// Lang is the language of human output: "en" or "es". Codes and field names never change.
type Lang string

// Supported languages.
const (
	LangEN Lang = "en"
	LangES Lang = "es"
)

// text is the one-sentence message of a code and its one-sentence fix.
type text struct{ Message, Fix string }

type codeInfo struct {
	Exit     int
	Severity string
	En, Es   text
}

var codeTable = map[string]codeInfo{
	CodeUnreachable: {ExitUnreachable, sevError,
		text{"The AI node agent is not running or cannot be reached.",
			"Start it with `sudo systemctl start localai-agent`. If it keeps stopping, run `journalctl -u localai-agent -n 50`."},
		text{"El agente del nodo de IA no está en ejecución o no responde.",
			"Inícielo con `sudo systemctl start localai-agent`. Si se detiene otra vez, ejecute `journalctl -u localai-agent -n 50`."}},
	CodePermission: {ExitDenied, sevError,
		text{"Your user is not allowed to talk to the agent.",
			"Run `sudo usermod -aG localai-operators $USER`, then log out and back in."},
		text{"Su usuario no tiene permiso para hablar con el agente.",
			"Ejecute `sudo usermod -aG localai-operators $USER` y luego cierre la sesión y vuelva a entrar."}},
	CodeVersionSkew: {ExitOK, sevWarning,
		text{"aictl and the agent are different versions.",
			"This is usually temporary during an update. If it stays, run `sudo systemctl restart localai-agent`."},
		text{"aictl y el agente son de versiones distintas.",
			"Suele ser temporal durante una actualización. Si sigue así, ejecute `sudo systemctl restart localai-agent`."}},
	CodeTimeout: {ExitTimeout, sevError,
		text{"The command took too long.",
			"Check `aictl status`; the action may still finish in the background."},
		text{"El comando tardó demasiado.",
			"Revise `aictl status`; la acción puede terminar por su cuenta en segundo plano."}},
	CodeUsage: {ExitUsage, sevError,
		text{"Unknown command or option.", "Run `aictl help`."},
		text{"Comando u opción desconocidos.", "Ejecute `aictl help`."}},
	CodeNeedRoot: {ExitDenied, sevError,
		text{"This command must be run as root.", "Run it with sudo: `sudo aictl ...`."},
		text{"Este comando debe ejecutarse como root.", "Ejecútelo con sudo: `sudo aictl ...`."}},
	CodeBadResponse: {ExitFailed, sevError,
		text{"The agent sent an answer aictl does not understand.",
			"Update with `sudo aictl update`, or report the request id."},
		text{"El agente envió una respuesta que aictl no entiende.",
			"Actualice con `sudo aictl update`, o informe el identificador de la solicitud."}},
	CodeInterrupted: {ExitInterrupted, sevInfo,
		text{"You stopped waiting; the action continues in the background.",
			"Check with `aictl status`."},
		text{"Dejó de esperar; la acción continúa en segundo plano.",
			"Revise con `aictl status`."}},
}

func (ci codeInfo) text(l Lang) text {
	if l == LangES {
		return ci.Es
	}
	return ci.En
}
