// Package auth decides who is calling the agent and what they may do (agent.md section 3.9).
//
// It implements api.Authenticator (steps 5 to 7 of the middleware chain) and registers the routes
// that create sessions, mint login codes and verify the admin PIN:
//
//   - the agent token (token.go): lat_ + base64url of 32 random bytes in /etc/localai/agent-token,
//     cached and re-read when its mtime changes;
//   - the socket peer (peer.go): uid 0 is admin, a member of localai-operators is operator;
//   - operator sessions (session.go): memory only, idle 12 h, absolute 7 d;
//   - login (UI) codes (uicode.go): six digits, single use, five minutes, at most three alive;
//   - the admin PIN (pin.go): argon2id PHC string in /etc/localai/admin-pin, re-read on every
//     verification, with the rate limit and lockout of ratelimit.go;
//   - PIN grants (grant.go): pg_ + base64url, five minutes, bound to the principal that earned them;
//   - the Portal hook (portalhook.go): a PortalSessions interface the Portal epic implements.
//
// Everything is local: no network, no clock but the injected one. Every secret comparison uses
// crypto/subtle, and no secret reaches a log line, the audit log or an error message; credentials
// appear only as a fingerprint (lat_...last4).
//
// The package imports api, not the other way round, so the route handlers of agent.md section 3.8.2
// and 3.8.3 live here (routes.go) and are registered through api.Registry.
package auth
