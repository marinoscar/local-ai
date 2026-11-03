package ready

import (
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// maxProblems bounds problems[]: the cloud schema refuses more than 100.
const maxProblems = 100

// RootFacts are the facts of the content root the report is about.
type RootFacts struct {
	ID string
	// Kind is ark.KindInternal or ark.KindArkDrive; anything else is judged like the internal root,
	// the way the planner does.
	Kind ark.RootKind
	Path string
	// Profile is the profile id the root is judged against.
	Profile string
	// State is the root's state; ark.StateUnclean is red (LAI-ARK-004).
	State ark.RootState
	// TotalBytes and FreeBytes are the filesystem's size and the bytes a non-root process may still use.
	TotalBytes, FreeBytes int64
	// ReadOnly: nothing may be written under the root.
	ReadOnly bool
}

// RunInfo is the last sync, verify or import run that touched a root.
type RunInfo struct {
	RunID string
	// Trigger is the run's trigger (manual, scheduled, cloud, mode_online, install, verify, import).
	Trigger    string
	FinishedAt time.Time
	// State is the run's final state: done, failed, cancelled or skipped.
	State string
}

// JournalFact is what the journal knows about one (root, item) pair that the manifests do not.
type JournalFact struct {
	// State is the item's state in items.json.
	State journal.ItemState
	// Version is the version that state is about (the target of the last attempt).
	Version string
	// Code is the doctor code of the last failure.
	Code string
	// CheckedCurrentAt is the last time a run resolved the item and confirmed that the version on the
	// root is the one wanted (items.json extras, ExtraCheckedCurrentAt); zero when never.
	CheckedCurrentAt time.Time
}

// KitFinding is one finding of the kit verify report (rebuild-kit.md section 4.4): a doctor code
// (LAI-KIT-040 to 043) and, when it is about one item, its id.
type KitFinding struct {
	Code string
	Item string
}

// KitReport is the kit verify report reduced to what the report needs. Until the kit verifier
// exists (KIT-03) it is empty and the kit rows are judged by age alone.
type KitReport struct {
	Findings []KitFinding
}

// Input is everything Build reads. Build is a pure function of it: no clock, no filesystem, no
// network, no catalog lookup that is not in the value.
type Input struct {
	// Now is the only clock.
	Now time.Time
	// NodeID and AgentVersion go into generated_by.
	NodeID, AgentVersion string

	Root RootFacts
	// Plan is the planner's output for this root (plan.Plan over the input the gatherer made, offline,
	// so versions are the ones the catalog pins). It supplies the desired set in priority order, what
	// the planner would do with each item, the budget and the minimum free space.
	Plan plan.RootPlan
	// Catalog and Profile describe the desired set; Profile is nil when ProfileUnknown.
	Catalog *catalog.Catalog
	Profile *catalog.Profile
	// ProfileUnknown: the root's profile is not in the catalog (LAI-CAT-003). There is nothing to
	// judge the root against, so the report is red.
	ProfileUnknown bool
	// Failure is the doctor code of a root that could not be read at all (its filesystem would not
	// answer). Like ProfileUnknown it makes the report red with that one problem, and nothing is
	// claimed about the content.
	Failure string
	// CatalogVersion is the catalog in use ("" when none is loaded).
	CatalogVersion string

	// Present holds the verified, servable copies on this root by item id (ark.Index.Copies filtered
	// to the root: a copy on an unclean root that was not verified at serve time is not in it).
	Present map[string]manifest.Manifest
	// Journal holds items.json's record of each item on this root.
	Journal map[string]JournalFact

	// LastSync is the last run that touched the root, of any trigger and any final state; nil when
	// none is recorded. LastDoneAt is when the last run that ended `done` finished, zero when none is
	// recorded. The age rules use LastDoneAt: a run that was skipped for lack of internet did not
	// bring the root up to date and must not reset its age.
	LastSync   *RunInfo
	LastDoneAt time.Time
	// OfflineMostly: the node was not in online mode for most of the time since the last sync. The
	// zero value means online, which is the default when no mode history is recorded.
	OfflineMostly bool
	// ReverifyDays is node.yaml sync.reverify_days; DefaultReverifyDays when zero.
	ReverifyDays int
	// NewestVerifiedElsewhere is the newest verified_at on the other mounted roots, for the
	// clock check, which compares the clock with every root.
	NewestVerifiedElsewhere time.Time

	// Kit is the kit verify report; Docs the facts of the rendered manuals.
	Kit  KitReport
	Docs DocsFacts
	// Loadable says whether a catalog model can load on this node; nil counts every model as loadable.
	Loadable func(modelID string) bool
}

// ExtraCheckedCurrentAt is the key of the journal's per-item extras (journal.Item.Extras) under which
// a run records that it resolved the item and confirmed the present version is the one wanted, as an
// RFC 3339 string. The sync engine writes it; this package reads it.
const ExtraCheckedCurrentAt = "checked_current_at"

// problemEntry is a problem and the colour it contributes, so the list can be sorted worst first.
type problemEntry struct {
	Problem
	colour Colour
}

type builder struct {
	in         Input
	now        time.Time
	clockWrong bool
	internal   bool
	survivors  map[string]bool
	problems   []problemEntry
	overall    Colour
}

// Build computes the report of one root.
func Build(in Input) Report {
	b := &builder{in: in, now: in.Now.UTC(), internal: in.Root.Kind != ark.KindArkDrive, overall: Green, survivors: map[string]bool{}}
	if in.Profile != nil {
		for _, id := range in.Profile.Survival.Models {
			b.survivors[id] = true
		}
	}
	newest := newestVerified(in.Present)
	if in.NewestVerifiedElsewhere.After(newest) {
		newest = in.NewestVerifiedElsewhere
	}
	b.clockWrong = !newest.IsZero() && b.now.Before(newest)

	rep := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   stamp(b.now),
		GeneratedBy:   GeneratedBy{NodeID: in.NodeID, AgentVersion: in.AgentVersion},
		ClockWrong:    b.clockWrong,
		Items:         []Item{},
		Problems:      []Problem{},
	}
	rep.Root = b.rootBlock()

	if in.LastSync != nil {
		rep.LastSync = &LastSync{
			RunID: in.LastSync.RunID, Trigger: in.LastSync.Trigger, FinishedAt: stamp(in.LastSync.FinishedAt),
			State: in.LastSync.State, AgeDays: ageDays(b.now, in.LastSync.FinishedAt),
		}
	}
	rep.LastFullReverifyAt = b.lastFullReverify()

	blocked := in.ProfileUnknown || in.Failure != ""
	switch {
	case in.ProfileUnknown:
		b.add(CodeUnknownProfile, "", Red)
	case in.Failure != "":
		b.add(in.Failure, "", Red)
	default:
		rep.Items = b.items()
		for _, it := range rep.Items {
			rep.Counts.add(it.Status)
		}
	}
	b.rootRules()

	needs := NeedsFor(in.Catalog, in.Profile, in.Loadable)
	rep.SurvivalCapable = !blocked && needs.Met(Verified(rep))

	rep.Overall = b.overall
	rep.Problems = b.sortedProblems()
	return rep
}

