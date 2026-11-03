package plan

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// ProfileOverrides is node.yaml profile_overrides (catalog.md section 4, "Node overrides"). It is the
// catalog package's type, so a caller converts the node.yaml block with catalog.Overrides(cfg...).
type ProfileOverrides = catalog.Overrides

// Modes, as the planner sees them (state.Mode values, kept as plain strings so this package does not
// import the state store). Only the two that forbid the network matter here.
const (
	ModeOnline   = "online"
	ModeOffline  = "offline"
	ModeSurvival = "survival"
)

// RootInfo is the content root a plan is for: the facts of ROOT.json and statfs the planner needs.
type RootInfo struct {
	// ID is "internal" or "ark-<8 hex>".
	ID string
	// Kind is ark.KindInternal or ark.KindArkDrive; anything else is planned like the internal root.
	Kind ark.RootKind
	// Path is the root directory, shown in the human plan header.
	Path string
	// Profile is the profile id the root is planned with (informational: the caller passes the
	// profile itself in PlanInput.Profile).
	Profile string
	// TotalBytes and FreeBytes are the filesystem size and the bytes a non-root process may still use.
	TotalBytes, FreeBytes int64
	// ReadOnly: nothing may be written (read-only mount, unclean drive, network filesystem). Every
	// action but keep becomes skip (read_only).
	ReadOnly bool
}

// NodeInfo is what the planner needs to know about this machine: the hardware (informational, the
// profile already encodes it) and the codename and architecture that filter the kit items (rebuild-kit.md
// section 3.2). An empty codename or arch means "unknown" and filters nothing.
type NodeInfo struct {
	Backend  string
	VRAMMiB  int64
	RAMMiB   int64
	Arch     string
	Codename string
}

// PartState is what the journal and the staging directory already hold for the item on this root:
// bytes that need not be fetched again. It counts only when Version is the target version.
type PartState struct {
	Version string
	// PartBytes are the bytes of the .part files, StagedBytes those of fully downloaded files that
	// wait for the swap.
	PartBytes, StagedBytes int64
}

func (p PartState) bytes() int64 { return p.PartBytes + p.StagedBytes }

// TrashEntry is one superseded version in .trash/<item>/<version>/ (ark-sync.md section 4.10). The
// planner only counts it: Eligible says whether retention would let it be purged to make room (the
// live replacement is verified), and the planner uses the eligible bytes to decide whether an item
// can start (section 4.8). It never purges anything.
type TrashEntry struct {
	ItemID, Version string
	Bytes           int64
	// TrashedAt orders the purge: oldest first.
	TrashedAt time.Time
	Eligible  bool
}

// Resolved is what the planner needs of a resolver's answer (ark-sync.md section 4.9): the concrete
// target version and the exact size. The resolvers (SYNC-09 to SYNC-11) have a richer type with the
// file list; an adapter reduces it to this. SizeBytes 0 means "unknown": the catalog size is used.
type Resolved struct {
	Version string
	SortKey string
	// SizeBytes is the total size of the target version.
	SizeBytes int64
}

// RootCopy is a verified copy of an item on another root (local-copy sourcing).
type RootCopy struct {
	RootID   string
	Manifest manifest.Manifest
}

// PlanInput is everything the planner reads (ark-sync.md section 4.4). Plan is a pure function of it:
// no clock, no filesystem, no network.
type PlanInput struct {
	Root    RootInfo
	Catalog *catalog.Catalog
	Profile *catalog.Profile
	// Overrides is node.yaml profile_overrides for the internal root; nil for a drive root.
	Overrides *ProfileOverrides
	Node      NodeInfo
	// Present holds the verified copies on this root, by item id.
	Present map[string]manifest.Manifest
	// Partial is the journal's view of unfinished downloads on this root, by item id.
	Partial map[string]PartState
	// Trash lists the superseded versions on this root.
	Trash []TrashEntry
	// Resolved is the resolvers' output by item id; an absent id is unresolved (offline, resolver
	// failed, no resolver yet). For an import the caller fills it from the drive's MANIFESTs.
	Resolved map[string]Resolved
	// OtherRoots lists, per item id, the verified copies on the other mounted roots, best first (the
	// order of ark.Index.Copies). A copy of the target version there makes the item a local copy.
	OtherRoots map[string][]RootCopy
	// Mode is the node mode ("online", "offline", "survival"); informational, and survival and
	// offline also forbid the network.
	Mode string
	// Offline forbids the network (connectivity none, or a caller that is importing). An item whose
	// target version has no local copy is then unresolved. Local copies work in every mode.
	Offline bool
	// Now is the only clock the planner has.
	Now time.Time
}

