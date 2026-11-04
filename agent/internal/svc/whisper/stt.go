package whisper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// POST /api/v1/speech/stt (companion-services.md section 3.4 and 4.2.5, agent.md section 3.8.25).
// The transcript is private: it is returned to the caller and never logged.

// Route settings.
const (
	// STTTimeout is the handler deadline of POST /speech/stt.
	STTTimeout = 120 * time.Second
	// UILanguageHeader is the language of the Portal, which the fix strings follow.
	UILanguageHeader = "X-LocalAI-UI-Language"
	// MaxPromptChars is the longest initial prompt.
	MaxPromptChars = 200
	// DefaultMaxAudioSeconds is whisper.max_audio_seconds when node.yaml has none.
	DefaultMaxAudioSeconds = 120
	// DefaultPort is whisper-server's port when node.yaml has none.
	DefaultPort = 8081

	maxFieldBytes = 4096
)

// Results of a request, the "result" label of localai_stt_requests_total.
const (
	resultOK       = "ok"
	resultBadAudio = "bad_audio"
	resultBusy     = "busy"
	resultTimeout  = "timeout"
	resultError    = "error"
)

// Messages that replace the registry text of a code in one situation. Silence gets its own sentence
// (companion-services.md section 4.2.5, step 4).
var silenceMessage = map[string]string{
	"en": "I couldn't hear anything. Try again closer to the microphone.",
	"es": "No pude oír nada. Intente otra vez más cerca del micrófono.",
}

// STTConfig is the part of the whisper block of node.yaml the route reads.
type STTConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Model           string `yaml:"model"`
	MaxAudioSeconds int    `yaml:"max_audio_seconds"`
}

// DefaultSTTConfig is the block as written when node.yaml has none.
func DefaultSTTConfig() STTConfig {
	return STTConfig{Enabled: true, Model: "auto", MaxAudioSeconds: DefaultMaxAudioSeconds}
}

// STTConfigFrom reads the whisper block of a loaded node.yaml (defaults for what it leaves out).
func STTConfigFrom(cfg config.Config) STTConfig {
	c := DefaultSTTConfig()
	if raw, ok := cfg.Blocks["whisper"]; ok {
		if err := raw.Decode(&c); err != nil {
			return DefaultSTTConfig() // the validator already refused such a file
		}
	}
	if c.MaxAudioSeconds <= 0 {
		c.MaxAudioSeconds = DefaultMaxAudioSeconds
	}
	return c
}

// STTDeps are what the route needs. Everything is optional.
type STTDeps struct {
	// Config returns the whisper block as it is now; default DefaultSTTConfig.
	Config func() STTConfig
	// Port returns ports.whisper; default DefaultPort. BaseURL, when set, wins (tests).
	Port    func() int
	BaseURL func() string
	// HTTP is the client to whisper-server; default a plain client without a proxy.
	HTTP Doer
	// Timeout is the time whisper gets; default ClientTimeout (60 s).
	Timeout time.Duration
	// ModelID returns the id of the model whisper-server runs ("" when none is known). When it is "",
	// whisper.model of node.yaml is used if it names an id.
	ModelID func() string
	// Queue bounds the requests; default 1 in flight and 2 waiting.
	Queue *Queue
	// Now is the clock; default time.Now.
	Now func() time.Time
	// Metrics receives localai_stt_requests_total and localai_stt_seconds; nil ignores them.
	Metrics *health.Metrics
	// Logger receives one line per request (never the transcript); default slog.Default().
	Logger *slog.Logger
}

// STTService is the speech-to-text route.
type STTService struct {
	cfg     func() STTConfig
	client  *Client
	modelID func() string
	queue   *Queue
	now     func() time.Time
	metrics *health.Metrics
	log     *slog.Logger

	// afterRequest is called when a request has finished and been counted. Tests use it to know the
	// handler is done without sleeping; nothing else sets it.
	afterRequest func()
}

// NewSTT returns the service.
func NewSTT(d STTDeps) *STTService {
	s := &STTService{cfg: d.Config, modelID: d.ModelID, queue: d.Queue, now: d.Now, metrics: d.Metrics, log: d.Logger}
	if s.cfg == nil {
		s.cfg = DefaultSTTConfig
	}
	if s.queue == nil {
		s.queue = NewQueue(DefaultInFlight, DefaultWaiting)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "whisper")
	base := d.BaseURL
	if base == nil {
		port := d.Port
		base = func() string {
			p := DefaultPort
			if port != nil {
				if v := port(); v > 0 {
					p = v
				}
			}
			return fmt.Sprintf("http://127.0.0.1:%d", p)
		}
	}
	s.client = &Client{BaseURL: base, HTTP: d.HTTP, Timeout: d.Timeout}
	return s
}

