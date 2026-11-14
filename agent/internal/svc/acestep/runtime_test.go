package acestep

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// installRuntime lays out <root>/<version>/{venv/bin/python, BUILD.json} and points current at it.
func installRuntime(t *testing.T, root, version, backend string) {
	t.Helper()
	dir := filepath.Join(root, version)
	if err := os.MkdirAll(filepath.Join(dir, "venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "venv", "bin", "python"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := `{"version":"` + version + `","backend":"` + backend + `","python":"3.12","lock_sha256":"x","built_at":"2026-11-14T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, BuildFile), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(root, "current"))
	if err := os.Symlink(version, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
}

func run(c doctor.Check) []string {
	var out []string
	for _, r := range c.Run(context.Background()) {
		out = append(out, r.Code)
	}
	return out
}

func TestRuntimeAndDoctorChecks(t *testing.T) {
	root := t.TempDir()
	backend := "cuda"
	wanted := true
	driver := "595.45.04"
	checks := DoctorChecks(CheckOptions{
		Wanted:  func() bool { return wanted },
		Runtime: Runtime{Root: root, Backend: func() string { return backend }},
		Driver:  func() string { return driver },
	})
	rt, drv := checks[0], checks[1]

	if got := run(rt); len(got) != 1 || got[0] != doctor.SVC150 {
		t.Fatalf("missing runtime: %v", got)
	}
	installRuntime(t, root, "1.5.0", "cuda")
	if got := run(rt); len(got) != 0 {
		t.Fatalf("installed runtime: %v", got)
	}
	backend = "vulkan"
	if got := run(rt); len(got) != 1 {
		t.Fatalf("runtime for another backend: %v", got)
	}
	wanted = false
	if got := run(rt); len(got) != 0 {
		t.Fatalf("music not wanted: %v", got)
	}
	wanted = true
	if got := run(drv); len(got) != 0 {
		t.Fatalf("new driver: %v", got)
	}
	driver = "535.104.05"
	if got := run(drv); len(got) != 1 || got[0] != doctor.SVC159 {
		t.Fatalf("old driver: %v", got)
	}
	driver = ""
	if got := run(drv); len(got) != 0 {
		t.Fatalf("unknown driver: %v", got)
	}
}
