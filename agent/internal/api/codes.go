package api

// Doctor codes this package emits (agent.md sections 3.7 and 8, doctor-codes.md).
//
// They live in this one file on purpose: scripts/check-doctor-codes.sh forbids a "LAI-..." literal in
// any non-test Go file outside internal/doctor/codes.go once that file exists. AGENT-11 replaces
// these constants with the ones from internal/doctor; nothing else in this package spells a code.
const (
	// CodeSocketBind: the unix socket could not be created (a bind error, not an HTTP status).
	CodeSocketBind = "LAI-AGT-005"
	// CodeTCPBind: the TCP port is used by another program (a bind error, not an HTTP status).
	CodeTCPBind = "LAI-AGT-006"
	// CodeInvalidRequest: malformed JSON, unknown field, invalid value, wrong content type, wrong method.
	CodeInvalidRequest = "LAI-AGT-020"
	// CodeNotFound: unknown route or entity.
	CodeNotFound = "LAI-AGT-021"
	// CodeBusy: another operation is already running.
	CodeBusy = "LAI-AGT-022"
	// CodeNotInMode: this action is not allowed in the current mode.
	CodeNotInMode = "LAI-AGT-023"
	// CodeStarting: the agent is starting or stopping (503 with Retry-After: 5).
	CodeStarting = "LAI-AGT-024"
	// CodeTooLarge: the request body is over the limit.
	CodeTooLarge = "LAI-AGT-025"
	// CodeInternal: a bug; details.request_id is always present.
	CodeInternal = "LAI-AGT-099"

	// CodeUnauthenticated: missing or invalid credentials.
	CodeUnauthenticated = "LAI-SEC-001"
	// CodeAdminRequired: the action needs the admin PIN.
	CodeAdminRequired = "LAI-SEC-002"
	// CodeRateLimited: too many wrong PIN or login-code attempts.
	CodeRateLimited = "LAI-SEC-004"
	// CodeBadHost: the Host header is not the agent's own address (DNS rebinding defence).
	CodeBadHost = "LAI-SEC-006"
	// CodeBadOrigin: the request came from another website.
	CodeBadOrigin = "LAI-SEC-007"
	// CodePeerNotOperator: the socket peer is not in localai-operators.
	CodePeerNotOperator = "LAI-SEC-013"

	// CodePortalSessionEnded: a Portal session was used where it is not allowed, or has ended.
	CodePortalSessionEnded = "LAI-SVC-110"

	// CodeRouterDown: the llama router is running but not answering (502).
	CodeRouterDown = "LAI-LLM-004"
	// CodeUpstreamTimeout: an upstream took too long (504).
	CodeUpstreamTimeout = "LAI-LLM-010"
)