// Routes returns POST /speech/stt for the operator and the Portal.
func (s *STTService) Routes() []api.Route {
	return []api.Route{{
		Method: http.MethodPost, Pattern: "/speech/stt", Auth: []api.Level{api.AuthOp, api.AuthPortal},
		Timeout: STTTimeout, MaxBody: api.SpeechMaxBody, MediaTypes: []string{"multipart/form-data"},
		Handler: http.HandlerFunc(s.serve),
	}}
}

// STTRoutes returns the routes of a service over d (the OpenAPI conformance test uses it with empty
// dependencies: only the metadata is compared).
func STTRoutes(d STTDeps) []api.Route { return NewSTT(d).Routes() }

// Queue returns the request queue (for tests and the health view).
func (s *STTService) Queue() *Queue { return s.queue }

// sttResponse is the body of a 200.
type sttResponse struct {
	Text                string   `json:"text"`
	Language            string   `json:"language"`
	LanguageProbability *float64 `json:"language_probability,omitempty"`
	DurationS           float64  `json:"duration_s"`
	Model               string   `json:"model"`
	ElapsedMS           int64    `json:"elapsed_ms"`
}

func invalid(field, reason string) *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}

func badAudio(status int, details map[string]any) *api.Error {
	return api.NewError(doctor.SVC014, status, details)
}

// uiLanguage reduces X-LocalAI-UI-Language ("es", "es-MX", "en-US,en;q=0.9") to "en", "es" or "".
func uiLanguage(h string) string {
	if i := strings.IndexAny(h, ",;"); i >= 0 {
		h = h[:i]
	}
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.IndexAny(h, "-_"); i >= 0 {
		h = h[:i]
	}
	if h == "en" || h == "es" {
		return h
	}
	return ""
}

// form is what a request carries.
type form struct {
	audio    []byte
	language string
	prompt   string
}

// readForm reads the multipart body: audio, language and prompt. Other fields are ignored.
func readForm(w http.ResponseWriter, r *http.Request) (form, error) {
	var f form
	body := http.MaxBytesReader(w, r.Body, api.SpeechMaxBody)
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return f, invalid("body", "bad_multipart")
	}
	mr := multipart.NewReader(body, params["boundary"])
	haveAudio := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return f, bodyError(err)
		}
		switch part.FormName() {
		case "audio":
			if haveAudio {
				return f, invalid("audio", "duplicate")
			}
			haveAudio = true
			if f.audio, err = io.ReadAll(part); err != nil {
				return f, bodyError(err)
			}
		case "language":
			v, err := readField(part)
			if err != nil {
				return f, err
			}
			f.language = strings.TrimSpace(v)
		case "prompt":
			v, err := readField(part)
			if err != nil {
				return f, err
			}
			f.prompt = strings.TrimSpace(v)
		default:
			_, _ = io.Copy(io.Discard, part)
		}
	}
	if !haveAudio {
		return f, badAudio(http.StatusBadRequest, map[string]any{"reason": ReasonEmpty, "field": "audio"})
	}
	return f, nil
}

// readField reads a small text field.
func readField(part io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
	if err != nil {
		return "", bodyError(err)
	}
	if len(b) > maxFieldBytes {
		return "", invalid("body", "field_too_long")
	}
	return string(b), nil
}

// bodyError maps a failure of reading the body: over the limit is 413 LAI-SVC-014, anything else a bad
// request.
func bodyError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return badAudio(http.StatusRequestEntityTooLarge, map[string]any{"reason": "too_large", "limit": tooBig.Limit})
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return invalid("body", "bad_multipart")
}

func (s *STTService) model(cfg STTConfig) string {
	if s.modelID != nil {
		if id := strings.TrimSpace(s.modelID()); id != "" {
			return id
		}
	}
	if m := strings.TrimSpace(cfg.Model); m != "" && m != "auto" {
		return m
	}
	return ""
}

