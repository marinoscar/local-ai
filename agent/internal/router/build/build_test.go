package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

var update = flag.Bool("update", false, "rewrite the golden files of testdata/golden")

// golden compares got with testdata/golden/<name>; -update rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (run with -update to accept):\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// cmakeCalls renders the cmake commands of a build as golden text: the timeout and the argv, with the
// temporary root and the commit replaced so that the file is stable.
func (h *harness) cmakeCalls() string {
	var b strings.Builder
	for _, c := range h.exec.all() {
		if filepath.Base(c.Path) != "cmake" {
			continue
		}
		fmt.Fprintf(&b, "[timeout %s] cmake %s\n", c.Timeout, strings.Join(c.Args, " "))
	}
	s := strings.ReplaceAll(b.String(), h.root, "<ROOT>")
	return strings.ReplaceAll(s, testSHA, "<SHA>")
}

func TestGoldenArgvPerBackend(t *testing.T) {
	for _, backend := range []string{"cuda", "vulkan", "cpu"} {
		t.Run(backend, func(t *testing.T) {
			h := newHarness(t, withBackend(backend))
			if backend == "vulkan" {
				h.devices = "Available devices:\n  Vulkan0: NVIDIA GeForce RTX 2070 (8192 MiB, 7000 MiB free)\n"
			}
			h.req.NoActivate = true
			if _, err := h.build(); err != nil {
				t.Fatal(err)
			}
			golden(t, "argv-"+backend+".txt", h.cmakeCalls())
		})
	}
}

func TestBuildUsesNinjaAndTheDocumentedTimeouts(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	var configure, build *execx.Cmd
	for _, c := range h.exec.all() {
		c := c
		if filepath.Base(c.Path) == "cmake" && c.Args[0] == "-S" {
			configure = &c
		}
		if filepath.Base(c.Path) == "cmake" && c.Args[0] == "--build" {
			build = &c
		}
	}
	if configure == nil || build == nil {
		t.Fatal("cmake was not run twice")
	}
	if strings.Join(configure.Args[4:6], " ") != "-G Ninja" {
		t.Errorf("configure does not use -G Ninja: %v", configure.Args)
	}
	if build.Timeout != 3*time.Hour {
		t.Errorf("build timeout = %s, want 3h", build.Timeout)
	}
	if configure.Timeout != ConfigureTimeout {
		t.Errorf("configure timeout = %s", configure.Timeout)
	}
	if got := strings.Join(build.Env, " "); got != "PATH="+execx.FixedPath+" LC_ALL=C" {
		t.Errorf("the build environment is %q", got)
	}
	if !filepath.IsAbs(configure.Path) || filepath.Base(configure.Path) != "cmake" {
		t.Errorf("cmake is not run by absolute path: %s", configure.Path)
	}
}

func TestMultiGPUArchIsOneArgument(t *testing.T) {
	h := newHarness(t)
	h.nvidiaOut = "7.5\n8.9\n"
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.exec.all() {
		if filepath.Base(c.Path) == "cmake" && c.Args[0] == "-S" {
			for _, a := range c.Args {
				if a == "-DCMAKE_CUDA_ARCHITECTURES=75;89" {
					return
				}
			}
			t.Fatalf("no -DCMAKE_CUDA_ARCHITECTURES=75;89 argument in %v", c.Args)
		}
	}
	t.Fatal("cmake was not configured")
}

