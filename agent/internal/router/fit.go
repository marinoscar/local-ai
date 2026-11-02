// Package router holds the pure decision logic of the llama-server router (llama-router.md): the
// fit rules and the eviction planner here, with no I/O, so every table test needs no fakes.
package router

import (
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// Constants of llama-router.md section 4.3.1.
const (
	// DefaultMemoryMarginMiB is M when the profile leaves router.memory_margin_mib unset.
	DefaultMemoryMarginMiB = 512
	// RAMReserveMiB is R_res: the RAM the system keeps for itself.
	RAMReserveMiB = 2048
	// RAMReserveUnifiedMiB is R_res on a unified-memory node, where GPU memory is system RAM.
	RAMReserveUnifiedMiB = 4096
)

// Env is the machine and profile side of the fit rules (the symbols of llama-router.md section
// 4.3.1). Build it with NewEnv from a hardware snapshot; tests may fill it directly. All sizes are
// MiB.
type Env struct {
	// Backend is B: cuda, vulkan or cpu.
	Backend string
	// GTotalMiB is G_total: the GPU memory the fit uses (0 on cpu).
	GTotalMiB int64
	// GOthersMiB is G_others: GPU memory held by processes outside localai-llama.
	GOthersMiB int64
	// Unified is true on a unified-memory node (GPU memory is system RAM).
	Unified bool
	// RTotalMiB and RAvailMiB are R_total (MemTotal) and R_avail (MemAvailable).
	RTotalMiB, RAvailMiB int64
	// MarginMiB is M, router.memory_margin_mib.
	MarginMiB int64
	// ModelsMax is N, router.models_max; values below 1 count as 1.
	ModelsMax int
	// WhisperReserveMiB is W_res: the VRAM reserved for a whisper that is about to start on the GPU
	// (0 when whisper is on the CPU or already running; its memory is then in GOthersMiB).
	WhisperReserveMiB int64
}

// NewEnv distils a hardware snapshot and a profile router block into an Env. backend is the node's
// backend (node.yaml llama.backend or the detected one); empty means snap.Backend, and when that is
// empty too, cpu. whisperReserveMiB is W_res, which the caller derives from the companion services.
// A router.memory_margin_mib of 0 (unset) is the default 512.
func NewEnv(snap hardware.Snapshot, backend string, r catalog.Router, whisperReserveMiB int64) Env {
	if backend == "" {
		backend = snap.Backend
	}
	if backend == "" {
		backend = hardware.BackendCPU
	}
	margin := int64(r.MemoryMarginMiB)
	if margin == 0 {
		margin = DefaultMemoryMarginMiB
	}
	return Env{
		Backend:           backend,
		GTotalMiB:         nonNegative(snap.FitMemory(backend)),
		GOthersMiB:        nonNegative(snap.OthersMiB(backend)),
		Unified:           snap.UnifiedMemory(backend),
		RTotalMiB:         nonNegative(snap.RAM.TotalMiB),
		RAvailMiB:         nonNegative(snap.RAM.AvailableMiB),
		MarginMiB:         margin,
		ModelsMax:         r.ModelsMax,
		WhisperReserveMiB: nonNegative(whisperReserveMiB),
	}
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// gpuBackend reports whether B is a GPU backend (cuda or vulkan).
func (e Env) gpuBackend() bool {
	return e.Backend == hardware.BackendCUDA || e.Backend == hardware.BackendVulkan
}

// ramBound reports whether the RAM rules apply: a unified-memory node or the cpu backend.
func (e Env) ramBound() bool { return e.Unified || e.Backend == hardware.BackendCPU }

// RAMReserve is R_res for this node.
func (e Env) RAMReserve() int64 {
	if e.Unified {
		return RAMReserveUnifiedMiB
	}
	return RAMReserveMiB
}

func (e Env) modelsMax() int {
	if e.ModelsMax < 1 {
		return 1
	}
	return e.ModelsMax
}

// Candidate is the model whose fit is asked: its catalog requirements and whether a complete,
// verified copy is on disk (llama-router.md section 4.1 step 2).
type Candidate struct {
	ID           string
	Requirements catalog.Requirements
	Present      bool
}

// StaticFit answers "can this model ever load on this hardware?" (llama-router.md section 4.3.2),
// which drives loadable in the API and decides whether the model gets a preset. The rules run in
// order and the first failure is the reason; G_others is deliberately ignored.
func (e Env) StaticFit(m Candidate) (bool, *Reason) {
	req := m.Requirements
	fail := func(r Reason) (bool, *Reason) { return false, &r }

	// 1. The backend is supported.
	if !contains(req.Backends, e.Backend) {
		return fail(reasonBackend(e.Backend))
	}
	// 2. GPU backends: the weights fit in graphics memory beside the safety margin.
	if e.gpuBackend() && req.MinVRAMMiB > e.GTotalMiB-e.MarginMiB {
		return fail(reasonGraphics(req.MinVRAMMiB, e.GTotalMiB))
	}
	// 3. Unified memory: the system also needs its (larger) RAM reserve.
	// 4. CPU backend: the model lives in RAM beside the reserve.
	if e.ramBound() && req.MinRAMMiB > e.RTotalMiB-e.RAMReserve() {
		return fail(reasonMemoryReserve(req.MinRAMMiB, e.RAMReserve(), e.RTotalMiB))
	}
	// 5. Every backend: the RAM requirement fits in the machine at all.
	if req.MinRAMMiB > e.RTotalMiB {
		return fail(reasonMemory(req.MinRAMMiB, e.RTotalMiB))
	}
	// 6. The files are present and verified.
	if !m.Present {
		return fail(reasonFilesMissing())
	}
	return true, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
