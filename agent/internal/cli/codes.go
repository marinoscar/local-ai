package cli

import (
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Doctor codes aictl itself raises (aictl.md section 8, doctor-codes.md section 2).
//
// The constants alias those of agent/internal/doctor/codes.go, the one file allowed to spell a code
// literal (scripts/check-doctor-codes.sh). The en/es strings below are this package's own table;
// internal/doctor holds text for every code (doctor.Text), so the table could become a lookup into
// it in a later change.
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
	// CodeNeedConfirm: the command needs a yes/no confirmation and cannot ask (no terminal, or --json)
	// and --yes was not given. Raised by Confirm and ConfirmTyped.
	CodeNeedConfirm = doctor.CLI007
	// CodeNeedPIN: the admin PIN is needed but there is no terminal to ask for it and --pin-stdin
	// was not given. Raised by Admin.
	CodeNeedPIN = doctor.CLI008
	// CodeBadResponse: the agent sent an answer aictl cannot decode.
	CodeBadResponse = doctor.CLI009
	// CodeInterrupted: the user pressed Ctrl-C while waiting.
	CodeInterrupted = doctor.CLI010
	// CodeInventoryMissing: inventory.local.yaml was not found; placeholders stay in the documents.
	CodeInventoryMissing = doctor.CLI013
	// CodeNoPDFPrinter: no Chromium is installed; docs render made HTML only (info).
	CodeNoPDFPrinter = doctor.CLI014
	// CodeCardTooLong: the survival card does not fit on one page.
	CodeCardTooLong = doctor.CLI015
	// CodeCardPrivate: the survival card would show a private value (an address, the tailnet or an e-mail).
	CodeCardPrivate = doctor.CLI016
)

// Codes of the local root commands (pin, token, catalog restore/use, backup-config; aictl.md
// sections 4.11 to 4.25). Other areas' codes appear here only because these commands raise them.
const (
	// CodeFileIO: a file could not be read or written (the message names the path and the reason).
	CodeFileIO = doctor.CLI012
	// CodeKitMissing: no ark kit with a catalog on the internal disk or a mounted drive.
	CodeKitMissing = doctor.KIT040
	// CodeNoDrive: `backup-config --to-ark` found no mounted AI-ARK drive.
	CodeNoDrive = doctor.ARK001
)

// Codes of the agent that the PIN flow reacts to (agent.md section 3.8.3). The agent supplies their
// text; the CLI only needs to recognise them. LAI-SEC-004 is api.CodeRateLimited.
const (
	// CodeWrongPIN: the PIN was wrong (details.remaining_attempts).
	CodeWrongPIN = doctor.SEC003
)

// Severities of a code, as in the doctor registry.
const (
	sevError   = out.SevError
	sevWarning = out.SevWarning
	sevInfo    = out.SevInfo
)

// Lang is the language of human output: "en" or "es". Codes and field names never change. The
// strings live in the i18n package.
type Lang = i18n.Lang

// Supported languages.
const (
	LangEN = i18n.EN
	LangES = i18n.ES
)

// text is the one-sentence message of a code and its one-sentence fix.
type text struct{ Message, Fix string }

type codeInfo struct {
	Exit     int
	Severity out.Severity
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
	CodeNeedConfirm: {ExitUsage, sevError,
		text{"This command needs a yes/no confirmation.",
			"Run it in a terminal, or add `--yes`."},
		text{"Este comando necesita una confirmación de sí o no.",
			"Ejecútelo en una terminal o agregue `--yes`."}},
	CodeNeedPIN: {ExitDenied, sevError,
		text{"The admin PIN is needed but there is no terminal to ask for it.",
			"Run it in a terminal, use `sudo`, or pass `--pin-stdin`."},
		text{"Se necesita el PIN de administrador pero no hay una terminal para pedirlo.",
			"Ejecútelo en una terminal, use `sudo` o pase `--pin-stdin`."}},
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
	CodeFileIO: {ExitFailed, sevError,
		text{"A file could not be read or written.",
			"Check the path and free space named in the message."},
		text{"No se pudo leer o escribir un archivo.",
			"Revise la ruta y el espacio libre que indica el mensaje."}},
	CodeInventoryMissing: {ExitOK, sevWarning,
		text{"inventory.local.yaml was not found; real addresses are shown as placeholders in the documents.",
			"Plug in the ark drive or pass `--inventory <file>`."},
		text{"No se encontró inventory.local.yaml; las direcciones reales aparecen como marcadores en los documentos.",
			"Conecte el disco del arca o use `--inventory <archivo>`."}},
	CodeNoPDFPrinter: {ExitOK, sevInfo,
		text{"No PDF printer (Chromium) is installed; only HTML was made.",
			"Install `chromium` to also get PDFs, or print the HTML pages from a browser."},
		text{"No hay impresora de PDF (Chromium) instalada; solo se hizo HTML.",
			"Instale `chromium` para obtener también PDF, o imprima las páginas HTML desde un navegador."}},
	CodeCardTooLong: {ExitFailed, sevError,
		text{"The survival card does not fit on one page.",
			"Shorten the text in `docs/runbooks/00-survival-card.md` (survival-portal.md section 4.12)."},
		text{"La tarjeta de supervivencia no cabe en una página.",
			"Acorte el texto de `docs/runbooks/00-survival-card.md` (survival-portal.md sección 4.12)."}},
	CodeCardPrivate: {ExitFailed, sevError,
		text{"The survival card would show a secret value (an address, the tailnet or an e-mail).",
			"Remove that placeholder from `docs/runbooks/00-survival-card.md` (survival-portal.md section 4.12)."},
		text{"La tarjeta de supervivencia mostraría un valor secreto (una dirección, la tailnet o un correo).",
			"Quite ese marcador de `docs/runbooks/00-survival-card.md` (survival-portal.md sección 4.12)."}},
	CodeKitMissing: {ExitNotFound, sevError,
		text{"The rebuild kit is missing parts for this computer.",
			"Run `aictl ark sync` with internet, or `aictl ark import` from a good drive."},
		text{"Al kit de reconstrucción le faltan partes para este equipo.",
			"Ejecute `aictl ark sync` con internet, o `aictl ark import` desde un disco bueno."}},
	CodeNoDrive: {ExitNotFound, sevError,
		text{"No AI-ARK drive is mounted.",
			"Plug in an AI-ARK drive, or run `aictl doctor`."},
		text{"No hay ningún disco AI-ARK montado.",
			"Conecte un disco AI-ARK, o ejecute `aictl doctor`."}},
	catalog.CodeRejected: {ExitFailed, sevError,
		text{"The new catalog failed validation; the previous one was kept.",
			"Nothing to do. Report it to the maintainer."},
		text{"El catálogo nuevo no pasó la validación; se conservó el anterior.",
			"No hay nada que hacer. Infórmelo al mantenedor."}},
}

func (ci codeInfo) text(l Lang) text {
	if l == LangES {
		return ci.Es
	}
	return ci.En
}
