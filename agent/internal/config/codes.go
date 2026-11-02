package config

// Doctor codes this package hands back to its callers (agent.md section 8, doctor-codes.md).
//
// They live in this one file on purpose: scripts/check-doctor-codes.sh forbids a "LAI-..." literal
// in any non-test Go file outside internal/doctor/codes.go once that file exists. AGENT-11 replaces
// these constants with the ones from internal/doctor; nothing else in this package spells a code.
const (
	// CodeNodeYAMLMissing: /etc/localai/node.yaml does not exist.
	CodeNodeYAMLMissing = "LAI-AGT-001"
	// CodeNodeYAMLInvalid: node.yaml cannot be read or has invalid values (a warning for unknown keys).
	CodeNodeYAMLInvalid = "LAI-AGT-002"
	// CodeRestartNeeded: a settings change needs a restart (of the agent, or of llama) to take effect.
	CodeRestartNeeded = "LAI-AGT-016"
	// CodeProfileMissing: node.yaml names a profile the loaded catalog does not have.
	CodeProfileMissing = "LAI-CAT-003"
	// CodeCloudURLNotHTTPS: cloud.url must start with https://.
	CodeCloudURLNotHTTPS = "LAI-CLD-062"
	// CodeSecretPermissions: a secret file had unsafe permissions (they were fixed).
	CodeSecretPermissions = "LAI-SEC-009"
)
