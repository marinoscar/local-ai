package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// idRe is the id format of models, content items and profiles (catalog.md section 2).
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)

const idFormat = `[a-z0-9][a-z0-9.\-]{2,63}`

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool { return oneOf(v, list) }

func quoted(set []string) string {
	q := make([]string, len(set))
	for i, s := range set {
		q[i] = s
	}
	return strings.Join(q, " | ")
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// checkID applies the id format and the uniqueness across models and ark (LAI-CAT-012).
func (s *lintState) checkID(file, path, id string, ids map[string]string) {
	if id == "" {
		return // already reported as a missing required field
	}
	idPath := joinPath(path, "id")
	if !idRe.MatchString(id) {
		s.add(file, CodeID, idPath, "id %q must match %s", truncate(id, 70), idFormat)
	}
	loc := file + ":" + idPath
	if first, dup := ids[id]; dup {
		s.add(file, CodeID, idPath, "duplicate id %q (first defined at %s)", id, first)
		return
	}
	ids[id] = loc
}

// pinnedSHA reports a sha256 that is not 64 lowercase hex characters (LAI-CAT-011).
func (s *lintState) pinnedSHA(file, path, sha string) {
	if sha == "" {
		s.add(file, CodePinning, path, "sha256 is required (64 lowercase hex characters)")
		return
	}
	s.optionalSHA(file, path, sha)
}

// optionalSHA checks the format of a sha256 that may legitimately be absent.
func (s *lintState) optionalSHA(file, path, sha string) {
	if sha != "" && !isHex(sha, 64) {
		s.add(file, CodePinning, path, "sha256 must be 64 lowercase hex characters, got %q", truncate(sha, 70))
	}
}

// unsafeName reports a file name that could escape its directory when the sync engine joins it.
func unsafeName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\\x00") {
		return true
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return true
		}
	}
	return false
}

func enumMsg(field string, got string, set []string) string {
	return fmt.Sprintf("%s must be one of %s, got %q", field, quoted(set), truncate(got, 40))
}
