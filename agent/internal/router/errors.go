package router

import (
	"errors"
	"net/http"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Doctor codes of the orchestrator (llama-router.md sections 4.4, 4.5 and 8; agent.md section 3.8).
// Like the other codes of this package they alias internal/doctor/codes.go, the one file that
// spells a "LAI-..." literal.
const (
	// CodeNotFound: the model id is not in the catalog (404).
	CodeNotFound Code = doctor.AGT021
	// CodeBusy: another model operation holds modelOpsMu (409, details.op_id).
	CodeBusy Code = doctor.AGT022
	// CodeRefused: the operation is refused in this mode or state (409): unloading the last LLM in
	// survival mode without force.
	CodeRefused Code = doctor.AGT023
	// CodeLoadTimeout: a model took too long to start; the previous model was started again (504).
	CodeLoadTimeout Code = doctor.LLM010
	// CodeNothingLoaded: no model could be started, not even the previous one (500, critical).
	CodeNothingLoaded Code = doctor.LLM012
	// CodeUnloadHung: a model would not stop, so the AI service was restarted (warning).
	CodeUnloadHung Code = doctor.LLM031
)

// OpError is a failed model operation: a doctor code, the HTTP status the API answers with, a
// plain-language English message and the details object of the error body (agent.md section 3.8).
// The orchestrator returns it for every refusal and failure, so a route only has to map it:
//
//	var oe *router.OpError
//	if errors.As(err, &oe) { status, code, details = oe.HTTPStatus(), oe.Code, oe.Details }
//
// Anything else the orchestrator returns is the caller's own context error (the caller gave up
// waiting; a running operation carries on) or ErrInterrupted.
type OpError struct {
	Code    Code
	Status  int
	Message string
	// MessageES is the Spanish text of a message built from the situation (the reason a model does
	// not fit, with its sizes); empty when the code's own text in the doctor table says it.
	MessageES string
	// Details is the error body's details. It is never nil. Typical keys: op_id, rolled_back,
	// restored, exit_code, cause.
	Details map[string]any
	// Err is the underlying error (a *BusyError, a router client error), when there is one.
	Err error
}

// Error implements error.
func (e *OpError) Error() string {
	s := string(e.Code) + ": " + e.Message
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Text returns the message in the given language ("es" and region variants give Spanish when there
// is a Spanish text; anything else English).
func (e *OpError) Text(lang string) string {
	l := strings.ToLower(lang)
	if (l == "es" || strings.HasPrefix(l, "es-") || strings.HasPrefix(l, "es_")) && e.MessageES != "" {
		return e.MessageES
	}
	return e.Message
}

// Unwrap lets errors.Is and errors.As reach the cause.
func (e *OpError) Unwrap() error { return e.Err }

// HTTPStatus is the status the API answers with for this error.
func (e *OpError) HTTPStatus() int { return e.Status }

// ErrInterrupted is returned when the agent shut down in the middle of an operation. The router
// call is not aborted (agent.md section 4.12 step 6); state.models.last_load says "interrupted".
var ErrInterrupted = errors.New("router: the agent is shutting down; the model operation was left to the router")

func opErr(code Code, status int, msg string, details map[string]any, cause error) *OpError {
	if details == nil {
		details = map[string]any{}
	}
	return &OpError{Code: code, Status: status, Message: msg, Details: details, Err: cause}
}

func errNotFound(id string) *OpError {
	return opErr(CodeNotFound, http.StatusNotFound, "There is no model with this id.", map[string]any{"model": id}, nil)
}

func errBusy(b *BusyError) *OpError {
	d := map[string]any{}
	if b.OpID != "" {
		d["op_id"] = b.OpID
	}
	if b.Kind != "" {
		d["running"] = b.Kind
	}
	return opErr(CodeBusy, http.StatusConflict, "Another model operation is running. Try again when it ends.", d, b)
}

func errNotLoadable(id string, r *Reason) *OpError {
	e := opErr(r.Code, http.StatusUnprocessableEntity, r.Message, map[string]any{"model": id}, nil)
	e.MessageES = r.MessageES
	return e
}

func errNotLlama(id, engine string) *OpError {
	return opErr(CodeNotFit, http.StatusUnprocessableEntity, "Only language models can be loaded here.", map[string]any{"model": id, "engine": engine}, nil)
}

func errNoRoom(id string, r Reason) *OpError {
	e := opErr(CodeNoRoom, http.StatusConflict, r.Message, map[string]any{"model": id}, nil)
	e.MessageES = r.MessageES
	return e
}

// errRouter maps a failed router call to 502 with LAI-LLM-004 (not answering) or LAI-LLM-005 (our
// key is rejected); any other failure is reported as not answering.
func errRouter(err error) *OpError {
	code := CodeOf(err)
	if code == "" {
		code = CodeRouterDown
	}
	msg := "The AI model service is not answering."
	if code == CodeKeyRejected {
		msg = "The AI model service rejected this computer's key."
	}
	return opErr(code, http.StatusBadGateway, msg, nil, err)
}

func errUnknownPreset(id string) *OpError {
	return opErr(CodePresetFailed, http.StatusInternalServerError,
		"The AI model service does not know this model yet; its entry in the model list is missing.",
		map[string]any{"model": id}, nil)
}

func errLastModel(id string) *OpError {
	return opErr(CodeRefused, http.StatusConflict,
		"In survival mode the last loaded model is kept. Use force to unload it anyway.",
		map[string]any{"model": id}, nil)
}

// errLoadFailed is the final answer of a load that failed and was rolled back (500 LAI-LLM-009 or,
// for a timeout, 504 LAI-LLM-010), or that left no model at all (500 LAI-LLM-012).
func errLoadFailed(id string, f *loadFailure, rolledBack bool, restored []string, nothing bool) *OpError {
	d := map[string]any{
		"model":       id,
		"rolled_back": rolledBack,
		"restored":    append([]string{}, restored...),
		"cause":       f.kind,
	}
	if f.exitCode != nil {
		d["exit_code"] = *f.exitCode
	}
	if f.victim != "" {
		d["unloading"] = f.victim
	}
	switch {
	case f.kind == kindEngineStop:
		return opErr(CodeEngineNotFreed, http.StatusInternalServerError,
			"A model in the way did not stop or free its graphics memory. The previous model was started again.", d, f.err)
	case nothing:
		return opErr(CodeNothingLoaded, http.StatusInternalServerError,
			"No model could be started, not even the previous one.", d, f.err)
	case f.timeout():
		return opErr(CodeLoadTimeout, http.StatusGatewayTimeout,
			"The model took too long to start. The previous model was started again.", d, f.err)
	default:
		return opErr(CodeModelFailed, http.StatusInternalServerError,
			"The model failed to start. The previous model was started again.", d, f.err)
	}
}
