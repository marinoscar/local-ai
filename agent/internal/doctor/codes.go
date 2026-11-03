package doctor

// The code table: the machine copy of the registry docs/specs/doctor-codes.md section 2, every code
// of every area, with its default severity and its message and fix in English and Spanish
// (agent.md section 4.13, doctor-codes.md section 1.5).
//
// Rules that scripts/check-doctor-codes.sh enforces on this file:
//   - every registry code has a constant below, and every constant is in the registry;
//   - this file is the only non-test Go file with a quoted "LAI-..." literal: other packages use the
//     constants (config.CodeNodeYAMLMissing = doctor.AGT001);
//   - every table entry has a non-empty Spanish title and fix, one per line as `Title:` and `Fix:`
//     with ES: last, and a Severity equal to the first word of the registry severity.
//
// The English title is the registry meaning; the English fix is the registry fix. Titles are one
// sentence of at most 120 characters (codes_test.go). The Spanish text exists only here and is
// reviewed in the pull request that adds or changes a code. A code is never renumbered or reused;
// a retired code stays with severity Retired.
//
// Keep the constants and the table in registry order.

// The codes. The name is the area and the number; the value is the string printed everywhere.
const (
	// LAI-AGT
	AGT001 = "LAI-AGT-001"
	AGT002 = "LAI-AGT-002"
	AGT003 = "LAI-AGT-003"
	AGT004 = "LAI-AGT-004"
	AGT005 = "LAI-AGT-005"
	AGT006 = "LAI-AGT-006"
	AGT007 = "LAI-AGT-007"
	AGT008 = "LAI-AGT-008"
	AGT009 = "LAI-AGT-009"
	AGT010 = "LAI-AGT-010"
	AGT011 = "LAI-AGT-011"
	AGT012 = "LAI-AGT-012"
	AGT013 = "LAI-AGT-013"
	AGT014 = "LAI-AGT-014"
	AGT015 = "LAI-AGT-015"
	AGT016 = "LAI-AGT-016"
	AGT017 = "LAI-AGT-017"
	AGT020 = "LAI-AGT-020"
	AGT021 = "LAI-AGT-021"
	AGT022 = "LAI-AGT-022"
	AGT023 = "LAI-AGT-023"
	AGT024 = "LAI-AGT-024"
	AGT025 = "LAI-AGT-025"
	AGT099 = "LAI-AGT-099"

	// LAI-CLI
	CLI001 = "LAI-CLI-001"
	CLI002 = "LAI-CLI-002"
	CLI003 = "LAI-CLI-003"
	CLI004 = "LAI-CLI-004"
	CLI005 = "LAI-CLI-005"
	CLI006 = "LAI-CLI-006"
	CLI007 = "LAI-CLI-007"
	CLI008 = "LAI-CLI-008"
	CLI009 = "LAI-CLI-009"
	CLI010 = "LAI-CLI-010"
	CLI011 = "LAI-CLI-011"
	CLI012 = "LAI-CLI-012"
	CLI013 = "LAI-CLI-013"
	CLI014 = "LAI-CLI-014"
	CLI015 = "LAI-CLI-015"
	CLI016 = "LAI-CLI-016"

	// LAI-LLM
	LLM001 = "LAI-LLM-001"
	LLM002 = "LAI-LLM-002"
	LLM003 = "LAI-LLM-003"
	LLM004 = "LAI-LLM-004"
	LLM005 = "LAI-LLM-005"
	LLM006 = "LAI-LLM-006"
	LLM007 = "LAI-LLM-007"
	LLM008 = "LAI-LLM-008"
	LLM009 = "LAI-LLM-009"
	LLM010 = "LAI-LLM-010"
	LLM011 = "LAI-LLM-011"
	LLM012 = "LAI-LLM-012"
	LLM013 = "LAI-LLM-013"
	LLM014 = "LAI-LLM-014"
	LLM015 = "LAI-LLM-015"
	LLM016 = "LAI-LLM-016"
	LLM017 = "LAI-LLM-017"
	LLM018 = "LAI-LLM-018"
	LLM019 = "LAI-LLM-019"
	LLM020 = "LAI-LLM-020"
	LLM021 = "LAI-LLM-021"
	LLM022 = "LAI-LLM-022"
	LLM023 = "LAI-LLM-023"
	LLM024 = "LAI-LLM-024"
	LLM025 = "LAI-LLM-025"
	LLM026 = "LAI-LLM-026"
	LLM027 = "LAI-LLM-027"
	LLM028 = "LAI-LLM-028"
	LLM029 = "LAI-LLM-029"
	LLM030 = "LAI-LLM-030"
	LLM031 = "LAI-LLM-031"

	// LAI-CAT
	CAT001 = "LAI-CAT-001"
	CAT002 = "LAI-CAT-002"
	CAT003 = "LAI-CAT-003"
	CAT004 = "LAI-CAT-004"
	CAT005 = "LAI-CAT-005"
	CAT010 = "LAI-CAT-010"
	CAT011 = "LAI-CAT-011"
	CAT012 = "LAI-CAT-012"
	CAT013 = "LAI-CAT-013"
	CAT014 = "LAI-CAT-014"
	CAT015 = "LAI-CAT-015"
	CAT016 = "LAI-CAT-016"
	CAT017 = "LAI-CAT-017"
	CAT018 = "LAI-CAT-018"
	CAT019 = "LAI-CAT-019"

	// LAI-NET
	NET001 = "LAI-NET-001"
	NET002 = "LAI-NET-002"
	NET003 = "LAI-NET-003"
	NET004 = "LAI-NET-004"
	NET005 = "LAI-NET-005"
	NET006 = "LAI-NET-006"
	NET007 = "LAI-NET-007"
	NET008 = "LAI-NET-008"
	NET009 = "LAI-NET-009"
	NET010 = "LAI-NET-010"
	NET011 = "LAI-NET-011"
	NET012 = "LAI-NET-012"
	NET013 = "LAI-NET-013"
	NET014 = "LAI-NET-014"

	// LAI-HW
	HW001 = "LAI-HW-001"
	HW002 = "LAI-HW-002"
	HW003 = "LAI-HW-003"
	HW004 = "LAI-HW-004"
	HW005 = "LAI-HW-005"
	HW006 = "LAI-HW-006"
	HW007 = "LAI-HW-007"
	HW008 = "LAI-HW-008"
	HW009 = "LAI-HW-009"
	HW010 = "LAI-HW-010"
	HW011 = "LAI-HW-011"
	HW012 = "LAI-HW-012"
	HW013 = "LAI-HW-013"
	HW014 = "LAI-HW-014"
	HW015 = "LAI-HW-015"
	HW016 = "LAI-HW-016"
	HW017 = "LAI-HW-017"
	HW018 = "LAI-HW-018"
	HW019 = "LAI-HW-019"
	HW020 = "LAI-HW-020"

	// LAI-SEC
	SEC001 = "LAI-SEC-001"
	SEC002 = "LAI-SEC-002"
	SEC003 = "LAI-SEC-003"
	SEC004 = "LAI-SEC-004"
	SEC005 = "LAI-SEC-005"
	SEC006 = "LAI-SEC-006"
	SEC007 = "LAI-SEC-007"
	SEC008 = "LAI-SEC-008"
	SEC009 = "LAI-SEC-009"
	SEC010 = "LAI-SEC-010"
	SEC011 = "LAI-SEC-011"
	SEC012 = "LAI-SEC-012"
	SEC013 = "LAI-SEC-013"
	SEC014 = "LAI-SEC-014"
	SEC015 = "LAI-SEC-015"

	// LAI-CLD
	CLD004 = "LAI-CLD-004"
	CLD008 = "LAI-CLD-008"
	CLD012 = "LAI-CLD-012"
	CLD013 = "LAI-CLD-013"
	CLD015 = "LAI-CLD-015"
	CLD020 = "LAI-CLD-020"
	CLD021 = "LAI-CLD-021"
	CLD022 = "LAI-CLD-022"
	CLD023 = "LAI-CLD-023"
	CLD024 = "LAI-CLD-024"
	CLD025 = "LAI-CLD-025"
	CLD026 = "LAI-CLD-026"
	CLD027 = "LAI-CLD-027"
	CLD028 = "LAI-CLD-028"
	CLD029 = "LAI-CLD-029"
	CLD030 = "LAI-CLD-030"
	CLD031 = "LAI-CLD-031"
	CLD032 = "LAI-CLD-032"
	CLD033 = "LAI-CLD-033"
	CLD034 = "LAI-CLD-034"
	CLD035 = "LAI-CLD-035"
	CLD050 = "LAI-CLD-050"
	CLD051 = "LAI-CLD-051"
	CLD052 = "LAI-CLD-052"
	CLD053 = "LAI-CLD-053"
	CLD054 = "LAI-CLD-054"
	CLD055 = "LAI-CLD-055"
	CLD056 = "LAI-CLD-056"
	CLD057 = "LAI-CLD-057"
	CLD058 = "LAI-CLD-058"
	CLD059 = "LAI-CLD-059"
	CLD060 = "LAI-CLD-060"
	CLD061 = "LAI-CLD-061"
	CLD062 = "LAI-CLD-062"
	CLD063 = "LAI-CLD-063"

	// LAI-SYN
	SYN001 = "LAI-SYN-001"
	SYN002 = "LAI-SYN-002"
	SYN003 = "LAI-SYN-003"
	SYN004 = "LAI-SYN-004"
	SYN005 = "LAI-SYN-005"
	SYN010 = "LAI-SYN-010"
	SYN011 = "LAI-SYN-011"
	SYN020 = "LAI-SYN-020"
	SYN021 = "LAI-SYN-021"
	SYN022 = "LAI-SYN-022"
	SYN023 = "LAI-SYN-023"
	SYN024 = "LAI-SYN-024"
	SYN025 = "LAI-SYN-025"
	SYN026 = "LAI-SYN-026"
	SYN027 = "LAI-SYN-027"
	SYN028 = "LAI-SYN-028"
	SYN030 = "LAI-SYN-030"
	SYN031 = "LAI-SYN-031"
	SYN032 = "LAI-SYN-032"
	SYN033 = "LAI-SYN-033"
	SYN034 = "LAI-SYN-034"
	SYN035 = "LAI-SYN-035"
	SYN040 = "LAI-SYN-040"
	SYN041 = "LAI-SYN-041"
	SYN042 = "LAI-SYN-042"
	SYN043 = "LAI-SYN-043"
	SYN044 = "LAI-SYN-044"
	SYN050 = "LAI-SYN-050"
	SYN051 = "LAI-SYN-051"
	SYN052 = "LAI-SYN-052"
	SYN060 = "LAI-SYN-060"
	SYN061 = "LAI-SYN-061"
	SYN062 = "LAI-SYN-062"
	SYN070 = "LAI-SYN-070"

	// LAI-ARK
	ARK001 = "LAI-ARK-001"
	ARK002 = "LAI-ARK-002"
	ARK003 = "LAI-ARK-003"
	ARK004 = "LAI-ARK-004"
	ARK005 = "LAI-ARK-005"
	ARK006 = "LAI-ARK-006"
	ARK007 = "LAI-ARK-007"
	ARK008 = "LAI-ARK-008"
	ARK009 = "LAI-ARK-009"
	ARK010 = "LAI-ARK-010"
	ARK011 = "LAI-ARK-011"
	ARK012 = "LAI-ARK-012"
	ARK014 = "LAI-ARK-014"
	ARK015 = "LAI-ARK-015"
	ARK020 = "LAI-ARK-020"
	ARK021 = "LAI-ARK-021"

	// LAI-SVC
	SVC001 = "LAI-SVC-001"
	SVC002 = "LAI-SVC-002"
	SVC003 = "LAI-SVC-003"
	SVC004 = "LAI-SVC-004"
	SVC005 = "LAI-SVC-005"
	SVC010 = "LAI-SVC-010"
	SVC011 = "LAI-SVC-011"
	SVC012 = "LAI-SVC-012"
	SVC013 = "LAI-SVC-013"
	SVC014 = "LAI-SVC-014"
	SVC015 = "LAI-SVC-015"
	SVC016 = "LAI-SVC-016"
	SVC020 = "LAI-SVC-020"
	SVC021 = "LAI-SVC-021"
	SVC022 = "LAI-SVC-022"
	SVC023 = "LAI-SVC-023"
	SVC024 = "LAI-SVC-024"
	SVC030 = "LAI-SVC-030"
	SVC031 = "LAI-SVC-031"
	SVC032 = "LAI-SVC-032"
	SVC033 = "LAI-SVC-033"
	SVC034 = "LAI-SVC-034"
	SVC040 = "LAI-SVC-040"
	SVC041 = "LAI-SVC-041"
	SVC050 = "LAI-SVC-050"
	SVC051 = "LAI-SVC-051"
	SVC100 = "LAI-SVC-100"
	SVC101 = "LAI-SVC-101"
	SVC102 = "LAI-SVC-102"
	SVC103 = "LAI-SVC-103"
	SVC104 = "LAI-SVC-104"
	SVC105 = "LAI-SVC-105"
	SVC106 = "LAI-SVC-106"
	SVC107 = "LAI-SVC-107"
	SVC108 = "LAI-SVC-108"
	SVC109 = "LAI-SVC-109"
	SVC110 = "LAI-SVC-110"
	SVC111 = "LAI-SVC-111"
	SVC112 = "LAI-SVC-112"
	SVC113 = "LAI-SVC-113"
	SVC114 = "LAI-SVC-114"
	SVC115 = "LAI-SVC-115"

	// LAI-GW
	GW001 = "LAI-GW-001"
	GW002 = "LAI-GW-002"
	GW003 = "LAI-GW-003"
	GW004 = "LAI-GW-004"
	GW010 = "LAI-GW-010"
	GW011 = "LAI-GW-011"
	GW012 = "LAI-GW-012"
	GW013 = "LAI-GW-013"
	GW020 = "LAI-GW-020"
	GW021 = "LAI-GW-021"
	GW022 = "LAI-GW-022"
	GW023 = "LAI-GW-023"
	GW024 = "LAI-GW-024"
	GW030 = "LAI-GW-030"
	GW031 = "LAI-GW-031"
	GW032 = "LAI-GW-032"
	GW040 = "LAI-GW-040"
	GW041 = "LAI-GW-041"
	GW042 = "LAI-GW-042"
	GW043 = "LAI-GW-043"
	GW044 = "LAI-GW-044"
	GW045 = "LAI-GW-045"
	GW050 = "LAI-GW-050"
	GW051 = "LAI-GW-051"
	GW052 = "LAI-GW-052"
	GW060 = "LAI-GW-060"

	// LAI-DSK
	DSK001 = "LAI-DSK-001"
	DSK002 = "LAI-DSK-002"
	DSK003 = "LAI-DSK-003"
	DSK004 = "LAI-DSK-004"
	DSK005 = "LAI-DSK-005"
	DSK006 = "LAI-DSK-006"
	DSK007 = "LAI-DSK-007"
	DSK008 = "LAI-DSK-008"
	DSK009 = "LAI-DSK-009"
	DSK010 = "LAI-DSK-010"
	DSK011 = "LAI-DSK-011"
	DSK012 = "LAI-DSK-012"
	DSK013 = "LAI-DSK-013"
	DSK014 = "LAI-DSK-014"
	DSK015 = "LAI-DSK-015"
	DSK016 = "LAI-DSK-016"

	// LAI-INS
	INS001 = "LAI-INS-001"
	INS002 = "LAI-INS-002"
	INS003 = "LAI-INS-003"
	INS004 = "LAI-INS-004"
	INS010 = "LAI-INS-010"
	INS011 = "LAI-INS-011"
	INS012 = "LAI-INS-012"
	INS013 = "LAI-INS-013"
	INS014 = "LAI-INS-014"
	INS015 = "LAI-INS-015"
	INS016 = "LAI-INS-016"
	INS017 = "LAI-INS-017"
	INS020 = "LAI-INS-020"
	INS021 = "LAI-INS-021"
	INS030 = "LAI-INS-030"
	INS031 = "LAI-INS-031"
	INS032 = "LAI-INS-032"
	INS033 = "LAI-INS-033"
	INS034 = "LAI-INS-034"
	INS040 = "LAI-INS-040"
	INS041 = "LAI-INS-041"
	INS042 = "LAI-INS-042"
	INS043 = "LAI-INS-043"
	INS044 = "LAI-INS-044"
	INS050 = "LAI-INS-050"
	INS060 = "LAI-INS-060"
	INS070 = "LAI-INS-070"

	// LAI-KIT
	KIT001 = "LAI-KIT-001"
	KIT002 = "LAI-KIT-002"
	KIT010 = "LAI-KIT-010"
	KIT011 = "LAI-KIT-011"
	KIT012 = "LAI-KIT-012"
	KIT013 = "LAI-KIT-013"
	KIT014 = "LAI-KIT-014"
	KIT020 = "LAI-KIT-020"
	KIT021 = "LAI-KIT-021"
	KIT022 = "LAI-KIT-022"
	KIT030 = "LAI-KIT-030"
	KIT040 = "LAI-KIT-040"
	KIT041 = "LAI-KIT-041"
	KIT042 = "LAI-KIT-042"
	KIT043 = "LAI-KIT-043"
	KIT044 = "LAI-KIT-044"

	// LAI-DRL
	DRL001 = "LAI-DRL-001"
	DRL002 = "LAI-DRL-002"
	DRL003 = "LAI-DRL-003"
	DRL004 = "LAI-DRL-004"
	DRL005 = "LAI-DRL-005"
	DRL006 = "LAI-DRL-006"
	DRL010 = "LAI-DRL-010"
	DRL011 = "LAI-DRL-011"
	DRL012 = "LAI-DRL-012"
	DRL020 = "LAI-DRL-020"
	DRL021 = "LAI-DRL-021"
	DRL022 = "LAI-DRL-022"
	DRL023 = "LAI-DRL-023"
	DRL024 = "LAI-DRL-024"
	DRL030 = "LAI-DRL-030"
	DRL031 = "LAI-DRL-031"
	DRL032 = "LAI-DRL-032"
	DRL033 = "LAI-DRL-033"
	DRL034 = "LAI-DRL-034"
	DRL040 = "LAI-DRL-040"
	DRL050 = "LAI-DRL-050"
	DRL060 = "LAI-DRL-060"
	DRL070 = "LAI-DRL-070"
	DRL071 = "LAI-DRL-071"
	DRL080 = "LAI-DRL-080"
	DRL081 = "LAI-DRL-081"
	DRL090 = "LAI-DRL-090"
	DRL091 = "LAI-DRL-091"
)

