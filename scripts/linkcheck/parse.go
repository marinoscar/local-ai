package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Link is one link target found in a markdown file.
type Link struct {
	Line   int
	Target string
}

// Doc is the result of parsing one markdown file.
type Doc struct {
	Links   []Link
	Anchors map[string]bool // heading slugs (with GitHub's -1, -2 duplicate suffixes) and explicit ids
}

var (
	reFence    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	reATX      = regexp.MustCompile(`^ {0,3}#{1,6}(?:\s+(.*?))?\s*$`)
	reSetext   = regexp.MustCompile(`^ {0,3}(=+|-+)\s*$`)
	reRefDef   = regexp.MustCompile(`^ {0,3}\[([^\]^][^\]]*)\]:\s*(<[^>]*>|\S+)`)
	reAutolink = regexp.MustCompile(`<((?:https?|ftp)://[^>\s]+)>`)
	reHTMLID   = regexp.MustCompile(`(?i)<[a-z][^>]*\s(?:id|name)\s*=\s*["']([^"']+)["']`)
	reInline   = regexp.MustCompile("`+[^`]*`+")
)

// parseMarkdown extracts links and heading anchors, ignoring fenced code,
// inline code spans and HTML comments.
func parseMarkdown(src string) Doc {
	doc := Doc{Anchors: map[string]bool{}}
	counts := map[string]int{}
	addHeading := func(text string) {
		slug := slugify(text)
		n := counts[slug]
		counts[slug] = n + 1
		if n > 0 {
			slug += "-" + strconv.Itoa(n)
		}
		doc.Anchors[slug] = true
	}

	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var fence string
	inComment := false
	prevParagraph := "" // last non-blank, non-structural line, for setext headings

	for i, line := range lines {
		lineNo := i + 1
		if fence != "" {
			if m := reFence.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) && strings.TrimSpace(line[len(m[0]):]) == "" {
				fence = ""
			}
			continue
		}
		if m := reFence.FindStringSubmatch(line); m != nil {
			fence = m[1]
			prevParagraph = ""
			continue
		}
		// HTML comments (single or multi line).
		clean := line
		for {
			if inComment {
				end := strings.Index(clean, "-->")
				if end < 0 {
					clean = ""
					break
				}
				clean = clean[end+3:]
				inComment = false
			}
			start := strings.Index(clean, "<!--")
			if start < 0 {
				break
			}
			end := strings.Index(clean[start+4:], "-->")
			if end < 0 {
				clean = clean[:start]
				inComment = true
				break
			}
			clean = clean[:start] + " " + clean[start+4+end+3:]
		}
		if strings.TrimSpace(clean) == "" {
			prevParagraph = ""
			continue
		}

		for _, m := range reHTMLID.FindAllStringSubmatch(clean, -1) {
			doc.Anchors[m[1]] = true
		}

		// Headings.
		if m := reATX.FindStringSubmatch(clean); m != nil {
			addHeading(m[1])
			prevParagraph = ""
			continue
		}
		if m := reSetext.FindStringSubmatch(clean); m != nil && prevParagraph != "" {
			addHeading(prevParagraph)
			prevParagraph = ""
			continue
		}
		prevParagraph = strings.TrimSpace(clean)

		// Strip inline code so links inside code spans are ignored.
		noCode := reInline.ReplaceAllString(clean, " ")

		if m := reRefDef.FindStringSubmatch(noCode); m != nil {
			doc.Links = append(doc.Links, Link{lineNo, strings.Trim(m[2], "<>")})
			continue
		}
		for _, m := range reAutolink.FindAllStringSubmatch(noCode, -1) {
			doc.Links = append(doc.Links, Link{lineNo, m[1]})
		}
		for _, t := range inlineTargets(noCode) {
			doc.Links = append(doc.Links, Link{lineNo, t})
		}
	}
	return doc
}

// inlineTargets finds the destinations of inline links `[text](dest "title")` and images.
func inlineTargets(s string) []string {
	var out []string
	for from := 0; ; {
		idx := strings.Index(s[from:], "](")
		if idx < 0 {
			break
		}
		pos := from + idx
		from = pos + 2
		if !strings.Contains(s[:pos], "[") {
			continue
		}
		t, ok := parseDest(s[pos+2:])
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// parseDest reads a link destination: `<...>` or a run without spaces with balanced parens.
func parseDest(s string) (string, bool) {
	s = strings.TrimLeft(s, " \t")
	if strings.HasPrefix(s, "<") {
		end := strings.Index(s, ">")
		if end < 0 {
			return "", false
		}
		return s[1:end], true
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return s[:i], i > 0
			}
			depth--
		case ' ', '\t':
			return s[:i], i > 0
		}
	}
	return "", false
}
