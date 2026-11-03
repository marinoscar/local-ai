package drill

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/i18n"
)

// Reporting (DRILL-06, drill.md section 4.5): what the rest of the agent and the cloud learn from the
// drill's documents. The Reporter is the one in-memory view of them. The alert checks (alerts.go) read
// it, and it is the cloud link's DrillSource, so the snapshot's drill block and the alerts can never
// disagree about which run is the last one.

// Observer is told of every document the engine saves: a finished run (any trigger), an aborted one
// and a manual_plug entry. *Reporter implements it. A skipped run is not saved through it.
type Observer interface {
	Observe(doc *Document)
}

func (e *Engine) observe(doc *Document) {
	if e.d.Observer != nil && doc != nil {
		e.d.Observer.Observe(doc)
	}
}

// Keys of the notification sentences of internal/i18n.
const (
	noticePassKey     = "drill.notify.pass"
	noticeProblemsKey = "drill.notify.problems"
)

// NoticeFor returns the desktop notification of a run (drill.md section 4.5): a scheduled run that
// completed says everything works offline, or that problems were found when it ended in warn or fail.
// Every other run (manual, aborted, skipped, a manual_plug entry) has none.
func NoticeFor(doc *Document) (events.DrillNotice, bool) {
	if doc == nil || doc.Trigger != TriggerScheduled || !completed(doc) {
		return events.DrillNotice{}, false
	}
	key := noticeProblemsKey
	if doc.Overall == OverallPass {
		key = noticePassKey
	}
	return events.DrillNotice{EN: i18n.T(i18n.EN, key), ES: i18n.T(i18n.ES, key)}, true
}

// doneEvent is the data of drill.done for a run's final document.
func doneEvent(doc *Document) events.DrillDone {
	ev := events.DrillDone{RunID: doc.RunID, Overall: doc.Overall, SurvivalReady: doc.SurvivalReady, Trigger: string(doc.Trigger)}
	if n, ok := NoticeFor(doc); ok {
		ev.Notice = &n
	}
	return ev
}

// completed reports whether a document is the result of a run that finished its checks: pass, warn
// or fail, with no state (an aborted, skipped or running document is not a drill result).
func completed(doc *Document) bool {
	if doc == nil || doc.State != "" || doc.FinishedAt == nil {
		return false
	}
	switch doc.Overall {
	case OverallPass, OverallWarn, OverallFail:
		return true
	}
	return false
}

// manualEntry reports whether a document is a usable manual_plug entry.
func manualEntry(doc *Document) bool {
	return doc != nil && doc.Trigger == TriggerManualPlug && doc.State == "" && doc.FinishedAt != nil &&
		(doc.Overall == OverallPass || doc.Overall == OverallFail)
}

// automatic reports whether a document is a completed run of the drill itself (scheduled, manual or
// cloud trigger, with checks), as opposed to a manual_plug record.
func automatic(doc *Document) bool {
	return doc != nil && doc.Trigger != TriggerManualPlug && completed(doc)
}

// Limits of the cloud's DrillResultsV1 (cloud-app.md section 3.6).
const (
	maxChecks      = 100
	maxFailed      = 50
	maxCheckIDLen  = 96
	maxDetailLen   = 500
	maxDetailKeys  = 50
	maxDetailKey   = 64
	maxModels      = 16
	maxTriggerLen  = 32
	maxAreaLen     = 32
	maxRunIDLen    = 64
	maxAgentVerLen = 64
	maxCatalogLen  = 32
)

