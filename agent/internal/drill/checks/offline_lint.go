package checks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/offlinescan"
)

// OfflineLintID is check 2 (drill.md section 4.2).
const OfflineLintID = "drl.config.offline_lint"

// maxConfigBytes bounds what the lint reads of one generated file; the Kiwix library of a node with
// many books is the largest, well under it.
const maxConfigBytes = 32 << 20

// maxListed bounds the findings the details list; the count is exact.
const maxListed = 50

// maxRemoteChars bounds the "remote" detail (a "; "-joined list), like the fence's "blocked".
const maxRemoteChars = 500

// OfflineLint configures check 2. A path left empty is not linted.
type OfflineLint struct {
	// ModelsINI is /etc/localai/models.ini: it must not make llama-server fetch anything (llama-router.md
	// section 3.5).
	ModelsINI string
	// LibraryXML is /var/lib/localai/kiwix/library.xml: every book path must be a file that exists
	// (companion-services.md section 3.2).
	LibraryXML string
	// WhisperEnv is /etc/localai/whisper.env: WHISPER_MODEL must be a file that exists.
	WhisperEnv string
	// Bundle is the Portal and maps bundle embedded in the binary (agent/ui/dist), scanned with the
	// rules of survival-portal.md section 4.11. Nil is not scanned.
	Bundle fs.FS
}

// Check returns check 2, drl.config.offline_lint ★: a static scan that nothing the node generated or
// embeds points at the internet. A file that is not there (a node with no whisper.env, no library yet)
// has nothing to point anywhere and is reported as "absent"; what the file's absence means for the
// features is for the checks that use them. Any remote reference, or a local file that does not exist,
// fails with LAI-DRL-010 and the details name the setting.
func (o OfflineLint) Check() drill.Check {
	return drill.Check{
		ID: OfflineLintID, Area: "config", Critical: true, Order: 2, FailCode: doctor.DRL010,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome { return o.run(ctx) },
	}
}

type lintResult struct {
	findings []string // what is wrong, one phrase each, naming the setting
	total    int
	status   map[string]any
}

func (r *lintResult) add(s string) {
	r.total++
	if len(r.findings) < maxListed {
		r.findings = append(r.findings, s)
	}
}

func (o OfflineLint) run(ctx context.Context) drill.Outcome {
	r := &lintResult{status: map[string]any{}}
	steps := []struct {
		name string
		fn   func(*lintResult)
	}{
		{"models_ini", func(r *lintResult) { lintModelsINI(r, o.ModelsINI) }},
		{"library_xml", func(r *lintResult) { lintLibrary(r, o.LibraryXML) }},
		{"whisper_env", func(r *lintResult) { lintWhisperEnv(r, o.WhisperEnv) }},
		{"portal_bundle", func(r *lintResult) { lintBundle(r, o.Bundle) }},
	}
	for _, s := range steps {
		if ctx.Err() != nil {
			return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL010, Details: map[string]any{"reason": "cancelled"}}
		}
		before := r.total
		s.fn(r)
		if r.status[s.name] == nil {
			r.status[s.name] = "ok"
		}
		if r.total > before {
			r.status[s.name] = fmt.Sprintf("%d finding(s)", r.total-before)
		}
	}
	details := r.status
	details["remote_count"] = r.total
	if r.total == 0 {
		return drill.Outcome{Result: drill.ResultPass, Details: details}
	}
	details["remote"] = cut(strings.Join(r.findings, "; "), maxRemoteChars)
	return drill.Outcome{Result: drill.ResultFail, Code: doctor.DRL010, Details: details}
}

// readCapped reads a generated file. exists is false when it is not there.
func readCapped(path string) (data []byte, exists bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err == nil && len(data) > maxConfigBytes {
		err = fmt.Errorf("larger than %d MiB", maxConfigBytes>>20)
	}
	return data, true, err
}