func TestBuildWithoutActivationWritesTheBuild(t *testing.T) {
	h := newHarness(t)
	h.req.NoActivate = true
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != testID || res.Switched || res.PreviousBuild != "" {
		t.Fatalf("result = %+v", res)
	}
	final := filepath.Join(h.root, "builds", testID)
	if res.Path != final || res.LogPath != filepath.Join(final, "build.log") {
		t.Errorf("paths %q %q", res.Path, res.LogPath)
	}
	for _, n := range binaries {
		fi, err := os.Stat(filepath.Join(final, "bin", n))
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v mode %v", n, err, fi)
		}
	}
	for _, gone := range []string{"builds/" + testID + ".tmp", "builds/" + testID + ".tmp.failed", "current.tmp"} {
		if h.exists(gone) {
			t.Errorf("%s should not exist", gone)
		}
	}
	if h.link() != "builds/"+oldID {
		t.Errorf("current moved to %q although activation was off", h.link())
	}
	if h.agent.restarts != 0 || h.exec.ran("systemctl") {
		t.Error("llama was restarted although activation was off")
	}

	// BUILD.json is the one of the spec, and it says the truth about the files.
	var info BuildInfo
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(final, "BUILD.json"))), &info); err != nil {
		t.Fatal(err)
	}
	if info.BuildID != testID || info.Commit != testSHA || info.Backend != "cuda" || info.CudaArch != "75" {
		t.Errorf("BUILD.json identity: %+v", info)
	}
	if info.Compiler != "GNU 15.2.0" || info.NVCC != "12.4.131" {
		t.Errorf("compiler %q nvcc %q", info.Compiler, info.NVCC)
	}
	if info.BuiltAt != testNow.Format(time.RFC3339) || info.BuiltBy != "aiagent 0.1.0" {
		t.Errorf("built_at %q built_by %q", info.BuiltAt, info.BuiltBy)
	}
	if len(info.CMakeFlags) < 8 || info.CMakeFlags[0] != "-DCMAKE_BUILD_TYPE=Release" || info.CMakeFlags[len(info.CMakeFlags)-1] != "-DCMAKE_CUDA_ARCHITECTURES=75" {
		t.Errorf("cmake_flags: %v", info.CMakeFlags)
	}
	if info.Smoke.Version != "version: 6000 (f3586e6)" || len(info.Smoke.Devices) != 1 || !strings.HasPrefix(info.Smoke.Devices[0], "CUDA0: NVIDIA GeForce RTX 2070") {
		t.Errorf("smoke: %+v", info.Smoke)
	}
	for _, n := range binaries {
		raw, _ := os.ReadFile(filepath.Join(final, "bin", n))
		sum := sha256.Sum256(raw)
		if info.SHA256[n] != hex.EncodeToString(sum[:]) {
			t.Errorf("sha256 of %s is wrong", n)
		}
	}
	if res.BuildJSON.BuildID != testID {
		t.Errorf("result build_json: %+v", res.BuildJSON)
	}
	logText := readFile(t, res.LogPath)
	if !strings.Contains(logText, "$ /usr/bin/cmake -S ") || !strings.Contains(logText, "--version") {
		t.Errorf("build.log misses the commands:\n%s", logText)
	}
	// The source tree stays for the next rebuild.
	if !h.exists("src/" + testSHA + "/CMakeLists.txt") {
		t.Error("the source tree was removed")
	}
}

func TestResultJSONShape(t *testing.T) {
	h := newHarness(t)
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range m {
		keys[k] = true
	}
	want := map[string]bool{"build_id": true, "switched": true, "previous_build": true, "build_json": true}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want exactly %v", keys, want)
	}
	for k := range want {
		if !keys[k] {
			t.Errorf("missing key %s", k)
		}
	}
	var bj map[string]json.RawMessage
	_ = json.Unmarshal(m["build_json"], &bj)
	for _, k := range []string{"build_id", "commit", "backend", "cuda_arch", "cmake_flags", "compiler", "nvcc", "built_at", "built_by", "sha256", "smoke"} {
		if _, ok := bj[k]; !ok {
			t.Errorf("build_json misses %s", k)
		}
	}
}

func TestCpuBuildJSONHasNoCudaFields(t *testing.T) {
	h := newHarness(t, withBackend("cpu"))
	h.req.NoActivate = true
	h.devices = ""
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != "f3586e66b-cpu" || res.BuildJSON.CudaArch != "" || res.BuildJSON.NVCC != "" {
		t.Errorf("%+v", res.BuildJSON)
	}
	if res.BuildJSON.Smoke.Devices == nil {
		t.Error("smoke.devices must be an empty list, not null")
	}
	if h.exec.ran("nvidia-smi") || h.exec.ran("nvcc") {
		t.Error("a cpu build asked the GPU tools")
	}
}

