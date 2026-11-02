package out

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ErrSecondDocument is returned when a command tries to write a second JSON document, or a document
// after JSON Lines (aictl.md section 3.5: one document per command). Nothing is written.
var ErrSecondDocument = errors.New("out: --json allows exactly one JSON document on stdout")

// ErrNotJSON is returned by Raw for a body that is not valid JSON. Nothing is written.
var ErrNotJSON = errors.New("out: body is not valid JSON")

type jsonMode int

const (
	jsonNone jsonMode = iota
	jsonDoc
	jsonLines
)

// JSON writes the machine output of --json: exactly one document per command, or JSON Lines for a
// streaming command. It is the only thing that may write to stdout in --json mode. Documents are
// UTF-8, compact, end with one newline and are not HTML-escaped. It is safe for concurrent use.
type JSON struct {
	mu   sync.Mutex
	w    io.Writer
	mode jsonMode
}

// NewJSON returns a writer on w (stdout).
func NewJSON(w io.Writer) *JSON { return &JSON{w: w} }

// Wrote reports whether anything has been written.
func (j *JSON) Wrote() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.mode != jsonNone
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode ends with exactly one newline
}

// Document writes v as the one JSON document of the command. A second call, or a call after Line,
// returns ErrSecondDocument and writes nothing.
func (j *JSON) Document(v any) error {
	b, err := encode(v)
	if err != nil {
		return err
	}
	return j.write(jsonDoc, b)
}

// Raw writes body, an answer of the agent, as the one document without reformatting it (surrounding
// white space is trimmed and one newline added). A body that is not valid JSON is ErrNotJSON.
func (j *JSON) Raw(body []byte) error {
	body = bytes.TrimSpace(body)
	if !json.Valid(body) {
		return ErrNotJSON
	}
	return j.write(jsonDoc, append(append([]byte(nil), body...), '\n'))
}

// Line writes v as one line of a JSON Lines stream (logs -f). A call after Document returns
// ErrSecondDocument.
func (j *JSON) Line(v any) error {
	b, err := encode(v)
	if err != nil {
		return err
	}
	return j.write(jsonLines, b)
}

// envelope is the error document of aictl.md section 3.5, in the agent's field order.
type envelope struct {
	Error envelopeBody `json:"error"`
}

type envelopeBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Fix     string         `json:"fix"`
	Details map[string]any `json:"details"`
}

// Envelope returns the error document for p: {"error":{"code","message","fix","details"}} with
// details always an object.
func Envelope(p Problem) ([]byte, error) {
	d := p.Details
	if d == nil {
		d = map[string]any{}
	}
	return encode(envelope{envelopeBody{Code: p.Code, Message: p.Message, Fix: p.Fix, Details: d}})
}

// Error writes the failure envelope for p. It is the document of the command, or one more line of a
// stream. If a document was already written it writes nothing and returns ErrSecondDocument: stdout
// keeps its one document and only the exit code reports the failure.
func (j *JSON) Error(p Problem) error {
	b, err := Envelope(p)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.mode == jsonDoc {
		return ErrSecondDocument
	}
	if j.mode == jsonNone {
		j.mode = jsonDoc
	}
	_, err = j.w.Write(b)
	return err
}

func (j *JSON) write(m jsonMode, b []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.mode == jsonDoc || (j.mode == jsonLines && m == jsonDoc) {
		return ErrSecondDocument
	}
	j.mode = m
	_, err := j.w.Write(b)
	return err
}
