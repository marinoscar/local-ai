package ready

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// The gatherer is the part of the package that does I/O, every piece of it behind a small interface
// or function, so a test drives it with fakes and Build never learns where its input came from.

// PlanSource is what the gatherer takes from the planner: the planner's input for one root, made
// offline (no resolver is called: a report must never wait for the network, and a node that cannot
// reach the internet must be judged by what the catalog pins and what is on its disks). *plan.Gatherer
// implements it.
type PlanSource interface {
	Gather(ctx context.Context, rootID string, opt plan.GatherOptions) (plan.PlanInput, error)
}

// JournalItems is the journal's item records (*journal.Items implements it).
type JournalItems interface {
	List() []journal.Item
}

// RunSource is the sync engine's record of runs (SYNC-07 implements it). For one root it returns the
// last run that touched it, of any trigger and any final state, and when the last run that ended done
// finished (zero when none did). A nil RunSource means no run is recorded.
type RunSource interface {
	LastRuns(rootID string) (last *RunInfo, lastDone time.Time)
}

// KitSource is the kit verify report (KIT-03 implements it): the findings for one root. A nil
// KitSource means the kit rows are judged by age alone.
type KitSource interface {
	KitReport(rootID string) KitReport
}

// DocsProbe says what the render of the manuals left on a root. A nil DocsProbe means IndexDocs.
type DocsProbe interface {
	Docs(root ark.Root) DocsFacts
}

// docsIndexName is the index of the rendered manuals inside the docs item directory (agent.md
// section 3.8.29).
const docsIndexName = "index.json"

// cardKind is the kind of the survival card in the docs index.
const cardKind = "card"

// IndexDocs reads the docs index of a root (`<root>/docs/index.json`) and reports what it can tell:
// whether the pages are there and whether the survival card has a PDF. It cannot tell whether the
// manuals were rendered with the real lab values, which the index does not record; the resolver that
// renders them (SYNC-11) wraps this probe and sets Placeholders.
type IndexDocs struct {
	FS config.FS
}

// Docs implements DocsProbe.
func (p IndexDocs) Docs(root ark.Root) DocsFacts {
	f := DocsFacts{Known: true}
	data, err := p.FS.ReadFile(filepath.Join(root.Path, "docs", docsIndexName))
	if err != nil {
		f.HTMLMissing = true
		return f
	}
	var idx struct {
		Docs []struct {
			Kind string  `json:"kind"`
			HTML string  `json:"html"`
			PDF  *string `json:"pdf"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(data, &idx); err != nil || len(idx.Docs) == 0 {
		f.HTMLMissing = true
		return f
	}
	cardSeen := false
	for _, d := range idx.Docs {
		if d.HTML == "" {
			f.HTMLMissing = true
		}
		if d.Kind == cardKind {
			cardSeen = true
			if d.PDF == nil || *d.PDF == "" {
				f.CardPDFMissing = true
			}
		}
	}
	if !cardSeen {
		f.CardPDFMissing = true
	}
	return f
}

// journalFacts reads the journal's record of every item on one root.
func journalFacts(items JournalItems, rootID string) map[string]JournalFact {
	out := map[string]JournalFact{}
	if items == nil {
		return out
	}
	for _, it := range items.List() {
		if it.RootID != rootID {
			continue
		}
		f := JournalFact{State: it.State, Version: it.Version, Code: it.Code}
		if raw, ok := it.Extras[ExtraCheckedCurrentAt]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					f.CheckedCurrentAt = t
				}
			}
		}
		out[it.ItemID] = f
	}
	return out
}

// newestElsewhere is the newest verified_at among the copies on the other roots, which the planner's
// input lists per item.
func newestElsewhere(other map[string][]plan.RootCopy) time.Time {
	var newest time.Time
	for _, copies := range other {
		for _, c := range copies {
			if c.Manifest.VerifiedAt.After(newest) {
				newest = c.Manifest.VerifiedAt
			}
		}
	}
	return newest
}

// scannable reports whether a root in this state is a mounted content root: one that is read and
// served from. An ejecting, unmounted, unusable or uninitialized root has no report (a drive that was
// pulled keeps its READY.json on the drive, and nobody can read it from here).
func scannable(s ark.RootState) bool {
	switch s {
	case ark.StateReady, ark.StateReadOnly, ark.StateUnclean, "":
		return true
	}
	return false
}

// assessFailureCode is the doctor code of a mounted root that could not be assessed at all (its
// filesystem would not answer): a drive reports read/write errors, the internal root an internal error.
func assessFailureCode(kind ark.RootKind) string {
	if kind == ark.KindArkDrive {
		return CodeDriveUnreadable
	}
	return CodeInternalError
}

// isUnknownProfile reports whether err says the root's profile is not in the catalog.
func isUnknownProfile(err error) (*plan.ErrUnknownProfile, bool) {
	var e *plan.ErrUnknownProfile
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// isNotExist is errors.Is(err, fs.ErrNotExist).
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
