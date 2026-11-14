package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/music"
	"github.com/marinoscar/local-ai/agent/internal/netx"
)

// music.generate (music.md section 4.7, cloud-app.md section 3.7): the node pulls the sample by a
// signed GET, makes the songs with the same local generate as POST /music/generate, and pushes each
// song to its signed PUT. Signed URLs are secrets for their lifetime: they are never logged, audited,
// stored or echoed in a failure message (only the HTTP status is).

// Music is the music service: *music.Service implements it.
type Music interface {
	StageSample(r io.Reader) (music.StagedSample, error)
	DiscardSample(path string)
	Generate(ctx context.Context, req music.GenerateRequest) (music.Accepted, error)
	Wait(ctx context.Context, opID string) (music.OpView, error)
	Cancel(ctx context.Context, opID, lang string, actor audit.Actor) (music.OpView, error)
	Song(id string) (music.Meta, string, error)
}

// HTTPDoer sends a request: *http.Client implements it.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Transfer retries (music.md section 4.7: up to 3 tries with backoff). Variables so the tests can
// shorten the waits.
var (
	transferAttempts = 3
	transferBackoff  = []time.Duration{2 * time.Second, 5 * time.Second}
	transferTimeout  = 10 * time.Minute
)

// MusicSample is the sample block of the params.
type MusicSample struct {
	URL       string
	SHA256    string
	Mime      string
	SizeBytes int64
}

// MusicGenerateParams is the decoded params of music.generate.
type MusicGenerateParams struct {
	RequestID  string
	Lyrics     string
	Tags       string
	DurationS  int
	Language   string
	Seed       *int64
	Variations int
	Sample     *MusicSample
	SampleMode string
	Strength   *float64
	PutURLs    []string
}

// MusicSong is one entry of the result.
type MusicSong struct {
	Index     int     `json:"index"`
	SHA256    string  `json:"sha256"`
	SizeBytes int64   `json:"size_bytes"`
	Mime      string  `json:"mime"`
	DurationS float64 `json:"duration_s"`
}

// MusicGenerateResult is the result of music.generate.
type MusicGenerateResult struct {
	Songs []MusicSong `json:"songs"`
}

var (
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sha256RE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	language2  = regexp.MustCompile(`^[a-z]{2}$`)
	sampleMIME = []string{"audio/wav", "audio/mpeg", "audio/flac", "audio/ogg"}
)

const maxURLChars = 4096

func checkHTTPS(path, raw string) error {
	if len(raw) > maxURLChars {
		return bad(path, "must be at most %d characters", maxURLChars)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return bad(path, "must be an https URL")
	}
	return nil
}

