package ready

import (
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// The views are the small interfaces other parts of the agent read the ark through: /status
// (statusapi.ArkView), the catalog's content rows (statusapi.ContentView) and the cloud state
// snapshot (cloud.ArkSource). The Service implements all three from the reports it holds, so every
// consumer sees the same colours and none re-implements a rule.

var (
	_ statusapi.ArkView     = (*Service)(nil)
	_ statusapi.ContentView = (*Service)(nil)
	_ cloud.ArkSource       = CloudSource{}
)

// ReadyPercent is the share of the wanted items that are verified, in whole percent: verified over
// everything that is wanted (verified, stale, missing, skipped, failed, quarantined; orphans are not
// wanted). ok is false when nothing is wanted.
func (r Report) ReadyPercent() (pct int, ok bool) {
	c := r.Counts
	wanted := c.Verified + c.Stale + c.Missing + c.Skipped + c.Failed + c.Quarantined
	if wanted == 0 {
		return 0, false
	}
	return c.Verified * 100 / wanted, true
}

// statusKind is the kind of a root in status.ark.roots: internal or drive.
func statusKind(k string) string {
	if k == string(ark.KindArkDrive) {
		return "drive"
	}
	return "internal"
}

// Ark implements statusapi.ArkView.
func (s *Service) Ark() statusapi.ArkInfo {
	info := statusapi.ArkInfo{}
	if s.d.SyncState != nil {
		info.SyncState = s.d.SyncState()
	}
	s.mu.RLock()
	reports := make([]Report, 0, len(s.order))
	for _, id := range s.order {
		reports = append(reports, s.cache[id].report)
	}
	s.mu.RUnlock()

	now := s.d.Clock.Now()
	for _, r := range reports {
		row := statusapi.ArkRoot{Path: r.Root.Path, Kind: statusKind(r.Root.Kind), Status: string(r.Overall)}
		if pct, ok := r.ReadyPercent(); ok {
			row.ReadyPct = &pct
		}
		if t, err := time.Parse(time.RFC3339, r.GeneratedAt); err == nil {
			row.UpdatedAt = t
		}
		info.Roots = append(info.Roots, row)
		if r.Root.Kind == string(ark.KindArkDrive) {
			info.DriveMounted = true
		}
	}
	if s.d.Runs != nil {
		for _, r := range reports {
			if _, done := s.d.Runs.LastRuns(r.Root.ID); done.After(info.LastSuccessAt) {
				info.LastSuccessAt = done
			}
		}
	}
	for _, m := range MetricsOf(reports, now) {
		info.Metrics = append(info.Metrics, statusapi.ArkMetric{Name: m.Name, Labels: m.Labels, Value: m.Value})
	}
	return info
}

// bestRank orders item statuses for ContentState: the status that says the most about having the
// content wins when roots disagree.
var bestRank = map[Status]int{
	StatusVerified: 7, StatusStale: 6, StatusOrphan: 5, StatusFailed: 4, StatusQuarantined: 3, StatusSkipped: 2, StatusMissing: 1,
}

// ContentState implements statusapi.ContentView: what the mounted roots hold of one catalog item, from
// their reports. A verified copy, a stale copy or an orphan counts as present; only a verified one is
// verified. When roots disagree the best status wins; an item no report lists is absent (empty
// Status).
func (s *Service) ContentState(id string) statusapi.ContentState {
	var out statusapi.ContentState
	best := 0
	for _, r := range s.Reports() {
		it, ok := r.Item(id)
		if !ok {
			continue
		}
		if rank := bestRank[it.Status]; rank > best {
			best, out.Status = rank, string(it.Status)
		}
		switch it.Status {
		case StatusVerified:
			out.Present, out.Verified = true, true
		case StatusStale, StatusOrphan:
			out.Present = true
		}
	}
	return out
}

// Tiers maps the items of a report to the cloud snapshot's tier verdicts (ark-sync.md section 4.16):
// per tier, ok when every planned item is verified (and when none is planned, for t1 and t2), missing
// when none is verified (a stale copy does not count), partial otherwise; t3 is not_planned when the
// profile plans no tier-3 item.
func Tiers(r Report) (t1, t2, t3 string) {
	verdict := func(tier int) string {
		planned, verified := 0, 0
		for _, it := range r.Items {
			if it.Status == StatusOrphan || it.Tier != tier {
				continue
			}
			planned++
			if it.Status == StatusVerified {
				verified++
			}
		}
		switch {
		case planned == 0 && tier == 3:
			return "not_planned"
		case planned == 0, verified == planned:
			return "ok"
		case verified == 0:
			return "missing"
		}
		return "partial"
	}
	return verdict(1), verdict(2), verdict(3)
}

// CloudSource is the Service as a cloud.ArkSource. It is a type of its own because the status view and
// the cloud view both call their method Ark and differ in what it returns.
type CloudSource struct{ S *Service }

// CloudSource returns the cloud.ArkSource of the service.
func (s *Service) CloudSource() CloudSource { return CloudSource{S: s} }

// Ark implements cloud.ArkSource: one row per mounted root with its tiers, counts and bytes and, as
// ReadyJSON, the exact bytes of its READY.json for the cloud link to strip and send. The drive's last
// synced time is taken from the drives that are mounted now; the engine's own record, which survives
// unplugging, is the better source once it exists.
func (c CloudSource) Ark() cloud.ArkReport {
	s := c.S
	out := cloud.ArkReport{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		cr := s.cache[id]
		r := cr.report
		row := cloud.ArkRootReport{
			ID: r.Root.ID, Path: r.Root.Path, Kind: r.Root.Kind, Present: true,
			ItemsMissing: r.Counts.Missing, ItemsFailed: r.Counts.Failed,
			BytesUsed: r.Root.UsedBytes, BytesFree: r.Root.FSFreeBytes,
			Overall: string(r.Overall), SurvivalCapable: r.SurvivalCapable,
			ReadyJSON: append([]byte(nil), cr.raw...),
		}
		row.T1, row.T2, row.T3 = Tiers(r)
		for _, it := range r.Items {
			if it.Status == StatusSkipped && it.Reason == ReasonBudget {
				row.ItemsSkippedBudget++
			}
		}
		if r.LastSync != nil {
			if t, err := time.Parse(time.RFC3339, r.LastSync.FinishedAt); err == nil && r.LastSync.State == "done" {
				row.LastSyncAt = t
			}
		}
		if r.LastFullReverifyAt != nil {
			if t, err := time.Parse(time.RFC3339, *r.LastFullReverifyAt); err == nil {
				row.LastVerifyAt = t
			}
		}
		if r.Root.Kind == string(ark.KindArkDrive) && row.LastSyncAt.After(out.DriveLastSyncedAt) {
			out.DriveLastSyncedAt = row.LastSyncAt
		}
		out.Roots = append(out.Roots, row)
	}
	return out
}
