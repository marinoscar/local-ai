package api

import (
	"bytes"
	"embed"
	"html/template"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// The bilingual error page (agent.md section 3.11): plain language, one big message, what to do, the
// doctor code. It carries its own CSS inline and loads nothing (no fonts, no scripts, no images), so
// it works with the network cable pulled (AGENTS.md rule 3).
//
//go:embed templates/error_en.html templates/error_es.html
var errorTemplates embed.FS

var errorPages = map[string]*template.Template{
	langEN: template.Must(template.ParseFS(errorTemplates, "templates/error_en.html")),
	langES: template.Must(template.ParseFS(errorTemplates, "templates/error_es.html")),
}

// PageLink is a button on an error page.
type PageLink struct {
	Href  string
	Label string
}

type pageData struct {
	Status    int
	Code      string
	Message   string
	Fix       string
	RequestID string
	Links     []PageLink
}

// CodeText returns the one-sentence message and fix of a doctor code in lang ("en" or "es"), the
// text the error envelope carries; an unknown code gets the generic text.
func CodeText(code, lang string) (message, fix string) {
	t := lookup(code, lang)
	return t.Message, t.Fix
}

// WantsHTML reports whether the client is a browser asking for a page: its Accept header lists
// text/html (or application/xhtml+xml) with a q-value above zero. A fetch() call (Accept: */*),
// curl and the API clients get the JSON envelope instead.
func WantsHTML(r *http.Request) bool {
	for _, part := range strings.Split(strings.Join(r.Header.Values("Accept"), ","), ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || (mt != "text/html" && mt != "application/xhtml+xml") {
			continue
		}
		if q, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(q, 64); err != nil || f <= 0 {
				continue
			}
		}
		return true
	}
	return false
}

// WriteErrorPage answers with err like WriteError does, except that a browser (WantsHTML) gets a
// page in its language instead of JSON: the message, what to do and the doctor code, with a link
// back to the Portal. The status, Retry-After and the other headers of the error are the same, and
// the page carries the global HTML headers. It must be called before the response has started.
func WriteErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	WriteErrorPageLinks(w, r, err, nil)
}

// WriteErrorPageLinks is WriteErrorPage with the buttons of the page chosen by the caller; nil
// means the default (back to the Portal).
func WriteErrorPageLinks(w http.ResponseWriter, r *http.Request, err error, links []PageLink) {
	if !WantsHTML(r) {
		WriteError(w, r, err)
		return
	}
	info := InfoFrom(r.Context())
	e := ErrorFrom(err, info.ID)
	if e.Status >= 500 && !isAPIError(err) {
		info.Err = err
	}
	lang := info.Lang
	if lang == "" {
		lang = resolveLang(r.Header.Get("Accept-Language"), "")
	}
	t := lookup(e.Code, lang)
	data := pageData{Status: e.Status, Code: e.Code, Message: e.Message, Fix: e.Fix, RequestID: info.ID, Links: links}
	if data.Message == "" {
		data.Message = t.Message
	}
	if data.Fix == "" {
		data.Fix = t.Fix
	}
	// The doctor texts mark buttons and commands with **bold**; the page shows them as plain words.
	data.Message, data.Fix = strings.ReplaceAll(data.Message, "**", ""), strings.ReplaceAll(data.Fix, "**", "")
	if data.Status < 400 || data.Status > 599 {
		data.Status = http.StatusInternalServerError
	}
	if data.Links == nil {
		data.Links = []PageLink{{Href: "/portal", Label: map[string]string{langEN: "Back to the Portal", langES: "Volver al Portal"}[lang]}}
	}
	tpl := errorPages[lang]
	if tpl == nil {
		tpl = errorPages[langEN]
	}
	var buf bytes.Buffer
	if terr := tpl.Execute(&buf, data); terr != nil {
		WriteError(w, r, terr) // cannot happen with an embedded template; fail safe
		return
	}
	info.ErrCode = e.Code

	h := w.Header()
	for k, vs := range e.Header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if e.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	SetHTMLHeaders(h)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Language", lang)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(data.Status)
	_, _ = w.Write(buf.Bytes())
}
