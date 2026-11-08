package cli

// helpES holds the Spanish one-line summary of every command, by its path ("kit verify"). Help prints
// it with --lang es (or a Spanish $LANG); a command without an entry shows its English summary, and
// TestHelpEverySummaryHasASpanishText fails until it gets one, so a new command cannot ship half
// translated. Only the summaries, the headings and the global flags are translated: a command's long
// description and the text of its own flags stay in English (aictl.md section 3.1).
var helpES = map[string]string{
	"ark":                    "Muestra qué tan completo está el contenido sin conexión y qué discos del arca hay conectados.",
	"ark status":             "Muestra qué tan completo está el contenido en este equipo y en el disco del arca.",
	"ark drives":             "Lista los discos que parecen un disco del arca, con los problemas de los que no se pueden usar.",
	"ark drives set-profile": "Elige el perfil que sigue un disco del arca.",
	"ark sync":               "Descarga y actualiza el contenido sin conexión (el arca).",
	"ark init":               "Prepara un disco AI-ARK nuevo y vacío (no lo formatea).",
	"ark mount":              "Monta un disco AI-ARK que está conectado pero sin montar.",
	"ark eject":              "Deja de usar un disco del arca con seguridad para poder desconectarlo.",
	"ark verify":             "Comprueba que el contenido guardado coincide con sus sumas de verificación.",
	"ark import":             "Copia contenido de un disco del arca a este equipo (sin internet).",
	"ark prune":              "Borra el contenido que el perfil ya no quiere y las versiones anteriores.",
	"ark hf-token":           "Guarda el token de Hugging Face que usa la sincronización para los modelos que piden iniciar sesión.",
	"ark hf-token set":       "Guarda un token de Hugging Face (lo pide sin mostrarlo).",
	"ark hf-token status":    "Dice si hay un token de Hugging Face guardado (nunca lo muestra).",
	"ark budget":             "Muestra o cambia cuánto disco puede usar el contenido sin conexión.",
	"backup-config":          "Guarda la configuración de este nodo en un archivo, o la restaura desde uno.",
	"bench":                  "Mide la memoria y la velocidad de un modelo, para el catálogo.",
	"catalog":                "Comprueba y administra el catálogo de modelos y contenido del arca.",
	"catalog lint":           "Valida una carpeta de catálogo (models.yaml, ark.yaml, profiles/, CATALOG_VERSION).",
	"catalog restore":        "Vuelve a poner el último catálogo bueno (del kit del arca, o el anterior a uno de prueba).",
	"catalog use":            "Instala una carpeta de catálogo borrador en un nodo de prueba (necesita --test).",
	"catalog refresh":        "Obtiene el catálogo más reciente de modelos y contenido.",
	"cloud":                  "Muestra o cambia el enlace con la aplicación en la nube.",
	"cloud status":           "Muestra si este nodo está enlazado con la nube y cómo va el enlace.",
	"cloud enroll":           "Conecta este nodo con la nube usando su credencial de nodo.",
	"cloud unenroll":         "Desconecta este nodo de la nube.",
	"cloud resend-key":       "Vuelve a enviar la clave de API a la nube.",
	"default":                "Muestra o cambia el modelo predeterminado (el que arranca con este equipo).",
	"delete":                 "Borra un modelo de este equipo.",
	"docs":                   "Genera los manuales sin conexión y la tarjeta de supervivencia.",
	"docs inventory":         "Instala el inventario del laboratorio con el que se llenan los manuales.",
	"docs inventory import":  "Comprueba un archivo de inventario del laboratorio y lo instala en este equipo.",
	"docs render":            "Convierte los manuales y las especificaciones a HTML (y PDF) y genera la tarjeta de supervivencia.",
	"doctor":                 "Ejecuta las comprobaciones de salud y explica cada problema con palabras sencillas.",
	"download":               "Descarga un modelo de internet (modo en línea).",
	"drill":                  "Ejecuta la prueba sin conexión (el simulacro), muestra sus resultados o registra la manual.",
	"drill show":             "Muestra los resultados del último simulacro (o de una ejecución).",
	"drill record-manual":    "Registra el simulacro trimestral de \"desenchufar\" que hizo a mano.",
	"key":                    "Muestra, copia o cambia la clave de API de este nodo.",
	"key show":               "Imprime la clave de API y las direcciones para usarla.",
	"key copy":               "Pone la clave de API en el portapapeles sin imprimirla.",
	"key rotate":             "Crea una clave de API nueva (la anterior deja de funcionar).",
	"key snippets":           "Imprime ejemplos listos para pegar que usan la clave.",
	"kit":                    "Trabaja con el kit de reconstrucción (paquetes y herramientas sin conexión).",
	"kit export-build":       "Guarda en el kit las compilaciones de los programas de IA de este equipo.",
	"kit install-debs":       "Instala paquetes del repositorio apt sin conexión del kit.",
	"kit install":            "Instala kiwix-tools, pmtiles, piper o el agente desde el kit.",
	"kit status":             "Muestra qué partes del kit de reconstrucción están presentes y verificadas.",
	"kit verify":             "Comprueba que el kit de reconstrucción está completo, firmado y sin daños.",
	"load":                   "Carga un modelo para que pueda responder (se descargan otros para hacer espacio).",
	"logs":                   "Muestra el registro del agente o de un servicio.",
	"mode":                   "Muestra el modo de este nodo, o lo cambia (en línea, sin conexión, supervivencia).",
	"models":                 "Lista los modelos: guardados, cargados y los que caben en este equipo.",
	"models show":            "Muestra un modelo en detalle.",
	"pin":                    "Administra el PIN de administrador de este nodo.",
	"pin set":                "Cambia el PIN de administrador (de 4 a 8 dígitos).",
	"profile":                "Muestra el perfil de hardware de este equipo.",
	"profile show":           "Muestra el perfil en uso, sus ajustes y el tamaño de contenido previsto.",
	"profile suggest":        "Dice qué perfil le conviene a este equipo.",
	"profile set":            "Elige el perfil de este nodo.",
	"rebuild-agent":          "Reconstruye el programa del agente desde el código fuente del kit, sin internet.",
	"rebuild-llama":          "Reconstruye el motor de IA (llama.cpp) desde el código fijado, incluso sin internet.",
	"rebuild-whisper":        "Reconstruye el programa de voz a texto (whisper.cpp) desde el código fijado.",
	"restart":                "Reinicia un servicio (llama, kiwix o whisper).",
	"services":               "Lista los servicios (llama, kiwix, whisper), o inicia, detiene o reinicia uno.",
	"services start":         "Inicia un servicio (llama, kiwix o whisper).",
	"services stop":          "Detiene un servicio (llama, kiwix o whisper).",
	"services restart":       "Reinicia un servicio (llama, kiwix o whisper).",
	"status":                 "Muestra el estado de este nodo: modo, modelo, servicios, arca, nube.",
	"token":                  "Administra el token del agente.",
	"token rotate":           "Crea un token nuevo del agente.",
	"ui-code":                "Imprime un código de un solo uso para entrar a la página web local.",
	"unload":                 "Descarga un modelo para liberar memoria gráfica.",
	"update":                 "Instala una versión más nueva del agente (necesita internet; si la nueva falla, se vuelve a poner la anterior).",
	"verify-release":         "Comprueba una carpeta de versión (firma y sumas de verificación) sin internet.",
	"version":                "Muestra las versiones de aictl y del agente.",
}

