package update

import (
	"log/slog"

	"github.com/marinoscar/local-ai/agent/internal/kit/keys"
)

// EmbeddedKeys returns the trusted release public keys: every key file committed under
// internal/kit/keys/ (minisign.pub, plus minisign*.pub while a key rotation is under way, release-signing.md
// section 5). The list is empty until the owner commits the production key; Verify then fails with ErrNoKeys,
// so an agent without a key refuses every update (LAI-AGT-010) instead of trusting anything.
//
// Only public keys live in the repository. A key file that does not parse is skipped and logged: it must
// never make the agent trust less strictly, and TestCommittedKeyParses in the keys package catches it in CI.
func EmbeddedKeys() []PublicKey {
	var out []PublicKey
	for _, f := range keys.Files() {
		k, err := ParsePublicKey(f.Data)
		if err != nil {
			slog.Warn("an embedded release key file is not a valid minisign public key and is ignored", "file", f.Name)
			continue
		}
		out = append(out, k)
	}
	return out
}
