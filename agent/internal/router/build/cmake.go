package build

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Tool is a program a build needs and the apt package that provides it.
type Tool struct {
	// Name is the program, looked up in the fixed PATH.
	Name string
	// Package is the apt package named in the error when it is missing.
	Package string
}

// Prerequisites are the programs a build of this backend needs before configure (llama-router.md
// section 4.10.2): cmake, ninja and a C and C++ compiler everywhere, plus nvcc for cuda and glslc for
// vulkan. KIT-04's preflight starts from this list. (git is not here: only some sources need it, and
// they say so themselves; the Vulkan headers are checked by cmake.)
func Prerequisites(backend string) []Tool {
	tools := []Tool{
		{"cmake", "cmake"},
		{"ninja", "ninja-build"},
		{"cc", "build-essential"},
		{"c++", "build-essential"},
	}
	switch backend {
	case BackendCUDA:
		tools = append(tools, Tool{"nvcc", "nvidia-cuda-toolkit"})
	case BackendVulkan:
		tools = append(tools, Tool{"glslc", "glslc"})
	}
	return tools
}

// Tools are the absolute paths of the programs a build runs, resolved once at the start from the
// fixed PATH (llama-router.md section 4.10, implementation note 2).
type Tools struct {
	CMake, Ninja, CC, CXX, NVCC, GLSLC string
}

// Preflight resolves the prerequisites of a backend. A missing program is LAI-LLM-018 naming the apt
// package to install (details.missing and details.packages; the message carries the command).
func (m *Manager) Preflight(backend string) (Tools, *Error) {
	var t Tools
	var missing, packages []string
	seen := map[string]bool{}
	for _, tool := range Prerequisites(backend) {
		path, err := m.opt.Lookup(tool.Name)
		if err != nil {
			missing = append(missing, tool.Name)
			if !seen[tool.Package] {
				seen[tool.Package] = true
				packages = append(packages, tool.Package)
			}
			continue
		}
		switch tool.Name {
		case "cmake":
			t.CMake = path
		case "ninja":
			t.Ninja = path
		case "cc":
			t.CC = path
		case "c++":
			t.CXX = path
		case "nvcc":
			t.NVCC = path
		case "glslc":
			t.GLSLC = path
		}
	}
	if len(missing) > 0 {
		return Tools{}, newErr(CodeBuildFailed, StagePreflight,
			"Building llama.cpp needs "+strings.Join(missing, ", ")+". Install it with: sudo apt install "+strings.Join(packages, " "),
			nil, "cause", "missing_tools", "missing", missing, "packages", packages, "backend", backend)
	}
	return t, nil
}

// Plan is everything a build does that can be computed without running anything: the directories and
// the argv of the two cmake calls. It is a pure function of the inputs, so tests pin it as golden
// files.
type Plan struct {
	// Commit is the full sha.
	Commit string
	// BuildID is "<9-char sha>-<backend>".
	BuildID string
	Backend string
	// CudaArch is "" except for cuda.
	CudaArch string
	Jobs     int
	// SourceDir is src/<commit>; OutDir is builds/<id>.tmp (O); BuildDir is O/build (B); FinalDir
	// is builds/<id>; FailedDir is builds/<id>.tmp.failed.
	SourceDir, OutDir, BuildDir, FinalDir, FailedDir string
	// CMakeFlags are the -D flags of the configure call, in order (BUILD.json's cmake_flags).
	CMakeFlags []string
	// ConfigureArgs and BuildArgs are the argv after the cmake binary.
	ConfigureArgs, BuildArgs []string
}

// BuildIDOf is the build id of a full or short sha and a backend: the first 9 characters of the sha,
// a dash and the backend ("f3586e66b-cuda").
func BuildIDOf(sha, backend string) string {
	return sha[:min(9, len(sha))] + "-" + backend
}

// NewPlan computes the plan of a build of llama.cpp sha (a full sha) under root.
func NewPlan(root string, in Inputs, sha string) Plan {
	return newPlan(LlamaRecipe, root, DefaultScratchDir, in, sha)
}

// newPlan computes the plan of a build of rec's program at sha under root. The source tree is
// <root>/src/<sha>, or <scratch>/<name>-<sha> for a recipe with ScratchSource.
func newPlan(rec Recipe, root, scratch string, in Inputs, sha string) Plan {
	id := BuildIDOf(sha, in.Backend)
	builds := filepath.Join(root, buildsDir)
	p := Plan{
		Commit: sha, BuildID: id, Backend: in.Backend, CudaArch: in.CudaArch, Jobs: in.Jobs,
		SourceDir: sourcePath(rec, root, scratch, sha),
		OutDir:    filepath.Join(builds, id+suffixTmp),
		FinalDir:  filepath.Join(builds, id),
		FailedDir: filepath.Join(builds, id+suffixFailed),
	}
	p.BuildDir = filepath.Join(p.OutDir, "build")

	in.Commit = sha // the flags see the full sha, whatever the caller was given
	p.CMakeFlags = rec.Flags(in)
	p.ConfigureArgs = append([]string{"-S", p.SourceDir, "-B", p.BuildDir, "-G", "Ninja"}, p.CMakeFlags...)
	p.BuildArgs = append(append([]string{"--build", p.BuildDir, "--config", "Release", "--target"}, rec.Targets...),
		"-j", strconv.Itoa(in.Jobs))
	return p
}

// sourcePath is where the source tree of sha lives for rec.
func sourcePath(rec Recipe, root, scratch, sha string) string {
	if rec.ScratchSource {
		return filepath.Join(scratch, rec.Name+"-"+sha)
	}
	return filepath.Join(root, srcDir, sha)
}

// hint maps a line of a failed build's log to the apt package that fixes it.
type hint struct {
	re  *regexp.Regexp
	pkg string
}

var hints = []hint{
	{regexp.MustCompile(`(?i)No CMAKE_CUDA_COMPILER|nvcc: (command )?not found|Could not find nvcc|CUDA Toolkit not found`), "nvidia-cuda-toolkit"},
	{regexp.MustCompile(`(?i)Could NOT find Vulkan|vulkan/vulkan\.h: No such file`), "libvulkan-dev"},
	{regexp.MustCompile(`(?i)glslc.*(not found|NOTFOUND)|Could NOT find.*glslc|Vulkan_GLSLC_EXECUTABLE`), "glslc"},
	{regexp.MustCompile(`(?i)spirv[-/_a-z]*\.hpp: No such file|spirv-headers`), "spirv-headers"},
	{regexp.MustCompile(`(?i)CMAKE_MAKE_PROGRAM is not set|Ninja.*not found|ninja: (command )?not found`), "ninja-build"},
	{regexp.MustCompile(`(?i)No CMAKE_CXX_COMPILER|No CMAKE_C_COMPILER|c\+\+: (command )?not found`), "build-essential"},
}

// hintPackages returns the apt packages a failed build's log points at, in table order.
func hintPackages(log string) []string {
	var out []string
	for _, h := range hints {
		if h.re.MatchString(log) {
			out = append(out, h.pkg)
		}
	}
	return out
}