// Actions of an item (ark-sync.md section 4.4.4).
type Action string

// The actions.
const (
	ActionKeep     Action = "keep"
	ActionDownload Action = "download"
	ActionUpdate   Action = "update"
	ActionSkip     Action = "skip"
	ActionOrphan   Action = "orphan"
)

// Reason says why an item got its action. The values are the closed set of the spec; time_limit and
// backend are produced by other parts (the run's time limit, the profile's backend filter) and kept
// here so the vocabulary lives in one place.
type Reason string

// The reasons.
const (
	ReasonRefreshDays Reason = "refresh_days"
	ReasonRefreshDue  Reason = "refresh_due"
	ReasonBudget      Reason = "budget"
	ReasonDisk        Reason = "disk"
	ReasonUnresolved  Reason = "unresolved"
	ReasonTimeLimit   Reason = "time_limit"
	ReasonReadOnly    Reason = "read_only"
	ReasonBackend     Reason = "backend"
	ReasonCatalogPin  Reason = "catalog_pin"
)

// Source says where the bytes of a download or update come from.
type Source string

// The sources.
const (
	SourceNetwork   Source = "network"
	SourceLocalCopy Source = "local_copy"
)

// Tier is the survival tier of a row: 1 to 3, or "k" for the pinned class (kit and docs), which is
// planned first and never dropped for budget. It marshals as the JSON number or the string "k".
type Tier struct {
	N      int
	Pinned bool
}

// String is the human column text: "k" or the number.
func (t Tier) String() string {
	if t.Pinned {
		return "k"
	}
	return strconv.Itoa(t.N)
}

// MarshalJSON implements json.Marshaler.
func (t Tier) MarshalJSON() ([]byte, error) {
	if t.Pinned {
		return []byte(`"k"`), nil
	}
	return []byte(strconv.Itoa(t.N)), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Tier) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		if s != "k" {
			return fmt.Errorf("plan: tier %q is not \"k\" or a number", s)
		}
		*t = Tier{Pinned: true}
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("plan: tier is not \"k\" or a number: %w", err)
	}
	*t = Tier{N: n}
	return nil
}

