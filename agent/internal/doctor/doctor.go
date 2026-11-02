// Package doctor is the agent's doctor framework (agent.md section 4.13): the code table (codes.go),
// the check registry and runner, and the alert manager that turns the results of the fast checks
// into alerts and alert.raised / alert.cleared events.
//
// The package is a leaf for the rest of the agent: it imports only internal/events. That is what
// lets every other package (config, catalog, audit, api, ...) take its doctor codes from here
// without an import cycle: config.CodeNodeYAMLMissing is doctor.AGT001, and scripts/check-doctor-codes.sh
// refuses a quoted code literal anywhere else. Domain packages register their checks with Register
// (a Check is a function over interfaces the domain package owns), and the sources of the built-in
// agt.* checks are plain data (see checks_agt.go and the sources package).
package doctor

import (
	"regexp"
	"strings"
)

// Severity of a code or a result (doctor-codes.md section 1.4). The order is
// info < warning < error < critical; Retired marks a code that is no longer emitted.
type Severity string

// The severities.
const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Error    Severity = "error"
	Critical Severity = "critical"
	// Retired is the registry severity "retired (vX.Y)": the code stays in the table, never reused,
	// and can no longer be raised or returned by a check.
	Retired Severity = "retired"
)

// Rank orders severities: info 1, warning 2, error 3, critical 4. Anything else is 0.
func (s Severity) Rank() int {
	switch s {
	case Info:
		return 1
	case Warning:
		return 2
	case Error:
		return 3
	case Critical:
		return 4
	}
	return 0
}

// Valid reports whether s is one of the four severities a result may carry (not Retired).
func (s Severity) Valid() bool { return s.Rank() > 0 }

// Languages of the code texts. Codes and field names are never translated (doctor-codes.md 1.1).
const (
	LangEN = "en"
	LangES = "es"
)

// Msg is one text in both languages.
type Msg struct{ EN, ES string }

// In returns the text in lang ("es" or "en"); any other language gets English.
func (m Msg) In(lang string) string {
	if lang == LangES && m.ES != "" {
		return m.ES
	}
	return m.EN
}

// Code is one entry of the code table.
type Code struct {
	// ID is the code, LAI-<AREA>-<NNN>.
	ID string
	// Severity is the default severity. A few codes escalate or relax (the registry "Sev" column
	// says when); the source of the result passes the effective one.
	Severity Severity
	// Title says what happened, in one sentence (at most 120 characters).
	Title Msg
	// Fix says what to do, in one or two sentences.
	Fix Msg
}

// CodePattern is the format of a code (doctor-codes.md section 1.1).
var CodePattern = regexp.MustCompile(`^LAI-(AGT|CLI|LLM|CAT|SYN|ARK|SVC|NET|CLD|GW|DSK|INS|KIT|DRL|SEC|HW)-[0-9]{3}$`)

var index = func() map[string]*Code {
	m := make(map[string]*Code, len(table))
	for i := range table {
		m[table[i].ID] = &table[i]
	}
	return m
}()

// Lookup returns the table entry of code.
func Lookup(code string) (Code, bool) {
	c, ok := index[code]
	if !ok {
		return Code{}, false
	}
	return *c, true
}

// Text returns the title and the fix of code in lang ("es" or "en"; anything else is English). ok
// is false for a code the table does not have.
func Text(code, lang string) (title, fix string, ok bool) {
	c, found := index[code]
	if !found {
		return "", "", false
	}
	return c.Title.In(lang), c.Fix.In(lang), true
}

// Codes returns the whole table in registry order. The caller owns the slice.
func Codes() []Code {
	out := make([]Code, len(table))
	copy(out, table)
	return out
}

// Emittable reports whether code may be raised or returned by a check: it is in the table and not
// retired.
func Emittable(code string) bool {
	c, ok := index[code]
	return ok && c.Severity != Retired
}

// Area returns the area prefix of a code ("HW" for LAI-HW-012), or "" when it has none.
func Area(code string) string {
	parts := strings.Split(code, "-")
	if len(parts) != 3 || parts[0] != "LAI" {
		return ""
	}
	return parts[1]
}

// Areas returns the area prefixes (doctor-codes.md section 1.3), sorted.
func Areas() []string {
	return []string{"AGT", "ARK", "CAT", "CLD", "CLI", "DRL", "DSK", "GW", "HW", "INS", "KIT", "LLM", "NET", "SEC", "SVC", "SYN"}
}

// ValidArea reports whether area is one of Areas (case-sensitive, upper case).
func ValidArea(area string) bool {
	for _, a := range Areas() {
		if a == area {
			return true
		}
	}
	return false
}
