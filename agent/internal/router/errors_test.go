package router

import (
	"errors"
	"net/http"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// TestEveryDocumentedErrorMapsToItsStatus pins the table of agent.md section 3.8.8 to 3.8.10: the
// code and the HTTP status of every error a load, unload or default can end with.
func TestEveryDocumentedErrorMapsToItsStatus(t *testing.T) {
	fail := &loadFailure{kind: kindFailed}
	timeout := &loadFailure{kind: kindTimeout}
	cases := []struct {
		name string
		err  *OpError
		code string
		stat int
	}{
		{"unknown model", errNotFound("x"), doctor.AGT021, http.StatusNotFound},
		{"busy", errBusy(&BusyError{OpID: "op_aaaaaaaaaaa2", Kind: HolderLoad}), doctor.AGT022, http.StatusConflict},
		{"single model in survival", errLastModel("x"), doctor.AGT023, http.StatusConflict},
		{"does not fit", errNotLoadable("x", &Reason{Code: CodeNotFit, Message: "no"}), doctor.CAT005, http.StatusUnprocessableEntity},
		{"files missing", errNotLoadable("x", &Reason{Code: CodeFilesMissing, Message: "no"}), doctor.LLM008, http.StatusUnprocessableEntity},
		{"not a llama model", errNotLlama("x", "whisper"), doctor.CAT005, http.StatusUnprocessableEntity},
		{"no room", errNoRoom("x", reasonNoRoom()), doctor.LLM011, http.StatusConflict},
		{"router down", errRouter(ErrDown), doctor.LLM004, http.StatusBadGateway},
		{"router rejects the key", errRouter(ErrUnauthorized), doctor.LLM005, http.StatusBadGateway},
		{"router error of another kind", errRouter(errors.New("boom")), doctor.LLM004, http.StatusBadGateway},
		{"load failed, rolled back", errLoadFailed("x", fail, true, []string{"a"}, false), doctor.LLM009, http.StatusInternalServerError},
		{"load timed out, rolled back", errLoadFailed("x", timeout, true, []string{"a"}, false), doctor.LLM010, http.StatusGatewayTimeout},
		{"nothing at all", errLoadFailed("x", fail, false, nil, true), doctor.LLM012, http.StatusInternalServerError},
		{"nothing at all after a timeout", errLoadFailed("x", timeout, false, nil, true), doctor.LLM012, http.StatusInternalServerError},
		{"router does not list the model", errUnknownPreset("x"), doctor.LLM007, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.err.Code) != tc.code || tc.err.HTTPStatus() != tc.stat {
				t.Errorf("got %s %d, want %s %d", tc.err.Code, tc.err.HTTPStatus(), tc.code, tc.stat)
			}
			if tc.err.Details == nil || tc.err.Message == "" {
				t.Errorf("details %v message %q", tc.err.Details, tc.err.Message)
			}
			if !doctor.Emittable(string(tc.err.Code)) {
				t.Errorf("%s is not in the doctor table", tc.err.Code)
			}
			if tc.err.Error() == "" {
				t.Error("empty Error()")
			}
		})
	}
}

func TestBusyErrorIsReachableAndCarriesTheHolder(t *testing.T) {
	be := &BusyError{OpID: "op_aaaaaaaaaaa2", Kind: HolderUnload}
	oe := errBusy(be)
	var got *BusyError
	if !errors.As(oe, &got) || got != be {
		t.Error("BusyError is not reachable through the OpError")
	}
	if oe.Details["op_id"] != "op_aaaaaaaaaaa2" || oe.Details["running"] != HolderUnload {
		t.Errorf("details: %v", oe.Details)
	}
	if d := errBusy(&BusyError{}).Details; len(d) != 0 {
		t.Errorf("an unknown holder invented details: %v", d)
	}
}

func TestLoadFailedDetails(t *testing.T) {
	code := 137
	oe := errLoadFailed("m", &loadFailure{kind: kindFailed, exitCode: &code, victim: "v"}, true, []string{"a", "b"}, false)
	if oe.Details["model"] != "m" || oe.Details["rolled_back"] != true || oe.Details["exit_code"] != 137 || oe.Details["unloading"] != "v" || oe.Details["cause"] != kindFailed {
		t.Errorf("details: %v", oe.Details)
	}
	if got, _ := oe.Details["restored"].([]string); len(got) != 2 {
		t.Errorf("restored: %v", oe.Details["restored"])
	}
	if e := errLoadFailed("m", &loadFailure{kind: kindFailed}, false, nil, true); e.Details["restored"] == nil {
		t.Error("restored must be an empty list, not null")
	}
}
