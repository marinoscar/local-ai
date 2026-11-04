package kiwix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// Fixed names of the library unit (companion-services.md sections 3.2 and 3.3, overview.md section
// 3.5). They are the canonical paths of the lab: the generated text never varies with the host, only
// with --threads.
const (
	// UnitName is localai-kiwix.service.
	UnitName = services.UnitKiwix
	// DefaultThreads is node.yaml kiwix.threads when the key is absent or 0.
	DefaultThreads = 4
	// MaxThreads is the largest kiwix.threads the renderer accepts; the loopback-only library has
	// no use for more.
	MaxThreads = 64

	unitFileMode os.FileMode = 0o644
)

// DefaultUnitPath returns /etc/systemd/system/localai-kiwix.service under root ("" means "/").
func DefaultUnitPath(root string) string {
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, "etc", "systemd", "system", UnitName)
}

// unitTemplate is the unit of companion-services.md section 3.3, byte for byte. {{.Threads}} is the
// only thing that varies. There is no header comment: the golden file is the spec text.
var unitTemplate = template.Must(template.New(UnitName).Option("missingkey=error").Parse(`[Unit]
Description=Local AI library (kiwix-serve)
Documentation=https://github.com/marinoscar/local-ai/blob/main/docs/specs/companion-services.md
After=local-fs.target
ConditionPathExists=/var/lib/localai/kiwix/library.xml
ConditionPathExists=/opt/localai/kiwix-tools/current/kiwix-serve

[Service]
Type=simple
User=localai
Group=localai
ExecStart=/opt/localai/kiwix-tools/current/kiwix-serve \
  --address=127.0.0.1 --port=8888 \
  --urlRootLocation=/library \
  --monitorLibrary --skipInvalid --blockexternal \
  --threads={{.Threads}} \
  --library /var/lib/localai/kiwix/library.xml
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
ReadOnlyPaths=/

[Install]
WantedBy=multi-user.target
`))

// UnitError is a node.yaml value that cannot be rendered into the unit. It carries LAI-AGT-002
// (node.yaml is invalid); the block validator normally catches it first.
type UnitError struct {
	Subject string // "threads"
	Reason  string
}

// Error implements error.
func (e *UnitError) Error() string { return "kiwix unit " + e.Subject + ": " + e.Reason }

// Code returns the doctor code of the problem: LAI-AGT-002.
func (e *UnitError) Code() string { return doctor.AGT002 }

// RenderUnit renders localai-kiwix.service for node.yaml kiwix.threads. It is pure and
// deterministic. threads 0 means the default (4); anything outside 1..MaxThreads is a UnitError.
func RenderUnit(threads int) ([]byte, error) {
	if threads == 0 {
		threads = DefaultThreads
	}
	if threads < 1 || threads > MaxThreads {
		return nil, &UnitError{Subject: "threads", Reason: "kiwix.threads " + strconv.Itoa(threads) + " is not between 1 and " + strconv.Itoa(MaxThreads)}
	}
	var b bytes.Buffer
	if err := unitTemplate.Execute(&b, map[string]string{"Threads": strconv.Itoa(threads)}); err != nil {
		return nil, fmt.Errorf("kiwix: render the unit: %w", err) // a template bug, never user input
	}
	return b.Bytes(), nil
}

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

// Write renders the unit for threads, replaces the file atomically (root 0644, config.WriteFileAtomicFS)
// when its bytes differ, and then runs daemon-reload. The reload runs even when the file was already
// right: it is cheap, and it is what repairs a run whose reload failed after the file was written.
// A failed reload is returned (the file is in place). A running kiwix-serve keeps its old
// command line until it is restarted; this function never starts, stops or restarts the unit, and
// never enables it (the installer does).
func (w *UnitWriter) Write(ctx context.Context, threads int) (WriteResult, error) {
	path := w.Path
	if path == "" {
		path = DefaultUnitPath("")
	}
	res := WriteResult{Path: path}
	if w.Systemd == nil {
		return res, errors.New("kiwix: the unit writer needs a Systemd (daemon-reload)")
	}
	fsys := w.FS
	if fsys == nil {
		fsys = config.OSFS{}
	}
	data, err := RenderUnit(threads)
	if err != nil {
		return res, err
	}
	cur, rerr := fsys.ReadFile(path)
	if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return res, fmt.Errorf("kiwix: read %s: %w", path, rerr)
	}
	if rerr != nil || !bytes.Equal(cur, data) {
		if werr := config.WriteFileAtomicFS(fsys, path, data, unitFileMode, w.UID, w.GID); werr != nil && !errors.Is(werr, config.ErrNotDurable) {
			return res, fmt.Errorf("kiwix: write %s: %w", path, werr)
		}
		res.Written = true
	}
	if err := w.Systemd.DaemonReload(ctx); err != nil {
		return res, fmt.Errorf("kiwix: daemon-reload after writing %s: %w", path, err)
	}
	return res, nil
}

// WriteUnit writes the unit on the real machine: /etc/systemd/system/localai-kiwix.service, root
// 0644, then daemon-reload through reload (internal/services.Manager).
func WriteUnit(ctx context.Context, reload DaemonReloader, threads int) (WriteResult, error) {
	return (&UnitWriter{Systemd: reload}).Write(ctx, threads)
}
