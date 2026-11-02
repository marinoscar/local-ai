package auth

import (
	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Doctor codes this package raises (agent.md section 8.4). The ones that are HTTP answers are the
// api package's; the rest are logged or raised as alerts. Like api/codes.go, this is the one file
// of the package that may spell a code (scripts/check-doctor-codes.sh).
const (
	// CodeTokenCreated: the agent token was missing and a new one was created (warning).
	CodeTokenCreated = "LAI-SEC-011"
	// CodeSecretPermissions: a secret file had unsafe permissions; they were fixed (warning).
	CodeSecretPermissions = config.CodeSecretPermissions

	// CodeRateLimited: too many wrong PIN or login-code attempts; critical at a PIN lockout.
	CodeRateLimited = api.CodeRateLimited
	// CodeWrongPIN: the admin PIN is wrong.
	CodeWrongPIN = api.CodeWrongPIN
)