func decodeMusicGenerate(raw json.RawMessage) (MusicGenerateParams, error) {
	var w struct {
		RequestID  *string           `json:"request_id"`
		Lyrics     *string           `json:"lyrics"`
		Tags       *string           `json:"tags"`
		DurationS  *int              `json:"duration_s"`
		Language   *string           `json:"language"`
		Seed       *int64            `json:"seed"`
		Variations *int              `json:"variations"`
		Sample     json.RawMessage   `json:"sample"`
		SampleMode *string           `json:"sample_mode"`
		Strength   *float64          `json:"strength"`
		Results    []json.RawMessage `json:"results"`
	}
	var p MusicGenerateParams
	if err := cloud.DecodeParams(raw, &w); err != nil {
		return p, err
	}
	for _, r := range []struct {
		path string
		ok   bool
	}{
		{"request_id", w.RequestID != nil}, {"lyrics", w.Lyrics != nil}, {"tags", w.Tags != nil},
		{"duration_s", w.DurationS != nil}, {"language", w.Language != nil}, {"variations", w.Variations != nil},
		{"results", w.Results != nil},
	} {
		if err := required(r.path, r.ok); err != nil {
			return p, err
		}
	}
	switch {
	case !uuidRE.MatchString(*w.RequestID):
		return p, bad("request_id", "must be a UUID")
	case !utf8.ValidString(*w.Lyrics) || len(*w.Lyrics) > music.MaxLyricsBytes:
		return p, bad("lyrics", "must be UTF-8 text of at most 8 KiB")
	case !utf8.ValidString(*w.Tags) || len(*w.Tags) > music.MaxTagsBytes:
		return p, bad("tags", "must be UTF-8 text of at most 512 bytes")
	case *w.DurationS < music.MinDurationS || *w.DurationS > music.MaxDurationS:
		return p, bad("duration_s", "must be 10 to 240")
	case !language2.MatchString(*w.Language):
		return p, bad("language", "must be a two-letter language code")
	case w.Seed != nil && (*w.Seed < 0 || *w.Seed > music.MaxSeed):
		return p, bad("seed", "must be 0 to 2147483647")
	case *w.Variations < 1 || *w.Variations > music.MaxVariations:
		return p, bad("variations", "must be 1 to 4")
	case len(w.Results) != *w.Variations:
		return p, bad("results", "must have one entry per variation")
	}
	p = MusicGenerateParams{
		RequestID: strings.ToLower(*w.RequestID), Lyrics: *w.Lyrics, Tags: *w.Tags, DurationS: *w.DurationS,
		Language: *w.Language, Seed: w.Seed, Variations: *w.Variations, Strength: w.Strength,
	}
	for i, r := range w.Results {
		var rw struct {
			PutURL *string `json:"put_url"`
		}
		path := fmt.Sprintf("results[%d]", i)
		if err := cloud.DecodeParams(r, &rw); err != nil {
			return MusicGenerateParams{}, prefixed(path, err)
		}
		if err := required(path+".put_url", rw.PutURL != nil); err != nil {
			return MusicGenerateParams{}, err
		}
		if err := checkHTTPS(path+".put_url", *rw.PutURL); err != nil {
			return MusicGenerateParams{}, err
		}
		p.PutURLs = append(p.PutURLs, *rw.PutURL)
	}
	if w.Sample != nil {
		var sw struct {
			URL       *string `json:"url"`
			SHA256    *string `json:"sha256"`
			Mime      *string `json:"mime"`
			SizeBytes *int64  `json:"size_bytes"`
		}
		if err := cloud.DecodeParams(w.Sample, &sw); err != nil {
			return MusicGenerateParams{}, prefixed("sample", err)
		}
		for _, r := range []struct {
			path string
			ok   bool
		}{{"sample.url", sw.URL != nil}, {"sample.sha256", sw.SHA256 != nil}, {"sample.mime", sw.Mime != nil}, {"sample.size_bytes", sw.SizeBytes != nil}} {
			if err := required(r.path, r.ok); err != nil {
				return MusicGenerateParams{}, err
			}
		}
		if err := checkHTTPS("sample.url", *sw.URL); err != nil {
			return MusicGenerateParams{}, err
		}
		if !sha256RE.MatchString(*sw.SHA256) {
			return MusicGenerateParams{}, bad("sample.sha256", "must be 64 lower case hex digits")
		}
		if err := oneOf("sample.mime", *sw.Mime, sampleMIME); err != nil {
			return MusicGenerateParams{}, err
		}
		if *sw.SizeBytes < 1 || *sw.SizeBytes > music.MaxSampleBytes {
			return MusicGenerateParams{}, bad("sample.size_bytes", "must be 1 to 20 MiB")
		}
		p.Sample = &MusicSample{URL: *sw.URL, SHA256: *sw.SHA256, Mime: *sw.Mime, SizeBytes: *sw.SizeBytes}
	}
	if w.SampleMode != nil {
		if p.Sample == nil {
			return MusicGenerateParams{}, bad("sample_mode", "needs a sample")
		}
		if err := oneOf("sample_mode", *w.SampleMode, []string{music.SampleSoundsLike, music.SampleSameSong}); err != nil {
			return MusicGenerateParams{}, err
		}
		p.SampleMode = *w.SampleMode
	}
	if w.Strength != nil {
		if p.Sample == nil {
			return MusicGenerateParams{}, bad("strength", "needs a sample")
		}
		if *w.Strength < 0 || *w.Strength > 1 {
			return MusicGenerateParams{}, bad("strength", "must be 0 to 1")
		}
	}
	return p, nil
}

