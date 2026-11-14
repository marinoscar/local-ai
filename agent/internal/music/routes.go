package music

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The routes of the music API (E11 contract section 3, agent.md section 3.8):
//
//	GET    /music/status                 op     5 s
//	POST   /music/generate               op     60 s   multipart/form-data, up to 21 MiB
//	GET    /music/ops/{op_id}            op     5 s
//	POST   /music/ops/{op_id}/cancel     op     5 s
//	GET    /music/songs                  op     5 s
//	GET    /music/songs/{id}/audio       op     none (stream, Range; ?download=1 for an attachment)
//	DELETE /music/songs/{id}             admin  10 s   (the confirmation of DELETE /models/{id})

// Route settings.
const (
	StatusTimeout   = 5 * time.Second
	GenerateTimeout = 60 * time.Second
	OpTimeout       = 5 * time.Second
	CancelTimeout   = 5 * time.Second
	SongsTimeout    = 5 * time.Second
	// AudioTimeout is none: the audio is a stream (Range), agent.md section 3.8.
	AudioTimeout  = 0
	DeleteTimeout = 10 * time.Second
	// GenerateMaxBody is the sample (20 MiB) plus the text fields and the multipart framing.
	GenerateMaxBody int64 = 21 << 20

	defaultSongsLimit = 50
	maxSongsLimit     = 200
	maxSmallField     = 64
)

// Routes returns the routes of the service.
func (s *Service) Routes() []api.Route {
	op := []api.Level{api.AuthOp}
	return []api.Route{
		{Method: http.MethodGet, Pattern: "/music/status", Auth: op, Timeout: StatusTimeout, Handler: api.HandlerFunc(s.serveStatus)},
		{
			Method: http.MethodPost, Pattern: "/music/generate", Auth: op, Timeout: GenerateTimeout,
			MaxBody: GenerateMaxBody, MediaTypes: []string{"multipart/form-data"}, Handler: api.HandlerFunc(s.serveGenerate),
		},
		{Method: http.MethodGet, Pattern: "/music/ops/{op_id}", Auth: op, Timeout: OpTimeout, Handler: api.HandlerFunc(s.serveOp)},
		{Method: http.MethodPost, Pattern: "/music/ops/{op_id}/cancel", Auth: op, Timeout: CancelTimeout, Handler: api.HandlerFunc(s.serveCancel)},
		{Method: http.MethodGet, Pattern: "/music/songs", Auth: op, Timeout: SongsTimeout, Handler: api.HandlerFunc(s.serveSongs)},
		{Method: http.MethodGet, Pattern: "/music/songs/{id}/audio", Auth: op, Timeout: AudioTimeout, Handler: api.HandlerFunc(s.serveAudio)},
		{Method: http.MethodDelete, Pattern: "/music/songs/{id}", Auth: []api.Level{api.AuthAdmin}, Timeout: DeleteTimeout, Handler: api.HandlerFunc(s.serveDelete)},
	}
}

// Routes returns the route metadata of a service without dependencies (the OpenAPI conformance test).
func Routes() []api.Route { return (&Service{}).Routes() }

// Register adds the routes to reg.
func (s *Service) Register(reg *api.Registry) error { return reg.Add(s.Routes()...) }

func lang(r *http.Request) string { return api.InfoFrom(r.Context()).Lang }

func (s *Service) serveStatus(w http.ResponseWriter, r *http.Request) error {
	api.WriteJSON(w, r, http.StatusOK, s.Status())
	return nil
}

func (s *Service) serveOp(w http.ResponseWriter, r *http.Request) error {
	v, err := s.Op(r.PathValue("op_id"), lang(r))
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusOK, v)
	return nil
}

func (s *Service) serveCancel(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("op_id")
	v, err := s.Cancel(r.Context(), id, lang(r), api.AuditActor(api.InfoFrom(r.Context())))
	if err != nil {
		return err
	}
	api.SetOpID(w, id)
	api.WriteJSON(w, r, http.StatusOK, v)
	return nil
}

func (s *Service) serveSongs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := defaultSongsLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxSongsLimit {
			return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "limit", "reason": "must be 1 to 200"})
		}
		limit = n
	}
	cursor := q.Get("cursor")
	if cursor != "" && !ValidSongID(cursor) {
		return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "cursor", "reason": "unknown"})
	}
	l, err := s.Songs(limit, cursor)
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusOK, l)
	return nil
}