func (b *builder) rootBlock() RootBlock {
	in := b.in
	rb := RootBlock{
		ID: in.Root.ID, Kind: string(in.Root.Kind), Path: in.Root.Path, Profile: in.Root.Profile,
		FSTotalBytes: in.Root.TotalBytes, FSFreeBytes: in.Root.FreeBytes,
		BudgetBytes: in.Plan.BudgetBytes, TrashBytes: in.Plan.TrashBytes, ReadOnly: in.Root.ReadOnly,
	}
	if in.CatalogVersion != "" {
		v := in.CatalogVersion
		rb.CatalogVersion = &v
	}
	for _, m := range in.Present {
		rb.UsedBytes += m.TotalBytes
	}
	return rb
}

// lastFullReverify is the oldest last_reverified_at of any copy on the root: the moment up to which
// everything on it has been re-hashed. It is null when the root holds nothing.
func (b *builder) lastFullReverify() *string {
	var oldest time.Time
	for _, m := range b.in.Present {
		if oldest.IsZero() || m.LastReverifiedAt.Before(oldest) {
			oldest = m.LastReverifiedAt
		}
	}
	if oldest.IsZero() {
		return nil
	}
	s := stamp(oldest)
	return &s
}

func newestVerified(present map[string]manifest.Manifest) time.Time {
	var newest time.Time
	for _, m := range present {
		if m.VerifiedAt.After(newest) {
			newest = m.VerifiedAt
		}
	}
	return newest
}

