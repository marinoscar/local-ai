package api

// text is the one-sentence message and the one-sentence fix of a code in one language.
type text struct{ Message, Fix string }

// Languages the envelope is localized into. Codes and field names never change (agent.md 3.7).
const (
	langEN = "en"
	langES = "es"
)

// messages is the local copy of the doctor code table for the codes this package and its status
// map use (agent.md sections 8.1 and 8.4). AGENT-11 replaces it with internal/doctor's table; the
// lookup below is the only reader, so the swap is one function.
var messages = map[string]map[string]text{
	CodeSocketBind: {
		langEN: {"The agent socket /run/localai/agent.sock could not be created.", "Restart the agent: `sudo systemctl restart localai-agent`."},
		langES: {"No se pudo crear el socket del agente /run/localai/agent.sock.", "Reinicie el agente: `sudo systemctl restart localai-agent`."},
	},
	CodeTCPBind: {
		langEN: {"Port 7070 is used by another program, so the local UI is unavailable.", "Find it with `sudo ss -ltnp 'sport = :7070'`, stop it, then restart the agent."},
		langES: {"El puerto 7070 lo usa otro programa, por lo que la interfaz local no está disponible.", "Búsquelo con `sudo ss -ltnp 'sport = :7070'`, deténgalo y reinicie el agente."},
	},
	CodeInvalidRequest: {
		langEN: {"The request was not valid.", "Check the command or the value named in the details."},
		langES: {"La solicitud no es válida.", "Revise el comando o el valor indicado en los detalles."},
	},
	CodeNotFound: {
		langEN: {"The thing asked for does not exist (model, service, content, route).", "Check the name with `aictl models` or `aictl status`."},
		langES: {"Lo solicitado no existe (modelo, servicio, contenido o ruta).", "Revise el nombre con `aictl models` o `aictl status`."},
	},
	CodeBusy: {
		langEN: {"Another operation is already running.", "Wait for it to finish (`aictl status`), then try again."},
		langES: {"Ya hay otra operación en curso.", "Espere a que termine (`aictl status`) y vuelva a intentarlo."},
	},
	CodeNotInMode: {
		langEN: {"This action is not allowed in the current mode.", "Switch mode first (`aictl mode online`), or add the option named in the details."},
		langES: {"Esta acción no está permitida en el modo actual.", "Cambie primero de modo (`aictl mode online`) o añada la opción indicada en los detalles."},
	},
	CodeStarting: {
		langEN: {"The agent is starting or stopping.", "Wait a few seconds and try again."},
		langES: {"El agente se está iniciando o deteniendo.", "Espere unos segundos y vuelva a intentarlo."},
	},
	CodeTooLarge: {
		langEN: {"The request or upload is too large.", "Use a smaller file."},
		langES: {"La solicitud o la carga es demasiado grande.", "Use un archivo más pequeño."},
	},
	CodeInternal: {
		langEN: {"Internal agent error.", "Run `aictl logs --unit agent` and report the request id in the details."},
		langES: {"Error interno del agente.", "Ejecute `aictl logs --unit agent` e informe el identificador de la solicitud que aparece en los detalles."},
	},
	CodeUnauthenticated: {
		langEN: {"Not signed in: the agent token or session is missing or wrong.", "Use `aictl` as a member of the `localai-operators` group, or sign in with a code from `aictl ui-code`."},
		langES: {"No ha iniciado sesión: falta el token del agente o la sesión, o no es correcto.", "Use `aictl` como miembro del grupo `localai-operators`, o inicie sesión con un código de `aictl ui-code`."},
	},
	CodeAdminRequired: {
		langEN: {"This action needs the admin PIN.", "Enter the admin PIN when asked, or run the command with `sudo`."},
		langES: {"Esta acción requiere el PIN de administrador.", "Introduzca el PIN de administrador cuando se pida, o ejecute el comando con `sudo`."},
	},
	CodeWrongPIN: {
		langEN: {"Wrong PIN.", "Try again. After 5 wrong tries you must wait 5 minutes."},
		langES: {"PIN incorrecto.", "Inténtelo de nuevo. Tras 5 intentos fallidos debe esperar 5 minutos."},
	},
	CodeNoPIN: {
		langEN: {"No admin PIN has been set.", "Run `sudo aictl pin set`."},
		langES: {"No se ha establecido un PIN de administrador.", "Ejecute `sudo aictl pin set`."},
	},
	CodeBadLoginCode: {
		langEN: {"The login code is wrong, used, or expired.", "Get a new code with `aictl ui-code`."},
		langES: {"El código de acceso es incorrecto, ya se usó o caducó.", "Obtenga un código nuevo con `aictl ui-code`."},
	},
	CodeSessionExpired: {
		langEN: {"Your session expired.", "Sign in again."},
		langES: {"Su sesión caducó.", "Inicie sesión de nuevo."},
	},
	CodeRateLimited: {
		langEN: {"Too many wrong PIN or login-code attempts.", "Wait the time shown. Forgotten PIN: `sudo aictl pin set`."},
		langES: {"Demasiados intentos fallidos de PIN o de código de acceso.", "Espere el tiempo indicado. Si olvidó el PIN: `sudo aictl pin set`."},
	},
	CodeBadHost: {
		langEN: {"Request refused: wrong address (Host header).", "Open `http://127.0.0.1:7070` exactly."},
		langES: {"Solicitud rechazada: dirección incorrecta (cabecera Host).", "Abra exactamente `http://127.0.0.1:7070`."},
	},
	CodeBadOrigin: {
		langEN: {"Request refused: it came from another website.", "Open the page from `http://127.0.0.1:7070` or the ai-desktop app."},
		langES: {"Solicitud rechazada: procede de otro sitio web.", "Abra la página desde `http://127.0.0.1:7070` o desde la aplicación ai-desktop."},
	},
	CodePeerNotOperator: {
		langEN: {"Your user is not allowed to use the agent.", "Ask an admin to run `sudo usermod -aG localai-operators <user>`, then log out and in."},
		langES: {"Su usuario no tiene permiso para usar el agente.", "Pida a un administrador que ejecute `sudo usermod -aG localai-operators <usuario>`, y luego cierre sesión y vuelva a entrar."},
	},
	CodePortalSessionEnded: {
		langEN: {"The Portal session ended.", "Reload the page."},
		langES: {"La sesión del Portal terminó.", "Recargue la página."},
	},
	CodeRouterDown: {
		langEN: {"The AI model service is running but not answering.", "Run `aictl restart llama`."},
		langES: {"El servicio del modelo de IA está en marcha pero no responde.", "Ejecute `aictl restart llama`."},
	},
	CodeUpstreamTimeout: {
		langEN: {"A model took too long to start; the previous model was started again.", "Try again; models on the ark drive load slower. Copy it inside with `aictl ark import`."},
		langES: {"Un modelo tardó demasiado en iniciarse; se volvió a iniciar el modelo anterior.", "Inténtelo de nuevo; los modelos del disco ark cargan más despacio. Cópielo dentro con `aictl ark import`."},
	},
}

// unknownText is what a code outside the local table gets when its handler gave no message.
var unknownText = map[string]text{
	langEN: {"The request failed.", "Run `aictl doctor` to see what is wrong."},
	langES: {"La solicitud falló.", "Ejecute `aictl doctor` para ver qué falla."},
}

// lookup returns the message and fix of code in lang, falling back to English, then to the generic
// text for a code the table does not know.
func lookup(code, lang string) text {
	if byLang, ok := messages[code]; ok {
		if t, ok := byLang[lang]; ok {
			return t
		}
		return byLang[langEN]
	}
	if t, ok := unknownText[lang]; ok {
		return t
	}
	return unknownText[langEN]
}
