package checks

import (
	"context"
	"sort"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/verify"
)

// Doctor codes of the file checks. They are the doctor table's constants; the one file that spells a
// code is internal/doctor/codes.go.
const (
	// CodeContentMissing: essential (tier-1) content is not on the node (LAI-DRL-011).
	CodeContentMissing = doctor.DRL011
	// CodeFilesDamaged: a stored file no longer matches its checksum, or the pass could not run
	// (LAI-DRL-012).
	CodeFilesDamaged = doctor.DRL012
)

// ReadyReports is the part of *ready.Service the file checks read: the readiness report of one root,
// which names every item the profile wants with its survival tier and its status.
type ReadyReports interface {
	Report(rootID string) (ready.Report, bool)
}

// readable reports whether a root can be read: it has a path and is ready, read-only or unclean (an
// unclean drive is read and verified, never written). A root that is not mounted, is ejecting or is
// unusable is left out, so a drive that is not plugged in contributes nothing and fails nothing.
func readable(r ark.Root) bool {
	if r.Path == "" {
		return false
	}
	switch r.State {
	case "", ark.StateReady, ark.StateReadOnly, ark.StateUnclean:
		return true
	}
	return false
}

func readableRoots(all []ark.Root) []ark.Root {
	var out []ark.Root
	for _, r := range all {
		if readable(r) {
			out = append(out, r)
		}
	}
	return out
}

// ---- check 3: drl.files.manifest ---------------------------------------------------------------------

// ManifestDeps are what check 3 needs.
type ManifestDeps struct {
	// Roots lists the mounted content roots (the internal root and any ark drive).
	Roots func() []ark.Root
	// FS is the filesystem the manifests are read through (ark.OSFS{} in the daemon).
	FS config.FS
	// Scan lists the items of a root with their manifests; nil means ark.ScanRoot. ScanRoot reads
	// every MANIFEST.json, validates it, and requires every listed file to exist with the recorded
	// size: an item that fails that is returned as a problem, not as an item.
	Scan func(ctx context.Context, fsys config.FS, root ark.Root) ([]ark.ScannedItem, []ark.Problem, error)
	// Ready is the readiness report per root: which items the profile wants, and which are tier 1.
	Ready ReadyReports
}

// rootFindings are the facts check 3 gathers about one root.
type rootFindings struct {
	ID       string
	Items    int
	Invalid  int
	Stale    []string
	Orphans  []string
	Absent   []string // wanted by the profile, not usable on the root, not tier 1
	Tier1    []string // wanted by the profile, tier 1, not usable on the root
	NoReport bool
}

// NewManifestCheck is check 3, drl.files.manifest (critical): for every item on every mounted root the
// MANIFEST is present and valid and the files are there with the right sizes. It reads manifests and
// stats files; it hashes nothing (check 4 does).
//
//	pass: every item wanted by the root's profile is present and current.
//	warn: a stale item, an orphan, a missing item that is not tier 1, or a damaged directory.
//	fail: a tier-1 item is missing, or the internal root cannot be read (LAI-DRL-011).
func NewManifestCheck(d ManifestDeps) drill.Check {
	return drill.Check{
		ID: "drl.files.manifest", Area: "files", Critical: true, Order: orderManifest,
		FailCode: CodeContentMissing,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			return runManifest(ctx, d)
		},
	}
}

func runManifest(ctx context.Context, d ManifestDeps) drill.Outcome {
	if d.Roots == nil {
		return contentSkip("not_available", nil)
	}
	scan := d.Scan
	if scan == nil {
		scan = ark.ScanRoot
	}
	roots := readableRoots(d.Roots())
	var finds []rootFindings
	internal, drives := false, 0
	for _, root := range roots {
		if root.Kind == ark.KindInternal {
			internal = true
		} else {
			drives++
		}
		items, problems, err := scan(ctx, d.FS, root)
		if err != nil {
			return contentFail(CodeContentMissing, map[string]any{"reason": "scan_failed", "root": root.ID, "error": reasonOf(err)})
		}
		f := rootFindings{ID: root.ID, Items: len(items), Invalid: len(problems)}
		present := map[string]bool{}
		for _, it := range items {
			present[it.Manifest.ItemID] = true
		}
		rep, ok := readyReport(d.Ready, root.ID)
		f.NoReport = !ok
		if ok {
			for _, it := range rep.Items {
				switch it.Status {
				case ready.StatusOrphan:
					f.Orphans = append(f.Orphans, it.ID)
					continue
				case ready.StatusStale:
					f.Stale = append(f.Stale, it.ID)
					if present[it.ID] {
						continue
					}
				case ready.StatusVerified:
					if present[it.ID] {
						continue
					}
				}
				// Wanted by the profile and not usable here: missing, skipped, failed, quarantined, or
				// "verified" by an old report whose files have gone since.
				if it.Tier == 1 {
					f.Tier1 = append(f.Tier1, it.ID)
				} else {
					f.Absent = append(f.Absent, it.ID)
				}
			}
		}
		sort.Strings(f.Tier1)
		sort.Strings(f.Absent)
		sort.Strings(f.Stale)
		sort.Strings(f.Orphans)
		finds = append(finds, f)
	}

	details := map[string]any{"roots": rootDetails(finds), "drive_roots": drives}
	if !internal {
		details["reason"] = "internal_root_unavailable"
		return contentFail(CodeContentMissing, details)
	}
	var tier1 []string
	warn := false
	for _, f := range finds {
		for _, id := range f.Tier1 {
			tier1 = append(tier1, f.ID+"/"+id)
		}
		if len(f.Stale)+len(f.Orphans)+len(f.Absent)+f.Invalid > 0 {
			warn = true
		}
	}
	switch {
	case len(tier1) > 0:
		details["missing_tier1"] = tier1
		return contentFail(CodeContentMissing, details)
	case warn:
		return contentWarn("", details)
	}
	return contentPass(details)
}

func readyReport(r ReadyReports, rootID string) (ready.Report, bool) {
	if r == nil {
		return ready.Report{}, false
	}
	return r.Report(rootID)
}

func rootDetails(fs []rootFindings) []map[string]any {
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		m := map[string]any{"id": f.ID, "items": f.Items}
		if f.Invalid > 0 {
			m["invalid_dirs"] = f.Invalid
		}
		if len(f.Tier1) > 0 {
			m["missing_tier1"] = f.Tier1
		}
		if len(f.Absent) > 0 {
			m["missing"] = f.Absent
		}
		if len(f.Stale) > 0 {
			m["stale"] = f.Stale
		}
		if len(f.Orphans) > 0 {
			m["orphans"] = f.Orphans
		}
		if f.NoReport {
			m["readiness"] = "unknown"
		}
		out = append(out, m)
	}
	return out
}

// ---- check 4: drl.files.hash ---------------------------------------------------------------------------

// HashVerifier is the part of *verify.Reverifier check 4 uses: ark-sync's own re-verification
// (ark-sync.md section 4.13). A copy that no longer matches its manifest is quarantined by that code,
// by its rules, and by nothing in the drill.
type HashVerifier interface {
	Candidates(ctx context.Context, o verify.Options) ([]verify.Candidate, error)
	Run(ctx context.Context, o verify.Options) (verify.Report, error)
}

// HashDeps are what check 4 needs.
type HashDeps struct {
	Verifier HashVerifier
	// Roots lists the mounted content roots; Ready names the items of the internal root.
	Roots func() []ark.Root
	Ready ReadyReports
	// ModelEngine returns the engine ("whisper", "piper", "llama") of a catalog model id and "" when it
	// is not one; whisper and piper models are always hashed.
	ModelEngine func(itemID string) string
	// NodeKit lists the catalog ids of the kit parts this node needs (rebuild-kit.md section 4.4); nil
	// means none.
	NodeKit func(ctx context.Context) []string
	// ReverifyDays is sync.reverify_days: the other items are hashed only when they are older than
	// this. Nil or non-positive means 30.
	ReverifyDays func() int
	// Budget is the time the pass may spend; zero means 90 minutes.
	Budget time.Duration
	// Now is the clock of the budget; time.Now when nil.
	Now func() time.Time
}

// MustHashItems is the set of item ids check 4 always hashes on the internal root: the survival models,
// the whisper and piper models, the tier-1 maps (the map, its place index) and the map assets, this
// node's kit parts, and the tier-1 ZIMs (drill.md section 4.2 check 4). Only an item the report says is
// present counts; one that is missing is check 3's finding, not a thing to hash.
func MustHashItems(rep ready.Report, engineOf func(itemID string) string, nodeKit []string) []string {
	kit := map[string]bool{}
	for _, id := range nodeKit {
		kit[id] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, it := range rep.Items {
		if it.Status != ready.StatusVerified && it.Status != ready.StatusStale {
			continue
		}
		engine := ""
		if engineOf != nil {
			engine = engineOf(it.ID)
		}
		switch {
		case it.Survival,
			engine == "whisper", engine == "piper",
			it.Kind == manifest.KindMaps && it.Tier == 1,
			it.ID == ark.MapAssetsID,
			kit[it.ID],
			it.Kind == manifest.KindKiwix && it.Tier == 1:
			add(it.ID)
		}
	}
	sort.Strings(out)
	return out
}

// NewHashCheck is check 4, drl.files.hash (critical, skipped by --quick): a full sha256 of the files
// that must be intact when the network is gone, through ark-sync's verifier.
//
//	pass: everything hashed matches its manifest.
//	warn: the 90-minute budget ran out; the items not reached are skipped, and listed.
//	fail: a copy no longer matches (LAI-DRL-012); ark-sync has already set it aside.
//	skip: no verifier is wired, or there was nothing to hash.
func NewHashCheck(d HashDeps) drill.Check {
	budget := d.Budget
	if budget <= 0 {
		budget = hashBudget
	}
	return drill.Check{
		ID: "drl.files.hash", Area: "files", Critical: true, Order: orderHash, SkipInQuick: true,
		Timeout: budget + (hashTimeout - hashBudget), FailCode: CodeFilesDamaged,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			return runHash(ctx, d, budget)
		},
	}
}

// maxListed bounds a list in a details map, so a damaged drive cannot make the document huge.
const maxListed = 50

type hashRun struct {
	outcomes []verify.Outcome
	left     []verify.Candidate
	stopped  bool
	done     map[string]bool // root + "\x00" + item
}

func (h *hashRun) add(rep verify.Report) {
	for _, o := range rep.Outcomes {
		h.outcomes = append(h.outcomes, o)
		if o.Root != "" {
			h.done[o.Root+"\x00"+o.Item] = true
		}
	}
	h.left = append(h.left, rep.Left...)
	h.stopped = h.stopped || rep.Stopped
}

func runHash(ctx context.Context, d HashDeps, budget time.Duration) drill.Outcome {
	if d.Verifier == nil || d.Roots == nil {
		return contentSkip("not_available", nil)
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	start := now()
	remaining := func() time.Duration { return budget - now().Sub(start) }
	days := 30
	if d.ReverifyDays != nil {
		if v := d.ReverifyDays(); v > 0 {
			days = v
		}
	}
	roots := readableRoots(d.Roots())
	var internal *ark.Root
	for i := range roots {
		if roots[i].Kind == ark.KindInternal {
			internal = &roots[i]
			break
		}
	}

	h := &hashRun{done: map[string]bool{}}
	fail := func(reason string, err error) drill.Outcome {
		return contentFail(CodeFilesDamaged, map[string]any{"reason": reason, "error": reasonOf(err)})
	}

	// Pass 1: what must be intact, on the internal root, whatever its age.
	if internal != nil {
		var nodeKit []string
		if d.NodeKit != nil {
			nodeKit = d.NodeKit(ctx)
		}
		if rep, ok := readyReport(d.Ready, internal.ID); ok {
			if must := MustHashItems(rep, d.ModelEngine, nodeKit); len(must) > 0 {
				r, err := d.Verifier.Run(ctx, verify.Options{Roots: []string{internal.ID}, Items: must, Budget: remaining()})
				if err != nil {
					return fail("verifier_error", err)
				}
				h.add(r)
			}
		}
	}

	// Pass 2: every other copy that is due (older than reverify_days), oldest first, until the budget
	// is spent. Copies that pass 1 just hashed are not read again.
	if !h.stopped {
		cands, err := d.Verifier.Candidates(ctx, verify.Options{DueDays: days})
		if err != nil {
			return fail("verifier_error", err)
		}
		byRoot := map[string][]string{}
		var order []string
		for _, c := range cands {
			if h.done[c.Root+"\x00"+c.Item] {
				continue
			}
			if _, ok := byRoot[c.Root]; !ok {
				order = append(order, c.Root)
			}
			byRoot[c.Root] = append(byRoot[c.Root], c.Item)
		}
		for i, rootID := range order {
			left := remaining()
			if left <= 0 {
				h.stopped = true
				for _, r := range order[i:] {
					for _, item := range byRoot[r] {
						h.left = append(h.left, verify.Candidate{Root: r, Item: item})
					}
				}
				break
			}
			r, err := d.Verifier.Run(ctx, verify.Options{Roots: []string{rootID}, Items: byRoot[rootID], Budget: left})
			if err != nil {
				return fail("verifier_error", err)
			}
			h.add(r)
			if h.stopped {
				for _, r := range order[i+1:] {
					for _, item := range byRoot[r] {
						h.left = append(h.left, verify.Candidate{Root: r, Item: item})
					}
				}
				break
			}
		}
	}

	var verified, bytes int64
	var damaged, skipped []map[string]any
	for _, o := range h.outcomes {
		bytes += o.Bytes
		switch o.Result {
		case verify.ResultVerified:
			verified++
		case verify.ResultQuarantined, verify.ResultFailed:
			m := map[string]any{"item": o.Item, "root": o.Root, "reason": o.Reason}
			if o.Restored != "" {
				m["restored_version"] = o.Restored
			}
			damaged = append(damaged, m)
		default:
			skipped = append(skipped, map[string]any{"item": o.Item, "root": o.Root, "reason": o.Reason})
		}
	}
	for _, c := range h.left {
		skipped = append(skipped, map[string]any{"item": c.Item, "root": c.Root, "reason": verify.ReasonTimeLimit})
	}
	details := map[string]any{"verified": verified, "bytes": bytes, "budget_minutes": int(budget / time.Minute)}
	if len(skipped) > 0 {
		details["skipped_count"] = len(skipped)
		details["skipped"] = clipList(skipped)
	}
	switch {
	case len(damaged) > 0:
		details["damaged"] = clipList(damaged)
		return contentFail(CodeFilesDamaged, details)
	case h.stopped:
		details["budget_exhausted"] = true
		return contentWarn(CodeFilesDamaged, details)
	case verified == 0:
		return contentSkip("nothing_to_hash", details)
	}
	return contentPass(details)
}

func clipList(l []map[string]any) []map[string]any {
	if len(l) > maxListed {
		return l[:maxListed]
	}
	return l
}