// add records a problem and folds its colour into overall. The severity is the code's own, except
// that the drive-age code is an error once it is red (the doctor table says "error after 90 days").
func (b *builder) add(code, item string, colour Colour) {
	sev := doctor.Warning
	if c, ok := doctor.Lookup(code); ok {
		sev = c.Severity
	}
	if code == CodeDriveStale && colour == Red {
		sev = doctor.Error
	}
	title, _, ok := doctor.Text(code, doctor.LangEN)
	if !ok {
		title = code
	}
	b.problems = append(b.problems, problemEntry{Problem: Problem{Code: code, Severity: sev, Item: item, Title: title}, colour: colour})
	b.overall = Worse(b.overall, colour)
}

// sortedProblems puts the worst first and keeps the order of discovery inside a colour, then caps the
// list at what the cloud accepts.
func (b *builder) sortedProblems() []Problem {
	sort.SliceStable(b.problems, func(i, j int) bool { return b.problems[i].colour.Rank() > b.problems[j].colour.Rank() })
	n := len(b.problems)
	if n > maxProblems {
		n = maxProblems
	}
	out := make([]Problem, n)
	for i := 0; i < n; i++ {
		out[i] = b.problems[i].Problem
	}
	return out
}

// itemMeta is what the catalog says about an item.
type itemMeta struct {
	kind     string
	tier     int
	survival bool
	closure  bool
}

func (b *builder) meta(id, fallbackKind string) itemMeta {
	m := itemMeta{kind: fallbackKind, tier: 2, survival: b.survivors[id]}
	cat := b.in.Catalog
	if cat == nil {
		return m
	}
	if mod, ok := cat.Model(id); ok {
		m.kind, m.tier = manifest.KindModel, tierOfModel(mod)
		return m
	}
	if c, ok := cat.ContentItem(id); ok {
		m.kind, m.tier = c.Kind, tierOfContent(c)
		m.closure = c.Source.Type == "apt_closure"
	}
	return m
}

