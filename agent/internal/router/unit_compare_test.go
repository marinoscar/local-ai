package router

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestRestartRequired(t *testing.T) {
	base := mustRender(t, UnitInputs{Router: routerBlock(1, 512)})
	rep := func(old, new string) string { return strings.Replace(base, old, new, 1) }
	cases := []struct {
		name    string
		old     string
		new     string
		restart bool
		changed []string
	}{
		{"identical", base, base, false, nil},
		{"a comment changed", base, rep("# Hardening that does not interfere", "# Hardening which does not interfere"), false, nil},
		{"the header changed", base, strings.Replace(base, "# Spec:", "# See:", 1), false, nil},
		{"blank lines added", base, strings.ReplaceAll(base, "\n[", "\n\n\n["), false, nil},
		{"the description changed", base, rep("Description=Local AI llama.cpp router", "Description=Other"), false, nil},
		{"the dependencies changed", base, rep("After=network.target local-fs.target", "After=network.target"), false, nil},
		{"the install target changed", base, rep("WantedBy=multi-user.target", "WantedBy=default.target"), false, nil},
		{"start limits changed", base, rep("StartLimitBurst=5", "StartLimitBurst=9"), false, nil},
		{"models-max changed", base, rep("--models-max 1", "--models-max 2"), true, []string{"ExecStart"}},
		{"the margin flag went away", base, rep("  --models-memory-margin 512 \\\n", ""), true, []string{"ExecStart"}},
		{"the port changed", base, rep("--port 8080", "--port 8081"), true, []string{"ExecStart"}},
		{"the cache moved", base, rep("LLAMA_CACHE=/var/lib/localai/llama-cache", "LLAMA_CACHE=/tmp/x"), true, []string{"Environment"}},
		{"an environment line was added", base, rep("ExecStart=", "Environment=GGML_X=1\nExecStart="), true, []string{"Environment"}},
		{"the restart policy changed", base, rep("Restart=on-failure", "Restart=always"), true, []string{"Restart"}},
		{"a hardening option was dropped", base, rep("LockPersonality=yes\n", ""), true, []string{"LockPersonality"}},
		{"a directive was added", base, rep("RestartSec=5\n", "RestartSec=5\nNice=5\n"), true, []string{"Nice"}},
		{"the restart delay changed", base, rep("RestartSec=5", "RestartSec=7"), true, []string{"RestartSec"}},
		{"the user changed", base, rep("User=localai", "User=root"), true, []string{"User"}},
		{"a unit that did not exist needs no restart", "", base, false, nil},
		{"only white space before", "  \n\n", base, false, nil},
	}
	for _, c := range cases {
		if got := RestartRequired([]byte(c.old), []byte(c.new)); got != c.restart {
			t.Errorf("%s: RestartRequired = %v, want %v", c.name, got, c.restart)
		}
		if c.old == "" || strings.TrimSpace(c.old) == "" {
			continue
		}
		if got := ChangedSettings([]byte(c.old), []byte(c.new)); !reflect.DeepEqual(got, c.changed) && !(len(got) == 0 && len(c.changed) == 0) {
			t.Errorf("%s: ChangedSettings = %v, want %v", c.name, got, c.changed)
		}
	}
}

func TestRestartRequiredIgnoresRewrapping(t *testing.T) {
	a := "[Service]\nExecStart=/bin/x \\\n  --a 1 \\\n  --b 2\nRestart=on-failure\n"
	b := "[Service]\nExecStart=/bin/x \\\n  # a comment inside a continuation is dropped by systemd\n  --a 1 \\\n    --b 2\n\n# note\nRestart=on-failure\n"
	if RestartRequired([]byte(a), []byte(b)) {
		t.Fatal("a re-wrapped ExecStart is the same command line")
	}
	c := "[Service]\nExecStart=/bin/x --a 1 --b 2\nRestart=on-failure\n"
	if RestartRequired([]byte(a), []byte(c)) {
		t.Fatal("a joined ExecStart is the same command line")
	}
	d := "[Service]\nExecStart=/bin/x --a 1 --b 3\nRestart=on-failure\n"
	if !RestartRequired([]byte(a), []byte(d)) {
		t.Fatal("another argument is another command line")
	}
}

func TestRestartRequiredRepeatedDirectivesCompareInOrder(t *testing.T) {
	a := "[Service]\nEnvironment=A=1\nEnvironment=B=2\n"
	b := "[Service]\nEnvironment=B=2\nEnvironment=A=1\n"
	if !RestartRequired([]byte(a), []byte(b)) {
		t.Fatal("a later Environment= wins, so the order matters")
	}
	if RestartRequired([]byte(a), []byte(a)) {
		t.Fatal("same")
	}
}

func TestRestartRequiredOnlyLooksAtService(t *testing.T) {
	a := "[Unit]\nDescription=a\n[Service]\nExecStart=/bin/x\n[Install]\nWantedBy=a.target\n"
	b := "[Unit]\nDescription=b\n[Service]\nExecStart=/bin/x\n[Install]\nWantedBy=b.target\n"
	if RestartRequired([]byte(a), []byte(b)) {
		t.Fatal("[Unit] and [Install] changes need no restart")
	}
	// A directive of the same name in another section does not count as [Service].
	c := "[Unit]\nExecStart=/bin/other\n[Service]\nExecStart=/bin/x\n"
	d := "[Unit]\nExecStart=/bin/another\n[Service]\nExecStart=/bin/x\n"
	if RestartRequired([]byte(c), []byte(d)) {
		t.Fatal("only [Service]")
	}
}

// TestRestartRequiredProperties over rendered units: reflexive, symmetric, and true exactly when
// the rendered argument lists or the port differ.
func TestRestartRequiredProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	sm := singleN3()
	gen := func() (UnitInputs, string) {
		var in UnitInputs
		if rng.Intn(3) == 0 {
			in = singleIn(sm)
		} else {
			in = routerIn(1+rng.Intn(3), []int{0, 512, 2048}[rng.Intn(3)])
		}
		in.Port = []int{0, 8080, 18080}[rng.Intn(3)]
		return in, mustRender(t, in)
	}
	for i := 0; i < 400; i++ {
		_, a := gen()
		_, b := gen()
		if RestartRequired([]byte(a), []byte(a)) {
			t.Fatal("not reflexive")
		}
		ab, ba := RestartRequired([]byte(a), []byte(b)), RestartRequired([]byte(b), []byte(a))
		if ab != ba {
			t.Fatal("not symmetric")
		}
		if (a != b) != ab {
			t.Fatalf("rendered units differ=%v but RestartRequired=%v\n%s\n---\n%s", a != b, ab, a, b)
		}
	}
}
