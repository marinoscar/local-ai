package item

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// Recover finishes the swap a crashed run left in flight (ark-sync.md sections 4.5.1 and 5). The
// journal's replay (journal.Open, Recovery.Intents) names the swaps that have a swap_plan and no
// swap_done; this redoes each plan, skipping the moves that are done, writes the commit if it was not
// yet written, and takes the item to verified. A swap that lost a file is failed (LAI-SYN-026) and the
// previous version is restored from .trash.
//
// The engine calls it for every intent with a Swap at start, before anything else touches the roots,
// so that no item stays half moved while the agent serves content. It returns ok false when there is
// nothing to do for this intent: no plan in flight, or the root is not mounted (an unplugged drive:
// the swap is finished when it comes back). Every other state of a crashed item (downloading,
// verifying, staged) is resumed by Machine.Run, which reads the record.
//
// Replaying twice is harmless: a swap that is complete is recognised by its committed manifest.
func (m *Machine) Recover(ctx context.Context, run *journal.Run, in journal.Intent) (Result, bool) {
	if in.Swap == nil || m.d.Roots == nil {
		return Result{}, false
	}
	for _, root := range m.d.Roots() {
		if root.ID != in.RootID {
			continue
		}
		sp, err := pathsFromPlan(root, in.ItemID, in.Version, *in.Swap)
		if err != nil {
			m.d.Logger.Error("unusable swap plan in the journal", "component", "sync", "root", in.RootID, "item", in.ItemID, "err", err.Error())
			return Result{Root: in.RootID, Item: in.ItemID, Version: in.Version, State: journal.StateFailed, Code: CodeSwapFailed, Reason: "swap", Err: err}, true
		}
		base := Result{Root: in.RootID, Item: in.ItemID, Version: in.Version}
		if cm, ok := Committed(m.d.FS, sp.D, in.Version); ok {
			return m.finishCommitted(ctx, run, sp, &cm, base), true
		}
		return m.finishSwap(ctx, run, sp, nil, nil, base, root, in.ItemID), true
	}
	return Result{}, false
}
