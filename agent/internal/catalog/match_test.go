package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

func i64(v int64) *int64 { return &v }

func rng(min, max *int64) *Range { return &Range{Min: min, Max: max} }

// loadHW reads a hardware snapshot fixture (the GET /hardware body).
func loadHW(t *testing.T, name string) hardware.Snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "hw", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s hardware.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// labProfiles are the node profiles of catalog.md section 4 with ranges that fit the lab nodes
// (max-128gb starts at 48 GiB of GPU memory so the 61 GiB of GTT of a unified node matches).
func labProfiles() []Profile {
	return []Profile{
		{ID: "small-8gb", Match: &Match{Backends: []string{"cuda"}, VRAMMiB: rng(i64(7000), i64(11999)), RAMMiB: rng(i64(12000), nil)}},
		{ID: ArkDriveProfileID},
		{ID: "max-128gb", Match: &Match{Backends: []string{"vulkan"}, VRAMMiB: rng(i64(49152), nil), RAMMiB: rng(i64(65536), nil)}},
		{ID: CPUOnlyProfileID, Match: &Match{Backends: []string{"cpu"}}},
		{ID: "cuda-12gb", Match: &Match{Backends: []string{"cuda"}, VRAMMiB: rng(i64(12000), i64(24999)), RAMMiB: rng(i64(12000), nil)}},
	}
}

