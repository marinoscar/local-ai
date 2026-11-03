package piper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// Route settings (agent.md section 3.8.25).
const (
	// TTSTimeout is the handler deadline of POST /speech/tts.
	TTSTimeout = 120 * time.Second
	// VoicesTimeout is the handler deadline of GET /speech/voices.
	VoicesTimeout = 5 * time.Second

	// UILanguageHeader is the Portal language the caller sends on every request (rule 4 of the voice
	// selection).
	UILanguageHeader = "X-LocalAI-UI-Language"

	// Response headers of a synthesis.
	HeaderVoice    = "X-LocalAI-Voice"
	HeaderLanguage = "X-LocalAI-Language"
	HeaderFallback = "X-LocalAI-Fallback"
)

// Speed range of a request (companion-services.md section 3.4).
const (
	MinSpeed     = 0.7
	MaxSpeed     = 1.5
	DefaultSpeed = 1.0
)

// Results of a request, the "result" label of localai_tts_requests_total.
const (
	resultOK           = "ok"
	resultInvalid      = "invalid"
	resultTooLong      = "too_long"
	resultBusy         = "busy"
	resultNoVoice      = "no_voice"
	resultNotInstalled = "not_installed"
	resultError        = "error"
	resultTimeout      = "timeout"
	resultClientGone   = "client_gone"
)

// Deps are what the speech routes need. Only Config, Catalog and Copies are required.
type Deps struct {
	// Config returns the piper block of node.yaml as it is now (a reload takes effect on the next
	// request).
	Config func() Config
	// Catalog lists the catalog's piper voices, profile voices first.
	Catalog VoiceCatalog
	// Copies finds the best verified copy of a voice's item (the ark index).
	Copies ark.CopyFinder
	// Streamer starts the program; default execx.Exec{}.
	Streamer execx.Streamer
	// FS and Clock are the disk and time seams; defaults config.OSFS{} and config.RealClock{}.
	FS    config.FS
	Clock config.Clock
	// Metrics receives localai_tts_requests_total and localai_tts_seconds; nil ignores them.
	Metrics *health.Metrics
	// Logger receives one line per request (never the text); default slog.Default().
	Logger *slog.Logger
	// Root is the install root (default DefaultRoot), WorkDir the child's working directory
	// (default DefaultWorkDir) and Credential the user the child runs as (nil keeps the agent's).
	Root       string
	WorkDir    string
	Credential *execx.Credential
	// Limiter bounds the concurrent syntheses; default 2 running and 4 queued.
	Limiter *Limiter
}

// Service is the text-to-speech service: the two routes and the health probe share it.
type Service struct {
	cfg     func() Config
	rt      Runtime
	voices  Voices
	limiter *Limiter
	clock   config.Clock
	metrics *health.Metrics
	log     *slog.Logger
}

// New returns the Service.
func New(d Deps) *Service {
	s := &Service{cfg: d.Config, limiter: d.Limiter, clock: d.Clock, metrics: d.Metrics, log: d.Logger}
	if s.cfg == nil {
		s.cfg = DefaultConfig
	}
	if s.limiter == nil {
		s.limiter = NewLimiter(DefaultMaxRunning, DefaultMaxQueued)
	}
	if s.clock == nil {
		s.clock = config.RealClock{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "piper")
	s.rt = Runtime{Streamer: d.Streamer, FS: d.FS, Root: d.Root, WorkDir: d.WorkDir, Credential: d.Credential}
	s.voices = Voices{Catalog: d.Catalog, Copies: d.Copies}
	return s
}

// Routes returns the two routes: POST /speech/tts and GET /speech/voices, both for the operator and
// the Portal.
func (s *Service) Routes() []api.Route {
	return []api.Route{
		{Method: http.MethodPost, Pattern: "/speech/tts", Auth: []api.Level{api.AuthOp, api.AuthPortal},
			Timeout: TTSTimeout, Handler: http.HandlerFunc(s.serveTTS)},
		{Method: http.MethodGet, Pattern: "/speech/voices", Auth: []api.Level{api.AuthOp, api.AuthPortal},
			Timeout: VoicesTimeout, Handler: http.HandlerFunc(s.serveVoices)},
	}
}

// Routes returns the routes of a Service over d (the OpenAPI conformance test uses it with empty
// dependencies: only the metadata is compared).
func Routes(d Deps) []api.Route { return New(d).Routes() }

// Register adds the routes to reg (nil means api.DefaultRegistry). The app wiring calls it once the
// ark index and the catalog exist; nothing here registers at init time.
func Register(reg *api.Registry, d Deps) error {
	if reg == nil {
		reg = api.DefaultRegistry
	}
	return reg.Add(Routes(d)...)
}

// Voices returns the installed-voice view the routes and the probe use.
func (s *Service) Voices() Voices { return s.voices }

// Runtime returns the exec contract the routes use.
func (s *Service) Runtime() Runtime { return s.rt }

// Config returns the piper block as it is now.
func (s *Service) Config() Config { return s.cfg() }

// ttsRequest is the body of POST /speech/tts.
type ttsRequest struct {
	Text     string   `json:"text"`
	Voice    *string  `json:"voice"`
	Language string   `json:"language"`
	Speed    *float64 `json:"speed"`
}

func invalid(field, reason string) *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}

