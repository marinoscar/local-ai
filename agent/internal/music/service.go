// Package music is the agent's music API (E11, #510): POST /api/v1/music/generate and the song store,
// in front of the loopback ACE-Step shim (engines/acestep). Clients never talk to the shim; the agent
// checks the input, stages the sample, hands one task to the shim per request, follows it, and turns
// its output files into songs under /var/lib/localai/music/songs/.
//
// Privacy: lyrics and sample bytes never reach a log line, an event or an error message; only sizes,
// counts and ids do. The lyrics are kept in the song's meta.json (the owner's data on the owner's
// disk).
//
// The engine itself (starting and stopping localai-music.service, the GPU budget) belongs to the
// acestep engine adapter of the router (#508). This package only asks "is music loaded?" through the
// Engine interface.
package music

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Input limits (E11 contract section 3).
const (
	MaxLyricsBytes   = 8 << 10
	MaxTagsBytes     = 512
	MinDurationS     = 10
	MaxDurationS     = 240
	DefaultDurationS = 60
	MaxVariations    = 4
	DefaultLanguage  = "en"
	DefaultMaxQueue  = 4
	maxSeed          = 1<<32 - 1
)

// Sample modes.
const (
	SampleSoundsLike = "sounds_like"
	SampleSameSong   = "same_song"
)

// Op states.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// ActionDelete is the audit action of DELETE /music/songs/{id}.
const ActionDelete = "music.delete"

// bytesPerSecond estimates the size of a song for the disk guard: 48 kHz, stereo, 16-bit WAV.
const bytesPerSecond = 48000 * 2 * 2

// keepFinished is how many finished operations GET /music/ops/{op_id} remembers.
const keepFinished = 128

// maxPollFailures is how many polls in a row may fail before the engine counts as dead.
const maxPollFailures = 3

var languageRE = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

// Publisher sends SSE events: *events.Bus implements it.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// Deps are what the service needs. Shim is required; everything else has a default.
type Deps struct {
	Shim Shim
	// Engine tells whether music is loaded; default HealthProbe over Shim.
	Engine Engine
	// Installed reports whether the music runtime is installed; default: RuntimeDir exists.
	Installed func() bool
	// SongsDir and WorkDir default to the canonical paths.
	SongsDir string
	WorkDir  string
	// MinFreeBytes is the profile's storage.min_free_bytes (0 when unknown).
	MinFreeBytes func() int64
	// FreeBytes returns the free space of the filesystem holding path; default statfs. A negative
	// answer means unknown and skips the disk guard.
	FreeBytes func(path string) (int64, error)
	// MaxQueue is how many songs may wait behind the running one; default 4 (MUSIC_MAX_QUEUE).
	MaxQueue  int
	Publisher Publisher
	Audit     api.AuditWriter
	// Poll is how often a running task is read; default 1 s.
	Poll   time.Duration
	Now    func() time.Time
	Logger *slog.Logger
}

// Service is the music API.
type Service struct {
	d     Deps
	store Store
	log   *slog.Logger

	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	ops    map[string]*op
	order  []string // op ids, oldest first
	closed bool
}

type op struct {
	id         string
	taskID     string
	state      string
	progress   *float64
	songIDs    []string
	code       string
	cancelReq  bool
	workDir    string
	req        GenerateRequest // Lyrics are dropped when the op ends
	modelID    string
	sample     *SampleMeta
	done       chan struct{}
	lastPubP   float64
	lastPubSt  string
	finishedAt time.Time
}

// New returns the service. It creates the directories and removes what an earlier run left behind
// (half-written songs, stale work directories).
func New(d Deps) (*Service, error) {
	if d.Shim == nil {
		return nil, errors.New("music: no shim")
	}
	if d.SongsDir == "" {
		d.SongsDir = DefaultSongsDir
	}
	if d.WorkDir == "" {
		d.WorkDir = DefaultWorkDir
	}
	if d.Engine == nil {
		d.Engine = HealthProbe{Shim: d.Shim}
	}
	if d.Installed == nil {
		d.Installed = func() bool { _, err := os.Stat(RuntimeDir); return err == nil }
	}
	if d.FreeBytes == nil {
		d.FreeBytes = statfsFree
	}
	if d.MaxQueue <= 0 {
		d.MaxQueue = DefaultMaxQueue
	}
	if d.Poll <= 0 {
		d.Poll = time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	for _, dir := range []string{d.SongsDir, d.WorkDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("music: %w", err)
		}
	}
	s := &Service{d: d, store: Store{Dir: d.SongsDir}, log: log.With("component", "music"), ops: map[string]*op{}}
	s.ctx, s.stop = context.WithCancel(context.Background())
	s.store.Sweep()
	s.sweepWork()
	return s, nil
}

