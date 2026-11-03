package app

import (
	"path/filepath"
	"strings"
)

// Paths are the files and directories of agent.md section 3.3, under a filesystem root. The root is
// "/" in production; an integration test points it at a temp directory so the whole daemon runs
// without touching the machine. The canonical names are written once, here, and every subsystem is
// handed its own path from this table.
type Paths struct {
	// Root is the filesystem root ("" and "/" both mean the real one).
	Root string
}

// Canonical absolute paths (overview section 3.2).
const (
	etcDir      = "/etc/localai"
	varDir      = "/var/lib/localai"
	runDir      = "/run/localai"
	internalDir = "/srv/localai"
	optDir      = "/opt/localai"
)

// Join returns the absolute canonical path abs under the root.
func (p Paths) Join(abs string) string {
	root := p.Root
	if root == "" || root == "/" {
		return filepath.Clean(abs)
	}
	return filepath.Join(root, strings.TrimPrefix(abs, "/"))
}

// Etc is /etc/localai.
func (p Paths) Etc() string { return p.Join(etcDir) }

// Var is /var/lib/localai.
func (p Paths) Var() string { return p.Join(varDir) }

// Run is /run/localai.
func (p Paths) Run() string { return p.Join(runDir) }

// NodeYAML is /etc/localai/node.yaml.
func (p Paths) NodeYAML() string { return p.Join(etcDir + "/node.yaml") }

// APIKey is /etc/localai/api-key.
func (p Paths) APIKey() string { return p.Join(etcDir + "/api-key") }

// AgentToken is /etc/localai/agent-token.
func (p Paths) AgentToken() string { return p.Join(etcDir + "/agent-token") }

// AdminPIN is /etc/localai/admin-pin.
func (p Paths) AdminPIN() string { return p.Join(etcDir + "/admin-pin") }

// CloudCredential is /etc/localai/cloud-credential.
func (p Paths) CloudCredential() string { return p.Join(etcDir + "/cloud-credential") }

// Catalog is /etc/localai/catalog.
func (p Paths) Catalog() string { return p.Join(etcDir + "/catalog") }

// State is /var/lib/localai/state.json.
func (p Paths) State() string { return p.Join(varDir + "/state.json") }

// Audit is /var/lib/localai/audit.jsonl.
func (p Paths) Audit() string { return p.Join(varDir + "/audit.jsonl") }

// CloudLink is /var/lib/localai/cloud/link.json.
func (p Paths) CloudLink() string { return p.Join(varDir + "/cloud/link.json") }

// Inflight is /var/lib/localai/cloud/inflight.json.
func (p Paths) Inflight() string { return p.Join(varDir + "/cloud/inflight.json") }

// DrillDir is /var/lib/localai/drill.
func (p Paths) DrillDir() string { return p.Join(varDir + "/drill") }

// Socket is /run/localai/agent.sock.
func (p Paths) Socket() string { return p.Join(runDir + "/agent.sock") }

// InternalRoot is the internal content root /srv/localai.
func (p Paths) InternalRoot() string { return p.Join(internalDir) }

// BuildJSON is /opt/localai/llama.cpp/current/BUILD.json.
func (p Paths) BuildJSON() string { return p.Join(optDir + "/llama.cpp/current/BUILD.json") }

// KiwixUnit is /etc/systemd/system/localai-kiwix.service.
func (p Paths) KiwixUnit() string { return p.Join("/etc/systemd/system/localai-kiwix.service") }

// DataDirs are the directories startup step 5 makes sure of, besides the run directory: the state
// directory's sync, drill, cloud and update folders (agent.md section 4.1 step 5).
func (p Paths) DataDirs() []string {
	return []string{
		p.Join(varDir + "/sync"), p.Join(varDir + "/drill"), p.Join(varDir + "/cloud"), p.Join(varDir + "/update"),
	}
}