// uiLanguage reduces X-LocalAI-UI-Language ("es", "es-MX", "en-US,en;q=0.9") to "en" or "es".
func uiLanguage(h string) string {
	if i := strings.IndexAny(h, ",;"); i >= 0 {
		h = h[:i]
	}
	switch l := primaryLanguage(h); l {
	case LangEN, LangES:
		return l
	}
	return ""
}

// serveTTS is POST /speech/tts.
func (s *Service) serveTTS(w http.ResponseWriter, r *http.Request) {
	var req ttsRequest
	// outcome records the one metric line of the request when the handler returns.
	voiceLabel, result := "none", resultInvalid
	var ranFor time.Duration
	defer func() {
		s.metrics.TTSRequest(voiceLabel, result)
		if ranFor > 0 {
			s.metrics.TTSSeconds(ranFor.Seconds())
		}
	}()
	fail := func(res string, err error) {
		result = res
		api.WriteError(w, r, err)
	}

	if err := api.DecodeJSON(w, r, &req); err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusRequestEntityTooLarge {
			result = resultTooLong
		}
		api.WriteError(w, r, err)
		return
	}
	switch req.Language {
	case "", "auto", LangEN, LangES:
	default:
		fail(resultInvalid, invalid("language", "must be auto, en or es"))
		return
	}
	speed := DefaultSpeed
	if req.Speed != nil {
		speed = *req.Speed
		if math.IsNaN(speed) || speed < MinSpeed || speed > MaxSpeed {
			fail(resultInvalid, invalid("speed", "must be between 0.7 and 1.5"))
			return
		}
	}
	if len(req.Text) > MaxRawBytes {
		fail(resultTooLong, tooLong(len(req.Text)))
		return
	}
	text := neutral(req.Text)
	if strings.TrimSpace(Localize(text, LangEN)) == "" {
		fail(resultInvalid, invalid("text", "empty_after_cleanup"))
		return
	}
	cfg := s.cfg()
	if !cfg.Enabled {
		fail(resultNoVoice, api.NewError(doctor.SVC022, http.StatusServiceUnavailable, map[string]any{"reason": "disabled"}))
		return
	}
	if err := s.rt.CheckBinary(); err != nil {
		fail(resultNotInstalled, synthAPIError(err))
		return
	}
	lang := ""
	if req.Language == LangEN || req.Language == LangES {
		lang = req.Language
	}
	in := Choice{Language: lang, Text: text, UILanguage: uiLanguage(r.Header.Get(UILanguageHeader))}
	if req.Voice != nil {
		in.Voice = *req.Voice
	}
	sel, err := s.voices.Select(in, cfg)
	switch {
	case errors.Is(err, errUnknownVoice):
		fail(resultInvalid, invalid("voice", "unknown_voice"))
		return
	case err != nil:
		fail(resultNoVoice, api.NewError(doctor.SVC022, http.StatusServiceUnavailable, nil))
		return
	}
	voiceLabel = sel.Voice.ID
	spoken := Localize(text, sel.Voice.Language)
	chars := len([]rune(spoken))
	if chars > MaxChars {
		fail(resultTooLong, tooLong(chars))
		return
	}

	// A slot: wait in the queue, or be refused when it is full.
	release, err := s.limiter.Acquire(r.Context())
	switch {
	case errors.Is(err, ErrBusy):
		fail(resultBusy, api.NewError(doctor.SVC015, http.StatusTooManyRequests, nil))
		return
	case errors.Is(err, context.Canceled):
		result = resultClientGone
		return
	case err != nil:
		fail(resultBusy, api.NewError(doctor.SVC015, http.StatusTooManyRequests, map[string]any{"reason": "queue_timeout"}))
		return
	}
	defer release()

	rate, err := s.rt.SampleRate(sel.Voice)
	if err != nil {
		s.logFailure(sel, chars, err)
		fail(resultError, synthAPIError(err))
		return
	}
	hdr, _ := StreamingHeader(rate) // the rate was checked by SampleRate
	began := s.clock.Now()
	proc, err := s.rt.Start(r.Context(), sel.Voice, spoken, cfg, speed)
	if err != nil {
		s.logFailure(sel, chars, err)
		res := resultError
		var se *SynthError
		if errors.As(err, &se) && se.Reason == ReasonNotInstalled {
			res = resultNotInstalled
		}
		fail(res, synthAPIError(err))
		return
	}

	st := &stream{w: w, rc: http.NewResponseController(w), hdr: hdr, limit: int64(rate) * 2 * maxAudioSeconds}
	st.headers = func() {
		h := w.Header()
		h.Set("Content-Type", "audio/wav")
		h.Set("Cache-Control", "no-store")
		h.Set(HeaderVoice, sel.Voice.ID)
		h.Set(HeaderLanguage, sel.Voice.Language)
		if sel.Fallback {
			h.Set(HeaderFallback, "true")
		}
	}
	outcome := st.pump(proc)
	res, werr := proc.Wait()
	ranFor = s.clock.Now().Sub(began)
	if ranFor <= 0 {
		ranFor = res.Duration
	}
	var serr error
	switch {
	case outcome == pumpClientGone:
		serr = context.Canceled
	case outcome == pumpTooLong:
		serr = &SynthError{Reason: ReasonTooLong}
	default:
		serr = Settle(res, werr, st.audio)
	}

	elapsed := ranFor.Milliseconds()
	switch {
	case serr == nil:
		result = resultOK
		s.log.Info("tts done", "chars", chars, "voice", sel.Voice.ID, "language", sel.Voice.Language,
			"elapsed_ms", elapsed, "audio_s", float64(st.audio/2)/float64(rate), "rule", sel.Rule)
		if sel.Fallback {
			s.log.Info("no voice for the requested language; another voice was used", "code", doctor.SVC021,
				"voice", sel.Voice.ID, "language", sel.Voice.Language)
		}
	case errors.Is(serr, context.Canceled):
		// The client left: piper was killed and there is nobody to answer.
		result = resultClientGone
		s.log.Info("tts stopped: the client left", "chars", chars, "voice", sel.Voice.ID,
			"language", sel.Voice.Language, "elapsed_ms", elapsed)
	default:
		result = resultError
		if errors.Is(serr, context.DeadlineExceeded) { // the route deadline passed while piper ran
			serr = &SynthError{Reason: ReasonTimeout, Err: serr}
		}
		var se *SynthError
		if errors.As(serr, &se) && se.Reason == ReasonTimeout {
			result = resultTimeout
		}
		s.logFailure(sel, chars, serr)
		if st.started {
			// Audio is on the wire, so the status line is gone: abort the connection, so that the
			// client sees a truncated stream and not a finished one.
			panic(http.ErrAbortHandler)
		}
		api.WriteError(w, r, synthAPIError(serr))
	}
}

