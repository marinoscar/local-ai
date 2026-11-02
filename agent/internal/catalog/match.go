package catalog

import (
	"fmt"
	"sort"

	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// Profile selection (catalog.md section 4, "Profile selection"): which node profile fits a machine.
// Everything here is pure: the hardware snapshot is passed in, nothing is read or written.

// ReasonCatchAll is the match reason of the cpu-only profile when it is chosen because no other
// profile fits the machine (its own match block, if any, does not hold).
const ReasonCatchAll = "no other profile fits this hardware"

// ReasonAnyHardware is the match reason of a profile whose match block states no condition.
const ReasonAnyHardware = "no hardware requirements"

// Facts are the three figures a profile's match block compares, taken from one hardware snapshot.
type Facts struct {
	// Backend is the detected backend: cuda, vulkan or cpu.
	Backend string
	// GPUMemoryMiB is G_total of llama-router.md section 4.3.1 for Backend: dedicated VRAM on a
	// discrete GPU, the usable GTT on a unified-memory node, 0 on cpu. Match.VRAMMiB is compared
	// with this figure, so the match rules and the fit rules never disagree.
	GPUMemoryMiB int64
	// RAMMiB is MemTotal. 0 means the sampler could not read it.
	RAMMiB int64
}

// DetectBackend is the node's backend as the hardware layer detected it (agent.md section 4.4.3):
// the snapshot's own Backend when set, else derived from the GPUs (NVIDIA, then AMD, else cpu).
func DetectBackend(snap hardware.Snapshot) string {
	if snap.Backend != "" {
		return snap.Backend
	}
	var nvidia, amd bool
	for _, g := range snap.GPUs {
		switch g.Vendor {
		case hardware.VendorNVIDIA:
			nvidia = true
		case hardware.VendorAMD:
			amd = true
		}
	}
	return hardware.BackendFor(nvidia, amd)
}

// FactsOf distils a snapshot into the figures a match block compares.
func FactsOf(snap hardware.Snapshot) Facts {
	b := DetectBackend(snap)
	f := Facts{Backend: b, GPUMemoryMiB: snap.FitMemory(b), RAMMiB: snap.RAM.TotalMiB}
	if f.GPUMemoryMiB < 0 {
		f.GPUMemoryMiB = 0
	}
	if f.RAMMiB < 0 {
		f.RAMMiB = 0
	}
	return f
}

// Holds reports whether the match block holds for the facts and, when it does, the plain-language
// reasons, one per condition the block states, for example "backend cuda" and
// "vram 8192 MiB in 7000..11999". A nil block never holds (such a profile is not a node profile).
//
// Missing hardware data never satisfies a condition: a range on RAM cannot hold when the RAM total
// is unknown (0), and a range on graphics memory cannot hold on a GPU backend whose memory is
// unknown (0). On the cpu backend graphics memory is a real 0 and a range is compared as is.
// An empty backends list means any backend; a missing range means any value.
func (m *Match) Holds(f Facts) (bool, []string) {
	if m == nil {
		return false, nil
	}
	var reasons []string
	if len(m.Backends) > 0 {
		if !contains(m.Backends, f.Backend) {
			return false, nil
		}
		reasons = append(reasons, "backend "+f.Backend)
	}
	if r := m.VRAMMiB; r != nil {
		if f.Backend != hardware.BackendCPU && f.GPUMemoryMiB <= 0 {
			return false, nil
		}
		if !r.contains(f.GPUMemoryMiB) {
			return false, nil
		}
		reasons = append(reasons, fmt.Sprintf("vram %d MiB in %s", f.GPUMemoryMiB, r))
	}
	if r := m.RAMMiB; r != nil {
		if f.RAMMiB <= 0 || !r.contains(f.RAMMiB) {
			return false, nil
		}
		reasons = append(reasons, fmt.Sprintf("ram %d MiB in %s", f.RAMMiB, r))
	}
	if len(reasons) == 0 {
		reasons = []string{ReasonAnyHardware}
	}
	return true, reasons
}

// contains reports whether v is inside the inclusive range; a missing bound is open.
func (r *Range) contains(v int64) bool {
	if r == nil {
		return true
	}
	return (r.Min == nil || v >= *r.Min) && (r.Max == nil || v <= *r.Max)
}

// String renders the range as "min..max", with an open side left empty ("12000..", "..11999").
func (r *Range) String() string {
	if r == nil {
		return ".."
	}
	var lo, hi string
	if r.Min != nil {
		lo = fmt.Sprint(*r.Min)
	}
	if r.Max != nil {
		hi = fmt.Sprint(*r.Max)
	}
	return lo + ".." + hi
}

// Suggest picks the profile for a machine and says why. Node profiles (every profile with a match
// block, except ark-drive-full) are evaluated in lexical order of profile id with cpu-only always
// last; the first whose match holds wins, so the result is deterministic even if two ranges
// overlap (lint, LAI-CAT-018, forbids that, but a catalog is data). ark-drive-full is never chosen,
// even if it wrongly carries a match.
//
// cpu-only is the catch-all: when no other profile holds it is chosen even if its own match block
// does not (a machine with a GPU too small for any other profile still lands on it), with the
// single reason ReasonCatchAll. With no cpu-only profile in the list and no match, the result is
// ("", nil). profiles is not modified.
func Suggest(profiles []Profile, snap hardware.Snapshot) (profileID string, reasons []string) {
	f := FactsOf(snap)
	var node []*Profile
	var cpu *Profile
	for i := range profiles {
		p := &profiles[i]
		switch {
		case p.ID == ArkDriveProfileID:
			continue
		case p.ID == CPUOnlyProfileID:
			if cpu == nil {
				cpu = p
			}
		case p.Match != nil:
			node = append(node, p)
		}
	}
	sort.SliceStable(node, func(i, j int) bool { return node[i].ID < node[j].ID })
	for _, p := range node {
		if ok, why := p.Match.Holds(f); ok {
			return p.ID, why
		}
	}
	if cpu == nil {
		return "", nil
	}
	if ok, why := cpu.Match.Holds(f); ok {
		return cpu.ID, why
	}
	return cpu.ID, []string{ReasonCatchAll}
}

// Suggest is the package function over the catalog's own profiles.
func (c *Catalog) Suggest(snap hardware.Snapshot) (profileID string, reasons []string) {
	return Suggest(c.Profiles, snap)
}
