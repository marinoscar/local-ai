package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Handlers of GET /cloud, POST /cloud/enroll, DELETE /cloud and POST /cloud/resend-key (agent.md
// section 3.8.26). They decode and authorize, call the cloud link through the CloudLink interface
// below and write what it answers; the link does the work (internal/cloud, agent.md section 4.8).
// internal/cloud imports this package for the wire types and the *Error values, so the link is an
// interface here.

// CloudLink is what the /cloud routes need from *cloud.Link. Every error it returns is an *Error
// carrying the documented code, status and details (a plain error is a bug and answers 500):
// enroll answers 422 LAI-CLD-051 when the cloud rejects the credential and 422 LAI-CLD-062 for an
// address that is not https, resend-key answers 409 LAI-AGT-023 when the node is not linked.
type CloudLink interface {
	// CloudStatus is the body of GET /cloud. It never blocks and never calls the cloud.
	CloudStatus() Cloud
	// Enroll stores the credential, registers synchronously and starts the link.
	Enroll(ctx context.Context, url, credential string) (CloudEnrolled, error)
	// Unenroll deregisters (3 s, errors ignored), deletes the credential and link.json.
	Unenroll(ctx context.Context) error
	// ResendKey pushes the llama key to the cloud now.
	ResendKey(ctx context.Context) (ResendKeyResult, error)
}

// CloudDeps are what the /cloud routes need. Link is required.
type CloudDeps struct {
	Link CloudLink
	// Audit records cloud.enroll and cloud.unenroll; optional.
	Audit AuditWriter
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// CloudRoutes returns GET /cloud (op, 5 s), POST /cloud/enroll (admin, 30 s), DELETE /cloud (admin,
// 15 s) and POST /cloud/resend-key (admin, 15 s).
func CloudRoutes(d CloudDeps) []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/cloud", Auth: []Level{AuthOp}, Timeout: 5 * time.Second, Handler: HandlerFunc(d.get)},
		{Method: http.MethodDelete, Pattern: "/cloud", Auth: []Level{AuthAdmin}, Timeout: 15 * time.Second, Handler: HandlerFunc(d.unenroll)},
		{Method: http.MethodPost, Pattern: "/cloud/enroll", Auth: []Level{AuthAdmin}, Timeout: 30 * time.Second, Handler: HandlerFunc(d.enroll)},
		{Method: http.MethodPost, Pattern: "/cloud/resend-key", Auth: []Level{AuthAdmin}, Timeout: 15 * time.Second, Handler: HandlerFunc(d.resendKey)},
	}
}

// RegisterCloud adds the /cloud routes to reg (DefaultRegistry when nil). The wiring in internal/app
// calls it (AGENT-20); the package does not register anything at init time.
func RegisterCloud(reg *Registry, d CloudDeps) error {
	if d.Link == nil {
		return errors.New("api: the cloud routes need a cloud link")
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(CloudRoutes(d)...)
}

func (d CloudDeps) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger.With("component", "api")
	}
	return slog.Default().With("component", "api")
}

func (d CloudDeps) get(w http.ResponseWriter, r *http.Request) error {
	if d.Link == nil {
		return NewError(CodeInternal, http.StatusInternalServerError, nil)
	}
	WriteJSON(w, r, http.StatusOK, d.Link.CloudStatus())
	return nil
}

func (d CloudDeps) enroll(w http.ResponseWriter, r *http.Request) error {
	if d.Link == nil {
		return NewError(CodeInternal, http.StatusInternalServerError, nil)
	}
	var body CloudEnrollRequest
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	body.URL, body.Credential = strings.TrimSpace(body.URL), strings.TrimSpace(body.Credential)
	for field, v := range map[string]string{"url": body.URL, "credential": body.Credential} {
		if v == "" {
			return NewError(CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": "required"})
		}
	}
	info := InfoFrom(r.Context())
	res, err := d.Link.Enroll(r.Context(), body.URL, body.Credential)
	target := map[string]string{"url": body.URL, "credential_fingerprint": logx.Redact(body.Credential)}
	if err != nil {
		d.audit(info, audit.ActionCloudEnroll, audit.ResultFail, errorCode(err), target)
		return err
	}
	target["state"] = res.State
	d.audit(info, audit.ActionCloudEnroll, audit.ResultOK, "", target)
	WriteJSON(w, r, http.StatusOK, res)
	return nil
}

func (d CloudDeps) unenroll(w http.ResponseWriter, r *http.Request) error {
	if d.Link == nil {
		return NewError(CodeInternal, http.StatusInternalServerError, nil)
	}
	info := InfoFrom(r.Context())
	if err := d.Link.Unenroll(r.Context()); err != nil {
		d.audit(info, audit.ActionCloudUnenroll, audit.ResultFail, errorCode(err), nil)
		return err
	}
	d.audit(info, audit.ActionCloudUnenroll, audit.ResultOK, "", nil)
	WriteJSON(w, r, http.StatusOK, CloudUnenrolled{Enrolled: false})
	return nil
}

func (d CloudDeps) resendKey(w http.ResponseWriter, r *http.Request) error {
	if d.Link == nil {
		return NewError(CodeInternal, http.StatusInternalServerError, nil)
	}
	var body struct{}
	if err := DecodeJSON(w, r, &body); err != nil {
		return err
	}
	res, err := d.Link.ResendKey(r.Context())
	if err != nil {
		return err
	}
	WriteJSON(w, r, http.StatusOK, res)
	return nil
}

// errorCode is the doctor code of an *Error, "" for anything else.
func errorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// audit records ev, logging a failure to write instead of failing the request.
func (d CloudDeps) audit(info *RequestInfo, action string, result audit.Result, code string, target map[string]string) {
	if d.Audit == nil {
		return
	}
	ev := audit.Event{Action: action, Result: result, Code: code, Actor: AuditActor(info), Target: target, ReqID: info.ID}
	if err := d.Audit.Write(ev); err != nil {
		d.log().Warn("audit write failed", "action", action, "error", err.Error())
	}
}
