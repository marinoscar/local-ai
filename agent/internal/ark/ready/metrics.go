package ready

import "time"

// The metrics of ark-sync.md section 7 that the reports can answer. They are shown in /status under
// ark.metrics (statusapi.ArkMetric); the sync counters of the engine (localai_sync_*) are the engine's.
const (
	// MetricRootOverall is 0, 1 or 2 for green, yellow or red.
	MetricRootOverall = "localai_ark_root_overall"
	// MetricRootFreeBytes is the free space of the root's filesystem.
	MetricRootFreeBytes = "localai_ark_root_free_bytes"
	// MetricReverifyAgeDaysMax is the age in days of the least recently re-verified copy of the root.
	MetricReverifyAgeDaysMax = "localai_ark_reverify_age_days_max"
)

// Metric is one gauge with its label (root=<id>).
type Metric struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// MetricsOf returns the three gauges of every report, in the order of the reports and then of the
// metric names. now is the clock the age is counted against.
func MetricsOf(reports []Report, now time.Time) []Metric {
	out := make([]Metric, 0, 3*len(reports))
	for _, r := range reports {
		labels := map[string]string{"root": r.Root.ID}
		out = append(out,
			Metric{Name: MetricRootOverall, Labels: labels, Value: float64(r.Overall.Gauge())},
			Metric{Name: MetricRootFreeBytes, Labels: labels, Value: float64(r.Root.FSFreeBytes)},
			Metric{Name: MetricReverifyAgeDaysMax, Labels: labels, Value: float64(reverifyAgeMax(r, now))},
		)
	}
	return out
}

// reverifyAgeMax is the days since the oldest last_reverified_at of any item of the report, 0 when
// none has one.
func reverifyAgeMax(r Report, now time.Time) int {
	max := 0
	for _, it := range r.Items {
		if it.LastReverifiedAt == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, it.LastReverifiedAt); err == nil {
			if a := ageDays(now, t); a > max {
				max = a
			}
		}
	}
	return max
}
