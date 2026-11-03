//go:build !race

package run

// killTrials is the number of SIGKILL trials (the spec's 50 random offsets).
const killTrials = 50
