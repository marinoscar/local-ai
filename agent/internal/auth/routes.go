package auth

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
)

// routeTimeout is the handler deadline of every route of this file (agent.md section 3.8 table).
// The PIN route is one argon2 verification (64 MiB, t = 3): about a second on the slowest node.
const routeTimeout = 5 * time.Second

// Routes returns the routes of agent.md sections 3.8.2 and 3.8.3: POST /session, POST /session/code,
// DELETE /session, POST /ui-code and POST /pin/verify.
func (s *Service) Routes() []api.Route {
	return []api.Route{
		{Method: http.MethodPost, Pattern: "/session", Auth: []api.Level{api.AuthOp}, Timeout: routeTimeout, Handler: api.HandlerFunc(s.createSession)},
		{Method: http.MethodPost, Pattern: "/session/code", Auth: []api.Level{api.AuthNone}, Timeout: routeTimeout, Handler: api.HandlerFunc(s.sessionFromCode)},
		{Method: http.MethodDelete, Pattern: "/session", Auth: []api.Level{api.AuthOp}, Timeout: routeTimeout, Handler: api.HandlerFunc(s.deleteSession)},
		{Method: http.MethodPost, Pattern: "/ui-code", Auth: []api.Level{api.AuthAdminStar}, Timeout: routeTimeout, Handler: api.HandlerFunc(s.mintUICode)},
		{Method: http.MethodPost, Pattern: "/pin/verify", Auth: []api.Level{api.AuthOp}, Timeout: routeTimeout, Handler: api.HandlerFunc(s.verifyPIN)},
	}
}

// Register adds the routes of svc to reg (api.DefaultRegistry when reg is nil). The daemon wiring
// (AGENT-20) calls it once, and passes svc to api.Options.Auth.
func Register(reg *api.Registry, svc *Service) error {
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(svc.Routes()...)
}

// empty is the body of the routes that take none: {} (a strict decode refuses any field).
type empty struct{}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

// POST /session: a session from the agent token. Bearer only: a cookie or socket credential does
// not mint another cookie.
func (s *Service) createSession(w http.ResponseWriter, r *http.Request) error {
	p := api.InfoFrom(r.Context()).Principal
	if p.Credential != api.CredBearer {
		return api.NewError(api.CodeUnauthenticated, http.StatusUnauthorized, map[string]any{"reason": "bearer_required"})
	}
	if err := api.DecodeJSONOrEmpty(w, r, &empty{}); err != nil {
		return err
	}
	cookie, _ := s.sessions.Create()
	s.record(r, audit.Event{Action: audit.ActionSessionCreate, Result: audit.ResultOK, Actor: actorOf(p), Target: map[string]string{"method": "bearer"}})
	noStore(w)
	w.Header().Add("Set-Cookie", SessionCookieHeader(cookie))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /session/code: a session from a login code. Level none, so the failure limiter guards it:
// 5 wrong codes in 5 minutes, counted for everyone, close it with 429 until the window frees.
func (s *Service) sessionFromCode(w http.ResponseWriter, r *http.Request) error {
	now := s.clock.Now()
	if wait, blocked := s.codeLim.blocked(now); blocked {
		return rateLimited(wait, false)
	}
	var body struct {
		Code *string `json:"code"`
	}
	if err := api.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Code == nil {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"reason": "missing_field", "field": "code"})
	}
	// A code that is not six digits cannot be a guess at a code: refuse it without counting.
	if !validCodeFormat(*body.Code) {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"reason": "invalid_value", "field": "code"})
	}
	if !s.codes.Consume(*body.Code) {
		s.codeLim.fail(now)
		s.log.Warn("a login code was refused", "code", api.CodeBadLoginCode, "req_id", api.RequestID(r))
		return api.NewError(api.CodeBadLoginCode, http.StatusUnauthorized, nil)
	}
	cookie, _ := s.sessions.Create()
	actor := audit.Anonymous()
	actor.Via = "ui_code"
	s.record(r, audit.Event{Action: audit.ActionSessionCreate, Result: audit.ResultOK, Actor: actor, Target: map[string]string{"method": "ui_code"}})
	noStore(w)
	w.Header().Add("Set-Cookie", SessionCookieHeader(cookie))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// DELETE /session: ends the caller's own session, and the PIN grants earned with it.