// helpWords are the fixed words of the help screens, in English and Spanish.
type helpWords struct {
	intro, usage, commands, run, marksLegend, globalFlags, marks, timeout, flags, subcommands, usageHint string
}

var helpEN = helpWords{
	intro:    "aictl: command line for the Local AI node agent.",
	usage:    "Usage:",
	commands: "Commands:",
	run:      "Run `aictl help <command>` for the details of one command.",
	marksLegend: "Marks: [local] works without the agent; [root] needs sudo; [admin] needs the admin PIN (root needs none).\n" +
		"The time after the marks is the default deadline of the command (--timeout changes it).",
	globalFlags: globalFlagsHelp,
	marks:       "Marks:",
	timeout:     "Default timeout: %s (change it with --timeout)",
	flags:       "Flags:",
	subcommands: "Subcommands:",
	usageHint:   "Usage: aictl [global flags] %s [flags] [args]",
}

var helpESWords = helpWords{
	intro:    "aictl: línea de comandos del agente de nodo de Local AI.",
	usage:    "Uso:",
	commands: "Comandos:",
	run:      "Ejecute `aictl help <comando>` para ver los detalles de un comando.",
	marksLegend: "Marcas: [local] funciona sin el agente; [root] necesita sudo; [admin] necesita el PIN de administrador (root no lo necesita).\n" +
		"El tiempo después de las marcas es el plazo predeterminado del comando (--timeout lo cambia).",
	globalFlags: globalFlagsHelpES,
	marks:       "Marcas:",
	timeout:     "Plazo predeterminado: %s (cámbielo con --timeout)",
	flags:       "Opciones:",
	subcommands: "Subcomandos:",
	usageHint:   "Uso: aictl [opciones globales] %s [opciones] [argumentos]",
}

func helpFor(l Lang) helpWords {
	if l == LangES {
		return helpESWords
	}
	return helpEN
}

// globalFlagsHelpES is globalFlagsHelp in Spanish. A test keeps both in step with parseGlobal.
const globalFlagsHelpES = `Opciones globales (antes del nombre del comando):
  --json              Salida para máquinas en stdout: un documento JSON, sin color ni preguntas
  --lang en|es        Idioma de la salida para personas (por defecto: según $LANG, si no en)
  --no-color          Sin color (también: NO_COLOR definido, TERM=dumb, o stdout no es una terminal)
  -y, --yes           Responder "sí" a las confirmaciones
  -q, --quiet         Solo errores y la línea de resultado esencial
  -v, --verbose       Más detalle: ids de petición, detalles, el transporte usado
  --socket <ruta>     Socket unix (por defecto /run/localai/agent.sock, o $LOCALAI_SOCKET)
  --url <url>         URL base del respaldo TCP, debe ser este equipo
                      (por defecto http://127.0.0.1:7070, o $LOCALAI_URL)
  --token-file <ruta> Token del respaldo TCP
                      (por defecto /etc/localai/agent-token, o $LOCALAI_TOKEN_FILE)
  --timeout <dur>     Plazo total, por ejemplo 90s o 10m (0: sin plazo)
  --pin-stdin         Leer el PIN de administrador de stdin (una línea) en vez de preguntarlo
  -h, --help          Ayuda del comando
`
