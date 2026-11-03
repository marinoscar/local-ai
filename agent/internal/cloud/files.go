package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Canonical paths (overview 3.2), joined to a root so tests can use a temporary directory.
const (
	// CredentialPath is /etc/localai/cloud-credential.
	CredentialPath = "/etc/localai/cloud-credential"
	// LinkPath is /var/lib/localai/cloud/link.json.
	LinkPath = "/var/lib/localai/cloud/link.json"
)

// ErrNoCredential: there is no cloud credential on this node (not enrolled).
var ErrNoCredential = errors.New("cloud: no credential")

// CredentialStore is /etc/localai/cloud-credential (root 0600): the node's own credential, the only
// secret the link persists (security-model.md section 2).
type CredentialStore struct {
	Path string
	FS   config.FS
}

func (s CredentialStore) fs() config.FS {
	if s.FS == nil {
		return config.OSFS{}
	}
	return s.FS
}

// Read returns the credential, ErrNoCredential when the file is missing or empty. A file with loose
// permissions is read anyway and tightened (the caller raises LAI-SEC-009 through the config
// package at startup); a symlink is refused.
func (s CredentialStore) Read() (string, error) {
	f, err := config.ReadSecretFile(s.fs(), s.Path, config.PolicyCloudCredential)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoCredential
		}
		return "", err
	}
	cred := strings.TrimSpace(string(f.Data))
	if cred == "" {
		return "", ErrNoCredential
	}
	if len(f.Issues) > 0 {
		// EnforceSecret fixes the owner first and stops there when it cannot (an agent that does not
		// run as root cannot chown), which would leave a loose mode behind. The mode is tightened
		// regardless: a credential must never stay group or world readable because the owner is wrong.
		if _, err := config.EnforceSecret(s.fs(), s.Path, config.PolicyCloudCredential); err != nil {
			_ = s.fs().Chmod(s.Path, config.PolicyCloudCredential.Mode)
		}
	}
	return cred, nil
}

// Write stores the credential atomically with mode 0600 (owner root when the agent runs as root).
func (s CredentialStore) Write(cred string) error {
	if err := config.WriteFileAtomicFS(s.fs(), s.Path, []byte(cred+"\n"), 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return fmt.Errorf("write the cloud credential: %w", err)
	}
	return nil
}

// Delete removes the credential; a missing file is fine.
func (s CredentialStore) Delete() error {
	if err := s.fs().Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete the cloud credential: %w", err)
	}
	return nil
}

// LinkRecord is link.json: what the agent remembers of the registration (agent.md section 4.8.2).
type LinkRecord struct {
	URL          string    `json:"url"`
	ServerNodeID string    `json:"server_node_id"`
	Name         string    `json:"name"`
	RegisteredAt time.Time `json:"registered_at"`
	// ServerFeatures are the features of the register or heartbeat answer (node_host_vitals).
	ServerFeatures []string `json:"server_features"`
	// Handshake is the last GET .../localai/config answer; nil until one succeeded.
	Handshake *HandshakeRecord `json:"handshake,omitempty"`
}

// HandshakeRecord is the stored form of the handshake (agent.md section 4.8.2).
type HandshakeRecord struct {
	Features           []string  `json:"features"`
	CommandPlane       string    `json:"command_plane"`
	HeartbeatIntervalS int       `json:"heartbeat_interval_s"`
	StateMinIntervalS  int       `json:"state_min_interval_s"`
	StateMaxIntervalS  int       `json:"state_max_interval_s"`
	ClaimWaitMaxMs     int       `json:"claim_wait_max_ms"`
	CheckedAt          time.Time `json:"checked_at"`
}

// LinkStore is /var/lib/localai/cloud/link.json (root 0600).
type LinkStore struct {
	Path string
	FS   config.FS
}

func (s LinkStore) fs() config.FS {
	if s.FS == nil {
		return config.OSFS{}
	}
	return s.FS
}

// Load returns the record; ok is false when there is none or it is unreadable (the next register
// writes a fresh one, so a damaged file is never an error for the link).
func (s LinkStore) Load() (rec LinkRecord, ok bool) {
	data, err := s.fs().ReadFile(s.Path)
	if err != nil {
		return LinkRecord{}, false
	}
	if json.Unmarshal(data, &rec) != nil || rec.ServerNodeID == "" {
		return LinkRecord{}, false
	}
	return rec, true
}

// Save writes the record atomically, mode 0600, creating the directory.
func (s LinkStore) Save(rec LinkRecord) error {
	if rec.ServerFeatures == nil {
		rec.ServerFeatures = []string{}
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.Path); dir != "." {
		if _, ok := s.fs().(config.OSFS); ok {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", dir, err)
			}
		}
	}
	if err := config.WriteFileAtomicFS(s.fs(), s.Path, append(data, '\n'), 0o600, -1, -1); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return fmt.Errorf("write link.json: %w", err)
	}
	return nil
}

// Delete removes link.json; a missing file is fine.
func (s LinkStore) Delete() error {
	if err := s.fs().Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete link.json: %w", err)
	}
	return nil
}
