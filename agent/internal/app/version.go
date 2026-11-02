package app

import (
	"encoding/json"
	"fmt"
	"runtime"
)

// Build metadata. The release build overwrites these with
//
//	-ldflags "-X github.com/marinoscar/local-ai/agent/internal/app.Version=… -X …Commit=… -X …BuiltAt=…"
//
// BuiltAt comes from SOURCE_DATE_EPOCH, never from the clock, so builds are reproducible.
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

// VersionLine is the human format printed by `aiagent version`
// (agent.md section 3.1): aiagent <semver> (<git-sha>, <goos>/<goarch>, built <rfc3339>).
func VersionLine() string {
	return fmt.Sprintf("aiagent %s (%s, %s/%s, built %s)", Version, Commit, runtime.GOOS, runtime.GOARCH, BuiltAt)
}

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	BuiltAt string `json:"built_at"`
}

// VersionJSON is the machine format printed by `aiagent version --json`. Self-update uses it to
// verify a staged binary (agent.md section 4.10), so the five keys are a contract.
func VersionJSON() string {
	b, _ := json.Marshal(versionInfo{Version, Commit, runtime.GOOS, runtime.GOARCH, BuiltAt})
	return string(b)
}
