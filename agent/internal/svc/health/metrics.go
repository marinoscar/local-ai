package health

import (
	"sort"
	"strconv"
	"sync"
)

// Metric names (companion-services.md section 7). There is no Prometheus endpoint in phase 1: the
// counters are exposed through GET /status?counters=1 (node.counters) by the CounterView the daemon
// wiring builds from Metrics.Counters.
const (
	MetricSTTRequests    = "localai_stt_requests_total"
	MetricSTTSeconds     = "localai_stt_seconds"
	MetricTTSRequests    = "localai_tts_requests_total"
	MetricTTSSeconds     = "localai_tts_seconds"
	MetricMapsTileBytes  = "localai_maps_tile_bytes_total"
	MetricServiceRestart = "localai_service_restarts_total"
)

// STTBuckets are the upper bounds, in seconds, of localai_stt_seconds (companion-services.md section 7).
var STTBuckets = []float64{1, 2, 5, 10, 30, 60}

// TTSBuckets are the upper bounds, in seconds, of localai_tts_seconds. The spec fixes the STT buckets
// only; synthesis of at most 2000 characters is shorter, hence the finer low end.
var TTSBuckets = []float64{0.5, 1, 2, 5, 10, 30}

// maxVoices bounds the label cardinality of the tts counter: a voice id past it counts as "other".
const maxVoices = 32

// Metrics holds the counters and histograms of the companion services in memory. It is safe for
// concurrent use; a nil *Metrics ignores every call, so a producer never checks. The labels are
// bounded sets (a result word, a voice id from the catalog, a service name), never user text.
type Metrics struct {
	mu        sync.Mutex
	stt       map[string]int64
	sttHist   *histogram
	tts       map[[2]string]int64
	voices    map[string]bool
	ttsHist   *histogram
	tileBytes int64
	restarts  map[string]int64
}

// NewMetrics returns empty metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		stt: map[string]int64{}, sttHist: newHistogram(STTBuckets),
		tts: map[[2]string]int64{}, voices: map[string]bool{}, ttsHist: newHistogram(TTSBuckets),
		restarts: map[string]int64{},
	}
}

// STTRequest counts one speech-to-text request with its result (ok, bad_audio, busy, timeout, error).
func (m *Metrics) STTRequest(result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.stt[result]++
	m.mu.Unlock()
}

// STTSeconds observes the duration of one speech-to-text request.
func (m *Metrics) STTSeconds(seconds float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sttHist.observe(seconds)
	m.mu.Unlock()
}

// TTSRequest counts one text-to-speech request with its voice and result.
func (m *Metrics) TTSRequest(voice, result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.voices[voice] {
		if len(m.voices) >= maxVoices {
			voice = "other"
		} else {
			m.voices[voice] = true
		}
	}
	m.tts[[2]string{voice, result}]++
	m.mu.Unlock()
}

// TTSSeconds observes the duration of one text-to-speech request.
func (m *Metrics) TTSSeconds(seconds float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.ttsHist.observe(seconds)
	m.mu.Unlock()
}

// MapsTileBytes adds the bytes of a served map tile response.
func (m *Metrics) MapsTileBytes(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	m.tileBytes += n
	m.mu.Unlock()
}

// ServiceRestart counts one restart of a service by the supervisor.
func (m *Metrics) ServiceRestart(service string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.restarts[service]++
	m.mu.Unlock()
}

// Counters returns the metrics as a map for node.counters: a counter is a number under its name with
// the labels in braces (`localai_stt_requests_total{result="ok"}`), a histogram an object with
// `count`, `sum` and cumulative `buckets` keyed by their upper bound and "+Inf". A metric that was
// never observed is absent, except the unlabelled byte counter, which is always present.
func (m *Metrics) Counters() map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for result, n := range m.stt {
		out[MetricSTTRequests+`{result="`+result+`"}`] = n
	}
	if m.sttHist.count > 0 {
		out[MetricSTTSeconds] = m.sttHist.snapshot()
	}
	for k, n := range m.tts {
		out[MetricTTSRequests+`{voice="`+k[0]+`",result="`+k[1]+`"}`] = n
	}
	if m.ttsHist.count > 0 {
		out[MetricTTSSeconds] = m.ttsHist.snapshot()
	}
	out[MetricMapsTileBytes] = m.tileBytes
	for svc, n := range m.restarts {
		out[MetricServiceRestart+`{service="`+svc+`"}`] = n
	}
	return out
}

// Names returns the sorted keys of Counters, for tests and docs.
func (m *Metrics) Names() []string {
	var keys []string
	for k := range m.Counters() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// histogram is a fixed-bucket histogram with cumulative counts.
type histogram struct {
	bounds []float64
	counts []int64 // per bound, not cumulative; the last is +Inf
	count  int64
	sum    float64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{bounds: bounds, counts: make([]int64, len(bounds)+1)}
}

func (h *histogram) observe(v float64) {
	h.count++
	h.sum += v
	for i, b := range h.bounds {
		if v <= b {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.bounds)]++
}

func (h *histogram) snapshot() map[string]any {
	buckets := map[string]any{}
	var cum int64
	for i, b := range h.bounds {
		cum += h.counts[i]
		buckets[strconv.FormatFloat(b, 'f', -1, 64)] = cum
	}
	cum += h.counts[len(h.bounds)]
	buckets["+Inf"] = cum
	return map[string]any{"count": h.count, "sum": h.sum, "buckets": buckets}
}
