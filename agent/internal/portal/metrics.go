package portal

import "sync"

// Metric names of this package (agent.md section 7). There is no Prometheus endpoint in phase 1: the
// daemon wiring exposes Counters through GET /status?counters=1.
const (
	MetricStatusRequests = "localai_portal_status_requests_total"
	MetricSessionsActive = "localai_portal_sessions_active"
	MetricRateLimited    = "localai_portal_rate_limited_total"
)

// Metrics holds the counters in memory. A nil *Metrics ignores every call.
type Metrics struct {
	mu       sync.Mutex
	status   map[string]int64
	limited  map[string]int64
	sessions func() int
}

// NewMetrics returns empty metrics.
func NewMetrics() *Metrics {
	return &Metrics{status: map[string]int64{}, limited: map[string]int64{}}
}

// StatusRequest counts one GET /portal/status by session kind (none, portal, full).
func (m *Metrics) StatusRequest(session string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.status[session]++
	m.mu.Unlock()
}

// RateLimited counts one 429 by bucket (stt, tts, load, import, restart).
func (m *Metrics) RateLimited(bucket string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.limited[bucket]++
	m.mu.Unlock()
}

// SetSessionsGauge sets where localai_portal_sessions_active is read from.
func (m *Metrics) SetSessionsGauge(f func() int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sessions = f
	m.mu.Unlock()
}

// Counters returns the values keyed the way node.counters is: name{label="value"}.
func (m *Metrics) Counters() map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.status {
		out[MetricStatusRequests+`{session="`+k+`"}`] = v
	}
	for k, v := range m.limited {
		out[MetricRateLimited+`{bucket="`+k+`"}`] = v
	}
	if m.sessions != nil {
		out[MetricSessionsActive] = m.sessions()
	}
	return out
}
