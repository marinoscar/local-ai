package modes

import (
	"strings"
	"testing"
)

func TestDaemonDownText(t *testing.T) {
	yes := []string{
		"failed to connect to local tailscaled; it doesn't appear to be running (sudo systemctl start tailscaled ?)",
		"dial unix /var/run/tailscale/tailscaled.sock: connect: no such file or directory",
		"dial unix /var/run/tailscale/tailscaled.sock: connect: connection refused",
	}
	for _, s := range yes {
		if !daemonDownText([]byte(s)) {
			t.Errorf("%q should say the daemon is down", s)
		}
	}
	for _, s := range []string{"", "Logged out.", "unexpected flag"} {
		if daemonDownText([]byte(s)) {
			t.Errorf("%q should not say the daemon is down", s)
		}
	}
}

func TestShellLineAndArgv(t *testing.T) {
	tt := target{8080}
	cases := []struct {
		got  []string
		want string
	}{
		{applyArgs(PatternA, tt), "serve --bg --tcp=18080 tcp://127.0.0.1:8080"},
		{applyArgs(PatternB, tt), "serve --bg --https=443 http://127.0.0.1:8080"},
		{removeArgs(PatternA), "serve --tcp=18080 off"},
		{removeArgs(PatternB), "serve --https=443 off"},
		{funnelArgs(443), "funnel --https=443 off"},
		{funnelArgs(18080), "funnel --tcp=18080 off"},
		{applyArgs(PatternB, target{9090}), "serve --bg --https=443 http://127.0.0.1:9090"},
	}
	for _, c := range cases {
		if got := strings.Join(c.got, " "); got != c.want {
			t.Errorf("argv %q, want %q", got, c.want)
		}
		for _, a := range c.got {
			if a == "reset" {
				t.Errorf("argv %q contains reset", c.got)
			}
		}
	}
	if got := shellLine(removeArgs(PatternA)); got != "sudo tailscale serve --tcp=18080 off" {
		t.Errorf("shellLine = %q", got)
	}
}