func isURL(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	for _, p := range []string{"http://", "https://", "ftp://", "hf://"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// remoteKey says whether a models.ini key makes llama-server fetch something: the Hugging Face and
// download flags (hf-repo, hf-file, hf-token and their short forms, model-url, mmproj-url, ...) and the
// Docker registry flags.
func remoteKey(k string) bool {
	k = strings.ToLower(strings.TrimLeft(strings.TrimSpace(k), "-"))
	switch k {
	case "hf", "hfd", "hfv", "hff", "hffv", "hft", "hfrd", "mu", "dr", "docker-repo":
		return true
	}
	return strings.HasPrefix(k, "hf-") || strings.HasSuffix(k, "-url") || k == "url"
}

func lintModelsINI(r *lintResult, path string) {
	if path == "" {
		r.status["models_ini"] = "not_linted"
		return
	}
	data, exists, err := readCapped(path)
	switch {
	case !exists:
		r.status["models_ini"] = "absent"
		return
	case err != nil:
		r.status["models_ini"] = "unreadable"
		return
	}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		switch {
		case line == "" || line[0] == ';' || line[0] == '#':
			continue
		case line[0] == '[' && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		where := fmt.Sprintf("models.ini line %d", n)
		if section != "" {
			where += " [" + clipText(section, 60) + "]"
		}
		switch {
		case remoteKey(key):
			r.add(where + " " + clipText(key, 40) + " (fetches from the internet)")
		case isURL(val):
			// The key is named; the value is not: a URL can carry a token.
			r.add(where + " " + clipText(key, 40) + " (a web address)")
		}
	}
}

type xmlBook struct {
	Path string `xml:"path,attr"`
}

func lintLibrary(r *lintResult, path string) {
	if path == "" {
		r.status["library_xml"] = "not_linted"
		return
	}
	data, exists, err := readCapped(path)
	switch {
	case !exists:
		r.status["library_xml"] = "absent"
		return
	case err != nil:
		r.status["library_xml"] = "unreadable"
		return
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	books := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			r.status["library_xml"] = "unparseable"
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "book" {
			continue
		}
		var b xmlBook
		if err := dec.DecodeElement(&b, &se); err != nil {
			r.status["library_xml"] = "unparseable"
			return
		}
		books++
		p := strings.TrimSpace(b.Path)
		switch {
		case p == "":
			continue
		case isURL(p):
			r.add("library.xml book " + clipText(p, 100) + " (a web address)")
			continue
		case !filepath.IsAbs(p):
			p = filepath.Join(filepath.Dir(path), p)
		}
		if st, err := os.Stat(p); err != nil {
			r.add("library.xml book " + clipText(b.Path, 100) + " (" + why(err) + ")")
		} else if !st.Mode().IsRegular() {
			r.add("library.xml book " + clipText(b.Path, 100) + " (not a file)")
		}
	}
	r.status["books"] = books
}

func why(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "does not exist"
	}
	return "cannot be read"
}

func lintWhisperEnv(r *lintResult, path string) {
	if path == "" {
		r.status["whisper_env"] = "not_linted"
		return
	}
	data, exists, err := readCapped(path)
	switch {
	case !exists:
		r.status["whisper_env"] = "absent"
		return
	case err != nil:
		r.status["whisper_env"] = "unreadable"
		return
	}
	model, found := "", false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || line[0] == '#' {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == "WHISPER_MODEL" {
			model, found = unquote(strings.TrimSpace(v)), true
		}
	}
	switch {
	case !found || model == "":
		r.status["whisper_env"] = "no_model"
	case isURL(model):
		r.add("whisper.env WHISPER_MODEL (a web address)")
	default:
		if st, err := os.Stat(model); err != nil {
			r.add("whisper.env WHISPER_MODEL " + clipText(model, 100) + " (" + why(err) + ")")
		} else if !st.Mode().IsRegular() {
			r.add("whisper.env WHISPER_MODEL " + clipText(model, 100) + " (not a file)")
		}
	}
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func lintBundle(r *lintResult, bundle fs.FS) {
	if bundle == nil {
		r.status["portal_bundle"] = "not_linted"
		return
	}
	res, err := offlinescan.ScanFS(bundle)
	if err != nil {
		r.status["portal_bundle"] = "unreadable"
		return
	}
	r.status["bundle_files"] = res.Files
	for _, f := range res.Findings {
		r.add("portal bundle " + clipText(f.String(), 140))
	}
}

// cut shortens s to at most n bytes without splitting a character and without an ellipsis, for a list
// whose limit is a hard maximum.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