// serve is POST /speech/stt.
func (s *STTService) serve(w http.ResponseWriter, r *http.Request) {
	began := s.now()
	result := resultError
	defer func() {
		s.metrics.STTRequest(result)
		s.metrics.STTSeconds(s.now().Sub(began).Seconds())
		if s.afterRequest != nil {
			s.afterRequest()
		}
	}()
	info := api.InfoFrom(r.Context())
	if l := uiLanguage(r.Header.Get(UILanguageHeader)); l != "" {
		info.Lang = l // the fix strings follow the Portal's language
	}
	fail := func(res string, err error) {
		result = res
		api.WriteError(w, r, err)
	}

	cfg := s.cfg()
	if !cfg.Enabled {
		fail(resultError, api.NewError(doctor.SVC011, http.StatusServiceUnavailable, map[string]any{"reason": "disabled"}))
		return
	}
	leave, err := s.queue.Enter()
	if err != nil {
		e := api.NewError(doctor.SVC015, http.StatusTooManyRequests, nil)
		e.RetryAfter = 3
		fail(resultBusy, e)
		return
	}
	defer leave()

	f, err := readForm(w, r)
	if err != nil {
		var ae *api.Error
		res := resultError
		if errors.As(err, &ae) {
			res = resultBadAudio
		}
		fail(res, err)
		return
	}
	language := f.language
	switch {
	case language == "":
		language = "auto"
	case !ValidLanguage(language):
		fail(resultBadAudio, invalid("language", "must be auto or a language code such as en"))
		return
	}
	if utf8.RuneCountInString(f.prompt) > MaxPromptChars {
		fail(resultBadAudio, invalid("prompt", "must be at most 200 characters"))
		return
	}

	pcm, audioSeconds, err := prepare(f.audio, float64(cfg.MaxAudioSeconds))
	if err != nil {
		fail(resultBadAudio, s.audioError(err, info.Lang))
		return
	}

	release, err := s.queue.Slot(r.Context())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return // the caller left
		}
		fail(resultError, err)
		return
	}
	defer release()

	started := s.now()
	res, err := s.client.Transcribe(r.Context(), EncodeWAV16(pcm, WhisperRate), language, f.prompt)
	if err != nil {
		var ce *ClientError
		switch {
		case errors.As(err, &ce) && ce.Kind == KindTimeout:
			s.log.Warn("stt timed out", "audio_s", round2(audioSeconds))
			fail(resultTimeout, api.NewError(doctor.SVC016, http.StatusGatewayTimeout, nil))
		case errors.As(err, &ce):
			code := doctor.SVC011
			if s.model(cfg) == "" {
				code = doctor.SVC010
			}
			d := map[string]any{"reason": ce.Reason}
			if ce.Status != 0 {
				d["status"] = ce.Status
			}
			s.log.Warn("stt failed", "code", code, "reason", ce.Reason, "status", ce.Status)
			fail(resultError, api.NewError(code, http.StatusServiceUnavailable, d))
		case errors.Is(err, context.Canceled):
			result = resultError // the caller left; nothing to answer
		default:
			fail(resultError, err)
		}
		return
	}

	elapsed := s.now().Sub(started)
	model := s.model(cfg)
	if model == "" {
		model = "whisper"
	}
	dur := res.Seconds
	if dur <= 0 {
		dur = audioSeconds
	}
	s.log.Info("stt done", "audio_s", round2(dur), "language", res.Language, "elapsed_ms", elapsed.Milliseconds(), "model", model)
	result = resultOK
	api.WriteJSON(w, r, http.StatusOK, sttResponse{
		Text: res.Text, Language: res.Language, LanguageProbability: res.Probability,
		DurationS: round2(dur), Model: model, ElapsedMS: elapsed.Milliseconds(),
	})
}

// prepare decodes the upload and returns the 16 kHz mono PCM16 for whisper and the recording's length.
func prepare(b []byte, maxSeconds float64) ([]int16, float64, error) {
	if len(b) == 0 {
		return nil, 0, bad(ReasonEmpty)
	}
	a, err := DecodeWAV(b, maxSeconds)
	if err != nil {
		return nil, 0, err
	}
	if a.RMSDBFS() < SilenceDBFS {
		return nil, 0, bad(ReasonSilent)
	}
	return Resample16k(a.Samples, a.Rate), a.Seconds(), nil
}

// audioError turns a decoder failure into the 400 LAI-SVC-014; silence carries its own sentence.
func (s *STTService) audioError(err error, lang string) *api.Error {
	var fe *FormatError
	if !errors.As(err, &fe) {
		return badAudio(http.StatusBadRequest, nil)
	}
	d := map[string]any{"reason": fe.Reason}
	if fe.Detail != 0 {
		d["value"] = fe.Detail
	}
	e := badAudio(http.StatusBadRequest, d)
	if fe.Reason == ReasonSilent {
		msg, ok := silenceMessage[lang]
		if !ok {
			msg = silenceMessage["en"]
		}
		return e.WithMessage(msg, "")
	}
	return e
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// EnvModelID reads WHISPER_MODEL_ID from whisper.env (companion-services.md section 4.2.2), "" when the
// file or the key is missing. readFile defaults to os.ReadFile.
func EnvModelID(path string, readFile func(string) ([]byte, error)) string {
	if readFile == nil {
		readFile = os.ReadFile
	}
	b, err := readFile(path)
	if err != nil {
		return ""
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		k, v, ok := strings.Cut(strings.TrimSpace(string(line)), "=")
		if ok && strings.TrimSpace(k) == "WHISPER_MODEL_ID" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
