package ready

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The colour rules of ark-sync.md section 4.12.2, as pure functions. Nothing here reads a clock, a
// file or the catalog: every function takes the facts it needs, so each row of each table is one test
// case.

// The doctor codes the rules raise. They alias the doctor table, the one place a code is spelled.
const (
	// CodeNoRecentSync: the internal root has had no successful sync for more than 14 days while online.
	CodeNoRecentSync = doctor.SYN004
	// CodeNotRechecked: a stored copy was not re-verified in twice reverify_days.
	CodeNotRechecked = doctor.SYN025
	// CodeLowFree: the root's free space is below its minimum.
	CodeLowFree = doctor.SYN040
	// CodeBudgetFull: the item was skipped because the budget is full.
	CodeBudgetFull = doctor.SYN041
	// CodeKitDoesNotFit: the rebuild kit or the manuals do not fit.
	CodeKitDoesNotFit = doctor.SYN042
	// CodeDocsPlaceholders: the manuals were made without the real lab values.
	CodeDocsPlaceholders = doctor.SYN062
	// CodeUnverifiedRepos: an apt kit item was built from vendor repositories whose signature was not
	// checked (sync.apt_unverified_repos: allow).
	CodeUnverifiedRepos = doctor.SYN063
	// CodeDocsNoCardPDF: the manuals were made without the survival-card PDF.
	CodeDocsNoCardPDF = doctor.CLI014
	// CodeDocsDamaged: the manuals' pages are missing or damaged.
	CodeDocsDamaged = doctor.SYN052
	// CodeDriveStale: the ark drive has not been updated for more than 30 days (red after 90).
	CodeDriveStale = doctor.ARK008
	// CodeUnclean: the ark drive's filesystem has errors.
	CodeUnclean = doctor.ARK004
	// CodeUnknownProfile: the root's profile is not in the catalog.
	CodeUnknownProfile = doctor.CAT003
	// CodeDriveUnreadable: a mounted drive could not be assessed (read/write errors).
	CodeDriveUnreadable = doctor.ARK014
	// CodeInternalError: a mounted internal root could not be assessed.
	CodeInternalError = doctor.AGT099
	// CodeKitMissingForNode, CodeKitMissingForLab, CodeKitWrongRelease and CodeKitCUDA are the
	// findings of the kit verify report (rebuild-kit.md section 4.4).
	CodeKitMissingForNode = doctor.KIT040
	CodeKitMissingForLab  = doctor.KIT041
	CodeKitWrongRelease   = doctor.KIT042
	CodeKitCUDA           = doctor.KIT043
)

// Thresholds of the tables, in days.
const (
	// ModelFreshDays and KiwixFreshDays: green up to this age; weights and ZIMs never go red by age.
	ModelFreshDays = 180
	KiwixFreshDays = 180
	// MapsFreshDays is the same for maps.
	MapsFreshDays = 365
	// ClosureFreshDays is for the apt closures of the kit (kit-debs-*, kit-nvidia-*).
	ClosureFreshDays = 60
	// KitFreshDays is for every other kit item.
	KitFreshDays = 90
	// UnknownKindFreshDays judges a kind the table does not name: the strictest of the others.
	UnknownKindFreshDays = 90

	// DriveYellowDays and DriveRedDays: a drive not synced for longer than this.
	DriveYellowDays = 30
	DriveRedDays    = 90
	// InternalStaleDays: the internal root without a successful sync for longer than this while online.
	InternalStaleDays = 14
	// ReverifyFactor: a copy not re-verified in this many times reverify_days is overdue.
	ReverifyFactor = 2
	// DefaultReverifyDays is node.yaml sync.reverify_days when the caller has no value.
	DefaultReverifyDays = 30
)

const day = 24 * time.Hour

// ageDays is whole days from ref to now, never negative (a clock behind ref gives 0).
func ageDays(now, ref time.Time) int {
	if ref.IsZero() || !now.After(ref) {
		return 0
	}
	return int(now.Sub(ref) / day)
}

// Freshness is the colour of an item's age (the first table of 4.12.2). kind is the item kind
// (manifest.Kind*), closure is true for an apt closure (kit-debs-*, kit-nvidia-*). Weights and ZIMs
// never go red by age; docs are judged by their facts, not their age (docsFreshness).
func Freshness(kind string, closure bool, age int) Colour {
	limit := UnknownKindFreshDays
	switch kind {
	case manifest.KindModel:
		limit = ModelFreshDays
	case manifest.KindKiwix:
		limit = KiwixFreshDays
	case manifest.KindMaps:
		limit = MapsFreshDays
	case manifest.KindKit:
		limit = KitFreshDays
		if closure {
			limit = ClosureFreshDays
		}
	}
	if age > limit {
		return Yellow
	}
	return Green
}

// DocsFacts are what the render of the manuals left behind, which the status of the docs item alone
// cannot say (the second half of the docs row). The zero value means "nothing is known".
type DocsFacts struct {
	// Known is true when the facts were read. Without them only the catalog version is judged.
	Known bool
	// HTMLMissing: the rendered pages are missing or the index is unreadable (red).
	HTMLMissing bool
	// Placeholders: rendered without the real lab values (LAI-SYN-062, yellow). The resolver that
	// renders the manuals (SYNC-11) knows it; the index file does not.
	Placeholders bool
	// CardPDFMissing: the survival-card PDF was not made (LAI-CLI-014, yellow).
	CardPDFMissing bool
}

// docsVerdict is the colour of a verified docs item and the doctor code that explains it: green when
// the catalog is current and the render is complete; yellow for an older catalog, placeholders or a
// missing card PDF; red for missing pages. The most serious cause is the code.
func docsVerdict(catalogOfCopy, currentCatalog string, f DocsFacts) (Colour, string) {
	if f.Known && f.HTMLMissing {
		return Red, CodeDocsDamaged
	}
	c, code := Green, ""
	raise := func(col Colour, k string) {
		if col.Rank() > c.Rank() {
			c, code = col, k
		}
	}
	if f.Known && f.Placeholders {
		raise(Yellow, CodeDocsPlaceholders)
	}
	if f.Known && f.CardPDFMissing {
		raise(Yellow, CodeDocsNoCardPDF)
	}
	if currentCatalog != "" && catalogOfCopy != currentCatalog {
		raise(Yellow, "")
	}
	return c, code
}

// tierClass says which column of the status table an item falls in.
type tierClass int

const (
	// classStrict: tier 1, a survival model, the kit and the manuals.
	classStrict tierClass = iota
	// classTier2 and classTier3 are the other two columns.
	classTier2
	classTier3
)

// classOf places an item in its column of the status table: survival models, kit and docs and tier 1
// are the strict column; otherwise the tier decides, and an unknown tier is judged as tier 2.
func classOf(tier int, survival bool, kind string) tierClass {
	switch {
	case survival, kind == manifest.KindKit, kind == manifest.KindDocs, tier == 1:
		return classStrict
	case tier >= 3:
		return classTier3
	}
	return classTier2
}

// StatusColour is the colour of an item's status (the second table of 4.12.2). A verified item is
// green here: its colour then comes from its freshness.
func StatusColour(s Status, tier int, survival bool, kind string) Colour {
	class := classOf(tier, survival, kind)
	switch s {
	case StatusVerified, StatusOrphan:
		return Green
	case StatusStale:
		if class == classTier3 {
			return Green
		}
		return Yellow
	case StatusMissing, StatusFailed, StatusQuarantined:
		if class == classStrict {
			return Red
		}
		return Yellow
	case StatusSkipped:
		switch class {
		case classStrict:
			return Red
		case classTier2:
			return Yellow
		}
		return Green
	}
	// An unknown status is never good news.
	return Red
}

// ItemColour is the colour an item contributes to overall: the worse of its status colour and its
// freshness (an empty freshness, as on a root whose clock is wrong or an item without a copy, adds
// nothing).
func ItemColour(it Item, survival bool) Colour {
	return Worse(StatusColour(it.Status, it.Tier, survival || it.Survival, it.Kind), it.Freshness)
}

// DriveAgeColour is the root rule of a drive: not synced for more than 30 days is yellow, more than
// 90 is red (LAI-ARK-008). ok is false when the age is within 30 days.
func DriveAgeColour(age int) (Colour, bool) {
	switch {
	case age > DriveRedDays:
		return Red, true
	case age > DriveYellowDays:
		return Yellow, true
	}
	return Green, false
}

// KitColour is the colour of one finding of the kit verify report on a root of the given kind
// (rebuild-kit.md section 4.4 and ark-sync.md 4.12.2): parts missing for this node are red on the
// internal root and yellow on a drive; parts missing for another pinned lab node are yellow; a CUDA
// toolkit that does not fit the driver is yellow; a kit for another Ubuntu release is red on the
// internal root and yellow on a drive, where closures of several releases are normal. ok is false for
// a code that is not a kit finding.
func KitColour(code string, internal bool) (Colour, bool) {
	switch code {
	case CodeKitMissingForNode, CodeKitWrongRelease:
		if internal {
			return Red, true
		}
		return Yellow, true
	case CodeKitMissingForLab, CodeKitCUDA:
		return Yellow, true
	}
	return Green, false
}

// ModeChange is one change of the node's mode: the mode it changed to, and when.
type ModeChange struct {
	At   time.Time
	Mode string
}

// MostlyOnline reports whether the node was in online mode for more than half of [from, to], given
// the changes of mode in time order (the mode before the first change is taken to be online). With no
// history it is true: a node that never recorded a change is assumed to have been online, so the
// no-recent-sync warning is not suppressed by missing data (ark-sync.md 4.12.2).
func MostlyOnline(changes []ModeChange, from, to time.Time) bool {
	if len(changes) == 0 || !to.After(from) {
		return true
	}
	var online time.Duration
	cur, at := "online", from
	for _, ch := range changes {
		if !ch.At.After(from) {
			cur = ch.Mode
			continue
		}
		if !ch.At.Before(to) {
			break
		}
		if cur == "online" {
			online += ch.At.Sub(at)
		}
		cur, at = ch.Mode, ch.At
	}
	if cur == "online" {
		online += to.Sub(at)
	}
	return online*2 > to.Sub(from)
}
