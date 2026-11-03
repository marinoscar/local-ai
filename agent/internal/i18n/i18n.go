// Package i18n holds the English and Spanish sentences the agent itself hands to a client in an
// event, such as the text of the desktop notification after a scheduled drill (drill.md section
// 4.5). The tables are the embedded en.json and es.json: one flat object of key to sentence. Doctor
// codes keep their own tables (internal/doctor/codes.go) and the aictl output layer its own
// (internal/cli/i18n); this package is only for text the agent publishes.
//
// Every key exists in both files; the test of this package fails when one has a key the other lacks
// or a blank sentence.
package i18n

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed en.json es.json
var files embed.FS

// Lang is a language of the tables.
type Lang string

// The languages.
const (
	EN Lang = "en"
	ES Lang = "es"
)

var (
	once   sync.Once
	tables map[Lang]map[string]string
)

func load() {
	tables = map[Lang]map[string]string{}
	for lang, name := range map[Lang]string{EN: "en.json", ES: "es.json"} {
		m := map[string]string{}
		if raw, err := files.ReadFile(name); err == nil {
			// An unreadable table is left empty: T then returns the key, which the test of this package
			// and any output make obvious, instead of a blank.
			_ = json.Unmarshal(raw, &m)
		}
		tables[lang] = m
	}
}

// T returns the sentence of key in lang ("es" gives Spanish, anything else English). An unknown key
// returns the key itself, so a missing sentence shows up instead of a blank.
func T(lang Lang, key string) string {
	once.Do(load)
	t := tables[EN]
	if strings.EqualFold(string(lang), string(ES)) {
		t = tables[ES]
	}
	if s, ok := t[key]; ok && s != "" {
		return s
	}
	return key
}

// Keys returns the keys of the English table in order; the other table must have the same ones.
func Keys(lang Lang) []string {
	once.Do(load)
	out := make([]string, 0, len(tables[lang]))
	for k := range tables[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
