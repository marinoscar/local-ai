package checks

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
)

// sineWAV is a mono 16-bit WAV with the streaming header the agent writes (sizes 0xFFFFFFFF), holding
// a sine of the given length and peak amplitude (0 is digital silence).
func sineWAV(t testing.TB, seconds, amp float64) []byte {
	t.Helper()
	const rate = 22050
	hdr, err := piper.StreamingHeader(rate)
	must(t, err)
	n := int(seconds * rate)
	pcm := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(amp * 32767 * math.Sin(2*math.Pi*220*float64(i)/rate))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	return append(hdr, pcm...)
}

type sttSpec struct {
	status   int
	text     string
	language string
	raw      string // a body sent as is
}

// speechAgent serves the two speech routes. stt maps which recording was posted ("en", "es" or
// "other") to the answer; tts maps the requested language to a handler.
type speechAgent struct {
	*fakeAgent
	posted []string // which recordings arrived, in order
	ttsReq []map[string]string
}

func newSpeechAgent(t testing.TB, stt map[string]sttSpec, tts map[string]http.HandlerFunc) *speechAgent {
	a := &speechAgent{fakeAgent: newFakeAgent()}
	en, es := drill.EnglishSample().WAV, drill.SpanishSample().WAV
	a.handle(APIBase+"/speech/stt", func(w http.ResponseWriter, r *http.Request) {
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Errorf("stt Content-Type = %q", r.Header.Get("Content-Type"))
			w.WriteHeader(400)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var audio []byte
		lang := ""
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			must(t, err)
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "audio":
				audio = b
			case "language":
				lang = string(b)
			}
		}
		if lang != "auto" {
			t.Errorf("stt language = %q, want auto", lang)
		}
		which := "other"
		switch {
		case bytes.Equal(audio, en):
			which = "en"
		case bytes.Equal(audio, es):
			which = "es"
		}
		a.posted = append(a.posted, which)
		spec, ok := stt[which]
		if !ok {
			spec = stt["*"]
		}
		if spec.raw != "" {
			text(spec.status, "application/json", spec.raw)(w, r)
			return
		}
		code := spec.status
		if code == 0 {
			code = 200
		}
		jsonOf(code, map[string]any{"text": spec.text, "language": spec.language, "duration_s": 4.0})(w, r)
	})
	a.handle(APIBase+"/speech/tts", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		must(t, json.NewDecoder(r.Body).Decode(&req))
		a.ttsReq = append(a.ttsReq, req)
		h, ok := tts[req["language"]]
		if !ok {
			h = tts["*"]
		}
		h(w, r)
	})
	return a
}

func wavReply(t testing.TB, seconds, amp float64, hdr map[string]string) http.HandlerFunc {
	b := sineWAV(t, seconds, amp)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		_, _ = w.Write(b)
	}
}

const (
	enSentence = "The quick brown fox jumps over the lazy dog."
	esSentence = "El veloz murciélago hindú comía feliz cardillo y kiwi."
)

