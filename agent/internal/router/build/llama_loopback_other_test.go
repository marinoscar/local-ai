//go:build !linux

package build

// bringUpLoopback does nothing off Linux: the real llama build test runs only where `unshare -n`
// exists.
func bringUpLoopback() error { return nil }
