package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

func TestParseCompiler(t *testing.T) {
	tests := []struct{ in, want string }{
		{"cc (Debian 15.2.0-1) 15.2.0\nCopyright (C) 2025", "GNU 15.2.0"},
		{"gcc (Ubuntu 13.2.0-23ubuntu4) 13.2.0\n", "GNU 13.2.0"},
		{"c++ (GCC) 14.1.1 20240522\n", "GNU 14.1.1"},
		{"Ubuntu clang version 18.1.3 (1ubuntu1)\nTarget: x86_64\n", "Clang 18.1.3"},
		{"clang version 17.0.6\n", "Clang 17.0.6"},
		{"some unknown compiler 1.0\n", "some unknown compiler 1.0"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := ParseCompiler(tc.in); got != tc.want {
			t.Errorf("ParseCompiler(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseNVCC(t *testing.T) {
	tests := []struct{ in, want string }{
		{"nvcc: NVIDIA (R) Cuda compiler driver\nCuda compilation tools, release 12.4, V12.4.131\n", "12.4.131"},
		{"Cuda compilation tools, release 11.8\n", "11.8"},
		{"command not found", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := ParseNVCC(tc.in); got != tc.want {
			t.Errorf("ParseNVCC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionsAreInformationOnly(t *testing.T) {
	// A compiler that will not print its version does not fail a good build.
	h := newHarness(t)
	h.tools["cc"] = true
	h.req.NoActivate = true
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if (filepath.Base(c.Path) == "cc" || filepath.Base(c.Path) == "nvcc") && len(c.Args) == 1 && c.Args[0] == "--version" {
			return execx.Result{ExitCode: 1}, nil, true
		}
		return execx.Result{}, nil, false
	})
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildJSON.Compiler != "unknown" || res.BuildJSON.NVCC != "unknown" {
		t.Errorf("compiler %q nvcc %q", res.BuildJSON.Compiler, res.BuildJSON.NVCC)
	}
}

func TestDefaultsOfNew(t *testing.T) {
	m := New(Options{Root: "/opt/localai/llama.cpp/"})
	if m.root != DefaultRoot || m.opt.BuiltBy == "" || m.opt.Exec == nil || m.opt.FS == nil || m.opt.Lookup == nil ||
		m.opt.Clock == nil || m.opt.Sleep == nil || m.opt.FreeBytes == nil || m.opt.MemAvailableMiB == nil ||
		m.opt.NumCPU == nil || len(m.opt.Sources) != 1 || m.log == nil {
		t.Fatalf("defaults missing: %+v", m.opt)
	}
	if m.opt.Timing.Build != 3*time.Hour || m.opt.Timing.HealthWait != time.Minute || m.opt.Timing.CheckWait != 3*time.Minute {
		t.Errorf("timing = %+v", m.opt.Timing)
	}
	if New(Options{}).root != DefaultRoot {
		t.Error("the default root is /opt/localai/llama.cpp")
	}
	if n := m.opt.NumCPU(); n != runtime.NumCPU() {
		t.Errorf("NumCPU = %d", n)
	}
}

func TestRealSystemReaders(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	free, err := osFreeBytes(t.TempDir())
	if err != nil || free == 0 {
		t.Errorf("osFreeBytes = %d, %v", free, err)
	}
	if _, err := osFreeBytes(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing path must be an error")
	}
	mem, err := readMemAvailableMiB()
	if err != nil || mem <= 0 {
		t.Errorf("readMemAvailableMiB = %d, %v", mem, err)
	}
}

func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	sleepCtx(ctx, time.Hour)
	if time.Since(start) > time.Minute {
		t.Error("a cancelled sleep must return at once")
	}
	sleepCtx(context.Background(), time.Millisecond)
}

func TestErrorText(t *testing.T) {
	cause := errors.New("disk on fire")
	e := newErr(CodeBuildFailed, StageBuild, "Building llama.cpp failed.", cause, "k", 1, "odd")
	if e.Error() != "LAI-LLM-018: Building llama.cpp failed.: disk on fire" || !errors.Is(e, cause) {
		t.Errorf("%q", e.Error())
	}
	if e.Details["k"] != 1 || e.Details["stage"] != "build" || len(e.Details) != 2 {
		t.Errorf("details %v", e.Details)
	}
	if (&Error{Code: "X", Message: "m"}).Error() != "X: m" {
		t.Error("no cause")
	}
}

func TestCodesAreTheDoctorTables(t *testing.T) {
	for _, c := range []string{CodeBuildFailed, CodeBuildUnusable, CodeNoCudaArch, CodeNoSource, CodeBusy} {
		if _, ok := doctor.Lookup(c); !ok {
			t.Errorf("%s is not in the doctor table", c)
		}
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log")
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("a fairly long line of build output that is repeated many times to pass the window\n")
	}
	b.WriteString("last\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailLines(OSFS{}, p, 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 || lines[2] != "last" || !strings.HasPrefix(lines[0], "a fairly long line") {
		t.Errorf("tail = %q", got)
	}
	if tailLines(OSFS{}, filepath.Join(dir, "absent"), 3) != "" {
		t.Error("a missing log has an empty tail")
	}
	small := filepath.Join(dir, "small")
	_ = os.WriteFile(small, []byte("one\ntwo\n"), 0o644)
	if got := tailLines(OSFS{}, small, 200); got != "one\ntwo" {
		t.Errorf("small log: %q", got)
	}
}

func TestHintPackages(t *testing.T) {
	tests := []struct{ log, want string }{
		{"CMake Error at CMakeLists.txt: No CMAKE_CUDA_COMPILER could be found.", "nvidia-cuda-toolkit"},
		{"fatal error: vulkan/vulkan.h: No such file or directory", "libvulkan-dev"},
		{"Could NOT find Vulkan (missing: Vulkan_LIBRARY)", "libvulkan-dev"},
		{"fatal error: spirv/unified1/spirv.hpp: No such file or directory", "spirv-headers"},
		{"CMake Error: CMake was unable to find a build program corresponding to \"Ninja\". CMAKE_MAKE_PROGRAM is not set.", "ninja-build"},
		{"No CMAKE_CXX_COMPILER could be found.", "build-essential"},
		{"error: something else entirely", ""},
	}
	for _, tc := range tests {
		got := strings.Join(hintPackages(tc.log), ",")
		if got != tc.want {
			t.Errorf("hintPackages(%q) = %q, want %q", tc.log, got, tc.want)
		}
	}
}