var (
	codePattern    = regexp.MustCompile(`^LAI-[A-Z]{2,3}-[0-9]{3}$`)
	modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{2,63}$`)
)

// LastRun is the last completed run of the drill, as the alert checks read it.
type LastRun struct {
	RunID      string
	FinishedAt time.Time
	// Overall is pass, warn or fail.
	Overall       string
	Trigger       Trigger
	SurvivalReady bool
	// FailedChecks are the doctor codes of the checks that failed or warned (and of a fence dial or a
	// failed restore); FailedCheckIDs are those checks' ids.
	FailedChecks   []string
	FailedCheckIDs []string
}

// ReporterOptions configure a Reporter. Results is required.
type ReporterOptions struct {
	Results *Results
	// State holds drill.watch_since and the fallback for drill.last_run_at; nil means the Reporter has
	// no anchor for the 45-day alert on a node that never ran a drill.
	State StateStore
	// Clock stamps drill.watch_since; default config.RealClock.
	Clock config.Clock
	// OnChange is called, outside any lock, after a document changed what the reporter reports. The
	// daemon wiring points it at the doctor manager's Tick so an alert shows up at once and not at the
	// next 60-second pass. It must not block.
	OnChange func()
	Logger   *slog.Logger
}

// Reporter keeps the last completed run and the last manual_plug entry in memory, from the documents
// on disk (Load) and from the ones the engine saves (Observe). It implements Observer and
// cloud.DrillSource and feeds the alert checks. It is safe for concurrent use.
type Reporter struct {
	o     ReporterOptions
	clock config.Clock
	log   *slog.Logger
	block stateBlock

	mu       sync.Mutex
	auto     *Document // newest completed run of the drill, nil if none
	autoJSON []byte    // its cloud DrillResultsV1, nil when it cannot be mapped
	manual   *Document // newest manual_plug entry, nil if none
}

var (
	_ Observer          = (*Reporter)(nil)
	_ cloud.DrillSource = (*Reporter)(nil)
)

// NewReporter returns a Reporter that has not read the disk yet: call Load once at startup.
func NewReporter(o ReporterOptions) (*Reporter, error) {
	if o.Results == nil {
		return nil, errors.New("drill: the reporter needs a results store")
	}
	r := &Reporter{o: o, clock: o.Clock, log: o.Logger}
	if r.clock == nil {
		r.clock = config.RealClock{}
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	r.log = r.log.With("component", "drill")
	if o.State != nil {
		r.block = stateBlock{store: o.State, log: r.log}
	}
	return r, nil
}

// Load reads the kept documents and, when the node has never been watched, stamps drill.watch_since
// in state.json: the date the 45-day alert counts from on a node that never ran a drill. It is called
// once at startup and is safe to call again. A damaged document is skipped, never an error.
func (r *Reporter) Load() error {
	docs, err := r.o.Results.All()
	if err != nil {
		return fmt.Errorf("drill: read the results: %w", err)
	}
	var auto, manual *Document
	for _, d := range docs { // newest first
		switch {
		case auto == nil && automatic(d):
			auto = d
		case manual == nil && manualEntry(d):
			manual = d
		}
	}
	r.mu.Lock()
	r.auto, r.manual = auto, manual
	r.autoJSON = mapResults(auto)
	r.mu.Unlock()

	if r.o.State != nil {
		if err := r.block.mutate(func(m map[string]json.RawMessage) {
			if _, ok := m[keyWatchSince]; !ok {
				put(m, keyWatchSince, r.clock.Now().UTC().Truncate(time.Second))
			}
		}); err != nil {
			r.log.Warn("could not record drill.watch_since", "error", err.Error())
		}
	}
	r.changed()
	return nil
}

// Observe implements Observer: a completed run replaces the last one when it finished later, and so
// does a manual_plug entry. An aborted document changes nothing: it is not a drill result.
func (r *Reporter) Observe(doc *Document) {
	r.mu.Lock()
	moved := false
	switch {
	case automatic(doc):
		if r.auto == nil || !finishedOf(doc).Before(finishedOf(r.auto)) {
			r.auto, r.autoJSON, moved = doc, mapResults(doc), true
		}
	case manualEntry(doc):
		if r.manual == nil || !finishedOf(doc).Before(finishedOf(r.manual)) {
			r.manual, moved = doc, true
		}
	}
	r.mu.Unlock()
	if moved {
		r.changed()
	}
}

func (r *Reporter) changed() {
	if r.o.OnChange != nil {
		r.o.OnChange()
	}
}

// LastRun returns the last completed run of the drill (not a manual_plug entry), false when none is
// kept.
func (r *Reporter) LastRun() (LastRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.auto == nil {
		return LastRun{}, false
	}
	return lastRunOf(r.auto), true
}

func lastRunOf(d *Document) LastRun {
	codes, ids := failures(d)
	return LastRun{
		RunID: d.RunID, FinishedAt: finishedOf(d).UTC(), Overall: d.Overall, Trigger: d.Trigger,
		SurvivalReady: d.SurvivalReady, FailedChecks: codes, FailedCheckIDs: ids,
	}
}

// Anchor is the instant the 45-day alert counts from: the end of the last completed run of any
// trigger except manual_plug (drill.md section 4.5), else drill.last_run_at of state.json (the
// documents may have been cleared), else drill.watch_since. The zero time means the node was never
// watched yet.
func (r *Reporter) Anchor() time.Time {
	if last, ok := r.LastRun(); ok {
		return last.FinishedAt
	}
	if r.o.State == nil {
		return time.Time{}
	}
	blk := r.block.read()
	for _, key := range []string{keyLastRunAt, keyWatchSince} {
		if raw, ok := blk[key]; ok {
			var t time.Time
			if json.Unmarshal(raw, &t) == nil && !t.IsZero() {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// Drill implements cloud.DrillSource: the snapshot's drill block input (cloud-app.md section 3.6).
//
//   - The newest completed run gives every field, and its results document without node_id and
//     without anything the strict schema does not know.
//   - A manual_plug entry that is newer than that run gives run_id, last_run_at and result only:
//     survival_ready stays the last automatic run's (false when there never was one), the failed lists
//     are empty and the results are never sent (the cloud link also refuses them for ManualPlug).
//   - An aborted run is ignored; with no completed run and no manual entry the answer is nil.
//
// The cloud link keeps track of which run's results the cloud accepted, so the same answer is given
// until the next run and the results travel once (Built.Commit).
func (r *Reporter) Drill() *cloud.DrillReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.manual != nil && (r.auto == nil || !finishedOf(r.manual).Before(finishedOf(r.auto))):
		rep := &cloud.DrillReport{
			RunID: r.manual.RunID, LastRunAt: finishedOf(r.manual).UTC(), Result: r.manual.Overall,
			FailedChecks: []string{}, FailedCheckIDs: []string{}, ManualPlug: true,
		}
		if r.auto != nil {
			rep.SurvivalReady = r.auto.SurvivalReady
		}
		return rep
	case r.auto != nil:
		last := lastRunOf(r.auto)
		return &cloud.DrillReport{
			RunID: last.RunID, LastRunAt: last.FinishedAt, Result: last.Overall, SurvivalReady: last.SurvivalReady,
			FailedChecks: last.FailedChecks, FailedCheckIDs: last.FailedCheckIDs,
			ResultsJSON: append([]byte(nil), r.autoJSON...),
		}
	}
	return nil
}

// failures lists the doctor codes and the ids of what failed or warned in a run: the checks, then the
// codes a run carries outside its checks (a refused dial of the fence, a failed restore).
func failures(d *Document) (codes, ids []string) {
	codes, ids = []string{}, []string{}
	seen := map[string]bool{}
	addCode := func(c string) {
		if codePattern.MatchString(c) && !seen[c] && len(codes) < maxFailed {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	for _, c := range d.Checks {
		if c.Result != ResultFail && c.Result != ResultWarn {
			continue
		}
		if len(ids) < maxFailed {
			ids = append(ids, clipUTF16(c.ID, maxCheckIDLen))
		}
		addCode(c.Code)
	}
	addCode(d.NetworkFence.Code)
	addCode(d.Code)
	return codes, ids
}

// mapResults renders a completed run as the cloud's DrillResultsV1: node_id and notes are dropped, the
// check details keep only strings (cut to 500), numbers, booleans and null, and every list and string
// is held to the schema's limit. It returns nil for a document that is not a completed run of the
// drill (a manual_plug entry is never mapped) or whose JSON cannot be built.
func mapResults(d *Document) []byte {
	if !automatic(d) {
		return nil
	}
	out := cloud.DrillResults{
		SchemaVersion: SchemaVersion,
		RunID:         clipUTF16(d.RunID, maxRunIDLen),
		Trigger:       clipUTF16(string(d.Trigger), maxTriggerLen),
		AgentVersion:  clipUTF16(d.AgentVersion, maxAgentVerLen),
		Mode:          oneOfStr(d.Mode, "online", "online", "offline", "survival"),
		Quick:         d.Quick,
		StartedAt:     rfc3339(d.StartedAt),
		FinishedAt:    rfc3339(*d.FinishedAt),
		Overall:       d.Overall,
		SurvivalReady: d.SurvivalReady,
		Counts:        cloud.DrillCounts{Pass: nonNeg(d.Counts.Pass), Warn: nonNeg(d.Counts.Warn), Fail: nonNeg(d.Counts.Fail), Skip: nonNeg(d.Counts.Skip)},
		Checks:        []cloud.DrillCheck{},
		Restored:      cloud.DrillRestore{LoadedModels: []string{}, OK: true},
		NetworkFence:  cloud.DrillFence{Enabled: d.NetworkFence.Enabled, BlockedDials: nonNeg(d.NetworkFence.BlockedDials)},
	}
	if cv := d.CatalogVersion; cv != "" && cloud.ValidCatalogVersion(cv) && len(cv) <= maxCatalogLen {
		out.CatalogVersion = &cv
	}
	for _, c := range d.Checks {
		if len(out.Checks) == maxChecks {
			break
		}
		if !c.Result.valid() {
			continue
		}
		ck := cloud.DrillCheck{
			ID: clipUTF16(c.ID, maxCheckIDLen), Area: clipUTF16(c.Area, maxAreaLen), Result: string(c.Result),
			MS: c.MS, Details: mapDetails(c.Details),
		}
		if ck.MS < 0 {
			ck.MS = 0
		}
		if codePattern.MatchString(c.Code) {
			ck.Code = c.Code
		}
		out.Checks = append(out.Checks, ck)
	}
	if rs := d.Restored; rs != nil {
		out.Restored.OK = rs.OK
		for _, m := range rs.LoadedModels {
			if len(out.Restored.LoadedModels) < maxModels && modelIDPattern.MatchString(m) {
				out.Restored.LoadedModels = append(out.Restored.LoadedModels, m)
			}
		}
		if modelIDPattern.MatchString(rs.DefaultModel) {
			dm := rs.DefaultModel
			out.Restored.DefaultModel = &dm
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return raw
}

// mapDetails keeps the details values the cloud schema allows: a string (cut to 500 characters), a
// number, a boolean or null. A list, an object or anything else is dropped, so a check cannot put
// free-form data into the snapshot by accident.
func mapDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		if len(out) == maxDetailKeys {
			break
		}
		key := clipUTF16(k, maxDetailKey)
		switch x := v.(type) {
		case nil:
			out[key] = nil
		case string:
			out[key] = clipUTF16(x, maxDetailLen)
		case bool:
			out[key] = x
		case json.Number:
			if f, err := x.Float64(); err == nil && finite(f) {
				out[key] = x
			}
		default:
			rv := reflect.ValueOf(v)
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				out[key] = rv.Int()
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				out[key] = rv.Uint()
			case reflect.Float32, reflect.Float64:
				if f := rv.Float(); finite(f) {
					out[key] = f
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func nonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func oneOfStr(v, def string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// clipUTF16 cuts s to at most n UTF-16 code units (how the cloud's schema counts a string's length),
// never in the middle of a character.
func clipUTF16(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}
