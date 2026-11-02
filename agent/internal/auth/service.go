package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Canonical paths of the secret files (overview section 3, agent.md section 3.3).
const (
	DefaultTokenPath = "/etc/localai/agent-token"
	DefaultPINPath   = "/etc/localai/admin-pin"
)

// Auditor is where audit events go. *audit.Log satisfies it. A failed write never fails a request:
// the audit log already mirrors the record to the journal and reports its own failure.
type Auditor interface{ Write(audit.Event) error }

// Config configures a Service. Every field is optional.
type Config struct {
	// TokenPath and PINPath default to the canonical /etc/localai files.
	TokenPath, PINPath string
	// FS is the filesystem seam; nil means the real one.
	FS config.FS
	// Clock is the time source; nil means the wall clock.
	Clock Clock
	// Groups looks up group membership of socket peers; nil means os/user.
	Groups GroupLookup
	// OperatorsGroup defaults to localai-operators.
	OperatorsGroup string
	// Portal is the Portal session hook; nil means there are no Portal sessions (the cookie is ignored).
	Portal PortalSessions
	// Audit receives session.create, ui_code.mint and pin.verify.*; nil discards them.
	Audit Auditor
	// Alert raises alerts (the PIN lockout); nil discards them.
	Alert AlertFunc
	// Logger gets component=auth lines; nil means slog.Default.
	Logger *slog.Logger
	// TokenPolicy and PINPolicy override the permission policy of the two files (tests run as an
	// ordinary user). Nil means config.PolicyAgentToken with the operators group as owner, and
	// config.PolicyAdminPIN.
	TokenPolicy, PINPolicy *config.SecretPolicy
}

// Service is the agent's authentication: it implements api.Authenticator and serves the session,
// UI-code and PIN routes.
type Service struct {
	log      *slog.Logger
	clock    Clock
	audit    Auditor
	portal   PortalSessions
	groups   *groupCache
	tokens   *TokenStore
	sessions *Sessions
	codes    *UICodes
	codeLim  *codeLimiter
	pin      *PIN
	grants   *Grants
}

var _ api.Authenticator = (*Service)(nil)

// New builds the service. It does not touch the filesystem; call EnsureToken at startup.
func New(cfg Config) *Service {
	if cfg.TokenPath == "" {
		cfg.TokenPath = DefaultTokenPath
	}
	if cfg.PINPath == "" {
		cfg.PINPath = DefaultPINPath
	}
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.Groups == nil {
		cfg.Groups = OSGroups{}
	}
	if cfg.OperatorsGroup == "" {
		cfg.OperatorsGroup = OperatorsGroup
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "auth")

	tokenPolicy := config.PolicyAgentToken
	if cfg.TokenPolicy != nil {
		tokenPolicy = *cfg.TokenPolicy
	} else if gid, err := cfg.Groups.GroupID(cfg.OperatorsGroup); err == nil {
		tokenPolicy.GID = gid
	} else {
		log.Warn("the operators group does not exist; the agent token will be root-only", "group", cfg.OperatorsGroup, "error", err.Error())
	}
	pinPolicy := config.PolicyAdminPIN
	if cfg.PINPolicy != nil {
		pinPolicy = *cfg.PINPolicy
	}

	s := &Service{
		log:      log,
		clock:    cfg.Clock,
		audit:    cfg.Audit,
		portal:   cfg.Portal,
		groups:   newGroupCache(cfg.Groups, cfg.OperatorsGroup, cfg.Clock),
		tokens:   NewTokenStore(cfg.FS, cfg.TokenPath, tokenPolicy, cfg.Clock, log),
		sessions: NewSessions(cfg.Clock),
		codes:    NewUICodes(cfg.Clock),
		codeLim:  newCodeLimiter(),
		pin:      NewPIN(cfg.FS, cfg.PINPath, pinPolicy, cfg.Clock, log, cfg.Alert),
		grants:   NewGrants(cfg.Clock),
	}
	// A rotated token ends the grants earned with the old one.
	s.tokens.OnChange(func() { s.grants.DropPrefix("bearer:") })
	return s
}

// EnsureToken loads the agent token, creating the file when it is absent or empty. created is true
// when a new token was written: the caller raises the LAI-SEC-011 alert (agent.md section 8.4).
func (s *Service) EnsureToken() (created bool, err error) { return s.tokens.Ensure() }

// Tokens, Sessions, Grants and PIN expose the stores for the daemon's wiring and for tests.
func (s *Service) Tokens() *TokenStore { return s.tokens }

// Sessions returns the operator session store.
func (s *Service) Sessions() *Sessions { return s.sessions }

