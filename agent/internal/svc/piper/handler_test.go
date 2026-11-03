package piper

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

var bothVoices = []string{"piper-en-medium", "piper-es-medium"}

func TestRoutesMetadata(t *testing.T) {
	routes := Routes(Deps{})
	if len(routes) != 2 {
		t.Fatalf("%d routes", len(routes))
	}
	want := map[string]struct{ timeout string }{
		"POST /speech/tts": {"2m0s"}, "GET /speech/voices": {"5s"},
	}
	for _, r := range routes {
		w, ok := want[r.Method+" "+r.Pattern]
		if !ok {
			t.Errorf("unexpected route %s %s", r.Method, r.Pattern)
			continue
		}
		if r.Timeout.String() != w.timeout {
			t.Errorf("%s %s: timeout %s, want %s", r.Method, r.Pattern, r.Timeout, w.timeout)
		}
		if len(r.Auth) != 2 || r.Auth[0] != api.AuthOp || r.Auth[1] != api.AuthPortal {
			t.Errorf("%s %s: auth %v, want op and portal", r.Method, r.Pattern, r.Auth)
		}
		if r.Handler == nil {
			t.Errorf("%s %s: no handler", r.Method, r.Pattern)
		}
	}
}

func TestRegisterAddsTheRoutesOnce(t *testing.T) {
	reg := api.NewRegistry()
	if err := Register(reg, Deps{}); err != nil {
		t.Fatal(err)
	}
	if got := len(reg.Routes()); got != 2 {
		t.Errorf("%d routes registered", got)
	}
	if err := Register(reg, Deps{}); err == nil {
		t.Error("a second registration must fail")
	}
}

func TestSynthesisStreamsAChunkedWAV(t *testing.T) {
	pcm := sine(44100, 0.4) // one second at 22 050 Hz? no: 44100 samples is two seconds
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Chunks: chunks(pcm, 20000)}}})

	resp, body := e.ok(map[string]any{"text": spokenText, "language": "en"}, nil)
	e.waitDone(1)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("content type %q", ct)
	}
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" || resp.ContentLength != -1 {
		t.Errorf("transfer encoding %v, length %d: the answer must be chunked", resp.TransferEncoding, resp.ContentLength)
	}
	if resp.Header.Get("X-LocalAI-Voice") != "piper-en-medium" || resp.Header.Get("X-LocalAI-Language") != "en" {
		t.Errorf("headers: %v", resp.Header)
	}
	if resp.Header.Get("X-LocalAI-Fallback") != "" {
		t.Errorf("no fallback happened, header %q", resp.Header.Get("X-LocalAI-Fallback"))
	}
	hdr, _ := StreamingHeader(22050)
	if !bytes.Equal(body[:HeaderSize], hdr) {
		t.Errorf("header %x, want %x", body[:HeaderSize], hdr)
	}
	if info, err := ParseHeader(body); err != nil || info.SampleRate != 22050 || info.DataBytes != -1 {
		t.Errorf("ParseHeader = %+v, %v", info, err)
	}
	if !bytes.Equal(body[HeaderSize:], pcm) {
		t.Errorf("the audio differs: %d bytes, want %d", len(body)-HeaderSize, len(pcm))
	}

	// The exec contract: fixed argv, text on stdin only, one process, waited for.
	calls := e.streamer.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d processes started", len(calls))
	}
	c := calls[0]
	if c.Path != e.root+"/current/piper" || c.Args[0] != "--model" || c.Args[len(c.Args)-1] != "0.25" {
		t.Errorf("command %s %q", c.Path, c.Args)
	}
	for _, a := range c.Args {
		if strings.Contains(a, spokenMarker) {
			t.Errorf("text in the argv: %q", a)
		}
	}
	p := e.streamer.Procs()[0]
	if got := string(p.Stdin()); got != spokenText+"\n" {
		t.Errorf("stdin = %q", got)
	}
	if !p.Waited() {
		t.Error("the process was not waited for")
	}
	if strings.Contains(e.logs.String(), spokenMarker) || strings.Contains(e.logs.String(), "stays out of the logs") {
		t.Errorf("the text is in the log:\n%s", e.logs.String())
	}
	for _, want := range []string{"tts done", fmt.Sprintf("chars=%d", len([]rune(spokenText))), "voice=piper-en-medium", "language=en", "elapsed_ms=2000"} {
		if !strings.Contains(e.logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, e.logs.String())
		}
	}
	m := e.metrics.Counters()
	if m[`localai_tts_requests_total{voice="piper-en-medium",result="ok"}`] != int64(1) {
		t.Errorf("counters = %v", m)
	}
	if h, ok := m["localai_tts_seconds"].(map[string]any); !ok || h["count"] != int64(1) || h["sum"] != 2.0 {
		t.Errorf("tts seconds = %v", m["localai_tts_seconds"])
	}
}

