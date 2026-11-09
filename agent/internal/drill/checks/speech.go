package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
)

// Doctor codes of the speech checks.
const (
	// CodeSTTInaccurate: speech recognition is less accurate than expected (LAI-DRL-031).
	CodeSTTInaccurate = doctor.DRL031
	// CodeSTTFailed: speech recognition failed (LAI-DRL-032).
	CodeSTTFailed = doctor.DRL032
	// CodeTTSFallback: a language has no voice of its own (LAI-DRL-033).
	CodeTTSFallback = doctor.DRL033
	// CodeTTSFailed: the voice failed (LAI-DRL-034).
	CodeTTSFailed = doctor.DRL034
)

// The thresholds of drill.md section 4.2 checks 10 to 12.
const (
	// sttPassWER and sttWarnWER bound the word error rate of the recordings: at most 0.25 passes, at
	// most 0.5 warns, more fails.
	sttPassWER = 0.25
	sttWarnWER = 0.5
	// roundtripPassWER and roundtripWarnWER are the bounds of check 12: at most 0.3 passes; above that
	// it only warns (it is informative).
	roundtripPassWER = 0.3
	// ttsMinSeconds, ttsMaxSeconds and ttsMinDBFS bound the synthesised sentence.
	ttsMinSeconds = 0.8
	ttsMaxSeconds = 8.0
	ttsMinDBFS    = -40.0
)

// The sentences the voices say (the English one is also the roundtrip's).
const (
	ttsTextEN = "Test of the emergency voice."
	ttsTextES = "Prueba de la voz de emergencia."
)

// SpeechDeps are what the speech checks need.
type SpeechDeps struct {
	HTTP AgentHTTP
	// English and Spanish are the recordings of checks 10; the zero value means the embedded ones.
	English, Spanish drill.Sample
}

func (d SpeechDeps) samples() (drill.Sample, drill.Sample) {
	en, es := d.English, d.Spanish
	if len(en.WAV) == 0 {
		en = drill.EnglishSample()
	}
	if len(es.WAV) == 0 {
		es = drill.SpanishSample()
	}
	return en, es
}

// ---- the two routes ----------------------------------------------------------------------------------