// Grants returns the PIN grant store.
func (s *Service) Grants() *Grants { return s.grants }

// UICodes returns the login-code store.
func (s *Service) UICodes() *UICodes { return s.codes }

// PIN returns the PIN verifier.
func (s *Service) PIN() *PIN { return s.pin }

// Authenticate implements api.Authenticator: principal resolution (step 5), the PIN grant (step 6)
// and the auth decision (step 7) of agent.md section 3.9.1. It always returns the resolved
// principal, even with an error, so the access log and audit know the caller.
func (s *Service) Authenticate(r *http.Request, rt *api.Route) (api.Principal, error) {
	info := api.InfoFrom(r.Context())

	var p api.Principal
	var deny *api.Error // what an anonymous caller is told on a route that needs authentication
	if info.Transport == api.TransportSocket {
		var perr *api.Error
		if p, perr = s.socketPrincipal(info.Peer); perr != nil {
			return p, perr // 403 LAI-SEC-013: applies to every request on the socket
		}
	} else {
		p, deny = s.tcpPrincipal(r)
	}

	// Step 6: the PIN grant lifts an operator to admin when it was earned by this very principal.
	grantHeader := r.Header.Get(GrantHeader)
	grantInvalid := false
	if grantHeader != "" && p.Kind == api.KindOperator {
		if s.grants.Valid(grantHeader, Binding(p)) {
			p.Kind, p.Via = api.KindAdmin, audit.ViaPIN
		} else {
			grantInvalid = true
		}
	}

	// Step 7: the decision. A level-none route lets everyone in; the server re-checks the rest.
	if rt == nil || rt.Auth == nil || rtAccepts(rt, api.AuthNone) || p.Satisfies(rt.Auth) {
		return p, nil
	}
	switch p.Kind {
	case api.KindOperator, api.KindAdmin:
		if grantInvalid {
			return p, api.NewError(api.CodeAdminRequired, http.StatusForbidden, map[string]any{"reason": "grant_expired"})
		}
		return p, api.NewError(api.CodeAdminRequired, http.StatusForbidden, map[string]any{"pin_required": true})
	case api.KindPortal:
		return p, api.NewError(api.CodePortalSessionEnded, http.StatusUnauthorized, nil)
	}
	if deny != nil {
		return p, deny
	}
	return p, api.NewError(api.CodeUnauthenticated, http.StatusUnauthorized, nil)
}

func rtAccepts(rt *api.Route, l api.Level) bool {
	for _, a := range rt.Auth {
		if a == l {
			return true
		}
	}
	return false
}

// tcpPrincipal resolves the credentials of a TCP request: bearer token, then the session cookie,
// then the Portal cookie. A credential that does not check out is skipped, not refused: the caller
// is anonymous, and a route that needs more answers 401 (a stale token must not break a survival
// route that needs none). deny names the better 401 for an anonymous caller who holds a cookie that
// is known but unusable: LAI-SEC-012 for an expired session, LAI-SVC-110 for a Portal session used
// where it is not allowed.
func (s *Service) tcpPrincipal(r *http.Request) (p api.Principal, deny *api.Error) {
	// 2. Authorization: Bearer lat_...
	if tok, ok := bearerToken(r); ok && s.tokens.Verify(tok) {
		return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: audit.ViaBearer, TokenFingerprint: TokenFingerprint(tok)}, nil
	}
	// 3. cookie localai_session
	if c, err := r.Cookie(SessionCookie); err == nil {
		switch id, state := s.sessions.Lookup(c.Value); state {
		case SessionValid:
			return api.Principal{Kind: api.KindOperator, Credential: api.CredSession, Via: audit.ViaSession, SessionID: id}, nil
		case SessionExpired:
			deny = api.NewError(api.CodeSessionExpired, http.StatusUnauthorized, nil)
		}
	}
	// 4. cookie lai_portal
	if c, err := r.Cookie(PortalCookie); err == nil && s.portal != nil {
		if id, ok := s.portal.Lookup(c.Value); ok {
			if s.portal.Allowed(r) {
				return api.Principal{Kind: api.KindPortal, Credential: api.CredPortal, Via: audit.ViaPortal, PortalSessionID: id}, nil
			}
			if deny == nil {
				deny = api.NewError(api.CodePortalSessionEnded, http.StatusUnauthorized, nil)
			}
		}
	}
	// 5. none
	return api.Anonymous(), deny
}

// bearerToken extracts the token of an Authorization: Bearer lat_... header. Anything else (another
// scheme, a token without the lat_ prefix, an empty value) is not a candidate.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(rest)
	if !strings.HasPrefix(tok, TokenPrefix) {
		return "", false
	}
	return tok, true
}
