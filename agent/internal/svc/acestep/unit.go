// Package acestep is the node's side of the music engine (E11, issues #508 and #511): the
// localai-music.service unit, the environment file the agent writes before it starts the unit, the
// engine adapter the load orchestrator uses for engine: acestep models (internal/router.EngineAdapter),
// and the doctor checks of the runtime. The engine itself is our Python shim around ACE-Step
// (engines/acestep, package localai_music_shim), installed by ensure into
// /opt/localai/acestep/<version>/venv. The song routes and the queue belong to internal/music.
//
// Everything outside the process (systemd, the shim's HTTP port, the disk, the GPU) is behind an
// interface, so the tests run on fakes and never start a real unit.
package acestep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Canonical names of the music engine (overview.md section 3, E11 contract section 1).
const (
	// UnitName is localai-music.service: never enabled at boot, started and stopped by the agent.
	UnitName = services.UnitMusic
	// RuntimeRoot holds one directory per installed version and the current link.
	RuntimeRoot = "/opt/localai/acestep"
	// Current is the link to the version in use.
	Current = RuntimeRoot + "/current"
	// Python is the interpreter of the runtime in use.
	Python = Current + "/venv/bin/python"
	// ShimModule is the module the unit runs (engines/acestep in the repository).
	ShimModule = "localai_music_shim"
	// EnvPath is the unit's environment file, written by the agent before every start.
	EnvPath = "/etc/localai/music.env"
	// StateDir is the engine's working area; WorkDir its per-task scratch, SongsDir the song store.
	StateDir = "/var/lib/localai/music"
	WorkDir  = StateDir + "/work"
	SongsDir = StateDir + "/songs"
	// ContentDir is where acestep models live on a content root (music/<model-id>).
	ContentDir = "music"

	unitFileMode os.FileMode = 0o644
)

// DefaultUnitPath returns /etc/systemd/system/localai-music.service under root ("" means "/").
func DefaultUnitPath(root string) string {
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, "etc", "systemd", "system", UnitName)
}

// unitText is the unit, byte for byte. Nothing in it varies: the port, the model and the device come
// from music.env. It has no [Install] section on purpose: the unit is never enabled, so it never
// starts at boot and never takes graphics memory a survival model needs; only the engine adapter
// starts it when an acestep model is loaded. Restart=no: a crash is the agent's to report (and the
// next load starts it again), never a loop that holds the GPU. The network is loopback only.
const unitText = `[Unit]
Description=Local AI music engine (ACE-Step shim)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/music.md
After=local-fs.target
ConditionPathExists=/etc/localai/music.env
ConditionPathExists=/opt/localai/acestep/current/venv/bin/python

[Service]
Type=simple
User=localai
Group=localai
SupplementaryGroups=video render
EnvironmentFile=/etc/localai/music.env
Environment=HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1
ExecStart=/opt/localai/acestep/current/venv/bin/python -m localai_music_shim --host 127.0.0.1 --port ${MUSIC_PORT}
WorkingDirectory=/var/lib/localai/music
Restart=no
TimeoutStartSec=30
TimeoutStopSec=20
KillMode=control-group
LimitMEMLOCK=infinity
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/localai/music
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
IPAddressDeny=any
IPAddressAllow=localhost
LockPersonality=yes
RestrictRealtime=yes
SystemCallArchitectures=native
`

// RenderUnit returns the text of localai-music.service. It is pure and deterministic.
func RenderUnit() []byte { return []byte(unitText) }

// DaemonReloader runs systemctl daemon-reload; internal/services.Manager implements it.
type DaemonReloader interface {
	DaemonReload(ctx context.Context) error
}

// UnitWriter writes the unit and reloads systemd. It never starts, stops or enables the unit.
type UnitWriter struct {
	// Path is the unit file; default DefaultUnitPath("").
	Path string
	// FS is the disk; default config.OSFS.
	FS config.FS
	// UID and GID own the file (root:root, the zero value); -1 leaves an id alone (tests).
	UID, GID int
	// Systemd runs daemon-reload; required.
	Systemd DaemonReloader
}

// Write replaces the unit file atomically (root 0644) when its bytes differ and runs daemon-reload.
// It reports whether the file was written.
func (w *UnitWriter) Write(ctx context.Context) (bool, error) {
	path := w.Path
	if path == "" {
		path = DefaultUnitPath("")
	}
	if w.Systemd == nil {
		return false, errors.New("acestep: the unit writer needs a Systemd (daemon-reload)")
	}
	fsys := w.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	data := RenderUnit()
	cur, rerr := fsys.ReadFile(path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return false, fmt.Errorf("acestep: read %s: %w", path, rerr)
	}
	written := false
	if rerr != nil || !bytes.Equal(cur, data) {
		if err := config.WriteFileAtomicFS(fsys, path, data, unitFileMode, w.UID, w.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
			return false, fmt.Errorf("acestep: write %s: %w", path, err)
		}
		written = true
	}
	if err := w.Systemd.DaemonReload(ctx); err != nil {
		return written, fmt.Errorf("acestep: daemon-reload after writing %s: %w", path, err)
	}
	return written, nil
}
