package host

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/acestep"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The engine.acestep step (E11, #511): the music runtime built from the kit's wheel bundle.

// engineKitDir is the kit item directory of a Python engine's bundles (rebuild-kit.md section 3.1).
func engineKitDir(kitDir, engine string) string { return filepath.Join(kitDir, "engines", engine) }

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

// Engine implements ensure.Kit: the newest bundle for the backend on the first root whose kit has one,
// with its sha256 from the item's MANIFEST.json (the record sync verified it against). A bundle with no
// record, or whose record is not a sha256, is damaged.
func (h *Host) Engine(ctx context.Context, engine, backend string) (ensure.Source, string, error) {
	if engine != "acestep" {
		return ensure.Source{}, "", ensure.ErrKitMissing
	}
	damaged := false
	for _, r := range h.roots() {
		if ctx.Err() != nil {
			return ensure.Source{}, "", ctx.Err()
		}
		kitDir := filepath.Join(r.Path, kit.DirName)
		file, version := acestep.FindBundle(kitDir, backend)
		if file == "" {
			continue
		}
		m, err := manifest.Load(config.OSFS{}, engineKitDir(kitDir, engine))
		if err != nil {
			damaged = true
			continue
		}
		want := path.Join(version, acestep.BundleName(version, backend))
		sum := ""
		for _, f := range m.Files {
			if f.Path == want {
				sum = f.SHA256
			}
		}
		if !manifest.IsSHA256(sum) {
			damaged = true
			continue
		}
		return ensure.Source{Root: r.Path, KitDir: kitDir, File: file, SHA256: sum}, version, nil
	}
	if damaged {
		return ensure.Source{}, "", ensure.ErrKitDamaged
	}
	return ensure.Source{}, "", ensure.ErrKitMissing
}

// InstallEngine implements ensure.Installer.
func (h *Host) InstallEngine(ctx context.Context, engine string, src ensure.Source, version, backend string) (ensure.ToolOutcome, error) {
	if engine != "acestep" {
		return ensure.ToolOutcome{}, fmt.Errorf("ensure: engine %q is not on the allowlist", engine)
	}
	in := acestep.Installer{Root: h.acestepRoot(), Runner: h.c.Runner, LookPath: h.lookPath, Now: h.c.Now}
	out, err := in.Install(ctx, src.File, src.SHA256, version, backend)
	if errors.Is(err, acestep.ErrBundleDamaged) {
		return ensure.ToolOutcome{}, ensure.ErrKitDamaged
	}
	return ensure.ToolOutcome{Version: out.Version, Previous: out.Previous, Unchanged: out.Unchanged}, err
}
