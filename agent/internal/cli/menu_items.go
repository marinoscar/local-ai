package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// menuTree is what the menu offers, ordered by how often a person needs it. Labels say what the
// person wants to do, not the command name; the command is shown when it runs. Every args value is
// an existing aictl command (menu_test.go checks each one against the registry).
func menuTree() []menuItem {
	return []menuItem{
		{en: "See the state of this computer", es: "Ver el estado de esta computadora", args: []string{"status"}},
		{en: "Find and fix problems (health check)", es: "Encontrar y arreglar problemas (revisión)", args: []string{"doctor"}},
		{en: "Use the AI: key and addresses", es: "Usar la IA: clave y direcciones", sub: []menuItem{
			{en: "Show the key and the addresses to use", es: "Mostrar la clave y las direcciones", args: []string{"key", "show"}},
			{en: "Copy the key (without showing it)", es: "Copiar la clave (sin mostrarla)", args: []string{"key", "copy"}},
			{en: "Examples ready to paste", es: "Ejemplos listos para pegar", args: []string{"key", "snippets"}},
			{en: "Code to sign in to the local web page", es: "Código para entrar a la página web local", args: []string{"ui-code"}},
			{en: "Make a new key (the old one stops working)", es: "Crear una clave nueva (la anterior deja de servir)", args: []string{"key", "rotate"},
				hintEN: "(admin PIN)", hintES: "(PIN de admin.)"},
		}},
		{en: "AI models: load, change, download", es: "Modelos de IA: cargar, cambiar, descargar", sub: []menuItem{
			{en: "List the models", es: "Ver la lista de modelos", args: []string{"models"}},
			{en: "Load a model", es: "Cargar un modelo", args: []string{"load"}, pick: modelPicker(modelLoadable,
				"Which model do you want to load?", "¿Qué modelo quiere cargar?",
				"No model can be loaded now. Download one first (Download a model).",
				"Ahora no se puede cargar ningún modelo. Primero descargue uno (Descargar un modelo).")},
			{en: "Unload a model (free memory)", es: "Descargar de memoria un modelo", args: []string{"unload"}, pick: modelPicker(modelLoaded,
				"Which model do you want to unload?", "¿Qué modelo quiere quitar de la memoria?",
				"No model is loaded.", "No hay ningún modelo cargado.")},
			{en: "Choose the model that starts with the computer", es: "Elegir el modelo que arranca con la computadora", args: []string{"default"}, pick: modelPicker(modelPresent,
				"Which model should start with this computer?", "¿Qué modelo debe arrancar con esta computadora?",
				"No model is stored on this computer yet.", "Todavía no hay ningún modelo guardado en esta computadora.")},
			{en: "Download a model (needs internet)", es: "Bajar un modelo (necesita internet)", args: []string{"download"}, pick: modelPicker(modelMissing,
				"Which model do you want to download?", "¿Qué modelo quiere bajar?",
				"Every model of this computer's profile is already here.", "Todos los modelos del perfil de esta computadora ya están aquí.")},
			{en: "Delete a model from this computer", es: "Borrar un modelo de esta computadora", args: []string{"delete"}, pick: modelPicker(modelPresent,
				"Which model do you want to delete?", "¿Qué modelo quiere borrar?",
				"No model is stored on this computer.", "No hay ningún modelo guardado en esta computadora."),
				hintEN: "(admin PIN)", hintES: "(PIN de admin.)"},
		}},
		{en: "Mode: online, offline or survival", es: "Modo: en línea, sin conexión o supervivencia", sub: []menuItem{
			{en: "Show the current mode", es: "Mostrar el modo actual", args: []string{"mode"}},
			{en: "Online: normal, part of the lab", es: "En línea: normal, parte del laboratorio", args: []string{"mode", "online"}},
			{en: "Offline: private, nothing leaves this computer", es: "Sin conexión: privado, nada sale de esta computadora", args: []string{"mode", "offline"}},
			{en: "Survival: emergency, everything on this computer", es: "Supervivencia: emergencia, todo en esta computadora", args: []string{"mode", "survival"}},
		}},
		{en: "Offline content and the AI-ARK drive", es: "Contenido sin conexión y la unidad AI-ARK", sub: []menuItem{
			{en: "How complete is the offline content?", es: "¿Qué tan completo está el contenido sin conexión?", args: []string{"ark", "status"}},
			{en: "Download and update the content (needs internet)", es: "Bajar y actualizar el contenido (necesita internet)", args: []string{"ark", "sync", "--wait"}},
			{en: "Copy content from the AI-ARK drive (no internet)", es: "Copiar contenido desde la unidad AI-ARK (sin internet)", args: []string{"ark", "import", "--wait"}},
			{en: "Check that the content is not damaged", es: "Comprobar que el contenido no está dañado", args: []string{"ark", "verify", "--wait"}},
			{en: "List the connected drives", es: "Ver las unidades conectadas", args: []string{"ark", "drives"}},
			{en: "Safely remove a drive", es: "Quitar una unidad de forma segura", args: []string{"ark", "eject"}, pick: drivePicker()},
		}},
		{en: "Repair with no internet (rebuild kit)", es: "Reparar sin internet (kit de reconstrucción)", sub: []menuItem{
			{en: "Is the rebuild kit complete?", es: "¿Está completo el kit de reconstrucción?", args: []string{"kit", "status"}},
			{en: "Check the rebuild kit (slow)", es: "Comprobar el kit de reconstrucción (lento)", args: []string{"kit", "verify"}},
			{en: "Rebuild the AI engine (llama.cpp)", es: "Reconstruir el motor de IA (llama.cpp)", args: []string{"rebuild-llama"}, hintEN: "(needs sudo)", hintES: "(necesita sudo)"},
			{en: "Rebuild speech-to-text (whisper.cpp)", es: "Reconstruir voz a texto (whisper.cpp)", args: []string{"rebuild-whisper"}, hintEN: "(needs sudo)", hintES: "(necesita sudo)"},
			{en: "Rebuild the agent", es: "Reconstruir el agente", args: []string{"rebuild-agent"}, hintEN: "(needs sudo)", hintES: "(necesita sudo)"},
		}},
		{en: "Services: AI engine, library, speech", es: "Servicios: motor de IA, biblioteca, voz", sub: []menuItem{
			{en: "Show the services", es: "Ver los servicios", args: []string{"services"}},
			{en: "Restart a service", es: "Reiniciar un servicio", args: []string{"services", "restart"}, pick: servicePicker()},
			{en: "Start a service", es: "Iniciar un servicio", args: []string{"services", "start"}, pick: servicePicker()},
			{en: "Stop a service", es: "Detener un servicio", args: []string{"services", "stop"}, pick: servicePicker()},
			{en: "Show the agent's log", es: "Ver el registro del agente", args: []string{"logs", "-n", "60"}},
		}},
		{en: "More: offline self-test, cloud link, profile", es: "Más: prueba sin conexión, nube, perfil", sub: []menuItem{
			{en: "Offline self-test, quick (a few minutes)", es: "Prueba sin conexión, rápida (unos minutos)", args: []string{"drill", "--quick", "--wait"}},
			{en: "Offline self-test, full (up to 30 minutes)", es: "Prueba sin conexión, completa (hasta 30 minutos)", args: []string{"drill", "--wait"}},
			{en: "Results of the last self-test", es: "Resultados de la última prueba", args: []string{"drill", "show"}},
			{en: "Link with the cloud", es: "Enlace con la nube", args: []string{"cloud", "status"}},
			{en: "Hardware profile of this computer", es: "Perfil de hardware de esta computadora", args: []string{"profile", "show"}},
			{en: "Which profile fits this computer?", es: "¿Qué perfil le corresponde a esta computadora?", args: []string{"profile", "suggest"}},
			{en: "Versions", es: "Versiones", args: []string{"version"}},
			{en: "All typed commands", es: "Todos los comandos escritos", args: []string{"help"}},
		}},
		{en: "Help in an emergency", es: "Ayuda en una emergencia", info: emergencyHelp},
	}
}

