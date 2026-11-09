// Package offlinescan is the Go port of scripts/check-offline-assets.mjs, the offline asset rule of
// survival-portal.md section 4.11: nothing the Portal or the maps viewer needs may come from another
// origin. The CI build scan runs the script on agent/ui/dist; the drill (check drl.config.offline_lint,
// drill.md section 4.2) runs this port on the files embedded in the agent binary, so a node proves
// for itself that the build it is running has no remote reference.
//
// The two must agree: the rules below are the script's, line for line, and scan_test.go compares them
// on the script's own fixtures (and, when node is installed, on the script's live output).
package offlinescan

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// space is the JavaScript \s class (Go's \s is ASCII only): the script's URL pattern ends at any of
// these characters.
const space = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	urlRE       = regexp.MustCompile(`(?i)https?://[^` + space + `"'` + "`" + `)<>\\]*`)
	forbiddenRE = regexp.MustCompile(`(?i)fonts\.googleapis|unpkg|jsdelivr|cdn`)
	swRE        = regexp.MustCompile(`navigator[` + space + `]*\.[` + space + `]*serviceWorker[` + space + `]*\.[` + space + `]*register`)
	commentRE   = regexp.MustCompile(`(?s)/\*.*?\*/`)
	licenceRE   = regexp.MustCompile(`(?i)@license|@preserve|copyright|licen[sc]e`)
	licFileRE   = regexp.MustCompile(`(?i)^licen[sc]e`)
)

// binary are the extensions the script does not scan (fonts, images, audio, archives, wasm).
var binary = map[string]bool{
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".ico": true, ".mp3": true, ".ogg": true, ".wav": true, ".mp4": true,
	".webm": true, ".pmtiles": true, ".zim": true, ".gz": true, ".br": true, ".wasm": true,
}

// allowedURLPrefixes are the URLs that are fine (compared case-insensitively): the agent's own
// address, the XML namespaces of http://www.w3.org/, and React's production error-message text, which
// is shown and never fetched.
var allowedURLPrefixes = []string{"http://127.0.0.1:", "http://localhost", "http://www.w3.org/", "https://react.dev/errors/"}

// Finding is one reference that breaks the rule.
type Finding struct {
	// File is the path inside the scanned tree (empty for ScanText).
	File string
	// Line is 1-based.
	Line int
	// Message is the offending URL, or `forbidden name "x"`, or "service worker registration".
	Message string
}

// String is the script's output line: file:line: message.
func (f Finding) String() string {
	if f.File == "" {
		return fmt.Sprintf("%d: %s", f.Line, f.Message)
	}
	return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Message)
}

// Result is the outcome of a tree scan: how many text files were scanned and what was found.
type Result struct {
	Files    int
	Findings []Finding
}

func lineOf(text string, index int) int {
	return 1 + strings.Count(text[:index], "\n")
}

// licenceRange is a block comment that carries a licence or attribution: the URLs inside it are text
// to read, not references.
type licenceRange struct{ start, end int }

func licenceRanges(text string) []licenceRange {
	var out []licenceRange
	for _, m := range commentRE.FindAllStringIndex(text, -1) {
		c := text[m[0]:m[1]]
		if strings.HasPrefix(c, "/*!") || licenceRE.MatchString(c) {
			out = append(out, licenceRange{m[0], m[1]})
		}
	}
	return out
}

// ScanText returns the findings of one file's text, in the script's order: URLs, then forbidden
// names, then service worker registrations. name decides whether it is a LICENSE file (every URL in
// one is fine).
func ScanText(name, text string) []Finding {
	var findings []Finding
	isLicenceFile := licFileRE.MatchString(path.Base(name))
	ranges := licenceRanges(text)
	inLicence := func(i int) bool {
		if isLicenceFile {
			return true
		}
		for _, r := range ranges {
			if i >= r.start && i < r.end {
				return true
			}
		}
		return false
	}
	for _, m := range urlRE.FindAllStringIndex(text, -1) {
		url := text[m[0]:m[1]]
		lower := strings.ToLower(url)
		allowed := false
		for _, p := range allowedURLPrefixes {
			if strings.HasPrefix(lower, strings.ToLower(p)) {
				allowed = true
				break
			}
		}
		if allowed || inLicence(m[0]) {
			continue
		}
		findings = append(findings, Finding{Line: lineOf(text, m[0]), Message: url})
	}
	for _, m := range forbiddenRE.FindAllStringIndex(text, -1) {
		findings = append(findings, Finding{Line: lineOf(text, m[0]), Message: fmt.Sprintf("forbidden name %q", text[m[0]:m[1]])})
	}
	for _, m := range swRE.FindAllStringIndex(text, -1) {
		findings = append(findings, Finding{Line: lineOf(text, m[0]), Message: "service worker registration"})
	}
	return findings
}

// ScanFS scans every text file of fsys (binary extensions are skipped), in path order like the script,
// and returns the findings with their file. The paths of an fs.FS use "/".
func ScanFS(fsys fs.FS) (Result, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	sort.Strings(paths) // the script sorts the whole path, not one directory at a time
	var res Result
	for _, p := range paths {
		if binary[strings.ToLower(path.Ext(p))] {
			continue
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return Result{}, err
		}
		res.Files++
		for _, f := range ScanText(p, strings.ToValidUTF8(string(b), "\uFFFD")) {
			f.File = p
			res.Findings = append(res.Findings, f)
		}
	}
	return res, nil
}
