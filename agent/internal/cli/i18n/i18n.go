// Package i18n holds the Spanish and English strings of the aictl output layer (aictl.md section
// 3.3). Codes, flag names and JSON field names are never translated; only sentences are.
//
// The doctor-code messages and fixes of aictl itself stay in the cli package next to the codes
// (codes.go); this package carries the labels and phrases the output layer prints around them.
package i18n

import (
	"fmt"
	"strings"
)

// Lang is the language of human output: "en" or "es".
type Lang string

// Supported languages.
const (
	EN Lang = "en"
	ES Lang = "es"
)

// Valid reports whether l is a supported language.
func (l Lang) Valid() bool { return l == EN || l == ES }

// Detect returns the language the environment asks for: Spanish when $LANG starts with "es" (for
// example es_CR.UTF-8), English otherwise. --lang, when given, wins over this and is applied by the
// caller.
func Detect(getenv func(string) string) Lang {
	if getenv != nil && strings.HasPrefix(strings.ToLower(getenv("LANG")), "es") {
		return ES
	}
	return EN
}

// Key names one string of the tables.
type Key string

// T returns the string for k in l, formatted with args when there are any. An unknown key returns
// the key itself, so a missing string shows up in output and in the table test, never as a blank.
func T(l Lang, k Key, args ...any) string {
	tbl := en
	if l == ES {
		tbl = es
	}
	s, ok := tbl[k]
	if !ok {
		s = string(k)
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Keys returns every key of the English table (the reference); tests compare it with Spanish.
func Keys() []Key {
	out := make([]Key, 0, len(en))
	for k := range en {
		out = append(out, k)
	}
	return out
}

// Plural returns the unit word for n: unit+".one" when n is 1, unit+".other" otherwise.
func Plural(l Lang, unit Key, n int64) string {
	if n == 1 {
		return T(l, unit+".one")
	}
	return T(l, unit+".other")
}
