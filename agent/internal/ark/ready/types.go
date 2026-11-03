// Package ready is the readiness report of a content root, READY.json (docs/specs/ark-sync.md
// section 4.12): the answer to "could this node survive with no internet today?". The desktop, the
// Survival Portal, `aictl ark status`, the drill and the cloud all read it.
//
// The package has two halves. ready.go, colours.go and survival.go are pure: Build turns an Input
// (the planner's output, the verified copies and their dates, the journal, free space, the mode) into
// a Report, so every colour rule is table-testable and the JSON is golden-testable. gather.go,
// service.go and io.go are the thin I/O around it: collecting an Input from the planner, the
// best-copy index and the journal, writing READY.json atomically (never on a read-only root, which
// is kept in memory), and regenerating it at mount, after a run and every six hours.
//
// One principle shapes every rule: the report never claims a readiness the node cannot deliver with
// the network cable pulled. A stale copy, a copy nobody re-verified for months, a clock that went
// backwards or a fact the node cannot know all push the colour towards red, never towards green.
package ready

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// SchemaVersion is the only schema_version this code writes and reads.
const SchemaVersion = 1

// FileName is the name of the report inside a content root.
const FileName = "READY.json"

// Colour is a green, yellow or red verdict. The order is green < yellow < red.
type Colour string

// The colours.
const (
	Green  Colour = "green"
	Yellow Colour = "yellow"
	Red    Colour = "red"
)

// Rank orders colours: green 1, yellow 2, red 3, anything else 0.
func (c Colour) Rank() int {
	switch c {
	case Green:
		return 1
	case Yellow:
		return 2
	case Red:
		return 3
	}
	return 0
}

// Valid reports whether c is one of the three colours.
func (c Colour) Valid() bool { return c.Rank() > 0 }

// Gauge is the value of the localai_ark_root_overall metric: 0 green, 1 yellow, 2 red. An unknown
// colour reads as red.
func (c Colour) Gauge() int {
	switch c {
	case Green:
		return 0
	case Yellow:
		return 1
	}
	return 2
}

