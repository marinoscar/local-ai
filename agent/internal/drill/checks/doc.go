// Package checks holds the drill checks that are not tied to one subsystem's package, one file each:
// check 1 drl.fence and the network fence adapter (fence.go), and check 2 drl.config.offline_lint
// (offline_lint.go). The daemon wiring (internal/app) builds them with the paths and the embedded files
// of the node and registers them on the drill's registry; nothing registers at init time.
//
// A check here is a plain drill.Check with its dependencies as fields, so every path is tested with a
// fake transport, a fake file system or a temp directory.
package checks
