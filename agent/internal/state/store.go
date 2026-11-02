package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"time"
)

// ErrClosed is returned by Mutate after Close.
var ErrClosed = errors.New("state: store is closed")

// DefaultFileMode is the mode of state.json: root:localai 0640 (agent.md section 3.5).
const DefaultFileMode os.FileMode = 0o640

// queueSize bounds the mutations waiting for the writer; senders block beyond it.
const queueSize = 256

// Owner is the uid and gid state.json is chown'ed to on every write (root:localai in production).
type Owner struct{ UID, GID int }

// Options configure Open. Every field is optional.
type Options struct {
	FS           FS               // default OSFS{}
	Mode         os.FileMode      // default DefaultFileMode
	Owner        *Owner           // nil: leave the owner alone (tests, non-root runs)
	AgentVersion string           // written to agent_version on every write
	Now          func() time.Time // default time.Now; tests inject a fake clock
	Logger       *slog.Logger     // default: discard
	OnFindings   func([]Finding)  // called from the writer goroutine when the finding set changes
}

// Store is the state store. All methods are safe for concurrent use.
//
// Locking (agent.md section 4.9, "state store" row): one writer goroutine applies mutations in the
// order they arrive and is the only code that touches the disk; readers take deep copies. No lock is
// held across file I/O, so a slow or stuck disk never blocks Snapshot.
type Store struct {
	path         string
	fs           FS
	mode         os.FileMode
	owner        *Owner
	agentVersion string
	now          func() time.Time
	log          *slog.Logger
	onFindings   func([]Finding)

	mu          sync.RWMutex // guards st, findings, source, writeErr: written by the writer, read by anyone
	st          State
	loadFinding *Finding
	writeErr    *Finding // non-nil while the last write failed (KindUnwritable)
	source      Source

	// Owned by the writer goroutine only.
	diskGood bool   // state.json on disk is known to be a complete good file
	lastGood []byte // its bytes, used when a hard link for .bak is impossible
	dirty    bool   // memory has changes that are not on disk yet

	q       chan *request
	closing chan struct{}
	done    chan struct{}
	once    sync.Once
}

type request struct {
	fn    func(*State) error // nil for a flush
	sync  bool
	reply chan reply
}

type reply struct {
	fnErr      error
	persistErr error
}

// Open loads the state at path (agent.md section 3.5 load rules) and starts the writer goroutine. It
// never fails because of the files themselves: a damaged or unreadable file degrades to the backup or
// to defaults with mode offline and is reported through Findings. It only returns an error for an
// empty path. Open writes nothing; the first Mutate writes the file.
func Open(path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("state: empty path")
	}
	s := &Store{
		path:         path,
		fs:           opts.FS,
		mode:         opts.Mode,
		owner:        opts.Owner,
		agentVersion: opts.AgentVersion,
		now:          opts.Now,
		log:          opts.Logger,
		onFindings:   opts.OnFindings,
		q:            make(chan *request, queueSize),
		closing:      make(chan struct{}),
		done:         make(chan struct{}),
	}
	if s.fs == nil {
		s.fs = OSFS{}
	}
	if s.mode == 0 {
		s.mode = DefaultFileMode
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.removeStrayTemps()
	l := s.load()
	s.st, s.source, s.loadFinding = l.st, l.source, l.finding
	s.diskGood, s.lastGood = l.diskGood, l.lastGood
	s.dirty = l.needsWrite || l.source == SourceBak || l.source == SourceDefaults
	if l.finding != nil {
		s.log.Warn("state: "+l.finding.Message, "details", l.finding.Details)
	}
	go s.run()
	return s, nil
}

// Path returns the state file path.
func (s *Store) Path() string { return s.path }

// Source says where the state loaded from at Open.
func (s *Store) Source() Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.source
}

// Snapshot returns a deep copy of the current state. The caller may modify it freely.
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.Clone()
}