// ItemPlan is one row of the plan. Pos is the 1-based position in the priority order (0 for an
// orphan, which has none). Sizes are bytes.
type ItemPlan struct {
	Pos  int    `json:"pos,omitempty"`
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Tier is nil for an orphan.
	Tier   *Tier  `json:"tier,omitempty"`
	Action Action `json:"action"`
	// FromVersion is the version present on this root; ToVersion the target (empty when unresolved).
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`
	// FinalBytes is what the row weighs in the steady state: the target size for a download or
	// update, the size on disk for a keep or an orphan, the target size (0 when unknown) for a skip.
	FinalBytes int64 `json:"final_bytes,omitempty"`
	// NeedBytes is what is still to be written: the target size less the bytes already in staging.
	NeedBytes int64  `json:"need_bytes,omitempty"`
	Source    Source `json:"source,omitempty"`
	// FromRoot is the root a local copy is taken from; After is set when that root is itself being
	// synced in this run, so the copy has to wait for it.
	FromRoot string `json:"from_root,omitempty"`
	After    string `json:"after,omitempty"`
	Reason   Reason `json:"reason,omitempty"`
	// AgeDays and RefreshDays explain refresh_days, refresh_due and the "refresh in N d" note: the
	// age of the present copy and the item's refresh_days.
	AgeDays     int `json:"age_days,omitempty"`
	RefreshDays int `json:"refresh_days,omitempty"`
	// PurgeBytes is how much eligible trash must be purged before this item can start.
	PurgeBytes int64 `json:"purge_bytes,omitempty"`
}

// Finding is a doctor code the plan raises (LAI-SYN-040, 041, 042, 051, LAI-CAT-004, LAI-ARK-011).
type Finding struct {
	Code     string          `json:"code"`
	Severity doctor.Severity `json:"severity"`
	// Items names the items the finding is about.
	Items []string `json:"items,omitempty"`
	// Bytes carries the size a finding is about (the orphans of LAI-SYN-051).
	Bytes int64 `json:"bytes,omitempty"`
}

// RootPlan is the plan of one root: the JSON of ark-sync.md section 4.4.4 plus a few additive keys that
// make the human text reproducible from the JSON alone (path, offline, not_for_node, findings,
// free_floor_bytes, trash_bytes).
type RootPlan struct {
	Root           string `json:"root"`
	Path           string `json:"path,omitempty"`
	Profile        string `json:"profile"`
	CatalogVersion string `json:"catalog_version"`
	Mode           string `json:"mode,omitempty"`
	// Offline is true when the plan was made without network access.
	Offline  bool `json:"offline,omitempty"`
	ReadOnly bool `json:"read_only,omitempty"`

	BudgetBytes  int64 `json:"budget_bytes"`
	PlannedBytes int64 `json:"planned_bytes"`
	FSFreeBytes  int64 `json:"fs_free_bytes"`
	MinFreeBytes int64 `json:"min_free_bytes"`
	// PeakBytes is the disk use at the worst moment of the run: what the content root holds now
	// (verified copies, trash, staging) plus every byte still to be written. Old versions stay in
	// .trash until the new one verifies, so nothing is subtracted.
	PeakBytes int64 `json:"peak_bytes"`
	// FreeFloorBytes is the lowest the free space gets during the run (after any trash purge).
	FreeFloorBytes int64 `json:"free_floor_bytes"`
	// TrashBytes is everything in .trash, eligible or not (the planner never purges it).
	TrashBytes int64 `json:"trash_bytes,omitempty"`

	Items []ItemPlan `json:"items"`
	// NotForNode lists the kit items left out because their codename or arch is not this node's.
	NotForNode []string  `json:"not_for_node,omitempty"`
	Findings   []Finding `json:"findings"`
}

// Totals are the figures of the "Downloads:" line of the human plan.
type Totals struct {
	// Items is the number of downloads and updates.
	Items int
	// NetworkBytes and LocalBytes split their NeedBytes by source.
	NetworkBytes, LocalBytes int64
}

// Totals sums the downloads and updates of the plan.
func (p RootPlan) Totals() Totals {
	var t Totals
	for _, it := range p.Items {
		if it.Action != ActionDownload && it.Action != ActionUpdate {
			continue
		}
		t.Items++
		if it.Source == SourceLocalCopy {
			t.LocalBytes += it.NeedBytes
		} else {
			t.NetworkBytes += it.NeedBytes
		}
	}
	return t
}

// Item returns the row for an item id.
func (p RootPlan) Item(id string) (ItemPlan, bool) {
	for _, it := range p.Items {
		if it.ID == id {
			return it, true
		}
	}
	return ItemPlan{}, false
}

// HasFinding reports whether the plan raises code.
func (p RootPlan) HasFinding(code string) bool {
	for _, f := range p.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// MarshalIndented is the canonical JSON of a plan: two-space indent and a trailing newline. The same
// input always gives the same bytes (the struct field order is fixed and there are no maps).
func (p RootPlan) MarshalIndented() ([]byte, error) {
	if p.Items == nil {
		p.Items = []ItemPlan{}
	}
	if p.Findings == nil {
		p.Findings = []Finding{}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// OnlyItems returns the plan with only the rows of the given item ids (the `items` filter of a dry
// run). Rows keep their positions. The budget, planned and peak figures are not recomputed: every rule
// was applied to the whole profile, and they keep describing that plan.
func (p RootPlan) OnlyItems(ids []string) RootPlan {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	rows := make([]ItemPlan, 0, len(ids))
	for _, it := range p.Items {
		if want[it.ID] {
			rows = append(rows, it)
		}
	}
	p.Items = rows
	return p
}
