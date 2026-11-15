package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/svc/acestep"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// The engine.acestep step (E11, #511): the music runtime built from the kit's runtime files.

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

// Engine implements ensure.Kit: the newest runtime version for the backend on the first root whose kit
// has one (kit/engines/acestep/<backend>/<version>/), with every file of that version and its sha256
// from the item's MANIFEST.json (the record sync verified it against). A version with no record, a file
// the record does not list as a sha256, or a recorded file that is missing, is damaged.
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
		dir, version := acestep.FindVersion(kitDir, backend)
		if dir == "" {
			continue
		}
		src, ok := engineFiles(kitDir, dir, version, backend)
		if !ok {
			damaged = true
			continue
		}
		src.Root, src.KitDir = r.Path, kitDir
		return src, version, nil
	}
	if damaged {
		return ensure.Source{}, "", ensure.ErrKitDamaged
	}
	return ensure.Source{}, "", ensure.ErrKitMissing
}

// engineFiles reads the record of a runtime version: every MANIFEST.json entry below <version>/, each a
// sha256 and present on disk, the lock among them.
func engineFiles(kitDir, dir, version, backend string) (ensure.Source, bool) {
	itemDir := acestep.KitDir(kitDir, backend)
	m, err := manifest.Load(config.OSFS{}, itemDir)
	if err != nil {
		return ensure.Source{}, false
	}
	src := ensure.Source{}
	prefix := version + "/"
	for _, f := range m.Files {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		if !manifest.IsSHA256(f.SHA256) {
			return ensure.Source{}, false
		}
		p := filepath.Join(itemDir, filepath.FromSlash(f.Path))
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return ensure.Source{}, false
		}
		src.Files = append(src.Files, ensure.FileSum{Path: p, SHA256: f.SHA256})
		if f.Path == path.Join(version, acestep.KitLock) {
			src.File, src.SHA256 = p, f.SHA256
		}
	}
	return src, src.File != "" && filepath.Dir(src.File) == dir
}

// InstallEngine implements ensure.Installer.
func (h *Host) InstallEngine(ctx context.Context, engine string, src ensure.Source, version, backend string) (ensure.ToolOutcome, error) {
	if engine != "acestep" {
		return ensure.ToolOutcome{}, fmt.Errorf("ensure: engine %q is not on the allowlist", engine)
	}
	files := make([]acestep.KitFile, 0, len(src.Files))
	for _, f := range src.Files {
		files = append(files, acestep.KitFile{Path: f.Path, SHA256: f.SHA256})
	}
	in := acestep.Installer{Root: h.acestepRoot(), Runner: h.c.Runner, LookPath: h.lookPath, Now: h.c.Now}
	out, err := in.Install(ctx, filepath.Dir(src.File), files, version, backend)
	if errors.Is(err, acestep.ErrKitDamaged) {
		return ensure.ToolOutcome{}, ensure.ErrKitDamaged
	}
	return ensure.ToolOutcome{Version: out.Version, Previous: out.Previous, Unchanged: out.Unchanged}, err
}
