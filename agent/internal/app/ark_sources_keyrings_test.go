package app

import "testing"

// The vendor keyrings are embedded in the agent and wired into the apt producer (issue #274); the Ubuntu
// archive keyring is not embedded, it is read from the node.
func TestAptVendorKeyringsAreWired(t *testing.T) {
	sr := newSrcRig(t, "online", true)
	d := sr.start()
	sr.waitStarted()
	apt := d.ark.sources.apt
	if apt.Keyrings == nil {
		t.Fatal("AptClosure.Keyrings is not wired")
	}
	for _, n := range []string{"tailscale-archive-keyring.gpg", "nvidia-cuda-archive-keyring.gpg"} {
		if b, err := apt.Keyrings(n); err != nil || len(b) == 0 {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := apt.Keyrings("ubuntu-archive-keyring.gpg"); err == nil {
		t.Error("the Ubuntu archive keyring must come from the node, not from the agent")
	}
}