func tooLong(chars int) *api.Error {
	return api.NewError(doctor.SVC024, http.StatusRequestEntityTooLarge, map[string]any{"chars": chars, "max": MaxChars})
}

// logFailure logs a failed synthesis with its code. The text is never in the line, and neither is
// the whole stderr: its first line, cut, is what an operator needs to start from.
func (s *Service) logFailure(sel Selection, chars int, err error) {
	attrs := []any{"code", doctor.SVC023, "chars", chars, "voice", sel.Voice.ID, "language", sel.Voice.Language}
	var se *SynthError
	if errors.As(err, &se) {
		attrs[1] = se.Code()
		attrs = append(attrs, "reason", se.Reason)
		if se.Reason == ReasonExit {
			attrs = append(attrs, "exit_code", se.ExitCode)
		}
		if first := firstLine(se.Stderr, 200); first != "" {
			attrs = append(attrs, "stderr", first)
		}
		if se.Err != nil {
			attrs = append(attrs, "error", se.Err.Error())
		}
	}
	s.log.Error("tts failed", attrs...)
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// synthAPIError maps a synthesis failure to its envelope: 503 LAI-SVC-020 when the program is
// missing, 500 LAI-SVC-023 otherwise, with the reason, the exit status and the first 4 KiB of stderr
// in details.
func synthAPIError(err error) *api.Error {
	var se *SynthError
	if !errors.As(err, &se) {
		return api.NewError(doctor.SVC023, http.StatusInternalServerError, map[string]any{"reason": ReasonStart})
	}
	d := map[string]any{"reason": se.Reason}
	if se.Reason == ReasonExit {
		d["exit_code"] = se.ExitCode
	}
	if se.Stderr != "" {
		d["stderr"] = se.Stderr
	}
	if se.Reason == ReasonNotInstalled {
		return api.NewError(doctor.SVC020, http.StatusServiceUnavailable, d)
	}
	return api.NewError(doctor.SVC023, http.StatusInternalServerError, d)
}

// ---------------------------------------------------------------- the streaming body

// readSize is the size of one read of stdout: about 0.7 s of audio at 22 050 Hz.
const readSize = 32 << 10

type pumpOutcome int

const (
	pumpEOF        pumpOutcome = iota // stdout reached its end
	pumpClientGone                    // writing to the client failed, or the request ended
	pumpTooLong                       // more audio than any text of MaxChars can make
)

// stream copies the program's PCM to the response behind the WAV header. The header goes out with
// the first audio, not before: until then a failure can still be an ordinary error response.
type stream struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	hdr     []byte
	limit   int64
	headers func() // sets the response headers just before the status line

	started bool  // the status line and the header were written
	audio   int64 // PCM bytes written
}

