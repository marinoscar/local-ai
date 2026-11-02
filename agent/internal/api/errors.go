package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Error is an API error: an HTTP status, a doctor code and the details of the envelope
// (agent.md section 3.7). Handlers return or pass it to WriteError.
//
// Message and Fix are optional: left empty they come from the doctor code table in the language of
// the request. A handler sets them (WithMessage) for a domain code that has a message built from the
// situation, such as the model name in LAI-LLM-009.
type Error struct {
	Status  int
	Code    string
	Message string
	Fix     string
	Details map[string]any
	// RetryAfter is sent as the Retry-After header, in seconds, when greater than zero.
	RetryAfter int
	// Header holds extra response headers (Allow on a 405).
	Header http.Header
}

// NewError returns an Error with the status and doctor code. details may be nil. A
// CodeStarting error carries Retry-After: 5 (agent.md section 3.7).
func NewError(code string, status int, details map[string]any) *Error {
	e := &Error{Status: status, Code: code, Details: details}
	if code == CodeStarting {
		e.RetryAfter = 5
	}
	return e
}

// WithMessage returns a copy of e with its own message and fix.
func (e *Error) WithMessage(message, fix string) *Error {
	c := *e
	c.Message, c.Fix = message, fix
	return &c
}

// Error implements the error interface. The text is for logs; it carries the code.
func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = lookup(e.Code, langEN).Message
	}
	return e.Code + ": " + msg
}

// envelope and envelopeBody are the JSON body of every non-2xx response; the wire types are
// ErrorEnvelope and ErrorBody (types.go), which mirror #/components/schemas/ErrorEnvelope.
type (
	envelope     = ErrorEnvelope
	envelopeBody = ErrorBody
)

// ErrorFrom classifies err into an *Error. An *Error passes through; a body over the limit becomes
// 413; a handler that ran past its route deadline becomes 504 with the internal code; anything else
// is a bug and becomes 500. The 500 and 504 envelopes carry details.request_id when reqID is given.
func ErrorFrom(err error, reqID string) *Error {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return NewError(CodeTooLarge, http.StatusRequestEntityTooLarge, map[string]any{"limit": tooBig.Limit})
	}
	details := map[string]any{}
	if reqID != "" {
		details["request_id"] = reqID
	}
	if errors.Is(err, context.DeadlineExceeded) {
		details["reason"] = "deadline_exceeded"
		return NewError(CodeInternal, http.StatusGatewayTimeout, details)
	}
	return NewError(CodeInternal, http.StatusInternalServerError, details)
}

// WriteError answers r with the error envelope for err (see ErrorFrom). It localizes message and fix,
// adds details.request_id to every 500, sets Retry-After and any extra headers, and records the code
// and the underlying error of a 5xx for the access log. It must be called before the response has
// started.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
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
	body := envelopeBody{Code: e.Code, Message: e.Message, Fix: e.Fix, Details: map[string]any{}}
	if body.Message == "" {
		body.Message = t.Message
	}
	if body.Fix == "" {
		body.Fix = t.Fix
	}
	for k, v := range e.Details {
		body.Details[k] = v
	}
	if e.Status == http.StatusInternalServerError {
		if _, ok := body.Details["request_id"]; !ok && info.ID != "" {
			body.Details["request_id"] = info.ID
		}
	}
	info.ErrCode = e.Code

	for k, vs := range e.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	status := e.Status
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	writeJSONBody(w, status, envelope{Error: body})
}

func isAPIError(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr)
}

// WriteJSON answers with status and v encoded as application/json; charset=utf-8. v is encoded
// before anything is written, so an encoding failure still produces a clean 500 envelope.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// writeJSONBody is WriteJSON for values that cannot fail to encode (the envelope).
func writeJSONBody(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// SetOpID sets X-LocalAI-Op-Id, which a mutating response carries when it created an operation
// (agent.md section 3.7). Call it before writing the response.
func SetOpID(w http.ResponseWriter, opID string) { w.Header().Set("X-LocalAI-Op-Id", opID) }

// HandlerFunc is a route handler that returns its error instead of writing it: a non-nil error is
// answered with WriteError (so an *Error keeps its status and code, anything else is a 500).
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// ServeHTTP calls f and writes its error, if any.
func (f HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := f(w, r); err != nil {
		WriteError(w, r, err)
	}
}

// resolveLang picks the language of error messages (agent.md section 3.7): Spanish when ui.language
// is "es" or when Accept-Language prefers Spanish, English otherwise. Of the languages the agent
// has, Accept-Language chooses by q-value, ties by order.
func resolveLang(acceptLanguage, uiLanguage string) string {
	if strings.EqualFold(strings.TrimSpace(uiLanguage), langES) {
		return langES
	}
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cands []cand
	for i, part := range strings.Split(acceptLanguage, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		if primary != langEN && primary != langES {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				} else {
					q = 0
				}
			}
		}
		if q > 0 {
			cands = append(cands, cand{primary, q, i})
		}
	}
	if len(cands) == 0 {
		return langEN
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].q > cands[j].q })
	return cands[0].lang
}