// sweepWork empties the work directory: no operation survives a restart.
func (s *Service) sweepWork() {
	ents, err := os.ReadDir(s.d.WorkDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "op_") || n == incomingDir {
			_ = os.RemoveAll(filepath.Join(s.d.WorkDir, n))
		}
	}
}

// Close stops following the running operations (they end failed) and waits for them.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	s.wg.Wait()
}

// Store returns the song store.
func (s *Service) Store() Store { return s.store }

// ---------------------------------------------------------------------------------------------------
// Status

// Status is GET /music/status.
type Status struct {
	Installed   bool      `json:"installed"`
	LoadedModel *string   `json:"loaded_model"`
	EngineState string    `json:"engine_state"`
	Queue       QueueView `json:"queue"`
}

// QueueView is the queue block of the status.
type QueueView struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Max     int `json:"max"`
}

// Status reports the runtime, the engine and the queue.
func (s *Service) Status() Status {
	state, model := s.d.Engine.EngineStatus()
	st := Status{Installed: s.d.Installed(), EngineState: state, Queue: QueueView{Max: s.d.MaxQueue}}
	if model != "" && (state == EngineReady || state == EngineStarting || state == EngineStopping) {
		st.LoadedModel = &model
	}
	s.mu.Lock()
	for _, o := range s.ops {
		switch o.state {
		case StateRunning:
			st.Queue.Running++
		case StateQueued:
			st.Queue.Queued++
		}
	}
	s.mu.Unlock()
	return st
}

// ---------------------------------------------------------------------------------------------------
// Samples

const incomingDir = "incoming"

// StagedSample is a sample written to the work directory.
type StagedSample struct {
	Path   string
	Bytes  int64
	SHA256 [32]byte
}

