package whisper

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

const transcriptMarker = "Zebra-Quokka-7431"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// fakeWhisper is a whisper-server stand-in.
type fakeWhisper struct {
	ts      *httptest.Server
	calls   atomic.Int32
	status  int
	body    string
	entered chan struct{} // receives once per call that arrived
	gate    chan struct{} // when non-nil a call waits for it to close
	got     chan []byte   // the WAV of the last call
	mu      sync.Mutex
}

func newFakeWhisper(t *testing.T) *fakeWhisper {
	f := &fakeWhisper{status: 200, body: `{"task":"transcribe","language":"en","duration":1,"text":" ` + transcriptMarker + ` ","detected_language_probability":0.9}`,
		entered: make(chan struct{}, 16), got: make(chan []byte, 16)}
	f.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if err := r.ParseMultipartForm(32 << 20); err == nil {
			if fh := r.MultipartForm.File["file"]; len(fh) == 1 {
				fd, _ := fh[0].Open()
				b, _ := io.ReadAll(fd)
				f.got <- b
			}
		}
		f.entered <- struct{}{}
		f.mu.Lock()
		gate, status, body := f.gate, f.status, f.body
		f.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.ts.Close)
	return f
}

type sttEnv struct {
	url     string
	fw      *fakeWhisper
	logs    *syncBuf
	metrics *health.Metrics
	queue   *Queue
	model   atomic.Value
	cfg     Config
	waited  chan struct{}
}

