package app

import (
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// readyModes is ready.Deps.Modes: the node's mode changes, oldest first, read from the state store's
// mode_history at the moment READY is computed (ark-sync.md section 4.12.2). A nil store has no
// history.
func readyModes(st *state.Store) func() []ready.ModeChange {
	if st == nil {
		return nil
	}
	return func() []ready.ModeChange {
		tl := st.Snapshot().ModeTimeline()
		out := make([]ready.ModeChange, 0, len(tl))
		for _, e := range tl {
			out = append(out, ready.ModeChange{At: e.At, Mode: string(e.Mode)})
		}
		return out
	}
}