func TestSmokeVersionFormats(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantOK  bool
	}{
		{"classic format", "version: 6000 (f3586e6)\nbuilt with cc\n", true},
		{"commit format", "version: 0.8.0-dev (build 12023, commit f3586e66b)\n", true},
		{"upper case", "VERSION: 6000 (F3586E6)\n", true},
		{"other commit", "version: 6000 (deadbee)\n", false},
		{"no commit at all", "version: 6000\n", false},
		{"too short a prefix", "version: 6000 (f3586e)\n", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.version = tc.version
			h.req.NoActivate = true
			_, err := h.build()
			if tc.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := mustErr(t, err)
			if e.Code != doctor.LLM019 || e.Stage != StageSmoke || e.Details["cause"] != "smoke_version" {
				t.Fatalf("got %+v", e)
			}
			failed := filepath.Join(h.root, "builds", testID+".tmp.failed")
			if _, err := os.Stat(filepath.Join(failed, "build.log")); err != nil {
				t.Errorf("the failed build is not kept: %v", err)
			}
			if h.exists("builds/" + testID) {
				t.Error("a build that failed its smoke test was put in place")
			}
			if h.link() != "builds/"+oldID {
				t.Errorf("current moved to %q", h.link())
			}
		})
	}
}

func TestSmokeDevices(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		devices  string
		wantFail bool
	}{
		{"cuda with CUDA0", "cuda", "ggml_cuda_init: found 1 CUDA devices:\n  Device 0: NVIDIA GeForce RTX 2070, compute capability 7.5\nAvailable devices:\n  CUDA0: NVIDIA GeForce RTX 2070 (8192 MiB, 7000 MiB free)\n", false},
		{"cuda without a GPU", "cuda", "Available devices:\n", true},
		{"cuda with only CUDA1", "cuda", "Available devices:\n  CUDA1: NVIDIA GeForce RTX 2070 (8192 MiB)\n", true},
		{"vulkan real device", "vulkan", "Available devices:\n  Vulkan0: AMD Radeon 890M (65536 MiB, 60000 MiB free)\n", false},
		{"vulkan llvmpipe only", "vulkan", "Available devices:\n  Vulkan0: llvmpipe (LLVM 15.0.7, 256 bits) (0 MiB, 0 MiB free)\n", true},
		{"vulkan llvmpipe and a GPU", "vulkan", "Available devices:\n  Vulkan0: llvmpipe (LLVM 15.0.7)\n  Vulkan1: AMD Radeon 890M (65536 MiB)\n", false},
		{"vulkan nothing", "vulkan", "Available devices:\n", true},
		{"cpu needs no device", "cpu", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withBackend(tc.backend))
			h.devices = tc.devices
			h.req.NoActivate = true
			_, err := h.build()
			if !tc.wantFail {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := mustErr(t, err)
			if e.Code != doctor.LLM019 || e.Details["cause"] != "smoke_devices" {
				t.Fatalf("got %+v", e)
			}
			if !h.exists("builds/" + testShort9 + "-" + tc.backend + ".tmp.failed") {
				t.Error("the failed build is not kept")
			}
		})
	}
}

func TestSmokeCommandFailures(t *testing.T) {
	t.Run("version exits non-zero", func(t *testing.T) {
		h := newHarness(t)
		h.versionRes = &execx.Result{ExitCode: 127, Stderr: []byte("error while loading shared libraries: libcuda.so.1")}
		h.req.NoActivate = true
		_, err := h.build()
		e := mustErr(t, err)
		if e.Code != doctor.LLM019 || e.Details["cause"] != "smoke_exit" || e.Details["exit_code"] != 127 {
			t.Fatalf("%+v", e)
		}
		if !strings.Contains(readFile(t, filepath.Join(h.root, "builds", testID+".tmp.failed", "build.log")), "libcuda.so.1") {
			t.Error("the smoke output is not in build.log")
		}
	})
	t.Run("list-devices cannot run", func(t *testing.T) {
		h := newHarness(t)
		h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
			if len(c.Args) > 0 && c.Args[0] == "--list-devices" {
				return execx.Result{}, execx.ErrTimeout, true
			}
			return execx.Result{}, nil, false
		})
		h.req.NoActivate = true
		_, err := h.build()
		if e := mustErr(t, err); e.Code != doctor.LLM019 || e.Details["cause"] != "smoke_exec" {
			t.Fatalf("%+v", e)
		}
	})
}

func TestNotRelocatableBuildIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		elf    []byte
		wantOK bool
	}{
		{"static program", elfFixture(false, "", ""), true},
		{"dynamic without runpath", elfFixture(true, "", ""), true},
		{"origin runpath", elfFixture(true, "$ORIGIN:/usr/lib/x86_64-linux-gnu", ""), true},
		{"runpath into /home", elfFixture(true, "/home/someone/llama.cpp/build-cuda/bin", ""), false},
		{"rpath into /home", elfFixture(true, "", "/home/someone/lib"), false},
		{"not an ELF", []byte("#!/bin/sh\necho hi\n"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.elf = tc.elf
			h.req.NoActivate = true
			_, err := h.build()
			if tc.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := mustErr(t, err)
			if e.Code != doctor.LLM019 || e.Stage != StageRunpath {
				t.Fatalf("got %+v", e)
			}
			if h.exec.ran("llama-server") {
				t.Error("the smoke test ran on a program that failed the relocatable check")
			}
			if !h.exists("builds/" + testID + ".tmp.failed") {
				t.Error("the failed build is not kept")
			}
		})
	}
}

func TestRunpathProblems(t *testing.T) {
	root := "/opt/localai/llama.cpp/builds"
	tests := []struct {
		name string
		elf  []byte
		want []string
	}{
		{"static", elfFixture(false, "", ""), nil},
		{"no runpath", elfFixture(true, "", ""), nil},
		{"system directories and origin", elfFixture(true, "$ORIGIN:/usr/lib:/opt/cuda/lib64", ""), nil},
		{"home", elfFixture(true, "/home/someone/llama.cpp/build-cuda/bin", ""), []string{"/home/someone/llama.cpp/build-cuda/bin"}},
		{"home exactly", elfFixture(true, "/home", ""), []string{"/home"}},
		{"home-like name is fine", elfFixture(true, "/homework/lib", ""), nil},
		{"one bad among good", elfFixture(true, "/usr/lib:/home/x/lib:$ORIGIN", ""), []string{"/home/x/lib"}},
		{"both tags", elfFixture(true, "/home/a", "/home/b"), []string{"/home/a", "/home/b"}},
		{"builds tmp tree", elfFixture(true, root+"/f3586e66b-cuda.tmp/build/bin", ""), []string{root + "/f3586e66b-cuda.tmp/build/bin"}},
		{"builds tmp failed", elfFixture(true, root+"/x-cuda.tmp.failed/bin", ""), []string{root + "/x-cuda.tmp.failed/bin"}},
		{"final builds dir is fine", elfFixture(true, root+"/f3586e66b-cuda/bin", ""), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "llama-server")
			if err := os.WriteFile(p, tc.elf, 0o755); err != nil {
				t.Fatal(err)
			}
			got, err := RunpathProblems(OSFS{}, p, root)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	if _, err := RunpathProblems(OSFS{}, filepath.Join(t.TempDir(), "missing"), root); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestBuildFailures(t *testing.T) {
	bigLog := func() string {
		var b strings.Builder
		for i := 1; i <= 500; i++ {
			fmt.Fprintf(&b, "[%d/500] Building CXX object %d\n", i, i)
		}
		return b.String()
	}
	tests := []struct {
		name      string
		setup     func(h *harness)
		wantCause string
		wantStage Stage
		check     func(t *testing.T, e *Error)
	}{
		{
			name: "configure fails", wantCause: "exit", wantStage: StageConfigure,
			setup: func(h *harness) { h.configure = exitWith(1, "CMake Error: No CMAKE_CUDA_COMPILER could be found.\n") },
			check: func(t *testing.T, e *Error) {
				if got := e.Details["hint_packages"]; fmt.Sprint(got) != "[nvidia-cuda-toolkit]" {
					t.Errorf("hint_packages = %v", got)
				}
				if !strings.Contains(e.Message, "sudo apt install nvidia-cuda-toolkit") {
					t.Errorf("the message does not name the package: %q", e.Message)
				}
			},
		},
		{
			name: "compile error keeps the last 200 lines", wantCause: "exit", wantStage: StageBuild,
			setup: func(h *harness) {
				h.buildRes = execx.Result{ExitCode: 1, Stdout: []byte(bigLog())}
			},
			check: func(t *testing.T, e *Error) {
				tail, _ := e.Details["log_tail"].(string)
				lines := strings.Split(tail, "\n")
				if len(lines) != 200 {
					t.Fatalf("log_tail has %d lines, want 200", len(lines))
				}
				if !strings.Contains(lines[len(lines)-1], "[exit status 1]") || !strings.Contains(tail, "[500/500]") || strings.Contains(tail, "[200/500]") {
					t.Errorf("wrong window of the log:\n%s", tail)
				}
			},
		},
		{
			name: "disk full during the build", wantCause: "exit", wantStage: StageBuild,
			setup: func(h *harness) {
				h.buildRes = execx.Result{ExitCode: 1, Stderr: []byte("ninja: error: write(...): No space left on device\n")}
			},
			check: func(t *testing.T, e *Error) {
				if !strings.Contains(e.Details["log_tail"].(string), "No space left on device") {
					t.Error("the disk-full message is not in the log tail")
				}
			},
		},
		{
			name: "the build process is killed", wantCause: "exit", wantStage: StageBuild,
			setup: func(h *harness) { h.buildRes = execx.Result{ExitCode: -1} },
			check: func(t *testing.T, e *Error) {
				if !strings.Contains(e.Message, "stopped before it finished") {
					t.Errorf("message %q", e.Message)
				}
			},
		},
		{
			name: "the build times out", wantCause: "timeout", wantStage: StageBuild,
			setup: func(h *harness) { h.buildErr = fmt.Errorf("%w after 3h0m0s: cmake", execx.ErrTimeout) },
			check: func(t *testing.T, e *Error) {
				if !errors.Is(e, execx.ErrTimeout) || !strings.Contains(e.Message, "3h0m0s") {
					t.Errorf("%v", e)
				}
			},
		},
		{
			name: "cmake cannot be started", wantCause: "exec", wantStage: StageBuild,
			setup: func(h *harness) { h.buildErr = errors.New("fork/exec: text file busy") },
		},
		{
			name: "a program is not produced", wantCause: "missing_binary", wantStage: StageInstall,
			setup: func(h *harness) { h.skipBinaries = true },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			_, err := h.build()
			e := mustErr(t, err)
			if e.Code != doctor.LLM018 || e.Stage != tc.wantStage || e.Details["cause"] != tc.wantCause {
				t.Fatalf("got code %s stage %s cause %v", e.Code, e.Stage, e.Details["cause"])
			}
			if tc.check != nil {
				tc.check(t, e)
			}
			failed := filepath.Join(h.root, "builds", testID+".tmp.failed")
			if e.Details["log_path"] != filepath.Join(failed, "build.log") {
				t.Errorf("log_path = %v", e.Details["log_path"])
			}
			if h.exists("builds/"+testID+".tmp") || h.exists("builds/"+testID+".tmp.failed/build") {
				t.Error("the build tree or the .tmp directory was left behind")
			}
			if h.link() != "builds/"+oldID || h.exists("builds/"+testID) {
				t.Errorf("a failed build changed what is installed (current = %q)", h.link())
			}
			if h.agent.restarts != 0 || h.exec.ran("systemctl") {
				t.Error("llama was restarted after a failed build")
			}
			if h.exec.ran("llama-server") {
				t.Error("a build that did not finish was smoke tested")
			}
		})
	}
}

func TestMissingToolsStopBeforeAnything(t *testing.T) {
	h := newHarness(t)
	h.tools["nvcc"] = false
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Stage != StagePreflight || e.Details["cause"] != "missing_tools" {
		t.Fatalf("%+v", e)
	}
	if h.exec.ran("cmake") || h.exists("builds/"+testID+".tmp") || h.exists("builds/"+testID+".tmp.failed") {
		t.Error("a missing prerequisite must stop before any build directory is made")
	}
}

func TestNotEnoughDiskStopsBeforeTheBuild(t *testing.T) {
	h := newHarness(t)
	h.opts.FreeBytes = func(string) (uint64, error) { return 1 << 30, nil }
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "disk_space" || e.Details["free_bytes"] != uint64(1<<30) {
		t.Fatalf("%+v", e)
	}
	if h.exec.ran("cmake") {
		t.Error("cmake ran with too little disk")
	}

	// A filesystem that cannot be read does not block a build.
	h = newHarness(t)
	h.opts.FreeBytes = func(string) (uint64, error) { return 0, errors.New("statfs: boom") }
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatalf("unreadable free space must not block: %v", err)
	}
}