func newSTTEnv(t *testing.T, mutate func(*sttEnv)) *sttEnv {
	t.Helper()
	e := &sttEnv{fw: newFakeWhisper(t), logs: &syncBuf{}, metrics: health.NewMetrics(), waited: make(chan struct{}, 8)}
	e.model.Store("whisper-small")
	if mutate != nil {
		mutate(e)
	}
	e.queue = NewQueue(0, -1)
	e.queue.onWait = func() { e.waited <- struct{}{} }
	svc := NewSTT(Deps{
		Config:  func() Config { return e.cfg },
		Model:   func() string { return e.model.Load().(string) },
		Client:  &Client{BaseURL: func() string { return e.fw.ts.URL }, Timeout: 300 * time.Millisecond},
		Queue:   e.queue,
		Metrics: e.metrics,
		Logger:  slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	reg := api.NewRegistry()
	if err := reg.Add(svc.Routes()...); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	srv, err := api.New(api.Options{
		Registry: reg, Port: port, Version: "test", Mode: func() string { return "online" },
		Auth: api.AuthFunc(func(*http.Request, *api.Route) (api.Principal, error) {
			return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
		}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMutationsOpen(true)
	srv.SetStarting(false)
	ts.Config.Handler = srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	e.url = ts.URL
	return e
}

// tonePCM16WAV is a 1 s, 440 Hz recording at rate in a mono PCM16 WAV.
func tonePCM16WAV(rate int, secs float64) []byte {
	n := int(float64(rate) * secs)
	return wavSpec{tag: 1, channels: 1, rate: rate, bits: 16, data: pcm16Of(tone(n, rate, 440, 0.4))}.bytes()
}

func pcm16Of(s []float32) []byte { return pcm16(ToPCM16(s)...) }

type field struct{ name, value string }

func multipartBody(t *testing.T, audio []byte, fields ...field) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if audio != nil {
		p, _ := mw.CreateFormFile("audio", "a.wav")
		_, _ = p.Write(audio)
	}
	for _, f := range fields {
		_ = mw.WriteField(f.name, f.value)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
}

func (e *sttEnv) post(t *testing.T, audio []byte, hdr http.Header, fields ...field) reply {
	t.Helper()
	body, ct := multipartBody(t, audio, fields...)
	return e.postRaw(t, body, ct, hdr)
}

func (e *sttEnv) postRaw(t *testing.T, body io.Reader, ct string, hdr http.Header) reply {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.url+"/api/v1/speech/stt", body)
	req.Header.Set("Content-Type", ct)
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp = apitest.Record(t, resp)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := reply{status: resp.StatusCode, header: resp.Header}
	_ = json.Unmarshal(b, &out.body)
	return out
}

func errCode(r reply) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func errMessage(r reply) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["message"].(string)
	return s
}

func TestSTTSuccess(t *testing.T) {
	e := newSTTEnv(t, nil)
	r := e.post(t, tonePCM16WAV(44100, 1), nil, field{"language", "en"}, field{"prompt", "names"})
	if r.status != 200 {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
	if r.body["text"] != transcriptMarker || r.body["language"] != "en" || r.body["model"] != "whisper-small" || r.body["language_probability"] != 0.9 {
		t.Fatalf("body = %v", r.body)
	}
	if d, _ := r.body["duration_s"].(float64); d < 0.99 || d > 1.01 {
		t.Fatalf("duration_s = %v", r.body["duration_s"])
	}
	// What whisper received is 16 kHz mono PCM16: 1 s is 32000 bytes of data behind 44 of header.
	got := <-e.fw.got
	a, err := DecodeWAV(got, 0)
	if err != nil || a.Rate != 16000 || a.Channels != 1 || a.Frames() != 16000 || len(got) != 44+32000 {
		t.Fatalf("whisper got %+v, %d bytes, %v", a.Rate, len(got), err)
	}
}

func TestSTTStereoAndFloatAreAccepted(t *testing.T) {
	e := newSTTEnv(t, nil)
	mono := tone(8000, 8000, 300, 0.4)
	stereo := make([]float32, 0, 2*len(mono))
	for _, s := range mono {
		stereo = append(stereo, s, s)
	}
	for name, wav := range map[string][]byte{
		"float32 stereo": wavSpec{tag: 3, channels: 2, rate: 8000, bits: 32, data: f32(stereo...)}.bytes(),
		"extensible":     wavSpec{tag: 3, channels: 2, rate: 8000, bits: 32, data: f32(stereo...), extensible: true}.bytes(),
	} {
		if r := e.post(t, wav, nil); r.status != 200 {
			t.Errorf("%s: status %d %v", name, r.status, r.body)
		}
	}
}

func TestSTTRefusals(t *testing.T) {
	long := wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: make([]byte, 2*16000*3)}.bytes()
	silent := wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: make([]byte, 2*16000)}.bytes()
	tests := []struct {
		name   string
		audio  []byte
		fields []field
		status int
		code   string
	}{
		{"not wav", []byte("ID3 this is an mp3"), nil, 400, "LAI-SVC-014"},
		{"8-bit", wavSpec{tag: 1, channels: 1, rate: 8000, bits: 8, data: []byte{1, 2, 3, 4}}.bytes(), nil, 400, "LAI-SVC-014"},
		{"empty audio", wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16}.bytes(), nil, 400, "LAI-SVC-014"},
		{"too long", long, nil, 400, "LAI-SVC-014"},
		{"silent", silent, nil, 400, "LAI-SVC-014"},
		{"missing audio", nil, nil, 400, "LAI-AGT-020"},
		{"bad language", tonePCM16WAV(16000, 1), []field{{"language", "English!"}}, 400, "LAI-AGT-020"},
		{"language injection", tonePCM16WAV(16000, 1), []field{{"language", "en\r\nx"}}, 400, "LAI-AGT-020"},
		{"prompt too long", tonePCM16WAV(16000, 1), []field{{"prompt", strings.Repeat("a", 201)}}, 400, "LAI-AGT-020"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newSTTEnv(t, func(e *sttEnv) { e.cfg.MaxAudioSeconds = 2 })
			r := e.post(t, tc.audio, nil, tc.fields...)
			if r.status != tc.status || errCode(r) != tc.code {
				t.Fatalf("status %d code %q, want %d %s: %v", r.status, errCode(r), tc.status, tc.code, r.body)
			}
			if e.fw.calls.Load() != 0 {
				t.Fatal("whisper must not be called for a refused recording")
			}
		})
	}
}

func TestSTTSilenceMessageFollowsTheLanguage(t *testing.T) {
	silent := wavSpec{tag: 1, channels: 1, rate: 16000, bits: 16, data: make([]byte, 2*16000)}.bytes()
	e := newSTTEnv(t, nil)
	if m := errMessage(e.post(t, silent, nil)); m != silentEN {
		t.Errorf("en message = %q", m)
	}
	if m := errMessage(e.post(t, silent, http.Header{"Accept-Language": {"es"}})); m != silentES {
		t.Errorf("es message = %q", m)
	}
}

