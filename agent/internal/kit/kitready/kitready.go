// Package kitready connects the kit verifier to the readiness report (rebuild-kit.md section 4.4,
// ark-sync.md sections 4.12.2 and 4.12.3, KIT-03).
//
// Source implements ready.KitSource: at every READY refresh (after a kit item changes, at mount and
// every 6 hours) the readiness service asks it for the kit findings of one root, and it answers from a
// quick verification (nothing is re-hashed: a refresh must be cheap, and the re-hash is the job of
// `aictl kit verify` and of sync's re-verification). The colours (LAI-KIT-040 red on the internal root
// and yellow on a drive, 041 and 043 yellow, 042 red) are the verifier's own, and ready.KitColour turns
// the findings into the problems of READY.json.
//
// The package sits beside internal/kit rather than in it because internal/ark/ready imports the
// planner, which imports internal/kit.
package kitready

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Source is ready.KitSource over the kit verifier. Fill Verifier, Roots and Now; the rest is optional.
type Source struct {
	Verifier kit.Verifier
	// Roots returns the mounted content roots (id, path, internal or drive).
	Roots func() []kit.StatusRoot
	// Node returns the facts about this computer (node.yaml, os-release, the driver); nil, or a nil
	// answer, means unknown and only the rules that need no fact run.
	Node func() *kit.NodeFacts
	// Keys are the trusted minisign keys (update.EmbeddedKeys()).
	Keys []update.PublicKey
	// Now is the clock.
	Now func() time.Time
	// ReverifyDays is sync.reverify_days; 0 is the default.
	ReverifyDays int

	mu   sync.Mutex
	last map[string]kit.VerifyReport
}

var _ ready.KitSource = (*Source)(nil)

// KitReport implements ready.KitSource: the findings of the quick verification of one root. A root the
// source does not know, or one with no kit/ folder, has none (the kit rows are then judged by age
// alone). Only the findings READY reads are returned: LAI-KIT-040 to 043, and not the optional ones (a
// missing prebuilt is a warning of `kit verify` that never changes a colour).
func (s *Source) KitReport(rootID string) ready.KitReport {
	rep, err := s.Verify(context.Background(), rootID, nil)
	if err != nil {
		return ready.KitReport{}
	}
	return Findings(rep)
}

// Findings reduces a report to what READY reads.
func Findings(rep kit.VerifyReport) ready.KitReport {
	var out ready.KitReport
	for _, f := range rep.Findings {
		switch f.Code {
		case doctor.KIT040, doctor.KIT041, doctor.KIT042, doctor.KIT043:
			if !f.Optional {
				out.Findings = append(out.Findings, ready.KitFinding{Code: f.Code, Item: f.Item})
			}
		}
	}
	return out
}

// Verify runs the quick verification of a root, optionally changing the options first (the drill
// passes its own: steps 1 to 5, no build). The report is kept for Last.
func (s *Source) Verify(ctx context.Context, rootID string, change func(*kit.Options)) (kit.VerifyReport, error) {
	var root kit.StatusRoot
	found := false
	for _, r := range s.Roots() {
		if r.ID == rootID {
			root, found = r, true
			break
		}
	}
	if !found {
		return kit.VerifyReport{}, kit.ErrNoKit
	}
	opts := kit.Options{
		Mode: kit.ModeQuick, Now: s.Now(), ReverifyDays: s.ReverifyDays,
		RootID: root.ID, Internal: root.Internal, Keys: s.Keys,
	}
	if s.Node != nil {
		opts.Node = s.Node()
	}
	if change != nil {
		change(&opts)
	}
	rep, err := s.Verifier.Verify(ctx, root.Path, opts)
	if err != nil {
		if errors.Is(err, kit.ErrNoKit) {
			s.forget(rootID)
		}
		return kit.VerifyReport{}, err
	}
	s.mu.Lock()
	if s.last == nil {
		s.last = map[string]kit.VerifyReport{}
	}
	s.last[rootID] = rep
	s.mu.Unlock()
	return rep, nil
}

// Last is the most recent report of a root that Verify (or a READY refresh) made.
func (s *Source) Last(rootID string) (kit.VerifyReport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rep, ok := s.last[rootID]
	return rep, ok
}

func (s *Source) forget(rootID string) {
	s.mu.Lock()
	delete(s.last, rootID)
	s.mu.Unlock()
}