type sttAnswer struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// transcribe posts a recording to POST /speech/stt with language auto, as the Portal's Talk does.
func transcribe(ctx context.Context, c AgentHTTP, wav []byte, name string) (sttAnswer, int, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="audio"; filename="`+name+`"`)
	h.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(h)
	if err != nil {
		return sttAnswer{}, 0, err
	}
	if _, err := part.Write(wav); err != nil {
		return sttAnswer{}, 0, err
	}
	if err := mw.WriteField("language", "auto"); err != nil {
		return sttAnswer{}, 0, err
	}
	if err := mw.Close(); err != nil {
		return sttAnswer{}, 0, err
	}
	hdr := http.Header{}
	hdr.Set("Content-Type", mw.FormDataContentType())
	hdr.Set(whisper.UILanguageHeader, "en")
	rp, err := callAgent(ctx, c, http.MethodPost, APIBase+"/speech/stt", hdr, &body, 1<<20)
	if err != nil {
		return sttAnswer{}, 0, err
	}
	if rp.Status != http.StatusOK {
		return sttAnswer{}, rp.Status, nil
	}
	var a sttAnswer
	if err := json.Unmarshal(rp.Body, &a); err != nil {
		return sttAnswer{}, rp.Status, err
	}
	return a, rp.Status, nil
}

// speech is a decoded synthesis.
type speech struct {
	WAV      []byte
	Seconds  float64
	DBFS     float64
	Voice    string
	Fallback bool
}

// ttsFailure says why a synthesis is unusable.
type ttsFailure struct {
	Reason string
	Status int
	Err    error
}

// synthesize posts a sentence to POST /speech/tts and measures the WAV it gets back.
func synthesize(ctx context.Context, c AgentHTTP, text, lang string) (speech, *ttsFailure) {
	b, _ := json.Marshal(map[string]string{"text": text, "language": lang})
	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set(piper.UILanguageHeader, lang)
	rp, err := callAgent(ctx, c, http.MethodPost, APIBase+"/speech/tts", hdr, bytes.NewReader(b), 4<<20)
	switch {
	case err != nil:
		return speech{}, &ttsFailure{Reason: "request_failed", Err: err}
	case rp.Status != http.StatusOK:
		return speech{}, &ttsFailure{Reason: "http_status", Status: rp.Status}
	}
	a, err := whisper.DecodeWAV(rp.Body, 0)
	if err != nil {
		return speech{}, &ttsFailure{Reason: "bad_wav", Err: err}
	}
	return speech{
		WAV: rp.Body, Seconds: a.Seconds(), DBFS: a.RMSDBFS(),
		Voice: rp.Header.Get(piper.HeaderVoice), Fallback: rp.Header.Get(piper.HeaderFallback) != "",
	}, nil
}

func (f *ttsFailure) details() map[string]any {
	d := map[string]any{"reason": f.Reason}
	if f.Status != 0 {
		d["status"] = f.Status
	}
	if f.Err != nil {
		d["error"] = reasonOf(f.Err)
	}
	return d
}

// round3 keeps a rate in the document short.
func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// primaryLang reduces "en", "en-US" and "English-ish" tags to the two-letter code.
func primaryLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	return l
}

// ---- checks 10: drl.stt.en, drl.stt.es -----------------------------------------------------------------

// NewSTTCheck is check 10 for one recording, drl.stt.<lang> (critical): the recording goes through
// POST /speech/stt with language auto, and the transcript is compared with what it says.
//
//	pass: WER at most 0.25 and the language recognised is the recording's.
//	warn: WER at most 0.5 (or the right words in the wrong language) (LAI-DRL-031).
//	fail: an error, or WER above 0.5 (LAI-DRL-032).
func NewSTTCheck(d SpeechDeps, lang string) drill.Check {
	return drill.Check{
		ID: "drl.stt." + lang, Area: "speech", Critical: true, Order: orderSTT,
		Timeout: speechTimeout, FailCode: CodeSTTFailed,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			if d.HTTP == nil {
				return contentSkip("not_available", nil)
			}
			en, es := d.samples()
			s := en
			if lang == "es" {
				s = es
			}
			ans, status, err := transcribe(ctx, d.HTTP, s.WAV, s.Language+".wav")
			switch {
			case err != nil:
				return contentFail(CodeSTTFailed, map[string]any{"reason": "request_failed", "error": reasonOf(err)})
			case status != http.StatusOK:
				return contentFail(CodeSTTFailed, map[string]any{"reason": statusReason(status)})
			}
			wer := drill.WER(s.Text, ans.Text)
			langOK := primaryLang(ans.Language) == s.Language
			details := map[string]any{"wer": round3(wer), "language": ans.Language, "expected_language": s.Language}
			switch {
			case wer <= sttPassWER && langOK:
				return contentPass(details)
			case wer <= sttWarnWER:
				return contentWarn(CodeSTTInaccurate, details)
			}
			return contentFail(CodeSTTFailed, details)
		},
	}
}

// ---- check 11: drl.tts.en, drl.tts.es ------------------------------------------------------------------

// NewTTSCheck is check 11 for one language, drl.tts.<lang> (critical): the emergency sentence goes
// through POST /speech/tts and the WAV is measured.
//
//	pass: the WAV decodes, lasts 0.8 to 8 s, is louder than -40 dBFS and no fallback voice spoke.
//	warn: a fallback voice spoke (LAI-DRL-033): the language has no voice of its own.
//	fail: an error, or a WAV that is broken, too short, too long or silent (LAI-DRL-034).
func NewTTSCheck(d SpeechDeps, lang string) drill.Check {
	text := ttsTextEN
	if lang == "es" {
		text = ttsTextES
	}
	return drill.Check{
		ID: "drl.tts." + lang, Area: "speech", Critical: true, Order: orderTTS,
		Timeout: speechTimeout, FailCode: CodeTTSFailed,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			if d.HTTP == nil {
				return contentSkip("not_available", nil)
			}
			sp, fail := synthesize(ctx, d.HTTP, text, lang)
			if fail != nil {
				return contentFail(CodeTTSFailed, fail.details())
			}
			details := map[string]any{"seconds": round3(sp.Seconds), "dbfs": round3(sp.DBFS), "voice": sp.Voice}
			switch {
			case sp.Seconds < ttsMinSeconds:
				details["reason"] = "too_short"
				return contentFail(CodeTTSFailed, details)
			case sp.Seconds > ttsMaxSeconds:
				details["reason"] = "too_long"
				return contentFail(CodeTTSFailed, details)
			case !(sp.DBFS > ttsMinDBFS):
				details["reason"] = "silent"
				return contentFail(CodeTTSFailed, details)
			case sp.Fallback:
				details["fallback"] = true
				return contentWarn(CodeTTSFallback, details)
			}
			return contentPass(details)
		},
	}
}

// ---- check 12: drl.speech.roundtrip --------------------------------------------------------------------

// NewRoundtripCheck is check 12, drl.speech.roundtrip: the English sentence of check 11 is
// synthesised and transcribed again. It is informative and never fails.
//
//	pass: WER at most 0.3.
//	warn: anything else, including a leg that errors.
func NewRoundtripCheck(d SpeechDeps) drill.Check {
	return drill.Check{
		ID: "drl.speech.roundtrip", Area: "speech", Order: orderRoundtrip, Timeout: speechTimeout,
		Run: func(ctx context.Context, _ *drill.Env) drill.Outcome {
			if d.HTTP == nil {
				return contentSkip("not_available", nil)
			}
			sp, fail := synthesize(ctx, d.HTTP, ttsTextEN, "en")
			if fail != nil {
				det := fail.details()
				det["leg"] = "tts"
				return contentWarn(CodeTTSFailed, det)
			}
			ans, status, err := transcribe(ctx, d.HTTP, sp.WAV, "roundtrip.wav")
			switch {
			case err != nil:
				return contentWarn(CodeSTTFailed, map[string]any{"leg": "stt", "reason": "request_failed", "error": reasonOf(err)})
			case status != http.StatusOK:
				return contentWarn(CodeSTTFailed, map[string]any{"leg": "stt", "reason": statusReason(status)})
			}
			wer := drill.WER(ttsTextEN, ans.Text)
			details := map[string]any{"wer": round3(wer), "language": ans.Language}
			if wer <= roundtripPassWER {
				return contentPass(details)
			}
			return contentWarn(CodeSTTInaccurate, details)
		},
	}
}