// table is the code table, in registry order. init builds the lookup map from it.
var table = []Code{
	{
		ID:       AGT001,
		Severity: Error,
		Title:    Msg{EN: "`/etc/localai/node.yaml` is missing", ES: "Falta `/etc/localai/node.yaml`"},
		Fix:      Msg{EN: "Restore it from the backup: `sudo cp /etc/localai/node.yaml.bak /etc/localai/node.yaml`, or run the installer again", ES: "Restáurelo desde la copia: `sudo cp /etc/localai/node.yaml.bak /etc/localai/node.yaml`, o ejecute el instalador otra vez"},
	},
	{
		ID:       AGT002,
		Severity: Error,
		Title:    Msg{EN: "`node.yaml` cannot be read or has invalid values (warning for unknown keys)", ES: "`node.yaml` no se puede leer o tiene valores no válidos (aviso si hay claves desconocidas)"},
		Fix:      Msg{EN: "Fix the line named in the details, or restore `node.yaml.bak`. The last good settings stay in use until then", ES: "Corrija la línea indicada en los detalles, o restaure `node.yaml.bak`. Mientras tanto se usa la última configuración buena"},
	},
	{
		ID:       AGT003,
		Severity: Warning,
		Title:    Msg{EN: "The state file was damaged; the agent used the backup or started in offline mode", ES: "El archivo de estado estaba dañado; el agente usó la copia de respaldo o arrancó en modo sin conexión"},
		Fix:      Msg{EN: "Check the mode with `aictl status` and set it again with `aictl mode <mode>`", ES: "Compruebe el modo con `aictl status` y vuelva a fijarlo con `aictl mode <modo>`"},
	},
	{
		ID:       AGT004,
		Severity: Critical,
		Title:    Msg{EN: "The agent cannot write its state (`/var/lib/localai` full or read-only)", ES: "El agente no puede escribir su estado (`/var/lib/localai` está lleno o es de solo lectura)"},
		Fix:      Msg{EN: "Free disk space (`aictl ark status` shows what uses it) and restart the agent: `sudo systemctl restart localai-agent`", ES: "Libere espacio en disco (`aictl ark status` muestra qué lo usa) y reinicie el agente: `sudo systemctl restart localai-agent`"},
	},
	{
		ID:       AGT005,
		Severity: Error,
		Title:    Msg{EN: "The agent socket `/run/localai/agent.sock` could not be created", ES: "No se pudo crear el socket del agente `/run/localai/agent.sock`"},
		Fix:      Msg{EN: "Restart the agent: `sudo systemctl restart localai-agent`", ES: "Reinicie el agente: `sudo systemctl restart localai-agent`"},
	},
	{
		ID:       AGT006,
		Severity: Error,
		Title:    Msg{EN: "Port 7070 is used by another program, so the local UI is unavailable", ES: "Otro programa usa el puerto 7070, así que la interfaz local no está disponible"},
		Fix:      Msg{EN: "Find it with `sudo ss -ltnp 'sport = :7070'`, stop it, then restart the agent", ES: "Búsquelo con `sudo ss -ltnp 'sport = :7070'`, deténgalo y reinicie el agente"},
	},
	{
		ID:       AGT007,
		Severity: Info,
		Title:    Msg{EN: "A mode change was interrupted and has been resumed", ES: "Un cambio de modo se interrumpió y se ha reanudado"},
		Fix:      Msg{EN: "Nothing to do", ES: "No hay que hacer nada"},
	},
	{
		ID:       AGT008,
		Severity: Warning,
		Title:    Msg{EN: "One step of a mode change failed or was skipped", ES: "Un paso de un cambio de modo falló o se omitió"},
		Fix:      Msg{EN: "Open `aictl doctor` to see which part failed. The mode itself is active", ES: "Abra `aictl doctor` para ver qué parte falló. El modo en sí está activo"},
	},
	{
		ID:       AGT009,
		Severity: Error,
		Title:    Msg{EN: "An agent update failed and the previous version was restored", ES: "Falló una actualización del agente y se restauró la versión anterior"},
		Fix:      Msg{EN: "Nothing is broken. Report the version in the details to the maintainer", ES: "No hay nada roto. Informe al mantenedor de la versión que aparece en los detalles"},
	},
	{
		ID:       AGT010,
		Severity: Critical,
		Title:    Msg{EN: "A downloaded update failed its signature or checksum check and was discarded", ES: "Una actualización descargada no pasó la comprobación de firma o suma de verificación y se descartó"},
		Fix:      Msg{EN: "Do not install it by hand. Report it to the maintainer", ES: "No la instale a mano. Informe al mantenedor"},
	},
	{
		ID:       AGT011,
		Severity: Critical,
		Title:    Msg{EN: "The agent is not running as root", ES: "El agente no se está ejecutando como root"},
		Fix:      Msg{EN: "Start it only through systemd: `sudo systemctl start localai-agent`", ES: "Inícielo solo mediante systemd: `sudo systemctl start localai-agent`"},
	},
	{
		ID:       AGT012,
		Severity: Warning,
		Title:    Msg{EN: "The computer's clock looks wrong", ES: "El reloj del equipo parece incorrecto"},
		Fix:      Msg{EN: "Set the correct date and time in system settings. Offline features still work", ES: "Ponga la fecha y la hora correctas en la configuración del sistema. Las funciones sin conexión siguen funcionando"},
	},
	{
		ID:       AGT013,
		Severity: Warning,
		Title:    Msg{EN: "The audit log cannot be written", ES: "No se puede escribir el registro de auditoría"},
		Fix:      Msg{EN: "Free disk space in `/var/lib/localai`", ES: "Libere espacio en disco en `/var/lib/localai`"},
	},
	{
		ID:       AGT014,
		Severity: Warning,
		Title:    Msg{EN: "The agent restarted more than 3 times in the last hour", ES: "El agente se reinició más de 3 veces en la última hora"},
		Fix:      Msg{EN: "Run `aictl logs --unit agent` and report the last error", ES: "Ejecute `aictl logs --unit agent` e informe del último error"},
	},
	{
		ID:       AGT015,
		Severity: Info,
		Title:    Msg{EN: "An agent update is available (or: already up to date)", ES: "Hay una actualización del agente disponible (o: ya está al día)"},
		Fix:      Msg{EN: "Run `sudo aictl update` when convenient", ES: "Ejecute `sudo aictl update` cuando le convenga"},
	},
	{
		ID:       AGT016,
		Severity: Info,
		Title:    Msg{EN: "A settings change needs a restart to take effect", ES: "Un cambio de configuración necesita un reinicio para aplicarse"},
		Fix:      Msg{EN: "Run `sudo systemctl restart localai-agent` (or `aictl restart llama` when the details say so)", ES: "Ejecute `sudo systemctl restart localai-agent` (o `aictl restart llama` cuando los detalles lo indiquen)"},
	},
	{
		ID:       AGT017,
		Severity: Warning,
		Title:    Msg{EN: "A cloud command was interrupted because the agent restarted", ES: "Un comando de la nube se interrumpió porque el agente se reinició"},
		Fix:      Msg{EN: "Run the command again from ai.marin.cr if it is still needed", ES: "Vuelva a ejecutar el comando desde ai.marin.cr si todavía hace falta"},
	},
	{
		ID:       AGT020,
		Severity: Error,
		Title:    Msg{EN: "The request was not valid", ES: "La solicitud no era válida"},
		Fix:      Msg{EN: "Check the command or the value named in the details", ES: "Compruebe el comando o el valor indicado en los detalles"},
	},
	{
		ID:       AGT021,
		Severity: Error,
		Title:    Msg{EN: "The thing asked for does not exist (model, service, content, route)", ES: "Lo solicitado no existe (modelo, servicio, contenido, ruta)"},
		Fix:      Msg{EN: "Check the name with `aictl models` or `aictl status`", ES: "Compruebe el nombre con `aictl models` o `aictl status`"},
	},
	{
		ID:       AGT022,
		Severity: Warning,
		Title:    Msg{EN: "Another operation is already running", ES: "Ya hay otra operación en curso"},
		Fix:      Msg{EN: "Wait for it to finish (`aictl status`), then try again", ES: "Espere a que termine (`aictl status`) y vuelva a intentarlo"},
	},
	{
		ID:       AGT023,
		Severity: Warning,
		Title:    Msg{EN: "This action is not allowed in the current mode", ES: "Esta acción no está permitida en el modo actual"},
		Fix:      Msg{EN: "Switch mode first (`aictl mode online`), or add the option named in the details", ES: "Cambie primero de modo (`aictl mode online`), o añada la opción indicada en los detalles"},
	},
	{
		ID:       AGT024,
		Severity: Info,
		Title:    Msg{EN: "The agent is starting or stopping", ES: "El agente se está iniciando o deteniendo"},
		Fix:      Msg{EN: "Wait a few seconds and try again", ES: "Espere unos segundos y vuelva a intentarlo"},
	},
	{
		ID:       AGT025,
		Severity: Error,
		Title:    Msg{EN: "The request or upload is too large", ES: "La solicitud o la subida es demasiado grande"},
		Fix:      Msg{EN: "Use a smaller file", ES: "Use un archivo más pequeño"},
	},
	{
		ID:       AGT099,
		Severity: Error,
		Title:    Msg{EN: "Internal agent error", ES: "Error interno del agente"},
		Fix:      Msg{EN: "Run `aictl logs --unit agent` and report the request id in the details", ES: "Ejecute `aictl logs --unit agent` e informe del id de solicitud que aparece en los detalles"},
	},
	{
		ID:       CLI001,
		Severity: Error,
		Title:    Msg{EN: "`aictl` cannot reach the agent", ES: "`aictl` no puede comunicarse con el agente"},
		Fix:      Msg{EN: "Start it: `sudo systemctl start localai-agent`. If it keeps stopping: `journalctl -u localai-agent -n 50`", ES: "Inícielo: `sudo systemctl start localai-agent`. Si se sigue deteniendo: `journalctl -u localai-agent -n 50`"},
	},
	{
		ID:       CLI002,
		Severity: Error,
		Title:    Msg{EN: "Your user may not talk to the agent", ES: "Puede que su usuario no tenga permiso para hablar con el agente"},
		Fix:      Msg{EN: "Run `sudo usermod -aG localai-operators $USER`, then log out and back in", ES: "Ejecute `sudo usermod -aG localai-operators $USER` y luego cierre sesión y vuelva a entrar"},
	},
	{
		ID:       CLI003,
		Severity: Warning,
		Title:    Msg{EN: "`aictl` and the agent are different versions", ES: "`aictl` y el agente son de versiones distintas"},
		Fix:      Msg{EN: "Usually temporary during an update. If it stays, run `sudo systemctl restart localai-agent`", ES: "Suele ser temporal durante una actualización. Si continúa, ejecute `sudo systemctl restart localai-agent`"},
	},
	{
		ID:       CLI004,
		Severity: Error,
		Title:    Msg{EN: "The command took too long", ES: "El comando tardó demasiado"},
		Fix:      Msg{EN: "Check `aictl status`; the action may still finish in the background", ES: "Compruebe `aictl status`; la acción aún puede terminar en segundo plano"},
	},
	{
		ID:       CLI005,
		Severity: Error,
		Title:    Msg{EN: "Unknown command or option", ES: "Comando u opción desconocidos"},
		Fix:      Msg{EN: "Run `aictl help`", ES: "Ejecute `aictl help`"},
	},
	{
		ID:       CLI006,
		Severity: Error,
		Title:    Msg{EN: "This command must be run as root", ES: "Este comando debe ejecutarse como root"},
		Fix:      Msg{EN: "Run it with `sudo aictl …`", ES: "Ejecútelo con `sudo aictl …`"},
	},
	{
		ID:       CLI007,
		Severity: Error,
		Title:    Msg{EN: "This command needs a yes/no confirmation", ES: "Este comando necesita una confirmación de sí o no"},
		Fix:      Msg{EN: "Run it in a terminal, or add `--yes`", ES: "Ejecútelo en una terminal, o añada `--yes`"},
	},
	{
		ID:       CLI008,
		Severity: Error,
		Title:    Msg{EN: "The admin PIN is needed but there is no terminal to ask for it", ES: "Se necesita el PIN de administrador pero no hay terminal para pedirlo"},
		Fix:      Msg{EN: "Run it in a terminal, use `sudo`, or pass `--pin-stdin`", ES: "Ejecútelo en una terminal, use `sudo`, o pase `--pin-stdin`"},
	},
	{
		ID:       CLI009,
		Severity: Error,
		Title:    Msg{EN: "The agent sent an answer `aictl` does not understand", ES: "El agente envió una respuesta que `aictl` no entiende"},
		Fix:      Msg{EN: "Update with `sudo aictl update`, or report the request id", ES: "Actualice con `sudo aictl update`, o informe del id de solicitud"},
	},
	{
		ID:       CLI010,
		Severity: Info,
		Title:    Msg{EN: "You stopped waiting; the action continues in the background", ES: "Dejó de esperar; la acción continúa en segundo plano"},
		Fix:      Msg{EN: "Check with `aictl status`", ES: "Compruébelo con `aictl status`"},
	},
	{
		ID:       CLI011,
		Severity: Warning,
		Title:    Msg{EN: "No clipboard tool is available", ES: "No hay ninguna herramienta de portapapeles disponible"},
		Fix:      Msg{EN: "Use `aictl key show` and copy the key by hand", ES: "Use `aictl key show` y copie la clave a mano"},
	},
	{
		ID:       CLI012,
		Severity: Error,
		Title:    Msg{EN: "A file could not be read or written", ES: "No se pudo leer o escribir un archivo"},
		Fix:      Msg{EN: "Check the path and free space named in the message", ES: "Compruebe la ruta y el espacio libre indicados en el mensaje"},
	},
	{
		ID:       CLI013,
		Severity: Warning,
		Title:    Msg{EN: "`inventory.local.yaml` was not found; placeholders were left in the documents", ES: "No se encontró `inventory.local.yaml`; se dejaron marcadores de posición en los documentos"},
		Fix:      Msg{EN: "Plug in the ark drive or pass `--inventory <file>`", ES: "Conecte la unidad del arca o pase `--inventory <archivo>`"},
	},
	{
		ID:       CLI014,
		Severity: Info,
		Title:    Msg{EN: "No PDF printer (Chromium) is installed; only HTML was made", ES: "No hay impresora de PDF (Chromium) instalada; solo se generó HTML"},
		Fix:      Msg{EN: "Install `chromium` to also get PDFs, or print the HTML pages from a browser", ES: "Instale `chromium` para obtener también PDF, o imprima las páginas HTML desde un navegador"},
	},
	{
		ID:       CLI015,
		Severity: Error,
		Title:    Msg{EN: "The survival card does not fit on one page", ES: "La tarjeta de supervivencia no cabe en una página"},
		Fix:      Msg{EN: "Shorten the text in `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12)", ES: "Acorte el texto de `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12)"},
	},
	{
		ID:       CLI016,
		Severity: Error,
		Title:    Msg{EN: "The survival card would show a secret value (an address, the tailnet or an e-mail)", ES: "La tarjeta de supervivencia mostraría un valor secreto (una dirección, la tailnet o un correo electrónico)"},
		Fix:      Msg{EN: "Remove that placeholder from `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12)", ES: "Quite ese marcador de posición de `docs/runbooks/00-survival-card.md` (survival-portal.md §4.12)"},
	},
	{
		ID:       LLM001,
		Severity: Critical,
		Title:    Msg{EN: "The AI model service (`localai-llama`) is not running", ES: "El servicio de modelos de IA (`localai-llama`) no se está ejecutando"},
		Fix:      Msg{EN: "Run `aictl restart llama`. If it fails again, run `aictl logs --unit llama`", ES: "Ejecute `aictl restart llama`. Si vuelve a fallar, ejecute `aictl logs --unit llama`"},
	},
	{
		ID:       LLM002,
		Severity: Critical,
		Title:    Msg{EN: "The llama-server program is missing", ES: "Falta el programa llama-server"},
		Fix:      Msg{EN: "Run `sudo aictl rebuild-llama`", ES: "Ejecute `sudo aictl rebuild-llama`"},
	},
	{
		ID:       LLM003,
		Severity: Error,
		Title:    Msg{EN: "Another program is using port 8080", ES: "Otro programa usa el puerto 8080"},
		Fix:      Msg{EN: "Run `sudo ss -ltnp 'sport = :8080'` to see it; if it is `llama-qwen`, run `sudo aiagent migrate llama-qwen`", ES: "Ejecute `sudo ss -ltnp 'sport = :8080'` para verlo; si es `llama-qwen`, ejecute `sudo aiagent migrate llama-qwen`"},
	},
	{
		ID:       LLM004,
		Severity: Error,
		Title:    Msg{EN: "The AI model service is running but not answering", ES: "El servicio de modelos de IA se está ejecutando pero no responde"},
		Fix:      Msg{EN: "Run `aictl restart llama`", ES: "Ejecute `aictl restart llama`"},
	},
	{
		ID:       LLM005,
		Severity: Critical,
		Title:    Msg{EN: "The AI model service rejects this node's own API key", ES: "El servicio de modelos de IA rechaza la clave de API propia de este nodo"},
		Fix:      Msg{EN: "Run `aictl restart llama`; if it persists, `sudo aictl key rotate`", ES: "Ejecute `aictl restart llama`; si continúa, `sudo aictl key rotate`"},
	},
	{
		ID:       LLM006,
		Severity: Warning,
		Title:    Msg{EN: "Someone edited `/etc/localai/models.ini` by hand; it was regenerated (copy saved next to it)", ES: "Alguien editó `/etc/localai/models.ini` a mano; se regeneró (la copia se guardó a su lado)"},
		Fix:      Msg{EN: "Make changes in the catalog or `node.yaml` instead", ES: "Haga los cambios en el catálogo o en `node.yaml`"},
	},
	{
		ID:       LLM007,
		Severity: Error,
		Title:    Msg{EN: "The model list for the AI service could not be generated", ES: "No se pudo generar la lista de modelos para el servicio de IA"},
		Fix:      Msg{EN: "Run `aictl doctor` and report the details", ES: "Ejecute `aictl doctor` e informe de los detalles"},
	},
	{
		ID:       LLM008,
		Severity: Warning,
		Title:    Msg{EN: "Model files are missing or not verified", ES: "Faltan archivos de modelos o no están verificados"},
		Fix:      Msg{EN: "Run `aictl download <model>` (online) or `aictl ark import` (from the ark drive)", ES: "Ejecute `aictl download <modelo>` (con conexión) o `aictl ark import` (desde la unidad del arca)"},
	},
	{
		ID:       LLM009,
		Severity: Error,
		Title:    Msg{EN: "A model failed to start (or stopped unexpectedly); the previous model was started again", ES: "Un modelo no arrancó (o se detuvo de forma inesperada); se volvió a iniciar el modelo anterior"},
		Fix:      Msg{EN: "Choose a smaller model (`aictl models --loadable`) or check `aictl logs --unit llama`", ES: "Elija un modelo más pequeño (`aictl models --loadable`) o revise `aictl logs --unit llama`"},
	},
	{
		ID:       LLM010,
		Severity: Error,
		Title:    Msg{EN: "A model took too long to start; the previous model was started again", ES: "Un modelo tardó demasiado en arrancar; se volvió a iniciar el modelo anterior"},
		Fix:      Msg{EN: "Try again; models on the ark drive load slower. Copy it inside with `aictl ark import`", ES: "Inténtelo de nuevo; los modelos de la unidad del arca cargan más despacio. Cópielo al equipo con `aictl ark import`"},
	},
	{
		ID:       LLM011,
		Severity: Warning,
		Title:    Msg{EN: "Not enough room to load this model without unloading others", ES: "No hay espacio suficiente para cargar este modelo sin descargar otros"},
		Fix:      Msg{EN: "Unload a model first, or allow unloading others", ES: "Descargue primero un modelo, o permita descargar otros"},
	},
	{
		ID:       LLM012,
		Severity: Critical,
		Title:    Msg{EN: "No model could be started, not even the previous one", ES: "No se pudo iniciar ningún modelo, ni siquiera el anterior"},
		Fix:      Msg{EN: "Run `aictl restart llama`, then `aictl load <default model>`. See the troubleshooting runbook", ES: "Ejecute `aictl restart llama` y luego `aictl load <modelo predeterminado>`. Consulte el runbook de solución de problemas"},
	},
	{
		ID:       LLM013,
		Severity: Error,
		Title:    Msg{EN: "The default model did not start at boot", ES: "El modelo predeterminado no arrancó al encender"},
		Fix:      Msg{EN: "Run `aictl load <model>` or choose another default with `aictl default <model>`", ES: "Ejecute `aictl load <modelo>` o elija otro predeterminado con `aictl default <modelo>`"},
	},
	{
		ID:       LLM014,
		Severity: Warning,
		Title:    Msg{EN: "No default model is set or available", ES: "No hay un modelo predeterminado definido o disponible"},
		Fix:      Msg{EN: "Run `aictl default <model>`", ES: "Ejecute `aictl default <modelo>`"},
	},
	{
		ID:       LLM015,
		Severity: Warning,
		Title:    Msg{EN: "Two models need different settings for the same option; it was left out", ES: "Dos modelos necesitan valores distintos para la misma opción; se dejó fuera"},
		Fix:      Msg{EN: "Report it to the catalog maintainer, or set it in `node.yaml` `llama.extra_env`", ES: "Informe al mantenedor del catálogo, o defínala en `node.yaml` `llama.extra_env`"},
	},
	{
		ID:       LLM016,
		Severity: Warning,
		Title:    Msg{EN: "The old `llama-qwen.service` is still enabled or running", ES: "El antiguo `llama-qwen.service` sigue habilitado o en ejecución"},
		Fix:      Msg{EN: "Run `sudo aiagent migrate llama-qwen`", ES: "Ejecute `sudo aiagent migrate llama-qwen`"},
	},
	{
		ID:       LLM017,
		Severity: Error,
		Title:    Msg{EN: "The migration was stopped before changing anything (model not in catalog, or settings differ)", ES: "La migración se detuvo antes de cambiar nada (modelo fuera del catálogo, o ajustes distintos)"},
		Fix:      Msg{EN: "Read the details; add the model to the catalog, or re-run with `--accept-catalog-flags`", ES: "Lea los detalles; añada el modelo al catálogo, o ejecute de nuevo con `--accept-catalog-flags`"},
	},
	{
		ID:       LLM018,
		Severity: Error,
		Title:    Msg{EN: "Building llama.cpp failed", ES: "Falló la compilación de llama.cpp"},
		Fix:      Msg{EN: "Check the build log in the details; install the missing package named there", ES: "Revise el registro de compilación en los detalles; instale el paquete que falta"},
	},
	{
		ID:       LLM019,
		Severity: Error,
		Title:    Msg{EN: "The new llama.cpp build did not work; the previous build is still in use", ES: "La nueva compilación de llama.cpp no funcionó; se sigue usando la anterior"},
		Fix:      Msg{EN: "Report the build log; nothing else to do", ES: "Informe del registro de compilación; no hay nada más que hacer"},
	},
	{
		ID:       LLM020,
		Severity: Warning,
		Title:    Msg{EN: "In survival mode the preferred model did not start; the next one is used", ES: "En modo supervivencia no arrancó el modelo preferido; se usa el siguiente"},
		Fix:      Msg{EN: "Nothing to do now. Later, check `aictl logs --unit llama`", ES: "No hay que hacer nada ahora. Más tarde, revise `aictl logs --unit llama`"},
	},
	{
		ID:       LLM021,
		Severity: Critical,
		Title:    Msg{EN: "In survival mode no model could be started", ES: "En modo supervivencia no se pudo iniciar ningún modelo"},
		Fix:      Msg{EN: "Run `aictl restart llama`. Library, maps and manuals still work", ES: "Ejecute `aictl restart llama`. La biblioteca, los mapas y los manuales siguen funcionando"},
	},
	{
		ID:       LLM022,
		Severity: Warning,
		Title:    Msg{EN: "A model is running on the CPU instead of the GPU (very slow)", ES: "Un modelo se ejecuta en la CPU en lugar de la GPU (muy lento)"},
		Fix:      Msg{EN: "Restart the computer; check the GPU driver (`aictl doctor` shows which)", ES: "Reinicie el equipo; revise el controlador de la GPU (`aictl doctor` indica cuál)"},
	},
	{
		ID:       LLM023,
		Severity: Info,
		Title:    Msg{EN: "The AI service runs in single-model mode", ES: "El servicio de IA funciona en modo de un solo modelo"},
		Fix:      Msg{EN: "Nothing to do. To switch back: `llama.mode: router` in `node.yaml`", ES: "No hay que hacer nada. Para volver atrás: `llama.mode: router` en `node.yaml`"},
	},
	{
		ID:       LLM024,
		Severity: Warning,
		Title:    Msg{EN: "Extra models from a download cache are visible to the AI service", ES: "El servicio de IA ve modelos adicionales de una caché de descargas"},
		Fix:      Msg{EN: "Empty `/var/lib/localai/llama-cache` and run `aictl restart llama`", ES: "Vacíe `/var/lib/localai/llama-cache` y ejecute `aictl restart llama`"},
	},
	{
		ID:       LLM025,
		Severity: Error,
		Title:    Msg{EN: "The benchmark failed", ES: "Falló la prueba de rendimiento"},
		Fix:      Msg{EN: "Make sure no other GPU program runs and try again", ES: "Asegúrese de que ningún otro programa use la GPU e inténtelo de nuevo"},
	},
	{
		ID:       LLM026,
		Severity: Warning,
		Title:    Msg{EN: "The model list served on port 8080 does not match the catalog", ES: "La lista de modelos servida en el puerto 8080 no coincide con el catálogo"},
		Fix:      Msg{EN: "Run `aictl restart llama`; if it persists, report it", ES: "Ejecute `aictl restart llama`; si continúa, infórmelo"},
	},
	{
		ID:       LLM027,
		Severity: Error,
		Title:    Msg{EN: "A model was stopped because the computer ran out of memory", ES: "Se detuvo un modelo porque el equipo se quedó sin memoria"},
		Fix:      Msg{EN: "Choose a smaller model or a shorter context", ES: "Elija un modelo más pequeño o un contexto más corto"},
	},
	{
		ID:       LLM028,
		Severity: Warning,
		Title:    Msg{EN: "More models are loaded than this node allows", ES: "Hay más modelos cargados de los que permite este nodo"},
		Fix:      Msg{EN: "Run `aictl unload <model>` for the ones you don't need", ES: "Ejecute `aictl unload <modelo>` para los que no necesite"},
	},
	{
		ID:       LLM029,
		Severity: Error,
		Title:    Msg{EN: "The migration from the old service was interrupted", ES: "Se interrumpió la migración desde el servicio antiguo"},
		Fix:      Msg{EN: "Run `sudo aiagent migrate llama-qwen` again (it continues), or `--rollback`", ES: "Ejecute `sudo aiagent migrate llama-qwen` otra vez (continúa donde se quedó), o `--rollback`"},
	},
	{
		ID:       LLM030,
		Severity: Warning,
		Title:    Msg{EN: "A model was running from the ark drive and the drive was removed", ES: "Un modelo se ejecutaba desde la unidad del arca y se retiró la unidad"},
		Fix:      Msg{EN: "Plug the drive back in, or load another model", ES: "Vuelva a conectar la unidad, o cargue otro modelo"},
	},
	{
		ID:       LLM031,
		Severity: Warning,
		Title:    Msg{EN: "A model would not stop, so the AI service was restarted", ES: "Un modelo no se detenía, así que se reinició el servicio de IA"},
		Fix:      Msg{EN: "Nothing to do", ES: "No hay que hacer nada"},
	},
	{
		ID:       CAT001,
		Severity: Error,
		Title:    Msg{EN: "Catalog file can't be parsed", ES: "No se puede analizar el archivo del catálogo"},
		Fix:      Msg{EN: "The catalog file is damaged. Run `aictl catalog restore`, which copies the last good one from the ark kit", ES: "El archivo del catálogo está dañado. Ejecute `aictl catalog restore`, que copia el último bueno desde el kit del arca"},
	},
	{
		ID:       CAT002,
		Severity: Warning,
		Title:    Msg{EN: "New catalog failed validation, previous kept", ES: "El nuevo catálogo no superó la validación; se conserva el anterior"},
		Fix:      Msg{EN: "Nothing to do. Report it to the maintainer", ES: "No hay que hacer nada. Informe al mantenedor"},
	},
	{
		ID:       CAT003,
		Severity: Error,
		Title:    Msg{EN: "The profile in node.yaml doesn't exist", ES: "El perfil de node.yaml no existe"},
		Fix:      Msg{EN: "Run `aictl profile suggest`, then `aictl profile set <id>`", ES: "Ejecute `aictl profile suggest` y luego `aictl profile set <id>`"},
	},
	{
		ID:       CAT004,
		Severity: Error,
		Title:    Msg{EN: "The profile references an unknown model/content id", ES: "El perfil hace referencia a un id de modelo o contenido desconocido"},
		Fix:      Msg{EN: "Catalog bug. Fix it in the repo", ES: "Es un error del catálogo. Corríjalo en el repositorio"},
	},
	{
		ID:       CAT005,
		Severity: Warning,
		Title:    Msg{EN: "A model doesn't fit this node (VRAM/RAM/backend)", ES: "Un modelo no cabe en este nodo (VRAM/RAM/backend)"},
		Fix:      Msg{EN: "Choose a smaller model. `aictl models --loadable` lists the ones that fit", ES: "Elija un modelo más pequeño. `aictl models --loadable` lista los que caben"},
	},
	{
		ID:       CAT010,
		Severity: Error,
		Title:    Msg{EN: "Lint: schema problem (unknown key, missing required field, wrong type, `schema_version` not 1)", ES: "Lint: problema de esquema (clave desconocida, campo obligatorio ausente, tipo incorrecto, `schema_version` ≠ 1)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT011,
		Severity: Error,
		Title:    Msg{EN: "Lint: pinning problem (`source.revision` not 40-hex, `sha256` not 64 hex, `size_bytes` missing)", ES: "Lint: problema de fijación (`source.revision` no es de 40 hex, `sha256` no es de 64 hex, falta `size_bytes`)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT012,
		Severity: Error,
		Title:    Msg{EN: "Lint: bad id format or duplicate id across models and ark", ES: "Lint: formato de id incorrecto o id duplicado entre modelos y arca"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT013,
		Severity: Error,
		Title:    Msg{EN: "Lint: model file rules (llama block, mmproj, draft, chat_template, piper and whisper files)", ES: "Lint: reglas de archivos de modelo (bloque llama, mmproj, draft, chat_template, archivos de piper y whisper)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT014,
		Severity: Error,
		Title:    Msg{EN: "Lint: forbidden `extra_args` or invalid `llama.env` key", ES: "Lint: `extra_args` prohibido o clave `llama.env` no válida"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT015,
		Severity: Error,
		Title:    Msg{EN: "Lint: ark item rules (kiwix `refresh_days`, bbox, kit/docs tier 1, kit id ↔ codename/arch)", ES: "Lint: reglas de elementos del arca (`refresh_days` de kiwix, bbox, nivel 1 de kit/docs, id del kit y nombre/arq.)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT016,
		Severity: Error,
		Title:    Msg{EN: "Lint: optional field misused (`extract`, `extra_repos`/unknown key name, `checksum`/`trust`/`tofu`, `url` vs `files`)", ES: "Lint: campo opcional mal usado (`extract`, `extra_repos`/clave desconocida, `checksum`/`trust`/`tofu`, `url` o `files`)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT017,
		Severity: Error,
		Title:    Msg{EN: "Lint: `portal_links` rule broken", ES: "Lint: se incumple una regla de `portal_links`"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT018,
		Severity: Error,
		Title:    Msg{EN: "Lint: profile rule broken (`router` settings, `include` lists, `ark-drive-full` match, overlapping `match` ranges)", ES: "Lint: regla de perfil incumplida (ajustes de `router`, listas `include`, `match` de `ark-drive-full`, rangos solapados)"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       CAT019,
		Severity: Error,
		Title:    Msg{EN: "Lint: `CATALOG_VERSION` missing or not `YYYY.MM.DD-N`, or wrong file layout", ES: "Lint: falta `CATALOG_VERSION` o no es `AAAA.MM.DD-N`, o la estructura de archivos es incorrecta"},
		Fix:      Msg{EN: "Fix the YAML at the reported path", ES: "Corrija el YAML en la ruta indicada"},
	},
	{
		ID:       NET001,
		Severity: Warning,
		Title:    Msg{EN: "Tailscale is not installed (online mode cannot publish this node)", ES: "Tailscale no está instalado (el modo en línea no puede publicar este nodo)"},
		Fix:      Msg{EN: "Install Tailscale ([runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md)) or set `tailscale.serve_pattern: none`", ES: "Instale Tailscale ([runbooks/05-add-a-node.md](../runbooks/05-add-a-node.md)) o fije `tailscale.serve_pattern: none`"},
	},
	{
		ID:       NET002,
		Severity: Warning,
		Title:    Msg{EN: "The Tailscale service is not running", ES: "El servicio de Tailscale no se está ejecutando"},
		Fix:      Msg{EN: "Run `sudo systemctl start tailscaled`", ES: "Ejecute `sudo systemctl start tailscaled`"},
	},
	{
		ID:       NET003,
		Severity: Warning,
		Title:    Msg{EN: "Tailscale is not logged in", ES: "Tailscale no ha iniciado sesión"},
		Fix:      Msg{EN: "Run `sudo tailscale up` and sign in. Local features keep working", ES: "Ejecute `sudo tailscale up` e inicie sesión. Las funciones locales siguen funcionando"},
	},
	{
		ID:       NET004,
		Severity: Error,
		Title:    Msg{EN: "Tailscale Serve could not be changed for port 8080", ES: "No se pudo cambiar Tailscale Serve para el puerto 8080"},
		Fix:      Msg{EN: "Run `tailscale serve status` and the command in the details by hand, then `aictl doctor`", ES: "Ejecute `tailscale serve status` y el comando de los detalles a mano, y luego `aictl doctor`"},
	},
	{
		ID:       NET005,
		Severity: Warning,
		Title:    Msg{EN: "Another Tailscale Serve listener uses our port, or an extra listener for port 8080 exists", ES: "Otro oyente de Tailscale Serve usa nuestro puerto, o existe un oyente adicional para el puerto 8080"},
		Fix:      Msg{EN: "Remove the extra listener with the exact command in the details (e.g. `sudo tailscale serve --tcp=18080 off`)", ES: "Quite el oyente adicional con el comando exacto de los detalles (p. ej. `sudo tailscale serve --tcp=18080 off`)"},
	},
	{
		ID:       NET006,
		Severity: Critical,
		Title:    Msg{EN: "This node is still published on the tailnet while in offline or survival mode", ES: "Este nodo sigue publicado en la tailnet estando en modo sin conexión o supervivencia"},
		Fix:      Msg{EN: "Run `sudo tailscale serve --https=443 off` and `sudo tailscale serve --tcp=18080 off`", ES: "Ejecute `sudo tailscale serve --https=443 off` y `sudo tailscale serve --tcp=18080 off`"},
	},
	{
		ID:       NET007,
		Severity: Info,
		Title:    Msg{EN: "The Tailscale Serve listener was missing and has been restored", ES: "Faltaba el oyente de Tailscale Serve y se ha restaurado"},
		Fix:      Msg{EN: "Nothing to do", ES: "No hay que hacer nada"},
	},
	{
		ID:       NET008,
		Severity: Info,
		Title:    Msg{EN: "The tailnet HTTPS certificate is still being issued", ES: "El certificado HTTPS de la tailnet aún se está emitiendo"},
		Fix:      Msg{EN: "Wait one minute and try again", ES: "Espere un minuto e inténtelo de nuevo"},
	},
	{
		ID:       NET009,
		Severity: Critical,
		Title:    Msg{EN: "Tailscale Funnel (public internet exposure) was enabled for this node's model port; it has been turned off", ES: "Se habilitó Tailscale Funnel (exposición pública a internet) para el puerto de modelos de este nodo; se ha desactivado"},
		Fix:      Msg{EN: "Never enable Funnel on AI nodes. Check who changed it", ES: "No habilite nunca Funnel en los nodos de IA. Averigüe quién lo cambió"},
	},
	{
		ID:       NET010,
		Severity: Critical,
		Title:    Msg{EN: "A Local AI service is listening on a network address, not only on this computer", ES: "Un servicio de Local AI escucha en una dirección de red, no solo en este equipo"},
		Fix:      Msg{EN: "Run `aictl doctor` for the service name and restart it: `aictl restart <service>`", ES: "Ejecute `aictl doctor` para ver el nombre del servicio y reinícielo: `aictl restart <servicio>`"},
	},
	{
		ID:       NET011,
		Severity: Info,
		Title:    Msg{EN: "No internet for 30 minutes", ES: "Sin internet durante 30 minutos"},
		Fix:      Msg{EN: "Everything here still works. Switch to Survival mode if this is an emergency: `aictl mode survival`", ES: "Todo lo de aquí sigue funcionando. Cambie al modo Supervivencia si es una emergencia: `aictl mode survival`"},
	},
	{
		ID:       NET012,
		Severity: Info,
		Title:    Msg{EN: "The internet works but the cloud (ai.marin.cr) does not answer", ES: "Internet funciona pero la nube (ai.marin.cr) no responde"},
		Fix:      Msg{EN: "Nothing to do on this node. Local features work", ES: "No hay nada que hacer en este nodo. Las funciones locales funcionan"},
	},
	{
		ID:       NET013,
		Severity: Warning,
		Title:    Msg{EN: "Names cannot be looked up (DNS), so downloads and the cloud fail", ES: "No se pueden resolver nombres (DNS), así que fallan las descargas y la nube"},
		Fix:      Msg{EN: "Check the network connection or router", ES: "Revise la conexión de red o el router"},
	},
	{
		ID:       NET014,
		Severity: Warning,
		Title:    Msg{EN: "Wi-Fi power saving is on, which makes a laptop server drop off the network", ES: "El ahorro de energía del Wi-Fi está activado, lo que hace que un portátil servidor se desconecte de la red"},
		Fix:      Msg{EN: "Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md)", ES: "Ejecute otra vez la configuración del portátil: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md)"},
	},
	{
		ID:       HW001,
		Severity: Error,
		Title:    Msg{EN: "No usable GPU was found, but this node is set up for one", ES: "No se encontró ninguna GPU utilizable, pero este nodo está configurado para una"},
		Fix:      Msg{EN: "Check that the graphics driver is installed (`nvidia-smi` or `vulkaninfo --summary`). The CPU-only models still work", ES: "Compruebe que el controlador gráfico esté instalado (`nvidia-smi` o `vulkaninfo --summary`). Los modelos solo para CPU siguen funcionando"},
	},
	{
		ID:       HW002,
		Severity: Error,
		Title:    Msg{EN: "The NVIDIA driver is not answering (`nvidia-smi` failed)", ES: "El controlador de NVIDIA no responde (`nvidia-smi` falló)"},
		Fix:      Msg{EN: "Restart the computer. If it persists, reinstall the driver ([runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md))", ES: "Reinicie el equipo. Si continúa, reinstale el controlador ([runbooks/06-rebuild-a-node-offline.md](../runbooks/06-rebuild-a-node-offline.md))"},
	},
	{
		ID:       HW003,
		Severity: Error,
		Title:    Msg{EN: "The AMD GPU device files are missing (`/dev/kfd`, `/dev/dri/renderD*`)", ES: "Faltan los archivos de dispositivo de la GPU AMD (`/dev/kfd`, `/dev/dri/renderD*`)"},
		Fix:      Msg{EN: "Restart the computer; check that the `amdgpu` driver loads", ES: "Reinicie el equipo; compruebe que se carga el controlador `amdgpu`"},
	},
	{
		ID:       HW004,
		Severity: Error,
		Title:    Msg{EN: "The `localai` user cannot use the GPU (not in `video`/`render`)", ES: "El usuario `localai` no puede usar la GPU (no está en `video`/`render`)"},
		Fix:      Msg{EN: "Run `sudo usermod -aG video,render localai` and `aictl restart llama`", ES: "Ejecute `sudo usermod -aG video,render localai` y `aictl restart llama`"},
	},
	{
		ID:       HW005,
		Severity: Warning,
		Title:    Msg{EN: "Other programs are using a lot of graphics memory", ES: "Otros programas están usando mucha memoria gráfica"},
		Fix:      Msg{EN: "Close games or other GPU programs, or log out of the desktop session", ES: "Cierre los juegos u otros programas de GPU, o cierre la sesión del escritorio"},
	},
	{
		ID:       HW006,
		Severity: Warning,
		Title:    Msg{EN: "The computer is almost out of memory (RAM)", ES: "Al equipo casi no le queda memoria (RAM)"},
		Fix:      Msg{EN: "Close other programs or load a smaller model", ES: "Cierre otros programas o cargue un modelo más pequeño"},
	},
	{
		ID:       HW007,
		Severity: Warning,
		Title:    Msg{EN: "Free disk space is below the profile's safety limit", ES: "El espacio libre en disco está por debajo del límite de seguridad del perfil"},
		Fix:      Msg{EN: "Remove files you don't need, or move content to the ark drive", ES: "Elimine archivos que no necesite, o mueva contenido a la unidad del arca"},
	},
	{
		ID:       HW008,
		Severity: Critical,
		Title:    Msg{EN: "A disk used by Local AI is more than 95% full", ES: "Un disco usado por Local AI está lleno en más del 95 %"},
		Fix:      Msg{EN: "Free space now: delete large personal files or old models (`aictl delete <model>`)", ES: "Libere espacio ahora: borre archivos personales grandes o modelos antiguos (`aictl delete <modelo>`)"},
	},
	{
		ID:       HW009,
		Severity: Warning,
		Title:    Msg{EN: "The GPU is very hot (87 °C or more)", ES: "La GPU está muy caliente (87 °C o más)"},
		Fix:      Msg{EN: "Make sure the vents are not blocked and the laptop is on a hard surface", ES: "Asegúrese de que las rejillas de ventilación no estén bloqueadas y de que el portátil esté sobre una superficie dura"},
	},
	{
		ID:       HW010,
		Severity: Warning,
		Title:    Msg{EN: "The CPU is very hot (95 °C or more)", ES: "La CPU está muy caliente (95 °C o más)"},
		Fix:      Msg{EN: "Same as above; clean the fans if dusty", ES: "Lo mismo que arriba; limpie los ventiladores si tienen polvo"},
	},
	{
		ID:       HW011,
		Severity: Warning,
		Title:    Msg{EN: "The computer is slowing down to cool itself (throttling)", ES: "El equipo está reduciendo su velocidad para enfriarse (limitación térmica)"},
		Fix:      Msg{EN: "Improve airflow; answers will be slower until it cools", ES: "Mejore la ventilación; las respuestas serán más lentas hasta que se enfríe"},
	},
	{
		ID:       HW012,
		Severity: Warning,
		Title:    Msg{EN: "Running on battery", ES: "Funcionando con batería"},
		Fix:      Msg{EN: "Plug in the charger. A laptop server stops when the battery runs out", ES: "Conecte el cargador. Un portátil servidor se apaga cuando se agota la batería"},
	},
	{
		ID:       HW013,
		Severity: Warning,
		Title:    Msg{EN: "Battery below 20%", ES: "Batería por debajo del 20 %"},
		Fix:      Msg{EN: "Plug in the charger soon", ES: "Conecte el cargador pronto"},
	},
	{
		ID:       HW014,
		Severity: Critical,
		Title:    Msg{EN: "Battery below 10%", ES: "Batería por debajo del 10 %"},
		Fix:      Msg{EN: "Plug in the charger now, or save your work", ES: "Conecte el cargador ahora, o guarde su trabajo"},
	},
	{
		ID:       HW015,
		Severity: Info,
		Title:    Msg{EN: "The power profile is not \"performance\"", ES: "El perfil de energía no es «performance»"},
		Fix:      Msg{EN: "Run `powerprofilesctl set performance`", ES: "Ejecute `powerprofilesctl set performance`"},
	},
	{
		ID:       HW016,
		Severity: Warning,
		Title:    Msg{EN: "The computer is using a lot of swap, which makes everything slow", ES: "El equipo usa mucho swap, lo que lo hace todo lento"},
		Fix:      Msg{EN: "Load a smaller model or close other programs", ES: "Cargue un modelo más pequeño o cierre otros programas"},
	},
	{
		ID:       HW017,
		Severity: Error,
		Title:    Msg{EN: "The configured backend does not match the hardware (e.g. CUDA set, no NVIDIA GPU)", ES: "El backend configurado no coincide con el hardware (p. ej. CUDA indicado, sin GPU NVIDIA)"},
		Fix:      Msg{EN: "Run `aictl profile suggest` and fix `llama.backend` in `node.yaml`", ES: "Ejecute `aictl profile suggest` y corrija `llama.backend` en `node.yaml`"},
	},
	{
		ID:       HW018,
		Severity: Error,
		Title:    Msg{EN: "Vulkan only sees a software renderer (llvmpipe), so models run on the CPU", ES: "Vulkan solo ve un renderizador por software (llvmpipe), así que los modelos se ejecutan en la CPU"},
		Fix:      Msg{EN: "Install the Mesa Vulkan drivers (`mesa-vulkan-drivers`) and restart", ES: "Instale los controladores Vulkan de Mesa (`mesa-vulkan-drivers`) y reinicie"},
	},
	{
		ID:       HW019,
		Severity: Error,
		Title:    Msg{EN: "The GPU's CUDA architecture could not be detected or is not supported by this build", ES: "No se pudo detectar la arquitectura CUDA de la GPU o esta compilación no la admite"},
		Fix:      Msg{EN: "Set `llama.cuda_arch` in `node.yaml` and run `sudo aictl rebuild-llama`", ES: "Defina `llama.cuda_arch` en `node.yaml` y ejecute `sudo aictl rebuild-llama`"},
	},
	{
		ID:       HW020,
		Severity: Warning,
		Title:    Msg{EN: "The laptop will sleep when the lid is closed", ES: "El portátil se suspenderá al cerrar la tapa"},
		Fix:      Msg{EN: "Run the laptop setup again: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md)", ES: "Ejecute otra vez la configuración del portátil: [runbooks/09-laptop-as-server.md](../runbooks/09-laptop-as-server.md)"},
	},
	{
		ID:       SEC001,
		Severity: Error,
		Title:    Msg{EN: "Not signed in: the agent token or session is missing or wrong", ES: "Sin sesión iniciada: falta el token o la sesión del agente, o es incorrecto"},
		Fix:      Msg{EN: "Use `aictl` as a member of the `localai-operators` group, or sign in with a code from `aictl ui-code`", ES: "Use `aictl` como miembro del grupo `localai-operators`, o inicie sesión con un código de `aictl ui-code`"},
	},
	{
		ID:       SEC002,
		Severity: Warning,
		Title:    Msg{EN: "This action needs the admin PIN", ES: "Esta acción necesita el PIN de administrador"},
		Fix:      Msg{EN: "Enter the admin PIN when asked, or run the command with `sudo`", ES: "Introduzca el PIN de administrador cuando se pida, o ejecute el comando con `sudo`"},
	},
	{
		ID:       SEC003,
		Severity: Warning,
		Title:    Msg{EN: "Wrong PIN", ES: "PIN incorrecto"},
		Fix:      Msg{EN: "Try again. After 5 wrong tries you must wait 5 minutes", ES: "Inténtelo de nuevo. Tras 5 intentos fallidos debe esperar 5 minutos"},
	},
	{
		ID:       SEC004,
		Severity: Warning,
		Title:    Msg{EN: "Too many wrong PIN or login-code attempts", ES: "Demasiados intentos fallidos de PIN o de código de acceso"},
		Fix:      Msg{EN: "Wait the time shown. Forgotten PIN: `sudo aictl pin set`", ES: "Espere el tiempo indicado. PIN olvidado: `sudo aictl pin set`"},
	},
	{
		ID:       SEC005,
		Severity: Error,
		Title:    Msg{EN: "No admin PIN has been set", ES: "No se ha definido ningún PIN de administrador"},
		Fix:      Msg{EN: "Run `sudo aictl pin set`", ES: "Ejecute `sudo aictl pin set`"},
	},
	{
		ID:       SEC006,
		Severity: Error,
		Title:    Msg{EN: "Request refused: wrong address (Host header)", ES: "Solicitud rechazada: dirección incorrecta (cabecera Host)"},
		Fix:      Msg{EN: "Open `http://127.0.0.1:7070` exactly", ES: "Abra exactamente `http://127.0.0.1:7070`"},
	},
	{
		ID:       SEC007,
		Severity: Error,
		Title:    Msg{EN: "Request refused: it came from another website", ES: "Solicitud rechazada: procede de otro sitio web"},
		Fix:      Msg{EN: "Open the page from `http://127.0.0.1:7070` or the ai-desktop app", ES: "Abra la página desde `http://127.0.0.1:7070` o la aplicación ai-desktop"},
	},
	{
		ID:       SEC008,
		Severity: Warning,
		Title:    Msg{EN: "The login code is wrong, used, or expired", ES: "El código de acceso es incorrecto, ya se usó o ha caducado"},
		Fix:      Msg{EN: "Get a new code with `aictl ui-code`", ES: "Obtenga un código nuevo con `aictl ui-code`"},
	},
	{
		ID:       SEC009,
		Severity: Warning,
		Title:    Msg{EN: "A secret file had unsafe permissions; they were fixed", ES: "Un archivo secreto tenía permisos inseguros; se corrigieron"},
		Fix:      Msg{EN: "Nothing to do. If it happens again, check who changed `/etc/localai`", ES: "No hay que hacer nada. Si vuelve a ocurrir, compruebe quién cambió `/etc/localai`"},
	},
	{
		ID:       SEC010,
		Severity: Critical,
		Title:    Msg{EN: "The llama API key file is missing or not 64 hex characters", ES: "Falta el archivo de la clave de API de llama o no tiene 64 caracteres hexadecimales"},
		Fix:      Msg{EN: "Run `sudo aictl key rotate`", ES: "Ejecute `sudo aictl key rotate`"},
	},
	{
		ID:       SEC011,
		Severity: Warning,
		Title:    Msg{EN: "The agent token was missing and a new one was created", ES: "Faltaba el token del agente y se creó uno nuevo"},
		Fix:      Msg{EN: "Restart ai-desktop; scripts must re-read `/etc/localai/agent-token`", ES: "Reinicie ai-desktop; los scripts deben volver a leer `/etc/localai/agent-token`"},
	},
	{
		ID:       SEC012,
		Severity: Info,
		Title:    Msg{EN: "Your session expired", ES: "Su sesión caducó"},
		Fix:      Msg{EN: "Sign in again", ES: "Inicie sesión de nuevo"},
	},
	{
		ID:       SEC013,
		Severity: Error,
		Title:    Msg{EN: "Your user is not allowed to use the agent", ES: "Su usuario no tiene permiso para usar el agente"},
		Fix:      Msg{EN: "Ask an admin to run `sudo usermod -aG localai-operators <user>`, then log out and in", ES: "Pida a un administrador que ejecute `sudo usermod -aG localai-operators <usuario>` y luego cierre sesión y vuelva a entrar"},
	},
	{
		ID:       SEC014,
		Severity: Critical,
		Title:    Msg{EN: "llama-server answers without the API key", ES: "llama-server responde sin la clave de API"},
		Fix:      Msg{EN: "Run `aictl restart llama`; if it persists, run `aictl doctor` and report it", ES: "Ejecute `aictl restart llama`; si continúa, ejecute `aictl doctor` e infórmelo"},
	},
	{
		ID:       SEC015,
		Severity: Warning,
		Title:    Msg{EN: "The old key path `/etc/llama-qwen/api-key` is not a link to the current key", ES: "La ruta antigua de la clave `/etc/llama-qwen/api-key` no es un enlace a la clave actual"},
		Fix:      Msg{EN: "Run `sudo aiagent migrate llama-qwen` again (it only fixes the link)", ES: "Ejecute `sudo aiagent migrate llama-qwen` otra vez (solo corrige el enlace)"},
	},
	{
		ID:       CLD004,
		Severity: Error,
		Title:    Msg{EN: "The credential belongs to a different node id", ES: "La credencial pertenece a otro id de nodo"},
		Fix:      Msg{EN: "Check `node_id` in `/etc/localai/node.yaml`, or enroll with the credential made for this node", ES: "Revise `node_id` en `/etc/localai/node.yaml`, o inscriba el nodo con la credencial creada para él"},
	},
	{
		ID:       CLD008,
		Severity: Warning,
		Title:    Msg{EN: "The cloud refused this computer's status report", ES: "La nube rechazó el informe de estado de este equipo"},
		Fix:      Msg{EN: "Update the agent (`sudo aictl update`) or tell the admin; local use is not affected", ES: "Actualice el agente (`sudo aictl update`) o avise al administrador; el uso local no se ve afectado"},
	},
	{
		ID:       CLD012,
		Severity: Error,
		Title:    Msg{EN: "A command failed while running", ES: "Un comando falló mientras se ejecutaba"},
		Fix:      Msg{EN: "Read the code in the command's error for the exact fix, then send it again", ES: "Lea el código del error del comando para ver la solución exacta y vuelva a enviarlo"},
	},
	{
		ID:       CLD013,
		Severity: Warning,
		Title:    Msg{EN: "Couldn't download the catalog from the cloud", ES: "No se pudo descargar el catálogo desde la nube"},
		Fix:      Msg{EN: "The node keeps its current catalog. It retries at the next sync", ES: "El nodo conserva su catálogo actual. Lo reintentará en la próxima sincronización"},
	},
	{
		ID:       CLD015,
		Severity: Error,
		Title:    Msg{EN: "An agent update failed and the old version was restored", ES: "Falló una actualización del agente y se restauró la versión anterior"},
		Fix:      Msg{EN: "Nothing is broken. Tell the admin; the update will be retried later", ES: "No hay nada roto. Avise al administrador; la actualización se reintentará más tarde"},
	},
	{
		ID:       CLD020,
		Severity: Warning,
		Title:    Msg{EN: "Node stopped reporting", ES: "El nodo dejó de informar"},
		Fix:      Msg{EN: "Check the computer is on and connected. If it was moved to Offline mode on purpose, ignore this", ES: "Compruebe que el equipo esté encendido y conectado. Si se pasó a modo sin conexión a propósito, ignore este aviso"},
	},
	{
		ID:       CLD021,
		Severity: Critical,
		Title:    Msg{EN: "Node offline for more than a day", ES: "El nodo lleva más de un día sin conexión"},
		Fix:      Msg{EN: "Turn the computer on or check its network. If it is retired, retire it in the cloud", ES: "Encienda el equipo o revise su red. Si está retirado, retírelo también en la nube"},
	},
	{
		ID:       CLD022,
		Severity: Warning,
		Title:    Msg{EN: "The AI-ARK drive hasn't been synced for more than 30 days", ES: "La unidad AI-ARK lleva más de 30 días sin sincronizarse"},
		Fix:      Msg{EN: "Plug the AI-ARK drive into the node and press \"Sync now\" (or run `aictl ark sync`)", ES: "Conecte la unidad AI-ARK al nodo y pulse «Sincronizar ahora» (o ejecute `aictl ark sync`)"},
	},
	{
		ID:       CLD023,
		Severity: Warning,
		Title:    Msg{EN: "The last ark sync failed", ES: "Falló la última sincronización del arca"},
		Fix:      Msg{EN: "Open the node's Ark tab, read the sync error code and follow its fix, then press \"Sync now\"", ES: "Abra la pestaña Arca del nodo, lea el código de error de sincronización y siga su solución, y luego pulse «Sincronizar ahora»"},
	},
	{
		ID:       CLD024,
		Severity: Critical,
		Title:    Msg{EN: "The monthly drill failed", ES: "Falló el simulacro mensual"},
		Fix:      Msg{EN: "Open the node in the cloud or run `aictl drill` on it and follow the failed check's fix", ES: "Abra el nodo en la nube o ejecute `aictl drill` en él y siga la solución de la comprobación fallida"},
	},
	{
		ID:       CLD025,
		Severity: Critical,
		Title:    Msg{EN: "Essential (tier 1) ark content is missing", ES: "Falta contenido esencial (nivel 1) del arca"},
		Fix:      Msg{EN: "Press \"Sync now\" while online, or import from the AI-ARK drive (`aictl ark import`)", ES: "Pulse «Sincronizar ahora» con conexión, o importe desde la unidad AI-ARK (`aictl ark import`)"},
	},
	{
		ID:       CLD026,
		Severity: Warning,
		Title:    Msg{EN: "The cloud has no API key for this node, so the gateway can't use it", ES: "La nube no tiene clave de API para este nodo, así que la pasarela no puede usarlo"},
		Fix:      Msg{EN: "Press \"Refresh status\" on the node; if it persists run `sudo aictl cloud resend-key` on the node", ES: "Pulse «Actualizar estado» en el nodo; si continúa, ejecute `sudo aictl cloud resend-key` en el nodo"},
	},
	{
		ID:       CLD027,
		Severity: Warning,
		Title:    Msg{EN: "The node's public address (ai-nN.marin.cr) is not answering", ES: "La dirección pública del nodo (ai-nN.marin.cr) no responde"},
		Fix:      Msg{EN: "Check `tailscale serve status` on the node and the vhost on the VPS (runbook 05)", ES: "Revise `tailscale serve status` en el nodo y el vhost en el VPS (runbook 05)"},
	},
	{
		ID:       CLD028,
		Severity: Info,
		Title:    Msg{EN: "The node uses an old catalog", ES: "El nodo usa un catálogo antiguo"},
		Fix:      Msg{EN: "Press \"Refresh catalog\" on the node", ES: "Pulse «Actualizar catálogo» en el nodo"},
	},
	{
		ID:       CLD029,
		Severity: Info,
		Title:    Msg{EN: "A newer agent is available", ES: "Hay un agente más nuevo disponible"},
		Fix:      Msg{EN: "Admin: Lab → node → Update agent", ES: "Administrador: Laboratorio → nodo → Actualizar agente"},
	},
	{
		ID:       CLD030,
		Severity: Warning,
		Title:    Msg{EN: "Commands are waiting longer than they should", ES: "Hay comandos esperando más de lo debido"},
		Fix:      Msg{EN: "Admin: check that the cloud's background jobs run (Admin → Jobs)", ES: "Administrador: compruebe que los trabajos en segundo plano de la nube se ejecutan (Administración → Trabajos)"},
	},
	{
		ID:       CLD031,
		Severity: Warning,
		Title:    Msg{EN: "A node was added but never connected", ES: "Se añadió un nodo pero nunca se conectó"},
		Fix:      Msg{EN: "Finish the install on that computer, or retire the node in the cloud", ES: "Termine la instalación en ese equipo, o retire el nodo en la nube"},
	},
	{
		ID:       CLD032,
		Severity: Critical,
		Title:    Msg{EN: "The cloud can't read its stored keys (encryption key missing or wrong)", ES: "La nube no puede leer sus claves guardadas (falta la clave de cifrado o es incorrecta)"},
		Fix:      Msg{EN: "Admin: restore `SECRETS_ENCRYPTION_KEY` from the backup, restart, then \"Ask all nodes to re-send keys\"", ES: "Administrador: restaure `SECRETS_ENCRYPTION_KEY` desde la copia de seguridad, reinicie y luego use «Pedir a todos los nodos que reenvíen sus claves»"},
	},
	{
		ID:       CLD033,
		Severity: Error,
		Title:    Msg{EN: "The catalog checker (`aictl`) is missing on the cloud server", ES: "Falta el verificador de catálogo (`aictl`) en el servidor de la nube"},
		Fix:      Msg{EN: "Admin: redeploy the cloud app (`laictl deploy update`)", ES: "Administrador: vuelva a desplegar la aplicación de la nube (`laictl deploy update`)"},
	},
	{
		ID:       CLD034,
		Severity: Info,
		Title:    Msg{EN: "The node is in Offline or Survival mode, so it won't take cloud commands", ES: "El nodo está en modo sin conexión o supervivencia, así que no aceptará comandos de la nube"},
		Fix:      Msg{EN: "Someone at the computer must switch it back to Online", ES: "Alguien en el equipo debe volver a ponerlo en modo en línea"},
	},
	{
		ID:       CLD035,
		Severity: Error,
		Title:    Msg{EN: "The node is retired or not enrolled yet", ES: "El nodo está retirado o aún no está inscrito"},
		Fix:      Msg{EN: "Finish enrolling it, or add a new node", ES: "Termine de inscribirlo, o añada un nodo nuevo"},
	},
	{
		ID:       CLD050,
		Severity: Info,
		Title:    Msg{EN: "This node is not connected to the cloud", ES: "Este nodo no está conectado a la nube"},
		Fix:      Msg{EN: "To connect it: `sudo aictl cloud enroll --url https://ai.marin.cr`", ES: "Para conectarlo: `sudo aictl cloud enroll --url https://ai.marin.cr`"},
	},
	{
		ID:       CLD051,
		Severity: Error,
		Title:    Msg{EN: "The cloud rejected this node's credential (revoked or wrong)", ES: "La nube rechazó la credencial de este nodo (revocada o incorrecta)"},
		Fix:      Msg{EN: "Create a new node credential at ai.marin.cr and run `sudo aictl cloud enroll` again", ES: "Cree una credencial de nodo nueva en ai.marin.cr y ejecute `sudo aictl cloud enroll` otra vez"},
	},
	{
		ID:       CLD052,
		Severity: Warning,
		Title:    Msg{EN: "An operator disabled this node in the cloud", ES: "Un operador deshabilitó este nodo en la nube"},
		Fix:      Msg{EN: "Re-enable it at ai.marin.cr (Nodes page). Local features work", ES: "Vuelva a habilitarlo en ai.marin.cr (página Nodos). Las funciones locales funcionan"},
	},
	{
		ID:       CLD053,
		Severity: Warning,
		Title:    Msg{EN: "The cloud cannot be reached right now", ES: "No se puede llegar a la nube en este momento"},
		Fix:      Msg{EN: "Nothing to do; the node retries by itself. Local features work", ES: "No hay que hacer nada; el nodo reintenta solo. Las funciones locales funcionan"},
	},
	{
		ID:       CLD054,
		Severity: Error,
		Title:    Msg{EN: "The cloud refused this node's registration", ES: "La nube rechazó el registro de este nodo"},
		Fix:      Msg{EN: "Check `aictl logs --unit agent` for the details and report them", ES: "Revise `aictl logs --unit agent` para ver los detalles e infórmelos"},
	},
	{
		ID:       CLD055,
		Severity: Info,
		Title:    Msg{EN: "The cloud is older than this agent; some details are not sent", ES: "La nube es más antigua que este agente; no se envían algunos detalles"},
		Fix:      Msg{EN: "Update the cloud app", ES: "Actualice la aplicación de la nube"},
	},
	{
		ID:       CLD056,
		Severity: Info,
		Title:    Msg{EN: "The cloud has no node-state endpoint yet; a reduced status is sent", ES: "La nube aún no tiene el punto de acceso del estado del nodo; se envía un estado reducido"},
		Fix:      Msg{EN: "Update the cloud app", ES: "Actualice la aplicación de la nube"},
	},
	{
		ID:       CLD057,
		Severity: Warning,
		Title:    Msg{EN: "The cloud asked for an action this node does not allow", ES: "La nube pidió una acción que este nodo no permite"},
		Fix:      Msg{EN: "Nothing to do. To allow it, edit `cloud.allow` in `node.yaml`", ES: "No hay que hacer nada. Para permitirla, edite `cloud.allow` en `node.yaml`"},
	},
	{
		ID:       CLD058,
		Severity: Warning,
		Title:    Msg{EN: "The cloud sent an invalid command", ES: "La nube envió un comando no válido"},
		Fix:      Msg{EN: "Report it to the maintainer", ES: "Informe al mantenedor"},
	},
	{
		ID:       CLD059,
		Severity: Info,
		Title:    Msg{EN: "A cloud command finished after the cloud stopped waiting for it", ES: "Un comando de la nube terminó después de que la nube dejara de esperarlo"},
		Fix:      Msg{EN: "Nothing to do; the cloud shows the current state", ES: "No hay que hacer nada; la nube muestra el estado actual"},
	},
	{
		ID:       CLD060,
		Severity: Warning,
		Title:    Msg{EN: "The cloud does not have this node's current API key yet", ES: "La nube aún no tiene la clave de API actual de este nodo"},
		Fix:      Msg{EN: "Check the connection; the agent retries every 5 minutes", ES: "Revise la conexión; el agente reintenta cada 5 minutos"},
	},
	{
		ID:       CLD061,
		Severity: Warning,
		Title:    Msg{EN: "Another computer is using this node id", ES: "Otro equipo está usando este id de nodo"},
		Fix:      Msg{EN: "Give each computer its own id in `node.yaml` (`node.id`)", ES: "Dé a cada equipo su propio id en `node.yaml` (`node.id`)"},
	},
	{
		ID:       CLD062,
		Severity: Error,
		Title:    Msg{EN: "The cloud address must start with https://", ES: "La dirección de la nube debe empezar por https://"},
		Fix:      Msg{EN: "Fix `cloud.url` in `node.yaml`", ES: "Corrija `cloud.url` en `node.yaml`"},
	},
	{
		ID:       CLD063,
		Severity: Info,
		Title:    Msg{EN: "A cloud command was stopped because the node is going offline", ES: "Un comando de la nube se detuvo porque el nodo pasa a modo sin conexión"},
		Fix:      Msg{EN: "Nothing to do. Run the command again from ai.marin.cr after the node is back online", ES: "No hay que hacer nada. Vuelva a ejecutar el comando desde ai.marin.cr cuando el nodo esté de nuevo en línea"},
	},
	{
		ID:       SYN001,
		Severity: Info,
		Title:    Msg{EN: "Scheduled sync skipped: no internet", ES: "Sincronización programada omitida: sin internet"},
		Fix:      Msg{EN: "Nothing to do. Sync will try again tonight.", ES: "No hay que hacer nada. La sincronización lo intentará de nuevo esta noche."},
	},
	{
		ID:       SYN002,
		Severity: Info,
		Title:    Msg{EN: "Sync skipped or stopped because the node is in offline or survival mode", ES: "Sincronización omitida o detenida porque el nodo está en modo sin conexión o supervivencia"},
		Fix:      Msg{EN: "Nothing to do. Switch to online mode to sync again.", ES: "No hay que hacer nada. Cambie al modo en línea para sincronizar de nuevo."},
	},
	{
		ID:       SYN003,
		Severity: Info,
		Title:    Msg{EN: "Scheduled sync skipped: running on battery", ES: "Sincronización programada omitida: funcionando con batería"},
		Fix:      Msg{EN: "Plug in the charger. Sync runs at night when plugged in.", ES: "Conecte el cargador. La sincronización se ejecuta por la noche con el equipo enchufado."},
	},
	{
		ID:       SYN004,
		Severity: Warning,
		Title:    Msg{EN: "No successful sync for more than 14 days while online", ES: "Más de 14 días sin una sincronización correcta estando en línea"},
		Fix:      Msg{EN: "Check the internet connection, then run `aictl ark sync`.", ES: "Revise la conexión a internet y luego ejecute `aictl ark sync`."},
	},
	{
		ID:       SYN005,
		Severity: Warning,
		Title:    Msg{EN: "Another sync is already running", ES: "Ya hay otra sincronización en curso"},
		Fix:      Msg{EN: "Wait for it to finish, or cancel it with `aictl ark sync --cancel`.", ES: "Espere a que termine, o cancélela con `aictl ark sync --cancel`."},
	},
	{
		ID:       SYN010,
		Severity: Warning,
		Title:    Msg{EN: "Manual sync refused in survival mode", ES: "Sincronización manual rechazada en modo supervivencia"},
		Fix:      Msg{EN: "Leave survival mode first (needs the PIN).", ES: "Salga primero del modo supervivencia (necesita el PIN)."},
	},
	{
		ID:       SYN011,
		Severity: Warning,
		Title:    Msg{EN: "Manual sync in offline mode needs confirmation", ES: "La sincronización manual en modo sin conexión necesita confirmación"},
		Fix:      Msg{EN: "Run again with `--allow-offline-mode`, or switch to online mode.", ES: "Ejecútela de nuevo con `--allow-offline-mode`, o cambie al modo en línea."},
	},
	{
		ID:       SYN020,
		Severity: Warning,
		Title:    Msg{EN: "A download didn't match its checksum and was quarantined", ES: "Una descarga no coincidía con su suma de verificación y se puso en cuarentena"},
		Fix:      Msg{EN: "Nothing to do: it will be downloaded again. If it repeats, report it.", ES: "No hay que hacer nada: se descargará de nuevo. Si se repite, infórmelo."},
	},
	{
		ID:       SYN021,
		Severity: Warning,
		Title:    Msg{EN: "The source's checksum or size disagrees with the catalog", ES: "La suma de verificación o el tamaño de la fuente no coincide con el catálogo"},
		Fix:      Msg{EN: "The catalog or the source is wrong. Report it to the maintainer. The old copy is kept.", ES: "El catálogo o la fuente son incorrectos. Informe al mantenedor. Se conserva la copia anterior."},
	},
	{
		ID:       SYN022,
		Severity: Warning,
		Title:    Msg{EN: "Kiwix checksum file missing or unreadable", ES: "Falta el archivo de sumas de verificación de Kiwix o no se puede leer"},
		Fix:      Msg{EN: "Nothing to do. It will be retried next run; the old copy is kept.", ES: "No hay que hacer nada. Se reintentará en la próxima ejecución; se conserva la copia anterior."},
	},
	{
		ID:       SYN023,
		Severity: Warning,
		Title:    Msg{EN: "Map extract failed `pmtiles verify`", ES: "La extracción de mapas no superó `pmtiles verify`"},
		Fix:      Msg{EN: "Nothing to do: it will be rebuilt next run.", ES: "No hay que hacer nada: se reconstruirá en la próxima ejecución."},
	},
	{
		ID:       SYN024,
		Severity: Warning,
		Title:    Msg{EN: "A source mirror is damaged or missing the pinned commit", ES: "Un espejo de código fuente está dañado o le falta el commit fijado"},
		Fix:      Msg{EN: "Run `aictl ark sync --item <id>`. If it repeats, delete the mirror with `aictl ark prune`.", ES: "Ejecute `aictl ark sync --item <id>`. Si se repite, borre el espejo con `aictl ark prune`."},
	},
	{
		ID:       SYN025,
		Severity: Error,
		Title:    Msg{EN: "A stored file no longer matches its checksum (disk damage), or wasn't re-checked in time", ES: "Un archivo guardado ya no coincide con su suma de verificación (disco dañado), o no se volvió a comprobar a tiempo"},
		Fix:      Msg{EN: "Run `aictl ark verify --full`. If files keep failing, the disk may be dying: copy the ark to another drive.", ES: "Ejecute `aictl ark verify --full`. Si los archivos siguen fallando, el disco puede estar muriendo: copie el arca a otra unidad."},
	},
	{
		ID:       SYN026,
		Severity: Error,
		Title:    Msg{EN: "A swap couldn't complete", ES: "No se pudo completar un intercambio de versiones"},
		Fix:      Msg{EN: "Run `aictl ark verify`. The previous version was restored if it existed.", ES: "Ejecute `aictl ark verify`. Se restauró la versión anterior si existía."},
	},
	{
		ID:       SYN027,
		Severity: Warning,
		Title:    Msg{EN: "Downloaded size differs from the expected size", ES: "El tamaño descargado difiere del esperado"},
		Fix:      Msg{EN: "Nothing to do: it will be downloaded again.", ES: "No hay que hacer nada: se descargará de nuevo."},
	},
	{
		ID:       SYN028,
		Severity: Warning,
		Title:    Msg{EN: "ZIM internal checksum failed", ES: "Falló la suma de verificación interna del ZIM"},
		Fix:      Msg{EN: "Nothing to do: it will be downloaded again.", ES: "No hay que hacer nada: se descargará de nuevo."},
	},
	{
		ID:       SYN030,
		Severity: Warning,
		Title:    Msg{EN: "The server refused or no longer has the file (403/404/410)", ES: "El servidor rechazó el archivo o ya no lo tiene (403/404/410)"},
		Fix:      Msg{EN: "Update the catalog (`aictl catalog refresh`). If it persists, report it.", ES: "Actualice el catálogo (`aictl catalog refresh`). Si continúa, infórmelo."},
	},
	{
		ID:       SYN031,
		Severity: Warning,
		Title:    Msg{EN: "Download failed after all retries", ES: "La descarga falló tras todos los reintentos"},
		Fix:      Msg{EN: "Check the internet connection. It resumes where it stopped next run.", ES: "Revise la conexión a internet. Se reanuda donde se quedó en la próxima ejecución."},
	},
	{
		ID:       SYN032,
		Severity: Info,
		Title:    Msg{EN: "Server doesn't support resume; the file restarted from zero", ES: "El servidor no admite reanudar; el archivo empezó de nuevo desde cero"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       SYN033,
		Severity: Warning,
		Title:    Msg{EN: "Couldn't find the newest version at the source", ES: "No se encontró la versión más reciente en la fuente"},
		Fix:      Msg{EN: "Nothing to do; it retries next run. If it persists, report it.", ES: "No hay que hacer nada; se reintenta en la próxima ejecución. Si continúa, infórmelo."},
	},
	{
		ID:       SYN034,
		Severity: Warning,
		Title:    Msg{EN: "Hugging Face needs a login or is rate-limiting", ES: "Hugging Face exige iniciar sesión o está limitando las peticiones"},
		Fix:      Msg{EN: "Put a Hugging Face token in `/etc/localai/hf-token`, or wait an hour.", ES: "Ponga un token de Hugging Face en `/etc/localai/hf-token`, o espere una hora."},
	},
	{
		ID:       SYN035,
		Severity: Error,
		Title:    Msg{EN: "The same item failed in 3 runs in a row", ES: "El mismo elemento falló en 3 ejecuciones seguidas"},
		Fix:      Msg{EN: "Run `aictl doctor` and read the item's last error.", ES: "Ejecute `aictl doctor` y lea el último error del elemento."},
	},
	{
		ID:       SYN040,
		Severity: Warning,
		Title:    Msg{EN: "Not enough free disk space", ES: "No hay suficiente espacio libre en disco"},
		Fix:      Msg{EN: "Free space on the disk, or lower the profile budget. Old versions in `.trash` are removed automatically when safe.", ES: "Libere espacio en el disco, o reduzca el presupuesto del perfil. Las versiones antiguas de `.trash` se eliminan solas cuando es seguro."},
	},
	{
		ID:       SYN041,
		Severity: Warning,
		Title:    Msg{EN: "Items skipped because the budget is full (a survival model, if named)", ES: "Elementos omitidos porque el presupuesto está lleno (un modelo de supervivencia, si se nombra)"},
		Fix:      Msg{EN: "Raise `budget_bytes` or remove other content in `node.yaml`.", ES: "Aumente `budget_bytes` o quite otro contenido en `node.yaml`."},
	},
	{
		ID:       SYN042,
		Severity: Error,
		Title:    Msg{EN: "The rebuild kit or manuals don't fit", ES: "El kit de reconstrucción o los manuales no caben"},
		Fix:      Msg{EN: "Free disk space. Without the kit this computer can't be rebuilt offline.", ES: "Libere espacio en disco. Sin el kit este equipo no se puede reconstruir sin conexión."},
	},
	{
		ID:       SYN043,
		Severity: Info,
		Title:    Msg{EN: "Old versions were deleted to make room", ES: "Se borraron versiones antiguas para hacer sitio"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       SYN044,
		Severity: Warning,
		Title:    Msg{EN: "The last verified copy was kept; nothing was deleted", ES: "Se conservó la última copia verificada; no se borró nada"},
		Fix:      Msg{EN: "This is the only verified copy on this computer. Copy it to another drive first (`aictl ark sync --root <drive>`), or delete it anyway with `aictl delete <model> --allow-last-copy`.", ES: "Es la única copia verificada de este equipo. Cópiela primero a otro disco (`aictl ark sync --root <disco>`), o bórrela igualmente con `aictl delete <modelo> --allow-last-copy`."},
	},
	{
		ID:       SYN050,
		Severity: Info,
		Title:    Msg{EN: "Sync journal was damaged and rebuilt", ES: "El diario de sincronización estaba dañado y se reconstruyó"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       SYN051,
		Severity: Info,
		Title:    Msg{EN: "Content no longer in the profile is using disk space", ES: "Contenido que ya no está en el perfil ocupa espacio en disco"},
		Fix:      Msg{EN: "Run `aictl ark prune --orphans` if you don't need it.", ES: "Ejecute `aictl ark prune --orphans` si no lo necesita."},
	},
	{
		ID:       SYN052,
		Severity: Warning,
		Title:    Msg{EN: "An item's MANIFEST is missing or damaged", ES: "Falta el MANIFEST de un elemento o está dañado"},
		Fix:      Msg{EN: "Run `aictl ark verify`; the item will be fetched again.", ES: "Ejecute `aictl ark verify`; el elemento se obtendrá de nuevo."},
	},
	{
		ID:       SYN060,
		Severity: Warning,
		Title:    Msg{EN: "Couldn't compute the package list for the rebuild kit", ES: "No se pudo calcular la lista de paquetes del kit de reconstrucción"},
		Fix:      Msg{EN: "Report it: a package name in the catalog probably changed. The old package set is kept.", ES: "Infórmelo: probablemente cambió el nombre de un paquete en el catálogo. Se conserva el conjunto de paquetes anterior."},
	},
	{
		ID:       SYN061,
		Severity: Error,
		Title:    Msg{EN: "A tool sync needs is missing (pmtiles, git, apt-get)", ES: "Falta una herramienta que necesita la sincronización (pmtiles, git, apt-get)"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`.", ES: "Ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       SYN062,
		Severity: Warning,
		Title:    Msg{EN: "Manuals were made without the real lab values", ES: "Los manuales se generaron sin los valores reales del laboratorio"},
		Fix:      Msg{EN: "Copy `inventory.local.yaml` to `/etc/localai/` or plug in the ark drive, then sync.", ES: "Copie `inventory.local.yaml` a `/etc/localai/` o conecte la unidad del arca, y luego sincronice."},
	},
	{
		ID:       SYN070,
		Severity: Error,
		Title:    Msg{EN: "A drive copy no longer matches its own checksum", ES: "Una copia de la unidad ya no coincide con su propia suma de verificación"},
		Fix:      Msg{EN: "That copy is damaged. Sync the drive on a computer with internet, or use another drive.", ES: "Esa copia está dañada. Sincronice la unidad en un equipo con internet, o use otra unidad."},
	},
	{
		ID:       ARK001,
		Severity: Error,
		Title:    Msg{EN: "No writable content root (no drive and internal read-only)", ES: "No hay raíz de contenido con escritura (ni unidad ni interna de solo lectura)"},
		Fix:      Msg{EN: "Plug in an AI-ARK drive, or run `aictl doctor`.", ES: "Conecte una unidad AI-ARK, o ejecute `aictl doctor`."},
	},
	{
		ID:       ARK002,
		Severity: Error,
		Title:    Msg{EN: "A drive labelled AI-ARK is not ext4", ES: "Una unidad con la etiqueta AI-ARK no es ext4"},
		Fix:      Msg{EN: "Reformat it as ext4 (see \"Formatting a new AI-ARK drive\"). This erases it.", ES: "Formatéela como ext4 (vea «Formatear una unidad AI-ARK nueva»). Esto la borra."},
	},
	{
		ID:       ARK003,
		Severity: Warning,
		Title:    Msg{EN: "The ark drive is read-only", ES: "La unidad del arca es de solo lectura"},
		Fix:      Msg{EN: "It can be read but not updated. Unplug it safely and plug it in again.", ES: "Se puede leer pero no actualizar. Retírela de forma segura y conéctela de nuevo."},
	},
	{
		ID:       ARK004,
		Severity: Error,
		Title:    Msg{EN: "The ark drive's filesystem has errors", ES: "El sistema de archivos de la unidad del arca tiene errores"},
		Fix:      Msg{EN: "Eject it, then run `sudo fsck.ext4 -f /dev/<device>` and plug it in again.", ES: "Expúlsela, ejecute `sudo fsck.ext4 -f /dev/<dispositivo>` y conéctela de nuevo."},
	},
	{
		ID:       ARK005,
		Severity: Warning,
		Title:    Msg{EN: "An AI-ARK drive is plugged in but not mounted", ES: "Hay una unidad AI-ARK conectada pero no montada"},
		Fix:      Msg{EN: "Press **Mount** in the app, or open it in the file manager.", ES: "Pulse **Montar** en la aplicación, o ábrala en el gestor de archivos."},
	},
	{
		ID:       ARK006,
		Severity: Error,
		Title:    Msg{EN: "Two drives have the same ID (a cloned drive)", ES: "Dos unidades tienen el mismo ID (una unidad clonada)"},
		Fix:      Msg{EN: "Unplug one. Give it a new ID with `sudo tune2fs -U random /dev/<device>`.", ES: "Desconecte una. Dele un ID nuevo con `sudo tune2fs -U random /dev/<dispositivo>`."},
	},
	{
		ID:       ARK007,
		Severity: Warning,
		Title:    Msg{EN: "The ark drive was unplugged without ejecting", ES: "La unidad del arca se desconectó sin expulsarla"},
		Fix:      Msg{EN: "Next time press **Eject** first. Plug it in again to let sync check it.", ES: "La próxima vez pulse **Expulsar** primero. Conéctela de nuevo para que la sincronización la compruebe."},
	},
	{
		ID:       ARK008,
		Severity: Warning,
		Title:    Msg{EN: "The ark drive hasn't been updated for more than 30 days (red after 90)", ES: "La unidad del arca lleva más de 30 días sin actualizarse (roja tras 90)"},
		Fix:      Msg{EN: "Plug it into a computer with internet overnight.", ES: "Conéctela durante la noche a un equipo con internet."},
	},
	{
		ID:       ARK009,
		Severity: Warning,
		Title:    Msg{EN: "The drive can't be ejected: files are in use", ES: "No se puede expulsar la unidad: hay archivos en uso"},
		Fix:      Msg{EN: "Close the listed programs or switch brain, then eject again.", ES: "Cierre los programas indicados o cambie de cerebro, y expúlsela de nuevo."},
	},
	{
		ID:       ARK010,
		Severity: Info,
		Title:    Msg{EN: "A new AI-ARK drive needs to be set up", ES: "Hay que preparar una unidad AI-ARK nueva"},
		Fix:      Msg{EN: "Press **Set up this drive**, or run `aictl ark init`.", ES: "Presione **Preparar este disco**, o ejecute `aictl ark init`."},
	},
	{
		ID:       ARK011,
		Severity: Info,
		Title:    Msg{EN: "The drive is too small for everything; lower tiers were skipped", ES: "La unidad es demasiado pequeña para todo; se omitieron los niveles inferiores"},
		Fix:      Msg{EN: "Nothing to do, or use a bigger drive.", ES: "No hay que hacer nada, o use una unidad más grande."},
	},
	{
		ID:       ARK012,
		Severity: Warning,
		Title:    Msg{EN: "The lab inventory on the drive and on this computer both changed", ES: "El inventario del laboratorio de la unidad y el de este equipo cambiaron los dos"},
		Fix:      Msg{EN: "Compare the two files and keep the right one. The other is saved as `.conflict`.", ES: "Compare los dos archivos y conserve el correcto. El otro se guarda como `.conflict`."},
	},
	{
		ID:       ARK014,
		Severity: Error,
		Title:    Msg{EN: "The drive reported read/write errors", ES: "La unidad informó de errores de lectura o escritura"},
		Fix:      Msg{EN: "Eject it and try another USB port or cable. If it repeats, replace the drive.", ES: "Expúlsela y pruebe otro puerto USB u otro cable. Si se repite, cambie la unidad."},
	},
	{
		ID:       ARK015,
		Severity: Warning,
		Title:    Msg{EN: "Several ark drives are connected; choose one", ES: "Hay varias unidades del arca conectadas; elija una"},
		Fix:      Msg{EN: "Run the import again with `--from <root-id>` (see `aictl ark drives`).", ES: "Ejecute la importación de nuevo con `--from <id-de-raíz>` (vea `aictl ark drives`)."},
	},
	{
		ID:       ARK020,
		Severity: Warning,
		Title:    Msg{EN: "The internal content folder was missing and was recreated empty", ES: "Faltaba la carpeta de contenido interna y se volvió a crear vacía"},
		Fix:      Msg{EN: "Run `aictl ark sync`, or `aictl ark import` from a drive.", ES: "Ejecute `aictl ark sync`, o `aictl ark import` desde un disco."},
	},
	{
		ID:       ARK021,
		Severity: Warning,
		Title:    Msg{EN: "The content folder is on a network disk; it is used read-only", ES: "La carpeta de contenido está en un disco de red; se usa solo de lectura"},
		Fix:      Msg{EN: "Move `/srv/localai` to a local disk.", ES: "Mueva `/srv/localai` a un disco local."},
	},
	{
		ID:       SVC001,
		Severity: Error,
		Title:    Msg{EN: "The library has no books", ES: "La biblioteca no tiene libros"},
		Fix:      Msg{EN: "Plug in the ark drive, or run `aictl ark import`.", ES: "Conecte la unidad del arca, o ejecute `aictl ark import`."},
	},
	{
		ID:       SVC002,
		Severity: Error,
		Title:    Msg{EN: "The library service isn't running", ES: "El servicio de la biblioteca no se está ejecutando"},
		Fix:      Msg{EN: "Press **Restart library**, or run `aictl services restart kiwix`.", ES: "Pulse **Reiniciar biblioteca**, o ejecute `aictl services restart kiwix`."},
	},
	{
		ID:       SVC003,
		Severity: Error,
		Title:    Msg{EN: "The library program isn't installed", ES: "El programa de la biblioteca no está instalado"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`.", ES: "Ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       SVC004,
		Severity: Warning,
		Title:    Msg{EN: "One library book couldn't be opened", ES: "No se pudo abrir un libro de la biblioteca"},
		Fix:      Msg{EN: "Run `aictl ark verify`. The other books still work.", ES: "Ejecute `aictl ark verify`. Los demás libros siguen funcionando."},
	},
	{
		ID:       SVC005,
		Severity: Warning,
		Title:    Msg{EN: "The library list couldn't be rebuilt; the old list is used", ES: "No se pudo reconstruir la lista de la biblioteca; se usa la lista anterior"},
		Fix:      Msg{EN: "Run `aictl services restart kiwix`. If it repeats, run `aictl doctor`.", ES: "Ejecute `aictl services restart kiwix`. Si se repite, ejecute `aictl doctor`."},
	},
	{
		ID:       SVC010,
		Severity: Error,
		Title:    Msg{EN: "No speech-recognition model is installed", ES: "No hay ningún modelo de reconocimiento de voz instalado"},
		Fix:      Msg{EN: "Run `aictl ark sync --item whisper-small` (online) or `aictl ark import` from the drive.", ES: "Ejecute `aictl ark sync --item whisper-small` (con conexión) o `aictl ark import` desde la unidad."},
	},
	{
		ID:       SVC011,
		Severity: Error,
		Title:    Msg{EN: "The speech-recognition service isn't running", ES: "El servicio de reconocimiento de voz no se está ejecutando"},
		Fix:      Msg{EN: "Run `aictl services restart whisper`.", ES: "Ejecute `aictl services restart whisper`."},
	},
	{
		ID:       SVC012,
		Severity: Error,
		Title:    Msg{EN: "The speech-recognition program isn't built for this computer", ES: "El programa de reconocimiento de voz no está compilado para este equipo"},
		Fix:      Msg{EN: "Run `sudo aictl rebuild-whisper`.", ES: "Ejecute `sudo aictl rebuild-whisper`."},
	},
	{
		ID:       SVC013,
		Severity: Info,
		Title:    Msg{EN: "Speech recognition runs on the processor to leave graphics memory for the AI", ES: "El reconocimiento de voz se ejecuta en el procesador para dejar la memoria gráfica a la IA"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       SVC014,
		Severity: Warning,
		Title:    Msg{EN: "The recording couldn't be used (wrong format, too long, or silent)", ES: "No se pudo usar la grabación (formato incorrecto, demasiado larga o en silencio)"},
		Fix:      Msg{EN: "Try again, speak closer to the microphone, and keep it under 2 minutes.", ES: "Inténtelo de nuevo, hable más cerca del micrófono y manténgala por debajo de 2 minutos."},
	},
	{
		ID:       SVC015,
		Severity: Warning,
		Title:    Msg{EN: "Too many voice requests at once", ES: "Demasiadas solicitudes de voz a la vez"},
		Fix:      Msg{EN: "Wait a few seconds and try again.", ES: "Espere unos segundos e inténtelo de nuevo."},
	},
	{
		ID:       SVC016,
		Severity: Warning,
		Title:    Msg{EN: "Speech recognition took too long", ES: "El reconocimiento de voz tardó demasiado"},
		Fix:      Msg{EN: "Try a shorter recording.", ES: "Pruebe con una grabación más corta."},
	},
	{
		ID:       SVC020,
		Severity: Error,
		Title:    Msg{EN: "The voice program isn't installed", ES: "El programa de voz no está instalado"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`.", ES: "Ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       SVC021,
		Severity: Warning,
		Title:    Msg{EN: "No voice for this language; another voice was used", ES: "No hay voz para este idioma; se usó otra voz"},
		Fix:      Msg{EN: "Install a voice for the language with `aictl ark sync`.", ES: "Instale una voz para el idioma con `aictl ark sync`."},
	},
	{
		ID:       SVC022,
		Severity: Warning,
		Title:    Msg{EN: "No voice is installed", ES: "No hay ninguna voz instalada"},
		Fix:      Msg{EN: "Answers are shown as text. Install a voice with `aictl ark import` or `aictl ark sync`.", ES: "Las respuestas se muestran como texto. Instale una voz con `aictl ark import` o `aictl ark sync`."},
	},
	{
		ID:       SVC023,
		Severity: Error,
		Title:    Msg{EN: "The voice program failed", ES: "El programa de voz falló"},
		Fix:      Msg{EN: "Try again. If it repeats, run `aictl doctor`.", ES: "Inténtelo de nuevo. Si se repite, ejecute `aictl doctor`."},
	},
	{
		ID:       SVC024,
		Severity: Info,
		Title:    Msg{EN: "The text was too long to read aloud at once", ES: "El texto era demasiado largo para leerlo en voz alta de una vez"},
		Fix:      Msg{EN: "Nothing to do: the app reads it in parts.", ES: "No hay que hacer nada: la aplicación lo lee por partes."},
	},
	{
		ID:       SVC030,
		Severity: Warning,
		Title:    Msg{EN: "No offline map is installed", ES: "No hay ningún mapa sin conexión instalado"},
		Fix:      Msg{EN: "Plug in the ark drive and run `aictl ark import`.", ES: "Conecte la unidad del arca y ejecute `aictl ark import`."},
	},
	{
		ID:       SVC031,
		Severity: Warning,
		Title:    Msg{EN: "Map labels (fonts) are missing", ES: "Faltan las etiquetas del mapa (fuentes)"},
		Fix:      Msg{EN: "Import `map-assets` from the drive: `aictl ark import --item map-assets`.", ES: "Importe `map-assets` desde la unidad: `aictl ark import --item map-assets`."},
	},
	{
		ID:       SVC032,
		Severity: Warning,
		Title:    Msg{EN: "Place search isn't installed", ES: "La búsqueda de lugares no está instalada"},
		Fix:      Msg{EN: "Import `places-central-america`: `aictl ark import --item places-central-america`.", ES: "Importe `places-central-america`: `aictl ark import --item places-central-america`."},
	},
	{
		ID:       SVC033,
		Severity: Info,
		Title:    Msg{EN: "The GPS isn't sending a position", ES: "El GPS no envía una posición"},
		Fix:      Msg{EN: "Move near a window or outside, or set your position by hand on the map.", ES: "Acérquese a una ventana o salga al exterior, o fije su posición a mano en el mapa."},
	},
	{
		ID:       SVC034,
		Severity: Error,
		Title:    Msg{EN: "A map file is damaged", ES: "Un archivo de mapa está dañado"},
		Fix:      Msg{EN: "Run `aictl ark verify --item <map id>`.", ES: "Ejecute `aictl ark verify --item <id del mapa>`."},
	},
	{
		ID:       SVC040,
		Severity: Warning,
		Title:    Msg{EN: "No model that understands photos can run here", ES: "Ningún modelo que entienda fotos puede ejecutarse aquí"},
		Fix:      Msg{EN: "Install a vision model that fits (`aictl models --loadable`).", ES: "Instale un modelo de visión que quepa (`aictl models --loadable`)."},
	},
	{
		ID:       SVC041,
		Severity: Error,
		Title:    Msg{EN: "The vision model is missing its image part (mmproj)", ES: "Al modelo de visión le falta su parte de imagen (mmproj)"},
		Fix:      Msg{EN: "Run `aictl ark verify --item <model id>`; it will be fetched again.", ES: "Ejecute `aictl ark verify --item <id del modelo>`; se obtendrá de nuevo."},
	},
	{
		ID:       SVC050,
		Severity: Info,
		Title:    Msg{EN: "A service was restarted automatically", ES: "Un servicio se reinició automáticamente"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       SVC051,
		Severity: Error,
		Title:    Msg{EN: "A service keeps failing and was left stopped", ES: "Un servicio sigue fallando y se dejó detenido"},
		Fix:      Msg{EN: "Run `aictl doctor` and `aictl logs --unit <unit>`.", ES: "Ejecute `aictl doctor` y `aictl logs --unit <unidad>`."},
	},
	{
		ID:       SVC100,
		Severity: Error,
		Title:    Msg{EN: "The Portal's screens are missing from the program", ES: "Faltan las pantallas del Portal en el programa"},
		Fix:      Msg{EN: "Use the links on this page. Then run `sudo install-node.sh --repair`.", ES: "Use los enlaces de esta página. Luego ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       SVC101,
		Severity: Info,
		Title:    Msg{EN: "Chat is locked outside survival mode", ES: "El chat está bloqueado fuera del modo supervivencia"},
		Fix:      Msg{EN: "Press **Start survival mode**, or log in.", ES: "Pulse **Iniciar modo supervivencia**, o inicie sesión."},
	},
	{
		ID:       SVC102,
		Severity: Error,
		Title:    Msg{EN: "The AI (llama) isn't answering", ES: "La IA (llama) no responde"},
		Fix:      Msg{EN: "Press **Switch brain** and choose one, or restart the computer.", ES: "Pulse **Cambiar de cerebro** y elija uno, o reinicie el equipo."},
	},
	{
		ID:       SVC103,
		Severity: Warning,
		Title:    Msg{EN: "The microphone is blocked or missing", ES: "El micrófono está bloqueado o no existe"},
		Fix:      Msg{EN: "Allow the microphone in the browser (lock icon next to the address), or plug one in.", ES: "Permita el micrófono en el navegador (icono de candado junto a la dirección), o conecte uno."},
	},
	{
		ID:       SVC104,
		Severity: Warning,
		Title:    Msg{EN: "This browser can't record", ES: "Este navegador no puede grabar"},
		Fix:      Msg{EN: "Use the Local AI app or Chromium/Firefox.", ES: "Use la aplicación Local AI o Chromium/Firefox."},
	},
	{
		ID:       SVC105,
		Severity: Warning,
		Title:    Msg{EN: "The recording couldn't be processed", ES: "No se pudo procesar la grabación"},
		Fix:      Msg{EN: "Try again.", ES: "Inténtelo de nuevo."},
	},
	{
		ID:       SVC106,
		Severity: Warning,
		Title:    Msg{EN: "No brain is running", ES: "No hay ningún cerebro en ejecución"},
		Fix:      Msg{EN: "Press **Switch brain** and choose one.", ES: "Pulse **Cambiar de cerebro** y elija uno."},
	},
	{
		ID:       SVC107,
		Severity: Error,
		Title:    Msg{EN: "The new brain couldn't start", ES: "El nuevo cerebro no pudo arrancar"},
		Fix:      Msg{EN: "Choose a smaller brain. The previous one was restarted.", ES: "Elija un cerebro más pequeño. Se reinició el anterior."},
	},
	{
		ID:       SVC108,
		Severity: Warning,
		Title:    Msg{EN: "The photo can't be used", ES: "No se puede usar la foto"},
		Fix:      Msg{EN: "Try another photo (JPEG or PNG, under 30 MB).", ES: "Pruebe con otra foto (JPEG o PNG, de menos de 30 MB)."},
	},
	{
		ID:       SVC109,
		Severity: Warning,
		Title:    Msg{EN: "A first-aid link points to a missing article", ES: "Un enlace de primeros auxilios apunta a un artículo que no existe"},
		Fix:      Msg{EN: "Report it. Use Library search meanwhile.", ES: "Infórmelo. Mientras tanto use la búsqueda de la Biblioteca."},
	},
	{
		ID:       SVC110,
		Severity: Info,
		Title:    Msg{EN: "The Portal session ended", ES: "La sesión del Portal terminó"},
		Fix:      Msg{EN: "Reload the page.", ES: "Recargue la página."},
	},
	{
		ID:       SVC111,
		Severity: Warning,
		Title:    Msg{EN: "Battery low", ES: "Batería baja"},
		Fix:      Msg{EN: "Plug in the charger when you can.", ES: "Conecte el cargador cuando pueda."},
	},
	{
		ID:       SVC112,
		Severity: Critical,
		Title:    Msg{EN: "Battery very low", ES: "Batería muy baja"},
		Fix:      Msg{EN: "Plug in now, or the computer will turn off.", ES: "Conecte el cargador ahora, o el equipo se apagará."},
	},
	{
		ID:       SVC113,
		Severity: Info,
		Title:    Msg{EN: "No graphics card in use; answers are slow", ES: "No se usa ninguna tarjeta gráfica; las respuestas son lentas"},
		Fix:      Msg{EN: "Nothing to do. Short questions work best.", ES: "No hay que hacer nada. Las preguntas cortas funcionan mejor."},
	},
	{
		ID:       SVC114,
		Severity: Warning,
		Title:    Msg{EN: "Too many brain switches; wait a few minutes", ES: "Demasiados cambios de cerebro; espere unos minutos"},
		Fix:      Msg{EN: "Wait a few minutes, then switch again.", ES: "Espere unos minutos y vuelva a cambiar."},
	},
	{
		ID:       SVC115,
		Severity: Warning,
		Title:    Msg{EN: "That chat page isn't available from the Portal", ES: "Esa página del chat no está disponible desde el Portal"},
		Fix:      Msg{EN: "Use the chat normally; settings and downloads are done by an operator in the Local AI app.", ES: "Use el chat con normalidad; los ajustes y las descargas los hace un operador en la aplicación Local AI."},
	},
	{
		ID:       GW001,
		Severity: Error,
		Title:    Msg{EN: "The request body is not valid JSON", ES: "El cuerpo de la solicitud no es JSON válido"},
		Fix:      Msg{EN: "Fix the request body; it must be a JSON object", ES: "Corrija el cuerpo de la solicitud; debe ser un objeto JSON"},
	},
	{
		ID:       GW002,
		Severity: Error,
		Title:    Msg{EN: "The request has no `model`", ES: "La solicitud no tiene `model`"},
		Fix:      Msg{EN: "Add `\"model\": \"<id>\"`; `GET /v1/models` lists the ids", ES: "Añada `\"model\": \"<id>\"`; `GET /v1/models` lista los ids"},
	},
	{
		ID:       GW003,
		Severity: Error,
		Title:    Msg{EN: "A parameter isn't supported on this endpoint", ES: "Un parámetro no se admite en este punto de acceso"},
		Fix:      Msg{EN: "Remove it (for example `stream` on embeddings)", ES: "Quítelo (por ejemplo `stream` en las incrustaciones)"},
	},
	{
		ID:       GW004,
		Severity: Error,
		Title:    Msg{EN: "The request is too large", ES: "La solicitud es demasiado grande"},
		Fix:      Msg{EN: "Send smaller images or audio (limit 8 MB JSON, 25 MB audio)", ES: "Envíe imágenes o audio más pequeños (límite de 8 MB de JSON, 25 MB de audio)"},
	},
	{
		ID:       GW010,
		Severity: Error,
		Title:    Msg{EN: "The API key is missing, wrong, revoked or expired", ES: "La clave de API falta, es incorrecta, está revocada o ha caducado"},
		Fix:      Msg{EN: "Create a new key at ai.marin.cr → API keys and use it as `Authorization: Bearer lai_…`", ES: "Cree una clave nueva en ai.marin.cr → Claves de API y úsela como `Authorization: Bearer lai_…`"},
	},
	{
		ID:       GW011,
		Severity: Warning,
		Title:    Msg{EN: "Too many wrong keys from this address", ES: "Demasiadas claves incorrectas desde esta dirección"},
		Fix:      Msg{EN: "Wait 5 minutes and use a valid key", ES: "Espere 5 minutos y use una clave válida"},
	},
	{
		ID:       GW012,
		Severity: Error,
		Title:    Msg{EN: "This key may not use that model", ES: "Esta clave no puede usar ese modelo"},
		Fix:      Msg{EN: "Use an allowed model, or ask the admin to widen the key", ES: "Use un modelo permitido, o pida al administrador que amplíe la clave"},
	},
	{
		ID:       GW013,
		Severity: Error,
		Title:    Msg{EN: "The key's owner can no longer use the gateway", ES: "El propietario de la clave ya no puede usar la pasarela"},
		Fix:      Msg{EN: "Ask the admin to restore access", ES: "Pida al administrador que restablezca el acceso"},
	},
	{
		ID:       GW020,
		Severity: Error,
		Title:    Msg{EN: "Unknown model", ES: "Modelo desconocido"},
		Fix:      Msg{EN: "Pick an id from `GET /v1/models`", ES: "Elija un id de `GET /v1/models`"},
	},
	{
		ID:       GW021,
		Severity: Error,
		Title:    Msg{EN: "No lab computer can run that model right now", ES: "Ningún equipo del laboratorio puede ejecutar ese modelo ahora mismo"},
		Fix:      Msg{EN: "Try later, pick another model, or ask the admin to load it", ES: "Inténtelo más tarde, elija otro modelo, o pida al administrador que lo cargue"},
	},
	{
		ID:       GW022,
		Severity: Info,
		Title:    Msg{EN: "The model is being loaded", ES: "El modelo se está cargando"},
		Fix:      Msg{EN: "Wait about a minute and try again", ES: "Espere aproximadamente un minuto e inténtelo de nuevo"},
	},
	{
		ID:       GW023,
		Severity: Warning,
		Title:    Msg{EN: "Every computer serving that model is busy", ES: "Todos los equipos que sirven ese modelo están ocupados"},
		Fix:      Msg{EN: "Try again in a few seconds", ES: "Inténtelo de nuevo en unos segundos"},
	},
	{
		ID:       GW024,
		Severity: Error,
		Title:    Msg{EN: "That endpoint isn't offered", ES: "Ese punto de acceso no se ofrece"},
		Fix:      Msg{EN: "Use chat/completions, completions, embeddings or audio/transcriptions", ES: "Use chat/completions, completions, embeddings o audio/transcriptions"},
	},
	{
		ID:       GW030,
		Severity: Warning,
		Title:    Msg{EN: "Too many requests per minute for this key", ES: "Demasiadas solicitudes por minuto para esta clave"},
		Fix:      Msg{EN: "Slow down, or ask the admin for a higher limit", ES: "Vaya más despacio, o pida al administrador un límite mayor"},
	},
	{
		ID:       GW031,
		Severity: Warning,
		Title:    Msg{EN: "Too many requests at the same time for this key", ES: "Demasiadas solicitudes simultáneas para esta clave"},
		Fix:      Msg{EN: "Wait for a running request to finish", ES: "Espere a que termine una solicitud en curso"},
	},
	{
		ID:       GW032,
		Severity: Warning,
		Title:    Msg{EN: "Today's token allowance for this key is used up", ES: "Se agotó la cuota de tokens de hoy para esta clave"},
		Fix:      Msg{EN: "Wait until 00:00 UTC, or ask the admin for a larger budget", ES: "Espere hasta las 00:00 UTC, o pida al administrador un presupuesto mayor"},
	},
	{
		ID:       GW040,
		Severity: Error,
		Title:    Msg{EN: "The gateway couldn't reach the node", ES: "La pasarela no pudo comunicarse con el nodo"},
		Fix:      Msg{EN: "Admin: check the node is online and `https://ai-nN.marin.cr/health` answers", ES: "Administrador: compruebe que el nodo esté en línea y que `https://ai-nN.marin.cr/health` responda"},
	},
	{
		ID:       GW041,
		Severity: Error,
		Title:    Msg{EN: "The node returned an error", ES: "El nodo devolvió un error"},
		Fix:      Msg{EN: "Try again; admin: check the node's llama logs", ES: "Inténtelo de nuevo; administrador: revise los registros de llama del nodo"},
	},
	{
		ID:       GW042,
		Severity: Error,
		Title:    Msg{EN: "The node refused the request (for example the prompt is longer than the model's context)", ES: "El nodo rechazó la solicitud (por ejemplo, el prompt es más largo que el contexto del modelo)"},
		Fix:      Msg{EN: "Shorten the prompt or follow the error message", ES: "Acorte el prompt o siga el mensaje de error"},
	},
	{
		ID:       GW043,
		Severity: Critical,
		Title:    Msg{EN: "The cloud's copy of a node's API key is wrong", ES: "La copia que tiene la nube de la clave de API de un nodo es incorrecta"},
		Fix:      Msg{EN: "Admin: press \"Refresh status\" on that node, or run `sudo aictl cloud resend-key` on it", ES: "Administrador: pulse «Actualizar estado» en ese nodo, o ejecute `sudo aictl cloud resend-key` en él"},
	},
	{
		ID:       GW044,
		Severity: Error,
		Title:    Msg{EN: "The node didn't start answering in time", ES: "El nodo no empezó a responder a tiempo"},
		Fix:      Msg{EN: "Try again; long prompts on big models can take minutes", ES: "Inténtelo de nuevo; los prompts largos en modelos grandes pueden tardar minutos"},
	},
	{
		ID:       GW045,
		Severity: Error,
		Title:    Msg{EN: "The node stopped in the middle of the answer", ES: "El nodo se detuvo a mitad de la respuesta"},
		Fix:      Msg{EN: "Ask again", ES: "Pregunte de nuevo"},
	},
	{
		ID:       GW050,
		Severity: Critical,
		Title:    Msg{EN: "The gateway can't read the stored node keys", ES: "La pasarela no puede leer las claves de nodo guardadas"},
		Fix:      Msg{EN: "Admin: see LAI-CLD-032", ES: "Administrador: vea LAI-CLD-032"},
	},
	{
		ID:       GW051,
		Severity: Warning,
		Title:    Msg{EN: "The gateway is turned off or the site is in maintenance", ES: "La pasarela está desactivada o el sitio está en mantenimiento"},
		Fix:      Msg{EN: "Try later; admin: check `LOCALAI_GATEWAY_ENABLED` and maintenance mode", ES: "Inténtelo más tarde; administrador: compruebe `LOCALAI_GATEWAY_ENABLED` y el modo de mantenimiento"},
	},
	{
		ID:       GW052,
		Severity: Error,
		Title:    Msg{EN: "Unexpected gateway error", ES: "Error inesperado de la pasarela"},
		Fix:      Msg{EN: "Try again; admin: check the API log for the request id", ES: "Inténtelo de nuevo; administrador: revise el registro de la API con el id de solicitud"},
	},
	{
		ID:       GW060,
		Severity: Warning,
		Title:    Msg{EN: "Some usage records couldn't be saved", ES: "No se pudieron guardar algunos registros de uso"},
		Fix:      Msg{EN: "Admin: check the database; requests themselves were not affected", ES: "Administrador: revise la base de datos; las solicitudes en sí no se vieron afectadas"},
	},
	{
		ID:       DSK001,
		Severity: Error,
		Title:    Msg{EN: "This user isn't allowed to control Local AI", ES: "Este usuario no tiene permiso para controlar Local AI"},
		Fix:      Msg{EN: "Run `sudo usermod -aG localai-operators $USER`, then log out and log back in", ES: "Ejecute `sudo usermod -aG localai-operators $USER`, y luego cierre sesión y vuelva a entrar"},
	},
	{
		ID:       DSK002,
		Severity: Warning,
		Title:    Msg{EN: "You were added to the operators group but haven't logged in again", ES: "Se le añadió al grupo de operadores pero no ha vuelto a iniciar sesión"},
		Fix:      Msg{EN: "Log out and log back in once", ES: "Cierre sesión y vuelva a entrar una vez"},
	},
	{
		ID:       DSK003,
		Severity: Error,
		Title:    Msg{EN: "The agent's key file doesn't exist yet", ES: "El archivo de clave del agente aún no existe"},
		Fix:      Msg{EN: "Press \"Start agent\". If it keeps happening, reinstall with `install-node.sh` (runbook 05)", ES: "Pulse «Iniciar agente». Si sigue ocurriendo, reinstale con `install-node.sh` (runbook 05)"},
	},
	{
		ID:       DSK004,
		Severity: Error,
		Title:    Msg{EN: "The Local AI agent is not running", ES: "El agente de Local AI no se está ejecutando"},
		Fix:      Msg{EN: "Press \"Start agent\" and enter the administrator password", ES: "Pulse «Iniciar agente» e introduzca la contraseña de administrador"},
	},
	{
		ID:       DSK005,
		Severity: Error,
		Title:    Msg{EN: "The agent couldn't be started", ES: "No se pudo iniciar el agente"},
		Fix:      Msg{EN: "Open a terminal and run `sudo aictl doctor`; follow what it says", ES: "Abra una terminal y ejecute `sudo aictl doctor`; siga lo que indique"},
	},
	{
		ID:       DSK006,
		Severity: Error,
		Title:    Msg{EN: "ai-desktop and the agent are different versions that don't work together", ES: "ai-desktop y el agente son versiones distintas que no funcionan juntas"},
		Fix:      Msg{EN: "Update both from the rebuild kit or with `sudo aictl update`", ES: "Actualice ambos desde el kit de reconstrucción o con `sudo aictl update`"},
	},
	{
		ID:       DSK007,
		Severity: Error,
		Title:    Msg{EN: "The agent refused this app's login", ES: "El agente rechazó el inicio de sesión de esta aplicación"},
		Fix:      Msg{EN: "Run `sudo aictl token rotate`, then restart ai-desktop. Meanwhile use a browser with `aictl ui-code`", ES: "Ejecute `sudo aictl token rotate` y reinicie ai-desktop. Mientras tanto use un navegador con `aictl ui-code`"},
	},
	{
		ID:       DSK008,
		Severity: Warning,
		Title:    Msg{EN: "No password keyring to protect the lab key", ES: "No hay llavero de contraseñas para proteger la clave del laboratorio"},
		Fix:      Msg{EN: "Accept weaker storage, or set up the GNOME keyring and connect again", ES: "Acepte un almacenamiento más débil, o configure el llavero de GNOME y conéctese de nuevo"},
	},
	{
		ID:       DSK009,
		Severity: Warning,
		Title:    Msg{EN: "The lab connection code expired or was refused", ES: "El código de conexión al laboratorio caducó o fue rechazado"},
		Fix:      Msg{EN: "Press \"Connect to the lab\" again and enter the new code within 15 minutes", ES: "Pulse «Conectar con el laboratorio» otra vez e introduzca el código nuevo en 15 minutos"},
	},
	{
		ID:       DSK010,
		Severity: Info,
		Title:    Msg{EN: "Can't reach the lab", ES: "No se puede llegar al laboratorio"},
		Fix:      Msg{EN: "Nothing to fix here; this computer works normally. The Lab tab retries by itself", ES: "Aquí no hay nada que arreglar; este equipo funciona con normalidad. La pestaña Laboratorio reintenta sola"},
	},
	{
		ID:       DSK011,
		Severity: Warning,
		Title:    Msg{EN: "The lab connection was removed or expired", ES: "La conexión con el laboratorio se eliminó o caducó"},
		Fix:      Msg{EN: "Press \"Connect to the lab\" again", ES: "Pulse «Conectar con el laboratorio» otra vez"},
	},
	{
		ID:       DSK012,
		Severity: Info,
		Title:    Msg{EN: "The app blocked a connection to the internet", ES: "La aplicación bloqueó una conexión a internet"},
		Fix:      Msg{EN: "Nothing to do: this is the privacy protection working", ES: "No hay que hacer nada: es la protección de privacidad funcionando"},
	},
	{
		ID:       DSK013,
		Severity: Warning,
		Title:    Msg{EN: "The system tray isn't available", ES: "La bandeja del sistema no está disponible"},
		Fix:      Msg{EN: "Turn on \"AppIndicator and KStatusNotifierItem Support\" in Extensions, then restart ai-desktop", ES: "Active «AppIndicator and KStatusNotifierItem Support» en Extensiones y reinicie ai-desktop"},
	},
	{
		ID:       DSK014,
		Severity: Warning,
		Title:    Msg{EN: "Couldn't turn on \"Start at login\"", ES: "No se pudo activar «Iniciar al arrancar la sesión»"},
		Fix:      Msg{EN: "Check that `~/.config/autostart` is writable, then try again", ES: "Compruebe que `~/.config/autostart` tenga permiso de escritura e inténtelo de nuevo"},
	},
	{
		ID:       DSK015,
		Severity: Critical,
		Title:    Msg{EN: "The app's security sandbox couldn't start", ES: "No se pudo iniciar el entorno aislado de seguridad de la aplicación"},
		Fix:      Msg{EN: "Reinstall ai-desktop from the .deb (`sudo apt install ./ai-desktop_<version>_amd64.deb`)", ES: "Reinstale ai-desktop desde el .deb (`sudo apt install ./ai-desktop_<versión>_amd64.deb`)"},
	},
	{
		ID:       DSK016,
		Severity: Warning,
		Title:    Msg{EN: "Couldn't check or download an update", ES: "No se pudo buscar o descargar una actualización"},
		Fix:      Msg{EN: "Try later, or install the update from the rebuild kit", ES: "Inténtelo más tarde, o instale la actualización desde el kit de reconstrucción"},
	},
	{
		ID:       INS001,
		Severity: Error,
		Title:    Msg{EN: "Unsupported Ubuntu version or architecture", ES: "Versión de Ubuntu o arquitectura no admitida"},
		Fix:      Msg{EN: "Install Ubuntu 24.04 or 26.04 LTS (amd64 or arm64).", ES: "Instale Ubuntu 24.04 o 26.04 LTS (amd64 o arm64)."},
	},
	{
		ID:       INS002,
		Severity: Error,
		Title:    Msg{EN: "Running inside a container", ES: "Se ejecuta dentro de un contenedor"},
		Fix:      Msg{EN: "Run the installer on the real machine.", ES: "Ejecute el instalador en la máquina real."},
	},
	{
		ID:       INS003,
		Severity: Error,
		Title:    Msg{EN: "Not run with sudo from a normal user", ES: "No se ejecutó con sudo desde un usuario normal"},
		Fix:      Msg{EN: "Run `sudo bash install-node.sh …` from the desktop user's terminal.", ES: "Ejecute `sudo bash install-node.sh …` desde la terminal del usuario del escritorio."},
	},
	{
		ID:       INS004,
		Severity: Error,
		Title:    Msg{EN: "Another installer is running", ES: "Se está ejecutando otro instalador"},
		Fix:      Msg{EN: "Wait for it to finish.", ES: "Espere a que termine."},
	},
	{
		ID:       INS010,
		Severity: Error,
		Title:    Msg{EN: "NVIDIA GPU found but its driver doesn't work", ES: "Se encontró una GPU NVIDIA pero su controlador no funciona"},
		Fix:      Msg{EN: "Accept the driver install and reboot, or continue without GPU.", ES: "Acepte la instalación del controlador y reinicie, o continúe sin GPU."},
	},
	{
		ID:       INS011,
		Severity: Warning,
		Title:    Msg{EN: "Secure Boot may block the NVIDIA driver", ES: "Secure Boot puede bloquear el controlador de NVIDIA"},
		Fix:      Msg{EN: "At the next boot choose \"Enroll MOK\" and type the password you set.", ES: "En el próximo arranque elija «Enroll MOK» e introduzca la contraseña que definió."},
	},
	{
		ID:       INS012,
		Severity: Warning,
		Title:    Msg{EN: "Less than 8 GB of RAM", ES: "Menos de 8 GB de RAM"},
		Fix:      Msg{EN: "Only the cpu-only profile will work well.", ES: "Solo funcionará bien el perfil cpu-only."},
	},
	{
		ID:       INS013,
		Severity: Error,
		Title:    Msg{EN: "Not enough free disk space", ES: "No hay suficiente espacio libre en disco"},
		Fix:      Msg{EN: "Free space or use `--yes` to accept a smaller budget.", ES: "Libere espacio o use `--yes` para aceptar un presupuesto menor."},
	},
	{
		ID:       INS014,
		Severity: Error,
		Title:    Msg{EN: "A needed port is in use", ES: "Un puerto necesario está en uso"},
		Fix:      Msg{EN: "Stop the program shown, then re-run.", ES: "Detenga el programa indicado y vuelva a ejecutar."},
	},
	{
		ID:       INS015,
		Severity: Error,
		Title:    Msg{EN: "No internet for an online install", ES: "Sin internet para una instalación en línea"},
		Fix:      Msg{EN: "Connect to the internet, or use `--from-ark <drive>`.", ES: "Conéctese a internet, o use `--from-ark <unidad>`."},
	},
	{
		ID:       INS016,
		Severity: Error,
		Title:    Msg{EN: "The ark drive lacks files for this computer", ES: "A la unidad del arca le faltan archivos para este equipo"},
		Fix:      Msg{EN: "Sync the drive on a computer with internet first.", ES: "Sincronice primero la unidad en un equipo con internet."},
	},
	{
		ID:       INS017,
		Severity: Warning,
		Title:    Msg{EN: "The clock is wrong", ES: "El reloj es incorrecto"},
		Fix:      Msg{EN: "Set the date and time, then re-run.", ES: "Ponga la fecha y la hora y vuelva a ejecutar."},
	},
	{
		ID:       INS020,
		Severity: Error,
		Title:    Msg{EN: "This computer already has a different node id", ES: "Este equipo ya tiene un id de nodo distinto"},
		Fix:      Msg{EN: "Keep the old id, or uninstall with `--purge-config` first.", ES: "Conserve el id antiguo, o desinstale antes con `--purge-config`."},
	},
	{
		ID:       INS021,
		Severity: Critical,
		Title:    Msg{EN: "The agent download failed its signature check", ES: "La descarga del agente no superó la comprobación de firma"},
		Fix:      Msg{EN: "Download again; if it repeats, don't install and report it.", ES: "Descargue de nuevo; si se repite, no lo instale e infórmelo."},
	},
	{
		ID:       INS030,
		Severity: Error,
		Title:    Msg{EN: "Package installation failed", ES: "Falló la instalación de paquetes"},
		Fix:      Msg{EN: "Read the apt messages above, fix, and re-run.", ES: "Lea los mensajes de apt de más arriba, corrija y vuelva a ejecutar."},
	},
	{
		ID:       INS031,
		Severity: Error,
		Title:    Msg{EN: "The old llama-qwen service is still using port 8080", ES: "El antiguo servicio llama-qwen sigue usando el puerto 8080"},
		Fix:      Msg{EN: "Re-run without `--no-migrate`.", ES: "Vuelva a ejecutar sin `--no-migrate`."},
	},
	{
		ID:       INS032,
		Severity: Error,
		Title:    Msg{EN: "Building llama.cpp failed", ES: "Falló la compilación de llama.cpp"},
		Fix:      Msg{EN: "Read `build.log` in the failed build folder under `/opt/localai/llama.cpp/builds/` (named in the message); run `sudo install-node.sh --repair`.", ES: "Lea `build.log` en la carpeta de compilación fallida de `/opt/localai/llama.cpp/builds/` (indicada en el mensaje); ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       INS033,
		Severity: Error,
		Title:    Msg{EN: "Migration failed and was rolled back", ES: "La migración falló y se deshizo"},
		Fix:      Msg{EN: "The old service is running again. Read the log and report it.", ES: "El servicio antiguo vuelve a estar en marcha. Lea el registro e infórmelo."},
	},
	{
		ID:       INS034,
		Severity: Critical,
		Title:    Msg{EN: "The agent didn't start", ES: "El agente no arrancó"},
		Fix:      Msg{EN: "Run `journalctl -u localai-agent -n 50` and `sudo install-node.sh --repair`.", ES: "Ejecute `journalctl -u localai-agent -n 50` y `sudo install-node.sh --repair`."},
	},
	{
		ID:       INS040,
		Severity: Warning,
		Title:    Msg{EN: "Lid/power-key settings aren't active", ES: "Los ajustes de la tapa y del botón de encendido no están activos"},
		Fix:      Msg{EN: "Reboot the computer (don't restart logind).", ES: "Reinicie el equipo (no reinicie logind)."},
	},
	{
		ID:       INS041,
		Severity: Warning,
		Title:    Msg{EN: "Sleep targets aren't masked", ES: "Los destinos de suspensión no están enmascarados"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`.", ES: "Ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       INS042,
		Severity: Warning,
		Title:    Msg{EN: "GNOME idle suspend is still on", ES: "La suspensión por inactividad de GNOME sigue activada"},
		Fix:      Msg{EN: "Log in as the desktop user and run `sudo install-node.sh --repair`.", ES: "Inicie sesión como usuario del escritorio y ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       INS043,
		Severity: Warning,
		Title:    Msg{EN: "Power profile isn't \"performance\"", ES: "El perfil de energía no es «performance»"},
		Fix:      Msg{EN: "Run `powerprofilesctl set performance`.", ES: "Ejecute `powerprofilesctl set performance`."},
	},
	{
		ID:       INS044,
		Severity: Warning,
		Title:    Msg{EN: "Wi-Fi power save is on", ES: "El ahorro de energía del Wi-Fi está activado"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`; use Ethernet if possible.", ES: "Ejecute `sudo install-node.sh --repair`; use Ethernet si es posible."},
	},
	{
		ID:       INS050,
		Severity: Error,
		Title:    Msg{EN: "Tailscale serve pattern doesn't match the inventory", ES: "El patrón de Tailscale Serve no coincide con el inventario"},
		Fix:      Msg{EN: "Use `--serve-pattern` with the value in the inventory.", ES: "Use `--serve-pattern` con el valor del inventario."},
	},
	{
		ID:       INS060,
		Severity: Warning,
		Title:    Msg{EN: "The admin PIN wasn't set", ES: "No se definió el PIN de administrador"},
		Fix:      Msg{EN: "Run `sudo aictl pin set`.", ES: "Ejecute `sudo aictl pin set`."},
	},
	{
		ID:       INS070,
		Severity: Info,
		Title:    Msg{EN: "A reboot is needed to finish the install", ES: "Hace falta reiniciar para terminar la instalación"},
		Fix:      Msg{EN: "Reboot when convenient.", ES: "Reinicie cuando le convenga."},
	},
	{
		ID:       KIT001,
		Severity: Error,
		Title:    Msg{EN: "A source package in the kit is damaged", ES: "Un paquete fuente del kit está dañado"},
		Fix:      Msg{EN: "Run `aictl kit verify --full`; sync the drive on a computer with internet.", ES: "Ejecute `aictl kit verify --full`; sincronice la unidad en un equipo con internet."},
	},
	{
		ID:       KIT002,
		Severity: Error,
		Title:    Msg{EN: "The kit doesn't have the needed llama.cpp version", ES: "El kit no tiene la versión de llama.cpp necesaria"},
		Fix:      Msg{EN: "Sync the kit with internet, or choose a commit that is in the kit.", ES: "Sincronice el kit con internet, o elija un commit que esté en el kit."},
	},
	{
		ID:       KIT010,
		Severity: Error,
		Title:    Msg{EN: "Build tools are missing", ES: "Faltan herramientas de compilación"},
		Fix:      Msg{EN: "Run `sudo aictl kit install-debs`.", ES: "Ejecute `sudo aictl kit install-debs`."},
	},
	{
		ID:       KIT011,
		Severity: Error,
		Title:    Msg{EN: "The CUDA compiler is missing or doesn't match the driver", ES: "Falta el compilador de CUDA o no coincide con el controlador"},
		Fix:      Msg{EN: "Run `sudo aictl kit install-debs`, or rebuild with `--backend vulkan`.", ES: "Ejecute `sudo aictl kit install-debs`, o recompile con `--backend vulkan`."},
	},
	{
		ID:       KIT012,
		Severity: Error,
		Title:    Msg{EN: "No real Vulkan graphics device was found", ES: "No se encontró ningún dispositivo gráfico Vulkan real"},
		Fix:      Msg{EN: "Install the graphics driver from the kit, or use `--backend cpu`.", ES: "Instale el controlador gráfico desde el kit, o use `--backend cpu`."},
	},
	{
		ID:       KIT013,
		Severity: Error,
		Title:    Msg{EN: "The NVIDIA driver isn't loaded", ES: "El controlador de NVIDIA no está cargado"},
		Fix:      Msg{EN: "Restart and choose the previous kernel in \"Advanced options\", or install the driver from the kit.", ES: "Reinicie y elija el kernel anterior en «Opciones avanzadas», o instale el controlador desde el kit."},
	},
	{
		ID:       KIT014,
		Severity: Error,
		Title:    Msg{EN: "Not enough disk space to build", ES: "No hay suficiente espacio en disco para compilar"},
		Fix:      Msg{EN: "Free 5 GB and try again.", ES: "Libere 5 GB e inténtelo de nuevo."},
	},
	{
		ID:       KIT020,
		Severity: Error,
		Title:    Msg{EN: "The build failed", ES: "Falló la compilación"},
		Fix:      Msg{EN: "Read the build log shown; run `aictl rebuild-llama --dry-run`.", ES: "Lea el registro de compilación mostrado; ejecute `aictl rebuild-llama --dry-run`."},
	},
	{
		ID:       KIT021,
		Severity: Error,
		Title:    Msg{EN: "The new build doesn't work; the old one is still in use", ES: "La nueva compilación no funciona; se sigue usando la anterior"},
		Fix:      Msg{EN: "Nothing breaks. Report the log.", ES: "No se rompe nada. Informe del registro."},
	},
	{
		ID:       KIT022,
		Severity: Retired,
		Title:    Msg{EN: "The new build failed after switching; the old one was restored (replaced by `LAI-LLM-019`)", ES: "La nueva compilación falló tras el cambio; se restauró la anterior (sustituido por `LAI-LLM-019`)"},
		Fix:      Msg{EN: "No longer used.", ES: "Ya no se usa."},
	},
	{
		ID:       KIT030,
		Severity: Error,
		Title:    Msg{EN: "The agent source in the kit isn't complete", ES: "El código fuente del agente en el kit no está completo"},
		Fix:      Msg{EN: "Sync the kit with internet.", ES: "Sincronice el kit con internet."},
	},
	{
		ID:       KIT040,
		Severity: Warning,
		Title:    Msg{EN: "The rebuild kit is missing parts for this computer", ES: "Al kit de reconstrucción le faltan partes para este equipo"},
		Fix:      Msg{EN: "Run `aictl ark sync` with internet, or `aictl ark import` from a good drive.", ES: "Ejecute `aictl ark sync` con internet, o `aictl ark import` desde una unidad buena."},
	},
	{
		ID:       KIT041,
		Severity: Warning,
		Title:    Msg{EN: "The ark drive lacks kit parts for another lab computer", ES: "A la unidad del arca le faltan partes del kit para otro equipo del laboratorio"},
		Fix:      Msg{EN: "Plug the drive into that computer while online, or sync it here.", ES: "Conecte la unidad a ese equipo estando en línea, o sincronícela aquí."},
	},
	{
		ID:       KIT042,
		Severity: Error,
		Title:    Msg{EN: "The kit is for a different Ubuntu version", ES: "El kit es de otra versión de Ubuntu"},
		Fix:      Msg{EN: "Sync the kit after the upgrade, with internet.", ES: "Sincronice el kit tras la actualización, con internet."},
	},
	{
		ID:       KIT043,
		Severity: Warning,
		Title:    Msg{EN: "The kit's CUDA version doesn't fit this driver", ES: "La versión de CUDA del kit no encaja con este controlador"},
		Fix:      Msg{EN: "Sync the kit with internet; meanwhile use the prebuilt or Vulkan.", ES: "Sincronice el kit con internet; mientras tanto use la compilación precompilada o Vulkan."},
	},
	{
		ID:       KIT044,
		Severity: Critical,
		Title:    Msg{EN: "The kit's agent files failed their signature check", ES: "Los archivos del agente del kit no superaron la comprobación de firma"},
		Fix:      Msg{EN: "Don't install them. Use another drive and report it.", ES: "No los instale. Use otra unidad e infórmelo."},
	},
	{
		ID:       DRL001,
		Severity: Warning,
		Title:    Msg{EN: "The last drill found problems", ES: "El último simulacro encontró problemas"},
		Fix:      Msg{EN: "Open Status, or run `aictl drill show`, and follow each fix.", ES: "Abra Estado, o ejecute `aictl drill show`, y siga cada solución."},
	},
	{
		ID:       DRL002,
		Severity: Info,
		Title:    Msg{EN: "The drill waited because a sync was running", ES: "El simulacro esperó porque había una sincronización en curso"},
		Fix:      Msg{EN: "Nothing to do. It runs later.", ES: "No hay que hacer nada. Se ejecutará más tarde."},
	},
	{
		ID:       DRL003,
		Severity: Info,
		Title:    Msg{EN: "The drill doesn't run in survival mode", ES: "El simulacro no se ejecuta en modo supervivencia"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       DRL004,
		Severity: Info,
		Title:    Msg{EN: "The drill waited because the AI was in use", ES: "El simulacro esperó porque la IA estaba en uso"},
		Fix:      Msg{EN: "Nothing to do.", ES: "No hay que hacer nada."},
	},
	{
		ID:       DRL005,
		Severity: Info,
		Title:    Msg{EN: "The drill skipped: battery below 50 %", ES: "Simulacro omitido: batería por debajo del 50 %"},
		Fix:      Msg{EN: "Plug in the charger.", ES: "Conecte el cargador."},
	},
	{
		ID:       DRL006,
		Severity: Warning,
		Title:    Msg{EN: "No drill in 45 days", ES: "Ningún simulacro en 45 días"},
		Fix:      Msg{EN: "Run `aictl drill`. Check that `localai-drill.timer` is enabled.", ES: "Ejecute `aictl drill`. Compruebe que `localai-drill.timer` esté habilitado."},
	},
	{
		ID:       DRL010,
		Severity: Error,
		Title:    Msg{EN: "A setting still points to the internet", ES: "Un ajuste sigue apuntando a internet"},
		Fix:      Msg{EN: "Run `aictl doctor`; the details name the setting.", ES: "Ejecute `aictl doctor`; los detalles nombran el ajuste."},
	},
	{
		ID:       DRL011,
		Severity: Error,
		Title:    Msg{EN: "Essential content is missing", ES: "Falta contenido esencial"},
		Fix:      Msg{EN: "Run `aictl ark sync` with internet, or `aictl ark import`.", ES: "Ejecute `aictl ark sync` con internet, o `aictl ark import`."},
	},
	{
		ID:       DRL012,
		Severity: Error,
		Title:    Msg{EN: "Stored files failed their check", ES: "Los archivos guardados no superaron su comprobación"},
		Fix:      Msg{EN: "Run `aictl ark verify --full`.", ES: "Ejecute `aictl ark verify --full`."},
	},
	{
		ID:       DRL020,
		Severity: Error,
		Title:    Msg{EN: "A survival brain couldn't start", ES: "Un cerebro de supervivencia no pudo arrancar"},
		Fix:      Msg{EN: "Run `aictl doctor`; choose a smaller brain if it doesn't fit.", ES: "Ejecute `aictl doctor`; elija un cerebro más pequeño si no cabe."},
	},
	{
		ID:       DRL021,
		Severity: Warning,
		Title:    Msg{EN: "A brain is slower than expected", ES: "Un cerebro es más lento de lo esperado"},
		Fix:      Msg{EN: "Check the computer is plugged in and not overheating.", ES: "Compruebe que el equipo esté enchufado y no se sobrecaliente."},
	},
	{
		ID:       DRL022,
		Severity: Error,
		Title:    Msg{EN: "A brain gave wrong answers to a simple test", ES: "Un cerebro dio respuestas incorrectas en una prueba sencilla"},
		Fix:      Msg{EN: "Run `aictl ark verify --item <model>`; report it if it repeats.", ES: "Ejecute `aictl ark verify --item <modelo>`; infórmelo si se repite."},
	},
	{
		ID:       DRL023,
		Severity: Error,
		Title:    Msg{EN: "The photo brain didn't read the test image", ES: "El cerebro de fotos no leyó la imagen de prueba"},
		Fix:      Msg{EN: "Run `aictl ark verify --item <model>`; report it.", ES: "Ejecute `aictl ark verify --item <modelo>`; infórmelo."},
	},
	{
		ID:       DRL024,
		Severity: Error,
		Title:    Msg{EN: "Survival brains don't fit on this computer", ES: "Los cerebros de supervivencia no caben en este equipo"},
		Fix:      Msg{EN: "Change the profile's survival models (`aictl profile`).", ES: "Cambie los modelos de supervivencia del perfil (`aictl profile`)."},
	},
	{
		ID:       DRL030,
		Severity: Error,
		Title:    Msg{EN: "Library search doesn't work", ES: "La búsqueda en la biblioteca no funciona"},
		Fix:      Msg{EN: "Run `aictl services restart kiwix`.", ES: "Ejecute `aictl services restart kiwix`."},
	},
	{
		ID:       DRL031,
		Severity: Warning,
		Title:    Msg{EN: "Speech recognition is less accurate than expected", ES: "El reconocimiento de voz es menos preciso de lo esperado"},
		Fix:      Msg{EN: "Nothing urgent. A bigger whisper model helps.", ES: "Nada urgente. Un modelo whisper más grande ayuda."},
	},
	{
		ID:       DRL032,
		Severity: Error,
		Title:    Msg{EN: "Speech recognition failed", ES: "Falló el reconocimiento de voz"},
		Fix:      Msg{EN: "Run `aictl services restart whisper`.", ES: "Ejecute `aictl services restart whisper`."},
	},
	{
		ID:       DRL033,
		Severity: Warning,
		Title:    Msg{EN: "A language has no voice of its own", ES: "Un idioma no tiene voz propia"},
		Fix:      Msg{EN: "Install a voice for it with `aictl ark sync`.", ES: "Instale una voz para él con `aictl ark sync`."},
	},
	{
		ID:       DRL034,
		Severity: Error,
		Title:    Msg{EN: "The voice failed", ES: "Falló la voz"},
		Fix:      Msg{EN: "Run `aictl doctor`.", ES: "Ejecute `aictl doctor`."},
	},
	{
		ID:       DRL040,
		Severity: Error,
		Title:    Msg{EN: "Maps don't work", ES: "Los mapas no funcionan"},
		Fix:      Msg{EN: "Run `aictl ark verify --item <map>`.", ES: "Ejecute `aictl ark verify --item <mapa>`."},
	},
	{
		ID:       DRL050,
		Severity: Error,
		Title:    Msg{EN: "The Portal or chat page doesn't work", ES: "La página del Portal o del chat no funciona"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`.", ES: "Ejecute `sudo install-node.sh --repair`."},
	},
	{
		ID:       DRL060,
		Severity: Warning,
		Title:    Msg{EN: "Manuals are missing", ES: "Faltan los manuales"},
		Fix:      Msg{EN: "Run `aictl ark sync --item docs-bundle`.", ES: "Ejecute `aictl ark sync --item docs-bundle`."},
	},
	{
		ID:       DRL070,
		Severity: Error,
		Title:    Msg{EN: "The rebuild kit is incomplete for this computer", ES: "El kit de reconstrucción está incompleto para este equipo"},
		Fix:      Msg{EN: "Run `aictl kit status` and sync the missing parts.", ES: "Ejecute `aictl kit status` y sincronice las partes que faltan."},
	},
	{
		ID:       DRL071,
		Severity: Error,
		Title:    Msg{EN: "llama.cpp could not be rebuilt offline", ES: "No se pudo recompilar llama.cpp sin conexión"},
		Fix:      Msg{EN: "Run `aictl rebuild-llama --dry-run` and fix what it lists.", ES: "Ejecute `aictl rebuild-llama --dry-run` y corrija lo que lista."},
	},
	{
		ID:       DRL080,
		Severity: Warning,
		Title:    Msg{EN: "Laptop power settings aren't right", ES: "Los ajustes de energía del portátil no son correctos"},
		Fix:      Msg{EN: "Run `sudo install-node.sh --repair`, then reboot.", ES: "Ejecute `sudo install-node.sh --repair` y luego reinicie."},
	},
	{
		ID:       DRL081,
		Severity: Warning,
		Title:    Msg{EN: "The clock looks wrong", ES: "El reloj parece incorrecto"},
		Fix:      Msg{EN: "Set the date and time in Settings.", ES: "Ponga la fecha y la hora en Ajustes."},
	},
	{
		ID:       DRL090,
		Severity: Error,
		Title:    Msg{EN: "The drill couldn't restore the normal brain", ES: "El simulacro no pudo restaurar el cerebro habitual"},
		Fix:      Msg{EN: "Press **Switch brain** and choose the usual one.", ES: "Pulse **Cambiar de cerebro** y elija el de siempre."},
	},
	{
		ID:       DRL091,
		Severity: Warning,
		Title:    Msg{EN: "Something tried to use the internet during the drill", ES: "Algo intentó usar internet durante el simulacro"},
		Fix:      Msg{EN: "Report the details; offline use may be affected.", ES: "Informe de los detalles; el uso sin conexión puede verse afectado."},
	},
}
