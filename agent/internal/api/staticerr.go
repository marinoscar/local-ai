package api

import (
	"net/http"

	"github.com/marinoscar/local-ai/agent/internal/api/static"
)

// StaticError turns what internal/api/static returned into an API error, for the handlers that serve
// files (/docs/, /maps/): a missing, forbidden or unsafe path is 404 LAI-AGT-021 with
// details.reason "not_found" (the client cannot tell them apart); a wrong method is 405 with Allow;
// a refused range is 416 with Content-Range. A nil error gives nil; any other error is passed on
// as a 404, never a 500, because a file that cannot be opened is a file that is not there.
func StaticError(err error) *Error {
	se := static.AsError(err)
	if se == nil {
		return nil
	}
	var e *Error
	switch se.Status {
	case http.StatusMethodNotAllowed:
		e = NewError(CodeInvalidRequest, se.Status, map[string]any{"reason": se.Reason})
	case http.StatusRequestedRangeNotSatisfiable:
		e = NewError(CodeInvalidRequest, se.Status, map[string]any{"reason": se.Reason})
	default:
		e = NewError(CodeNotFound, http.StatusNotFound, map[string]any{"reason": "not_found"})
	}
	if len(se.Header) > 0 {
		e.Header = se.Header.Clone()
	}
	return e
}