func TestSTT(t *testing.T) {
	cases := []struct {
		name   string
		lang   string
		spec   sttSpec
		result drill.Result
		code   string
	}{
		{"English, perfect", "en", sttSpec{text: enSentence, language: "en"}, drill.ResultPass, ""},
		{"English, casing and punctuation differ", "en", sttSpec{text: "the quick brown fox jumps over the lazy dog", language: "en"}, drill.ResultPass, ""},
		{"English, two words wrong of nine (0.22)", "en", sttSpec{text: "The quick brown cat jumps over the lazy log.", language: "en"}, drill.ResultPass, ""},
		{"English, three wrong of nine (0.33)", "en", sttSpec{text: "The quick brown cat jumps under the lazy log.", language: "en"}, drill.ResultWarn, doctor.DRL031},
		{"English, four wrong of nine (0.44)", "en", sttSpec{text: "The quick brown cat jumps under the happy log.", language: "en"}, drill.ResultWarn, doctor.DRL031},
		{"English, five wrong of nine (0.56)", "en", sttSpec{text: "A quick brown cat jumps under the happy log.", language: "en"}, drill.ResultFail, doctor.DRL032},
		{"English heard as the wrong language", "en", sttSpec{text: enSentence, language: "es"}, drill.ResultWarn, doctor.DRL031},
		{"English, language tag with a region", "en", sttSpec{text: enSentence, language: "en-US"}, drill.ResultPass, ""},
		{"English, nothing heard", "en", sttSpec{text: "", language: "en"}, drill.ResultFail, doctor.DRL032},
		{"English, whisper down", "en", sttSpec{status: 503, raw: `{"error":{"code":"LAI-SVC-011"}}`}, drill.ResultFail, doctor.DRL032},
		{"English, the route answers a damaged body", "en", sttSpec{status: 200, raw: "{"}, drill.ResultFail, doctor.DRL032},
		{"Spanish, perfect with accents", "es", sttSpec{text: esSentence, language: "es"}, drill.ResultPass, ""},
		{"Spanish, accents lost", "es", sttSpec{text: "El veloz murcielago hindu comia feliz cardillo y kiwi", language: "es"}, drill.ResultPass, ""},
		{"Spanish, small model (4 wrong of nine)", "es", sttSpec{text: "El veloz murcea la bovindu comía feliz cardiño y kiwi.", language: "es"}, drill.ResultWarn, doctor.DRL031},
		{"Spanish, gibberish", "es", sttSpec{text: "thank you for watching", language: "en"}, drill.ResultFail, doctor.DRL032},
		{"Spanish, whisper busy (429)", "es", sttSpec{status: 429, raw: `{}`}, drill.ResultFail, doctor.DRL032},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newSpeechAgent(t, map[string]sttSpec{tc.lang: tc.spec}, nil)
			out := run(t, NewSTTCheck(SpeechDeps{HTTP: a}, tc.lang))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if len(a.posted) != 1 || a.posted[0] != tc.lang {
				t.Errorf("posted = %v, want the %s recording once", a.posted, tc.lang)
			}
		})
	}
}

func TestSTTNumbersAndWiring(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		c := NewSTTCheck(SpeechDeps{}, lang)
		if c.ID != "drl.stt."+lang || !c.Critical || c.Order != 10 || c.FailCode != doctor.DRL032 {
			t.Errorf("%s check = %+v", lang, c)
		}
		if out := run(t, c); out.Result != drill.ResultSkip {
			t.Errorf("%s with nothing wired: %+v", lang, out)
		}
	}
	a := newFakeAgent()
	a.take("/api", true)
	if out := run(t, NewSTTCheck(SpeechDeps{HTTP: a}, "en")); out.Result != drill.ResultFail || out.Code != doctor.DRL032 {
		t.Errorf("agent unreachable: %+v", out)
	}
}

func TestTTS(t *testing.T) {
	cases := []struct {
		name   string
		lang   string
		h      func(testing.TB) http.HandlerFunc
		result drill.Result
		code   string
	}{
		{"English voice", "en", func(t testing.TB) http.HandlerFunc {
			return wavReply(t, 2.1, 0.5, map[string]string{piper.HeaderVoice: "en_US-lessac-medium", piper.HeaderLanguage: "en"})
		}, drill.ResultPass, ""},
		{"Spanish voice", "es", func(t testing.TB) http.HandlerFunc { return wavReply(t, 2.6, 0.4, nil) }, drill.ResultPass, ""},
		{"a fallback voice spoke", "es", func(t testing.TB) http.HandlerFunc {
			return wavReply(t, 2.6, 0.4, map[string]string{piper.HeaderFallback: "true"})
		}, drill.ResultWarn, doctor.DRL033},
		{"shortest accepted", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 0.81, 0.5, nil) }, drill.ResultPass, ""},
		{"too short", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 0.5, 0.5, nil) }, drill.ResultFail, doctor.DRL034},
		{"longest accepted", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 7.9, 0.5, nil) }, drill.ResultPass, ""},
		{"too long", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 8.5, 0.5, nil) }, drill.ResultFail, doctor.DRL034},
		{"digital silence", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 2, 0, nil) }, drill.ResultFail, doctor.DRL034},
		{"too quiet (about -52 dBFS)", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 2, 0.0035, nil) }, drill.ResultFail, doctor.DRL034},
		{"quiet but audible (about -34 dBFS)", "en", func(t testing.TB) http.HandlerFunc { return wavReply(t, 2, 0.03, nil) }, drill.ResultPass, ""},
		{"a fallback voice that is silent still fails", "en", func(t testing.TB) http.HandlerFunc {
			return wavReply(t, 2, 0, map[string]string{piper.HeaderFallback: "true"})
		}, drill.ResultFail, doctor.DRL034},
		{"not a WAV", "en", func(testing.TB) http.HandlerFunc { return text(200, "audio/wav", "not audio at all") }, drill.ResultFail, doctor.DRL034},
		{"the voice failed", "en", func(testing.TB) http.HandlerFunc { return status(503) }, drill.ResultFail, doctor.DRL034},
		{"no voice installed", "es", func(testing.TB) http.HandlerFunc { return status(503) }, drill.ResultFail, doctor.DRL034},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newSpeechAgent(t, nil, map[string]http.HandlerFunc{tc.lang: tc.h(t)})
			out := run(t, NewTTSCheck(SpeechDeps{HTTP: a}, tc.lang))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			wantText := "Test of the emergency voice."
			if tc.lang == "es" {
				wantText = "Prueba de la voz de emergencia."
			}
			if len(a.ttsReq) != 1 || a.ttsReq[0]["text"] != wantText || a.ttsReq[0]["language"] != tc.lang {
				t.Errorf("tts request = %v", a.ttsReq)
			}
		})
	}
}

