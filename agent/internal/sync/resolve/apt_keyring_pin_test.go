package resolve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit/aptkeys"
)

// A keyring that fails its pinned fingerprint is refused with LAI-SYN-060 whatever sync.apt_unverified_repos
// says: it must never fall back to "take the repository unverified" (issue #274).
func TestAptPinnedKeyringMismatchIsFatalEvenWithAllow(t *testing.T) {
	for name, allow := range map[string]func() bool{"allow": yes, "refuse": no} {
		t.Run(name, func(t *testing.T) {
			r := newUnvRig(t, allow, true, unvTailscale, unvNvidia)
			r.a.Keyrings = func(n string) ([]byte, error) {
				if n == ubuntuKeyring {
					return []byte("ubuntu-keyring"), nil
				}
				return nil, fmt.Errorf("%w: %s has AAAA, the pin is BBBB", aptkeys.ErrMismatch, n)
			}
			_, err := r.a.Resolve(context.Background(), unvItem(unvRepos()...))
			if CodeOf(err) != doctor.SYN060 || !strings.Contains(err.Error(), "does not match the fingerprint pinned") {
				t.Fatalf("err = %v (code %q), want LAI-SYN-060 about the pin", err, CodeOf(err))
			}
			if n := len(r.run.Calls()); n != 0 {
				t.Errorf("%d commands ran before the refusal", n)
			}
		})
	}
}

// With the real embedded keyrings wired in, the extra repositories are verified (nothing unverified).
func TestAptEmbeddedVendorKeyringsVerifyExtraRepos(t *testing.T) {
	r := newUnvRig(t, no, true)
	r.a.Keyrings = func(n string) ([]byte, error) {
		if n == ubuntuKeyring {
			return []byte("ubuntu-keyring"), nil
		}
		return aptkeys.Get(n)
	}
	got, err := r.a.unverifiedRepos(&aptSource{Extra: unvRepos()}, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("unverified = %v, err = %v", got, err)
	}
}