// pump reads stdout until its end. A read returns whatever piper wrote so far, so the first chunk
// is on the wire as soon as the first sentence is synthesized. An odd byte at the end of a read is
// held back to pair with the next one, and a last odd byte is dropped: a sample is two bytes.
func (st *stream) pump(p execx.Process) pumpOutcome {
	buf := make([]byte, 1+readSize)
	held := 0 // 0 or 1: a byte waiting at buf[0] for its pair
	for {
		n, err := p.Stdout().Read(buf[held:])
		total := held + n
		if even := total &^ 1; even > 0 {
			if !st.write(buf[:even]) {
				p.Kill()
				_, _ = io.Copy(io.Discard, p.Stdout())
				return pumpClientGone
			}
			if st.audio > st.limit {
				p.Kill()
				_, _ = io.Copy(io.Discard, p.Stdout())
				return pumpTooLong
			}
			held = total - even
			if held == 1 {
				buf[0] = buf[even]
			}
		} else {
			held = total
		}
		if err != nil {
			return pumpEOF
		}
	}
}

// write sends audio (preceded by the WAV header the first time) and flushes it. It reports false
// when the client is gone.
func (st *stream) write(pcm []byte) bool {
	if !st.started {
		st.headers()
		st.w.WriteHeader(http.StatusOK)
		if _, err := st.w.Write(st.hdr); err != nil {
			return false
		}
		st.started = true
	}
	if _, err := st.w.Write(pcm); err != nil {
		return false
	}
	st.audio += int64(len(pcm))
	if err := st.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	return true
}

// ---------------------------------------------------------------- GET /speech/voices

// voiceView is one row of GET /speech/voices.
type voiceView struct {
	ID          string `json:"id"`
	Language    string `json:"language"`
	DisplayName string `json:"display_name"`
	Default     bool   `json:"default"`
}

// serveVoices is GET /speech/voices: the installed voices, one default per language.
func (s *Service) serveVoices(w http.ResponseWriter, r *http.Request) {
	rows := []voiceView{}
	if cfg := s.cfg(); cfg.Enabled {
		all := s.voices.All()
		defaults := Defaults(all, cfg)
		for _, v := range all {
			rows = append(rows, voiceView{ID: v.ID, Language: v.Language, DisplayName: v.DisplayName, Default: defaults[v.Language] == v.ID})
		}
	}
	api.WriteJSON(w, r, http.StatusOK, map[string]any{"voices": rows})
}
