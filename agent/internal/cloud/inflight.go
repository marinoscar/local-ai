package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// InflightPath is /var/lib/localai/cloud/inflight.json (agent.md section 4.8.6, step 3).
const InflightPath = "/var/lib/localai/cloud/inflight.json"

// InflightEntry is a cloud command the agent is running: what a later process needs to report it
// (the claim token is the cloud's lease handle for this one command; the file is root 0600 and the
// entry is removed when the command settles, so nothing outlives the command; it is never logged).
// An entry whose lease may still be valid when the agent starts is reported as failed
// (LAI-AGT-017), except agent.update, which AGENT-19 settles after the restart it causes.
type InflightEntry struct {
	JobID string `json:"job_id"`
	// Type is the full type, "localai.model.load".
	Type string `json:"type"`
	// Plane is localai or eab: the paths to settle on.
	Plane string `json:"plane"`
	// ClaimToken is omitted when the assignment had none.
	ClaimToken string `json:"claim_token,omitempty"`
	// LeaseExpiresAt is the end of the lease as last known (the claim answer, then each renewal).
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	StartedAt      time.Time `json:"started_at"`
}

type inflightFile struct {
	Version int             `json:"version"`
	Jobs    []InflightEntry `json:"jobs"`
}

// ErrInflightCorrupt: inflight.json exists but is not a valid inflight file.
var ErrInflightCorrupt = errors.New("cloud: inflight.json is not valid")

// InflightStore is inflight.json. It holds every command in flight (one on the localai plane, up to
// cloud.concurrency on the EAB plane). The Claimer is its only writer while it runs; the update code
// (AGENT-19) reads it at startup through Load.
type InflightStore struct {
	Path string
	FS   config.FS
}

func (s InflightStore) fs() config.FS {
	if s.FS == nil {
		return config.OSFS{}
	}
	return s.FS
}

func (s InflightStore) path() string {
	if s.Path == "" {
		return InflightPath
	}
	return s.Path
}

// Load returns the entries; none when the file is missing. A file that cannot be parsed is
// ErrInflightCorrupt (the caller deletes it: a command is never re-run from it, only reported).
func (s InflightStore) Load() ([]InflightEntry, error) {
	data, err := s.fs().ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f inflightFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 {
		return nil, ErrInflightCorrupt
	}
	return f.Jobs, nil
}

// Save writes the entries atomically, root 0600. No entries removes the file.
func (s InflightStore) Save(entries []InflightEntry) error {
	if len(entries) == 0 {
		if err := s.fs().Remove(s.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete inflight.json: %w", err)
		}
		return nil
	}
	data, err := json.MarshalIndent(inflightFile{Version: 1, Jobs: entries}, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path()); dir != "." {
		if _, ok := s.fs().(config.OSFS); ok {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		}
	}
	if err := config.WriteFileAtomicFS(s.fs(), s.path(), append(data, '\n'), 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return fmt.Errorf("write inflight.json: %w", err)
	}
	return nil
}