func (s *Service) deleteSession(w http.ResponseWriter, r *http.Request) error {
	p := api.InfoFrom(r.Context()).Principal
	if p.Credential != api.CredSession {
		return api.NewError(api.CodeUnauthenticated, http.StatusUnauthorized, map[string]any{"reason": "session_cookie_required"})
	}
	s.sessions.Delete(p.SessionID)
	s.grants.DropBinding(Binding(p))
	noStore(w)
	w.Header().Add("Set-Cookie", ClearSessionCookieHeader())
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// POST /ui-code: mints a login code for the local UI.
func (s *Service) mintUICode(w http.ResponseWriter, r *http.Request) error {
	if err := api.DecodeJSONOrEmpty(w, r, &empty{}); err != nil {
		return err
	}
	p := api.InfoFrom(r.Context()).Principal
	code, expires := s.codes.Mint()
	// The code itself is never audited or logged: only that one was minted, and by whom.
	s.record(r, audit.Event{Action: audit.ActionUICodeMint, Result: audit.ResultOK, Actor: actorOf(p)})
	noStore(w)
	api.WriteJSON(w, r, http.StatusOK, map[string]any{"code": code, "expires_at": expires.UTC().Format(time.RFC3339)})
	return nil
}

// POST /pin/verify: checks the admin PIN and, when right, returns a grant bound to the caller.
func (s *Service) verifyPIN(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		PIN *string `json:"pin"`
	}
	if err := api.DecodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.PIN == nil {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"reason": "missing_field", "field": "pin"})
	}
	p := api.InfoFrom(r.Context()).Principal
	binding := Binding(p)
	if binding == "" { // an operator always has one; a principal without cannot hold a grant
		return api.NewError(api.CodeUnauthenticated, http.StatusUnauthorized, nil)
	}

	err := s.pin.Verify(*body.PIN)
	var wrong *api.Error
	switch {
	case err == nil:
		grant, expires := s.grants.Mint(binding)
		s.record(r, audit.Event{Action: audit.ActionPINVerifyOK, Result: audit.ResultOK, Actor: actorOf(p)})
		noStore(w)
		api.WriteJSON(w, r, http.StatusOK, map[string]any{"grant": grant, "expires_at": expires.UTC().Format(time.RFC3339)})
		return nil
	case errors.Is(err, ErrPINNotSet):
		return api.NewError(api.CodeNoPIN, http.StatusConflict, nil)
	default:
		var rl *RateLimitedError
		var wp *WrongPINError
		switch {
		case errors.As(err, &rl):
			if rl.Started { // only the request that started a lockout is audited, not every refusal after it
				s.record(r, audit.Event{Action: audit.ActionPINVerifyFail, Result: audit.ResultDenied, Code: api.CodeRateLimited, Actor: actorOf(p), Target: map[string]string{"locked": "true"}})
			}
			wrong = rateLimited(rl.RetryAfter, rl.Locked)
		case errors.As(err, &wp):
			s.record(r, audit.Event{Action: audit.ActionPINVerifyFail, Result: audit.ResultFail, Code: api.CodeWrongPIN, Actor: actorOf(p), Target: map[string]string{"remaining_attempts": strconv.Itoa(wp.Remaining)}})
			wrong = api.NewError(api.CodeWrongPIN, http.StatusUnauthorized, map[string]any{"remaining_attempts": wp.Remaining})
		default:
			return err
		}
	}
	return wrong
}

// rateLimited builds the 429 LAI-SEC-004 answer with Retry-After.
func rateLimited(wait time.Duration, locked bool) *api.Error {
	secs := retrySeconds(wait)
	e := api.NewError(api.CodeRateLimited, http.StatusTooManyRequests, map[string]any{"retry_after_s": secs, "locked": locked})
	e.RetryAfter = secs
	return e
}

// record writes an audit event stamped with the request id. A failure is not the caller's problem:
// audit.Log has already mirrored the record to the journal and reports its own health.
func (s *Service) record(r *http.Request, ev audit.Event) {
	if s.audit == nil {
		return
	}
	ev.ReqID = api.RequestID(r)
	if err := s.audit.Write(ev); err != nil {
		s.log.Debug("audit write failed", "action", ev.Action, "error", err.Error())
	}
}

// actorOf maps a principal to the audit actor: its kind, how it authenticated, the peer uid on the
// socket, and the token fingerprint (never the token).
func actorOf(p api.Principal) audit.Actor {
	via := p.Via
	switch {
	case p.Kind == api.KindAdmin && p.Via == audit.ViaPIN: // lifted by a grant: keep "pin"
	case p.Credential == api.CredBearer:
		via = audit.ViaBearer
	case p.Credential == api.CredSession:
		via = audit.ViaSession
	case p.Credential == api.CredSocket:
		via = audit.ViaSocket
	}
	var a audit.Actor
	switch p.Kind {
	case api.KindAdmin:
		a = audit.Admin(via)
	case api.KindOperator:
		a = audit.Operator(via)
	case api.KindPortal:
		a = audit.Portal()
	default:
		a = audit.Anonymous()
	}
	if p.Credential == api.CredSocket {
		a = a.WithUID(p.UID)
	}
	a.Fingerprint = p.TokenFingerprint
	return a
}