// StageSample copies r (at most MaxSampleBytes) into the work directory and hashes it. Too large is a
// *SampleError; the partial file is removed. The caller hands Path to Generate, which takes it over,
// or removes it with DiscardSample.
func (s *Service) StageSample(r io.Reader) (StagedSample, error) {
	dir := filepath.Join(s.d.WorkDir, incomingDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return StagedSample{}, err
	}
	f, err := os.CreateTemp(dir, "sample-*")
	if err != nil {
		return StagedSample{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, MaxSampleBytes+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > MaxSampleBytes {
		err = &SampleError{ReasonTooLarge}
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return StagedSample{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return StagedSample{Path: f.Name(), Bytes: n, SHA256: sum}, nil
}

// DiscardSample removes a staged sample that will not be used.
func (s *Service) DiscardSample(path string) {
	if path != "" && s.inWork(path) {
		_ = os.Remove(path)
	}
}

func (s *Service) inWork(path string) bool {
	root := filepath.Clean(s.d.WorkDir) + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(path), root)
}

// ---------------------------------------------------------------------------------------------------
// Generate

// GenerateRequest is one song request. Zero values take the defaults (duration 60, language en, one
// variation, sounds_like with strength 0.5, or 1.0 for same_song).
type GenerateRequest struct {
	Lyrics     string
	Tags       string
	DurationS  int
	Language   string
	Seed       *int64
	Variations int
	// SamplePath is a staged sample (StageSample) or "". Generate takes it over: it is moved into the
	// operation or removed, whatever the outcome.
	SamplePath string
	SampleMode string
	Strength   *float64
	// Origin is local or cloud; RequestID is the cloud's request id (kept in meta.json).
	Origin    string
	RequestID string
	Actor     audit.Actor
}

// Accepted is the 202 of POST /music/generate.
type Accepted struct {
	OpID    string   `json:"op_id"`
	SongIDs []string `json:"song_ids"`
}

func inputError(field, reason string) *api.Error {
	return api.NewError(doctor.SVC154, http.StatusBadRequest, map[string]any{"field": field, "reason": reason})
}

// sampleErr maps a refused sample to 400 (or 413 for too large) LAI-SVC-154.
func sampleErr(err error) error {
	var se *SampleError
	if !errors.As(err, &se) {
		return err
	}
	status := http.StatusBadRequest
	if se.Reason == ReasonTooLarge {
		status = http.StatusRequestEntityTooLarge
	}
	return api.NewError(doctor.SVC154, status, map[string]any{"field": "sample", "reason": se.Reason})
}

// normalize fills the defaults and checks the limits.
func (r *GenerateRequest) normalize() error {
	if r.DurationS == 0 {
		r.DurationS = DefaultDurationS
	}
	if r.Language == "" {
		r.Language = DefaultLanguage
	}
	if r.Variations == 0 {
		r.Variations = 1
	}
	if r.Origin == "" {
		r.Origin = OriginLocal
	}
	switch {
	case !utf8.ValidString(r.Lyrics):
		return inputError("lyrics", "not_utf8")
	case len(r.Lyrics) > MaxLyricsBytes:
		return inputError("lyrics", "too_long")
	case !utf8.ValidString(r.Tags):
		return inputError("tags", "not_utf8")
	case len(r.Tags) > MaxTagsBytes:
		return inputError("tags", "too_long")
	case strings.TrimSpace(r.Lyrics) == "" && strings.TrimSpace(r.Tags) == "":
		return inputError("lyrics", "empty")
	case r.DurationS < MinDurationS || r.DurationS > MaxDurationS:
		return inputError("duration_s", "out_of_range")
	case !languageRE.MatchString(r.Language):
		return inputError("language", "invalid")
	case r.Variations < 1 || r.Variations > MaxVariations:
		return inputError("variations", "out_of_range")
	case r.Seed != nil && (*r.Seed < 0 || *r.Seed > maxSeed):
		return inputError("seed", "out_of_range")
	case r.Origin != OriginLocal && r.Origin != OriginCloud:
		return inputError("origin", "invalid")
	}
	if r.SamplePath == "" {
		if r.SampleMode != "" {
			return inputError("sample_mode", "needs_sample")
		}
		if r.Strength != nil {
			return inputError("strength", "needs_sample")
		}
		return nil
	}
	if r.SampleMode == "" {
		r.SampleMode = SampleSoundsLike
	}
	if r.SampleMode != SampleSoundsLike && r.SampleMode != SampleSameSong {
		return inputError("sample_mode", "invalid")
	}
	if r.Strength == nil {
		v := 0.5
		if r.SampleMode == SampleSameSong {
			v = 1.0
		}
		r.Strength = &v
	}
	if *r.Strength < 0 || *r.Strength > 1 {
		return inputError("strength", "out_of_range")
	}
	return nil
}

// Generate checks req, hands one task to the engine and returns at once; the operation is followed
// in the background. Errors (all *api.Error): 400/413 LAI-SVC-154 bad input or sample, 409
// LAI-SVC-151 music not loaded, 429 LAI-SVC-153 queue full, 507 LAI-SVC-155 not enough disk.
func (s *Service) Generate(ctx context.Context, req GenerateRequest) (Accepted, error) {
	sample := req.SamplePath
	consumed := false
	defer func() {
		if !consumed {
			s.DiscardSample(sample)
		}
	}()
	if sample != "" && !s.inWork(sample) {
		sample = "" // never touch a file outside the work directory
		return Accepted{}, inputError("sample", "invalid")
	}
	if err := req.normalize(); err != nil {
		return Accepted{}, err
	}
	var info SampleInfo
	if sample != "" {
		var err error
		if info, err = InspectSample(sample); err != nil {
			return Accepted{}, sampleErr(err)
		}
	}

	state, model := s.d.Engine.EngineStatus()
	if state != EngineReady {
		return Accepted{}, api.NewError(doctor.SVC151, http.StatusConflict, map[string]any{"engine_state": state})
	}
	if err := s.diskGuard(int64(req.Variations) * int64(req.DurationS) * bytesPerSecond); err != nil {
		return Accepted{}, err
	}

	o := &op{id: newID("op_"), state: StateQueued, req: req, modelID: model, done: make(chan struct{})}
	for i := 0; i < req.Variations; i++ {
		o.songIDs = append(o.songIDs, newID("sng_"))
	}
	o.workDir = filepath.Join(s.d.WorkDir, o.id)

	// Reserve the place in the queue before the engine is asked, so two requests cannot both take
	// the last one.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Accepted{}, api.NewError(api.CodeStarting, http.StatusServiceUnavailable, nil)
	}
	if s.queuedLocked() >= s.d.MaxQueue {
		s.mu.Unlock()
		return Accepted{}, queueFull()
	}
	s.addLocked(o)
	s.mu.Unlock()

	treq := TaskRequest{
		Lyrics: req.Lyrics, Tags: req.Tags, DurationS: req.DurationS, Language: req.Language, Seed: req.Seed,
		Variations: req.Variations, OutputDir: filepath.Join(o.workDir, "out"),
	}
	if err := os.MkdirAll(o.workDir, 0o750); err != nil {
		s.drop(o)
		return Accepted{}, err
	}
	if sample != "" {
		dst := filepath.Join(o.workDir, "sample."+info.Format.Ext)
		if err := os.Rename(sample, dst); err != nil {
			s.drop(o)
			return Accepted{}, err
		}
		consumed = true
		mode := req.SampleMode
		treq.SamplePath, treq.SampleMode, treq.Strength = &dst, &mode, req.Strength
		o.sample = &SampleMeta{Mode: mode, Strength: *req.Strength, Mime: info.Format.Mime, SizeBytes: info.Bytes, Seconds: round3(info.Seconds)}
	}

	taskID, err := s.d.Shim.Submit(ctx, treq)
	if err != nil {
		s.drop(o)
		switch {
		case errors.Is(err, ErrQueueFull):
			return Accepted{}, queueFull()
		case errors.Is(err, ErrNotReady), errors.Is(err, ErrUnreachable):
			return Accepted{}, api.NewError(doctor.SVC151, http.StatusConflict, map[string]any{"engine_state": EngineStopped})
		}
		s.log.Warn("the music engine refused a task", "code", doctor.SVC152, "error", err.Error())
		return Accepted{}, api.NewError(doctor.SVC152, http.StatusBadGateway, map[string]any{"reason": "refused"})
	}
	consumed = true
	s.mu.Lock()
	o.taskID = taskID
	s.mu.Unlock()

	s.log.Info("song requested", "op_id", o.id, "origin", req.Origin, "lyrics_bytes", len(req.Lyrics), "tags_bytes", len(req.Tags),
		"duration_s", req.DurationS, "variations", req.Variations, "sample_bytes", info.Bytes, "language", req.Language)
	s.publish(o)
	s.wg.Add(1)
	go s.follow(o)
	return Accepted{OpID: o.id, SongIDs: append([]string{}, o.songIDs...)}, nil
}

func queueFull() *api.Error {
	e := api.NewError(doctor.SVC153, http.StatusTooManyRequests, nil)
	e.RetryAfter = 30
	return e
}

func (s *Service) queuedLocked() int {
	n := 0
	for _, o := range s.ops {
		if o.state == StateQueued {
			n++
		}
	}
	return n
}

func (s *Service) addLocked(o *op) {
	s.ops[o.id] = o
	s.order = append(s.order, o.id)
}

// drop forgets an op that never reached the engine and removes its directory.
func (s *Service) drop(o *op) {
	s.mu.Lock()
	delete(s.ops, o.id)
	for i, id := range s.order {
		if id == o.id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	_ = os.RemoveAll(o.workDir)
}

// diskGuard refuses when saving need bytes would leave less than the profile's minimum free.
func (s *Service) diskGuard(need int64) error {
	free, err := s.d.FreeBytes(s.d.SongsDir)
	if err != nil || free < 0 {
		return nil // unknown: the write itself will fail if the disk is full
	}
	var min int64
	if s.d.MinFreeBytes != nil {
		min = max(s.d.MinFreeBytes(), 0)
	}
	if free-need < min {
		return api.NewError(doctor.SVC155, http.StatusInsufficientStorage, map[string]any{"free_bytes": free, "needed_bytes": need + min})
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------
// Following an operation

func (s *Service) follow(o *op) {
	defer s.wg.Done()
	defer os.RemoveAll(o.workDir)
	t := time.NewTicker(s.d.Poll)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-s.ctx.Done():
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.d.Shim.Cancel(cctx, o.taskID)
			cancel()
			s.finish(o, StateFailed, doctor.AGT024, nil)
			return
		case <-t.C:
		}
		if s.isFinal(o) { // cancelled while the engine could not be reached
			return
		}
		st, err := s.d.Shim.Task(s.ctx, o.taskID)
		if err != nil {
			if s.ctx.Err() != nil {
				continue
			}
			failures++
			if errors.Is(err, ErrTaskNotFound) || failures >= maxPollFailures {
				s.log.Warn("the music engine stopped during a song", "code", doctor.SVC152, "op_id", o.id)
				s.finish(o, StateFailed, doctor.SVC152, nil)
				return
			}
			continue
		}
		failures = 0
		switch st.State {
		case TaskQueued, TaskRunning:
			s.update(o, st.State, st.Progress)
		case TaskDone:
			s.save(o, st.Outputs)
			return
		case TaskCancelled:
			s.finish(o, StateCancelled, doctor.SVC158, nil)
			return
		default: // failed, or a state this agent does not know
			s.log.Warn("the music engine failed a song", "code", doctor.SVC152, "op_id", o.id)
			s.finish(o, StateFailed, doctor.SVC152, nil)
			return
		}
	}
}

func (s *Service) isFinal(o *op) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return final(o.state)
}

func final(state string) bool {
	return state == StateDone || state == StateFailed || state == StateCancelled
}

func (s *Service) update(o *op, state string, progress *float64) {
	s.mu.Lock()
	if final(o.state) {
		s.mu.Unlock()
		return
	}
	o.state = state
	if progress != nil {
		p := min(max(*progress, 0), 1)
		o.progress = &p
	}
	s.mu.Unlock()
	s.publish(o)
}

// save turns the engine's files into songs. A song exists only when all of the operation's songs
// could be stored; on a failure the ones already stored are removed again.
func (s *Service) save(o *op, outs []TaskOutput) {
	s.mu.Lock()
	cancelled := o.cancelReq
	req := o.req
	songIDs := append([]string{}, o.songIDs...)
	s.mu.Unlock()
	if cancelled {
		s.finish(o, StateCancelled, doctor.SVC158, nil)
		return
	}
	n := min(len(outs), len(songIDs))
	if n == 0 {
		s.finish(o, StateFailed, doctor.SVC152, nil)
		return
	}
	var need int64
	for _, out := range outs[:n] {
		if !s.inDir(out.Path, o.workDir) {
			s.log.Warn("the music engine named a file outside the operation", "code", doctor.SVC152, "op_id", o.id)
			s.finish(o, StateFailed, doctor.SVC152, nil)
			return
		}
		if st, err := os.Stat(out.Path); err == nil {
			need += st.Size()
		}
	}
	if err := s.diskGuard(need); err != nil {
		s.log.Warn("no disk space for a song", "code", doctor.SVC155, "op_id", o.id)
		s.finish(o, StateFailed, doctor.SVC155, nil)
		return
	}
	now := s.d.Now().UTC().Truncate(time.Second)
	var saved []string
	for i, out := range outs[:n] {
		ext, mime := extOf(out)
		m := Meta{
			ID: songIDs[i], CreatedAt: now, OpID: o.id, Variation: i + 1, Lyrics: req.Lyrics, Tags: req.Tags,
			Language: req.Language, DurationS: round3(out.DurationS), Requested: req.DurationS, ModelID: o.modelID,
			Seed: seedFor(req.Seed, i), Mime: mime, Origin: req.Origin, RequestID: req.RequestID, Sample: o.sample,
		}
		if _, err := s.store.Add(out.Path, ext, m); err != nil {
			for _, id := range saved {
				_ = s.store.Delete(id)
			}
			code := doctor.SVC152
			if errors.Is(err, syscall.ENOSPC) {
				code = doctor.SVC155
			}
			s.log.Warn("a song could not be stored", "code", code, "op_id", o.id, "error", err.Error())
			s.finish(o, StateFailed, code, nil)
			return
		}
		saved = append(saved, m.ID)
	}
	s.finish(o, StateDone, "", saved)
}

// seedFor is the seed of variation i: the engine uses seed+i (engines/acestep backend.py).
func seedFor(seed *int64, i int) *int64 {
	if seed == nil {
		return nil
	}
	v := *seed + int64(i)
	return &v
}

func extOf(out TaskOutput) (ext, mime string) {
	switch strings.ToLower(filepath.Ext(out.Path)) {
	case ".mp3":
		return "mp3", "audio/mpeg"
	case ".flac":
		return "flac", "audio/flac"
	case ".ogg":
		return "ogg", "audio/ogg"
	}
	return "wav", "audio/wav"
}

func (s *Service) inDir(path, dir string) bool {
	root := filepath.Clean(dir) + string(filepath.Separator)
	return filepath.IsAbs(path) && strings.HasPrefix(filepath.Clean(path), root)
}

// finish ends o once (later calls do nothing) and publishes music.done. songs replaces the song ids
// of a done operation; any other end has none.
func (s *Service) finish(o *op, state, code string, songs []string) bool {
	// The work directory goes first, so whoever sees the end sees no leftovers.
	_ = os.RemoveAll(o.workDir)
	s.mu.Lock()
	if final(o.state) {
		s.mu.Unlock()
		return false
	}
	o.state, o.code = state, code
	if state == StateDone {
		o.songIDs = songs
		one := 1.0
		o.progress = &one
	} else {
		o.songIDs = []string{}
	}
	o.req.Lyrics, o.req.Tags = "", "" // nothing private outlives the operation in memory
	o.finishedAt = s.d.Now()
	close(o.done)
	s.pruneLocked()
	s.mu.Unlock()
	s.log.Info("song finished", "op_id", o.id, "state", state, "code", code, "songs", len(songs))
	s.publish(o)
	return true
}

// pruneLocked forgets the oldest finished operations beyond keepFinished.
func (s *Service) pruneLocked() {
	finished := 0
	for _, id := range s.order {
		if o := s.ops[id]; o != nil && final(o.state) {
			finished++
		}
	}
	for i := 0; finished > keepFinished && i < len(s.order); {
		id := s.order[i]
		if o := s.ops[id]; o != nil && final(o.state) {
			delete(s.ops, id)
			s.order = append(s.order[:i], s.order[i+1:]...)
			finished--
			continue
		}
		i++
	}
}

// publish sends music.progress for a running operation (on a state change or a progress step of at
// least 2 %) and music.done for a finished one.
func (s *Service) publish(o *op) {
	if s.d.Publisher == nil {
		return
	}
	s.mu.Lock()
	ev := events.MusicOp{OpID: o.id, State: o.state, SongIDs: append([]string{}, o.songIDs...), Code: o.code}
	if o.progress != nil {
		p := *o.progress
		ev.Progress = &p
	}
	typ := events.TypeMusicProgress
	if final(o.state) {
		typ = events.TypeMusicDone
	} else {
		p := 0.0
		if ev.Progress != nil {
			p = *ev.Progress
		}
		if o.lastPubSt == o.state && p-o.lastPubP < 0.02 {
			s.mu.Unlock()
			return
		}
		o.lastPubSt, o.lastPubP = o.state, p
	}
	s.mu.Unlock()
	if err := s.d.Publisher.Publish(typ, o.id, ev); err != nil {
		s.log.Debug("a music event was not published", "error", err.Error())
	}
}

// ---------------------------------------------------------------------------------------------------
// Operations

// OpView is GET /music/ops/{op_id}.
type OpView struct {
	State    string   `json:"state"`
	Progress *float64 `json:"progress"`
	SongIDs  []string `json:"song_ids"`
	Error    *OpError `json:"error"`
}

// OpError is the error of a failed or cancelled operation.
type OpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func notFound(what string) *api.Error {
	return api.NewError(api.CodeNotFound, http.StatusNotFound, map[string]any{"what": what})
}

// view renders o; the caller holds s.mu.
func (s *Service) viewLocked(o *op, lang string) OpView {
	v := OpView{State: o.state, SongIDs: append([]string{}, o.songIDs...)}
	if o.progress != nil {
		p := *o.progress
		v.Progress = &p
	}
	if o.code != "" {
		v.Error = &OpError{Code: o.code, Message: codeText(o.code, lang)}
	}
	return v
}

func codeText(code, lang string) string {
	c, ok := doctor.Lookup(code)
	if !ok {
		return code
	}
	if lang == "es" && c.Title.ES != "" {
		return c.Title.ES
	}
	return c.Title.EN
}

// Op returns the view of an operation; 404 LAI-AGT-021 for an unknown one.
func (s *Service) Op(opID, lang string) (OpView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.ops[opID]
	if !ok {
		return OpView{}, notFound("op")
	}
	return s.viewLocked(o, lang), nil
}

// Cancel cancels an operation. Cancelling a finished one changes nothing and answers its view.
func (s *Service) Cancel(ctx context.Context, opID, lang string) (OpView, error) {
	s.mu.Lock()
	o, ok := s.ops[opID]
	if !ok {
		s.mu.Unlock()
		return OpView{}, notFound("op")
	}
	if final(o.state) {
		v := s.viewLocked(o, lang)
		s.mu.Unlock()
		return v, nil
	}
	o.cancelReq = true
	taskID := o.taskID
	s.mu.Unlock()
	if err := s.d.Shim.Cancel(ctx, taskID); err != nil {
		// The engine is gone or forgot the task: nothing runs any more.
		s.finish(o, StateCancelled, doctor.SVC158, nil)
	}
	s.log.Info("song cancel requested", "op_id", opID)
	return s.Op(opID, lang)
}

// Wait blocks until the operation ends (or ctx does) and returns its view.
func (s *Service) Wait(ctx context.Context, opID string) (OpView, error) {
	s.mu.Lock()
	o, ok := s.ops[opID]
	s.mu.Unlock()
	if !ok {
		return OpView{}, notFound("op")
	}
	select {
	case <-o.done:
	case <-ctx.Done():
		return OpView{}, ctx.Err()
	}
	return s.Op(opID, "en")
}

// ---------------------------------------------------------------------------------------------------
// Songs

// Songs is GET /music/songs.
func (s *Service) Songs(limit int, cursor string) (SongList, error) {
	l, err := s.store.Page(limit, cursor)
	if errors.Is(err, errBadCursor) {
		return SongList{}, api.NewError(api.CodeInvalidRequest, http.StatusBadRequest, map[string]any{"field": "cursor", "reason": "unknown"})
	}
	return l, err
}

// Song returns the metadata and the audio path of a song; 404 LAI-AGT-021 for an unknown one.
func (s *Service) Song(id string) (Meta, string, error) {
	m, err := s.store.Meta(id)
	if errors.Is(err, ErrSongNotFound) {
		return Meta{}, "", notFound("song")
	}
	if err != nil {
		return Meta{}, "", err
	}
	return m, s.store.AudioPath(m), nil
}

// DeletedSong is the answer of DELETE /music/songs/{id}.
type DeletedSong struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// DeleteSong removes a song and writes the music.delete audit line.
func (s *Service) DeleteSong(ctx context.Context, id string, actor audit.Actor) (DeletedSong, error) {
	err := s.store.Delete(id)
	result, code := audit.ResultOK, ""
	if err != nil {
		result = audit.ResultFail
		if errors.Is(err, ErrSongNotFound) {
			code = api.CodeNotFound
		} else {
			code = doctor.AGT099
		}
	}
	if ValidSongID(id) {
		s.auditWrite(ctx, result, code, actor, id)
	}
	if errors.Is(err, ErrSongNotFound) {
		return DeletedSong{}, notFound("song")
	}
	if err != nil {
		return DeletedSong{}, err
	}
	s.log.Info("song deleted", "song_id", id)
	return DeletedSong{ID: id, Deleted: true}, nil
}

func (s *Service) auditWrite(ctx context.Context, result audit.Result, code string, actor audit.Actor, id string) {
	if s.d.Audit == nil {
		return
	}
	ev := audit.Event{Action: ActionDelete, Result: result, Code: code, Actor: actor, Target: map[string]string{"song": id}, ReqID: api.InfoFrom(ctx).ID}
	if err := s.d.Audit.Write(ev); err != nil {
		s.log.Warn("audit write failed", "action", ActionDelete, "error", err.Error())
	}
}

// ---------------------------------------------------------------------------------------------------
// Helpers

const idAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// newID returns prefix plus 12 random base32 characters (agent.md section 2).
func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("music: no randomness: " + err.Error())
	}
	out := []byte(prefix)
	for _, x := range b {
		out = append(out, idAlphabet[int(x)&31])
	}
	return string(out)
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
