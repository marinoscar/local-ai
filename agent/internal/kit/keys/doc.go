// Package keys holds the Local AI release signing public key, minisign.pub (public key only, never the secret
// key; docs/architecture/release-signing.md).
//
// minisign.pub is the single committed copy. The release workflow verifies SHA256SUMS.minisig against it and
// stamps it into install-node.sh (scripts/release/stamp-installer.sh); the agent embeds it for self-update and
// `aictl verify-release` (docs/specs/agent.md section 4.10, docs/specs/rebuild-kit.md section 3.4). The file is
// created by the owner (release-signing.md section 3); until then the format test in this package skips.
package keys