// emergencyHelp is the text screen of the last item: the three things to know with no internet.
func emergencyHelp(l Lang, sym out.Symbols) string {
	if l == LangES {
		return strings.Join([]string{
			"AYUDA EN UNA EMERGENCIA",
			"",
			sym.Arrow + " Sin internet, esta computadora sigue funcionando sola.",
			sym.Arrow + " Ponga el modo Supervivencia: menú principal, opción 5, luego 4.",
			"  No necesita PIN. Para salir de Supervivencia sí se necesita el PIN.",
			sym.Arrow + " Abra el Portal de supervivencia en el navegador:",
			"    http://127.0.0.1:7070",
			"  Ahí están el chat, la biblioteca, los mapas y la voz, todo sin internet.",
			sym.Arrow + " Si algo falla: menú principal, opción 2 (Encontrar y arreglar problemas).",
			"  Cada problema trae un código LAI-… y cómo arreglarlo.",
			sym.Arrow + " Guía impresa: \"01 Modo supervivencia\", en papel junto a la unidad AI-ARK.",
		}, "\n")
	}
	return strings.Join([]string{
		"HELP IN AN EMERGENCY",
		"",
		sym.Arrow + " With no internet, this computer keeps working on its own.",
		sym.Arrow + " Switch to Survival mode: main menu, item 5, then 4.",
		"  No PIN is needed. Leaving Survival mode needs the admin PIN.",
		sym.Arrow + " Open the Survival Portal in the web browser:",
		"    http://127.0.0.1:7070",
		"  Chat, the library, maps and speech are there, all with no internet.",
		sym.Arrow + " If something is wrong: main menu, item 2 (Find and fix problems).",
		"  Every problem comes with a LAI-… code and how to fix it.",
		sym.Arrow + " Printed guide: \"01 Survival mode\" (the paper copy kept with the AI-ARK drive).",
	}, "\n")
}

