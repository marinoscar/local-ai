package kit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

const gb = int64(1_000_000_000)

// planFor plans one root of the fixture catalog the way `aictl ark sync --dry-run` does for a node
// that has nothing yet: every desired item resolves to the version the catalog pins (plan.FromCatalog),
// and to a made-up one where only a resolver could say (a git mirror, an apt closure).
func planFor(t *testing.T, c *catalog.Catalog, profile string, root plan.RootInfo, node plan.NodeInfo) plan.RootPlan {
	t.Helper()
	prof, ok := c.Profile(profile)
	if !ok {
		t.Fatalf("no profile %s", profile)
	}
	in := plan.PlanInput{Root: root, Catalog: c, Profile: prof, Node: node, Mode: plan.ModeOnline, Now: epoch,
		Present: nil, Resolved: map[string]plan.Resolved{}}
	for _, it := range plan.DesiredItems(c, prof, nil, root, node).Items {
		r, ok := plan.FromCatalog(it, c.Version)
		if !ok {
			r = plan.Resolved{Version: "head-0001", SizeBytes: it.CatalogBytes()}
		}
		in.Resolved[it.ID] = r
	}
	return plan.Plan(in)
}

func internalInfo(profile string) plan.RootInfo {
	return plan.RootInfo{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/srv/localai", Profile: profile,
		TotalBytes: 2000 * gb, FreeBytes: 1900 * gb}
}

func driveInfo() plan.RootInfo {
	return plan.RootInfo{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/media/operator/AI-ARK/localai", Profile: "ark-drive-full",
		TotalBytes: 4000 * gb, FreeBytes: 3900 * gb}
}

type kitRow struct {
	Pos    int         `json:"pos"`
	ID     string      `json:"id"`
	Dir    string      `json:"dir"`
	Action plan.Action `json:"action"`
	To     string      `json:"to_version,omitempty"`
	Bytes  int64       `json:"final_bytes"`
}

type kitPlan struct {
	Root       string   `json:"root"`
	Profile    string   `json:"profile"`
	Budget     int64    `json:"budget_bytes"`
	Planned    int64    `json:"planned_bytes"`
	Kit        []kitRow `json:"kit"`
	NotForNode []string `json:"not_for_node,omitempty"`
	Findings   []string `json:"findings,omitempty"`
}

func kitRowsOf(p plan.RootPlan) kitPlan {
	out := kitPlan{Root: p.Root, Profile: p.Profile, Budget: p.BudgetBytes, Planned: p.PlannedBytes, NotForNode: p.NotForNode}
	for _, it := range p.Items {
		if it.Kind == "kit" {
			out.Kit = append(out.Kit, kitRow{it.Pos, it.ID, it.Dir, it.Action, it.ToVersion, it.FinalBytes})
		}
	}
	for _, f := range p.Findings {
		out.Findings = append(out.Findings, f.Code)
	}
	return out
}

// The acceptance of KIT-01: a dry run on a fixture node plans each kit item into its section 3.1 path.
// The golden files hold the kit rows of the plan of each profile; the JSON is exactly what
// `aictl ark sync --dry-run --json | jq '.items[] | select(.kind=="kit")'` shows (id, dir, size).
func TestDryRunPlansEveryKitItemIntoItsPath(t *testing.T) {
	c := fixtureCatalog(t)
	cases := []struct {
		name    string
		profile string
		root    plan.RootInfo
		node    plan.NodeInfo
	}{
		{"small-8gb", "small-8gb", internalInfo("small-8gb"), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}},
		{"cuda-12gb", "cuda-12gb", internalInfo("cuda-12gb"), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}},
		{"max-128gb", "max-128gb", internalInfo("max-128gb"), plan.NodeInfo{Backend: "vulkan", Arch: "amd64", Codename: "resolute"}},
		{"cpu-only", "cpu-only", internalInfo("cpu-only"), plan.NodeInfo{Backend: "cpu", Arch: "amd64", Codename: "resolute"}},
		{"ark-drive-full", "ark-drive-full", driveInfo(), plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(t, c, tc.profile, tc.root, tc.node)
			kp := kitRowsOf(p)
			// every kit item the profile names is planned, none is dropped for budget, and each row's
			// dir is the layout's
			prof, _ := c.Profile(tc.profile)
			want := 0
			for _, id := range prof.Include.Content {
				if strings.HasPrefix(id, "kit-") {
					want++
				}
			}
			if len(kp.Kit)+len(kp.NotForNode) != want {
				t.Errorf("planned %d kit rows and filtered %d, the profile names %d", len(kp.Kit), len(kp.NotForNode), want)
			}
			for _, r := range kp.Kit {
				ci := item(t, c, r.ID)
				wantDir, err := kitItemDir(ci)
				if err != nil {
					t.Fatal(err)
				}
				if r.Dir != wantDir {
					t.Errorf("%s: dir = %q, want %q", r.ID, r.Dir, wantDir)
				}
				if r.Action != plan.ActionDownload {
					t.Errorf("%s: action = %s, want download on an empty root", r.ID, r.Action)
				}
				if r.Bytes != ci.ExpectedSizeBytes {
					t.Errorf("%s: size = %d, want the catalog's %d", r.ID, r.Bytes, ci.ExpectedSizeBytes)
				}
			}
			if len(kp.Findings) != 0 {
				t.Errorf("findings = %v", kp.Findings)
			}
			data, err := json.MarshalIndent(kp, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden(t, "plan-"+tc.name+".json", string(data)+"\n")
		})
	}
}

