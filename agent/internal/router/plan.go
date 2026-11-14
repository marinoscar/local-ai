package router

import (
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// Loaded is a model the router has loaded (status loaded or loading), as the planner sees it.
type Loaded struct {
	ID string
	// Engine is the catalog engine that holds the model; "" means llama (the router). The count rule
	// counts the models of the candidate's engine only; the graphics and RAM rules count every engine
	// (one GPU budget across engines, E11).
	Engine       string
	Requirements catalog.Requirements
	// LastUsed is state.models.last_used; the zero time (never used) is the oldest.
	LastUsed time.Time
	// EffectiveMiB is the device total of the router's memory.effective for this model, when the
	// router reports it. nil, or a value that is not positive, means "not reported".
	EffectiveMiB *int64
}

// UsedVRAM is used_v(k): the router's reported effective device memory, else need_v(k).
func (l Loaded) UsedVRAM() int64 {
	if l.EffectiveMiB != nil && *l.EffectiveMiB > 0 {
		return *l.EffectiveMiB
	}
	return l.Requirements.MinVRAMMiB
}

// Shortfall says which dynamic-fit predicates fail (llama-router.md section 4.3.3).
type Shortfall struct {
	// Count: |K| + 1 > N.
	Count bool
	// GPU: the graphics-memory sum exceeds G_total (GPU backends only).
	GPU bool
	// RAM: the RAM sum exceeds what is available (unified and cpu only).
	RAM bool
}

// Any reports whether the model does not fit.
func (s Shortfall) Any() bool { return s.Count || s.GPU || s.RAM }

// DynamicFit answers "can m load now?" when keep stays loaded and evicted is unloaded first
// (llama-router.md section 4.3.3). evicted matters only to the RAM rule, which credits the RAM of
// the models taken out.
//
//	count: |keep| + 1 <= N
//	GPU:   need_v(m) + sum(used_v(keep)) + G_others + W_res + M <= G_total          (cuda, vulkan)
//	RAM:   need_r(m) + sum(need_r(keep)) <= R_avail + sum(need_r(evicted)) - R_res  (unified, cpu)
//
// A unified-memory node is a vulkan node, so it must satisfy both the GPU and the RAM rule.
func (e Env) DynamicFit(m Candidate, keep, evicted []Loaded) Shortfall {
	var s Shortfall
	s.Count = sameEngine(m.Engine, keep)+1 > e.engineMax(m.Engine)
	if e.gpuBackend() {
		sum := m.Requirements.MinVRAMMiB + e.GOthersMiB + e.WhisperReserveMiB + e.MarginMiB
		for _, k := range keep {
			sum += k.UsedVRAM()
		}
		s.GPU = sum > e.GTotalMiB
	}
	if e.ramBound() {
		need := m.Requirements.MinRAMMiB
		for _, k := range keep {
			need += k.Requirements.MinRAMMiB
		}
		room := nonNegative(e.RAvailMiB) - e.RAMReserve()
		for _, x := range evicted {
			room += x.Requirements.MinRAMMiB
		}
		s.RAM = need > room
	}
	return s
}

// PlanError is the planner's refusal: LAI-LLM-011, there is no room without unloading others and
// the caller did not allow it. Use errors.As to read the Reason.
type PlanError struct{ Reason Reason }

func (e *PlanError) Error() string { return string(e.Reason.Code) + ": " + e.Reason.Message }

// Plan is the eviction planner (llama-router.md section 4.3.3). It starts with K = loaded and, while
// the dynamic fit of m fails, removes from K the least recently used model (ties: larger used_v
// first, then ID, so the result is deterministic). It returns the models to unload in the order to
// unload them; the slice is empty, never nil, when nothing has to go.
//
//   - K non-empty, the fit fails and unloadOthers is false: *PlanError with LAI-LLM-011.
//   - K empty and the fit still fails (other programs hold the memory): the load goes ahead and
//     warn says why, LAI-HW-005 when graphics memory is short, LAI-HW-006 when only RAM is.
//   - Otherwise warn is nil.
//
// Plan applies nothing, and the caller is expected to have passed StaticFit. An entry in loaded for
// m itself is ignored (the orchestrator answers "already loaded" before planning); a duplicate ID
// counts once.
func (e Env) Plan(m Candidate, loaded []Loaded, unloadOthers bool) (evict []string, warn *Reason, err error) {
	order := evictionOrder(m.ID, loaded)
	evict = []string{}
	for i := 0; ; i++ {
		keep, gone := order[i:], order[:i]
		short := e.DynamicFit(m, keep, gone)
		if !short.Any() {
			return evict, nil, nil
		}
		if len(keep) == 0 {
			w := reasonRAMLow()
			if short.GPU {
				w = reasonOthersVRAM()
			}
			return evict, &w, nil
		}
		if !unloadOthers {
			return nil, nil, &PlanError{Reason: reasonNoRoom()}
		}
		// Only the count is short: evicting a model of another engine would not help, so the least
		// recently used model of the candidate's own engine goes (E11).
		victim := 0
		if !short.GPU && !short.RAM {
			victim = -1
			for j, k := range keep {
				if engineOf(k.Engine) == engineOf(m.Engine) {
					victim = j
					break
				}
			}
			if victim < 0 {
				return nil, nil, &PlanError{Reason: reasonNoRoom()}
			}
		}
		evict = append(evict, keep[victim].ID)
		order = append(append(append([]Loaded{}, order[:i]...), keep[victim]), removeAt(keep, victim)...)
	}
}

// engineOf names the engine of a model; "" is llama.
func engineOf(engine string) string {
	if engine == "" {
		return catalog.EngineLlama
	}
	return engine
}

// sameEngine counts the models of keep that run on engine.
func sameEngine(engine string, keep []Loaded) int {
	n := 0
	for _, k := range keep {
		if engineOf(k.Engine) == engineOf(engine) {
			n++
		}
	}
	return n
}

// engineMax is the number of models one engine runs at once: models_max for llama, one for any
// other engine (the music engine holds one model, E11).
func (e Env) engineMax(engine string) int {
	if engineOf(engine) == catalog.EngineLlama {
		return e.modelsMax()
	}
	return 1
}

func removeAt(l []Loaded, i int) []Loaded {
	out := make([]Loaded, 0, len(l)-1)
	out = append(out, l[:i]...)
	return append(out, l[i+1:]...)
}

// evictionOrder returns the loaded models without m itself and without duplicate IDs, least
// recently used first; ties by larger used_v, then by ID.
func evictionOrder(skip string, loaded []Loaded) []Loaded {
	seen := make(map[string]bool, len(loaded))
	out := make([]Loaded, 0, len(loaded))
	for _, l := range loaded {
		if l.ID == skip || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.LastUsed.Equal(b.LastUsed) {
			return a.LastUsed.Before(b.LastUsed)
		}
		if av, bv := a.UsedVRAM(), b.UsedVRAM(); av != bv {
			return av > bv
		}
		return a.ID < b.ID
	})
	return out
}
