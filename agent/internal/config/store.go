package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPollInterval is how often the store looks at node.yaml (agent.md 3.4, hot reload).
const DefaultPollInterval = 10 * time.Second

// maxNodeYAML bounds what the store will read; a real node.yaml is a few KiB.
const maxNodeYAML = 1 << 20

// defaultFileMode is node.yaml's mode when the store has to create it (root:localai 0640).
const defaultFileMode os.FileMode = 0o640

// Options configures a Store. Every field is optional.
type Options struct {
	Root     string        // filesystem root, so tests run in a temp dir; "" means "/"
	Path     string        // node.yaml path; default NodeYAMLPath(Root)
	FS       FS            // default OSFS
	Clock    Clock         // default RealClock
	Interval time.Duration // reload poll interval; default DefaultPollInterval
	Hooks    Hooks         // catalog, detection and block-validator seams
}

// Status is the health of the config source, for the alert manager (AGENT-11).
type Status struct {
	// Code is CodeNodeYAMLMissing, CodeNodeYAMLInvalid, the code of a lone non-blocking problem
	// (CodeProfileMissing), or "" when node.yaml is healthy.
	Code string `json:"code,omitempty"`
	// Severity is "error" when the file is missing or invalid (the last valid config stays in use),
	// "warning" when it loaded with problems (unknown top-level key, profile not in the catalog).
	Severity Severity `json:"severity,omitempty"`
	// Problems is the details.problems[] list of the latest evaluation, warnings included.
	Problems Problems `json:"problems,omitempty"`
	// Pending lists settings in the file whose new value needs an agent restart to take effect
	// (CodeRestartNeeded). It stays until the file matches the running agent again.
	Pending []RestartNeed `json:"pending,omitempty"`
	// Safe is true while the agent runs on SafeDefaults.
	Safe bool `json:"safe,omitempty"`
	// LoadedAt is when the effective config was last replaced.
	LoadedAt time.Time `json:"loaded_at"`
}

// RestartNeed is one setting that needs a restart (CodeRestartNeeded). Target is "agent" for
// `systemctl restart localai-agent`, or "llama" for `aictl restart llama`.
type RestartNeed struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// ChangeSource says what triggered a Change.
type ChangeSource string

const (
	SourceReload ChangeSource = "reload" // the poll saw node.yaml change on disk
	SourceUpdate ChangeSource = "update" // Store.Update wrote it
)

// Change is delivered to subscribers when the effective config or the status changes.
type Change struct {
	Source ChangeSource
	Old    Config // effective config before
	New    Config // effective config after
	// Changed lists the dotted paths whose effective value changed ("cloud.allow", "logging.level").
	// Owned blocks appear by block name ("sync").
	Changed []string
	// Restart lists the restarts this change raised (CodeRestartNeeded, info).
	Restart []RestartNeed
	Status  Status
}

// Store holds the effective node.yaml config. Get is a cheap snapshot; Update rewrites the file
// keeping comments; Start/Stop run the 10 s reload poll; Subscribe delivers changes.
//
// The effective config differs from the file in one way: the settings that need an agent restart
// (node.id, ports.*, cloud.url) keep the value the agent started with, so no component ever sees
// them change under it. The file's new value is reported in Status.Pending.
type Store struct {
	path     string
	bakPath  string
	fs       FS
	clock    Clock
	interval time.Duration
	hooks    Hooks

	opMu sync.Mutex // serializes Update and reload; held across file IO

	mu      sync.RWMutex
	eff     Config // effective config
	file    Config // last valid config parsed from the file, unpinned
	running Config // config at start: the pinned values of the restart-required settings
	status  Status
	seen    fileStamp

	subMu   sync.Mutex
	subs    map[int]chan Change
	nextSub int

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Open loads node.yaml and returns a Store. It never fails: a missing file (CodeNodeYAMLMissing) or
// an invalid one (CodeNodeYAMLInvalid) puts the store on SafeDefaults and says so in Status, so the
// agent can start and survival stays reachable. Leftover temp files of an interrupted write are removed.
func Open(o Options) *Store {
	s := &Store{
		path: o.Path, fs: o.FS, clock: o.Clock, interval: o.Interval, hooks: o.Hooks,
		subs: map[int]chan Change{},
	}
	if s.path == "" {
		s.path = NodeYAMLPath(o.Root)
	}
	s.bakPath = s.path + ".bak"
	if s.fs == nil {
		s.fs = OSFS{}
	}
	if s.clock == nil {
		s.clock = RealClock{}
	}
	if s.interval <= 0 {
		s.interval = DefaultPollInterval
	}
	SweepTemps(s.fs, s.path)
	SweepTemps(s.fs, s.bakPath)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.refresh(true, true)
	return s
}

// Path is the node.yaml path this store reads and writes.
func (s *Store) Path() string { return s.path }

// Get returns a snapshot of the effective config. The caller owns it.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.eff.Clone()
}

// Status returns the health of the config source.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status.clone()
}