// prefixed puts the path of a nested object in front of a *cloud.DecodeError from it.
func prefixed(path string, err error) error {
	var de *cloud.DecodeError
	if errors.As(err, &de) {
		if de.Path == "" {
			return &cloud.DecodeError{Path: path, Problem: de.Problem}
		}
		return &cloud.DecodeError{Path: path + "." + de.Path, Problem: de.Problem}
	}
	return err
}

func (d Deps) musicHTTP() HTTPDoer {
	if d.MusicHTTP != nil {
		return d.MusicHTTP
	}
	return netx.Client("cloud-music")
}

// newMusicGenerate is the cloud's song request. It is cancellable: the claim loop stopping cancels
// the local operation (the songs already stored stay).
func newMusicGenerate(d Deps) cloud.Handler {
	return typed[MusicGenerateParams]{
		name: cloud.TypeMusicGenerate, cancellable: true, decode: decodeMusicGenerate,
		run: func(ctx context.Context, job cloud.Job, p MusicGenerateParams) (cloud.Outcome, error) {
			return runMusicGenerate(ctx, d, job, p)
		},
	}
}

func runMusicGenerate(ctx context.Context, d Deps, job cloud.Job, p MusicGenerateParams) (cloud.Outcome, error) {
	log := job.Logger
	req := music.GenerateRequest{
		Lyrics: p.Lyrics, Tags: p.Tags, DurationS: p.DurationS, Language: p.Language, Seed: p.Seed,
		Variations: p.Variations, SampleMode: p.SampleMode, Strength: p.Strength,
		Origin: music.OriginCloud, RequestID: p.RequestID, Actor: job.Actor,
	}
	if p.Sample != nil {
		path, err := downloadSample(ctx, d, p.Sample)
		if err != nil {
			if log != nil {
				log.Warn("the sample of a cloud song request could not be used", "code", doctor.SVC156, "reason", err.Error())
			}
			return cloud.Outcome{}, &cloud.JobError{Code: doctor.SVC156, Message: "The sample could not be downloaded or its checksum is wrong (" + err.Error() + ")."}
		}
		req.SamplePath = path
	}
	acc, err := d.Music.Generate(ctx, req) // takes over the sample, whatever happens
	if err != nil {
		return cloud.Outcome{}, err
	}
	view, err := d.Music.Wait(ctx, acc.OpID)
	if err != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = d.Music.Cancel(cctx, acc.OpID, "en", job.Actor)
		cancel()
		return cloud.Outcome{}, &cloud.JobError{Code: doctor.SVC158, Message: "The song was cancelled before it was finished."}
	}
	if view.State != music.StateDone {
		code, msg := doctor.SVC152, "The song could not be made."
		if view.Error != nil {
			code, msg = view.Error.Code, view.Error.Message
		}
		return cloud.Outcome{}, &cloud.JobError{Code: code, Message: msg}
	}
	res := MusicGenerateResult{Songs: []MusicSong{}}
	for i, id := range view.SongIDs {
		if i >= len(p.PutURLs) {
			break
		}
		meta, path, err := d.Music.Song(id)
		if err != nil {
			return cloud.Outcome{}, &cloud.JobError{Code: doctor.SVC157, Message: "A finished song could not be read for the upload."}
		}
		sum, size, err := uploadSong(ctx, d, path, meta.Mime, p.PutURLs[i])
		if err != nil {
			if log != nil {
				log.Warn("a cloud song upload failed", "code", doctor.SVC157, "song_id", id, "reason", err.Error())
			}
			return cloud.Outcome{}, &cloud.JobError{Code: doctor.SVC157, Message: fmt.Sprintf("Song %d could not be uploaded (%s); it is kept on the node.", i+1, err.Error())}
		}
		res.Songs = append(res.Songs, MusicSong{Index: i, SHA256: sum, SizeBytes: size, Mime: meta.Mime, DurationS: meta.DurationS})
	}
	return cloud.Outcome{Result: res}, nil
}

