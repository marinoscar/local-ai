package auth

import "net/http"

// PortalCookie names the restricted Portal session cookie (survival-portal.md section 3.3).
const PortalCookie = "lai_portal"

// PortalSessions is the hook through which principal resolution reaches the Portal session store
// (step 5, fourth bullet). AGENT-06 owns only this interface and the "portal" principal kind; the
// store, the cookie issue in GET /portal, the route allowlist, the Origin rule, the rate limits and
// the drop on leaving survival belong to PORTAL-02, which implements it. The fake in auth/fake
// stands in for tests.
type PortalSessions interface {
	// Lookup resolves a lai_portal cookie value to a session id. ok is false for an unknown or
	// expired cookie, which then resolves to anonymous.
	Lookup(cookie string) (id string, ok bool)
	// Allowed reports whether a Portal session may be used for this request: the routes and
	// restrictions of survival-portal.md section 3.3. A request it refuses answers 401 LAI-SVC-110
	// on a route that needs authentication.
	Allowed(r *http.Request) bool
	// DropAll invalidates every Portal session (the mode left survival).
	DropAll()
}