// Findings returns what the doctor should report right now: the damage found at Open (if any) and the
// unwritable condition while the last write failed. The slice is a copy.
func (s *Store) Findings() []Finding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findingsLocked()
}

func (s *Store) findingsLocked() []Finding {
	var out []Finding
	if s.loadFinding != nil {
		out = append(out, copyFinding(*s.loadFinding))
	}
	if s.writeErr != nil {
		out = append(out, copyFinding(*s.writeErr))
	}
	return out
}

func copyFinding(f Finding) Finding {
	f.Details = deepCopy(f.Details)
	return f
}

// Writable reports whether the last write reached the disk (true before any write too).
func (s *Store) Writable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.writeErr == nil
}

// Mutate applies fn to a copy of the state, then persists the result. Mutations run one at a time in
// arrival order. If fn returns an error the state is unchanged, nothing is written and the error is
// returned. fn must not keep the pointer after it returns.
//
// Writes are group-committed: mutations that queue up while a write is in flight share the next
// write, and a mutation that changes nothing writes nothing. Mutate returns once the write that
// contains its change has been fsynced.
//
// If the state directory cannot be written (full, read-only, no permission) Mutate still succeeds: the
// new state is held in memory, Findings reports the unwritable condition, and the next mutation (or
// Flush) retries. Use MutateSync for the writes that must not be merged with later ones.
func (s *Store) Mutate(fn func(*State) error) error {
	return s.submit(&request{fn: fn}).fnErr
}

// MutateSync is Mutate for a change that must reach the disk on its own: it is never merged with
// mutations that arrive after it, and it writes even if fn changed nothing. The mode engine records
// every transition step with it (agent.md section 4.2.2).
func (s *Store) MutateSync(fn func(*State) error) error {
	return s.submit(&request{fn: fn, sync: true}).fnErr
}

// Flush writes the current state now if anything is not on disk yet, and returns the write error
// (nil when nothing was pending). Close calls it implicitly.
func (s *Store) Flush() error {
	r := s.submit(&request{sync: true})
	if r.fnErr != nil {
		return r.fnErr
	}
	return r.persistErr
}

// Close finishes queued mutations, makes a last attempt to write anything still pending and stops
// the writer. Snapshot and Findings keep working afterwards; Mutate returns ErrClosed.
func (s *Store) Close() error {
	s.once.Do(func() { close(s.closing) })
	<-s.done
	return nil
}

func (s *Store) submit(r *request) reply {
	r.reply = make(chan reply, 1)
	select {
	case s.q <- r:
	case <-s.done:
		return reply{fnErr: ErrClosed}
	}
	select {
	case rep := <-r.reply:
		return rep
	case <-s.done:
		select {
		case rep := <-r.reply:
			return rep
		default:
			return reply{fnErr: ErrClosed}
		}
	}
}

// run is the writer goroutine.
func (s *Store) run() {
	defer close(s.done)
	for {
		select {
		case r := <-s.q:
			s.handle(r)
		case <-s.closing:
			for {
				select {
				case r := <-s.q:
					s.handle(r)
				default:
					if s.dirty {
						_ = s.writeNow()
					}
					return
				}
			}
		}
	}
}

// handle runs one batch: the first request plus everything already queued behind it, up to and
// including the first sync request, applied in order and persisted with one write.
func (s *Store) handle(first *request) {
	batch := []*request{first}
	for !batch[len(batch)-1].sync {
		select {
		case r := <-s.q:
			batch = append(batch, r)
			continue
		default:
		}
		break
	}

	replies := make([]reply, len(batch))
	force := false
	for i, r := range batch {
		if r.fn == nil { // Flush: write only what is pending
			continue
		}
		force = force || r.sync
		replies[i].fnErr = s.apply(r.fn)
	}
	var perr error
	if force || s.dirty {
		perr = s.writeNow()
	}
	for i, r := range batch {
		replies[i].persistErr = perr
		r.reply <- replies[i]
	}
}

