package router

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestDynamicFit(t *testing.T) {
	// ai-n2: cuda 12288 MiB, M 512, N 2.
	n2 := Env{Backend: "cuda", GTotalMiB: 12288, MarginMiB: 512, ModelsMax: 2, RTotalMiB: 32768, RAvailMiB: 28000}
	m := cand("vlm", 3200, 2000)
	chat := loaded("chat-9b", 7000, 8000, at(1))

	cases := []struct {
		name string
		env  Env
		keep []Loaded
		want Shortfall
	}{
		{"empty keep set fits", n2, nil, Shortfall{}},
		{"one kept, exactly at G_total", withOthers(n2, 12288-512-3200-7000), []Loaded{chat}, Shortfall{}},
		{"one kept, one MiB over G_total", withOthers(n2, 12288-512-3200-7000+1), []Loaded{chat}, Shortfall{GPU: true}},
		{"count: N reached", withMax(n2, 1), []Loaded{chat}, Shortfall{Count: true}},
		{"count: one below N", withMax(n2, 2), []Loaded{chat}, Shortfall{}},
		{"count and GPU both short", Env{Backend: "cuda", GTotalMiB: 8000, MarginMiB: 512, ModelsMax: 1}, []Loaded{chat}, Shortfall{Count: true, GPU: true}},
		{"whisper reserve counts", withWhisper(n2, 1577), []Loaded{chat}, Shortfall{GPU: true}},
		{"whisper reserve at the limit", withWhisper(n2, 1576), []Loaded{chat}, Shortfall{}},
		{"reported effective memory replaces need_v", n2, []Loaded{withEffective(chat, 9000)}, Shortfall{GPU: true}},
		{"effective memory not reported (nil) uses need_v", n2, []Loaded{chat}, Shortfall{}},
		{"effective memory zero counts as not reported", n2, []Loaded{withEffective(chat, 0)}, Shortfall{}},
		{"cpu backend skips the GPU rule", Env{Backend: "cpu", ModelsMax: 2, RAvailMiB: 20000, RTotalMiB: 32768, MarginMiB: 512}, []Loaded{chat}, Shortfall{}},
		{"a vulkan dedicated card skips the RAM rule", Env{Backend: "vulkan", GTotalMiB: 12288, MarginMiB: 512, ModelsMax: 2, RAvailMiB: 10}, nil, Shortfall{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.env.DynamicFit(m, c.keep, nil); got != c.want {
				t.Errorf("DynamicFit = %+v, want %+v", got, c.want)
			}
		})
	}

	t.Run("RAM rule on cpu with eviction credit", func(t *testing.T) {
		cpu := Env{Backend: "cpu", ModelsMax: 3, RTotalMiB: 16384, RAvailMiB: 9000, MarginMiB: 512}
		a := loaded("a", 0, 4000, at(1))
		b := loaded("b", 0, 3000, at(2))
		cm := cand("new", 0, 3000)
		// keep both, nothing evicted: 3000+4000+3000 = 10000 > 9000-2048.
		if got := cpu.DynamicFit(cm, []Loaded{a, b}, nil); !got.RAM {
			t.Errorf("keep both: %+v", got)
		}
		// keep b, a evicted: 3000+3000 = 6000 <= 9000+4000-2048 = 10952.
		if got := cpu.DynamicFit(cm, []Loaded{b}, []Loaded{a}); got.Any() {
			t.Errorf("keep b: %+v", got)
		}
	})

	t.Run("unified must pass both the GPU and the RAM rule", func(t *testing.T) {
		u := Env{Backend: "vulkan", Unified: true, GTotalMiB: 61440, MarginMiB: 512, ModelsMax: 3, RTotalMiB: 65536, RAvailMiB: 7000}
		um := cand("new", 1000, 3000)
		// RAM: 3000 + 0 <= 7000-4096 = 2904 fails; GPU fine.
		if got := u.DynamicFit(um, nil, nil); got != (Shortfall{RAM: true}) {
			t.Errorf("RAM only: %+v", got)
		}
		u.RAvailMiB = 7096 // 3000 <= 3000
		if got := u.DynamicFit(um, nil, nil); got.Any() {
			t.Errorf("boundary: %+v", got)
		}
		u.GOthersMiB = 61440 // GPU now short as well
		if got := u.DynamicFit(um, nil, nil); got != (Shortfall{GPU: true}) {
			t.Errorf("GPU only: %+v", got)
		}
	})
}

