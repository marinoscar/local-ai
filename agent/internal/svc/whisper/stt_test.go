package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

const secretTranscript = "the-private-words-never-in-a-log"

// fakeWhisper is a whisper-server: it records what it received and answers as scripted.
type fakeWhisper struct {
	*httptest.Server
	mu       sync.Mutex
	calls    int
	fields   map[string]string
	fileWAV  []byte
	status   int
	reply    string
	hold     chan struct{} // when non-nil, an answer waits for it to close
	inFlight int
	maxSeen  int
}

func newFakeWhisper(t *testing.T) *fakeWhisper {
	f := &fakeWhisper{status: 200, reply: `{"task":"transcribe","language":"english","duration":2.5,"text":" ` + secretTranscript + ` ","detected_language_probability":0.9}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("fake whisper: %v", err)
			return
		}
		file, _, _ := r.FormFile("file")
		wav, _ := io.ReadAll(file)
		f.mu.Lock()
		f.calls++
		f.inFlight++
		if f.inFlight > f.maxSeen {
			f.maxSeen = f.inFlight
		}
		f.fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			f.fields[k] = v[0]
		}
		f.fileWAV = wav
		status, reply, hold := f.status, f.reply, f.hold
		f.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
			}
		}
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWhisper) snapshot() (calls int, fields map[string]string, wav []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.fields, f.fileWAV
}

// syncBuf is a log sink safe for concurrent writers.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type rig struct {
	t       *testing.T
	fake    *fakeWhisper
	svc     *STTService
	logs    *syncBuf
	metrics *health.Metrics
	cfg     STTConfig
	model   string
}

func newRig(t *testing.T, mutate ...func(*STTDeps)) *rig {
	r := &rig{t: t, fake: newFakeWhisper(t), logs: &syncBuf{}, metrics: health.NewMetrics(), cfg: DefaultSTTConfig(), model: "whisper-small"}
	d := STTDeps{
		Config: func() STTConfig { return r.cfg }, BaseURL: func() string { return r.fake.URL },
		ModelID: func() string { return r.model }, Metrics: r.metrics, Timeout: 2 * time.Second,
		Logger: slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, m := range mutate {
		m(&d)
	}
	r.svc = NewSTT(d)
	return r
}

// speech is a mono 16-bit recording of a tone: loud enough, seconds long, at rate.
func speech(rate int, seconds float64, channels int) []byte {
	tn := tone(440, rate, seconds, 0.3)
	var pcm []int16
	for _, s := range tn {
		for c := 0; c < channels; c++ {
			pcm = append(pcm, toPCM16(float64(s)))
		}
	}
	return riff(chunk("fmt ", fmtBody(1, channels, rate, 16)), chunk("data", pcm16(pcm...)))
}

type part struct{ name, value string }

func multipartBody(audio []byte, haveAudio bool, fields ...part) (body []byte, ctype string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if haveAudio {
		fw, _ := mw.CreateFormFile("audio", "rec.wav")
		fw.Write(audio)
	}
	for _, f := range fields {
		mw.WriteField(f.name, f.value)
	}
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

func (r *rig) post(body []byte, ctype string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/speech/stt", bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	r.svc.serve(rec, req)
	return rec
}

func (r *rig) postAudio(audio []byte, fields ...part) *httptest.ResponseRecorder {
	b, ct := multipartBody(audio, true, fields...)
	return r.post(b, ct)
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) api.ErrorBody {
	t.Helper()
	var env api.ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not an error envelope: %q", rec.Body.String())
	}
	return env.Error
}

func (r *rig) counter(result string) int64 {
	v, _ := r.metrics.Counters()[health.MetricSTTRequests+`{result="`+result+`"}`].(int64)
	return v
}

func TestSTTSuccess(t *testing.T) {
	r := newRig(t)
	rec := r.postAudio(speech(44100, 2, 2), part{"language", "en"}, part{"prompt", "names: Ana"})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["text"] != secretTranscript || got["language"] != "en" || got["model"] != "whisper-small" ||
		got["duration_s"] != 2.5 || got["language_probability"] != 0.9 {
		t.Errorf("body = %v", got)
	}
	if _, ok := got["elapsed_ms"]; !ok {
		t.Error("no elapsed_ms")
	}
	_, fields, wav := r.fake.snapshot()
	if fields["language"] != "en" || fields["prompt"] != "names: Ana" || fields["response_format"] != "verbose_json" {
		t.Errorf("fields = %v", fields)
	}
	// What whisper got is a 16 kHz mono PCM16 WAV of the same length: 2 s.
	a, err := DecodeWAV(wav, 0)
	if err != nil || a.Rate != WhisperRate || a.Channels != 1 || len(a.Samples) != 2*WhisperRate {
		t.Errorf("wav sent to whisper: %+v err %v", a, err)
	}
	if r.counter("ok") != 1 {
		t.Errorf("counters = %v", r.metrics.Counters())
	}
}

func TestSTTDefaultsToAutoAndNoModelLabel(t *testing.T) {
	r := newRig(t)
	r.model = ""
	rec := r.postAudio(speech(16000, 1, 1))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	_, fields, _ := r.fake.snapshot()
	if fields["language"] != "auto" {
		t.Errorf("language = %q", fields["language"])
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["model"] != "whisper" {
		t.Errorf("model = %v", got["model"])
	}
}

func TestSTTModelFromNodeYAMLWhenTheEnvFileHasNone(t *testing.T) {
	r := newRig(t)
	r.model = ""
	r.cfg.Model = "whisper-medium"
	rec := r.postAudio(speech(16000, 1, 1))
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["model"] != "whisper-medium" {
		t.Errorf("status %d body %v", rec.Code, got)
	}
}

func TestSTTErrors(t *testing.T) {
	long := speech(8000, 3, 1)
	eightBit := riff(chunk("fmt ", fmtBody(1, 1, 16000, 8)), chunk("data", []byte{1, 2, 3, 4}))
	silent := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", make([]byte, 32000)))
	cases := []struct {
		name   string
		setup  func(r *rig)
		do     func(r *rig) *httptest.ResponseRecorder
		status int
		code   string
		reason string
		result string
	}{
		{"not a wav", nil, func(r *rig) *httptest.ResponseRecorder { return r.postAudio([]byte("ID3 this is an mp3, trust me")) }, 400, doctor.SVC014, ReasonNotWAV, "bad_audio"},
		{"8-bit pcm", nil, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(eightBit) }, 400, doctor.SVC014, ReasonEncoding, "bad_audio"},
		{"empty audio part", nil, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(nil) }, 400, doctor.SVC014, ReasonEmpty, "bad_audio"},
		{"no audio part", nil, func(r *rig) *httptest.ResponseRecorder {
			b, ct := multipartBody(nil, false, part{"language", "en"})
			return r.post(b, ct)
		}, 400, doctor.SVC014, ReasonEmpty, "bad_audio"},
		{"too long", func(r *rig) { r.cfg.MaxAudioSeconds = 2 }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(long) }, 400, doctor.SVC014, ReasonTooLong, "bad_audio"},
		{"silent", nil, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(silent) }, 400, doctor.SVC014, ReasonSilent, "bad_audio"},
		{"over 25 MiB", nil, func(r *rig) *httptest.ResponseRecorder {
			return r.postAudio(append(speech(16000, 1, 1), make([]byte, int(api.SpeechMaxBody))...))
		}, 413, doctor.SVC014, "too_large", "bad_audio"},
		{"duplicate audio", nil, func(r *rig) *httptest.ResponseRecorder {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for i := 0; i < 2; i++ {
				fw, _ := mw.CreateFormFile("audio", "r.wav")
				fw.Write(speech(16000, 1, 1))
			}
			mw.Close()
			return r.post(buf.Bytes(), mw.FormDataContentType())
		}, 400, api.CodeInvalidRequest, "duplicate", "bad_audio"},
		{"bad language", nil, func(r *rig) *httptest.ResponseRecorder {
			return r.postAudio(speech(16000, 1, 1), part{"language", "English!"})
		}, 400, api.CodeInvalidRequest, "", "bad_audio"},
		{"prompt over 200 chars", nil, func(r *rig) *httptest.ResponseRecorder {
			return r.postAudio(speech(16000, 1, 1), part{"prompt", strings.Repeat("ñ", 201)})
		}, 400, api.CodeInvalidRequest, "", "bad_audio"},
		{"not multipart", nil, func(r *rig) *httptest.ResponseRecorder { return r.post([]byte("{}"), "application/json") }, 400, api.CodeInvalidRequest, "bad_multipart", "bad_audio"},
		{"broken multipart", nil, func(r *rig) *httptest.ResponseRecorder {
			return r.post([]byte("--x\r\nContent-Disposition: form-data; name=\"audio\"\r\n\r\nabc"), "multipart/form-data; boundary=x")
		}, 400, api.CodeInvalidRequest, "bad_multipart", "bad_audio"},
		{"whisper answers 500", func(r *rig) { r.fake.status, r.fake.reply = 500, "boom" }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(speech(16000, 1, 1)) }, 503, doctor.SVC011, "status", "error"},
		{"whisper answers an error field", func(r *rig) { r.fake.reply = `{"error":"x"}` }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(speech(16000, 1, 1)) }, 503, doctor.SVC011, "error_field", "error"},
		{"service down", func(r *rig) { r.fake.Close() }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(speech(16000, 1, 1)) }, 503, doctor.SVC011, "connect", "error"},
		{"service down and no model", func(r *rig) { r.fake.Close(); r.model = "" }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(speech(16000, 1, 1)) }, 503, doctor.SVC010, "connect", "error"},
		{"speech disabled", func(r *rig) { r.cfg.Enabled = false }, func(r *rig) *httptest.ResponseRecorder { return r.postAudio(speech(16000, 1, 1)) }, 503, doctor.SVC011, "disabled", "error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.setup != nil {
				c.setup(r)
			}
			rec := c.do(r)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			e := errorBody(t, rec)
			if e.Code != c.code || e.Message == "" || e.Fix == "" {
				t.Errorf("error = %+v, want code %s with a message and a fix", e, c.code)
			}
			if c.reason != "" && e.Details["reason"] != c.reason {
				t.Errorf("details = %v, want reason %s", e.Details, c.reason)
			}
			if r.counter(c.result) != 1 {
				t.Errorf("counters = %v, want one %s", r.metrics.Counters(), c.result)
			}
			if calls, _, _ := r.fake.snapshot(); calls != 0 && c.result == "bad_audio" {
				t.Errorf("whisper was called %d time(s) for a request that never reached it", calls)
			}
		})
	}
}

func TestSTTSilenceMessageIsBilingual(t *testing.T) {
	silent := riff(chunk("fmt ", fmtBody(1, 1, 16000, 16)), chunk("data", make([]byte, 32000)))
	r := newRig(t)
	b, ct := multipartBody(silent, true)
	for _, c := range []struct{ header, want string }{
		{"", "I couldn't hear anything. Try again closer to the microphone."},
		{"en-US,en;q=0.9", "I couldn't hear anything. Try again closer to the microphone."},
		{"es-MX", "No pude oír nada. Intente otra vez más cerca del micrófono."},
		{"fr", "I couldn't hear anything. Try again closer to the microphone."},
	} {
		rec := r.post(b, ct, UILanguageHeader, c.header)
		if rec.Code != 400 {
			t.Fatalf("status %d", rec.Code)
		}
		if got := errorBody(t, rec); got.Message != c.want || got.Code != doctor.SVC014 {
			t.Errorf("header %q: %+v", c.header, got)
		}
	}
}

func TestSTTHangingWhisperIs504(t *testing.T) {
	r := newRig(t, func(d *STTDeps) { d.Timeout = 60 * time.Millisecond })
	r.fake.hold = make(chan struct{})
	defer close(r.fake.hold)
	began := time.Now()
	rec := r.postAudio(speech(16000, 1, 1))
	if rec.Code != 504 || errorBody(t, rec).Code != doctor.SVC016 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if time.Since(began) > 5*time.Second {
		t.Error("the timeout did not cut the call")
	}
	if r.counter("timeout") != 1 {
		t.Errorf("counters = %v", r.metrics.Counters())
	}
	// The slot is free again.
	if n, in := r.svc.Queue().Stats(); n != 0 || in != 0 {
		t.Errorf("queue = %d admitted, %d in flight", n, in)
	}
}

func TestSTTThirdWaitingRequestGets429(t *testing.T) {
	r := newRig(t)
	r.fake.hold = make(chan struct{})
	waiting := make(chan struct{}, 4)
	r.svc.Queue().onWait = func() { waiting <- struct{}{} }
	body, ct := multipartBody(speech(16000, 1, 1), true)

	type out struct{ rec *httptest.ResponseRecorder }
	results := make(chan out, 3)
	send := func() {
		go func() { results <- out{r.post(body, ct)} }()
	}
	send() // goes to whisper and is held there
	for {
		if calls, _, _ := r.fake.snapshot(); calls == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	send()
	send()
	<-waiting
	<-waiting // two requests wait behind the one in flight

	rec := r.post(body, ct) // the 4th: nowhere to wait
	if rec.Code != 429 || errorBody(t, rec).Code != doctor.SVC015 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d retry-after %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if r.counter("busy") != 1 {
		t.Errorf("counters = %v", r.metrics.Counters())
	}

	close(r.fake.hold)
	for i := 0; i < 3; i++ {
		if o := <-results; o.rec.Code != 200 {
			t.Errorf("queued request %d: status %d: %s", i, o.rec.Code, o.rec.Body)
		}
	}
	r.fake.mu.Lock()
	seen := r.fake.maxSeen
	r.fake.mu.Unlock()
	if seen != 1 {
		t.Errorf("whisper saw %d requests at once, want 1", seen)
	}
}

func TestSTTCallerLeavingWhileQueuedFreesTheQueue(t *testing.T) {
	r := newRig(t)
	r.fake.hold = make(chan struct{})
	defer close(r.fake.hold)
	waiting := make(chan struct{}, 1)
	r.svc.Queue().onWait = func() { waiting <- struct{}{} }
	body, ct := multipartBody(speech(16000, 1, 1), true)
	go r.post(body, ct) // in flight, held
	for {
		if calls, _, _ := r.fake.snapshot(); calls == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/speech/stt", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	r.svc.afterRequest = func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	go r.svc.serve(rec, req)
	<-waiting
	cancel()
	<-done
	if rec.Body.Len() != 0 {
		t.Errorf("an answer was written to a caller that left: %s", rec.Body)
	}
	if n, _ := r.svc.Queue().Stats(); n != 1 {
		t.Errorf("admitted = %d, want only the request in flight", n)
	}
}

func TestSTTNeverLogsTheTranscript(t *testing.T) {
	r := newRig(t)
	if rec := r.postAudio(speech(16000, 1, 1), part{"prompt", "hello prompt words"}); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	r.fake.status = 500
	r.postAudio(speech(16000, 1, 1))
	logs := r.logs.String()
	for _, secret := range []string{secretTranscript, "hello prompt words"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs)
		}
	}
	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["msg"] == "stt done" {
			line = m
		}
	}
	if line == nil || line["component"] != "whisper" || line["language"] != "en" || line["model"] != "whisper-small" || line["audio_s"] != 2.5 {
		t.Errorf("stt done line = %v\n%s", line, logs)
	}
	if _, ok := line["elapsed_ms"]; !ok {
		t.Error("no elapsed_ms in the log line")
	}
}

func TestSTTRouteMetadata(t *testing.T) {
	rts := STTRoutes(STTDeps{})
	if len(rts) != 1 {
		t.Fatalf("%d routes", len(rts))
	}
	rt := rts[0]
	if rt.Method != "POST" || rt.Pattern != "/speech/stt" || rt.Timeout != 120*time.Second ||
		rt.MaxBody != 25<<20 || len(rt.MediaTypes) != 1 || rt.MediaTypes[0] != "multipart/form-data" || rt.Handler == nil {
		t.Errorf("route = %+v", rt)
	}
	if len(rt.Auth) != 2 || rt.Auth[0] != api.AuthOp || rt.Auth[1] != api.AuthPortal {
		t.Errorf("auth = %v", rt.Auth)
	}
}

func TestSTTPortFromNodeYAML(t *testing.T) {
	var asked []string
	d := STTDeps{Port: func() int { return 9191 }, HTTP: doerFunc(func(req *http.Request) (*http.Response, error) {
		asked = append(asked, req.URL.String())
		return nil, io.ErrUnexpectedEOF
	})}
	NewSTT(d).client.Transcribe(context.Background(), []byte("x"), "auto", "")
	if len(asked) != 1 || asked[0] != "http://127.0.0.1:9191/inference" {
		t.Errorf("asked %v", asked)
	}
	asked = nil
	NewSTT(STTDeps{HTTP: d.HTTP}).client.Transcribe(context.Background(), []byte("x"), "auto", "")
	if len(asked) != 1 || asked[0] != "http://127.0.0.1:8081/inference" {
		t.Errorf("default port: asked %v", asked)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestEnvModelID(t *testing.T) {
	env := "WHISPER_MODEL=/srv/x/ggml-small.bin\nWHISPER_THREADS=4\n# c\nWHISPER_MODEL_ID=\"whisper-small\"\n"
	read := func(string) ([]byte, error) { return []byte(env), nil }
	if got := EnvModelID("/etc/localai/whisper.env", read); got != "whisper-small" {
		t.Errorf("got %q", got)
	}
	if got := EnvModelID("x", func(string) ([]byte, error) { return nil, io.EOF }); got != "" {
		t.Errorf("missing file: %q", got)
	}
	if got := EnvModelID("x", func(string) ([]byte, error) { return []byte("WHISPER_MODEL=/x\n"), nil }); got != "" {
		t.Errorf("no key: %q", got)
	}
}