// apply runs fn on a copy and keeps the result only if fn succeeded and the result serialises.
func (s *Store) apply(fn func(*State) error) (err error) {
	cp := s.st.Clone() // only this goroutine writes s.st, so reading it here needs no lock
	cp.normalize()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("state: mutation panicked: %v", p)
		}
	}()
	if err := fn(&cp); err != nil {
		return err
	}
	cp.normalize()
	if _, err := json.Marshal(cp); err != nil {
		return fmt.Errorf("state: the mutation produces a state that cannot be written: %w", err)
	}
	if reflect.DeepEqual(cp, s.st) {
		return nil
	}
	s.mu.Lock()
	s.st = cp
	s.mu.Unlock()
	s.dirty = true
	return nil
}

// writeNow stamps written_at and agent_version, serialises and persists the state, and updates the
// unwritable finding.
func (s *Store) writeNow() error {
	now := s.now().UTC()
	s.mu.Lock()
	s.st.WrittenAt = now
	s.st.AgentVersion = s.agentVersion
	data, merr := json.MarshalIndent(s.st, "", "  ")
	s.mu.Unlock()
	if merr != nil { // apply() already proved the state serialises, so this is a bug, not a disk problem
		s.setWriteErr(merr)
		return merr
	}
	data = append(data, '\n')
	err := s.persist(data)
	if err == nil {
		s.dirty = false
	}
	s.setWriteErr(err)
	return err
}

func (s *Store) setWriteErr(err error) {
	s.mu.Lock()
	before := s.writeErr
	switch {
	case err == nil:
		s.writeErr = nil
	case before == nil:
		s.writeErr = &Finding{Kind: KindUnwritable, Severity: SeverityCritical, Message: msgUnwritable, Details: map[string]any{
			"path":  s.path,
			"error": err.Error(),
			"since": s.now().UTC().Format(time.RFC3339),
		}}
	default:
		s.writeErr.Details["error"] = err.Error()
	}
	changed := (before == nil) != (s.writeErr == nil)
	var snap []Finding
	if changed {
		snap = s.findingsLocked()
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	if err != nil {
		s.log.Error("state: cannot write state; keeping it in memory", "path", s.path, "error", err)
	} else {
		s.log.Info("state: writing works again", "path", s.path)
	}
	if s.onFindings != nil {
		s.onFindings(snap)
	}
}

// persist keeps the previous good file as .bak, then replaces state.json atomically.
func (s *Store) persist(data []byte) error {
	owner := -1
	group := -1
	if s.owner != nil {
		owner, group = s.owner.UID, s.owner.GID
	}
	if s.diskGood {
		s.rotateBak(owner, group)
	}
	if err := WriteFileAtomic(s.fs, s.path, data, s.mode, owner, group); err != nil {
		// The rename is the commit point: before it state.json is untouched. A failed directory fsync
		// happens after it, so the new file may be in place; either way the file is a complete one.
		return err
	}
	s.diskGood = true
	s.lastGood = data
	return nil
}

// rotateBak makes state.json.bak the current (about to be replaced) state.json: a hard link renamed
// over the old backup, which is atomic and copies no data. Without hard links it falls back to an
// atomic write of the bytes last known good. A failure here never blocks the main write.
func (s *Store) rotateBak(owner, group int) {
	bak := s.bakPath()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return
	}
	tmp := bak + tmpInfix + hex.EncodeToString(suffix)
	if err := s.fs.Link(s.path, tmp); err == nil {
		if err := s.fs.Rename(tmp, bak); err != nil {
			_ = s.fs.Remove(tmp)
			s.log.Warn("state: cannot rotate the backup", "error", err)
		}
		return
	}
	if s.lastGood == nil {
		return
	}
	if err := WriteFileAtomic(s.fs, bak, s.lastGood, s.mode, owner, group); err != nil {
		s.log.Warn("state: cannot rotate the backup", "error", err)
	}
}
