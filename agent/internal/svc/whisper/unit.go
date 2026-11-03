// Package whisper holds the node's side of the speech-to-text service (companion-services.md section
// 4.2): the localai-whisper.service unit, the doctor check for the program it runs, and the base
// pieces `aictl rebuild-whisper` uses. The program is built by internal/router/build (WhisperRecipe).
// The model selection, whisper.env and the GPU or CPU rule belong to SVC-06, the /speech/stt route to
// SVC-07; this package owns none of them.
package whisper

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

// Fixed names of the speech unit (companion-services.md sections 3.2 and 3.3, overview.md section
// 3.5). They are the canonical paths of the lab: the generated text never varies with the host.
const (
	// UnitName is localai-whisper.service.
	UnitName = services.UnitWhisper
	// Root is /opt/localai/whisper.cpp: builds/ and the current symlink live under it.
	Root = "/opt/localai/whisper.cpp"
	// ServerPath is the program the unit runs; the build of the node is what current points at.
	ServerPath = Root + "/current/bin/whisper-server"
	// EnvPath is the environment file of the unit (SVC-06 writes it).
	EnvPath = "/etc/localai/whisper.env"

	unitFileMode os.FileMode = 0o644
)

// DefaultUnitPath returns /etc/systemd/system/localai-whisper.service under root ("" means "/").
func DefaultUnitPath(root string) string {
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, "etc", "systemd", "system", UnitName)
}

// unitText is the unit of companion-services.md section 3.3, byte for byte; nothing in it varies, the
// model, the threads and the device arguments come from whisper.env. $WHISPER_DEVICE_ARGS is unbraced
// on purpose: systemd splits it into words, and an empty value adds no argument. PrivateDevices is not
// set, because the GPU device nodes are needed. There is no header comment: the golden file is the
// spec text.
const unitText = `[Unit]
Description=Local AI speech-to-text (whisper.cpp server)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/companion-services.md
After=local-fs.target
ConditionPathExists=/etc/localai/whisper.env
ConditionPathExists=/opt/localai/whisper.cpp/current/bin/whisper-server

[Service]
Type=simple
User=localai
Group=localai
SupplementaryGroups=video render
EnvironmentFile=/etc/localai/whisper.env
ExecStart=/opt/localai/whisper.cpp/current/bin/whisper-server \
  --host 127.0.0.1 --port 8081 \
  --model ${WHISPER_MODEL} \
  --threads ${WHISPER_THREADS} \
  --language auto \
  $WHISPER_DEVICE_ARGS
WorkingDirectory=/var/lib/localai
Restart=on-failure
RestartSec=5
LimitMEMLOCK=infinity
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`

// RenderUnit returns the text of localai-whisper.service. It is pure and deterministic.
func RenderUnit() []byte { return []byte(unitText) }

// DaemonReloader runs systemctl daemon-reload; internal/services.Manager implements it.
type DaemonReloader interface {
	DaemonReload(ctx context.Context) error
}

// UnitWriter writes the unit and reloads systemd.
type UnitWriter struct {
	// Path is the unit file; default DefaultUnitPath("").
	Path string
	// FS is the disk; default config.OSFS.
	FS config.FS
	// UID and GID own the file. The zero value is root:root, as the unit must be; -1 leaves an id
	// alone (tests that do not run as root).
	UID, GID int
	// Systemd runs daemon-reload; required.
	Systemd DaemonReloader
}

// WriteResult is what one Write did.
type WriteResult struct {
	Path string
	// Written is false when the file already held exactly these bytes.
	Written bool
}

// Write replaces the unit file atomically (root 0644, config.WriteFileAtomicFS) when its bytes
// differ and then runs daemon-reload. The reload runs even when the file was already right: it is
// cheap, and it is what repairs a run whose reload failed after the file was written. A failed reload
// is returned (the file is in place). A running whisper-server keeps its old command line until it is
// restarted; this function never starts, stops, restarts or enables the unit.
func (w *UnitWriter) Write(ctx context.Context) (WriteResult, error) {
	path := w.Path
	if path == "" {
		path = DefaultUnitPath("")
	}
	res := WriteResult{Path: path}
	if w.Systemd == nil {
		return res, errors.New("whisper: the unit writer needs a Systemd (daemon-reload)")
	}
	fsys := w.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	data := RenderUnit()
	cur, rerr := fsys.ReadFile(path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return res, fmt.Errorf("whisper: read %s: %w", path, rerr)
	}
	if rerr != nil || !bytes.Equal(cur, data) {
		if werr := config.WriteFileAtomicFS(fsys, path, data, unitFileMode, w.UID, w.GID); werr != nil && !errors.Is(werr, config.ErrNotDurable) {
			return res, fmt.Errorf("whisper: write %s: %w", path, werr)
		}
		res.Written = true
	}
	if err := w.Systemd.DaemonReload(ctx); err != nil {
		return res, fmt.Errorf("whisper: daemon-reload after writing %s: %w", path, err)
	}
	return res, nil
}

// WriteUnit writes the unit on the real machine: /etc/systemd/system/localai-whisper.service, root
// 0644, then daemon-reload through reload (internal/services.Manager).
func WriteUnit(ctx context.Context, reload DaemonReloader) (WriteResult, error) {
	return (&UnitWriter{Systemd: reload}).Write(ctx)
}