// transferError is a failed transfer; retry says whether another try may help. Its text never holds
// a URL.
type transferError struct {
	msg   string
	retry bool
}

func (e *transferError) Error() string { return e.msg }

// retryable: a network error, a timeout, 408, 429 and 5xx; a signed URL that is refused (403) or gone
// (404) will not get better.
func statusError(code int) *transferError {
	return &transferError{msg: fmt.Sprintf("HTTP %d", code), retry: code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500}
}

// withRetries runs fn up to transferAttempts times while it fails with a retryable error.
func withRetries(ctx context.Context, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; attempt < transferAttempts; attempt++ {
		if attempt > 0 {
			wait := transferBackoff[min(attempt-1, len(transferBackoff)-1)]
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return &transferError{msg: "cancelled"}
			case <-t.C:
			}
		}
		tctx, cancel := context.WithTimeout(ctx, transferTimeout)
		err = fn(tctx)
		cancel()
		var te *transferError
		if err == nil || !errors.As(err, &te) || !te.retry {
			return err
		}
	}
	return err
}

// netErr hides the URL of a transport failure.
func netErr(ctx context.Context, err error) *transferError {
	if ctx.Err() != nil {
		return &transferError{msg: "timed out", retry: true}
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return &transferError{msg: "timed out", retry: true}
	}
	return &transferError{msg: "network error", retry: true}
}

// downloadSample GETs the sample into the work directory (at most size_bytes) and checks its size
// and sha256. The path it returns belongs to Generate from then on.
func downloadSample(ctx context.Context, d Deps, s *MusicSample) (string, error) {
	var staged music.StagedSample
	err := withRetries(ctx, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		if err != nil {
			return &transferError{msg: "bad URL"}
		}
		resp, err := d.musicHTTP().Do(req)
		if err != nil {
			return netErr(ctx, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return statusError(resp.StatusCode)
		}
		if resp.ContentLength >= 0 && resp.ContentLength != s.SizeBytes {
			return &transferError{msg: "wrong size"}
		}
		st, err := d.Music.StageSample(io.LimitReader(resp.Body, s.SizeBytes+1))
		if err != nil {
			if ctx.Err() != nil {
				return netErr(ctx, err)
			}
			return &transferError{msg: "the sample could not be stored", retry: false}
		}
		staged = st
		return nil
	})
	if err != nil {
		return "", err
	}
	if staged.Bytes != s.SizeBytes {
		d.Music.DiscardSample(staged.Path)
		return "", &transferError{msg: "wrong size"}
	}
	if hex.EncodeToString(staged.SHA256[:]) != s.SHA256 {
		d.Music.DiscardSample(staged.Path)
		return "", &transferError{msg: "checksum mismatch"}
	}
	return staged.Path, nil
}

// uploadSong PUTs the file at path to a signed URL and returns its sha256 and size.
func uploadSong(ctx context.Context, d Deps, path, mime, putURL string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, &transferError{msg: "the song file is missing"}
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", 0, &transferError{msg: "the song file could not be read"}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	err = withRetries(ctx, func(ctx context.Context) error {
		f, err := os.Open(path)
		if err != nil {
			return &transferError{msg: "the song file is missing"}
		}
		defer f.Close()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL, f)
		if err != nil {
			return &transferError{msg: "bad URL"}
		}
		req.ContentLength = size
		req.Header.Set("Content-Type", mime)
		resp, err := d.musicHTTP().Do(req)
		if err != nil {
			return netErr(ctx, err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return statusError(resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return sum, size, nil
}
