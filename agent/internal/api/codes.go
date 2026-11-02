package api

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package emits (agent.md sections 3.7 and 8, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh). Nothing else in this package spells a code.
const (
	// CodeSocketBind: the unix socket could not be created (a bind error, not an HTTP status).
	CodeSocketBind = doctor.AGT005
	// CodeTCPBind: the TCP port is used by another program (a bind error, not an HTTP status).
	CodeTCPBind = doctor.AGT006
	// CodeInvalidRequest: malformed JSON, unknown field, invalid value, wrong content type, wrong method.
	CodeInvalidRequest = doctor.AGT020
	// CodeNotFound: unknown route or entity.
	CodeNotFound = doctor.AGT021
	// CodeBusy: another operation is already running.
	CodeBusy = doctor.AGT022
	// CodeNotInMode: this action is not allowed in the current mode.
	CodeNotInMode = doctor.AGT023
	// CodeStarting: the agent is starting or stopping (503 with Retry-After: 5).
	CodeStarting = doctor.AGT024
	// CodeTooLarge: the request body is over the limit.
	CodeTooLarge = doctor.AGT025
	// CodeInternal: a bug; details.request_id is always present.
	CodeInternal = doctor.AGT099

	// CodeUnauthenticated: missing or invalid credentials.
	CodeUnauthenticated = doctor.SEC001
	// CodeAdminRequired: the action needs the admin PIN.
	CodeAdminRequired = doctor.SEC002
	// CodeWrongPIN: the admin PIN is wrong.
	CodeWrongPIN = doctor.SEC003
	// CodeRateLimited: too many wrong PIN or login-code attempts.
	CodeRateLimited = doctor.SEC004
	// CodeNoPIN: no admin PIN has been set (or its file cannot be used).
	CodeNoPIN = doctor.SEC005
	// CodeBadHost: the Host header is not the agent's own address (DNS rebinding defence).
	CodeBadHost = doctor.SEC006
	// CodeBadOrigin: the request came from another website.
	CodeBadOrigin = doctor.SEC007
	// CodeBadLoginCode: the login (UI) code is wrong, used or expired.
	CodeBadLoginCode = doctor.SEC008
	// CodeSessionExpired: the session cookie names a session that has expired.
	CodeSessionExpired = doctor.SEC012
	// CodePeerNotOperator: the socket peer is not in localai-operators.
	CodePeerNotOperator = doctor.SEC013

	// CodePortalSessionEnded: a Portal session was used where it is not allowed, or has ended.
	CodePortalSessionEnded = doctor.SVC110

	// CodeRouterDown: the llama router is running but not answering (502).
	CodeRouterDown = doctor.LLM004
	// CodeUpstreamTimeout: an upstream took too long (504).
	CodeUpstreamTimeout = doctor.LLM010
)
