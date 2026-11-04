package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseHardenArgs(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		bad  string
	}{
		{"none", nil, ""},
		{"all", []string{"--dry-run", "--json", "--hardening", "--no-ssh", "--desktop-user", "alice", "--state-dir", "/x", "--root", "/y"}, ""},
		{"check", []string{"--check", "--json"}, ""},
		{"revert", []string{"--revert", "--dry-run"}, ""},
		{"unknown", []string{"--bogus"}, "unknown argument"},
		{"missing value", []string{"--desktop-user"}, "needs a value"},
		{"conflict", []string{"--check", "--revert"}, "conflict"},
	} {
		_, err := parseHardenArgs(c.args)
		if c.bad == "" && err != nil || c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestSetupDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSetup([]string{"harden", "--bogus"}, &out, &errb); code != 77 && code != 2 {
		t.Errorf("bad flag as non-root = %d", code)
	}
	old := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = old }()
	if code := runSetup([]string{"harden", "--bogus"}, &out, &errb); code != 2 {
		t.Errorf("bad flag = %d", code)
	}
}

// A temp root with no battery is not a laptop: nothing runs, nothing is written, exit 0.
func TestHardenSkipsADesktop(t *testing.T) {
	root := t.TempDir()
	var out, errb bytes.Buffer
	code := runSetupHarden(context.Background(), []string{"--root", root, "--json"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var rep map[string]any
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep["skipped"] == nil {
		t.Errorf("json %q: %v", out.String(), err)
	}
}

func TestHardenCheckWhenNeverApplied(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSetupHarden(context.Background(), []string{"--root", t.TempDir(), "--check"}, &out, &errb); code != 1 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(out.String(), "has not been applied") {
		t.Errorf("out %q", out.String())
	}
}
