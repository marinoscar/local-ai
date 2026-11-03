package build

import "github.com/marinoscar/local-ai/agent/internal/kit"

// Recipe is what differs between the programs the kit can rebuild offline (rebuild-kit.md section
// 4.3): the llama.cpp recipe is LlamaRecipe; whisper.cpp (SVC-05, KIT-05) declares its own and reuses
// the pieces that take a Recipe: KitSources and ChainWithKit (the offline source), the preflight
// (Manager.PreflightReport), ExtractArchive (every archive extraction), the CUDA redistributable
// wiring and the unprivileged compile. The Manager itself builds llama.cpp: the configure flags, the
// directory layout and the activation are llama-router.md section 4.10, so a recipe is data, not a
// second builder.
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
}

// LlamaRecipe is llama.cpp (llama-router.md section 4.10).
var LlamaRecipe = Recipe{
	Name:     kit.SourceStemLlama,
	CloneURL: DefaultCloneURL,
	Programs: []string{"llama-server", "llama-cli", "llama-bench"},
	Tools:    KitPrerequisites,
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
