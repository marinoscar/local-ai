package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/setup/binaries"
	"github.com/marinoscar/local-ai/agent/internal/setup/dirs"
)

// asRoot and runSetupArgs are in setup_test.go.

func TestSetupDirsAndBinariesAreRootOnly(t *testing.T) {
	asRoot(t, 1000)
	for _, step := range []string{"dirs", "binaries"} {
		if code, _, e := runSetupArgs(t, step, "--dry-run"); code != 77 || !strings.Contains(e, "root") {
			t.Fatalf("%s: code %d %q", step, code, e)
		}
	}
}

func TestSetupDirsBadArguments(t *testing.T) {
	asRoot(t, 0)
	if code, _, e := runSetupArgs(t, "dirs", "--bogus"); code != 2 || !strings.Contains(e, "LAI-CLI-005") {
		t.Fatalf("code %d %q", code, e)
	}
}

func TestSetupDirsDryRunAndCheck(t *testing.T) {
	asRoot(t, 0)
	old := newDirsSystem
	newDirsSystem = func() dirs.System { return nilSystem{} }
	t.Cleanup(func() { newDirsSystem = old })

	code, out, e := runSetupArgs(t, "dirs", "--dry-run", "--desktop-user", "alice")
	if code != 0 || !strings.Contains(out, "[dry-run] would create folder /etc/localai (root:localai 0750)") || e != "" {
		t.Fatalf("code %d out %q err %q", code, out, e)
	}
	if code, _, _ := runSetupArgs(t, "dirs", "--check", "--desktop-user", "alice"); code != 1 {
		t.Fatalf("--check with pending changes must exit 1, got %d", code)
	}
	code, out, _ = runSetupArgs(t, "dirs", "--dry-run", "--json", "--no-polkit", "--desktop-user", "alice")
	var rep dirs.Report
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || !rep.DryRun || !rep.Pending() {
		t.Fatalf("json: code %d %q", code, out)
	}
}

// nilSystem reports an empty machine: no users, no groups, no files; nothing may be written.
type nilSystem struct{}

func (nilSystem) LookupUser(n string) (int, int, bool, error) {
	if n == "alice" {
		return 1000, 1000, true, nil
	}
	return 0, 0, false, nil
}
func (nilSystem) LookupGroup(string) (int, bool, error)                 { return 0, false, nil }
func (nilSystem) InGroup(string, string) (bool, error)                  { return false, nil }
func (nilSystem) AddGroup(context.Context, string) error                { panic("dry run wrote") }
func (nilSystem) AddUser(context.Context, string, string) error         { panic("dry run wrote") }
func (nilSystem) AddToGroups(context.Context, string, []string) error   { panic("dry run wrote") }
func (nilSystem) Stat(string) (dirs.Info, error)                        { return dirs.Info{}, nil }
func (nilSystem) Mkdir(string, os.FileMode) error                       { panic("dry run wrote") }
func (nilSystem) Chown(string, int, int) error                          { panic("dry run wrote") }
func (nilSystem) Chmod(string, os.FileMode) error                       { panic("dry run wrote") }
func (nilSystem) Names(uid, gid int) string                             { return "x" }
func (nilSystem) ReadFile(string) ([]byte, error)                       { return nil, os.ErrNotExist }
func (nilSystem) WriteFile(string, []byte, os.FileMode, int, int) error { panic("dry run wrote") }
func (nilSystem) Rename(string, string) error                           { panic("dry run wrote") }

type stubInst struct{ calls []string }

func (s *stubInst) Locate(_ context.Context, k, tool string) (*kit.Located, error) {
	s.calls = append(s.calls, "locate "+tool)
	return &kit.Located{Path: k + "/" + tool, Version: "9.9"}, nil
}
func (s *stubInst) Install(_ context.Context, k, tool string) (*kit.ToolResult, error) {
	s.calls = append(s.calls, "install "+tool)
	return &kit.ToolResult{Tool: tool, Version: "9.9"}, nil
}
func (s *stubInst) InstallFile(context.Context, string, string, string, string) (*kit.ToolResult, error) {
	return nil, nil
}

func TestSetupBinariesFromKit(t *testing.T) {
	asRoot(t, 0)
	st := &stubInst{}
	old := newBinariesInstaller
	newBinariesInstaller = func() binaries.Installer { return st }
	t.Cleanup(func() { newBinariesInstaller = old })

	code, out, e := runSetupArgs(t, "binaries", "--kit", "/mnt/localai/kit", "--tools", "pmtiles", "--dry-run")
	if code != 0 || !strings.Contains(out, "[dry-run] would install pmtiles 9.9") || strings.Join(st.calls, ",") != "locate pmtiles" {
		t.Fatalf("code %d out %q err %q calls %v", code, out, e, st.calls)
	}
	st.calls = nil
	code, out, _ = runSetupArgs(t, "binaries", "--kit", "/mnt/localai/kit")
	if code != 0 || strings.Join(st.calls, ",") != "install kiwix-tools,install pmtiles,install piper" || !strings.Contains(out, "installed piper 9.9") {
		t.Fatalf("code %d out %q calls %v", code, out, st.calls)
	}
}

func TestSetupBinariesArgumentChecks(t *testing.T) {
	asRoot(t, 0)
	for _, args := range [][]string{{"binaries"}, {"binaries", "--online", "--kit", "/k"}, {"binaries", "--bogus"}} {
		if code, _, e := runSetupArgs(t, args...); code != 2 || !strings.Contains(e, "exactly one") && !strings.Contains(e, "LAI-CLI-005") {
			t.Errorf("%v: code %d %q", args, code, e)
		}
	}
}
