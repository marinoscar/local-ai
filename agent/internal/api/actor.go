package api

import "github.com/marinoscar/local-ai/agent/internal/audit"

// AuditActor maps the request's principal to the actor an audit record carries: the kind, how the
// credential was presented (bearer, session, socket, pin, portal) and the socket peer's uid. Handlers
// outside this package (the status, profile and catalog routes) use it so that every route records
// the caller the same way.
func AuditActor(info *RequestInfo) audit.Actor { return serviceActor(info) }
