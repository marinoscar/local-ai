package drill

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
)

// The keys of the state.json drill block (agent.md section 3.5, drill.md section 4.3). The block is
// stored raw by the state package, so a key this code does not know survives a rewrite.
const (
	keyOwner      = "_owner"
	keyLastRunAt  = "last_run_at"
	keyLastResult = "last_result"
	keyInProgress = "in_progress"
	keyDeferred   = "deferred"
	ownerName     = "drill.md"
)

// StateStore is the part of *state.Store the drill uses. MutateSync matters: drill.in_progress must
// be on disk before the first check changes the node, or a crash would leave nothing to recover from.
type StateStore interface {
	Snapshot() state.State
	MutateSync(fn func(*state.State) error) error
}

// Captured is what the drill records before it touches the node (drill.md section 4.3).
type Captured struct {
	LoadedModels   []string `json:"loaded_models"`
	DefaultModel   string   `json:"default_model"`
	WhisperRunning bool     `json:"whisper_running"`
}

func (c Captured) normalized() Captured {
	if c.LoadedModels == nil {
		c.LoadedModels = []string{}
	}
	return c
}

// InProgress is the drill.in_progress object.
type InProgress struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	Captured  Captured  `json:"captured"`
}

// deferral is the persisted retry schedule of a scheduled run that a precondition held back
// (drill.md section 4.1: a sync that is running is retried daily for 7 days, a busy llama every 2
// hours for 48 hours and then the drill runs as --quick). It lives in state.json, so an agent
// restart keeps the schedule.
type deferral struct {
	Code        string    `json:"code"`
	Quick       bool      `json:"quick"`
	Only        []string  `json:"only,omitempty"`
	FirstAt     time.Time `json:"first_at"`
	NextRetryAt time.Time `json:"next_retry_at"`
	DeadlineAt  time.Time `json:"deadline_at"`
	Attempts    int       `json:"attempts"`
}

// stateBlock reads and writes the drill block.
type stateBlock struct {
	store StateStore
	log   *slog.Logger
}

func parseBlock(raw json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// read returns the block as a map of raw values.
func (b stateBlock) read() map[string]json.RawMessage {
	return parseBlock(b.store.Snapshot().Drill)
}

// mutate runs fn on the block under MutateSync and stores the result, stamping _owner.
func (b stateBlock) mutate(fn func(m map[string]json.RawMessage)) error {
	return b.store.MutateSync(func(st *state.State) error {
		m := parseBlock(st.Drill)
		fn(m)
		if _, ok := m[keyOwner]; !ok {
			m[keyOwner] = json.RawMessage(`"` + ownerName + `"`)
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		st.Drill = raw
		return nil
	})
}

func put(m map[string]json.RawMessage, key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	m[key] = raw
}

// inProgress returns drill.in_progress, nil when null or absent. A value that does not parse is
// treated as absent: there is nothing the restore could do with it.
func (b stateBlock) inProgress() *InProgress {
	raw, ok := b.read()[keyInProgress]
	if !ok || string(raw) == "null" {
		return nil
	}
	var p InProgress
	if err := json.Unmarshal(raw, &p); err != nil || p.RunID == "" {
		b.log.Warn("drill.in_progress is unreadable and is ignored", "error", errText(err))
		return nil
	}
	p.Captured = p.Captured.normalized()
	return &p
}

func (b stateBlock) setInProgress(p InProgress) error {
	p.Captured = p.Captured.normalized()
	return b.mutate(func(m map[string]json.RawMessage) { put(m, keyInProgress, p) })
}

func (b stateBlock) clearInProgress() error {
	return b.mutate(func(m map[string]json.RawMessage) { m[keyInProgress] = json.RawMessage("null") })
}

// setLast records the last completed run (drill.last_run_at, drill.last_result).
func (b stateBlock) setLast(at time.Time, result string) error {
	return b.mutate(func(m map[string]json.RawMessage) {
		put(m, keyLastRunAt, at.UTC())
		put(m, keyLastResult, result)
	})
}

func (b stateBlock) deferral() *deferral {
	raw, ok := b.read()[keyDeferred]
	if !ok || string(raw) == "null" {
		return nil
	}
	var d deferral
	if err := json.Unmarshal(raw, &d); err != nil || d.Code == "" {
		b.log.Warn("drill.deferred is unreadable and is ignored", "error", errText(err))
		return nil
	}
	return &d
}

func (b stateBlock) setDeferral(d *deferral) error {
	return b.mutate(func(m map[string]json.RawMessage) {
		if d == nil {
			delete(m, keyDeferred)
			return
		}
		put(m, keyDeferred, d)
	})
}

func errText(err error) string {
	if err == nil {
		return "missing run_id"
	}
	return err.Error()
}
