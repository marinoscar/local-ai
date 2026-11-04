package whisper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// STTTimeout is the handler deadline of POST /speech/stt (agent.md section 3.8.25).
const STTTimeout = 120 * time.Second

// Limits of a request (companion-services.md section 3.4).
const (
	// DefaultMaxAudioSeconds is whisper.max_audio_seconds when node.yaml does not say.
	DefaultMaxAudioSeconds = 120
	// MaxPromptChars is the longest initial prompt.
	MaxPromptChars = 200
	// maxFieldBytes bounds the small text fields of the form.
	maxFieldBytes = 4 << 10
)

// Results of a request, the "result" label of localai_stt_requests_total.
const (
	resultOK       = "ok"
	resultBadAudio = "bad_audio"
	resultBusy     = "busy"
	resultTimeout  = "timeout"
	resultError    = "error"
)

// Silence messages (companion-services.md section 4.2.5); the fix stays the one of LAI-SVC-014.
const (
	silentEN = "I couldn't hear anything. Try again closer to the microphone."
	silentES = "No se oyó nada. Intente otra vez más cerca del micrófono."
)

var languageRE = regexp.MustCompile(`^(?:[a-z]{2,3}|auto)$`)

// Deps are what the STT route needs.
type Deps struct {
	// Config returns the whisper block of node.yaml as it is now.
	Config func() Config
	// Model returns the id of the model the unit serves; "" means none is installed (LAI-SVC-010).
	Model func() string
	// Port returns ports.whisper; default 8081.
	Port func() int
	// Client calls whisper-server; the default talks to 127.0.0.1:Port().
	Client *Client
	// Queue bounds the requests; default 1 in flight and 2 waiting.
	Queue *Queue
	// Metrics receives localai_stt_requests_total and localai_stt_seconds; nil ignores them.
	Metrics *health.Metrics
	// Logger receives one line per request (never the transcript); default slog.Default().
	Logger *slog.Logger
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// STT is the speech-to-text service.
type STT struct {
	cfg     func() Config
	model   func() string
	client  *Client
	queue   *Queue
	metrics *health.Metrics
	log     *slog.Logger
	now     func() time.Time
}

// NewSTT returns the service.
func NewSTT(d Deps) *STT {
	s := &STT{cfg: d.Config, model: d.Model, client: d.Client, queue: d.Queue, metrics: d.Metrics, log: d.Logger, now: d.Now}
	if s.cfg == nil {
		s.cfg = func() Config { return Config{} }
	}
	if s.model == nil {
		s.model = func() string { return "" }
	}
	if s.queue == nil {
		s.queue = NewQueue(MaxInFlight, MaxWaiting)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "whisper")
	if s.client == nil {
		port := d.Port
		s.client = &Client{BaseURL: func() string {
			p := 8081
			if port != nil {
				p = port()
			}
			return fmt.Sprintf("http://127.0.0.1:%d", p)
		}}
	}
	return s
}

// Routes returns POST /speech/stt for the operator and the Portal.
func (s *STT) Routes() []api.Route {
	return []api.Route{{
		Method: http.MethodPost, Pattern: "/speech/stt", Auth: []api.Level{api.AuthOp, api.AuthPortal},
		Timeout: STTTimeout, MaxBody: api.SpeechMaxBody, MediaTypes: []string{"multipart/form-data"},
		Handler: http.HandlerFunc(s.serveSTT),
	}}
}

// STTRoutes returns the routes of a service over d (the OpenAPI conformance test uses it with empty
// dependencies: only the metadata is compared).
func STTRoutes(d Deps) []api.Route { return NewSTT(d).Routes() }

// form is a parsed request.
type form struct {
	audio    []byte
	language string
	prompt   string
}

func badAudio(reason string, status int) *api.Error {
	return api.NewError(doctor.SVC014, status, map[string]any{"reason": reason})
}

func invalid(field, reason string) *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}

// readForm reads the multipart body into memory (no temp files). The body is already capped at
// SpeechMaxBody; going over it is 413.
func readForm(w http.ResponseWriter, r *http.Request) (form, error) {
	f := form{language: "auto"}
	r.Body = http.MaxBytesReader(w, r.Body, api.SpeechMaxBody)
	mr, err := r.MultipartReader()
	if err != nil {
		return f, invalid("body", "not_multipart")
	}
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
			b, err := io.ReadAll(part)
			if err != nil {
				return f, bodyError(err)
			}
			f.audio, haveAudio = b, true
		case "language", "prompt":
			b, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
			if err != nil {
				return f, bodyError(err)
			}
			if len(b) > maxFieldBytes {
				return f, invalid(part.FormName(), "too_long")
			}
			if part.FormName() == "language" {
				if v := strings.TrimSpace(string(b)); v != "" {
					f.language = v
				}
			} else {
				f.prompt = string(b)
			}
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, api.SpeechMaxBody))
		}
	}
	if !haveAudio {
		return f, invalid("audio", "missing")
	}
	return f, nil
}