func (st Status) clone() Status {
	st.Problems = append(Problems(nil), st.Problems...)
	st.Pending = append([]RestartNeed(nil), st.Pending...)
	return st
}

// Subscribe returns a channel of changes and a function that cancels the subscription. The channel
// is buffered; when a subscriber falls behind, the oldest queued change is dropped, so it always
// ends up seeing the latest. Call Get for the current config.
func (s *Store) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, 16)
	s.subMu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

func (s *Store) notify(c Change) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs {
		for try := 0; try < 4; try++ {
			select {
			case ch <- c:
				try = 4
			default:
				select {
				case <-ch: // full: drop the oldest
				default:
				}
			}
		}
	}
}

// Update edits node.yaml through its yaml.Node tree and rewrites it atomically. fn receives the
// top-level mapping; use SetKey, DeleteKey and LookupKey (or edit the nodes directly). fn runs with
// the store locked and must not call the store.
//
// The result is validated first: if it would be invalid, Update returns a *ValidationError and
// writes nothing. Otherwise the previous file is saved as node.yaml.bak (only when it was itself
// valid, so the .bak is always the last good copy), the new file replaces it atomically with the old
// mode and owner, and the new config is applied at once. A file whose content does not change is
// not rewritten. If node.yaml is missing, fn starts from an empty document.
func (s *Store) Update(fn func(root *yaml.Node) error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	orig, st, rerr := s.readFile()
	if rerr != nil {
		return fmt.Errorf("read %s: %w", s.path, rerr)
	}
	doc, err := s.editableDoc(orig, st.present)
	if err != nil {
		return err
	}
	if err := fn(doc.Content[0]); err != nil {
		return err
	}
	out, err := encodeDocument(doc, orig)
	if err != nil {
		return fmt.Errorf("encode node.yaml: %w", err)
	}
	cfg, probs := Load(out, &s.hooks)
	if probs.HasErrors() {
		return &ValidationError{Problems: probs}
	}
	if st.present && bytes.Equal(orig, out) {
		return nil
	}

	mode, uid, gid := defaultFileMode, -1, -1
	if info, serr := s.fs.Stat(s.path); serr == nil {
		mode = info.Mode().Perm()
		if u, g, ok := ownerOf(info); ok {
			uid, gid = u, g
		}
	}
	if st.present {
		if _, origProbs := Load(orig, &s.hooks); !origProbs.HasErrors() {
			if err := WriteFileAtomicFS(s.fs, s.bakPath, orig, mode, uid, gid); err != nil && !errors.Is(err, ErrNotDurable) {
				return fmt.Errorf("save %s: %w", s.bakPath, err)
			}
		}
	}
	werr := WriteFileAtomicFS(s.fs, s.path, out, mode, uid, gid)
	if werr != nil && !errors.Is(werr, ErrNotDurable) {
		return fmt.Errorf("write %s: %w", s.path, werr)
	}
	// The file is replaced: record it so the poll does not see our own write as an edit.
	if _, nst, nerr := s.readFile(); nerr == nil {
		s.seen = nst
	}
	s.apply(loadResult{kind: kindOK, cfg: cfg, probs: probs}, false, SourceUpdate)
	return werr // nil, or ErrNotDurable: the new config is in place and applied either way
}

// editableDoc parses the current file into a document whose first content node is a mapping.
func (s *Store) editableDoc(orig []byte, present bool) (*yaml.Node, error) {
	var doc yaml.Node
	if present {
		if err := yaml.Unmarshal(orig, &doc); err != nil {
			return nil, fmt.Errorf("node.yaml is not valid YAML, fix or restore it first: %w", err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	switch top := doc.Content[0]; {
	case isNull(top):
		doc.Content[0] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: top.HeadComment}
	case top.Kind != yaml.MappingNode:
		return nil, errors.New("node.yaml's top level is not a map of settings, fix or restore it first")
	}
	return &doc, nil
}

// fileStamp identifies one state of node.yaml for the reload poll: mtime and size to notice an
// edit cheaply, the sha256 to ignore a touch that changed nothing.
type fileStamp struct {
	present bool
	mtime   int64
	size    int64
	errText string
	sum     [sha256.Size]byte
	hashed  bool
}

func (a fileStamp) sameFile(b fileStamp) bool {
	return a.present == b.present && a.mtime == b.mtime && a.size == b.size && a.errText == b.errText
}

// readFile stats and reads node.yaml. A missing file is (nil, {present:false}, nil).
func (s *Store) readFile() ([]byte, fileStamp, error) {
	info, err := s.fs.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fileStamp{}, nil
	}
	if err != nil {
		return nil, fileStamp{errText: err.Error()}, err
	}
	st := fileStamp{present: true, mtime: info.ModTime().UnixNano(), size: info.Size()}
	fail := func(err error) ([]byte, fileStamp, error) {
		st.errText = err.Error()
		return nil, st, err
	}
	if !info.Mode().IsRegular() {
		return fail(errors.New("not a regular file"))
	}
	if info.Size() > maxNodeYAML {
		return fail(fmt.Errorf("larger than %d bytes", maxNodeYAML))
	}
	data, err := s.fs.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fileStamp{}, nil
	}
	if err != nil {
		return fail(err)
	}
	st.sum = sha256.Sum256(data)
	st.hashed = true
	return data, st, nil
}

