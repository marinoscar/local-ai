package docsrender

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlaceholderRegex(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"nothing here", nil},
		{"<TAILNET_IP_AI_N1> and <TAILNET_IP_AI_N1>", []string{"TAILNET_IP_AI_N1"}},
		{"<B> <A_1>", []string{"A_1"}},           // one letter is too short: needs 2..64
		{"<lowercase> <Mixed_Case> <1ABC>", nil}, // only UPPER_SNAKE starting with a letter
		{"`<DESKTOP_USER>` in code", []string{"DESKTOP_USER"}},
		{"<AB>", []string{"AB"}},
		{"<" + strings.Repeat("A", 64) + ">", []string{strings.Repeat("A", 64)}},
		{"<" + strings.Repeat("A", 65) + ">", nil},
		{"<NODE ID> <NODE-ID>", nil},
		{"a<TAILNET>b<TAILNET>c <VPS_PUBLIC_IP>", []string{"TAILNET", "VPS_PUBLIC_IP"}},
	}
	for _, c := range cases {
		if got := FindPlaceholders(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FindPlaceholders(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestProtectAndRestore(t *testing.T) {
	values := map[string]string{"USER_X": "a<b>&*c", "EMPTY": ""}
	src, subs := protect("hi <USER_X>, <UNKNOWN_ONE>, <EMPTY>, <USER_X>", values)
	if strings.Contains(src, "<") {
		t.Fatalf("a placeholder survived protect: %q", src)
	}
	if got, want := subs.plain(src), "hi a<b>&*c, <UNKNOWN_ONE>, <EMPTY>, a<b>&*c"; got != want {
		t.Errorf("plain = %q, want %q", got, want)
	}
	// In HTML the value and an unknown placeholder are both escaped, never interpreted.
	if got, want := subs.html(src), "hi a&lt;b&gt;&amp;*c, &lt;UNKNOWN_ONE&gt;, &lt;EMPTY&gt;, a&lt;b&gt;&amp;*c"; got != want {
		t.Errorf("html = %q, want %q", got, want)
	}
	// A sentinel with no entry restores to nothing instead of panicking.
	if got := subs.plain("x" + sentOpen + "99" + sentClose); got != "x" {
		t.Errorf("stray sentinel = %q", got)
	}
}

func TestBuiltinSecretList(t *testing.T) {
	secret := []string{"TAILNET", "TAILNET_IP_AI_N1", "TAILNET_IP_VPS", "TAILNET_IP", "ROUTE53_ZONE_ID", "VPS_PUBLIC_IP",
		"VPS_HOSTNAME", "LAN_IP_AI_N4", "ADMIN_EMAIL", "OWNER_EMAIL"}
	for _, n := range secret {
		if !builtinSecret(n) {
			t.Errorf("%s must be secret", n)
		}
	}
	for _, n := range []string{"NODE_ID", "DESKTOP_USER", "EMERGENCY_NUMBER", "BRAINS", "CATALOG_VERSION", "RENDER_DATE", "IP", "LLAMA_PORT"} {
		if builtinSecret(n) {
			t.Errorf("%s must not be secret", n)
		}
	}
}
