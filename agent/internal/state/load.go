package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FindingKind names a condition the store reports. The doctor (AGENT-11) maps a kind to its code:
// KindDamaged is LAI-AGT-003 and KindUnwritable is LAI-AGT-004 (agent.md section 8.1). The codes
// themselves live only in internal/doctor/codes.go, as scripts/check-doctor-codes.sh requires.
type FindingKind string

// The two conditions the store reports.
const (
	// KindDamaged: state.json was damaged (backup used, defaults used, or written by a newer agent).
	KindDamaged FindingKind = "state_damaged"
	// KindUnwritable: the state directory cannot be written (full, read-only, no permission).
	KindUnwritable FindingKind = "state_unwritable"
)

// Severity of a finding.
type Severity string

// Severities used by the store.
const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding is one reportable condition with its doctor details.
type Finding struct {
	Kind     FindingKind
	Severity Severity
	// Message is the English doctor message of the code (agent.md section 8.1).
	Message string
	// Details for KindDamaged: source ("bak" or "defaults" or "file"), reason, schema_version,
	// corrupt_copy. For KindUnwritable: path, error, since.
	Details map[string]any
}

const (
	msgDamaged    = "The state file was damaged; the agent used the backup or started in offline mode"
	msgUnwritable = "The agent cannot write its state (/var/lib/localai full or read-only)"
)

// Source says where the loaded state came from.
type Source string

// The four sources.
const (
	SourceFile     Source = "file"     // state.json parsed
	SourceBak      Source = "bak"      // state.json.bak used
	SourceDefaults Source = "defaults" // both unusable: defaults with mode offline
	SourceFresh    Source = "fresh"    // no file at all: a new node
)

var errNoSchema = errors.New("schema_version is missing or below 1")

// parse decodes state.json bytes. An error means "corrupt" (load rule 1). A mode that is not one of
// the three is not corruption but is never applied: it becomes offline, the safe mode.
func parse(data []byte) (st State, badMode string, err error) {
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, "", err
	}
	if st.SchemaVersion < 1 {
		return State{}, "", errNoSchema
	}
	if !st.Mode.Valid() {
		badMode = string(st.Mode)
		st.Mode = ModeOffline
	}
	st.normalize()
	return st, badMode, nil
}

type loaded struct {
	st       State
	source   Source
	finding  *Finding
	diskGood bool   // state.json on disk is known to hold a good copy of what we loaded
	lastGood []byte // that copy's bytes (only when diskGood)
	// needsWrite: what is in memory differs from state.json (loaded from the backup or defaults, or a
	// mode was coerced), so the next write happens even when a mutation changes nothing.
	needsWrite bool
}

// load applies the load rules of agent.md section 3.5 and quarantines bad files.
func (s *Store) load() loaded {
	mainRaw, mainErr := s.fs.ReadFile(s.path)
	mainMissing := errors.Is(mainErr, fs.ErrNotExist)
	bakRaw, bakErr := s.fs.ReadFile(s.bakPath())
	bakMissing := errors.Is(bakErr, fs.ErrNotExist)

	var mainSt State
	var mainBadMode string
	mainReason := ""
	switch {
	case mainMissing:
		mainReason = "state.json is missing"
	case mainErr != nil:
		mainReason = "state.json cannot be read: " + mainErr.Error()
	default:
		var perr error
		mainSt, mainBadMode, perr = parse(mainRaw)
		if perr != nil {
			mainReason = "state.json is corrupt: " + perr.Error()
		}
	}
	if mainReason == "" {
		l := loaded{st: mainSt, source: SourceFile, diskGood: true, lastGood: mainRaw, needsWrite: mainBadMode != ""}
		l.finding = s.damagedFinding(SourceFile, "", mainSt.SchemaVersion, mainBadMode, "")
		return l
	}

	// state.json is unusable. Quarantine it (a missing file has nothing to keep), then try the backup.
	corruptCopy := ""
	if !mainMissing && mainErr == nil {
		corruptCopy = s.quarantine(s.path, s.path+".corrupt-")
	}
	if mainMissing && bakMissing {
		return loaded{st: newState(), source: SourceFresh}
	}
	if bakErr == nil {
		bakSt, bakBadMode, perr := parse(bakRaw)
		if perr == nil {
			l := loaded{st: bakSt, source: SourceBak}
			l.finding = s.damagedFinding(SourceBak, mainReason, bakSt.SchemaVersion, bakBadMode, corruptCopy)
			return l
		}
		mainReason += "; state.json.bak is corrupt: " + perr.Error()
		if c := s.quarantine(s.bakPath(), s.bakPath()+".corrupt-"); c != "" && corruptCopy == "" {
			corruptCopy = c
		}
	} else if !bakMissing {
		mainReason += "; state.json.bak cannot be read: " + bakErr.Error()
	}
	st := newState()
	st.Mode = ModeOffline
	l := loaded{st: st, source: SourceDefaults}
	l.finding = s.damagedFinding(SourceDefaults, mainReason, st.SchemaVersion, "", corruptCopy)
	return l
}

// damagedFinding builds the KindDamaged finding, or nil when the load was clean.
func (s *Store) damagedFinding(src Source, reason string, schemaVersion int, badMode, corruptCopy string) *Finding {
	newer := schemaVersion > SchemaVersion
	if src == SourceFile && !newer && badMode == "" {
		return nil
	}
	f := &Finding{Kind: KindDamaged, Severity: SeverityWarning, Message: msgDamaged, Details: map[string]any{"source": string(src)}}
	if src == SourceDefaults {
		f.Severity = SeverityCritical
	}
	if reason != "" {
		f.Details["reason"] = reason
	}
	if newer {
		f.Details["schema_version"] = schemaVersion
	}
	if badMode != "" {
		f.Details["invalid_mode"] = badMode
	}
	if corruptCopy != "" {
		f.Details["corrupt_copy"] = corruptCopy
	}
	return f
}

// quarantine renames a bad file to <prefix><rfc3339 UTC>, adding -2, -3 ... when that name is taken.
// It returns the new path, or "" when the rename failed (a read-only directory: the file stays).
func (s *Store) quarantine(path, prefix string) string {
	stamp := s.now().UTC().Format(time.RFC3339)
	target := prefix + stamp
	for n := 2; ; n++ {
		exists, err := s.fs.Exists(target)
		if err != nil {
			return ""
		}
		if !exists {
			break
		}
		target = prefix + stamp + "-" + strconv.Itoa(n)
	}
	if err := s.fs.Rename(path, target); err != nil {
		s.log.Warn("state: cannot keep the bad file", "path", path, "error", err)
		return ""
	}
	return target
}

// removeStrayTemps deletes temp files a killed write left behind (this store is the only writer).
func (s *Store) removeStrayTemps() {
	dir := filepath.Dir(s.path)
	names, err := s.fs.ReadDirNames(dir)
	if err != nil {
		return
	}
	base := filepath.Base(s.path)
	for _, n := range names {
		if strings.HasPrefix(n, base+tmpInfix) || strings.HasPrefix(n, base+".bak"+tmpInfix) {
			if err := s.fs.Remove(filepath.Join(dir, n)); err != nil {
				s.log.Warn("state: cannot remove a stray temp file", "name", n, "error", err)
			}
		}
	}
}

func (s *Store) bakPath() string { return s.path + ".bak" }
