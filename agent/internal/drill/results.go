package drill

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

// Defaults of the results store (drill.md section 3.2).
const (
	// DefaultDir is where the run documents live.
	DefaultDir = "/var/lib/localai/drill"
	// DefaultKeep is the number of run documents kept.
	DefaultKeep = 24
	// DefaultFileMode is the mode of a run document (localai 0640).
	DefaultFileMode os.FileMode = 0o640
	// LatestName is the symlink to the last finished run.
	LatestName = "latest.json"

	partialSuffix = ".partial"
	tmpInfix      = ".tmp-"
)

// runIDPattern is a run id: drl-YYYYMMDDTHHMMSSZ-<4hex>, or drl-YYYYMMDDTHHMMSSZ-<4hex>-manual for a
// manual_plug entry (DRILL-06; the bare ...-manual form is accepted too). It is checked before an id
// becomes a file name, so a request can never name a path.
var runIDPattern = regexp.MustCompile(`^drl-[0-9]{8}T[0-9]{6}Z-([0-9a-f]{4}(-manual)?|manual)$`)

// ValidRunID reports whether id has the shape of a run id.
func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

// ErrNotFound is returned by the results store when a run, or any finished run, is not kept.
var ErrNotFound = errors.New("drill: no such run")

// FS is the filesystem seam of the results store: the atomic writer's state.FS plus the symlink
// operations of latest.json and MkdirAll. OSFS is the real one; the tests use an in-memory one.
type FS interface {
	state.FS
	Symlink(oldname, newname string) error
	Readlink(name string) (string, error)
	MkdirAll(path string, perm os.FileMode) error
}

// OSFS is the real filesystem.
type OSFS struct{ state.OSFS }

var _ FS = OSFS{}

// Symlink implements FS.
func (OSFS) Symlink(oldname, newname string) error { return os.Symlink(oldname, newname) }

// Readlink implements FS.
func (OSFS) Readlink(name string) (string, error) { return os.Readlink(name) }

// MkdirAll implements FS.
func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// ResultsOptions configure a Results. Every field is optional.
type ResultsOptions struct {
	FS    FS          // default OSFS
	Dir   string      // default DefaultDir
	Keep  int         // default DefaultKeep
	Mode  os.FileMode // default DefaultFileMode
	Owner *state.Owner
}

// Results is the store of run documents in the drill directory: <run-id>.json, the latest.json
// symlink and, while a run is active, <run-id>.partial (the checkpoint crash recovery reads). It is
// safe for concurrent use.
type Results struct {
	fs    FS
	dir   string
	keep  int
	mode  os.FileMode
	owner *state.Owner

	mu sync.Mutex
}

// NewResults returns a store over o.
func NewResults(o ResultsOptions) *Results {
	r := &Results{fs: o.FS, dir: o.Dir, keep: o.Keep, mode: o.Mode, owner: o.Owner}
	if r.fs == nil {
		r.fs = OSFS{}
	}
	if r.dir == "" {
		r.dir = DefaultDir
	}
	if r.keep < 1 {
		r.keep = DefaultKeep
	}
	if r.mode == 0 {
		r.mode = DefaultFileMode
	}
	return r
}

// Dir returns the drill directory.
func (s *Results) Dir() string { return s.dir }

func (s *Results) path(runID string) string { return filepath.Join(s.dir, runID+".json") }

func (s *Results) uidgid() (int, int) {
	if s.owner != nil {
		return s.owner.UID, s.owner.GID
	}
	return -1, -1
}

// encode renders a document as indented JSON without HTML escaping.
func encode(doc *Document) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Results) write(path string, data []byte) error {
	if err := s.fs.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("drill: create %s: %w", s.dir, err)
	}
	uid, gid := s.uidgid()
	return state.WriteFileAtomic(s.fs, path, data, s.mode, uid, gid)
}

// movesLatest reports whether a saved document becomes latest.json: a finished run, not a skipped
// one (a skipped scheduled run would hide the last real result) and not a manual_plug entry (it has
// no checks; the last automated run stays the latest).
func movesLatest(doc *Document) bool {
	return doc.State != StateSkipped && doc.Trigger != TriggerManualPlug
}

