package config

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package hands back to its callers (agent.md section 8, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh). Nothing else in this package spells a code.
const (
	// CodeNodeYAMLMissing: /etc/localai/node.yaml does not exist.
	CodeNodeYAMLMissing = doctor.AGT001
	// CodeNodeYAMLInvalid: node.yaml cannot be read or has invalid values (a warning for unknown keys).
	CodeNodeYAMLInvalid = doctor.AGT002
	// CodeRestartNeeded: a settings change needs a restart (of the agent, or of llama) to take effect.
	CodeRestartNeeded = doctor.AGT016
	// CodeProfileMissing: node.yaml names a profile the loaded catalog does not have.
	CodeProfileMissing = doctor.CAT003
	// CodeCloudURLNotHTTPS: cloud.url must start with https://.
	CodeCloudURLNotHTTPS = doctor.CLD062
	// CodeSecretPermissions: a secret file had unsafe permissions (they were fixed).
	CodeSecretPermissions = doctor.SEC009
)