func TestOddByteChunksNeverSplitASample(t *testing.T) {
	pcm := sine(1000, 0.3)
	// Cut at odd offsets: every sample straddles two reads.
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Chunks: [][]byte{pcm[:1], pcm[1:6], pcm[6:1001], pcm[1001:]}}}})
	resp, body := e.ok(map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
	e.waitDone(1)
	if resp.StatusCode != 200 || !bytes.Equal(body[HeaderSize:], pcm) {
		t.Errorf("status %d, audio differs (%d bytes, want %d)", resp.StatusCode, len(body)-HeaderSize, len(pcm))
	}
}

func TestATrailingOddByteIsDropped(t *testing.T) {
	pcm := sine(500, 0.3)
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Chunks: [][]byte{append(append([]byte(nil), pcm...), 0x55)}}}})
	resp, body := e.ok(map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
	e.waitDone(1)
	if resp.StatusCode != 200 || len(body)-HeaderSize != len(pcm) {
		t.Errorf("status %d, %d audio bytes, want %d", resp.StatusCode, len(body)-HeaderSize, len(pcm))
	}
}

func TestVoiceSelectionOverHTTP(t *testing.T) {
	cases := []struct {
		name      string
		voices    []string
		body      map[string]any
		hdr       map[string]string
		wantVoice string
		wantLang  string
		fallback  bool
	}{
		{"english text is detected", bothVoices, map[string]any{"text": "Press firmly on the wound with a clean cloth."}, nil, "piper-en-medium", "en", false},
		{"spanish text auto-detects es", bothVoices, map[string]any{"text": "Hierva el agua durante un minuto."}, nil, "piper-es-medium", "es", false},
		{"spanish text with language auto", bothVoices, map[string]any{"text": "Hierva el agua durante un minuto, por favor.", "language": "auto"}, nil, "piper-es-medium", "es", false},
		{"explicit language wins over the text", bothVoices, map[string]any{"text": "Hierva el agua durante un minuto.", "language": "en"}, nil, "piper-en-medium", "en", false},
		{"explicit voice wins over everything", bothVoices, map[string]any{"text": "Press firmly on the wound with a clean cloth.", "voice": "piper-es-medium", "language": "en"}, nil, "piper-es-medium", "es", false},
		{"null voice is no voice", bothVoices, map[string]any{"text": "Hierva el agua durante un minuto, por favor.", "voice": nil}, nil, "piper-es-medium", "es", false},
		{"short text uses the portal language", bothVoices, map[string]any{"text": "Hola"}, map[string]string{"X-LocalAI-UI-Language": "es"}, "piper-es-medium", "es", false},
		{"portal language with a region and weights", bothVoices, map[string]any{"text": "Ok"}, map[string]string{"X-LocalAI-UI-Language": "es-MX,en;q=0.5"}, "piper-es-medium", "es", false},
		{"unknown portal language is ignored", bothVoices, map[string]any{"text": "Ok"}, map[string]string{"X-LocalAI-UI-Language": "fr"}, "piper-en-medium", "en", true},
		{"language without a voice falls back", []string{"piper-en-medium"}, map[string]any{"text": "Hierva el agua.", "language": "es"}, nil, "piper-en-medium", "en", true},
		{"only a spanish voice, english text", []string{"piper-es-medium"}, map[string]any{"text": "Press firmly on the wound with a clean cloth."}, nil, "piper-es-medium", "es", true},
	}
	for _, c := range cases {
		e := newEnv(t, envOpts{voices: c.voices, scripts: []fake.Script{{Chunks: [][]byte{sine(200, 0.3)}}}})
		resp, _ := e.ok(c.body, c.hdr)
		e.waitDone(1)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", c.name, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("X-LocalAI-Voice"); got != c.wantVoice {
			t.Errorf("%s: voice %q, want %q", c.name, got, c.wantVoice)
		}
		if got := resp.Header.Get("X-LocalAI-Language"); got != c.wantLang {
			t.Errorf("%s: language %q, want %q", c.name, got, c.wantLang)
		}
		if got := resp.Header.Get("X-LocalAI-Fallback") == "true"; got != c.fallback {
			t.Errorf("%s: fallback %v, want %v", c.name, got, c.fallback)
		}
		if c.fallback && !strings.Contains(e.logs.String(), doctor.SVC021) {
			t.Errorf("%s: the fallback must log %s:\n%s", c.name, doctor.SVC021, e.logs.String())
		}
	}
}