func kitItemDir(it catalog.ContentItem) (string, error) {
	l, err := kit.LayoutOf(it)
	if err != nil {
		return "", err
	}
	return "kit/" + l.Dir, nil
}

// An internal root keeps only the apt closures of its own codename and architecture (the planner's
// filter of kit items); a drive keeps all of them. Every other kit item is for every node.
func TestPlannerFiltersKitClosuresByCodenameAndArch(t *testing.T) {
	c := fixtureCatalog(t)
	cases := []struct {
		name     string
		root     plan.RootInfo
		node     plan.NodeInfo
		notFor   string // the filtered ids, space separated
		hasDebs  []string
		keepsGo  bool
		profile  string
		keepsISO bool
	}{
		{"resolute amd64 node keeps its closure and drops the arm64 one", internalInfo("ark-drive-full"), plan.NodeInfo{Arch: "amd64", Codename: "resolute"},
			"kit-debs-resolute-arm64", []string{"kit-debs-resolute-amd64"}, true, "ark-drive-full", true},
		{"resolute arm64 node drops the amd64 closure and the amd64 driver", internalInfo("ark-drive-full"), plan.NodeInfo{Arch: "arm64", Codename: "resolute"},
			"kit-debs-resolute-amd64 kit-nvidia-resolute-amd64-595-open", []string{"kit-debs-resolute-arm64"}, true, "ark-drive-full", true},
		{"a node on another Ubuntu release gets no closure at all", internalInfo("ark-drive-full"), plan.NodeInfo{Arch: "amd64", Codename: "noble"},
			"kit-debs-resolute-amd64 kit-debs-resolute-arm64 kit-nvidia-resolute-amd64-595-open", nil, true, "ark-drive-full", true},
		{"an unknown codename and arch filters nothing", internalInfo("ark-drive-full"), plan.NodeInfo{},
			"", []string{"kit-debs-resolute-amd64", "kit-debs-resolute-arm64"}, true, "ark-drive-full", true},
		{"the drive root is never filtered", driveInfo(), plan.NodeInfo{Arch: "amd64", Codename: "noble"},
			"", []string{"kit-debs-resolute-amd64", "kit-debs-resolute-arm64"}, true, "ark-drive-full", true},
		{"a CPU profile on the matching node", internalInfo("cpu-only"), plan.NodeInfo{Backend: "cpu", Arch: "amd64", Codename: "resolute"},
			"", []string{"kit-debs-resolute-amd64"}, true, "cpu-only", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(t, c, tc.profile, tc.root, tc.node)
			if got := strings.Join(p.NotForNode, " "); got != tc.notFor {
				t.Errorf("not_for_node = %q, want %q", got, tc.notFor)
			}
			rows := map[string]plan.ItemPlan{}
			for _, it := range p.Items {
				rows[it.ID] = it
			}
			for _, id := range tc.hasDebs {
				if _, ok := rows[id]; !ok {
					t.Errorf("%s is not planned", id)
				}
			}
			for _, id := range strings.Fields(tc.notFor) {
				if _, ok := rows[id]; ok {
					t.Errorf("%s is planned although it is for another node", id)
				}
			}
			if _, ok := rows["kit-go-toolchain"]; ok != tc.keepsGo {
				t.Errorf("kit-go-toolchain planned = %v", ok)
			}
			if _, ok := rows["kit-ubuntu-iso-26.04"]; ok != tc.keepsISO {
				t.Errorf("the ISO is planned = %v, want %v", ok, tc.keepsISO)
			}
		})
	}
}

// The kit is pinned class: when it does not fit, the plan says so (LAI-SYN-042) instead of dropping it,
// and every kit row keeps its directory.
func TestKitRowsAreNeverDroppedForBudget(t *testing.T) {
	c := fixtureCatalog(t)
	root := internalInfo("small-8gb")
	prof, _ := c.Profile("small-8gb")
	small := *prof
	small.Storage.BudgetBytes = 3 * gb
	in := plan.PlanInput{Root: root, Catalog: c, Profile: &small, Node: plan.NodeInfo{Backend: "cuda", Arch: "amd64", Codename: "resolute"},
		Mode: plan.ModeOnline, Now: epoch, Resolved: map[string]plan.Resolved{}}
	for _, it := range plan.DesiredItems(c, &small, nil, root, in.Node).Items {
		in.Resolved[it.ID] = plan.Resolved{Version: "v1", SizeBytes: it.CatalogBytes()}
	}
	p := plan.Plan(in)
	found := false
	for _, f := range p.Findings {
		found = found || f.Code == "LAI-SYN-042"
	}
	if !found {
		t.Errorf("no LAI-SYN-042 for a kit that does not fit: %+v", p.Findings)
	}
	for _, it := range p.Items {
		if it.Kind == "kit" && it.Dir == "" {
			t.Errorf("%s lost its directory", it.ID)
		}
	}
}