func TestPlanAcceptanceCases(t *testing.T) {
	t.Run("ai-n3: load the VLM while the 9B is loaded (N=1) evicts the 9B", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		evict, warn, err := e.Plan(cand("vlm-small", 3200, 2500), []Loaded{loaded("chat-9b", 7000, 8000, at(5))}, true)
		expectPlan(t, evict, warn, err, []string{"chat-9b"})
	})

	t.Run("ai-n3: unload_others false refuses with LAI-LLM-011", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		evict, warn, err := e.Plan(cand("vlm-small", 3200, 2500), []Loaded{loaded("chat-9b", 7000, 8000, at(5))}, false)
		if evict != nil || warn != nil {
			t.Errorf("evict=%v warn=%+v on a refusal", evict, warn)
		}
		var pe *PlanError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v, want *PlanError", err)
		}
		if pe.Reason.Code != CodeNoRoom || pe.Reason.Message == "" || pe.Reason.MessageES == "" {
			t.Errorf("reason = %+v", pe.Reason)
		}
		if err.Error() != "LAI-LLM-011: There is not enough room to load this model without unloading another one." {
			t.Errorf("Error() = %q", err.Error())
		}
	})

	t.Run("ai-n3: nothing loaded, nothing to evict", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		evict, warn, err := e.Plan(cand("chat-9b", 7000, 8000), nil, false)
		expectPlan(t, evict, warn, err, nil)
	})

	// ai-n1: unified Vulkan, N=3. G_total 62464 (GTT), G_others 1000, M 512.
	n1 := NewEnv(snapAiN1(), "", routerBlock(3, 0), 0)
	older := loaded("moe-35b", 20000, 8000, at(5))
	older.EffectiveMiB = i64p(21000) // the router reports more than the catalog minimum
	newer := loaded("chat-9b", 10000, 8000, at(10))
	pair := []Loaded{newer, older} // deliberately not in LRU order

	t.Run("ai-n1 N=3, two loaded, VRAM fits: no eviction", func(t *testing.T) {
		evict, warn, err := n1.Plan(cand("vlm", 15000, 4000), pair, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("ai-n1 boundary: exactly G_total still fits", func(t *testing.T) {
		// 62464 - 1000 (others) - 512 (margin) - 21000 - 10000 = 29952
		evict, warn, err := n1.Plan(cand("vlm", 29952, 4000), pair, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("ai-n1 one MiB over: the least recently used goes", func(t *testing.T) {
		evict, warn, err := n1.Plan(cand("vlm", 29953, 4000), pair, true)
		expectPlan(t, evict, warn, err, []string{"moe-35b"})
	})
	t.Run("ai-n1 needs both gone when one is not enough", func(t *testing.T) {
		// Without the older one: 61464-512-10000 = 50952 available; ask for more than that.
		evict, warn, err := n1.Plan(cand("huge", 50953, 4000), pair, true)
		expectPlan(t, evict, warn, err, []string{"moe-35b", "chat-9b"})
	})
	t.Run("ai-n1 with unload_others false and room: still fine", func(t *testing.T) {
		evict, warn, err := n1.Plan(cand("vlm", 15000, 4000), pair, false)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("ai-n1 with unload_others false and no room: LLM-011", func(t *testing.T) {
		_, _, err := n1.Plan(cand("vlm", 29953, 4000), pair, false)
		wantNoRoom(t, err)
	})
	t.Run("ai-n1 count: the fourth model needs an eviction even with plenty of memory", func(t *testing.T) {
		three := append([]Loaded{loaded("tiny", 500, 1000, at(1))}, pair...)
		evict, warn, err := n1.Plan(cand("vlm", 1000, 1000), three, true)
		expectPlan(t, evict, warn, err, []string{"tiny"})
	})
	t.Run("count: more loaded than N (LAI-LLM-028) evicts down to N-1", func(t *testing.T) {
		e := Env{Backend: "cuda", GTotalMiB: 60000, MarginMiB: 512, ModelsMax: 2}
		three := []Loaded{loaded("c", 100, 0, at(3)), loaded("a", 100, 0, at(1)), loaded("b", 100, 0, at(2))}
		evict, warn, err := e.Plan(cand("new", 100, 0), three, true)
		expectPlan(t, evict, warn, err, []string{"a", "b"})
	})
	t.Run("ai-n1 unified: RAM rule can force an eviction while graphics memory has room", func(t *testing.T) {
		env := n1
		env.RAvailMiB = 20000
		// RAM: 4000 + 8000 + 8000 = 20000 > 20000-4096 -> evict the older one (credit 8000):
		// 4000 + 8000 = 12000 <= 20000+8000-4096.
		evict, warn, err := env.Plan(cand("vlm", 3000, 4000), pair, true)
		expectPlan(t, evict, warn, err, []string{"moe-35b"})
	})

	t.Run("G_others alone too big: load anyway with LAI-HW-005", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		e.GOthersMiB = 7900 // 3200 + 7900 + 512 > 8192 even with nothing else loaded
		evict, warn, err := e.Plan(cand("vlm-small", 3200, 2500), nil, true)
		expectPlan(t, evict, warn, err, nil)
		wantWarn(t, warn, CodeOthersVRAM)
	})
	t.Run("G_others alone too big, unload_others false: warning, not a refusal", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		e.GOthersMiB = 7900
		evict, warn, err := e.Plan(cand("vlm-small", 3200, 2500), nil, false)
		expectPlan(t, evict, warn, err, nil)
		wantWarn(t, warn, CodeOthersVRAM)
	})
	t.Run("G_others too big even after evicting the 9B: the eviction stays in the plan", func(t *testing.T) {
		e := NewEnv(snapAiN3(), "", routerBlock(1, 0), 0)
		e.GOthersMiB = 7900
		evict, warn, err := e.Plan(cand("vlm-small", 3200, 2500), []Loaded{loaded("chat-9b", 7000, 8000, at(5))}, true)
		expectPlan(t, evict, warn, err, []string{"chat-9b"})
		wantWarn(t, warn, CodeOthersVRAM)
	})

	t.Run("RAM taken by others on a cpu node: load anyway with LAI-HW-006", func(t *testing.T) {
		e := Env{Backend: "cpu", ModelsMax: 1, RTotalMiB: 16384, RAvailMiB: 3000, MarginMiB: 512}
		evict, warn, err := e.Plan(cand("chat-9b", 0, 2500), nil, true)
		expectPlan(t, evict, warn, err, nil)
		wantWarn(t, warn, CodeRAMLow)
	})
	t.Run("graphics shortage wins over RAM shortage in the warning", func(t *testing.T) {
		e := Env{Backend: "vulkan", Unified: true, GTotalMiB: 8000, GOthersMiB: 7900, ModelsMax: 1, RTotalMiB: 16384, RAvailMiB: 3000, MarginMiB: 512}
		_, warn, err := e.Plan(cand("chat", 3200, 2500), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		wantWarn(t, warn, CodeOthersVRAM)
	})

	t.Run("cpu node: LRU eviction when RAM is short", func(t *testing.T) {
		e := Env{Backend: "cpu", ModelsMax: 2, RTotalMiB: 16384, RAvailMiB: 12000, MarginMiB: 512}
		two := []Loaded{loaded("b", 0, 3000, at(2)), loaded("a", 0, 4000, at(1))}
		// 3000 + 4000 + 3000 = 10000 > 12000-2048 -> evict a; 3000+3000 <= 12000+4000-2048.
		evict, warn, err := e.Plan(cand("new", 0, 3000, "cpu"), two, true)
		expectPlan(t, evict, warn, err, []string{"a"})
	})
}

func TestPlanWhisperReserve(t *testing.T) {
	// ai-n2: 12288 MiB, N=2. The 9B (7000) is loaded; the VLM needs 3200. With M 512 that is 10712.
	snap := snapAiN2()
	chat := loaded("chat-9b", 7000, 8000, at(1))
	vlm := cand("vlm-small", 3200, 2500)

	t.Run("whisper on gpu and not running yet: its VRAM is reserved and the 9B goes", func(t *testing.T) {
		e := NewEnv(snap, "", routerBlock(2, 0), 2000) // 10712 + 2000 > 12288
		evict, warn, err := e.Plan(vlm, []Loaded{chat}, true)
		expectPlan(t, evict, warn, err, []string{"chat-9b"})
	})
	t.Run("whisper running: already in G_others, W_res is 0, no double count", func(t *testing.T) {
		s := snapAiN2()
		s.GPUs[0].VRAMUsedByOthersMiB = i64p(1500) // the running whisper, measured
		e := NewEnv(s, "", routerBlock(2, 0), 0)
		// 10712 + 1500 = 12212 <= 12288: fits. Counting whisper twice (1500 + 2000) would evict.
		evict, warn, err := e.Plan(vlm, []Loaded{chat}, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("whisper on cpu: W_res is 0", func(t *testing.T) {
		e := NewEnv(snap, "", routerBlock(2, 0), 0)
		evict, warn, err := e.Plan(vlm, []Loaded{chat}, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("the reserve is exact at the boundary", func(t *testing.T) {
		fits := NewEnv(snap, "", routerBlock(2, 0), 12288-10712)
		evict, warn, err := fits.Plan(vlm, []Loaded{chat}, true)
		expectPlan(t, evict, warn, err, nil)
		over := NewEnv(snap, "", routerBlock(2, 0), 12288-10712+1)
		evict, warn, err = over.Plan(vlm, []Loaded{chat}, true)
		expectPlan(t, evict, warn, err, []string{"chat-9b"})
	})
}

func TestPlanOrderingAndTieBreaks(t *testing.T) {
	// N=1 so exactly one eviction is needed per case; the case says which one goes first, and with
	// N=2 and a second needed slot the second goes next.
	e := Env{Backend: "cuda", GTotalMiB: 100000, MarginMiB: 512, ModelsMax: 1}
	m := cand("new", 100, 0)

	cases := []struct {
		name   string
		loaded []Loaded
		want   []string
	}{
		{"oldest last_used first", []Loaded{loaded("b", 500, 0, at(9)), loaded("a", 500, 0, at(1))}, []string{"a", "b"}},
		{"never used is the oldest", []Loaded{loaded("used", 500, 0, at(1)), loaded("never", 500, 0, time.Time{})}, []string{"never", "used"}},
		{"tie on last_used: larger used_v first", []Loaded{loaded("small", 3000, 0, at(4)), loaded("big", 7000, 0, at(4))}, []string{"big", "small"}},
		{"tie uses the reported effective memory, not need_v", []Loaded{
			loaded("catalog-big", 7000, 0, at(4)),
			withEffective(loaded("measured-big", 3000, 0, at(4)), 9000),
		}, []string{"measured-big", "catalog-big"}},
		{"full tie: ID order", []Loaded{loaded("m2", 5000, 0, at(4)), loaded("m1", 5000, 0, at(4))}, []string{"m1", "m2"}},
		{"last_used beats size", []Loaded{loaded("big-recent", 9000, 0, at(9)), loaded("small-old", 100, 0, at(1))}, []string{"small-old", "big-recent"}},
		{"two never used tie by size", []Loaded{loaded("s", 100, 0, time.Time{}), loaded("l", 900, 0, time.Time{})}, []string{"l", "s"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// With N=1 every loaded model must go: the whole eviction order is visible.
			evict, warn, err := e.Plan(m, c.loaded, true)
			expectPlan(t, evict, warn, err, c.want)
			// The order does not depend on the order the caller listed them in.
			rev := make([]Loaded, len(c.loaded))
			for i, l := range c.loaded {
				rev[len(rev)-1-i] = l
			}
			evict, warn, err = e.Plan(m, rev, true)
			expectPlan(t, evict, warn, err, c.want)
		})
	}
}

func TestPlanInputHandling(t *testing.T) {
	e := Env{Backend: "cuda", GTotalMiB: 100000, MarginMiB: 512, ModelsMax: 2}

	t.Run("the candidate itself in the loaded set is ignored", func(t *testing.T) {
		evict, warn, err := e.Plan(cand("same", 100, 0), []Loaded{loaded("same", 100, 0, at(1))}, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("duplicate ids count once", func(t *testing.T) {
		evict, warn, err := e.Plan(cand("new", 100, 0), []Loaded{loaded("a", 100, 0, at(1)), loaded("a", 100, 0, at(1))}, true)
		expectPlan(t, evict, warn, err, nil)
	})
	t.Run("models_max below one counts as one", func(t *testing.T) {
		z := e
		z.ModelsMax = 0
		evict, warn, err := z.Plan(cand("new", 100, 0), []Loaded{loaded("a", 100, 0, at(1))}, true)
		expectPlan(t, evict, warn, err, []string{"a"})
	})
	t.Run("the caller's slice is not reordered", func(t *testing.T) {
		in := []Loaded{loaded("b", 100, 0, at(9)), loaded("a", 100, 0, at(1))}
		z := e
		z.ModelsMax = 1
		if _, _, err := z.Plan(cand("new", 100, 0), in, true); err != nil {
			t.Fatal(err)
		}
		if in[0].ID != "b" || in[1].ID != "a" {
			t.Errorf("input reordered: %v %v", in[0].ID, in[1].ID)
		}
	})
	t.Run("the default model is evicted like any other (llama-router.md 4.3.3, R8)", func(t *testing.T) {
		// The spec has no pin: a default model that is the least recently used is the first to go.
		z := e
		z.ModelsMax = 1
		evict, _, err := z.Plan(cand("new", 100, 0), []Loaded{loaded("default-model", 100, 0, at(1))}, true)
		if err != nil || !reflect.DeepEqual(evict, []string{"default-model"}) {
			t.Errorf("evict=%v err=%v", evict, err)
		}
	})
}

// TestPlanProperties checks invariants over many random machines, loaded sets and candidates,
// recomputing the section 4.3.3 arithmetic independently of DynamicFit.
func TestPlanProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(25))
	backends := []string{"cuda", "vulkan", "vulkan-unified", "cpu"}
	seen := map[string]int{} // how often each outcome came up, so the generator cannot go stale

	for iter := 0; iter < 5000; iter++ {
		var e Env
		switch b := backends[rng.Intn(len(backends))]; b {
		case "vulkan-unified":
			e.Backend, e.Unified = "vulkan", true
		default:
			e.Backend = b
		}
		if e.Backend != "cpu" {
			e.GTotalMiB = int64(4000 + rng.Intn(60000))
			e.GOthersMiB = int64(rng.Intn(3) * rng.Intn(9000))
		}
		e.RTotalMiB = int64(8000 + rng.Intn(120000))
		e.RAvailMiB = int64(rng.Intn(int(e.RTotalMiB)))
		e.MarginMiB = int64(rng.Intn(3) * 512)
		e.ModelsMax = 1 + rng.Intn(4)
		if rng.Intn(3) == 0 {
			e.WhisperReserveMiB = int64(rng.Intn(3000))
		}

		var in []Loaded
		for i, n := 0, rng.Intn(5); i < n; i++ {
			l := loaded(fmt.Sprintf("m%d", i), int64(rng.Intn(15000)), int64(rng.Intn(12000)), time.Time{})
			if rng.Intn(5) > 0 { // some never used
				l.LastUsed = at(rng.Intn(4)) // few distinct times, so ties are common
			}
			if rng.Intn(3) == 0 {
				l.EffectiveMiB = i64p(int64(rng.Intn(16000)))
			}
			in = append(in, l)
		}
		m := cand("new", int64(rng.Intn(15000)), int64(rng.Intn(12000)))
		unload := rng.Intn(4) > 0

		evict, warn, err := e.Plan(m, in, unload)
		desc := fmt.Sprintf("iter %d env=%+v loaded=%d unload=%v evict=%v warn=%v err=%v", iter, e, len(in), unload, evict, warn, err)

		if err != nil {
			var pe *PlanError
			if !errors.As(err, &pe) || pe.Reason.Code != CodeNoRoom || evict != nil || warn != nil || unload || len(in) == 0 {
				t.Fatalf("bad refusal: %s", desc)
			}
			// A refusal means the full loaded set does not fit.
			if !e.DynamicFit(m, in, nil).Any() {
				t.Fatalf("refused although it fits: %s", desc)
			}
			seen["refused"]++
			continue
		}
		if evict == nil {
			t.Fatalf("evict is nil on success: %s", desc)
		}

		// The evicted set: known ids, no duplicates, never the candidate.
		byID := map[string]Loaded{}
		for _, l := range in {
			byID[l.ID] = l
		}
		gone := map[string]bool{}
		var evictedL []Loaded
		for _, id := range evict {
			l, ok := byID[id]
			if !ok || gone[id] || id == m.ID {
				t.Fatalf("bad evicted id %q: %s", id, desc)
			}
			gone[id] = true
			evictedL = append(evictedL, l)
		}
		var keep []Loaded
		for _, l := range in {
			if !gone[l.ID] {
				keep = append(keep, l)
			}
		}

		// Never evicts when everything already fits; never when unloading is not allowed.
		if len(evict) > 0 && !e.DynamicFit(m, in, nil).Any() {
			t.Fatalf("evicted although it fits: %s", desc)
		}
		if !unload && len(evict) > 0 {
			t.Fatalf("evicted with unload_others false: %s", desc)
		}

		// Eviction order is LRU first, ties by larger used_v, then ID; and what stays is never
		// "less recently used" than what went.
		for i := range evictedL {
			for _, k := range keep {
				if before(k, evictedL[i]) {
					t.Fatalf("kept %s is older than evicted %s: %s", k.ID, evictedL[i].ID, desc)
				}
			}
			if i > 0 && !before(evictedL[i-1], evictedL[i]) {
				t.Fatalf("evicted out of order: %s", desc)
			}
		}

		if warn == nil {
			// Never over-commits: the final keep set satisfies every rule of section 4.3.3.
			if len(keep)+1 > e.ModelsMax {
				t.Fatalf("over the model count: %s", desc)
			}
			if e.Backend != "cpu" {
				sum := m.Requirements.MinVRAMMiB + e.GOthersMiB + e.WhisperReserveMiB + e.MarginMiB
				for _, k := range keep {
					sum += usedV(k)
				}
				if sum > e.GTotalMiB {
					t.Fatalf("over-commits graphics memory (%d > %d): %s", sum, e.GTotalMiB, desc)
				}
			}
			if e.Unified || e.Backend == "cpu" {
				need := m.Requirements.MinRAMMiB
				for _, k := range keep {
					need += k.Requirements.MinRAMMiB
				}
				room := e.RAvailMiB - e.RAMReserve()
				for _, x := range evictedL {
					room += x.Requirements.MinRAMMiB
				}
				if need > room {
					t.Fatalf("over-commits RAM (%d > %d): %s", need, room, desc)
				}
			}
			// Minimal: putting the last evicted model back breaks the fit.
			if n := len(evictedL); n > 0 {
				if !e.DynamicFit(m, append(append([]Loaded{}, keep...), evictedL[n-1]), evictedL[:n-1]).Any() {
					t.Fatalf("evicted more than needed: %s", desc)
				}
			}
		} else {
			// A warning only comes when nothing is left to evict and it still does not fit.
			if len(keep) != 0 || len(evict) != len(in) {
				t.Fatalf("warning with models still kept: %s", desc)
			}
			if !e.DynamicFit(m, nil, evictedL).Any() {
				t.Fatalf("warning although it fits: %s", desc)
			}
			if warn.Code != CodeOthersVRAM && warn.Code != CodeRAMLow {
				t.Fatalf("unexpected warning code: %s", desc)
			}
			if warn.Code == CodeRAMLow && e.DynamicFit(m, nil, evictedL).GPU {
				t.Fatalf("RAM warning while graphics memory is short: %s", desc)
			}
		}

		switch {
		case warn != nil:
			seen[string(warn.Code)]++
		case len(evict) > 0:
			seen["evicted"]++
		default:
			seen["fits"]++
		}
		seen[e.Backend]++

		// Deterministic and independent of the caller's order.
		shuffled := append([]Loaded{}, in...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		evict2, warn2, err2 := e.Plan(m, shuffled, unload)
		if err2 != nil || !reflect.DeepEqual(evict, evict2) || !reflect.DeepEqual(warn, warn2) {
			t.Fatalf("not deterministic: %s -> %v %v %v", desc, evict2, warn2, err2)
		}
	}
	for _, k := range []string{"refused", "evicted", "fits", string(CodeOthersVRAM), string(CodeRAMLow), "cuda", "vulkan", "cpu"} {
		if seen[k] < 50 {
			t.Errorf("outcome %q came up only %d times in 5000 runs; tune the generator (%v)", k, seen[k], seen)
		}
	}
}

// --- helpers -------------------------------------------------------------------------------

func before(a, b Loaded) bool {
	if !a.LastUsed.Equal(b.LastUsed) {
		return a.LastUsed.Before(b.LastUsed)
	}
	if usedV(a) != usedV(b) {
		return usedV(a) > usedV(b)
	}
	return a.ID < b.ID
}

func usedV(l Loaded) int64 {
	if l.EffectiveMiB != nil && *l.EffectiveMiB > 0 {
		return *l.EffectiveMiB
	}
	return l.Requirements.MinVRAMMiB
}

func withOthers(e Env, v int64) Env  { e.GOthersMiB = v; return e }
func withMax(e Env, n int) Env       { e.ModelsMax = n; return e }
func withWhisper(e Env, v int64) Env { e.WhisperReserveMiB = v; return e }
func withEffective(l Loaded, v int64) Loaded {
	l.EffectiveMiB = i64p(v)
	return l
}

func expectPlan(t *testing.T, evict []string, warn *Reason, err error, want []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evict == nil {
		t.Fatal("evict is nil; want a non-nil slice")
	}
	if len(want) == 0 && len(evict) == 0 {
		return
	}
	if !reflect.DeepEqual(evict, want) {
		t.Errorf("evict = %v, want %v", evict, want)
	}
}

func wantWarn(t *testing.T, warn *Reason, code Code) {
	t.Helper()
	if warn == nil {
		t.Fatalf("warning = nil, want %s", code)
	}
	if warn.Code != code || warn.Message == "" || warn.MessageES == "" {
		t.Errorf("warning = %+v, want code %s with en and es text", warn, code)
	}
}

func wantNoRoom(t *testing.T, err error) {
	t.Helper()
	var pe *PlanError
	if !errors.As(err, &pe) || pe.Reason.Code != CodeNoRoom {
		t.Fatalf("err = %v, want LAI-LLM-011", err)
	}
}