func (s *Service) serveAudio(w http.ResponseWriter, r *http.Request) error {
	m, path, err := s.Song(r.PathValue("id"))
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return notFound("song")
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", m.Mime)
	w.Header().Set("Cache-Control", "private, max-age=0")
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+`; filename="`+m.ID+"."+strings.TrimPrefix(m.Audio, "audio.")+`"`)
	http.ServeContent(w, r, "", st.ModTime(), f)
	return nil
}

func (s *Service) serveDelete(w http.ResponseWriter, r *http.Request) error {
	res, err := s.DeleteSong(r.Context(), r.PathValue("id"), api.AuditActor(api.InfoFrom(r.Context())))
	if err != nil {
		return err
	}
	api.WriteJSON(w, r, http.StatusOK, res)
	return nil
}

// ---------------------------------------------------------------------------------------------------
// POST /music/generate

func (s *Service) serveGenerate(w http.ResponseWriter, r *http.Request) error {
	req, err := s.readGenerate(w, r)
	if err != nil {
		s.DiscardSample(req.SamplePath)
		return err
	}
	req.Origin = OriginLocal
	req.Actor = api.AuditActor(api.InfoFrom(r.Context()))
	acc, err := s.Generate(r.Context(), req)
	if err != nil {
		return err
	}
	api.SetOpID(w, acc.OpID)
	api.WriteJSON(w, r, http.StatusAccepted, acc)
	return nil
}

func malformed() *api.Error {
	return api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "body", "reason": "bad_multipart"})
}

// bodyErr maps a failure to read the body: over the limit is 413 LAI-SVC-154.
func bodyErr(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return api.NewError(doctor.SVC154, http.StatusRequestEntityTooLarge, map[string]any{"field": "body", "reason": ReasonTooLarge, "limit": tooBig.Limit})
	}
	var se *SampleError
	if errors.As(err, &se) {
		return sampleErr(err)
	}
	return malformed()
}

// readGenerate reads the multipart form. The sample streams to the work directory; the text fields
// are bounded. A field the API does not know is refused.
func (s *Service) readGenerate(w http.ResponseWriter, r *http.Request) (GenerateRequest, error) {
	var req GenerateRequest
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return req, malformed()
	}
	mr := multipart.NewReader(http.MaxBytesReader(w, r.Body, GenerateMaxBody), params["boundary"])
	seen := map[string]bool{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return req, bodyErr(err)
		}
		name := part.FormName()
		if seen[name] {
			return req, inputError(cutName(name), "duplicate")
		}
		seen[name] = true
		if name == "sample" {
			st, err := s.StageSample(part)
			if err != nil {
				return req, bodyErr(err)
			}
			req.SamplePath = st.Path
			continue
		}
		limit := maxSmallField
		switch name {
		case "lyrics":
			limit = MaxLyricsBytes
		case "tags":
			limit = MaxTagsBytes
		}
		val, err := readField(part, limit)
		if err != nil {
			if errors.Is(err, errFieldTooLong) {
				return req, inputError(name, "too_long")
			}
			return req, bodyErr(err)
		}
		if err := setField(&req, name, val); err != nil {
			return req, err
		}
	}
	return req, nil
}

var errFieldTooLong = errors.New("field too long")

func readField(part io.Reader, limit int) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(b) > limit {
		return "", errFieldTooLong
	}
	return string(b), nil
}

func cutName(n string) string {
	if len(n) > 32 {
		n = n[:32]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, n)
}

func setField(req *GenerateRequest, name, val string) error {
	trim := strings.TrimSpace(val)
	integer := func() (int64, error) {
		n, err := strconv.ParseInt(trim, 10, 64)
		if err != nil {
			return 0, inputError(name, "not_an_integer")
		}
		return n, nil
	}
	switch name {
	case "lyrics":
		req.Lyrics = val
	case "tags":
		req.Tags = trim
	case "language":
		req.Language = trim
	case "sample_mode":
		req.SampleMode = trim
	case "duration_s":
		n, err := integer()
		if err != nil {
			return err
		}
		if n < MinDurationS || n > MaxDurationS {
			return inputError(name, "out_of_range")
		}
		req.DurationS = int(n)
	case "variations":
		n, err := integer()
		if err != nil {
			return err
		}
		if n < 1 || n > MaxVariations {
			return inputError(name, "out_of_range")
		}
		req.Variations = int(n)
	case "seed":
		if trim == "" {
			return nil
		}
		n, err := integer()
		if err != nil {
			return err
		}
		req.Seed = &n
	case "strength":
		f, err := strconv.ParseFloat(trim, 64)
		if err != nil || f != f {
			return inputError(name, "not_a_number")
		}
		req.Strength = &f
	default:
		return inputError(cutName(name), "unknown_field")
	}
	return nil
}
