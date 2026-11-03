//go:build !linux

package run

// LowPriority is a no-op off Linux (there is no ioprio_set); a worker runs at normal priority.
func LowPriority(bool) error { return nil }
