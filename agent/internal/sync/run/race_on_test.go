//go:build race

package run

// killTrials is the number of SIGKILL trials: the race detector makes every child process several
// times slower, so the instrumented run keeps a dozen (the plain run does the 50 of the spec).
const killTrials = 12