func TestCancelledBuildLeavesNothingAndKeepsCurrent(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "cmake" && c.Args[0] == "--build" {
			cancel()
			return execx.Result{}, ctx.Err(), true
		}
		return execx.Result{}, nil, false
	})
	_, err := h.manager().Build(ctx, h.req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Errorf("a cancelled build is not a doctor error: %v", e)
	}
	for _, p := range []string{"builds/" + testID + ".tmp", "builds/" + testID + ".tmp.failed", "builds/" + testID} {
		if h.exists(p) {
			t.Errorf("%s was left behind", p)
		}
	}
	if h.link() != "builds/"+oldID || h.agent.restarts != 0 {
		t.Error("a cancelled build must leave current alone")
	}
}

func TestSecondBuildWhileOneRunsIsRefused(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(h.root, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := lockFile(filepath.Join(h.root, ".build.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, berr := h.build()
	e := mustErr(t, berr)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "locked" {
		t.Fatalf("%+v", e)
	}
	release()
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatalf("the lock must be released: %v", err)
	}
}

func TestRebuildingTheActiveBuildWithoutActivationIsRefused(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.makeBuild(testID, testNow.Add(-time.Hour))
	if err := os.Symlink("builds/"+testID, filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	h.req.NoActivate = true
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "active_build" {
		t.Fatalf("%+v", e)
	}
	if h.exec.ran("cmake") {
		t.Error("cmake ran")
	}
}

func TestRebuildingTheActiveBuildMovesTheOldOneAside(t *testing.T) {
	h := newHarness(t, withoutPrevious())
	h.makeBuild(testID, testNow.Add(-time.Hour))
	if err := os.Symlink("builds/"+testID, filepath.Join(h.root, "current")); err != nil {
		t.Fatal(err)
	}
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	oldName := testID + ".old-20261102T100000Z"
	if !h.exists("builds/" + oldName + "/BUILD.json") {
		t.Fatalf("the old build was not moved to %s", oldName)
	}
	if res.PreviousBuild != oldName || !res.Switched || h.link() != "builds/"+testID {
		t.Errorf("result %+v link %q", res, h.link())
	}
	// A failed check goes back to the moved-aside copy, not to the new build of the same name.
	h2 := newHarness(t, withoutPrevious())
	h2.makeBuild(testID, testNow.Add(-time.Hour))
	if err := os.Symlink("builds/"+testID, filepath.Join(h2.root, "current")); err != nil {
		t.Fatal(err)
	}
	h2.probe.completeErr = errors.New("boom")
	if _, err := h2.build(); err == nil {
		t.Fatal("want a failed check")
	}
	if h2.link() != "builds/"+oldName {
		t.Errorf("current = %q, want the moved-aside build", h2.link())
	}
}

func TestProgressIsReported(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var stages []Stage
	h.req.Progress = func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, p.Stage)
	}
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	want := []Stage{StageSource, StageConfigure, StageBuild, StageInstall, StageSmoke, StageSwitch, StageCheck, StageRetention}
	if fmt.Sprint(stages) != fmt.Sprint(want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
}

func TestInterruptedBuildLeftoverIsReplaced(t *testing.T) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, "builds", testID+".tmp", "stale", "file"), "x")
	h.req.NoActivate = true
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if h.exists("builds/" + testID + "/stale") {
		t.Error("the leftover of an interrupted build ended up in the new build")
	}
}
