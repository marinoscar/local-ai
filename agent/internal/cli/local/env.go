// Package local holds the work of the aictl commands that run in the aictl process, as root, with no
// agent (aictl.md sections 4.11.3, 4.11.4, 4.19, 4.21, 4.25): `pin set`, `token rotate`,
// `catalog restore`, `catalog use --test`, and `backup-config` with its `--restore`. They must work
// when the agent is down or broken, so they write the files directly through internal/config,
// internal/auth, internal/audit and internal/catalog and never touch the network.
//
// The package knows nothing about flags, prompts or output: the commands in package cli do that and
// call the functions here with an Env. Every location is in Paths, so tests run in temporary
// directories standing in for /etc/localai, /var/lib/localai and /srv/localai.
package local

import (
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Group names of overview.md section 3.1.
const (
	// OperatorsGroup owns the agent token.
	OperatorsGroup = "localai-operators"
	// ServiceGroup owns the configuration directory and the catalog.
	ServiceGroup = "localai"
)

// Modes of the files these commands write (overview.md section 3.2).
const (
	ModeSecret  os.FileMode = 0o600 // admin-pin, backups
	ModeToken   os.FileMode = 0o640 // agent-token
	ModeCatalog os.FileMode = 0o640 // catalog files
	ModeCatDir  os.FileMode = 0o750 // catalog directories
)

// Paths are the locations the commands work on.
type Paths struct {
	EtcDir       string // /etc/localai
	StateDir     string // /var/lib/localai
	InternalRoot string // /srv/localai, the internal content root
	SystemdDir   string // /etc/systemd/system
	Mountinfo    string // /proc/self/mountinfo
}

// DefaultPaths are the canonical paths (overview.md section 3.2).
func DefaultPaths() Paths {
	return Paths{
		EtcDir:       "/etc/localai",
		StateDir:     "/var/lib/localai",
		InternalRoot: "/srv/localai",
		SystemdDir:   "/etc/systemd/system",
		Mountinfo:    "/proc/self/mountinfo",
	}
}

// PINFile is /etc/localai/admin-pin.
func (p Paths) PINFile() string { return filepath.Join(p.EtcDir, "admin-pin") }

// TokenFile is /etc/localai/agent-token.
func (p Paths) TokenFile() string { return filepath.Join(p.EtcDir, "agent-token") }

// NodeYAML is /etc/localai/node.yaml.
func (p Paths) NodeYAML() string { return filepath.Join(p.EtcDir, "node.yaml") }

// CatalogDir is /etc/localai/catalog.
func (p Paths) CatalogDir() string { return filepath.Join(p.EtcDir, "catalog") }

// CatalogPrev is /etc/localai/catalog.prev.
func (p Paths) CatalogPrev() string { return filepath.Join(p.EtcDir, "catalog.prev") }

// AuditFile is /var/lib/localai/audit.jsonl.
func (p Paths) AuditFile() string { return filepath.Join(p.StateDir, "audit.jsonl") }

// StateFile is /var/lib/localai/state.json.
func (p Paths) StateFile() string { return filepath.Join(p.StateDir, "state.json") }

// Env is everything the commands read from the machine, so tests can replace it.
type Env struct {
	Paths Paths
	// RootUID and RootGID own the secret files (0 and 0; tests use the ids of the test user, because
	// only root may chown to another owner).
	RootUID, RootGID int
	// LookupGroup and LookupUser resolve a name to a numeric id (os/user by default).
	LookupGroup func(name string) (int, error)
	LookupUser  func(name string) (int, error)
	// GroupName and UserName resolve an id to a name; "" when the id has no name.
	GroupName func(gid int) string
	UserName  func(uid int) string
	// Now is the clock.
	Now func() time.Time
	// Runner runs the few external programs (blkid, systemctl) with a fixed argv. Nil means none are
	// available.
	Runner execx.Runner
	// LookPath finds a program in the fixed PATH (execx.Lookup by default).
	LookPath func(name string) (string, error)
	// FS is the filesystem the atomic writes go through (config.OSFS when nil); tests inject faults.
	FS config.FS
	// AgentVersion is recorded in a backup manifest.
	AgentVersion string
}

// DefaultEnv is the real machine.
func DefaultEnv(agentVersion string) Env {
	return Env{
		Paths:   DefaultPaths(),
		RootUID: 0, RootGID: 0,
		LookupGroup: func(name string) (int, error) {
			g, err := user.LookupGroup(name)
			if err != nil {
				return -1, err
			}
			return strconv.Atoi(g.Gid)
		},
		LookupUser: func(name string) (int, error) {
			u, err := user.Lookup(name)
			if err != nil {
				return -1, err
			}
			return strconv.Atoi(u.Uid)
		},
		GroupName: func(gid int) string {
			if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
				return g.Name
			}
			return ""
		},
		UserName: func(uid int) string {
			if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
				return u.Username
			}
			return ""
		},
		Now:          time.Now,
		Runner:       execx.Exec{},
		LookPath:     execx.Lookup,
		AgentVersion: agentVersion,
	}
}

func (e Env) fsys() config.FS {
	if e.FS != nil {
		return e.FS
	}
	return config.OSFS{}
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Audit actions of these commands that internal/audit has no constant for (agent.md section 4.11).
const (
	ActionCatalogRestore = "catalog.restore"
	ActionCatalogUseTest = "catalog.use_test"
	ActionConfigBackup   = "config.backup"
	ActionConfigRestore  = "config.restore"
	// ActionHFTokenSet is `aictl ark hf-token set`; the token is never in the record.
	ActionHFTokenSet = "hf_token.set"
	// ActionInventoryImport is `aictl docs inventory import`; no inventory value is in the record.
	ActionInventoryImport = "inventory.import"
)

// Audit appends one record as actor cli-root. The log is opened without rotation (only the agent
// rotates), the record goes in one write(2) on an O_APPEND descriptor and is fsync'd, and the log is
// closed again; nothing is mirrored to the console. The error, if any, is the audit log's own
// (LAI-AGT-013): callers report it as a warning, because the action it records has already happened.
func (e Env) Audit(action string, result audit.Result, code string, target map[string]string) error {
	log, _ := audit.Open(e.Paths.AuditFile(),
		audit.WithoutRotation(),
		audit.WithClock(e.now),
		audit.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer log.Close()
	return log.Write(audit.Event{Action: action, Result: result, Code: code, Actor: audit.CLIRoot(), Target: target})
}