// Save writes the final document of a run, points latest.json at it when the run finished, removes
// the run's checkpoint and applies the retention: the newest Keep finished documents are kept, and of
// the skipped ones only the newest.
func (s *Results) Save(doc *Document) error {
	if !ValidRunID(doc.RunID) {
		return fmt.Errorf("drill: invalid run id %q", doc.RunID)
	}
	data, err := encode(doc)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(s.path(doc.RunID), data); err != nil {
		return err
	}
	_ = s.fs.Remove(filepath.Join(s.dir, doc.RunID+partialSuffix))
	var errs []error
	if movesLatest(doc) {
		if err := s.relink(doc.RunID); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.prune(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Checkpoint writes the in-flight document of an active run to <run-id>.partial. A crash leaves it
// behind, and Recover turns it into the aborted run's document.
func (s *Results) Checkpoint(doc *Document) error {
	if !ValidRunID(doc.RunID) {
		return fmt.Errorf("drill: invalid run id %q", doc.RunID)
	}
	data, err := encode(doc)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(filepath.Join(s.dir, doc.RunID+partialSuffix), data)
}

// Partial returns the checkpoint of a run, or ErrNotFound.
func (s *Results) Partial(runID string) (*Document, error) {
	if !ValidRunID(runID) {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.fs.ReadFile(filepath.Join(s.dir, runID+partialSuffix))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("drill: checkpoint of %s is unreadable: %w", runID, err)
	}
	return &doc, nil
}

// relink points latest.json at the run's file: a symlink created beside it and renamed over it, so
// a reader sees the old target or the new one and never a missing link.
func (s *Results) relink(runID string) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	link := filepath.Join(s.dir, LatestName)
	tmp := link + tmpInfix + hex.EncodeToString(suffix)
	if err := s.fs.Symlink(runID+".json", tmp); err != nil {
		return fmt.Errorf("drill: link %s: %w", LatestName, err)
	}
	if err := s.fs.Rename(tmp, link); err != nil {
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("drill: link %s: %w", LatestName, err)
	}
	return s.fs.SyncDir(s.dir)
}

// Get returns the document of a run, ErrNotFound when it is not kept (or the id has the wrong shape).
func (s *Results) Get(runID string) ([]byte, error) {
	if !ValidRunID(runID) {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.fs.ReadFile(s.path(runID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// Has reports whether a document or a checkpoint of the run is kept.
func (s *Results) Has(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok, _ := s.fs.Exists(s.path(runID)); ok {
		return true
	}
	ok, _ := s.fs.Exists(filepath.Join(s.dir, runID+partialSuffix))
	return ok
}

// Latest returns the last finished run's document. It follows latest.json and, when the link is
// missing or dangling (a crash between the write and the link, or a pruned target), falls back to the
// newest finished document in the directory, so the answer never depends on the link alone.
func (s *Results) Latest() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target, err := s.fs.Readlink(filepath.Join(s.dir, LatestName)); err == nil {
		id := strings.TrimSuffix(filepath.Base(target), ".json")
		if ValidRunID(id) {
			if data, err := s.fs.ReadFile(s.path(id)); err == nil {
				return data, nil
			}
		}
	}
	runs, err := s.list()
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].skipped || runs[i].noLatest {
			continue
		}
		return s.fs.ReadFile(s.path(runs[i].id))
	}
	return nil, ErrNotFound
}

// All returns every kept document, newest first by the time the run finished (then by run id), for
// the reporter's startup, whatever their state. A document that cannot be read or decoded is left
// out (the retention and Latest treat such a file the same way). A missing directory is no
// documents.
func (s *Results) All() ([]*Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.list()
	if err != nil {
		return nil, err
	}
	out := make([]*Document, 0, len(runs))
	for _, r := range runs {
		data, err := s.fs.ReadFile(s.path(r.id))
		if err != nil {
			continue
		}
		var doc Document
		if json.Unmarshal(data, &doc) != nil || doc.RunID != r.id {
			continue
		}
		out = append(out, &doc)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := finishedOf(out[i]), finishedOf(out[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].RunID > out[j].RunID
	})
	return out, nil
}

// finishedOf is the time a document's run finished, its start for one that has no finish time.
func finishedOf(d *Document) time.Time {
	if d.FinishedAt != nil {
		return *d.FinishedAt
	}
	return d.StartedAt
}

// run is a kept document, as the retention sees it.
type run struct {
	id       string
	skipped  bool
	noLatest bool
}

// list returns the kept documents in run id order (oldest first: the timestamp sorts first).
func (s *Results) list() ([]run, error) {
	names, err := s.fs.ReadDirNames(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, n := range names {
		if id, ok := strings.CutSuffix(n, ".json"); ok && ValidRunID(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]run, 0, len(ids))
	for _, id := range ids {
		r := run{id: id}
		if data, err := s.fs.ReadFile(s.path(id)); err == nil {
			var head struct {
				State   State   `json:"state"`
				Trigger Trigger `json:"trigger"`
			}
			if json.Unmarshal(data, &head) == nil {
				r.skipped = head.State == StateSkipped
				r.noLatest = head.Trigger == TriggerManualPlug
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// prune applies the retention and sweeps what a crash leaves: temp files, and checkpoints of runs
// that have a final document.
func (s *Results) prune() error {
	names, err := s.fs.ReadDirNames(s.dir)
	if err != nil {
		return err
	}
	var errs []error
	have := map[string]bool{}
	for _, n := range names {
		if id, ok := strings.CutSuffix(n, ".json"); ok {
			have[id] = true
		}
	}
	for _, n := range names {
		switch {
		case strings.Contains(n, tmpInfix):
			errs = append(errs, ignoreMissing(s.fs.Remove(filepath.Join(s.dir, n))))
		case strings.HasSuffix(n, partialSuffix) && have[strings.TrimSuffix(n, partialSuffix)]:
			errs = append(errs, ignoreMissing(s.fs.Remove(filepath.Join(s.dir, n))))
		}
	}
	runs, err := s.list()
	if err != nil {
		return err
	}
	var finished, skipped []string
	for _, r := range runs {
		if r.skipped {
			skipped = append(skipped, r.id)
		} else {
			finished = append(finished, r.id)
		}
	}
	var drop []string
	if len(finished) > s.keep {
		drop = append(drop, finished[:len(finished)-s.keep]...)
	}
	if len(skipped) > 1 {
		drop = append(drop, skipped[:len(skipped)-1]...)
	}
	// The run latest.json names is never dropped, however many manual entries came after it.
	keepID := ""
	if target, err := s.fs.Readlink(filepath.Join(s.dir, LatestName)); err == nil {
		keepID = strings.TrimSuffix(filepath.Base(target), ".json")
	}
	for _, id := range drop {
		if id == keepID {
			continue
		}
		errs = append(errs, ignoreMissing(s.fs.Remove(s.path(id))))
	}
	return errors.Join(errs...)
}

func ignoreMissing(err error) error {
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
