package whisper

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// whisperRoot makes a whisper root with the given builds; current points at the build named by
// current ("" for no link).
func whisperRoot(t *testing.T, current string, builds ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, b := range builds {
		dir := filepath.Join(root, "builds", b, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "whisper-server"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if current != "" {
		if err := os.Symlink("builds/"+current, filepath.Join(root, "current")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDoctorCheckRaisesSVC012WhenTheProgramIsNotBuilt(t *testing.T) {
	cfg := func(want string) func() string { return func() string { return want } }
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		enabled func() bool
		backend func() string
		reason  string // "" = no result
	}{
		{"built for the configured backend", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cuda", "1a2b3c4d5-cuda") }, nil, cfg("cuda"), ""},
		{"built, backend not asked", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cpu", "1a2b3c4d5-cpu") }, nil, nil, ""},
		{"built, backend unknown", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cpu", "1a2b3c4d5-cpu") }, nil, cfg(""), ""},
		{"no current link at all", func(t *testing.T) string { return whisperRoot(t, "") }, nil, cfg("cuda"), "missing"},
		{"a dangling link", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cuda") }, nil, nil, "missing"},
		{"current has no server", func(t *testing.T) string {
			root := whisperRoot(t, "1a2b3c4d5-cuda")
			if err := os.MkdirAll(filepath.Join(root, "builds", "1a2b3c4d5-cuda", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}, nil, nil, "missing"},
		{"the server is a directory", func(t *testing.T) string {
			root := whisperRoot(t, "1a2b3c4d5-cuda")
			if err := os.MkdirAll(filepath.Join(root, "builds", "1a2b3c4d5-cuda", "bin", "whisper-server"), 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}, nil, nil, "not_a_file"},
		{"a processor build on a node set to cuda", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cpu", "1a2b3c4d5-cpu") }, nil, cfg("cuda"), "wrong_backend"},
		{"a cuda build on a node set to vulkan", func(t *testing.T) string { return whisperRoot(t, "1a2b3c4d5-cuda", "1a2b3c4d5-cuda") }, nil, cfg("vulkan"), "wrong_backend"},
		{"a move-aside name is not a backend", func(t *testing.T) string {
			return whisperRoot(t, "1a2b3c4d5-cuda.old-20261102T100000Z", "1a2b3c4d5-cuda.old-20261102T100000Z")
		}, nil, cfg("cuda"), ""},
		{"speech switched off reports nothing", func(t *testing.T) string { return whisperRoot(t, "") }, func() bool { return false }, cfg("cuda"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.setup(t)
			chk := DoctorCheck(CheckOptions{Root: root, Enabled: tc.enabled, Backend: tc.backend})
			if chk.ID != CheckBuilt || chk.Area != "SVC" || !chk.Fast {
				t.Fatalf("check = %+v", chk)
			}
			res := chk.Run(context.Background())
			if tc.reason == "" {
				if len(res) != 0 {
					t.Fatalf("results = %+v, want none", res)
				}
				return
			}
			if len(res) != 1 || res[0].Code != doctor.SVC012 || res[0].Details["reason"] != tc.reason {
				t.Fatalf("results = %+v, want SVC-012 %s", res, tc.reason)
			}
			if want := filepath.Join(root, "current", "bin", "whisper-server"); res[0].Details["path"] != want {
				t.Errorf("path = %v, want %s", res[0].Details["path"], want)
			}
			if !doctor.Emittable(res[0].Code) {
				t.Error("the code is not emittable")
			}
		})
	}
}

func TestDoctorCheckNamesTheBackendsOfAWrongBuild(t *testing.T) {
	root := whisperRoot(t, "1a2b3c4d5-cpu", "1a2b3c4d5-cpu")
	res := DoctorCheck(CheckOptions{Root: root, Backend: func() string { return " cuda " }}).Run(context.Background())
	if len(res) != 1 || res[0].Details["have"] != "cpu" || res[0].Details["want"] != "cuda" {
		t.Fatalf("results = %+v", res)
	}
}

func TestBuildBackend(t *testing.T) {
	for in, want := range map[string]string{
		"builds/1a2b3c4d5-cuda":      "cuda",
		"/opt/x/builds/abc-vulkan/":  "vulkan",
		"1a2b3c4d5-cpu":              "cpu",
		"1a2b3c4d5-rocm":             "",
		"nobackend":                  "",
		"1a2b3c4d5-cuda.old-2026-01": "",
	} {
		if got := buildBackend(in); got != want {
			t.Errorf("buildBackend(%q) = %q, want %q", in, got, want)
		}
	}
}
