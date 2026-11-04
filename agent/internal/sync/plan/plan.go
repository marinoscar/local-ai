// Package plan is the ark-sync planner (docs/specs/ark-sync.md section 4.4): given a content root,
// the profile, what is on disk and what the sources offer, it says per item what to keep, download,
// update, skip or flag as an orphan, in a stable priority order, with a reason for everything it does
// not do. It is also what the dry run shows a human before hours of downloads.
//
// plan.go (with types.go) is a pure function: Plan reads only its PlanInput. It reads no clock (the
// caller passes Now), touches no filesystem and opens no socket, so every rule is table-testable.
// gather.go is the only place that does I/O, behind small interfaces; render.go prints a plan.
//
// Two promises that shape the code. Sync never deletes the last verified copy of anything, so the
// planner has no delete action at all: an item that left the profile becomes an orphan row, kept
// until a person prunes it. And the rebuild kit and the manuals (kit and docs items) are planned
// first and are never dropped for budget, because they are what makes a rebuild with no internet
// possible; when they do not fit the plan says so (LAI-SYN-042) instead of quietly dropping them.
package plan

import (
	"regexp"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The doctor codes a plan raises. They alias the doctor table, the one place a code is spelled.
const (
	// CodeDiskFull: an item cannot start because the free space would fall below the minimum.
	CodeDiskFull = doctor.SYN040
	// CodeBudgetFull: items were skipped because the budget is full (error when a survival model is).
	CodeBudgetFull = doctor.SYN041
	// CodeKitDoesNotFit: the rebuild kit or the manuals do not fit.
	CodeKitDoesNotFit = doctor.SYN042
	// CodeOrphans: content that left the profile uses more than 5 % of the budget.
	CodeOrphans = doctor.SYN051
	// CodeUnknownID: an id of the profile or the overrides is not in the catalog; it was dropped.
	CodeUnknownID = doctor.CAT004
	// CodeDriveTooSmall: the drive cannot hold everything; lower tiers were skipped.
	CodeDriveTooSmall = doctor.ARK011
)

const (
	day = 24 * time.Hour
	// orphanPercent is the share of the budget above which orphans are reported (strictly more).
	orphanPercent = 5
	// driveMinFreeBytes is the floor of a drive's min_free (2 % of the filesystem, at least 5 GB).
	driveMinFreeBytes = 5_000_000_000
	// defaultTier is the survival tier of an item that declares none (ark-sync.md section 4.4.2).
	defaultTier = 2
)

// CatalogItem is one desired item as the planner sees it: a models.yaml entry or an ark.yaml entry.
type CatalogItem struct {
	ID string
	// Kind is manifest.KindModel for a model and the ark.yaml kind otherwise.
	Kind string
	// Engine is the model engine; empty for content.
	Engine  string
	Model   *catalog.Model
	Content *catalog.ContentItem
}

// ModelItem wraps a catalog model.
func ModelItem(m *catalog.Model) CatalogItem {
	return CatalogItem{ID: m.ID, Kind: manifest.KindModel, Engine: m.Engine, Model: m}
}

// ContentItem wraps a catalog content entry.
func ContentItem(c *catalog.ContentItem) CatalogItem {
	return CatalogItem{ID: c.ID, Kind: c.Kind, Content: c}
}

// ItemDir is the item directory of the item relative to the content root, slash separated
// (ark-sync.md section 3.1: models/<id>, kiwix/<id>, maps/<id>, docs, kit/<subdir>), the one place the
// answer comes from (ark.ItemDir; kit.LayoutOf for the subdir of a kit item). It is empty when the
// layout has no place for the item (a kit id that rebuild-kit.md section 3.1 does not know): the plan
// still lists the item, and the downloader refuses to write it.
func (c CatalogItem) ItemDir() string { return itemDir(c) }

func itemDir(c CatalogItem) string {
	ref := ark.ItemRef{Kind: c.Kind, ID: c.ID}
	switch {
	case c.Model != nil:
		ref.Kind, ref.Engine = manifest.KindModel, c.Model.Engine
	case c.Content != nil && c.Kind == manifest.KindKit:
		r, err := kit.ItemRef(*c.Content)
		if err != nil {
			return ""
		}
		ref = r
	}
	dir, err := ark.ItemDir(ref)
	if err != nil {
		return ""
	}
	return dir
}

// IsPinnedClass reports whether the item is of the pinned class: kit and docs, always planned first
// and never dropped for budget.
func (c CatalogItem) IsPinnedClass() bool {
	return c.Kind == manifest.KindKit || c.Kind == manifest.KindDocs
}

// SourceType is the catalog source type ("huggingface", "kiwix", "url", ...), the key resolvers are
// registered under.
func (c CatalogItem) SourceType() string {
	if c.Model != nil {
		return c.Model.Source.Type
	}
	if c.Content != nil {
		return c.Content.Source.Type
	}
	return ""
}

// RefreshDays is the item's refresh_days (0 for a model or an item without one).
func (c CatalogItem) RefreshDays() int {
	if c.Content != nil {
		return c.Content.RefreshDays
	}
	return 0
}

// CatalogBytes is the size the catalog states: the sum of a model's file sizes, or a content item's
// expected_size_bytes. 0 means the catalog does not know.
func (c CatalogItem) CatalogBytes() int64 {
	if c.Model != nil {
		return catalog.ModelSizeBytes(c.Model)
	}
	if c.Content != nil {
		return c.Content.ExpectedSizeBytes
	}
	return 0
}

// Pinned reports whether a change of the catalog is always applied to a present copy: a model (its
// revision is pinned), or a content item whose source names its version in the catalog (a pinned
// kiwix file, a url that is not "latest", a release tag, a git mirror, the rendered docs). The items
// that follow "latest" (kiwix latest, the planet extract, the apt closure) change on their own and
// are refreshed by refresh_days instead (ark-sync.md section 4.4.3).
func (c CatalogItem) Pinned() bool {
	if c.Model != nil {
		return true
	}
	if c.Content == nil {
		return false
	}
	src := c.Content.Source
	switch src.Type {
	case "kiwix":
		return src.VersionPolicy == "pinned"
	case "url":
		return src.VersionPolicy != "latest"
	case "pmtiles_extract", "apt_closure":
		return false
	}
	return true
}

// UnknownRef is an id of the profile or the overrides that the catalog does not have.
type UnknownRef struct {
	// Field is where it was written: include.models, include.content, add_models or add_content.
	Field string
	ID    string
}

// Desired is the desired set of a root before sizing.
type Desired struct {
	// Items are the desired items in list order (models, then content), unknown ids dropped and kit
	// items that are not for this node filtered out.
	Items []CatalogItem
	// Unknown are the ids dropped with LAI-CAT-004.
	Unknown []UnknownRef
	// NotForNode are the ids of the kit items filtered out for this node, in list order.
	NotForNode []string
}

// DesiredItems computes the desired set (ark-sync.md section 4.4.1): the profile's models then its
// content, plus the overrides' additions and minus their removals (first-seen order, no duplicates, a
// removal wins over an addition), unknown ids dropped, and, on the internal root, the kit items that
// are specific to another codename or arch left out. It is pure and exported because the gatherer
// resolves exactly this set.
func DesiredItems(cat *catalog.Catalog, prof *catalog.Profile, ov *ProfileOverrides, root RootInfo, node NodeInfo) Desired {
	var d Desired
	if cat == nil || prof == nil {
		return d
	}
	var o ProfileOverrides
	if ov != nil {
		o = *ov
	}
	type listed struct {
		field string
		id    string
		model bool
	}
	var order []listed
	seen := map[string]bool{}
	add := func(field string, ids, remove []string, model bool) {
		removed := map[string]bool{}
		for _, id := range remove {
			removed[id] = true
		}
		for _, id := range ids {
			if removed[id] || seen[id] {
				continue
			}
			seen[id] = true
			order = append(order, listed{field, id, model})
		}
	}
	// Models first (include, then add), then content, so the list position is the combined order.
	add("include.models", prof.Include.Models, o.RemoveModels, true)
	add("add_models", o.AddModels, o.RemoveModels, true)
	add("include.content", prof.Include.Content, o.RemoveContent, false)
	add("add_content", o.AddContent, o.RemoveContent, false)

	for _, l := range order {
		var it CatalogItem
		if l.model {
			m, ok := cat.Model(l.id)
			if !ok {
				d.Unknown = append(d.Unknown, UnknownRef{Field: l.field, ID: l.id})
				continue
			}
			it = ModelItem(m)
		} else {
			c, ok := cat.ContentItem(l.id)
			if !ok {
				d.Unknown = append(d.Unknown, UnknownRef{Field: l.field, ID: l.id})
				continue
			}
			it = ContentItem(c)
		}
		if root.Kind != ark.KindArkDrive && kitNotForNode(it, node) {
			d.NotForNode = append(d.NotForNode, it.ID)
			continue
		}
		d.Items = append(d.Items, it)
	}
	return d
}

// kitNotForNode reports whether a kit item is specific to a codename or arch that is not the node's
// (kit-debs-<codename>-<arch> and kit-nvidia-...: apt closures carry both). An unknown node value
// filters nothing: the planner keeps what it cannot judge.
func kitNotForNode(it CatalogItem, node NodeInfo) bool {
	if it.Content == nil || it.Kind != manifest.KindKit || it.Content.Source.Type != "apt_closure" {
		return false
	}
	src := it.Content.Source
	if node.Codename != "" && src.Codename != "" && src.Codename != node.Codename {
		return true
	}
	return node.Arch != "" && src.Arch != "" && src.Arch != node.Arch
}

var zimSuffix = regexp.MustCompile(`_(\d{4}-\d{2})\.zim$`)

// FromCatalog derives the target version and size from what the catalog pins, for the items whose
// version needs no network: a model's revision (its first 12 characters), a pinned kiwix file, a
// pinned url (the first 12 characters of the sha256), a release tag, and the rendered docs
// ("catalog-<catalog version>"). It reports false for the items that follow "latest" or a git mirror,
// which only a resolver can answer. The gatherer uses it for items no resolver answered, so an offline
// node still knows what it should have.
func FromCatalog(it CatalogItem, catalogVersion string) (Resolved, bool) {
	size := it.CatalogBytes()
	switch {
	case it.Model != nil:
		src := it.Model.Source
		if src.Type == "huggingface" && src.Revision != "" {
			return Resolved{Version: short(src.Revision, 12), SizeBytes: size}, true
		}
		if len(it.Model.Files) > 0 && it.Model.Files[0].SHA256 != "" {
			return Resolved{Version: short(it.Model.Files[0].SHA256, 12), SizeBytes: size}, true
		}
	case it.Content != nil:
		src := it.Content.Source
		switch src.Type {
		case "kiwix":
			if src.VersionPolicy == "pinned" && src.Pinned != nil {
				if m := zimSuffix.FindStringSubmatch(src.Pinned.File); m != nil {
					return Resolved{Version: m[1], SortKey: m[1], SizeBytes: size}, true
				}
				if src.Pinned.SHA256 != "" {
					return Resolved{Version: short(src.Pinned.SHA256, 12), SizeBytes: size}, true
				}
			}
		case "url":
			if src.VersionPolicy == "latest" {
				return Resolved{}, false
			}
			sha := src.SHA256
			if sha == "" && len(src.Files) > 0 {
				sha = src.Files[0].SHA256
			}
			if sha != "" {
				return Resolved{Version: short(sha, 12), SizeBytes: size}, true
			}
		case "github_release":
			if src.Tag != "" {
				return Resolved{Version: src.Tag, SizeBytes: size}, true
			}
		case "repo_docs":
			if catalogVersion != "" {
				return Resolved{Version: "catalog-" + catalogVersion, SortKey: catalogVersion, SizeBytes: size}, true
			}
		}
	}
	return Resolved{}, false
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// limits returns the budget (the steady-state limit) and min_free (the transient one) of the root.
// The internal root takes both from its profile, the budget replaced by a positive override. A drive
// has min_free of 2 % of its size (at least 5 GB) and its budget is the whole filesystem less that
// (ark-sync.md section 4.8).
func limits(in PlanInput) (budget, minFree int64) {
	if in.Root.Kind == ark.KindArkDrive {
		minFree = in.Root.TotalBytes / 100 * 2
		if minFree < driveMinFreeBytes {
			minFree = driveMinFreeBytes
		}
		budget = in.Root.TotalBytes - minFree
		if budget < 0 {
			budget = 0
		}
	} else if in.Profile != nil {
		budget, minFree = in.Profile.Storage.BudgetBytes, in.Profile.Storage.MinFreeBytes
	}
	if in.Overrides != nil && in.Overrides.BudgetBytes != nil && *in.Overrides.BudgetBytes > 0 {
		budget = *in.Overrides.BudgetBytes
	}
	return budget, minFree
}

// cand is a desired item with its sort key.
type cand struct {
	item CatalogItem
	// idx is the list position (the final tie-break).
	idx    int
	pinned bool
	tier   int
	// survival is 0 for a model of profile.survival.models, 1 for everything else.
	survival int
}

func rank(items []CatalogItem, prof *catalog.Profile) []cand {
	surv := map[string]bool{}
	if prof != nil {
		for _, id := range prof.Survival.Models {
			surv[id] = true
		}
	}
	cs := make([]cand, len(items))
	for i, it := range items {
		c := cand{item: it, idx: i, pinned: it.IsPinnedClass(), survival: 1}
		switch {
		case it.Model != nil:
			c.tier = it.Model.SurvivalTier
			if surv[it.ID] {
				c.survival = 0
			}
		case it.Content != nil:
			c.tier = it.Content.SurvivalTier
		}
		if c.tier == 0 {
			c.tier = defaultTier
		}
		cs[i] = c
	}
	sort.SliceStable(cs, func(a, b int) bool {
		x, y := cs[a], cs[b]
		if x.pinned != y.pinned {
			return x.pinned
		}
		if x.tier != y.tier {
			return x.tier < y.tier
		}
		if x.survival != y.survival {
			return x.survival < y.survival
		}
		return x.idx < y.idx
	})
	return cs
}

// finding builds a Finding with the code's default severity.
func finding(code string, items []string, bytes int64) Finding {
	sev := doctor.Warning
	if c, ok := doctor.Lookup(code); ok {
		sev = c.Severity
	}
	return Finding{Code: code, Severity: sev, Items: items, Bytes: bytes}
}

// run is the state of one planning pass.
type run struct {
	in          PlanInput
	offline     bool
	budget      int64
	minFree     int64
	used        int64        // steady-state bytes of the rows planned so far
	free        int64        // simulated free space after the downloads started so far
	floor       int64        // the lowest value free reached
	trash       []TrashEntry // eligible trash left, oldest first
	needTotal   int64
	budgetSkips []string // non-survival items skipped for budget
	survSkips   []string // survival models skipped for budget
	diskSkips   []string
	kitFails    []string // pinned-class items that did not fit
	survival    map[string]bool
}

// Plan computes the plan of one root. It is deterministic: the same input gives the same plan, byte
// for byte once rendered, and it never reads anything but in.
func Plan(in PlanInput) RootPlan {
	budget, minFree := limits(in)
	r := &run{
		in: in, budget: budget, minFree: minFree,
		offline:  in.Offline || in.Mode == ModeOffline || in.Mode == ModeSurvival,
		free:     in.Root.FreeBytes,
		floor:    -1,
		survival: map[string]bool{},
	}
	if in.Profile != nil {
		for _, id := range in.Profile.Survival.Models {
			r.survival[id] = true
		}
	}
	for _, t := range in.Trash {
		if t.Eligible {
			r.trash = append(r.trash, t)
		}
	}
	sort.SliceStable(r.trash, func(a, b int) bool {
		x, y := r.trash[a], r.trash[b]
		if !x.TrashedAt.Equal(y.TrashedAt) {
			return x.TrashedAt.Before(y.TrashedAt)
		}
		if x.ItemID != y.ItemID {
			return x.ItemID < y.ItemID
		}
		return x.Version < y.Version
	})

	out := RootPlan{
		Root: in.Root.ID, Path: in.Root.Path, Profile: in.Root.Profile,
		Mode: in.Mode, Offline: r.offline, ReadOnly: in.Root.ReadOnly,
		BudgetBytes: budget, FSFreeBytes: in.Root.FreeBytes, MinFreeBytes: minFree,
		Items: []ItemPlan{}, Findings: []Finding{},
	}
	if in.Profile != nil && out.Profile == "" {
		out.Profile = in.Profile.ID
	}
	if in.Catalog != nil {
		out.CatalogVersion = in.Catalog.Version
	}

	d := DesiredItems(in.Catalog, in.Profile, in.Overrides, in.Root, in.Node)
	out.NotForNode = d.NotForNode
	desired := map[string]bool{}
	for _, it := range d.Items {
		desired[it.ID] = true
	}

	for i, c := range rank(d.Items, in.Profile) {
		ip := r.decide(c)
		ip.Pos = i + 1
		out.Items = append(out.Items, ip)
	}

	// Orphans: verified copies that are not in the desired set. Kept, never deleted by sync.
	var orphans []string
	for id := range in.Present {
		if !desired[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)
	var orphanBytes int64
	for _, id := range orphans {
		m := in.Present[id]
		orphanBytes += m.TotalBytes
		out.Items = append(out.Items, ItemPlan{
			ID: id, Kind: m.ItemKind, Action: ActionOrphan, FromVersion: m.Version, FinalBytes: m.TotalBytes,
		})
	}

	out.PlannedBytes = r.used
	// The lowest free space the run reaches after an item starts; with no download it never moves.
	out.FreeFloorBytes = r.floor
	if r.floor < 0 {
		out.FreeFloorBytes = in.Root.FreeBytes
	}
	out.PeakBytes = r.footprint(in) + r.needTotal
	for _, t := range in.Trash {
		out.TrashBytes += t.Bytes
	}
	out.Findings = r.findings(d, orphans, orphanBytes)
	return out
}

// footprint is what the root holds now: verified copies (orphans included), the trash and the staged
// bytes of unfinished downloads.
func (r *run) footprint(in PlanInput) int64 {
	var n int64
	for _, m := range in.Present {
		n += m.TotalBytes
	}
	for _, t := range in.Trash {
		n += t.Bytes
	}
	for _, p := range in.Partial {
		n += p.bytes()
	}
	return n
}

// decide is the per-item decision of ark-sync.md section 4.4.3, in priority order.
func (r *run) decide(c cand) ItemPlan {
	in := r.in
	it := c.item
	tier := Tier{N: c.tier}
	if c.pinned {
		tier = Tier{Pinned: true}
	}
	ip := ItemPlan{ID: it.ID, Kind: it.Kind, Dir: itemDir(it), Tier: &tier, RefreshDays: it.RefreshDays()}

	pres, hasPres := in.Present[it.ID]
	var presBytes int64
	if hasPres {
		presBytes = pres.TotalBytes
		ip.FromVersion = pres.Version
		if !pres.DownloadedAt.IsZero() && in.Now.After(pres.DownloadedAt) {
			ip.AgeDays = int(in.Now.Sub(pres.DownloadedAt) / day)
		}
	}
	res, resolved := in.Resolved[it.ID]
	resolved = resolved && res.Version != ""
	if resolved {
		ip.ToVersion = res.Version
	}

	// final_bytes: the resolver's size, else the catalog's, else the present copy's.
	final := int64(0)
	switch {
	case resolved && res.SizeBytes > 0:
		final = res.SizeBytes
	case it.CatalogBytes() > 0:
		final = it.CatalogBytes()
	default:
		final = presBytes
	}

	keep := func(reason Reason) ItemPlan {
		ip.Action, ip.Reason, ip.FinalBytes = ActionKeep, reason, presBytes
		r.used += presBytes
		return ip
	}
	skip := func(reason Reason) ItemPlan {
		ip.Action, ip.Reason, ip.FinalBytes = ActionSkip, reason, final
		if hasPres {
			// The old copy stays and still weighs in the steady state.
			r.used += presBytes
		}
		if reason == ReasonBudget {
			if r.survival[it.ID] {
				r.survSkips = append(r.survSkips, it.ID)
			} else {
				r.budgetSkips = append(r.budgetSkips, it.ID)
			}
		}
		if reason == ReasonDisk {
			r.diskSkips = append(r.diskSkips, it.ID)
		}
		if c.pinned && (reason == ReasonBudget || reason == ReasonDisk) {
			r.kitFails = append(r.kitFails, it.ID)
		}
		return ip
	}

	// Same version as the target: nothing to do.
	if hasPres && resolved && pres.Version == res.Version {
		return keep("")
	}
	// A different version is present: apply it only when a refresh is due.
	var dueReason Reason
	if hasPres && resolved {
		isDue, why := r.refreshDue(it, pres)
		if !isDue {
			return keep(ReasonRefreshDays)
		}
		dueReason = why
	}
	// Unresolved: the target is unknown, so a present copy stays and an absent one cannot be planned.
	if !resolved {
		if hasPres {
			return keep(ReasonUnresolved)
		}
		if in.Root.ReadOnly {
			return skip(ReasonReadOnly)
		}
		if r.offline && !in.Import {
			return skip(ReasonNeedsNetwork)
		}
		return skip(ReasonUnresolved)
	}
	// Where the bytes would come from. Local copies work in every mode; the network does not offline.
	localFrom := ""
	for _, oc := range in.OtherRoots[it.ID] {
		if oc.RootID != in.Root.ID && oc.Manifest.Version == res.Version {
			localFrom = oc.RootID
			break
		}
	}
	localBuild := localFrom == "" && r.offline && in.LocalBuild[it.ID]
	if localFrom == "" && r.offline && !localBuild {
		if hasPres {
			return keep(ReasonUnresolved)
		}
		if in.Root.ReadOnly {
			return skip(ReasonReadOnly)
		}
		if in.Import {
			return skip(ReasonUnresolved)
		}
		return skip(ReasonNeedsNetwork)
	}
	if in.Root.ReadOnly {
		return skip(ReasonReadOnly)
	}
	// Budget: the steady state. The pinned class is never dropped for it.
	if !c.pinned && r.used+final > r.budget {
		if hasPres {
			return keep(ReasonBudget)
		}
		return skip(ReasonBudget)
	}
	// Free space: the transient state. Eligible trash may be purged, oldest first, to make room.
	need := final
	if p, ok := in.Partial[it.ID]; ok && p.Version == res.Version {
		need -= p.bytes()
	}
	if hasPres && pres.Version == res.Version {
		need = 0
	}
	if need < 0 {
		need = 0
	}
	purged, ok := r.makeRoom(need)
	if !ok {
		return skip(ReasonDisk)
	}
	r.free += purged - need
	if r.floor < 0 || r.free < r.floor {
		r.floor = r.free
	}
	r.needTotal += need
	if c.pinned && r.used+final > r.budget {
		r.kitFails = append(r.kitFails, it.ID)
	}
	r.used += final

	ip.NeedBytes, ip.FinalBytes, ip.PurgeBytes = need, final, purged
	ip.Reason = dueReason
	if hasPres {
		ip.Action = ActionUpdate
	} else {
		ip.Action = ActionDownload
	}
	switch {
	case localFrom != "":
		ip.Source, ip.FromRoot = SourceLocalCopy, localFrom
	case localBuild:
		ip.Source = SourceLocalBuild
	default:
		ip.Source = SourceNetwork
	}
	return ip
}

// refreshDue implements "refresh due" (section 4.4.3): the target differs from the present version
// and either the item is a model or pinned (a catalog change is a deliberate decision, reason
// catalog_pin) or it has refresh_days and the present copy is older than that (reason refresh_due).
func (r *run) refreshDue(it CatalogItem, pres manifest.Manifest) (bool, Reason) {
	if it.Pinned() {
		return true, ReasonCatalogPin
	}
	if days := it.RefreshDays(); days > 0 {
		if pres.DownloadedAt.IsZero() || r.in.Now.Sub(pres.DownloadedAt) > time.Duration(days)*day {
			return true, ReasonRefreshDue
		}
	}
	return false, ""
}

// makeRoom checks fs_free - need >= min_free (section 4.8). When it does not hold, it takes eligible
// trash, oldest first, until it does; it returns the bytes taken and whether the item can start. On
// failure nothing is consumed.
func (r *run) makeRoom(need int64) (purged int64, ok bool) {
	if r.free-need >= r.minFree {
		return 0, true
	}
	i := 0
	free := r.free
	for ; i < len(r.trash) && free-need < r.minFree; i++ {
		free += r.trash[i].Bytes
		purged += r.trash[i].Bytes
	}
	if free-need < r.minFree {
		return 0, false
	}
	r.trash = r.trash[i:]
	return purged, true
}

// findings lists the doctor codes the plan raises, in a fixed order.
func (r *run) findings(d Desired, orphans []string, orphanBytes int64) []Finding {
	out := []Finding{}
	if len(d.Unknown) > 0 {
		ids := make([]string, len(d.Unknown))
		for i, u := range d.Unknown {
			ids[i] = u.ID
		}
		out = append(out, finding(CodeUnknownID, ids, 0))
	}
	if len(r.kitFails) > 0 {
		out = append(out, finding(CodeKitDoesNotFit, r.kitFails, 0))
	}
	drive := r.in.Root.Kind == ark.KindArkDrive
	if len(r.survSkips) > 0 {
		f := finding(CodeBudgetFull, r.survSkips, 0)
		f.Severity = doctor.Error
		out = append(out, f)
	}
	if len(r.budgetSkips) > 0 && !drive {
		out = append(out, finding(CodeBudgetFull, r.budgetSkips, 0))
	}
	if len(r.diskSkips) > 0 {
		out = append(out, finding(CodeDiskFull, r.diskSkips, 0))
	}
	if r.budget > 0 && orphanBytes*100 > r.budget*orphanPercent {
		out = append(out, finding(CodeOrphans, orphans, orphanBytes))
	}
	if drive && (len(r.budgetSkips) > 0 || len(r.survSkips) > 0) {
		out = append(out, finding(CodeDriveTooSmall, append(append([]string{}, r.survSkips...), r.budgetSkips...), 0))
	}
	return out
}

// PlanAll plans several roots of one run and sequences the downloads between them: when the same
// version of an item is to be fetched from the network into more than one root, only the first root
// (in the order given) downloads it; every later root copies it from that one, after it has finished
// (source local_copy, from_root and after naming the first root). Nothing is downloaded twice, and the
// plans stay valid when read one root at a time.
func PlanAll(inputs []PlanInput) []RootPlan {
	plans := make([]RootPlan, len(inputs))
	for i, in := range inputs {
		plans[i] = Plan(in)
	}
	type fetch struct{ root, version string }
	first := map[string]fetch{}
	for i := range plans {
		p := &plans[i]
		for j := range p.Items {
			it := &p.Items[j]
			if (it.Action != ActionDownload && it.Action != ActionUpdate) || it.Source != SourceNetwork {
				continue
			}
			f, ok := first[it.ID]
			if !ok {
				first[it.ID] = fetch{root: p.Root, version: it.ToVersion}
				continue
			}
			if f.version == it.ToVersion {
				it.Source, it.FromRoot, it.After = SourceLocalCopy, f.root, f.root
			}
		}
	}
	return plans
}