func TestSTTOverTheBodyLimitIs413(t *testing.T) {
	e := newSTTEnv(t, nil)
	big := make([]byte, int(api.SpeechMaxBody)+1024)
	copy(big, "RIFF")
	r := e.post(t, big, nil)
	if r.status != 413 {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
	if c := errCode(r); c != "LAI-SVC-014" && c != "LAI-AGT-025" {
		t.Fatalf("code %q", c)
	}
}

func TestSTTNotMultipart(t *testing.T) {
	e := newSTTEnv(t, nil)
	r := e.postRaw(t, strings.NewReader("hello"), "multipart/form-data; boundary=zz", nil)
	if r.status != 400 {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
}

func TestSTTNoModelIs503(t *testing.T) {
	e := newSTTEnv(t, func(e *sttEnv) { e.model.Store("") })
	r := e.post(t, tonePCM16WAV(16000, 1), nil)
	if r.status != 503 || errCode(r) != "LAI-SVC-010" {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
}

func TestSTTWhisperDownIs503(t *testing.T) {
	for _, status := range []int{500, 503} {
		e := newSTTEnv(t, func(e *sttEnv) { e.fw.status, e.fw.body = status, "boom" })
		r := e.post(t, tonePCM16WAV(16000, 1), nil)
		if r.status != 503 || errCode(r) != "LAI-SVC-011" {
			t.Fatalf("whisper %d -> status %d: %v", status, r.status, r.body)
		}
	}
	// Connection refused.
	e := newSTTEnv(t, nil)
	e.fw.ts.Close()
	if r := e.post(t, tonePCM16WAV(16000, 1), nil); r.status != 503 || errCode(r) != "LAI-SVC-011" {
		t.Fatalf("refused -> status %d: %v", r.status, r.body)
	}
}

func TestSTTHangingWhisperIs504(t *testing.T) {
	e := newSTTEnv(t, func(e *sttEnv) { e.fw.gate = make(chan struct{}) })
	r := e.post(t, tonePCM16WAV(16000, 1), nil)
	if r.status != 504 || errCode(r) != "LAI-SVC-016" {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
}

func TestSTTThirdWaitingRequestGets429(t *testing.T) {
	gate := make(chan struct{})
	e := newSTTEnv(t, func(e *sttEnv) { e.fw.gate = gate })
	wav := tonePCM16WAV(16000, 1)
	results := make(chan reply, 3)
	send := func() { results <- e.post(t, wav, nil) }

	go send() // A: in flight
	<-e.fw.entered
	go send() // B: waits
	<-e.waited
	go send() // C: waits
	<-e.waited

	r := e.post(t, wav, nil) // D: refused
	if r.status != 429 || errCode(r) != "LAI-SVC-015" || r.header.Get("Retry-After") == "" {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
	close(gate)
	// A may have timed out at 300 ms by now; what matters is B and C were admitted, not refused.
	for i := 0; i < 3; i++ {
		if got := <-results; got.status == 429 {
			t.Fatalf("a queued request was refused: %v", got.body)
		}
	}
}

func TestSTTNeverLogsTheTranscript(t *testing.T) {
	e := newSTTEnv(t, nil)
	if r := e.post(t, tonePCM16WAV(16000, 1), nil, field{"prompt", "secret-prompt-text"}); r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "stt done") || !strings.Contains(logs, "audio_s=") || !strings.Contains(logs, "model=whisper-small") {
		t.Fatalf("no stt done line: %s", logs)
	}
	if strings.Contains(logs, transcriptMarker) || strings.Contains(logs, "secret-prompt-text") {
		t.Fatalf("the transcript or prompt reached a log: %s", logs)
	}
}

func TestSTTMetrics(t *testing.T) {
	e := newSTTEnv(t, nil)
	e.post(t, tonePCM16WAV(16000, 1), nil)
	e.post(t, []byte("nope"), nil)
	c := e.metrics.Counters()
	if c[`localai_stt_requests_total{result="ok"}`] != int64(1) || c[`localai_stt_requests_total{result="bad_audio"}`] != int64(1) {
		t.Fatalf("counters = %v", c)
	}
}

func TestSTTDisabledIs503(t *testing.T) {
	off := false
	e := newSTTEnv(t, func(e *sttEnv) { e.cfg.Enabled = &off })
	if r := e.post(t, tonePCM16WAV(16000, 1), nil); r.status != 503 || errCode(r) != "LAI-SVC-011" {
		t.Fatalf("status %d: %v", r.status, r.body)
	}
}
