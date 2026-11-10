package ensure

import (
	"errors"
	"fmt"

	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// The allowlist (ADR-0010): the executor runs a step only when its id, kind, target and confirmation flag
// are exactly one row of this table. A step made up by a bug, a stale plan or a hostile caller is refused
// before anything is touched. The agent binary, apt packages outside the verified kit repository, a
// deletion, a download and a shell command are not in the table and cannot be added by data.

// ErrNotAllowed is returned for a step that is not one row of the allowlist.
var ErrNotAllowed = errors.New("ensure: this action is not on the allowlist")

type allowRow struct {
	kind    Kind
	target  string
	confirm bool
}

// allowlist is keyed by step id.
var allowlist = map[string]allowRow{
	IDKiwix:          {KindTool, kit.ToolKiwix, false},
	IDPmtiles:        {KindTool, kit.ToolPmtiles, false},
	IDPiper:          {KindTool, kit.ToolPiper, false},
	IDPackages:       {KindPackages, "packages", false},
	IDWhisperRestore: {KindWhisperRestore, "whisper", false},
	IDWhisperBuild:   {KindWhisperBuild, "whisper", true},
	IDRestartKiwix:   {KindRestart, "kiwix", true},
}

// Allowed checks one step against the table.
func Allowed(s Step) error {
	row, ok := allowlist[s.ID]
	switch {
	case !ok:
		return fmt.Errorf("%w: unknown step %q", ErrNotAllowed, s.ID)
	case s.Kind != row.kind || s.Target != row.target:
		return fmt.Errorf("%w: step %q is %s/%s, not %s/%s", ErrNotAllowed, s.ID, s.Kind, s.Target, row.kind, row.target)
	case s.NeedsConfirmation != row.confirm:
		return fmt.Errorf("%w: step %q has the wrong confirmation flag", ErrNotAllowed, s.ID)
	}
	if s.Kind == KindPackages {
		if len(s.Packages) == 0 {
			return fmt.Errorf("%w: the package step names no package", ErrNotAllowed)
		}
		for _, p := range s.Packages {
			if !kit.ValidPackageName(p) {
				return fmt.Errorf("%w: %q is not a package name", ErrNotAllowed, p)
			}
		}
	}
	return nil
}

// RequiresConfirmation reports whether the step with the id never runs without the caller's confirmation.
func RequiresConfirmation(id string) bool { return allowlist[id].confirm }

// KnownStep reports whether id is a step id.
func KnownStep(id string) bool { _, ok := allowlist[id]; return ok }