// modelFilter says which models a picker offers.
type modelFilter func(m wireModel) bool

func modelLoadable(m wireModel) bool { return m.Present && m.Loadable && !m.Loaded }
func modelLoaded(m wireModel) bool   { return m.Loaded }
func modelPresent(m wireModel) bool  { return m.Present }
func modelMissing(m wireModel) bool  { return !m.Present && m.InProfile }

// modelPicker lists the models of `aictl models --json` that pass keep.
func modelPicker(keep modelFilter, titleEN, titleES, emptyEN, emptyES string) *menuPicker {
	return &menuPicker{
		source:  []string{"models"},
		titleEN: titleEN, titleES: titleES, emptyEN: emptyEN, emptyES: emptyES,
		choices: func(raw []byte, l Lang) []menuChoice {
			var ms wireModels
			if json.Unmarshal(raw, &ms) != nil {
				return nil
			}
			var cs []menuChoice
			for _, m := range ms.Models {
				if !keep(m) {
					continue
				}
				label := m.ID
				if m.DisplayName != "" {
					label = fmt.Sprintf("%s (%s)", m.DisplayName, m.ID)
				}
				if m.SizeBytes > 0 {
					label += ", " + out.Size(m.SizeBytes)
				}
				if m.Default {
					label += pick(l, ", starts with the computer", ", arranca con la computadora")
				}
				cs = append(cs, menuChoice{label: label, value: m.ID})
			}
			return cs
		},
	}
}

// drivePicker lists the drives of `aictl ark drives --json` that have an id (a set-up AI-ARK drive).
func drivePicker() *menuPicker {
	return &menuPicker{
		source:  []string{"ark", "drives"},
		titleEN: "Which drive do you want to remove?", titleES: "¿Qué unidad quiere quitar?",
		emptyEN: "No AI-ARK drive is connected.", emptyES: "No hay ninguna unidad AI-ARK conectada.",
		choices: func(raw []byte, l Lang) []menuChoice {
			var res struct {
				Drives []wireDrive `json:"drives"`
			}
			if json.Unmarshal(raw, &res) != nil {
				return nil
			}
			var cs []menuChoice
			for _, d := range res.Drives {
				if d.id() == "" {
					continue
				}
				label := d.name()
				if d.Mount != "" {
					label += " (" + d.Mount + ")"
				}
				cs = append(cs, menuChoice{label: label, value: d.id()})
			}
			return cs
		},
	}
}

// servicePicker is the fixed list of services (aictl.md, services).
func servicePicker() *menuPicker {
	return &menuPicker{
		titleEN: "Which service?", titleES: "¿Qué servicio?",
		choices: func(_ []byte, l Lang) []menuChoice {
			return []menuChoice{
				{label: pick(l, "AI engine (llama)", "Motor de IA (llama)"), value: "llama"},
				{label: pick(l, "Library (kiwix)", "Biblioteca (kiwix)"), value: "kiwix"},
				{label: pick(l, "Speech-to-text (whisper)", "Voz a texto (whisper)"), value: "whisper"},
			}
		},
	}
}