func TestTextIsCleanedBeforeItIsSpoken(t *testing.T) {
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Chunks: [][]byte{sine(200, 0.3)}}, {Chunks: [][]byte{sine(200, 0.3)}}}})
	e.ok(map[string]any{"text": "# Steps\n- **Boil** the water\n- See https://example.test/x \U0001F600\n```\nrm -rf /\n```", "language": "en"}, nil)
	e.waitDone(1)
	e.ok(map[string]any{"text": "Vea https://example.test/x ahora", "language": "es"}, nil)
	e.waitDone(1)
	procs := e.streamer.Procs()
	if got, want := string(procs[0].Stdin()), "Steps. Boil the water. See link. (code omitted)\n"; got != want {
		t.Errorf("stdin 1 = %q, want %q", got, want)
	}
	if got, want := string(procs[1].Stdin()), "Vea enlace ahora\n"; got != want {
		t.Errorf("stdin 2 = %q, want %q", got, want)
	}
}

func TestSpeedDividesTheLengthScale(t *testing.T) {
	e := newEnv(t, envOpts{voices: bothVoices, configure: func(c *Config) { c.LengthScale = 1.2 },
		scripts: []fake.Script{{Chunks: [][]byte{sine(200, 0.3)}}, {Chunks: [][]byte{sine(200, 0.3)}}}})
	e.ok(map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
	e.waitDone(1)
	e.ok(map[string]any{"text": "Hello there my friend", "language": "en", "speed": 1.5}, nil)
	e.waitDone(1)
	calls := e.streamer.Calls()
	arg := func(args []string, name string) string {
		for i, a := range args {
			if a == name {
				return args[i+1]
			}
		}
		return ""
	}
	if got := arg(calls[0].Args, "--length_scale"); got != "1.2" {
		t.Errorf("default speed: --length_scale %q, want 1.2", got)
	}
	if got := arg(calls[1].Args, "--length_scale"); got != "0.8" {
		t.Errorf("speed 1.5: --length_scale %q, want 0.8", got)
	}
}

func TestRejectedRequests(t *testing.T) {
	long := strings.Repeat("word ", 401) // 2005 characters
	exact := strings.Repeat("a", 2000)
	cases := []struct {
		name       string
		voices     []string
		noBinary   bool
		disable    bool
		body       string
		wantStatus int
		wantCode   string
		wantDetail map[string]any
	}{
		{"text too long after cleanup", bothVoices, false, false, `{"text":"` + long + `"}`, 413, doctor.SVC024, map[string]any{"max": float64(2000)}},
		{"markdown that shrinks under the limit is fine", bothVoices, false, false, `{"text":"` + strings.Repeat("**a** ", 400) + `"}`, 200, "", nil},
		{"exactly 2000 characters is fine", bothVoices, false, false, `{"text":"` + exact + `"}`, 200, "", nil},
		{"2001 characters", bothVoices, false, false, `{"text":"` + exact + `b"}`, 413, doctor.SVC024, nil},
		{"raw text far over the bound", bothVoices, false, false, `{"text":"` + strings.Repeat("a", MaxRawBytes+1) + `"}`, 413, doctor.SVC024, nil},
		{"empty text", bothVoices, false, false, `{"text":""}`, 400, api.CodeInvalidRequest, map[string]any{"field": "text"}},
		{"text only emoji", bothVoices, false, false, `{"text":"\ud83d\ude00"}`, 400, api.CodeInvalidRequest, map[string]any{"field": "text"}},
		{"missing text", bothVoices, false, false, `{}`, 400, api.CodeInvalidRequest, map[string]any{"field": "text"}},
		{"unknown language", bothVoices, false, false, `{"text":"Hello","language":"fr"}`, 400, api.CodeInvalidRequest, map[string]any{"field": "language"}},
		{"speed too low", bothVoices, false, false, `{"text":"Hello","speed":0.69}`, 400, api.CodeInvalidRequest, map[string]any{"field": "speed"}},
		{"speed too high", bothVoices, false, false, `{"text":"Hello","speed":1.51}`, 400, api.CodeInvalidRequest, map[string]any{"field": "speed"}},
		{"speed zero", bothVoices, false, false, `{"text":"Hello","speed":0}`, 400, api.CodeInvalidRequest, map[string]any{"field": "speed"}},
		{"unknown voice", bothVoices, false, false, `{"text":"Hello","voice":"piper-fr-medium"}`, 400, api.CodeInvalidRequest, map[string]any{"field": "voice", "reason": "unknown_voice"}},
		{"unknown field", bothVoices, false, false, `{"text":"Hello","pitch":2}`, 400, api.CodeInvalidRequest, map[string]any{"field": "pitch"}},
		{"malformed json", bothVoices, false, false, `{"text":`, 400, api.CodeInvalidRequest, nil},
		{"no voice installed", nil, false, false, `{"text":"Hello"}`, 503, doctor.SVC022, nil},
		{"the binary is missing", bothVoices, true, false, `{"text":"Hello"}`, 503, doctor.SVC020, map[string]any{"reason": "not_installed"}},
		{"piper is turned off", bothVoices, false, true, `{"text":"Hello"}`, 503, doctor.SVC022, map[string]any{"reason": "disabled"}},
	}
	for _, c := range cases {
		e := newEnv(t, envOpts{voices: c.voices, noBinary: c.noBinary, scripts: []fake.Script{{Chunks: [][]byte{sine(200, 0.3)}}},
			configure: func(cfg *Config) { cfg.Enabled = !c.disable }})
		resp := e.ttsRaw(context.Background(), c.body, nil)
		apitest.Record(t, resp)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		e.waitDone(1)
		if resp.StatusCode != c.wantStatus {
			t.Errorf("%s: status %d, want %d: %.200s", c.name, resp.StatusCode, c.wantStatus, b)
			continue
		}
		if c.wantStatus == 200 {
			continue
		}
		code, details := errorBody(t, b)
		if code != c.wantCode {
			t.Errorf("%s: code %s, want %s", c.name, code, c.wantCode)
		}
		for k, v := range c.wantDetail {
			if details[k] != v {
				t.Errorf("%s: details[%s] = %v, want %v (%v)", c.name, k, details[k], v, details)
			}
		}
		if got := len(e.streamer.Calls()); got != 0 {
			t.Errorf("%s: %d processes started for a refused request", c.name, got)
		}
	}
}

func TestWrongContentTypeAndMethod(t *testing.T) {
	e := newEnv(t, envOpts{voices: bothVoices})
	req, _ := e.request(context.Background(), http.MethodPost, "/speech/tts", `{"text":"hi there"}`, map[string]string{"Content-Type": "text/plain"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	apitest.Record(t, resp)
	if resp.StatusCode != http.StatusUnsupportedMediaType && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("text/plain: status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	// A request that was refused before the handler never reaches the limiter or the metrics.
	if len(e.metrics.Counters()) > 1 { // localai_maps_tile_bytes_total is always present
		t.Errorf("counters %v", e.metrics.Counters())
	}
}

func TestPiperFailuresBeforeAudioAreJSONErrors(t *testing.T) {
	cases := []struct {
		name       string
		script     fake.Script
		voiceCfg   string // overrides the voice's .onnx.json
		wantReason string
		wantExit   float64
		wantStderr string
	}{
		{"non-zero exit", fake.Script{ExitCode: 3, Stderr: "[piper] error: could not load the model"}, "", ReasonExit, 3, "could not load the model"},
		{"killed by a signal", fake.Script{ExitCode: -1}, "", ReasonExit, -1, ""},
		{"empty output", fake.Script{}, "", ReasonEmpty, 0, ""},
		{"timeout", fake.Script{Outcome: fake.Timeout}, "", ReasonTimeout, 0, ""},
		{"cannot start", fake.Script{StartErr: errors.New("exec format error")}, "", ReasonStart, 0, ""},
		{"one byte is no audio", fake.Script{Chunks: [][]byte{{1}}}, "", ReasonEmpty, 0, ""},
		{"damaged voice config", fake.Script{}, `{"audio":{"sample_rate":5}}`, ReasonBadConfig, 0, ""},
	}
	for _, c := range cases {
		e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{c.script}})
		if c.voiceCfg != "" {
			vo := e.svc.Voices().All()[0]
			if err := writeFile(vo.Config, c.voiceCfg); err != nil {
				t.Fatal(err)
			}
		}
		resp := e.tts(context.Background(), map[string]any{"text": spokenText, "language": "en"}, nil)
		apitest.Record(t, resp)
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		e.waitDone(1)
		if resp.StatusCode != 500 {
			t.Errorf("%s: status %d, want 500: %.200s", c.name, resp.StatusCode, b)
			continue
		}
		code, details := errorBody(t, b)
		if code != doctor.SVC023 || details["reason"] != c.wantReason {
			t.Errorf("%s: %s %v, want LAI-SVC-023 and reason %s", c.name, code, details, c.wantReason)
		}
		if c.wantReason == ReasonExit && details["exit_code"] != c.wantExit {
			t.Errorf("%s: exit_code %v, want %v", c.name, details["exit_code"], c.wantExit)
		}
		if c.wantStderr != "" && !strings.Contains(details["stderr"].(string), c.wantStderr) {
			t.Errorf("%s: stderr %v, want it to hold %q", c.name, details["stderr"], c.wantStderr)
		}
		if _, ok := details["request_id"]; !ok {
			t.Errorf("%s: a 500 carries details.request_id", c.name)
		}
		for _, p := range e.streamer.Procs() {
			if !p.Waited() {
				t.Errorf("%s: the process was not waited for", c.name)
			}
		}
		if strings.Contains(e.logs.String(), spokenMarker) {
			t.Errorf("%s: the text is in the log:\n%s", c.name, e.logs.String())
		}
		if !strings.Contains(e.logs.String(), "tts failed") || !strings.Contains(e.logs.String(), doctor.SVC023) {
			t.Errorf("%s: the failure must be logged with %s:\n%s", c.name, doctor.SVC023, e.logs.String())
		}
		if run, q := e.svc.limiter.Stats(); run != 0 || q != 0 {
			t.Errorf("%s: slot leaked: %d running, %d queued", c.name, run, q)
		}
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }

func jsonBody(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// A failure after audio began cannot be an error response: the connection is aborted, so the client
// sees a truncated stream, not a finished one.
func TestPiperFailureAfterAudioTruncatesTheStream(t *testing.T) {
	pcm := sine(20000, 0.3)
	cases := []struct {
		name   string
		script fake.Script
	}{
		{"non-zero exit", fake.Script{Chunks: [][]byte{pcm}, ExitCode: 1, Stderr: "crashed"}},
		{"timeout", fake.Script{Chunks: [][]byte{pcm}, Outcome: fake.Timeout}},
	}
	for _, c := range cases {
		e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{c.script}})
		resp := e.tts(context.Background(), map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d: audio had started", c.name, resp.StatusCode)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err == nil {
			t.Errorf("%s: the stream ended cleanly with %d bytes; it must look truncated", c.name, len(got))
		}
		e.waitDone(1)
		if !strings.Contains(e.logs.String(), doctor.SVC023) {
			t.Errorf("%s: LAI-SVC-023 must be logged:\n%s", c.name, e.logs.String())
		}
		m := e.metrics.Counters()
		want := `localai_tts_requests_total{voice="piper-en-medium",result="error"}`
		if c.name == "timeout" {
			want = `localai_tts_requests_total{voice="piper-en-medium",result="timeout"}`
		}
		if m[want] != int64(1) {
			t.Errorf("%s: counters %v", c.name, m)
		}
		if !e.streamer.Procs()[0].Waited() {
			t.Errorf("%s: process not waited for", c.name)
		}
	}
}

func TestClientDisconnectKillsPiperAndFreesTheSlot(t *testing.T) {
	pcm := sine(20000, 0.3)
	// The process produces a first chunk and then runs on until it is killed.
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Chunks: [][]byte{pcm}, Outcome: fake.Hang}}})
	ctx, cancel := context.WithCancel(context.Background())
	resp := e.tts(ctx, map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
	// The client has APPLIED the first audio: it read it.
	buf := make([]byte, HeaderSize+100)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("reading the first audio: %v", err)
	}
	cancel() // the client leaves mid-synthesis
	_ = resp.Body.Close()
	e.waitDone(1)

	p := e.streamer.Procs()[0]
	if !p.Killed() || !p.Waited() {
		t.Errorf("killed=%v waited=%v: piper must be killed and reaped when the client leaves", p.Killed(), p.Waited())
	}
	if run, q := e.svc.limiter.Stats(); run != 0 || q != 0 {
		t.Errorf("slot leaked: %d running, %d queued", run, q)
	}
	if got := e.metrics.Counters()[`localai_tts_requests_total{voice="piper-en-medium",result="client_gone"}`]; got != int64(1) {
		t.Errorf("counters %v", e.metrics.Counters())
	}
	if strings.Contains(e.logs.String(), doctor.SVC023) {
		t.Errorf("a client that left is not a piper failure:\n%s", e.logs.String())
	}
}

func TestClientDisconnectBeforeAnyAudioKillsPiper(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	e := newEnv(t, envOpts{voices: bothVoices, scripts: []fake.Script{{Release: hold, Chunks: [][]byte{sine(100, 0.3)}}}})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		b, _ := jsonBody(map[string]any{"text": "Hello there my friend", "language": "en"})
		req, _ := e.request(ctx, http.MethodPost, "/speech/tts", b, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		errc <- err
	}()
	p := <-e.streamer.Started() // piper is running and has produced nothing
	cancel()
	<-errc
	e.waitDone(1)
	if !p.Killed() || !p.Waited() {
		t.Errorf("killed=%v waited=%v", p.Killed(), p.Waited())
	}
	if got := e.metrics.Counters()[`localai_tts_requests_total{voice="piper-en-medium",result="client_gone"}`]; got != int64(1) {
		t.Errorf("counters %v", e.metrics.Counters())
	}
}

func TestSeventhRequestIsRefusedWithRetryAfter(t *testing.T) {
	// Two syntheses run (held), four wait, the seventh request is refused (section 4.3.2).
	hold := make(chan struct{})
	var scripts []fake.Script
	for i := 0; i < 2; i++ {
		scripts = append(scripts, fake.Script{Release: hold, Chunks: [][]byte{sine(200, 0.3)}})
	}
	for i := 0; i < 4; i++ {
		scripts = append(scripts, fake.Script{Chunks: [][]byte{sine(200, 0.3)}})
	}
	e := newEnv(t, envOpts{voices: bothVoices, scripts: scripts})
	queued := make(chan struct{}, 8)
	e.svc.limiter.onWait = func() { queued <- struct{}{} }

	type answer struct {
		status int
		err    error
	}
	answers := make(chan answer, 6)
	send := func() {
		go func() {
			b, _ := jsonBody(map[string]any{"text": "Hello there my friend", "language": "en"})
			req, _ := e.request(context.Background(), http.MethodPost, "/speech/tts", b, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				answers <- answer{0, err}
				return
			}
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			answers <- answer{resp.StatusCode, err}
		}()
	}
	// Two running: wait until both processes started.
	send()
	<-e.streamer.Started()
	send()
	<-e.streamer.Started()
	// Four queued: wait until each is in the queue before sending the next.
	for i := 0; i < 4; i++ {
		send()
		<-queued
	}
	if run, q := e.svc.limiter.Stats(); run != 2 || q != 4 {
		t.Fatalf("limiter: %d running, %d queued, want 2 and 4", run, q)
	}
	// The seventh.
	resp := e.tts(context.Background(), map[string]any{"text": "Hello there my friend", "language": "en"}, nil)
	apitest.Record(t, resp)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("7th request: status %d, want 429: %.200s", resp.StatusCode, b)
	}
	if code, _ := errorBody(t, b); code != doctor.SVC015 {
		t.Errorf("code %s, want LAI-SVC-015", code)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 carries Retry-After")
	}
	e.waitDone(1) // the refused request is counted too
	if got := e.metrics.Counters()[`localai_tts_requests_total{voice="piper-en-medium",result="busy"}`]; got != int64(1) {
		t.Errorf("counters %v", e.metrics.Counters())
	}
	if n := len(e.streamer.Calls()); n != 2 {
		t.Errorf("%d processes started while the slots were held, want 2", n)
	}

	// Release the two running syntheses: the queue drains and all six requests succeed.
	close(hold)
	for i := 0; i < 6; i++ {
		a := <-answers
		if a.err != nil || a.status != 200 {
			t.Errorf("request %d: %d, %v", i, a.status, a.err)
		}
	}
	e.waitDone(6)
	if run, q := e.svc.limiter.Stats(); run != 0 || q != 0 {
		t.Errorf("after everything: %d running, %d queued", run, q)
	}
	for i, p := range e.streamer.Procs() {
		if !p.Waited() {
			t.Errorf("process %d not waited for", i)
		}
	}
}

func TestClientLeavingWhileQueuedLeavesTheQueue(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	e := newEnv(t, envOpts{voices: bothVoices, limiter: NewLimiter(1, 4),
		scripts: []fake.Script{{Release: hold, Chunks: [][]byte{sine(200, 0.3)}}}})
	queued := make(chan struct{}, 4)
	e.svc.limiter.onWait = func() { queued <- struct{}{} }

	go func() {
		b, _ := jsonBody(map[string]any{"text": "Hello there my friend", "language": "en"})
		req, _ := e.request(context.Background(), http.MethodPost, "/speech/tts", b, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-e.streamer.Started()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		b, _ := jsonBody(map[string]any{"text": "Hello there my friend", "language": "en"})
		req, _ := e.request(ctx, http.MethodPost, "/speech/tts", b, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		errc <- err
	}()
	<-queued
	cancel()
	<-errc
	e.waitDone(1) // the queued request finished without ever starting a process
	if got := len(e.streamer.Calls()); got != 1 {
		t.Errorf("%d processes started, want 1", got)
	}
	if _, q := e.svc.limiter.Stats(); q != 0 {
		t.Errorf("%d left in the queue", q)
	}
}

func TestVoicesListsInstalledVoicesWithOneDefaultPerLanguage(t *testing.T) {
	cat := StaticCatalog{
		{ID: "piper-en-low", Language: "en", DisplayName: "English (US), low"},
		{ID: "piper-en-medium", Language: "en", DisplayName: "English (US), medium"},
		{ID: "piper-es-medium", Language: "es", DisplayName: "Spanish (Latin America), medium"},
		{ID: "piper-es-low", Language: "es", DisplayName: "Spanish, low"},
	}
	e := newEnv(t, envOpts{voices: []string{"piper-en-low", "piper-en-medium", "piper-es-medium"}, catalog: cat})
	type row struct {
		ID, Language, DisplayName string
		Default                   bool
	}
	get := func() []row {
		req, _ := e.request(context.Background(), http.MethodGet, "/speech/voices", "", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		apitest.Record(t, resp)
		var out struct {
			Voices []struct {
				ID          string `json:"id"`
				Language    string `json:"language"`
				DisplayName string `json:"display_name"`
				Default     bool   `json:"default"`
			} `json:"voices"`
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		var rows []row
		for _, v := range out.Voices {
			rows = append(rows, row{v.ID, v.Language, v.DisplayName, v.Default})
		}
		return rows
	}
	rows := get()
	want := []row{
		{"piper-en-low", "en", "English (US), low", true},
		{"piper-en-medium", "en", "English (US), medium", false},
		{"piper-es-medium", "es", "Spanish (Latin America), medium", true},
	}
	if len(rows) != len(want) {
		t.Fatalf("voices = %+v, want %+v (verified voices only)", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	// node.yaml's default_voice moves the default; one default per language remains.
	e.setConfig(func(c *Config) { c.DefaultVoice.EN = "piper-en-medium" })
	defaults := map[string]int{}
	for _, r := range get() {
		if r.Default {
			defaults[r.Language]++
			if r.Language == "en" && r.ID != "piper-en-medium" {
				t.Errorf("the configured default is %s", r.ID)
			}
		}
	}
	if defaults["en"] != 1 || defaults["es"] != 1 {
		t.Errorf("defaults per language = %v", defaults)
	}
	// Turned off: an empty list, never null.
	e.setConfig(func(c *Config) { c.Enabled = false })
	if got := get(); len(got) != 0 {
		t.Errorf("disabled: %+v", got)
	}
}

func TestVoicesWithNothingInstalledIsAnEmptyArray(t *testing.T) {
	e := newEnv(t, envOpts{})
	req, _ := e.request(context.Background(), http.MethodGet, "/speech/voices", "", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	apitest.Record(t, resp)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(b)) != `{"voices":[]}` {
		t.Errorf("body = %s", b)
	}
}

func TestHeaderBytesAreLittleEndian(t *testing.T) {
	h, _ := StreamingHeader(22050)
	if binary.LittleEndian.Uint32(h[24:]) != 22050 {
		t.Error("sample rate is not little endian")
	}
}
