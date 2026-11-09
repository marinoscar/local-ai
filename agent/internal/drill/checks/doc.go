// Package checks holds the concrete checks of the monthly drill (drill.md section 4.2), registered in
// the engine's drill.Registry by the daemon wiring.
//
// This file set is DRILL-04, the content and service checks: 3 drl.files.manifest, 4 drl.files.hash,
// 8 drl.kiwix.search, 9 drl.portal.firstaid, 10 drl.stt.*, 11 drl.tts.*, 12 drl.speech.roundtrip,
// 13 drl.maps.tile, 14 drl.maps.search, 15 drl.portal, 16 drl.docs and 19 drl.services. The other
// checks live in files of their own issues (the model checks, the kit and power checks).
//
// DRILL-03 adds the model checks: 5 drl.llm.answer.<id>, 6 drl.vlm.photo.<id>, 7 drl.llm.fit (llm.go,
// vlm.go, fit.go) and 22 drl.restore (restorecheck.go), registered by Register (models.go). They see
// only small interfaces (the model layer drill.ModelOps, the chat endpoint, the node's facts), never the
// router, systemd or the network, so every pass, warn and fail path runs against fakes.
//
// DRILL-02 adds check 1 drl.fence with the network fence adapter NetFence (fence.go) and check 2
// drl.config.offline_lint (offline_lint.go); internal/app/drill_checks_fence.go builds them with the
// paths and the embedded files of the node. Their dependencies are fields, so every path is tested with
// a fake transport, a fake file system or a temp directory.
//
// Properties of every check in this package:
//
//   - A check reaches a service only through the agent's own routes (a request to the agent as the
//     internal admin, AgentHTTP), the same code path a user's request takes, and through small
//     interfaces for the rest (the readiness reports, the re-verifier, the service supervisor). Each
//     interface has a fake in the tests, so the pass, warn and fail paths and their doctor codes are
//     table-tested without a service, a disk or the network.
//   - A check never downloads, never deletes and never writes. Check 4 hashes through ark-sync's own
//     verifier, so a damaged copy is quarantined by ark-sync's rules and by nothing in this package.
//   - A fact that only a drive can answer is a skip when the drive is not plugged in, never a fail: the
//     internal root must stand alone.
//   - Nothing waits on the wall clock: time comes from the drill.Env (and from an injected clock for
//     the hashing budget), so a test moves it by hand.
package checks