// Worse returns the worse of two colours.
func Worse(a, b Colour) Colour {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Status is the status of one item of a root (ark-sync.md section 4.12.1).
type Status string

// The item statuses.
const (
	// StatusVerified: the planned version is present and verified.
	StatusVerified Status = "verified"
	// StatusStale: an older verified version is present; the planned newer one is not.
	StatusStale Status = "stale"
	// StatusMissing: no verified copy, and it should be here.
	StatusMissing Status = "missing"
	// StatusSkipped: no copy, and the planner skipped it (budget or disk).
	StatusSkipped Status = "skipped"
	// StatusFailed: no verified copy; the last attempt failed.
	StatusFailed Status = "failed"
	// StatusQuarantined: no verified copy; what was fetched failed its checksum and was set aside.
	StatusQuarantined Status = "quarantined"
	// StatusOrphan: present but not in the profile.
	StatusOrphan Status = "orphan"
)

// Statuses lists the seven statuses in the order of the counts.
var Statuses = []Status{StatusVerified, StatusStale, StatusMissing, StatusSkipped, StatusFailed, StatusQuarantined, StatusOrphan}

// Valid reports whether s is one of the seven statuses.
func (s Status) Valid() bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// The reasons an item can carry. They are short English phrases, not sentences for the operator: the
// doctor code next to them has the plain-language title and the fix in both languages. None of them
// holds a path, a URL or an error text, because READY.json goes to the cloud.
const (
	ReasonBudget         = "budget"
	ReasonDisk           = "disk"
	ReasonReadOnly       = "read_only"
	ReasonUpdateFailed   = "update failed"
	ReasonUpdatePending  = "update pending"
	ReasonDownloadFailed = "download failed"
	ReasonChecksum       = "checksum mismatch"
	ReasonNotDownloaded  = "not downloaded yet"
)

// Counts is counts: the number of items of each status. Every key is always present.
type Counts struct {
	Verified    int `json:"verified"`
	Stale       int `json:"stale"`
	Missing     int `json:"missing"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
	Quarantined int `json:"quarantined"`
	Orphan      int `json:"orphan"`
}

func (c *Counts) add(s Status) {
	switch s {
	case StatusVerified:
		c.Verified++
	case StatusStale:
		c.Stale++
	case StatusMissing:
		c.Missing++
	case StatusSkipped:
		c.Skipped++
	case StatusFailed:
		c.Failed++
	case StatusQuarantined:
		c.Quarantined++
	case StatusOrphan:
		c.Orphan++
	}
}

// Of returns the count of one status.
func (c Counts) Of(s Status) int {
	switch s {
	case StatusVerified:
		return c.Verified
	case StatusStale:
		return c.Stale
	case StatusMissing:
		return c.Missing
	case StatusSkipped:
		return c.Skipped
	case StatusFailed:
		return c.Failed
	case StatusQuarantined:
		return c.Quarantined
	case StatusOrphan:
		return c.Orphan
	}
	return 0
}

// GeneratedBy says which agent wrote the report. node_id is dropped before the report goes to the
// cloud (the cloud knows the node already).
type GeneratedBy struct {
	NodeID       string `json:"node_id"`
	AgentVersion string `json:"agent_version"`
}

// RootBlock is root: the facts of the content root the report is about. Path is dropped before the
// report goes to the cloud (a drive's mount path holds the desktop user's name).
type RootBlock struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Profile is the profile the root is judged against.
	Profile string `json:"profile"`
	// CatalogVersion is the catalog the report was made with; null when no catalog is loaded.
	CatalogVersion *string `json:"catalog_version"`
	FSTotalBytes   int64   `json:"fs_total_bytes"`
	FSFreeBytes    int64   `json:"fs_free_bytes"`
	BudgetBytes    int64   `json:"budget_bytes"`
	// UsedBytes is what the verified copies on the root weigh (orphans included); TrashBytes is what
	// superseded versions in .trash weigh.
	UsedBytes  int64 `json:"used_bytes"`
	TrashBytes int64 `json:"trash_bytes"`
	ReadOnly   bool  `json:"read_only"`
}

// LastSync is last_sync: the last sync, verify or import run that touched the root, of any trigger
// and any final state. AgeDays is whole days since FinishedAt, never negative.
type LastSync struct {
	RunID      string `json:"run_id"`
	Trigger    string `json:"trigger"`
	FinishedAt string `json:"finished_at"`
	State      string `json:"state"`
	AgeDays    int    `json:"age_days"`
}

// Item is one entry of items. Fields that do not apply to the status are omitted, not zero.
type Item struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Tier is the survival tier, 1 to 3.
	Tier int `json:"tier"`
	// Survival is true for a model of profile.survival.models, and omitted otherwise.
	Survival bool   `json:"survival,omitempty"`
	Status   Status `json:"status"`
	// Version is the version that is present on the root.
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Code is the doctor code that explains the status or a yellow or red freshness.
	Code             string `json:"code,omitempty"`
	VerifiedAt       string `json:"verified_at,omitempty"`
	LastReverifiedAt string `json:"last_reverified_at,omitempty"`
	CheckedCurrentAt string `json:"checked_current_at,omitempty"`
	// AgeDays is whole days since CheckedCurrentAt, else VerifiedAt; never negative.
	AgeDays *int `json:"age_days,omitempty"`
	// Freshness is the colour of the age column alone (not of the status). It is omitted when the item
	// has no copy, for an orphan, and for every item when the clock is wrong.
	Freshness Colour `json:"freshness,omitempty"`
	Bytes     *int64 `json:"bytes,omitempty"`
	Trust     string `json:"trust,omitempty"`
}

// Problem is one entry of problems: a doctor code, how serious the code is, the item it is about
// (empty for the root as a whole) and its plain-language title. The fix of the code is in the doctor
// table, in both languages, next to the title; UIs look it up by code.
type Problem struct {
	Code     string          `json:"code"`
	Severity doctor.Severity `json:"severity"`
	Item     string          `json:"item,omitempty"`
	Title    string          `json:"title"`
}

// Report is READY.json, schema v1.
type Report struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   string      `json:"generated_at"`
	GeneratedBy   GeneratedBy `json:"generated_by"`
	Root          RootBlock   `json:"root"`
	Overall       Colour      `json:"overall"`
	// SurvivalCapable: this root alone holds everything survival needs (survival.go). The node-level
	// answer, over every mounted root, is NodeSurvivalCapable.
	SurvivalCapable bool `json:"survival_capable"`
	// ClockWrong is true when the clock is earlier than a time the node has already seen. Every
	// freshness is then omitted and no age rule is applied.
	ClockWrong         bool      `json:"clock_wrong"`
	LastSync           *LastSync `json:"last_sync"`
	LastFullReverifyAt *string   `json:"last_full_reverify_at"`
	Counts             Counts    `json:"counts"`
	Items              []Item    `json:"items"`
	Problems           []Problem `json:"problems"`
}

// MarshalIndented is the canonical JSON of a report: two-space indent and a trailing newline. The
// same report always gives the same bytes (the field order is fixed and there are no maps).
func (r Report) MarshalIndented() ([]byte, error) {
	if r.Items == nil {
		r.Items = []Item{}
	}
	if r.Problems == nil {
		r.Problems = []Problem{}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// In returns the report with the title of every problem in lang ("es" or "en"; anything else is
// English), taken from the doctor table. The report on disk and the one the cloud receives are
// English; GET /ark answers in the language of the request.
func (r Report) In(lang string) Report {
	if len(r.Problems) == 0 {
		return r
	}
	ps := make([]Problem, len(r.Problems))
	copy(ps, r.Problems)
	for i := range ps {
		if title, _, ok := doctor.Text(ps[i].Code, lang); ok {
			ps[i].Title = title
		}
	}
	r.Problems = ps
	return r
}

// Item returns the entry of an item id.
func (r Report) Item(id string) (Item, bool) {
	for _, it := range r.Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// HasProblem reports whether the report carries code.
func (r Report) HasProblem(code string) bool {
	for _, p := range r.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// Errors of Parse and Read.
var (
	// ErrCorrupt: not JSON, a wrong type, or a field the rest of the code cannot work without.
	ErrCorrupt = errors.New("ready: READY.json is damaged")
	// ErrSchema: schema_version is not 1. A newer file is left alone.
	ErrSchema = errors.New("ready: unsupported schema_version")
)

// Parse reads a READY.json. Unknown keys are accepted (a newer agent may have written the file on a
// drive); a file that is not JSON, has another schema_version, or lacks a colour, a generation time or
// a root id is ErrCorrupt or ErrSchema.
func Parse(data []byte) (Report, error) {
	var r Report
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if r.SchemaVersion != SchemaVersion {
		return Report{}, fmt.Errorf("%w: schema_version is %d, this agent reads %d", ErrSchema, r.SchemaVersion, SchemaVersion)
	}
	if !r.Overall.Valid() {
		return Report{}, fmt.Errorf("%w: overall %q is not green, yellow or red", ErrCorrupt, r.Overall)
	}
	if _, err := time.Parse(time.RFC3339, r.GeneratedAt); err != nil {
		return Report{}, fmt.Errorf("%w: generated_at: %v", ErrCorrupt, err)
	}
	if r.Root.ID == "" {
		return Report{}, fmt.Errorf("%w: root.id is empty", ErrCorrupt)
	}
	for i, it := range r.Items {
		if it.ID == "" || !it.Status.Valid() {
			return Report{}, fmt.Errorf("%w: items[%d] has no id or an unknown status %q", ErrCorrupt, i, it.Status)
		}
	}
	return r, nil
}

// stamp formats a time as RFC 3339 UTC to the second, the format of every timestamp of the file.
func stamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }
