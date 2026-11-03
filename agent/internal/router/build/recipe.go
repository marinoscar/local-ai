package build

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Recipe is what differs between the programs the kit can rebuild offline (rebuild-kit.md section
// 4.3): the llama.cpp recipe is LlamaRecipe and the whisper.cpp one (SVC-05) is WhisperRecipe. Both
// run on the same Manager: the offline source (KitSources and ChainWithKit), the preflight
// (Manager.PreflightReport), ExtractArchive (every archive extraction), the CUDA redistributable
// wiring, the unprivileged compile, the install, the atomic switch with its rollback and the
// retention are shared; a recipe says which flags and targets to build, which programs to install,
// how to test them, which service to restart and which codes to report under. A recipe is data plus
// its smoke test, not a second builder.
type Recipe struct {
	// Name is the kit name of the source: the stem of kit/src/<Name>-<commit>.tar.gz and of
	// kit/git/<Name>.git ("llama.cpp", "whisper.cpp").
	Name string
	// CloneURL is the upstream repository of the online source.
	CloneURL string
	// Programs are the executables a build installs, in the order of BUILD.json's sha256.
	Programs []string
	// Tools lists the programs a build for the backend needs before configure, with the apt package
	// that provides each; the preflight resolves them and prints the install command for the missing
	// ones.
	Tools func(backend string) []Tool

	// Root is the directory of the program's builds (<root>/builds, <root>/current); it is the
	// default of Options.Root.
	Root string
	// Service is the short name of the service that runs the program ("llama", "whisper"): the
	// agent route is /services/<Service>/restart and node.yaml's block of the same name holds the
	// commit. Unit is its systemd unit, restarted with systemctl when the agent is down.
	Service, Unit string
	// Flags are the -D flags of the configure call for the inputs, in order; Targets the cmake
	// targets built.
	Flags   func(in Inputs) []string
	Targets []string
	// ScratchSource says the source tree is extracted into the scratch directory as
	// <scratch>/<Name>-<commit>, replaced on every build and removed afterwards (whisper.cpp); the
	// default is <root>/src/<commit>, reused when it is complete and kept by the retention (llama.cpp).
	ScratchSource bool
	// KitPreflight runs the offline preflight (Manager.PreflightReport) on every build, not only
	// with FromKit or DryRun.
	KitPreflight bool
	// AllowAuto accepts the backend "auto", which is also what an absent backend means: the llama
	// backend of the node (Defaults.LlamaBackend).
	AllowAuto bool
	// BuildFailedCode and UnusableCode are the doctor codes of a failed build and of a build that
	// does not work (llama: LAI-LLM-018 and LAI-LLM-019; whisper: LAI-KIT-020 and LAI-KIT-021).
	BuildFailedCode, UnusableCode string
	// AuditAction is the audit line of a switch attempt.
	AuditAction string
	// Keep is the number of builds kept besides current when Request.Keep is 0 (0: DefaultKeep).
	Keep int

	// smoke is the recipe's smoke test of the installed programs (nil: llama's).
	smoke func(r *run, ctx context.Context) (Smoke, *Error)
}

// LlamaRecipe is llama.cpp (llama-router.md section 4.10).
var LlamaRecipe = Recipe{
	Name:            kit.SourceStemLlama,
	CloneURL:        DefaultCloneURL,
	Programs:        []string{"llama-server", "llama-cli", "llama-bench"},
	Tools:           KitPrerequisites,
	Root:            DefaultRoot,
	Service:         "llama",
	Unit:            services.UnitLlama,
	Flags:           llamaFlags,
	Targets:         []string{"llama-server", "llama-cli", "llama-bench"},
	BuildFailedCode: CodeBuildFailed,
	UnusableCode:    CodeBuildUnusable,
	AuditAction:     audit.ActionLlamaBuildSwitch,
}

// defaultKeep is the number of builds kept besides current when the request does not say.
func (m *Manager) defaultKeep() int {
	if k := m.opt.Recipe.Keep; k > 0 {
		return k
	}
	return DefaultKeep
}

// llamaFlags are the configure flags of llama.cpp (llama-router.md section 4.10.2).
func llamaFlags(in Inputs) []string {
	flags := []string{
		"-DCMAKE_BUILD_TYPE=Release",
		"-DBUILD_SHARED_LIBS=OFF",
		"-DLLAMA_CURL=OFF",
		"-DLLAMA_BUILD_TESTS=OFF",
		"-DLLAMA_BUILD_EXAMPLES=OFF",
		"-DGGML_NATIVE=ON",
		// A tree extracted from a tarball or `git archive` has no .git, so llama.cpp would report
		// "commit unknown" and the smoke test (llama-server --version must name the commit) would
		// refuse every build. The commit is given to cmake instead.
		"-DLLAMA_BUILD_COMMIT=" + in.Commit,
	}
	return append(flags, ggmlBackendFlags(in)...)
}

// ggmlBackendFlags are the flags the ggml backends of llama.cpp and whisper.cpp share.
func ggmlBackendFlags(in Inputs) []string {
	switch in.Backend {
	case BackendCUDA:
		return []string{"-DGGML_CUDA=ON", "-DCMAKE_CUDA_ARCHITECTURES=" + in.CudaArch}
	case BackendVulkan:
		return []string{"-DGGML_VULKAN=ON"}
	}
	return nil
}

// KitPrerequisites is the tool list of the offline preflight (rebuild-kit.md section 4.3.1): the C
// and C++ compilers, cmake, ninja and pkg-config everywhere; nvcc for cuda; glslc, the Vulkan
// headers and vulkaninfo for vulkan. The names are the programs a Debian system has after
// installing the packages of the kit's debs closure (rebuild-kit.md section 4.1.4); git is not here
// because only the git-based sources need it and they say so themselves (MissingToolError).
func KitPrerequisites(backend string) []Tool {
	tools := []Tool{
		{"gcc", "build-essential"},
		{"g++", "build-essential"},
		{"cmake", "cmake"},
		{"ninja", "ninja-build"},
		{"pkg-config", "pkg-config"},
	}
	switch backend {
	case BackendCUDA:
		tools = append(tools, Tool{"nvcc", "nvidia-cuda-toolkit"})
	case BackendVulkan:
		tools = append(tools, Tool{"glslc", "glslc"}, Tool{"vulkaninfo", "vulkan-tools"})
	}
	return tools
}
