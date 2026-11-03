// Package drill is the engine of the monthly offline-readiness drill (drill.md, DRILL-01): the run
// lifecycle, the preconditions, an ordered registry of checks with timeouts, the results document and
// its retention, the state.json drill block, the events and the metrics.
//
// A node that has not been tested offline is not an ark, so the drill proves, without disconnecting
// the network and without changing the mode, that everything needed offline still works. This package
// is the framework every check plugs into; the checks themselves are other issues (the network fence
// and check 2: DRILL-02; the model checks and the state restore: DRILL-03; content and services:
// DRILL-04; kit, power and clock: DRILL-05; alerts, the cloud snapshot and the manual record:
// DRILL-06; the aictl surface: AICTL-10).
//
// Properties the rest of the agent relies on:
//
//   - One drill at a time. Running reports it, so the sync engine can refuse to start while it is
//     active (drill.md section 4.1 gives the other direction).
//   - The drill never publishes anything and never mutates content: a Check gets a read-only Env and
//     the system actor (the internal admin context: no token exists inside the agent).
//   - Every exit restores the node. The previous state is captured before the first check and
//     persisted in state.json (drill.in_progress), and the restore runs on completion, on Abort, on
//     Shutdown and after a panic. A crash is recovered by Recover at the next start.
//   - A check that hangs, panics or runs past its timeout fails with the code it declared, and the run
//     continues.
//   - Nothing waits on the wall clock. Time, check timeouts and the retry schedule all come from a
//     config.Clock, so tests move time by hand.
//
// The seam pattern is the agent's: the engine sees the mode engine, the ark sync engine, the router,
// the power source, the network fence, the state restorer, the state store and the event bus through
// the small interfaces in this package, so every path is tested with fakes. Nothing registers at init
// time and nothing here is wired into the daemon (internal/app does that, AGENT-20).
package drill
