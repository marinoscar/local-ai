package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// DecodeJSON reads the request body as exactly one JSON value into dst (a pointer), strictly
// (agent.md section 3.7): an unknown field, a value of the wrong type, malformed JSON, an empty
// body and trailing data after the value are all 400 with code LAI-AGT-020 and a details.reason; the
// first two name the field in details.field. A body over the route's limit is 413 with
// LAI-AGT-025. The returned error is an *Error ready for WriteError.
//
// The limit is the route's (RequestInfo.MaxBody; 1 MiB by default); the middleware already caps the
// body with it, and DecodeJSON caps it again so a handler called outside the chain is safe too.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSON(w, r, dst, false)
}

// DecodeJSONOrEmpty is DecodeJSON for a body that is optional: an empty body leaves dst untouched
// and returns nil. Everything else is as strict as DecodeJSON.
func DecodeJSONOrEmpty(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSON(w, r, dst, true)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	limit := InfoFrom(r.Context()).MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			if allowEmpty {
				return nil
			}
			return invalidBody("empty_body", nil)
		}
		return classifyDecodeError(err)
	}
	// Exactly one value: anything but end of input after it is an error.
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return invalidBody("trailing_data", nil)
	default:
		return classifyDecodeError(err)
	}
}

func classifyDecodeError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return ErrorFrom(err, "")
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		d := map[string]any{"expected": typeErr.Type.String()}
		if typeErr.Field != "" {
			d["field"] = typeErr.Field
		}
		return invalidBody("wrong_type", d)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return invalidBody("malformed_json", map[string]any{"offset": syntaxErr.Offset})
	}
	if name, ok := unknownField(err); ok {
		return invalidBody("unknown_field", map[string]any{"field": name})
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return invalidBody("malformed_json", nil)
	}
	return invalidBody("read_error", nil)
}

// unknownField extracts the field name from the decoder's `json: unknown field "x"` error, the only
// way the standard library reports it.
func unknownField(err error) (string, bool) {
	const prefix = "json: unknown field "
	msg := err.Error()
	if !strings.HasPrefix(msg, prefix) {
		return "", false
	}
	name, uerr := strconv.Unquote(strings.TrimPrefix(msg, prefix))
	if uerr != nil {
		return "", false
	}
	return name, true
}

func invalidBody(reason string, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	details["reason"] = reason
	return NewError(CodeInvalidRequest, http.StatusBadRequest, details)
}
