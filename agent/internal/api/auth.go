package api

import "net/http"

// Level is the authentication level a route requires (agent.md section 3.8 legend).
type Level string

// The five levels. A route lists the levels it accepts; satisfying any one is enough.
const (
	// AuthNone: anonymous allowed.
	AuthNone Level = "none"
	// AuthOp: an operator (bearer token, operator session, or a socket peer in localai-operators).
	AuthOp Level = "op"
	// AuthPortal: the restricted Portal session (cookie lai_portal).
	AuthPortal Level = "portal"
	// AuthAdmin: admin (socket uid 0, or an operator with a valid PIN grant).
	AuthAdmin Level = "admin"
	// AuthAdminStar: admin, or a socket peer in localai-operators (modes.md section 2.4).
	AuthAdminStar Level = "admin*"
)

// Valid reports whether l is one of the five levels.
func (l Level) Valid() bool {
	switch l {
	case AuthNone, AuthOp, AuthPortal, AuthAdmin, AuthAdminStar:
		return true
	}
	return false
}

// PrincipalKind is who a request acts as.
type PrincipalKind string

// The kinds of principal a request can have (agent.md section 4.11, audit actor kinds).
const (
	KindAnonymous PrincipalKind = "anonymous"
	KindOperator  PrincipalKind = "operator"
	KindAdmin     PrincipalKind = "admin"
	KindPortal    PrincipalKind = "portal"
)

// Credential is what the principal proved itself with. The CSRF rule (step 8) applies to the cookie
// credentials only: a bearer token or a socket peer cannot be attached by a browser.
type Credential string

// The credentials.
const (
	CredNone    Credential = ""
	CredSocket  Credential = "socket"  // SO_PEERCRED on the unix socket
	CredBearer  Credential = "bearer"  // Authorization: Bearer lat_...
	CredSession Credential = "session" // cookie localai_session
	CredPortal  Credential = "portal"  // cookie lai_portal
)

// Principal is the result of authentication (steps 5 and 6).
type Principal struct {
	Kind       PrincipalKind
	Credential Credential
	// Via refines the kind for logs and audit: "bearer", "session", "uid=1000", "pin", ...
	Via string
}

// Anonymous is the principal of a request without credentials.
func Anonymous() Principal { return Principal{Kind: KindAnonymous} }

// String is the access-log form: "anonymous", "operator:bearer", "operator:uid=1000", "admin:pin".
func (p Principal) String() string {
	kind := p.Kind
	if kind == "" {
		kind = KindAnonymous
	}
	if p.Via == "" {
		return string(kind)
	}
	return string(kind) + ":" + p.Via
}

// Satisfies reports whether p meets at least one of levels. An admin also satisfies op. admin* is
// satisfied by an admin, or by an operator that came in over the socket (a member of
// localai-operators, agent.md section 3.8).
func (p Principal) Satisfies(levels []Level) bool {
	for _, l := range levels {
		switch l {
		case AuthNone:
			return true
		case AuthOp:
			if p.Kind == KindOperator || p.Kind == KindAdmin {
				return true
			}
		case AuthAdmin:
			if p.Kind == KindAdmin {
				return true
			}
		case AuthAdminStar:
			if p.Kind == KindAdmin || (p.Kind == KindOperator && p.Credential == CredSocket) {
				return true
			}
		case AuthPortal:
			if p.Kind == KindPortal {
				return true
			}
		}
	}
	return false
}

// Authenticator is the seam for steps 5 to 7 of the middleware chain (agent.md section 3.9.1):
// principal resolution (socket peer, bearer, session cookie, Portal cookie), the PIN grant and the
// auth decision. AGENT-06 implements it; this package ships AllowAll, DenyAll and AuthFunc.
//
// Authenticate returns the principal, or an error to answer instead (an *Error is written as is; any
// other error becomes 500). It reads the transport and socket peer from InfoFrom(r.Context()). It is
// called for every matched route, including those at level none, because the socket-peer rule
// (403 when the peer is not an operator) applies to every request. The server re-checks the returned
// principal against route.Auth with Principal.Satisfies and refuses a principal that does not
// satisfy it, so an Authenticator may return the principal and leave the decision to the server.
type Authenticator interface {
	Authenticate(r *http.Request, route *Route) (Principal, error)
}

// AuthFunc adapts a function to an Authenticator.
type AuthFunc func(r *http.Request, route *Route) (Principal, error)

// Authenticate calls f.
func (f AuthFunc) Authenticate(r *http.Request, route *Route) (Principal, error) { return f(r, route) }

// AllowAll authenticates every request as an admin. It exists for tests and must never be wired
// into the daemon: it makes every route reachable by anyone who can connect.
type AllowAll struct{}

// Authenticate returns an admin principal with Via "allow-all".
func (AllowAll) Authenticate(*http.Request, *Route) (Principal, error) {
	return Principal{Kind: KindAdmin, Credential: CredBearer, Via: "allow-all"}, nil
}

// DenyAll authenticates nobody: routes at level none see an anonymous principal, every other route
// answers 401. It is the server's default when no Authenticator is given, so a server without auth
// fails closed.
type DenyAll struct{}

// Authenticate returns the anonymous principal, or 401 for a route that needs authentication.
func (DenyAll) Authenticate(_ *http.Request, route *Route) (Principal, error) {
	if route != nil && route.accepts(AuthNone) {
		return Anonymous(), nil
	}
	return Anonymous(), NewError(CodeUnauthenticated, http.StatusUnauthorized, nil)
}
