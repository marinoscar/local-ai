package app

import "testing"

// A zero Env (what `aiagent serve` passes) must get a working program lookup: buildDrives calls
// env.Lookup("blkid") and used to panic on a real node because the default was documented but never set.
func TestEnvDefaultsSetLookup(t *testing.T) {
	var e Env
	e.defaults()
	if e.Lookup == nil {
		t.Fatal("Env.Lookup is nil after defaults()")
	}
	if _, err := e.Lookup("sh"); err != nil {
		t.Fatalf("Lookup(sh) = %v", err)
	}
}
