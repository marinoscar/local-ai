package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func asRoot(t *testing.T, uid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
}

func runSetupArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"aiagent", "setup"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func fixtures(t *testing.T, yamlPattern, invPattern string) (nodeYAML, inv string) {
	t.Helper()
	dir := t.TempDir()
	nodeYAML = filepath.Join(dir, "node.yaml")
	body := "node: {id: ai-n4}\nprofile: small-8gb\nllama: {backend: cuda}\n"
	if yamlPattern != "" {
		body += "tailscale: {serve_pattern: " + yamlPattern + "}\n"
	}
	if err := os.WriteFile(nodeYAML, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	inv = filepath.Join(dir, "inventory.local.yaml")
	if invPattern != "" {
		text := "schema_version: 1\nlab: {tailnet: tailtest}\nnodes: [{id: ai-n4, tailnet_ip: \"198.51.100.4\", serve_pattern: " + invPattern + "}]\n"
		if err := os.WriteFile(inv, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestSetupRootOnlyAndUsage(t *testing.T) {
	asRoot(t, 1000)
	if code, _, e := runSetupArgs(t, "summary"); code != 77 || !strings.Contains(e, "root") {
		t.Errorf("non-root: code %d, stderr %q", code, e)
	}
	asRoot(t, 0)
	if code, _, _ := runSetupArgs(t); code != 2 {
		t.Errorf("no step: code %d, want 2", code)
	}
	if code, _, e := runSetupArgs(t, "frobnicate"); code != 2 || !strings.Contains(e, "unknown step") {
		t.Errorf("unknown step: code %d, stderr %q", code, e)
	}
}

func TestSetupServeCheck(t *testing.T) {
	asRoot(t, 0)
	tests := []struct {
		name, yamlP, invP, flag string
		code                    int
		out                     string
	}{
		{"inventory decides", "", "B", "", 0, "B\n"},
		{"flag matches", "", "B", "B", 0, "B\n"},
		{"flag contradicts: LAI-INS-050", "", "B", "A", 1, ""},
		{"node.yaml contradicts: LAI-INS-050", "A", "B", "", 1, ""},
		{"nothing known", "", "", "", 0, "none\n"},
	}
	for _, tc := range tests {
		ny, inv := fixtures(t, tc.yamlP, tc.invP)
		args := []string{"serve-check", "--node-yaml", ny, "--inventory", inv}
		if tc.flag != "" {
			args = append(args, "--serve-pattern", tc.flag)
		}
		code, out, e := runSetupArgs(t, args...)
		if code != tc.code || out != tc.out {
			t.Errorf("%s: code %d out %q, want %d %q (stderr %q)", tc.name, code, out, tc.code, tc.out, e)
		}
		if tc.code == 1 && !strings.Contains(e, "LAI-INS-050") {
			t.Errorf("%s: stderr lacks the code: %q", tc.name, e)
		}
	}
}

func TestSetupSummary(t *testing.T) {
	asRoot(t, 0)
	ny, inv := fixtures(t, "B", "B")
	key := filepath.Join(t.TempDir(), "api-key")
	secret := strings.Repeat("ab12", 16)
	if err := os.WriteFile(key, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, e := runSetupArgs(t, "summary", "--node-yaml", ny, "--inventory", inv, "--api-key-file", key,
		"--machine", "test-box", "--laptop", "--reboot-required", "--user", "someone")
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, e)
	}
	for _, want := range []string{"Local AI node ai-n4 is installed.", "profile small-8gb, backend cuda", "ab12…ab12",
		"test-box.tailtest.ts.net", "198.51.100.4", "Pattern B:", "REBOOT"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Error("the full API key was printed")
	}
	// Without an inventory the same run prints placeholders.
	code, out, _ = runSetupArgs(t, "summary", "--node-yaml", ny, "--inventory", filepath.Join(t.TempDir(), "none"), "--machine", "test-box")
	if code != 0 || !strings.Contains(out, "<TAILNET_IP_AI_N4>") || strings.Contains(out, "198.51.100.4") {
		t.Errorf("placeholders expected, code %d:\n%s", code, out)
	}
	if code, _, e := runSetupArgs(t, "summary", "--node-yaml", filepath.Join(t.TempDir(), "none")); code != 2 || !strings.Contains(e, "no node id") {
		t.Errorf("no node id: code %d stderr %q", code, e)
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey("ab12cd34ef56ab12cd34ef56"); got != "ab12…ef56" {
		t.Errorf("mask = %q", got)
	}
	if got := maskKey(" short "); got != "(not set)" {
		t.Errorf("short = %q", got)
	}
}
