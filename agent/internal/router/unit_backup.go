package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/marinoscar/local-ai/agent/internal/config"
)

// UnitBackup is the unit as it was before single mode rewrote it for another model (llama-router.md
// section 4.8): the bytes in memory, which Restore puts back exactly, and a copy next to the unit as
// localai-llama.service.prev, which is what an operator (or a crash mid-switch) finds on disk.
type UnitBackup struct {
	// Exists is false when there was no unit file; Restore then removes the file again.
	Exists bool
	Data   []byte
	// Recorded is state.llama.unit_sha256 at the time.
	Recorded string
}

// Backup takes the backup of the unit. The in-memory copy is what Restore uses; the .prev copy is
// kept so that a crash in the middle of a switch leaves the previous unit on disk. A .prev that
// cannot be written is logged and does not fail the backup: the memory copy is enough while the
// agent lives, and the generator writes the same copy when it replaces the file.
func (g *UnitGenerator) Backup(ctx context.Context) (UnitBackup, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return UnitBackup{}, err
	}
	b := UnitBackup{Recorded: g.opt.State.UnitSHA256()}
	cur, err := g.opt.FS.ReadFile(g.opt.Paths.Unit)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return b, nil
	case err != nil:
		return b, fmt.Errorf("router: could not read the llama unit to back it up: %w", err)
	}
	b.Exists, b.Data = true, cur
	prev := g.opt.Paths.Unit + ".prev"
	if old, perr := g.opt.FS.ReadFile(prev); perr != nil || !bytes.Equal(old, cur) {
		if werr := config.WriteFileAtomicFS(g.opt.FS, prev, cur, unitFileMode, g.opt.UnitOwner.UID, g.opt.UnitOwner.GID); werr != nil && !errors.Is(werr, config.ErrNotDurable) {
			g.log.Warn("could not keep the previous llama unit next to it", "error", werr)
		}
	}
	return b, nil
}

// Restore puts the backed-up unit back, byte for byte, records its hash and runs daemon-reload. It
// is the first step of the rollback of a failed single-mode load. The hash it records is the one the
// backup was taken under, so a unit that was a hand edit stays reported as one.
func (g *UnitGenerator) Restore(ctx context.Context, b UnitBackup) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !b.Exists {
		if err := g.opt.FS.Remove(g.opt.Paths.Unit); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("router: could not remove the llama unit: %w", err)
		}
	} else if err := config.WriteFileAtomicFS(g.opt.FS, g.opt.Paths.Unit, b.Data, unitFileMode, g.opt.UnitOwner.UID, g.opt.UnitOwner.GID); err != nil && !errors.Is(err, config.ErrNotDurable) {
		return fmt.Errorf("router: could not restore the previous llama unit: %w", err)
	}
	if b.Exists {
		if serr := g.opt.State.SetUnitSHA256(b.Recorded); serr != nil {
			g.log.Error("could not record the hash of the restored llama unit", "error", serr)
		}
	}
	if g.opt.Systemd == nil {
		g.reloadPending = false
		return nil
	}
	if err := g.opt.Systemd.DaemonReload(ctx); err != nil {
		g.reloadPending = true
		return fmt.Errorf("router: daemon-reload after restoring the llama unit: %w", err)
	}
	g.reloadPending = false
	return nil
}
