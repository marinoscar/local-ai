package resolve

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// kiwixEntry is one dated ZIM of a book found in a directory listing.
type kiwixEntry struct {
	// File is the file name, "<book>_<YYYY-MM>.zim".
	File string
	// Version is "YYYY-MM".
	Version string
}

// kiwixHrefRe finds the href attributes of a listing. It is a tolerant scan, not an HTML parser
// (ark-sync.md section 4.9.2): double quotes, single quotes or bare values, any case.
var kiwixHrefRe = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// kiwixBookRe is the book name rule: what may become part of a pattern and a URL path.
var kiwixBookRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// parseKiwixListing returns the dated ZIMs of book in the listing body, newest first. It accepts every
// href whose last path element (after %-decoding and without a query or fragment) is exactly
// "<book>_<YYYY-MM>.zim" with a month from 01 to 12; "<file>.torrent", ".meta4", ".sha256" and every
// other neighbour never match, because the pattern is anchored. A file listed twice counts once.
func parseKiwixListing(body []byte, book string) []kiwixEntry {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(book) + `_(\d{4}-(?:0[1-9]|1[0-2]))\.zim$`)
	seen := map[string]bool{}
	var out []kiwixEntry
	for _, m := range kiwixHrefRe.FindAllSubmatch(body, -1) {
		raw := string(m[1]) + string(m[2]) + string(m[3])
		name, ok := kiwixHrefName(raw)
		if !ok || seen[name] {
			continue
		}
		sub := re.FindStringSubmatch(name)
		if sub == nil {
			continue
		}
		seen[name] = true
		out = append(out, kiwixEntry{File: name, Version: sub[1]})
	}
	// "YYYY-MM" sorts as text; a tie cannot happen (the file name has the version in it).
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// kiwixHrefName reduces an href to the file name it points at: the last element of its path, with %
// escapes decoded. An href that decodes to something with a separator, a control character or a NUL is
// not a plain file name and is ignored.
func kiwixHrefName(href string) (string, bool) {
	href = strings.TrimSpace(href)
	if i := strings.IndexAny(href, "?#"); i >= 0 {
		href = href[:i]
	}
	if href == "" || strings.HasSuffix(href, "/") {
		return "", false
	}
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		href = u.Path // decodes %-escapes and drops a scheme and host
	} else if dec, derr := url.PathUnescape(href); derr == nil {
		href = dec
	} else {
		return "", false
	}
	name := path.Base(href)
	if name == "." || name == ".." || name == "/" || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return name, true
}

// kiwixVersionFile splits "<book>_<YYYY-MM>.zim" into its version; ok is false for any other name.
func kiwixVersionFile(book, name string) (version string, ok bool) {
	rest, found := strings.CutPrefix(name, book+"_")
	if !found {
		return "", false
	}
	v, found := strings.CutSuffix(rest, ".zim")
	if !found || len(v) != 7 || v[4] != '-' {
		return "", false
	}
	if _, err := strconv.Atoi(v[:4]); err != nil {
		return "", false
	}
	if mo, err := strconv.Atoi(v[5:]); err != nil || mo < 1 || mo > 12 {
		return "", false
	}
	return v, true
}
