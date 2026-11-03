package health_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

func TestMetricsCounters(t *testing.T) {
	m := health.NewMetrics()
	c := m.Counters()
	if len(c) != 1 || c[health.MetricMapsTileBytes] != int64(0) {
		t.Fatalf("empty metrics: %v", c)
	}
	m.STTRequest("ok")
	m.STTRequest("ok")
	m.STTRequest("timeout")
	for _, s := range []float64{0.5, 1, 1.5, 7, 61} {
		m.STTSeconds(s)
	}
	m.TTSRequest("voice-en", "ok")
	m.TTSRequest("voice-es", "error")
	m.TTSSeconds(0.4)
	m.TTSSeconds(40)
	m.MapsTileBytes(100)
	m.MapsTileBytes(-5)
	m.MapsTileBytes(0)
	m.MapsTileBytes(23)
	m.ServiceRestart("kiwix")
	m.ServiceRestart("kiwix")
	m.ServiceRestart("whisper")

	c = m.Counters()
	for k, want := range map[string]int64{
		`localai_stt_requests_total{result="ok"}`:                     2,
		`localai_stt_requests_total{result="timeout"}`:                1,
		`localai_tts_requests_total{voice="voice-en",result="ok"}`:    1,
		`localai_tts_requests_total{voice="voice-es",result="error"}`: 1,
		`localai_maps_tile_bytes_total`:                               123,
		`localai_service_restarts_total{service="kiwix"}`:             2,
		`localai_service_restarts_total{service="whisper"}`:           1,
	} {
		if c[k] != want {
			t.Errorf("%s = %v, want %d", k, c[k], want)
		}
	}
	// Cumulative buckets of the STT histogram: 0.5 and 1 are <= 1; 1.5 <= 2; 7 <= 10; 61 only in +Inf.
	h, ok := c[health.MetricSTTSeconds].(map[string]any)
	if !ok {
		t.Fatalf("stt histogram: %v", c[health.MetricSTTSeconds])
	}
	b := h["buckets"].(map[string]any)
	want := map[string]int64{"1": 2, "2": 3, "5": 3, "10": 4, "30": 4, "60": 4, "+Inf": 5}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("bucket %s = %v, want %d", k, b[k], v)
		}
	}
	if h["count"] != int64(5) || h["sum"] != 71.0 {
		t.Errorf("count %v sum %v", h["count"], h["sum"])
	}
	if _, err := json.Marshal(c); err != nil {
		t.Errorf("counters are not JSON: %v", err)
	}
	if got := m.Names(); len(got) != 9 {
		t.Errorf("%d names: %v", len(got), got)
	}
}

func TestMetricsNilAndCardinality(t *testing.T) {
	var m *health.Metrics
	m.STTRequest("ok")
	m.STTSeconds(1)
	m.TTSRequest("v", "ok")
	m.TTSSeconds(1)
	m.MapsTileBytes(1)
	m.ServiceRestart("kiwix")
	if got := m.Counters(); len(got) != 0 {
		t.Errorf("nil metrics: %v", got)
	}

	m = health.NewMetrics()
	for i := 0; i < 100; i++ {
		m.TTSRequest(fmt.Sprintf("voice-%d", i), "ok")
	}
	voices := 0
	for k := range m.Counters() {
		if len(k) > len(health.MetricTTSRequests) && k[:len(health.MetricTTSRequests)] == health.MetricTTSRequests {
			voices++
		}
	}
	if voices != 33 { // 32 distinct voices and "other"
		t.Errorf("%d label sets, want 33", voices)
	}
}

func TestMetricsConcurrent(t *testing.T) {
	m := health.NewMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.STTRequest("ok")
				m.STTSeconds(1)
				m.ServiceRestart("kiwix")
				_ = m.Counters()
			}
		}()
	}
	wg.Wait()
	if got := m.Counters()[`localai_stt_requests_total{result="ok"}`]; got != int64(800) {
		t.Errorf("count %v", got)
	}
}