// items builds the entries: the desired items in the planner's priority order, then the orphans
// sorted by id.
func (b *builder) items() []Item {
	var out []Item
	desired := map[string]bool{}
	for _, row := range b.in.Plan.Items {
		if row.Action == plan.ActionOrphan {
			continue
		}
		desired[row.ID] = true
		out = append(out, b.desired(row))
	}
	var orphans []string
	for id := range b.in.Present {
		if !desired[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		out = append(out, b.orphan(id, b.in.Present[id]))
	}
	return out
}

func (b *builder) orphan(id string, m manifest.Manifest) Item {
	meta := b.meta(id, m.ItemKind)
	it := Item{ID: id, Kind: meta.kind, Tier: meta.tier, Status: StatusOrphan}
	b.fillPresent(&it, m, JournalFact{})
	// An orphan is not wanted, so its age says nothing about readiness.
	it.AgeDays, it.Freshness = nil, ""
	return it
}

// fillPresent sets the fields that describe the copy on the root.
func (b *builder) fillPresent(it *Item, m manifest.Manifest, j JournalFact) {
	it.Version = m.Version
	it.VerifiedAt = stamp(m.VerifiedAt)
	it.LastReverifiedAt = stamp(m.LastReverifiedAt)
	if !j.CheckedCurrentAt.IsZero() {
		it.CheckedCurrentAt = stamp(j.CheckedCurrentAt)
	}
	ref := j.CheckedCurrentAt
	if ref.IsZero() {
		ref = m.VerifiedAt
	}
	age := ageDays(b.now, ref)
	it.AgeDays = &age
	bytes := m.TotalBytes
	it.Bytes = &bytes
	it.Trust = string(m.Trust)
}

// desired builds the entry of one item of the desired set and records what it contributes to the
// root's colour and problems.
func (b *builder) desired(row plan.ItemPlan) Item {
	meta := b.meta(row.ID, row.Kind)
	j := b.in.Journal[row.ID]
	pres, hasPres := b.in.Present[row.ID]
	pinnedClass := meta.kind == manifest.KindKit || meta.kind == manifest.KindDocs

	it := Item{ID: row.ID, Kind: meta.kind, Tier: meta.tier, Survival: meta.survival}
	it.Status, it.Reason, it.Code = statusOf(row, hasPres, pres, j, pinnedClass)
	if hasPres {
		b.fillPresent(&it, pres, j)
	} else if it.Status == StatusMissing || it.Status == StatusFailed || it.Status == StatusQuarantined {
		if row.FinalBytes > 0 {
			n := row.FinalBytes
			it.Bytes = &n
		}
	}

	// Freshness: the age column, or for the manuals the facts of the render.
	fresh := Green
	if hasPres {
		if meta.kind == manifest.KindDocs {
			var code string
			fresh, code = docsVerdict(pres.CatalogVersion, b.in.CatalogVersion, b.in.Docs)
			if code != "" && it.Code == "" {
				it.Code = code
			}
		} else {
			fresh = Freshness(meta.kind, meta.closure, *it.AgeDays)
		}
	}
	colour := StatusColour(it.Status, meta.tier, meta.survival, meta.kind)
	if hasPres {
		if meta.kind == manifest.KindDocs || !b.clockWrong {
			colour = Worse(colour, fresh)
		}
		if !b.clockWrong {
			it.Freshness = fresh
		}
		// An apt kit built from repositories whose signature was not checked is never green
		// (LAI-SYN-063): the manifest names them (source.unverified_repos).
		if len(pres.Source.StringList(manifest.SourceUnverifiedRepos)) > 0 {
			colour = Worse(colour, Yellow)
			if it.Code == "" {
				it.Code = CodeUnverifiedRepos
			}
		}
	}
	if colour != Green && it.Code != "" {
		b.add(it.Code, it.ID, colour)
	} else {
		b.overall = Worse(b.overall, colour)
	}
	return it
}

// statusOf decides the status of a desired item from what the planner would do with it, what is on
// the root and what the journal says, and the reason and doctor code that go with it.
//
//   - A copy is present and is the one wanted (the planner keeps it: same version, or nothing newer is
//     known, or a refresh is not due yet): verified.
//   - A copy is present but a newer one is planned and not installed (an update is pending, was skipped
//     for budget, disk or a read-only root, or failed): stale.
//   - No copy: quarantined or failed when the journal says so, skipped when the planner left it out
//     for budget or disk, else missing.
func statusOf(row plan.ItemPlan, hasPres bool, pres manifest.Manifest, j JournalFact, pinnedClass bool) (Status, string, string) {
	failed := j.State == journal.StateFailed || j.State == journal.StateQuarantined
	budgetCode, diskCode := CodeBudgetFull, CodeLowFree
	if pinnedClass {
		budgetCode, diskCode = CodeKitDoesNotFit, CodeKitDoesNotFit
	}

	if hasPres {
		newerFailed := failed && j.Version != "" && j.Version != pres.Version
		keepsCurrent := row.Action == plan.ActionKeep &&
			(row.Reason == "" || row.Reason == plan.ReasonRefreshDays || row.Reason == plan.ReasonUnresolved)
		switch {
		case newerFailed && j.State == journal.StateQuarantined:
			return StatusStale, ReasonChecksum, j.Code
		case newerFailed:
			return StatusStale, ReasonUpdateFailed, j.Code
		case keepsCurrent:
			return StatusVerified, "", ""
		case row.Reason == plan.ReasonBudget:
			return StatusStale, ReasonBudget, budgetCode
		case row.Reason == plan.ReasonDisk:
			return StatusStale, ReasonDisk, diskCode
		case row.Reason == plan.ReasonReadOnly:
			return StatusStale, ReasonReadOnly, ""
		}
		return StatusStale, ReasonUpdatePending, ""
	}

	switch {
	case j.State == journal.StateQuarantined:
		return StatusQuarantined, ReasonChecksum, j.Code
	case j.State == journal.StateFailed:
		return StatusFailed, ReasonDownloadFailed, j.Code
	case row.Action == plan.ActionSkip && row.Reason == plan.ReasonBudget:
		return StatusSkipped, ReasonBudget, budgetCode
	case row.Action == plan.ActionSkip && row.Reason == plan.ReasonDisk:
		return StatusSkipped, ReasonDisk, diskCode
	case row.Action == plan.ActionSkip && row.Reason == plan.ReasonTimeLimit:
		return StatusSkipped, string(plan.ReasonTimeLimit), ""
	case row.Action == plan.ActionSkip && row.Reason == plan.ReasonReadOnly:
		return StatusMissing, ReasonReadOnly, ""
	}
	return StatusMissing, ReasonNotDownloaded, ""
}

// rootRules applies the rules that are about the root as a whole (the last table of 4.12.2).
func (b *builder) rootRules() {
	in := b.in

	// An unclean filesystem is red whatever its content looks like.
	if in.Root.State == ark.StateUnclean {
		b.add(CodeUnclean, "", Red)
	}

	// Age rules. They compare the clock with dates, so a clock known to be wrong switches them off.
	if !b.clockWrong {
		b.syncAgeRule()
		b.reverifyRule()
	}

	// Free space is not about time and applies regardless.
	if in.Plan.MinFreeBytes > 0 && in.Root.FreeBytes < in.Plan.MinFreeBytes {
		b.add(CodeLowFree, "", Yellow)
	}

	// The kit verify report.
	seen := map[string]bool{}
	for _, f := range in.Kit.Findings {
		col, ok := KitColour(f.Code, b.internal)
		if !ok {
			continue
		}
		key := f.Code + "/" + f.Item
		if seen[key] {
			continue
		}
		seen[key] = true
		b.add(f.Code, f.Item, col)
	}
}

// syncReference is when the root was last brought up to date: the last run that ended done; failing
// that the newest evidence on the root itself (a verified_at or a checked_current_at), so a node with
// no run record is judged by what is on its disk; zero when the root holds nothing.
func (b *builder) syncReference() time.Time {
	in := b.in
	if !in.LastDoneAt.IsZero() {
		return in.LastDoneAt
	}
	if in.LastSync != nil && in.LastSync.State == "done" && !in.LastSync.FinishedAt.IsZero() {
		return in.LastSync.FinishedAt
	}
	ref := newestVerified(in.Present)
	for _, j := range in.Journal {
		if j.CheckedCurrentAt.After(ref) {
			ref = j.CheckedCurrentAt
		}
	}
	return ref
}

func (b *builder) syncAgeRule() {
	ref := b.syncReference()
	if ref.IsZero() {
		return
	}
	age := ageDays(b.now, ref)
	if !b.internal {
		if col, ok := DriveAgeColour(age); ok {
			b.add(CodeDriveStale, "", col)
		}
		return
	}
	if age > InternalStaleDays && !b.in.OfflineMostly {
		b.add(CodeNoRecentSync, "", Yellow)
	}
}

// reverifyRule is "any item not re-verified in 2 x reverify_days": one problem, naming the item that
// is the most overdue.
func (b *builder) reverifyRule() {
	days := b.in.ReverifyDays
	if days <= 0 {
		days = DefaultReverifyDays
	}
	limit := ReverifyFactor * days
	worstAge, worstID := -1, ""
	for id, m := range b.in.Present {
		age := ageDays(b.now, m.LastReverifiedAt)
		if age > limit && (age > worstAge || (age == worstAge && id < worstID)) {
			worstAge, worstID = age, id
		}
	}
	if worstID != "" {
		b.add(CodeNotRechecked, worstID, Yellow)
	}
}
