package static

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Error is why a file was not served. Status is the HTTP status to answer (404, 405 or 416); Header
// holds the headers that go with it (Allow on a 405, Content-Range on a 416); Reason is a short
// machine word for the error details ("not_found", "method_not_allowed", "multiple_ranges"). Err is
// the underlying cause for the log, never for the client: a missing file and a forbidden one both
// give 404 "not_found".
type Error struct {
	Status int
	Reason string
	Header http.Header
	Err    error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("static: %d %s: %v", e.Status, e.Reason, e.Err)
	}
	return fmt.Sprintf("static: %d %s", e.Status, e.Reason)
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

func notFound(cause error) *Error {
	return &Error{Status: http.StatusNotFound, Reason: "not_found", Err: cause}
}

// ServeFile answers r with the file at path, which must come from SafeJoin. Only GET and HEAD are
// served. It supports a single byte range (206 with Content-Range) and refuses several ranges with
// 416, so a request can never make the agent build a multipart answer. A file that is not a regular
// file (directory, FIFO, device) is not found.
//
// ServeFile either serves the file or returns an *Error without having written anything; the caller
// answers it. It sets Accept-Ranges: bytes, X-Content-Type-Options: nosniff, a weak ETag and
// Last-Modified (so a revalidation costs nothing), and Cache-Control: no-cache unless the caller set
// one already. The file is opened here and closed before ServeFile returns.
func ServeFile(w http.ResponseWriter, r *http.Request, path string) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return &Error{Status: http.StatusMethodNotAllowed, Reason: "method_not_allowed",
			Header: http.Header{"Allow": []string{"GET, HEAD"}}}
	}
	f, fi, err := openRegular(path)
	if err != nil {
		return notFound(err)
	}
	defer f.Close()

	if rng := r.Header.Get("Range"); multipleRanges(rng) || unsatisfiable(rng, fi.Size()) {
		reason := "range_not_satisfiable"
		if multipleRanges(rng) {
			reason = "multiple_ranges"
		}
		return &Error{Status: http.StatusRequestedRangeNotSatisfiable, Reason: reason,
			Header: http.Header{"Content-Range": []string{"bytes */" + strconv.FormatInt(fi.Size(), 10)}}}
	}

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("X-Content-Type-Options", "nosniff")
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("ETag", fmt.Sprintf(`W/"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	if h.Get("Content-Type") == "" {
		if ct := ContentType(path); ct != "" {
			h.Set("Content-Type", ct)
		} // else http.ServeContent sniffs the first 512 bytes
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
	return nil
}

// Resolve returns the real path of rel below root for serving: the SafeJoin result, or, when that is
// a directory ("" for the root itself, "sub" or "sub/"), the index file inside it, which goes through
// SafeJoin too, so an index that is a symbolic link out of the root is not followed. Every failure is
// a 404 *Error.
func Resolve(root, rel, index string) (string, *Error) {
	p, err := SafeJoin(root, rel)
	if err != nil {
		return "", notFound(err)
	}
	if isDir(p) {
		if index == "" {
			return "", notFound(errIsDir)
		}
		p, err = SafeJoin(root, path.Join(strings.TrimSuffix(rel, "/"), index))
		if err != nil {
			return "", notFound(err)
		}
	}
	return p, nil
}

// ServeUnder serves rel (relative, decoded, as for SafeJoin) below root: Resolve, then ServeFile.
// Errors are as for ServeFile; every SafeJoin failure is a 404.
func ServeUnder(w http.ResponseWriter, r *http.Request, root, rel, index string) error {
	p, err := Resolve(root, rel, index)
	if err != nil {
		return err
	}
	return ServeFile(w, r, p)
}

// multipleRanges reports whether a Range header asks for more than one byte range.
func multipleRanges(h string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok {
		return false
	}
	n := 0
	for _, part := range strings.Split(rest, ",") {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n > 1
}

// unsatisfiable reports whether a well-formed single range starts at or past the end of a file of
// the given size (or is a zero-length suffix). A malformed header is left to net/http, which
// answers 416 itself.
func unsatisfiable(h string, size int64) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok || strings.Contains(rest, ",") {
		return false
	}
	start, _, ok := strings.Cut(strings.TrimSpace(rest), "-")
	if !ok {
		return false
	}
	if start == "" { // suffix: bytes=-N
		_, n, _ := strings.Cut(strings.TrimSpace(rest), "-")
		v, err := strconv.ParseInt(n, 10, 64)
		return err == nil && v == 0
	}
	v, err := strconv.ParseInt(start, 10, 64)
	return err == nil && v >= size
}

// contentTypes covers what the content roots hold, so the answer does not depend on a system MIME
// database that an offline or minimal node may not have. Text types carry a charset.
var contentTypes = map[string]string{
	".html":    "text/html; charset=utf-8",
	".htm":     "text/html; charset=utf-8",
	".css":     "text/css; charset=utf-8",
	".js":      "text/javascript; charset=utf-8",
	".mjs":     "text/javascript; charset=utf-8",
	".json":    "application/json; charset=utf-8",
	".txt":     "text/plain; charset=utf-8",
	".md":      "text/plain; charset=utf-8",
	".xml":     "application/xml; charset=utf-8",
	".svg":     "image/svg+xml",
	".png":     "image/png",
	".jpg":     "image/jpeg",
	".jpeg":    "image/jpeg",
	".gif":     "image/gif",
	".webp":    "image/webp",
	".ico":     "image/x-icon",
	".pdf":     "application/pdf",
	".woff":    "font/woff",
	".woff2":   "font/woff2",
	".pmtiles": "application/octet-stream",
	".wasm":    "application/wasm",
}

// ContentType returns the content type for a file name by extension, "" when unknown (the caller
// then lets net/http sniff it).
func ContentType(name string) string {
	return contentTypes[strings.ToLower(filepath.Ext(name))]
}

// IsHTML reports whether a file name is an HTML page, which is what gets a Content-Security-Policy.
func IsHTML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".htm"
}

// AsError returns err as an *Error (a SafeJoin error becomes a 404), nil for nil.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var se *Error
	if errors.As(err, &se) {
		return se
	}
	return notFound(err)
}
