package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/setup/detect"
)

// SurvivalModel is one model the profile keeps for survival mode.
type SurvivalModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Suggestion is the JSON of `aiagent setup profile-suggest --json` (installer.md section 4.3 P3).
type Suggestion struct {
	Schema      string   `json:"schema"`
	Profile     string   `json:"profile"`      // the profile that will be used
	DisplayName string   `json:"display_name"` // plain-language name
	Suggested   string   `json:"suggested"`    // what the hardware matches
	Reasons     []string `json:"reasons"`      // why the suggestion matches
	Override    bool     `json:"override"`     // --profile was given
	// Mismatch is true when the chosen profile's match block does not hold for this hardware.
	Mismatch        bool            `json:"mismatch"`
	MismatchCode    string          `json:"mismatch_code,omitempty"`
	MismatchMessage string          `json:"mismatch_message,omitempty"`
	Backend         string          `json:"backend"`
	VRAMMiB         int64           `json:"vram_mib"`
	RAMMiB          int64           `json:"ram_mib"`
	BudgetBytes     int64           `json:"budget_bytes"`
	MinFreeBytes    int64           `json:"min_free_bytes"`
	DefaultModel    string          `json:"default_model"`
	Survival        []SurvivalModel `json:"survival_models"`
	CatalogVersion  string          `json:"catalog_version"`
}

// Snapshot builds the hardware snapshot the catalog's match rules read from what preflight detected.
func Snapshot(gpu detect.GPU, ramMiB int64) hardware.Snapshot {
	s := hardware.Snapshot{Backend: gpu.Backend}
	s.RAM.TotalMiB = ramMiB
	if gpu.Backend == detect.BackendCUDA || gpu.Backend == detect.BackendVulkan {
		vendor := gpu.Vendor
		g := hardware.GPU{Vendor: vendor, Name: gpu.Name, Backend: gpu.Backend, VRAMTotalMiB: gpu.VRAMMiB}
		if vendor == hardware.VendorAMD {
			g.UnifiedMemory = true
		}
		s.GPUs = []hardware.GPU{g}
	}
	return s
}

// Suggest evaluates the catalog for this machine. override ("" = none) is the --profile value.
func Suggest(c *catalog.Catalog, snap hardware.Snapshot, override string) (Suggestion, error) {
	id, reasons := c.Suggest(snap)
	out := Suggestion{Schema: "localai.profile-suggest/1", Suggested: id, Reasons: append([]string{}, reasons...),
		Backend: catalog.DetectBackend(snap), VRAMMiB: catalog.FactsOf(snap).GPUMemoryMiB, RAMMiB: snap.RAM.TotalMiB,
		CatalogVersion: c.Version, Survival: []SurvivalModel{}}
	chosen := id
	if override != "" {
		chosen, out.Override = override, true
	}
	p, ok := c.Profile(chosen)
	if !ok {
		return out, fmt.Errorf("%s: profile %q is not in the catalog", doctor.CAT003, chosen)
	}
	out.Profile, out.DisplayName = p.ID, p.DisplayName
	out.BudgetBytes, out.MinFreeBytes, out.DefaultModel = p.Storage.BudgetBytes, p.Storage.MinFreeBytes, p.DefaultModel
	for _, m := range p.Survival.Models {
		sm := SurvivalModel{ID: m, DisplayName: m}
		if mm, ok := c.Model(m); ok {
			sm.DisplayName = mm.DisplayName
		}
		out.Survival = append(out.Survival, sm)
	}
	if out.Override && p.ID != id {
		if holds, _ := p.Match.Holds(catalog.FactsOf(snap)); !holds {
			out.Mismatch, out.MismatchCode = true, doctor.CAT005
			out.MismatchMessage = fmt.Sprintf("Profile %q is meant for different hardware than this computer has (this computer matches %q).", p.ID, id)
		}
	}
	return out, nil
}

func runProfileSuggest(ctx context.Context, d Deps, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("profile-suggest", stderr)
	var asJSON, dry bool
	var override, backend, dir, ark string
	fs.BoolVar(&asJSON, "json", false, "print JSON")
	fs.BoolVar(&dry, "dry-run", false, "accepted for symmetry: this step never changes anything")
	fs.StringVar(&override, "profile", "", "use this profile instead of the suggestion")
	fs.StringVar(&backend, "backend", "", "force the backend (cuda, vulkan, cpu)")
	fs.StringVar(&dir, "catalog", "", "catalog folder (default: the installed catalog)")
	fs.StringVar(&ark, "from-ark", "", "ark mount: read the catalog from its kit when none is installed")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 || (backend != "" && backend != "cuda" && backend != "vulkan" && backend != "cpu") {
		fmt.Fprint(stderr, inst02Usage)
		return ExitUsage
	}
	if dir == "" {
		dir = d.CatalogDir
		if _, err := d.Env.ReadFile(filepath.Join(dir, "CATALOG_VERSION")); err != nil && ark != "" {
			dir = filepath.Join(ark, "localai", "kit", "catalog")
		}
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup profile-suggest: the catalog in %s cannot be read: %v\n", dir, err)
		return ExitFailed
	}
	gpu := d.Env.DetectGPU(ctx)
	if gpu.DriverBroken {
		gpu.Backend = detect.BackendCPU
	}
	if backend != "" {
		gpu.Backend = backend
	}
	s, err := Suggest(cat, Snapshot(gpu, d.Env.MemTotalMiB()), override)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup profile-suggest: %v\n", err)
		return ExitFailed
	}
	if asJSON {
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return ExitOK
	}
	printSuggestion(stdout, s)
	return ExitOK
}

// printSuggestion says it in plain words (installer.md section 4.3 P3).
func printSuggestion(w io.Writer, s Suggestion) {
	fmt.Fprintf(w, "Profile: %s (%s)\n", s.DisplayName, s.Profile)
	if s.Override {
		fmt.Fprintf(w, "  chosen by you with --profile; the hardware would suggest %q\n", s.Suggested)
	} else {
		fmt.Fprintf(w, "  chosen because: %s\n", strings.Join(s.Reasons, ", "))
	}
	fmt.Fprintf(w, "  content budget: %s, keep at least %s free\n", gb(uint64(s.BudgetBytes)), gb(uint64(s.MinFreeBytes)))
	names := make([]string, 0, len(s.Survival))
	for _, m := range s.Survival {
		names = append(names, m.DisplayName)
	}
	fmt.Fprintf(w, "  survival models (always kept for emergencies): %s\n", strings.Join(names, ", "))
	if s.Mismatch {
		fmt.Fprintf(w, "  WARNING %s: %s\n", s.MismatchCode, s.MismatchMessage)
	}
}
