package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

func TestParseComputeCaps(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"one GPU", "7.5\n", "75"},
		{"no trailing newline", "7.5", "75"},
		{"two different GPUs", "7.5\n8.9\n", "75;89"},
		{"two identical GPUs", "7.5\n7.5\n", "75"},
		{"sorted whatever the order", "8.9\n7.5\n", "75;89"},
		{"numeric sort, not text sort", "10.0\n7.5\n", "75;100"},
		{"blank lines and spaces", "\n 8.6 \n\n", "86"},
		{"N/A is ignored", "N/A\n7.5\n", "75"},
		{"only N/A", "[N/A]\n", ""},
		{"empty", "", ""},
		{"garbage", "NVIDIA-SMI has failed\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseComputeCaps(tc.in); got != tc.want {
				t.Errorf("ParseComputeCaps(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJobsFormula(t *testing.T) {
	tests := []struct {
		nproc int
		mem   int64
		want  int
	}{
		{8, 10240, 5}, // ai-n3: about 10 GiB free gets -j 5
		{4, 10240, 4}, // processors limit
		{16, 4095, 1}, // under 4 GiB: one job
		{16, 4096, 2},
		{16, 0, 1}, // unreadable memory
		{1, 65536, 1},
		{0, 65536, 1}, // never below one
		{32, 131072, 32},
	}
	for _, tc := range tests {
		if got := Jobs(tc.nproc, tc.mem); got != tc.want {
			t.Errorf("Jobs(%d, %d MiB) = %d, want %d", tc.nproc, tc.mem, got, tc.want)
		}
	}
}

func TestDetectCudaArch(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		res      *execx.Result
		err      error
		noTool   bool
		want     string
		wantCode string
	}{
		{name: "one GPU", out: "7.5\n", want: "75"},
		{name: "two GPUs", out: "7.5\n8.9\n", want: "75;89"},
		{name: "no answer", out: "", wantCode: doctor.HW019},
		{name: "N/A", out: "N/A\n", wantCode: doctor.HW019},
		{name: "nvidia-smi missing", noTool: true, wantCode: doctor.HW019},
		{name: "nvidia-smi fails", res: &execx.Result{ExitCode: 9, Stderr: []byte("NVIDIA-SMI has failed")}, wantCode: doctor.HW019},
		{name: "nvidia-smi cannot run", err: errors.New("exec format error"), wantCode: doctor.HW019},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.nvidiaOut = tc.out
			if tc.noTool {
				h.tools["nvidia-smi"] = false
			}
			if tc.res != nil || tc.err != nil {
				h.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
					if strings.HasSuffix(c.Path, "nvidia-smi") {
						if tc.res != nil {
							return *tc.res, nil, true
						}
						return execx.Result{}, tc.err, true
					}
					return execx.Result{}, nil, false
				})
			}
			m := h.manager()
			got, e := m.DetectCudaArch(context.Background())
			if tc.wantCode != "" {
				if e == nil || e.Code != tc.wantCode {
					t.Fatalf("want code %s, got %v", tc.wantCode, e)
				}
				return
			}
			if e != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, e, tc.want)
			}
			// The query is exactly the one of the spec.
			calls := h.exec.lines()
			if len(calls) != 1 || calls[0] != "nvidia-smi --query-gpu=compute_cap --format=csv,noheader" {
				t.Errorf("calls = %q", calls)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	arch := "86"
	short := strings.ToUpper(testSHA[:7]) // upper case is accepted and lowered
	tests := []struct {
		name     string
		node     config.LlamaBlock
		req      Request
		want     Inputs
		wantCode string
		wantErr  string
	}{
		{
			name: "all from the node (cuda arch from node.yaml, no nvidia-smi)",
			node: config.LlamaBlock{Commit: testSHA, Backend: "cuda", CudaArch: &arch},
			want: Inputs{Commit: testSHA, ShortCommit: testShort9, Backend: "cuda", CudaArch: "86", Jobs: 5, Activate: true, Keep: 2},
		},
		{
			name: "flags beat the node, arch list kept",
			node: config.LlamaBlock{Commit: "1234567", Backend: "vulkan"},
			req:  Request{Commit: testSHA, Backend: "cuda", CudaArch: "75;86", Jobs: 3, NoActivate: true, Keep: 4},
			want: Inputs{Commit: testSHA, ShortCommit: testShort9, Backend: "cuda", CudaArch: "75;86", Jobs: 3, Activate: false, Keep: 4},
		},
		{
			name: "detected arch when neither flag nor node has one",
			node: config.LlamaBlock{Commit: testSHA, Backend: "cuda"},
			want: Inputs{Commit: testSHA, ShortCommit: testShort9, Backend: "cuda", CudaArch: "75", Jobs: 5, Activate: true, Keep: 2},
		},
		{
			name: "short commit, upper case lowered; cpu has no arch",
			node: config.LlamaBlock{Backend: "cpu"},
			req:  Request{Commit: short},
			want: Inputs{Commit: strings.ToLower(short), ShortCommit: strings.ToLower(short), Backend: "cpu", Jobs: 5, Activate: true, Keep: 2},
		},
		{name: "vulkan", node: config.LlamaBlock{Commit: testSHA, Backend: "vulkan"}, want: Inputs{Commit: testSHA, ShortCommit: testShort9, Backend: "vulkan", Jobs: 5, Activate: true, Keep: 2}},
		{name: "no commit", node: config.LlamaBlock{Backend: "cuda"}, wantCode: doctor.LLM018, wantErr: "No llama.cpp commit"},
		{name: "bad commit", req: Request{Commit: "not-hex", Backend: "cpu"}, wantCode: doctor.LLM018, wantErr: "7 to 40"},
		{name: "commit too short", req: Request{Commit: "abc123", Backend: "cpu"}, wantCode: doctor.LLM018, wantErr: "7 to 40"},
		{name: "no backend", node: config.LlamaBlock{Commit: testSHA}, wantCode: doctor.LLM018, wantErr: "No llama.cpp backend"},
		{name: "unknown backend", node: config.LlamaBlock{Commit: testSHA}, req: Request{Backend: "metal"}, wantCode: doctor.LLM018, wantErr: "cuda, vulkan or cpu"},
		{name: "malformed arch", node: config.LlamaBlock{Commit: testSHA}, req: Request{Backend: "cuda", CudaArch: "7.5"}, wantCode: doctor.HW019, wantErr: "75 or 75;86"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			node := tc.node
			h.opts.Node = func() config.LlamaBlock { return node }
			m := h.manager()
			got, err := m.Resolve(context.Background(), tc.req)
			if tc.wantCode != "" {
				e := mustErr(t, err)
				if e.Code != tc.wantCode || !strings.Contains(e.Message, tc.wantErr) {
					t.Fatalf("got %s %q, want %s containing %q", e.Code, e.Message, tc.wantCode, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("inputs\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestResolveJobsFromMemoryAndCPUs(t *testing.T) {
	h := newHarness(t)
	h.opts.NumCPU = func() int { return 2 }
	h.opts.MemAvailableMiB = func() (int64, error) { return 65536, nil }
	in, err := h.manager().Resolve(context.Background(), Request{Backend: "cpu"})
	if err != nil || in.Jobs != 2 {
		t.Fatalf("jobs = %d, %v; want 2", in.Jobs, err)
	}
	h.opts.MemAvailableMiB = func() (int64, error) { return 0, errors.New("no /proc") }
	in, err = h.manager().Resolve(context.Background(), Request{Backend: "cpu"})
	if err != nil || in.Jobs != 1 {
		t.Fatalf("unreadable memory: jobs = %d, %v; want 1", in.Jobs, err)
	}
}

func TestParseMemAvailable(t *testing.T) {
	got, err := parseMemAvailableMiB([]byte("MemTotal:       16384000 kB\nMemFree:         100 kB\nMemAvailable:   10485760 kB\n"))
	if err != nil || got != 10240 {
		t.Fatalf("got %d, %v; want 10240", got, err)
	}
	if _, err := parseMemAvailableMiB([]byte("MemTotal: 1 kB\n")); err == nil {
		t.Error("a meminfo without MemAvailable must be an error")
	}
}

func TestPreflight(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		missing  []string
		wantMiss []string
		wantPkgs []string
	}{
		{name: "cuda all present", backend: "cuda"},
		{name: "cpu all present", backend: "cpu"},
		{name: "cuda without nvcc", backend: "cuda", missing: []string{"nvcc"}, wantMiss: []string{"nvcc"}, wantPkgs: []string{"nvidia-cuda-toolkit"}},
		{name: "vulkan without glslc", backend: "vulkan", missing: []string{"glslc"}, wantMiss: []string{"glslc"}, wantPkgs: []string{"glslc"}},
		{name: "no ninja", backend: "cpu", missing: []string{"ninja"}, wantMiss: []string{"ninja"}, wantPkgs: []string{"ninja-build"}},
		{name: "no compiler names build-essential once", backend: "cpu", missing: []string{"cc", "c++"}, wantMiss: []string{"cc", "c++"}, wantPkgs: []string{"build-essential"}},
		{name: "cuda tool not needed on cpu", backend: "cpu", missing: []string{"nvcc", "glslc"}},
		{name: "several", backend: "cuda", missing: []string{"cmake", "ninja", "nvcc"}, wantMiss: []string{"cmake", "ninja", "nvcc"}, wantPkgs: []string{"cmake", "ninja-build", "nvidia-cuda-toolkit"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			for _, n := range tc.missing {
				h.tools[n] = false
			}
			tools, e := h.manager().Preflight(tc.backend)
			if tc.wantMiss == nil {
				if e != nil {
					t.Fatal(e)
				}
				if tools.CMake != "/usr/bin/cmake" || tools.Ninja == "" || tools.CC == "" {
					t.Errorf("tools not resolved: %+v", tools)
				}
				return
			}
			if e == nil || e.Code != doctor.LLM018 {
				t.Fatalf("want LAI-LLM-018, got %v", e)
			}
			if !reflect.DeepEqual(e.Details["missing"], tc.wantMiss) || !reflect.DeepEqual(e.Details["packages"], tc.wantPkgs) {
				t.Errorf("details = %v", e.Details)
			}
			if !strings.Contains(e.Message, "sudo apt install "+strings.Join(tc.wantPkgs, " ")) {
				t.Errorf("message does not name the package: %q", e.Message)
			}
		})
	}
}

func TestBuildIDOf(t *testing.T) {
	if got := BuildIDOf(testSHA, "cuda"); got != testID {
		t.Errorf("BuildIDOf = %q", got)
	}
	if got := BuildIDOf("abc1234", "cpu"); got != "abc1234-cpu" {
		t.Errorf("short sha: %q", got)
	}
}

// A toolkit installed by NVIDIA's own installer lives in /usr/local/cuda, which is not on the fixed PATH:
// the build finds it there and tells cmake where its root is.
func TestPreflightFindsTheToolkitOfNvidiasInstaller(t *testing.T) {
	h := newHarness(t)
	h.tools["nvcc"] = false
	nvcc := filepath.Join(h.root, "cuda", "bin", "nvcc")
	h.write(nvcc, "#!/bin/sh\n")
	if err := os.Chmod(nvcc, 0o755); err != nil {
		t.Fatal(err)
	}
	h.opts.LocalNVCC = nvcc
	tools, e := h.manager().Preflight("cuda")
	if e != nil {
		t.Fatal(e)
	}
	if tools.NVCC != nvcc {
		t.Fatalf("NVCC = %q, want %q", tools.NVCC, nvcc)
	}
	plan := Plan{SourceDir: "/s", BuildDir: "/b"}.WithLocalCuda(nvcc)
	joined := strings.Join(plan.ConfigureArgs, " ")
	for _, want := range []string{"-DCUDAToolkit_ROOT=" + filepath.Join(h.root, "cuda"), "-DCMAKE_CUDA_COMPILER=" + nvcc} {
		if !strings.Contains(joined, want) {
			t.Errorf("configure args lack %s: %s", want, joined)
		}
	}
}