type loadKind int

const (
	kindOK loadKind = iota
	kindMissing
	kindInvalid
)

type loadResult struct {
	kind  loadKind
	cfg   Config
	probs Problems
}

func (s *Store) evaluate(data []byte, st fileStamp, rerr error) loadResult {
	switch {
	case rerr == nil && !st.present:
		return loadResult{kind: kindMissing}
	case rerr != nil:
		return loadResult{kind: kindInvalid, probs: Problems{{Message: "cannot be read: " + rerr.Error(), Severity: SeverityError}}}
	}
	cfg, probs := Load(data, &s.hooks)
	if probs.HasErrors() {
		return loadResult{kind: kindInvalid, probs: probs}
	}
	return loadResult{kind: kindOK, cfg: cfg, probs: probs}
}

// refresh looks at node.yaml and applies it when it changed (or always, with force). The caller
// holds opMu. It reports whether it evaluated the file.
func (s *Store) refresh(force, initial bool) bool {
	data, st, rerr := s.readFile()
	if !force && !initial {
		if st.sameFile(s.seen) {
			return false
		}
		if st.hashed && s.seen.hashed && st.sum == s.seen.sum {
			s.seen = st // touched, not edited
			return false
		}
	}
	s.seen = st
	s.apply(s.evaluate(data, st, rerr), initial, SourceReload)
	return true
}

// apply installs an evaluated file: the effective config, the status, and a Change for subscribers
// (not for the initial load). The caller holds opMu.
func (s *Store) apply(r loadResult, initial bool, src ChangeSource) {
	s.mu.Lock()
	oldEff, oldFile, oldStatus := s.eff, s.file, s.status
	newEff, newFile := oldEff, oldFile
	status := Status{LoadedAt: oldStatus.LoadedAt, Pending: oldStatus.Pending}

	switch r.kind {
	case kindOK:
		newFile = r.cfg
		if initial {
			s.running = r.cfg
		}
		newEff = pinRestartRequired(r.cfg, s.running)
		status = statusOf(r)
	case kindMissing:
		status.Code, status.Severity = CodeNodeYAMLMissing, SeverityError
	case kindInvalid:
		status.Code, status.Severity, status.Problems = CodeNodeYAMLInvalid, SeverityError, r.probs
	}
	if initial && r.kind != kindOK {
		safe := SafeDefaults()
		s.running, newEff, newFile = safe, safe, safe
	}
	status.Safe = s.running.SafeDefaults
	status.Pending = pendingRestarts(s.running, newFile)
	if initial || !equalConfig(oldEff, newEff) {
		status.LoadedAt = s.clock.Now()
	}

	s.eff, s.file, s.status = newEff, newFile, status
	s.mu.Unlock()
	if initial {
		return
	}

	changed := changedPaths(oldEff, newEff)
	restart := restartsRaised(oldFile, newFile, s.running)
	if len(changed) == 0 && len(restart) == 0 && statusEqual(oldStatus, status) {
		return
	}
	s.notify(Change{Source: src, Old: oldEff.Clone(), New: newEff.Clone(), Changed: changed, Restart: restart, Status: status.clone()})
}

// statusOf derives the Status of a file that loaded.
func statusOf(r loadResult) Status {
	st := Status{Problems: r.probs}
	if len(r.probs) == 0 {
		return st
	}
	st.Severity = SeverityWarning
	st.Code = r.probs[0].Code
	for _, p := range r.probs {
		if p.Code == "" {
			st.Code = CodeNodeYAMLInvalid
			break
		}
	}
	return st
}

func statusEqual(a, b Status) bool {
	return a.Code == b.Code && a.Severity == b.Severity && a.Safe == b.Safe &&
		equalAny(a.Problems, b.Problems) && equalAny(a.Pending, b.Pending)
}

// CheckNow looks at node.yaml once and applies it if it changed since the last look (mtime or size
// differ and the content hash differs). It is what each poll tick runs. It reports whether the file
// changed.
func (s *Store) CheckNow() bool {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.refresh(false, false)
}

// ReloadNow re-reads and re-validates node.yaml even if it did not change, for when something the
// validation depends on changed (the catalog loaded, a block validator was registered).
func (s *Store) ReloadNow() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.refresh(true, false)
}

// Name implements the app worker interface.
func (s *Store) Name() string { return "config" }

// Start begins the reload poll (every Options.Interval, 10 s by default) and returns at once. The
// loop ends when ctx is cancelled or Stop is called. A second Start is an error.
func (s *Store) Start(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.cancel != nil {
		return errors.New("config: store already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	ticker := s.clock.NewTicker(s.interval)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C():
				s.CheckNow()
			}
		}
	}()
	return nil
}

// Stop ends the poll and waits for it, within ctx.
func (s *Store) Stop(ctx context.Context) error {
	s.runMu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.runMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
