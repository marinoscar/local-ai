package kit

import (
	"context"
	"errors"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Status (rebuild-kit.md section 3.5, `aictl kit status`): which parts of the kit are present and
// verified on each root, for this computer and, on a drive, for every other computer of pins.json. It
// is the quick verification (nothing is re-hashed) of every root, laid out per root.

// StatusRoot is a content root to report on.
type StatusRoot struct {
	// ID is "internal", the drive's id or a path; Path the content root (the folder with kit/).
	ID       string
	Path     string
	Internal bool
}

// StatusOptions are the inputs of Status besides the roots.
type StatusOptions struct {
	Now          time.Time
	ReverifyDays int
	// Node holds the facts about this computer; nil when unknown.
	Node *NodeFacts
	// Keys are the trusted minisign keys.
	Keys []update.PublicKey
}

// RootStatus is the kit of one root.
type RootStatus struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	Internal bool   `json:"internal"`
	// HasKit is false when the root has no kit/ folder.
	HasKit    bool       `json:"has_kit"`
	Overall   string     `json:"overall,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// Parts, Node and Lab say what is present and verified: the parts of the layout, what this
	// computer needs, and what every other computer of pins.json needs.
	Parts     []PartResult     `json:"parts,omitempty"`
	Node      *NodeResult      `json:"node,omitempty"`
	Lab       []NodeResult     `json:"lab,omitempty"`
	Signature *SignatureResult `json:"signature,omitempty"`
	Findings  []Finding        `json:"findings,omitempty"`
	Notes     []Note           `json:"notes,omitempty"`
}

// StatusReport is what `aictl kit status` prints.
type StatusReport struct {
	// Overall is the worst colour of the roots that have a kit ("" when none has).
	Overall string       `json:"overall,omitempty"`
	Roots   []RootStatus `json:"roots"`
}

// Status reports the kit of every root. It re-hashes nothing, so it is fast on a drive of hundreds of
// gigabytes; `aictl kit verify` is the command that reads the files again.
func (v Verifier) Status(ctx context.Context, roots []StatusRoot, opts StatusOptions) (StatusReport, error) {
	out := StatusReport{Roots: []RootStatus{}}
	for _, root := range roots {
		rs := RootStatus{Root: root.ID, Path: root.Path, Internal: root.Internal}
		rep, err := v.Verify(ctx, root.Path, Options{
			Mode: ModeQuick, Now: opts.Now, ReverifyDays: opts.ReverifyDays,
			RootID: root.ID, Internal: root.Internal, Node: opts.Node, Keys: opts.Keys,
		})
		switch {
		case errors.Is(err, ErrNoKit):
			out.Roots = append(out.Roots, rs)
			continue
		case err != nil:
			return StatusReport{}, err
		}
		at := rep.CheckedAt
		rs.HasKit, rs.Overall, rs.CheckedAt = true, rep.Overall, &at
		rs.Parts, rs.Node, rs.Lab, rs.Signature = rep.Parts, rep.Node, rep.Lab, rep.Signature
		rs.Findings, rs.Notes = rep.Findings, rep.Notes
		out.Roots = append(out.Roots, rs)
		out.Overall = worseColour(out.Overall, rep.Overall)
	}
	return out, nil
}

func worseColour(a, b string) string {
	rank := func(c string) int {
		switch c {
		case Red:
			return 3
		case Yellow:
			return 2
		case Green:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}