func TestTTSNumbersAndWiring(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		c := NewTTSCheck(SpeechDeps{}, lang)
		if c.ID != "drl.tts."+lang || !c.Critical || c.Order != 11 || c.FailCode != doctor.DRL034 {
			t.Errorf("%s check = %+v", lang, c)
		}
		if out := run(t, c); out.Result != drill.ResultSkip {
			t.Errorf("%s with nothing wired: %+v", lang, out)
		}
	}
	a := newFakeAgent()
	a.take("/api", true)
	if out := run(t, NewTTSCheck(SpeechDeps{HTTP: a}, "en")); out.Result != drill.ResultFail || out.Code != doctor.DRL034 {
		t.Errorf("agent unreachable: %+v", out)
	}
}

func TestRoundtrip(t *testing.T) {
	good := wavReply(t, 2, 0.5, nil)
	cases := []struct {
		name   string
		stt    sttSpec
		tts    http.HandlerFunc
		result drill.Result
		code   string
	}{
		{"the sentence comes back", sttSpec{text: "Test of the emergency voice.", language: "en"}, good, drill.ResultPass, ""},
		{"one word of five lost (0.2)", sttSpec{text: "Test of the voice.", language: "en"}, good, drill.ResultPass, ""},
		{"two words of five lost (0.4)", sttSpec{text: "Test the voice.", language: "en"}, good, drill.ResultWarn, doctor.DRL031},
		{"nothing comes back", sttSpec{text: "", language: "en"}, good, drill.ResultWarn, doctor.DRL031},
		{"recognition fails", sttSpec{status: 503, raw: "{}"}, good, drill.ResultWarn, doctor.DRL032},
		{"synthesis fails", sttSpec{text: "x", language: "en"}, status(500), drill.ResultWarn, doctor.DRL034},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newSpeechAgent(t, map[string]sttSpec{"*": tc.stt}, map[string]http.HandlerFunc{"en": tc.tts})
			out := run(t, NewRoundtripCheck(SpeechDeps{HTTP: a}))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if tc.name == "the sentence comes back" && (len(a.posted) != 1 || a.posted[0] != "other") {
				t.Errorf("the synthesised audio was not what was transcribed: %v", a.posted)
			}
		})
	}
	c := NewRoundtripCheck(SpeechDeps{})
	if c.ID != "drl.speech.roundtrip" || c.Critical || c.Order != 12 {
		t.Errorf("check = %+v", c)
	}
	if out := run(t, c); out.Result != drill.ResultSkip {
		t.Errorf("nothing wired: %+v", out)
	}
	un := newFakeAgent()
	un.take("/api", true)
	if out := run(t, NewRoundtripCheck(SpeechDeps{HTTP: un})); out.Result != drill.ResultWarn {
		t.Errorf("unreachable must only warn: %+v", out)
	}
}
