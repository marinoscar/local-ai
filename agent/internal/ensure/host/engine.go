package host

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/acestep"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The engine.acestep step (E11, #511): the music runtime built from the kit's wheelhouse, the item
// kit-engine-acestep-<backend> at kit/engines/acestep/<version>/ (rebuild-kit.md section 3.2.2), which ark sync
// fills on an online node.

// engineItem is the kit item of a Python engine's wheelhouse for a backend.
func engineItem(engine, backend string) string { return "kit-engine-" + engine + "-" + backend }

func (h *Host) acestepRoot() string { return filepath.Join(h.c.OptRoot, "acestep") }

// EngineVersion implements ensure.Machine.
func (h *Host) EngineVersion(engine, backend string) string {
	if engine != "acestep" {
		return ""
	}
	b, err := (acestep.Runtime{Root: h.acestepRoot(), Backend: func() string { return backend }}).Current()
	if err != nil {
		return ""
	}
	return b.Version
}

// wheelhouse reads the wheelhouse of a kit root: the item's MANIFEST.json must name the item of this backend, its
// SHA256SUMS must be the tree the manifest records, and the newest version folder with a lock is the one to
// install. ErrKitMissing when the kit holds no wheelhouse for this backend, ErrKitDamaged when its record is
// unusable.
func wheelhouse(kitDir, engine, backend string) (acestep.Wheelhouse, string, error) {
	dir := filepath.Join(kitDir, kit.PartEngines, engine)
	fsys := config.OSFS{}
	m, err := manifest.Read(fsys, manifest.Path(dir))
	if err != nil {
		if manifest.IsInvalid(err) {
			return acestep.Wheelhouse{}, "", ensure.ErrKitDamaged
		}
		return acestep.Wheelhouse{}, "", ensure.ErrKitMissing
	}
	if m.ItemID != engineItem(engine, backend) {
		return acestep.Wheelhouse{}, "", ensure.ErrKitMissing
	}
	sums, err := manifest.ReadSums(fsys, dir)
	if err != nil || !m.IsDir() {
		return acestep.Wheelhouse{}, "", ensure.ErrKitDamaged
	}
	if tree, err := manifest.TreeSHA256(sums); err != nil || tree != m.TreeSHA256 {
		return acestep.Wheelhouse{}, "", ensure.ErrKitDamaged
	}
	all := make(map[string]string, len(sums))
	for _, s := range sums {
		all[s.Path] = s.SHA256
	}
	versions := acestep.Versions(all)
	if len(versions) == 0 {
		return acestep.Wheelhouse{}, "", ensure.ErrKitDamaged
	}
	return acestep.Sub(dir, all, versions[0]), versions[0], nil
}

// Engine implements ensure.Kit: the first root whose kit verified the wheelhouse of this backend (the quick kit
// check: manifest, presence and sizes), with the version folder to install and the sha256 of its lock. The files
// themselves are re-hashed by the install, right before they are used.
func (h *Host) Engine(ctx context.Context, engine, backend string) (ensure.Source, string, error) {
	if engine != "acestep" {
		return ensure.Source{}, "", ensure.ErrKitMissing
	}
	r, damaged, err := h.firstVerified(ctx, kit.PartEngines, engineItem(engine, backend))
	if err != nil {
		return ensure.Source{}, "", kitErr(damaged, err)
	}
	kitDir := filepath.Join(r.Path, kit.DirName)
	wh, version, err := wheelhouse(kitDir, engine, backend)
	if err != nil {
		return ensure.Source{}, "", err
	}
	return ensure.Source{Root: r.Path, KitDir: kitDir, File: wh.Dir, SHA256: wh.Sums[acestep.WheelhouseLock]}, version, nil
}

// InstallEngine implements ensure.Installer: it reads the wheelhouse again from the kit the planner chose and
// builds the runtime from it.
func (h *Host) InstallEngine(ctx context.Context, engine string, src ensure.Source, version, backend string) (ensure.ToolOutcome, error) {
	if engine != "acestep" {
		return ensure.ToolOutcome{}, fmt.Errorf("ensure: engine %q is not on the allowlist", engine)
	}
	wh, got, err := wheelhouse(src.KitDir, engine, backend)
	if err != nil {
		return ensure.ToolOutcome{}, err
	}
	if got != version {
		return ensure.ToolOutcome{}, fmt.Errorf("ensure: the kit now holds the music runtime %s, not %s: run it again", got, version)
	}
	in := acestep.Installer{Root: h.acestepRoot(), Runner: h.c.Runner, LookPath: h.lookPath, Now: h.c.Now}
	out, err := in.Install(ctx, wh, version, backend)
	if errors.Is(err, acestep.ErrWheelhouseDamaged) {
		return ensure.ToolOutcome{}, ensure.ErrKitDamaged
	}
	return ensure.ToolOutcome{Version: out.Version, Previous: out.Previous, Unchanged: out.Unchanged}, err
}
