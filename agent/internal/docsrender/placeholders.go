// Package docsrender turns the Markdown documentation tree into the offline manuals that
// `aictl docs render` writes (docs/specs/aictl.md section 4.22): one self-contained HTML page per
// document (embedded print CSS, no network assets), an index, optional PDFs made by a locally
// installed Chromium, and the one-page survival card of this node.
//
// Everything here works without the internet. The package never prints doctor codes: it returns
// typed errors and warnings and the CLI layer maps them to codes.
package docsrender

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// placeholderRE is the placeholder syntax of the public docs: <UPPER_SNAKE_CASE> (aictl.md 4.22).
var placeholderRE = regexp.MustCompile(`<([A-Z][A-Z0-9_]{1,63})>`)

// wholePlaceholderRE matches a value that is nothing but one placeholder (an inventory.example.yaml scalar).
var wholePlaceholderRE = regexp.MustCompile(`^<([A-Z][A-Z0-9_]{1,63})>$`)

// FindPlaceholders returns the distinct placeholder names in src, sorted.
func FindPlaceholders(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range placeholderRE.FindAllStringSubmatch(src, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Sentinels. A placeholder cannot stay in the Markdown source as "<NAME>" (the parser would read it
// as a raw HTML tag and drop it) and a filled value must not be parsed as Markdown. So every
// placeholder is replaced by a private-use token before parsing and swapped for the escaped value
// (or the escaped "<NAME>") in the finished HTML. This also covers fenced code blocks, where the
// runbooks put commands.
const (
	sentOpen  = ""
	sentClose = ""
)

var sentinelRE = regexp.MustCompile(sentOpen + `([0-9]+)` + sentClose)

type substitution struct {
	name   string
	value  string
	filled bool
}

// substitutions remembers what each sentinel of one document stands for.
type substitutions struct {
	items []substitution
}

// protect replaces every placeholder in src by a sentinel. values maps a name to its value; a name
// that is not there (or whose value is empty) stays a placeholder.
func protect(src string, values map[string]string) (string, *substitutions) {
	subs := &substitutions{}
	out := placeholderRE.ReplaceAllStringFunc(src, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := values[name]
		subs.items = append(subs.items, substitution{name: name, value: v, filled: ok && v != ""})
		return sentOpen + strconv.Itoa(len(subs.items)-1) + sentClose
	})
	return out, subs
}

func (s *substitutions) lookup(idx string) (substitution, bool) {
	i, err := strconv.Atoi(idx)
	if err != nil || i < 0 || i >= len(s.items) {
		return substitution{}, false
	}
	return s.items[i], true
}

// plain resolves the sentinels in in to the text a reader sees (for titles and heading ids).
func (s *substitutions) plain(in string) string {
	return sentinelRE.ReplaceAllStringFunc(in, func(m string) string {
		it, ok := s.lookup(sentinelRE.FindStringSubmatch(m)[1])
		switch {
		case !ok:
			return ""
		case it.filled:
			return it.value
		}
		return "<" + it.name + ">"
	})
}

// html resolves the sentinels in already-escaped HTML to escaped text.
func (s *substitutions) html(in string) string {
	return sentinelRE.ReplaceAllStringFunc(in, func(m string) string {
		return html.EscapeString(s.plain(m))
	})
}

// builtinSecret is the fixed secret list that always wins (security-model.md section 5): every
// tailnet IP, the tailnet name, the Route 53 zone, the VPS address and host name, every LAN IP and
// every e-mail field. Names in secret_fields of inventory.example.yaml come on top (Pairs.IsSecret).
func builtinSecret(name string) bool {
	switch {
	case name == "TAILNET", name == "ROUTE53_ZONE_ID", name == "VPS_PUBLIC_IP", name == "VPS_HOSTNAME":
		return true
	case strings.HasPrefix(name, "TAILNET_IP"), strings.HasPrefix(name, "LAN_IP"):
		return true
	case strings.Contains(name, "EMAIL"):
		return true
	}
	return false
}