// bodyError maps a read failure: over the limit is 413 LAI-SVC-014, anything else a malformed form.
func bodyError(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return badAudio("too_large", http.StatusRequestEntityTooLarge)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return invalid("body", "malformed_multipart")
}

// wavError maps a decoder refusal to 400 LAI-SVC-014.
func wavError(err error) *api.Error {
	reason := "unsupported"
	switch {
	case errors.Is(err, ErrNotWAV):
		reason = "not_wav"
	case errors.Is(err, ErrEmptyAudio):
		reason = "empty"
	case errors.Is(err, ErrTooLong):
		reason = "too_long"
	}
	return badAudio(reason, http.StatusBadRequest)
}

// serveSTT is POST /speech/stt.
func (s *STT) serveSTT(w http.ResponseWriter, r *http.Request) {
	began := s.now()
	result := resultError
	defer func() {
		s.metrics.STTRequest(result)
		s.metrics.STTSeconds(s.now().Sub(began).Seconds())
	}()
	fail := func(res string, err error) {
		result = res
		api.WriteError(w, r, err)
	}

	f, err := readForm(w, r)
	if err != nil {
		res := resultError
		var ae *api.Error
		if errors.As(err, &ae) && ae.Code == doctor.SVC014 {
			res = resultBadAudio
		}
		fail(res, err)
		return
	}
	if !languageRE.MatchString(f.language) {
		fail(resultError, invalid("language", "must be auto or a language code"))
		return
	}
	if utf8.RuneCountInString(f.prompt) > MaxPromptChars {
		fail(resultError, invalid("prompt", "over 200 characters"))
		return
	}
	cfg := s.cfg()
	if cfg.Enabled != nil && !*cfg.Enabled {
		fail(resultError, api.NewError(doctor.SVC011, http.StatusServiceUnavailable, map[string]any{"reason": "disabled"}))
		return
	}
	model := s.model()
	if model == "" {
		fail(resultError, api.NewError(doctor.SVC010, http.StatusServiceUnavailable, nil))
		return
	}
	maxSeconds := cfg.MaxAudioSeconds
	if maxSeconds <= 0 {
		maxSeconds = DefaultMaxAudioSeconds
	}

	audio, err := DecodeWAV(f.audio, float64(maxSeconds))
	if err != nil {
		fail(resultBadAudio, wavError(err))
		return
	}
	seconds := audio.Seconds()
	mono := Resample(audio.Mono(), audio.Rate, WhisperRate)
	if len(mono) == 0 {
		fail(resultBadAudio, badAudio("empty", http.StatusBadRequest))
		return
	}
	if RMSdBFS(mono) < SilenceDBFS {
		e := badAudio("silent", http.StatusBadRequest)
		if api.InfoFrom(r.Context()).Lang == "es" {
			e = e.WithMessage(silentES, "")
		} else {
			e = e.WithMessage(silentEN, "")
		}
		fail(resultBadAudio, e)
		return
	}
	wav := EncodeWAV16(ToPCM16(mono), WhisperRate)

	release, err := s.queue.Acquire(r.Context())
	switch {
	case errors.Is(err, ErrBusy):
		e := api.NewError(doctor.SVC015, http.StatusTooManyRequests, nil)
		e.RetryAfter = 3
		fail(resultBusy, e)
		return
	case err != nil: // the caller left, or the route deadline passed while waiting
		result = resultError
		return
	}
	defer release()

	res, err := s.client.Transcribe(r.Context(), wav, f.language, f.prompt)
	switch {
	case errors.Is(err, ErrInferenceTimeout):
		s.log.Warn("stt timed out", "code", doctor.SVC016, "audio_s", seconds, "model", model)
		fail(resultTimeout, api.NewError(doctor.SVC016, http.StatusGatewayTimeout, nil))
		return
	case errors.Is(err, ErrServerDown):
		s.log.Warn("stt failed: whisper-server not available", "code", doctor.SVC011, "audio_s", seconds, "model", model)
		fail(resultError, api.NewError(doctor.SVC011, http.StatusServiceUnavailable, nil))
		return
	case err != nil: // the caller left
		result = resultError
		return
	}

	elapsed := s.now().Sub(began).Milliseconds()
	out := map[string]any{
		"text": res.Text, "language": res.Language, "duration_s": seconds, "model": model, "elapsed_ms": elapsed,
	}
	if res.LanguageProbability != nil {
		out["language_probability"] = *res.LanguageProbability
	}
	result = resultOK
	// Never the transcript: it may be private.
	s.log.Info("stt done", "audio_s", seconds, "language", res.Language, "elapsed_ms", elapsed, "model", model)
	api.WriteJSON(w, r, http.StatusOK, out)
}