func TestSuggestLabNodes(t *testing.T) {
	tests := []struct {
		hw      string
		want    string
		reasons []string
	}{
		{"ai-n3", "small-8gb", []string{"backend cuda", "vram 8192 MiB in 7000..11999", "ram 14336 MiB in 12000.."}},
		{"ai-n2", "cuda-12gb", []string{"backend cuda", "vram 12282 MiB in 12000..24999", "ram 32768 MiB in 12000.."}},
		{"ai-n1", "max-128gb", []string{"backend vulkan", "vram 62464 MiB in 49152..", "ram 124928 MiB in 65536.."}},
		{"cpu", CPUOnlyProfileID, []string{"backend cpu"}},
	}
	for _, tc := range tests {
		t.Run(tc.hw, func(t *testing.T) {
			got, reasons := Suggest(labProfiles(), loadHW(t, tc.hw))
			if got != tc.want {
				t.Fatalf("Suggest = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(reasons, tc.reasons) {
				t.Fatalf("reasons = %q, want %q", reasons, tc.reasons)
			}
		})
	}
}

func TestSuggestUnifiedMatchesOnGTTNotOnDedicatedVRAM(t *testing.T) {
	// ai-n1 reports the GTT (61 GiB) as its fit memory and matches max-128gb on it.
	snap := loadHW(t, "ai-n1")
	if got, _ := Suggest(labProfiles(), snap); got != "max-128gb" {
		t.Fatalf("unified node: %q", got)
	}
	// The same card seen as a discrete GPU with only its ~2 GiB of dedicated memory does not.
	snap.GPUs[0].UnifiedMemory = false
	snap.GPUs[0].VRAMTotalMiB = 2048
	got, reasons := Suggest(labProfiles(), snap)
	if got != CPUOnlyProfileID || !reflect.DeepEqual(reasons, []string{ReasonCatchAll}) {
		t.Fatalf("2 GiB of VRAM: %q %q, want the cpu-only catch-all", got, reasons)
	}
}

func TestSuggestCPUOnlyIsLast(t *testing.T) {
	// cpu-only with no match conditions would match a GPU node too, and it sorts before cuda-12gb
	// and small-8gb, yet the GPU profile wins.
	profs := labProfiles()
	for i := range profs {
		if profs[i].ID == CPUOnlyProfileID {
			profs[i].Match = &Match{}
		}
	}
	for _, hw := range []string{"ai-n3", "ai-n2", "ai-n1"} {
		got, _ := Suggest(profs, loadHW(t, hw))
		if got == CPUOnlyProfileID {
			t.Errorf("%s: cpu-only won over a GPU profile", hw)
		}
	}
	got, reasons := Suggest(profs, loadHW(t, "cpu"))
	if got != CPUOnlyProfileID || !reflect.DeepEqual(reasons, []string{ReasonAnyHardware}) {
		t.Errorf("cpu box: %q %q", got, reasons)
	}
}

func TestSuggestArkDriveNeverSelected(t *testing.T) {
	// ark-drive-full is skipped even when it wrongly carries a match that holds, and it is not a
	// fallback: with no cpu-only the answer is empty.
	profs := []Profile{{ID: ArkDriveProfileID, Match: &Match{}}}
	if got, reasons := Suggest(profs, loadHW(t, "ai-n3")); got != "" || reasons != nil {
		t.Fatalf("got %q %q, want none", got, reasons)
	}
	profs = append(profs, Profile{ID: "a-first", Match: &Match{Backends: []string{"cuda"}}})
	if got, _ := Suggest(profs, loadHW(t, "ai-n3")); got != "a-first" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestOverlapIsDeterministic(t *testing.T) {
	// Lint forbids overlapping node profiles; at runtime the lexical order of ids decides, whatever
	// the order of the list.
	a := Profile{ID: "a-gpu", Match: &Match{Backends: []string{"cuda"}}}
	b := Profile{ID: "b-gpu", Match: &Match{Backends: []string{"cuda"}, VRAMMiB: rng(i64(7000), nil)}}
	z := Profile{ID: "z-gpu", Match: &Match{}}
	for _, order := range [][]Profile{{a, b, z}, {z, b, a}, {b, z, a}} {
		cp := append([]Profile{}, order...)
		if got, _ := Suggest(cp, loadHW(t, "ai-n3")); got != "a-gpu" {
			t.Fatalf("order %v: %q", ids(order), got)
		}
		// The input slice is not reordered.
		if !reflect.DeepEqual(ids(cp), ids(order)) {
			t.Fatalf("Suggest reordered its input")
		}
	}
}

func ids(ps []Profile) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestSuggestBoundaries(t *testing.T) {
	small := []Profile{
		{ID: "small-8gb", Match: &Match{Backends: []string{"cuda"}, VRAMMiB: rng(i64(7000), i64(11999)), RAMMiB: rng(i64(12000), nil)}},
		{ID: CPUOnlyProfileID, Match: &Match{Backends: []string{"cpu"}}},
	}
	tests := []struct {
		name      string
		vram, ram int64
		want      string
	}{
		{"vram at min", 7000, 16000, "small-8gb"},
		{"vram one below min", 6999, 16000, CPUOnlyProfileID},
		{"vram at max", 11999, 16000, "small-8gb"},
		{"vram one above max", 12000, 16000, CPUOnlyProfileID},
		{"ram at min", 8192, 12000, "small-8gb"},
		{"ram one below min", 8192, 11999, CPUOnlyProfileID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := hardware.Snapshot{
				GPUs: []hardware.GPU{{Vendor: hardware.VendorNVIDIA, VRAMTotalMiB: tc.vram}},
				RAM:  hardware.RAM{TotalMiB: tc.ram},
			}
			if got, _ := Suggest(small, snap); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestMissingHardwareData(t *testing.T) {
	gpuMax := []Profile{
		{ID: "tiny-gpu", Match: &Match{Backends: []string{"cuda"}, VRAMMiB: rng(nil, i64(11999))}},
		{ID: "ram-capped", Match: &Match{Backends: []string{"cpu"}, RAMMiB: rng(nil, i64(8192))}},
		{ID: CPUOnlyProfileID, Match: &Match{Backends: []string{"cpu"}}},
	}
	tests := []struct {
		name string
		snap hardware.Snapshot
		want string
	}{
		// An NVIDIA GPU whose memory could not be read must not match a max-only range.
		{"cuda with unknown VRAM", hardware.Snapshot{
			GPUs: []hardware.GPU{{Vendor: hardware.VendorNVIDIA}}, RAM: hardware.RAM{TotalMiB: 16384}}, CPUOnlyProfileID},
		// Unknown RAM cannot satisfy a max-only range either.
		{"cpu with unknown RAM", hardware.Snapshot{}, CPUOnlyProfileID},
		{"cpu with RAM in range", hardware.Snapshot{RAM: hardware.RAM{TotalMiB: 4096}}, "ram-capped"},
		// The zero snapshot (nothing sampled yet) is a CPU box.
		{"zero snapshot", hardware.Snapshot{}, CPUOnlyProfileID},
		// A negative figure counts as unknown.
		{"negative RAM", hardware.Snapshot{RAM: hardware.RAM{TotalMiB: -1}}, CPUOnlyProfileID},
		// The snapshot's own backend wins over the GPU list.
		{"explicit backend", hardware.Snapshot{Backend: hardware.BackendCPU,
			GPUs: []hardware.GPU{{Vendor: hardware.VendorNVIDIA, VRAMTotalMiB: 8192}}, RAM: hardware.RAM{TotalMiB: 4096}}, "ram-capped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := Suggest(gpuMax, tc.snap); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestNoCPUOnlyProfile(t *testing.T) {
	profs := []Profile{{ID: "small-8gb", Match: &Match{Backends: []string{"cuda"}}}}
	if got, reasons := Suggest(profs, loadHW(t, "cpu")); got != "" || reasons != nil {
		t.Fatalf("got %q %q", got, reasons)
	}
}

func TestSuggestGPUTooSmallFallsToCPUOnly(t *testing.T) {
	snap := hardware.Snapshot{
		GPUs: []hardware.GPU{{Vendor: hardware.VendorNVIDIA, VRAMTotalMiB: 4096}},
		RAM:  hardware.RAM{TotalMiB: 16384},
	}
	got, reasons := Suggest(labProfiles(), snap)
	if got != CPUOnlyProfileID || !reflect.DeepEqual(reasons, []string{ReasonCatchAll}) {
		t.Fatalf("got %q %q", got, reasons)
	}
}

func TestMatchHolds(t *testing.T) {
	f := Facts{Backend: "cuda", GPUMemoryMiB: 8192, RAMMiB: 14336}
	var nilMatch *Match
	if ok, _ := nilMatch.Holds(f); ok {
		t.Error("a nil match must not hold")
	}
	if ok, r := (&Match{Backends: []string{"vulkan", "cuda"}}).Holds(f); !ok || !reflect.DeepEqual(r, []string{"backend cuda"}) {
		t.Errorf("backend list: %v %q", ok, r)
	}
	if ok, _ := (&Match{Backends: []string{"vulkan"}}).Holds(f); ok {
		t.Error("wrong backend held")
	}
	rs := []struct {
		r    *Range
		want string
	}{
		{rng(i64(1), i64(2)), "1..2"}, {rng(i64(1), nil), "1.."}, {rng(nil, i64(2)), "..2"}, {rng(nil, nil), ".."}, {nil, ".."},
	}
	for _, c := range rs {
		if got := c.r.String(); got != c.want {
			t.Errorf("Range.String = %q, want %q", got, c.want)
		}
	}
}

func TestCatalogSuggestUsesItsProfiles(t *testing.T) {
	c := newCatalog("v", nil, nil, labProfiles())
	if got, _ := c.Suggest(loadHW(t, "ai-n2")); got != "cuda-12gb" {
		t.Fatalf("got %q", got)
	}
}
