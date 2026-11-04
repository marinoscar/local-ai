package setup

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/setup/units"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/whisper"
)

func init() { register("units", unitsStep) }

const unitDir = "/etc/systemd/system"

// Units enabled by `aiagent setup units` (installer.md P9 step 2). The timers also start now; starting a
// timer does not run its service.
var (
	enableUnits    = []string{"localai-agent.service", "localai-llama.service", "localai-kiwix.service", "localai-whisper.service"}
	enableNowUnits = []string{"localai-sync.timer", "localai-drill.timer"}
)

// unitFile is one file to write under /etc/systemd/system.
type unitFile struct {
	rel  string // relative to the unit directory
	data []byte
}

// renderUnits renders every unit, deterministically. root is the filesystem root for the node.yaml and the
// catalog the llama unit and the kiwix thread count come from; an unreadable one falls back to the defaults
// (the agent regenerates localai-llama.service itself from the live profile, llama-router.md 3.2).
func renderUnits(root string) ([]unitFile, []string, error) {
	var notes []string
	var out []unitFile
	add := func(rel string, data []byte, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		out = append(out, unitFile{rel, data})
		return nil
	}

	b, err := units.AgentTemplate()
	if e := add(units.AgentUnit, b, err); e != nil {
		return nil, nil, e
	}

	cfg, _ := config.Parse(readOrNil(config.NodeYAMLPath(root)), nil)
	var rt catalog.Router
	if cfg.Profile != "" {
		if cat, cerr := catalog.Load(filepath.Join(root, "etc", "localai", "catalog")); cerr == nil {
			if p, ok := cat.Profile(cfg.Profile); ok {
				rt = p.Router
			}
		} else {
			notes = append(notes, "the catalog is not installed yet, so the llama unit uses the default router settings; the agent regenerates it from the profile")
		}
	}
	in := router.UnitInputs{Mode: router.LlamaModeRouter, Router: rt}
	if cfg.Ports.Llama != 0 {
		in.Port = cfg.Ports.Llama
	}
	r, err := router.RenderUnit(in)
	if e := add("localai-llama.service", r.Data, err); e != nil {
		return nil, nil, e
	}
	if drop := router.RenderDropIn(cfg.Llama.ExtraEnv); drop != nil {
		out = append(out, unitFile{"localai-llama.service.d/50-env.conf", drop})
	}

	var kb struct {
		Threads int `yaml:"threads"`
	}
	if raw, ok := cfg.Blocks["kiwix"]; ok {
		_ = yaml.Unmarshal(raw, &kb)
	}
	kb2, err := kiwix.RenderUnit(kb.Threads)
	if e := add("localai-kiwix.service", kb2, err); e != nil {
		return nil, nil, e
	}
	if e := add("localai-whisper.service", whisper.RenderUnit(), nil); e != nil {
		return nil, nil, e
	}

	st, ss, err := units.SyncTemplates()
	if err != nil {
		return nil, nil, err
	}
	out = append(out, unitFile{units.SyncServiceUnit, ss}, unitFile{units.SyncTimerUnit, st})
	for _, n := range []string{"localai-drill.service", "localai-drill.timer"} {
		d, err := units.Drill.ReadFile(n)
		if e := add(n, d, err); e != nil {
			return nil, nil, e
		}
	}
	return out, notes, nil
}

func readOrNil(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// unitsStep is `aiagent setup units`: write every unit, back up the ones that change, daemon-reload, enable.
func unitsStep(ctx context.Context, e *Env, args []string) int {
	fs := flag.NewFlagSet("units", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var dry bool
	var backupDir string
	fs.BoolVar(&dry, "dry-run", false, "")
	fs.StringVar(&backupDir, "backup-dir", "/var/lib/localai/install/backup", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(e.Stderr, "usage: aiagent setup units [--dry-run] [--backup-dir <dir>]")
		return ExitUsage
	}
	if !needRoot(e, dry, "units") {
		return ExitNotRoot
	}
	files, notes, err := renderUnits(e.Root)
	if err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup units: cannot render the unit files: %v\n", err)
		return ExitFailed
	}
	for _, n := range notes {
		e.say("note: %s", n)
	}

	dir := filepath.Join(e.Root, unitDir)
	stamp := time.Now().UTC().Format("20060102T150405Z")
	changed := 0
	for _, f := range files {
		dst := filepath.Join(dir, f.rel)
		old, rerr := os.ReadFile(dst)
		switch {
		case rerr == nil && bytes.Equal(old, f.data):
			e.say("already up to date: %s", dst)
			continue
		case dry && rerr == nil:
			e.say("[dry-run] would change %s (and keep a backup)", dst)
		case dry:
			e.say("[dry-run] would create %s", dst)
		}
		changed++
		if dry {
			continue
		}
		if rerr == nil {
			bak := filepath.Join(e.Root, backupDir, stamp, unitDir, f.rel)
			if err := os.MkdirAll(filepath.Dir(bak), 0o700); err != nil {
				fmt.Fprintf(e.Stderr, "aiagent setup units: cannot make the backup folder: %v\n", err)
				return ExitFailed
			}
			if err := os.WriteFile(bak, old, 0o600); err != nil {
				fmt.Fprintf(e.Stderr, "aiagent setup units: cannot back up %s: %v\n", dst, err)
				return ExitFailed
			}
			e.say("backed up %s to %s", dst, bak)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintf(e.Stderr, "aiagent setup units: cannot create %s: %v\n", filepath.Dir(dst), err)
			return ExitFailed
		}
		if err := config.WriteFileAtomic(dst, f.data, 0o644, -1, -1); err != nil {
			fmt.Fprintf(e.Stderr, "aiagent setup units: cannot write %s: %v\n", dst, err)
			return ExitFailed
		}
		e.say("wrote %s", dst)
	}

	if dry {
		e.say("[dry-run] would run: systemctl daemon-reload")
		e.say("[dry-run] would run: systemctl enable %s", join(enableUnits))
		e.say("[dry-run] would run: systemctl enable --now %s", join(enableNowUnits))
		return ExitOK
	}
	if e.Systemctl == nil {
		return ExitOK
	}
	if changed > 0 {
		if err := e.Systemctl.DaemonReload(ctx); err != nil {
			fmt.Fprintf(e.Stderr, "aiagent setup units: %v\n", err)
			return ExitFailed
		}
	}
	if err := e.Systemctl.Enable(ctx, enableUnits...); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup units: %v\n", err)
		return ExitFailed
	}
	if err := e.Systemctl.EnableNow(ctx, enableNowUnits...); err != nil {
		fmt.Fprintf(e.Stderr, "aiagent setup units: %v\n", err)
		return ExitFailed
	}
	e.say("units written: %d changed, %d enabled", changed, len(enableUnits)+len(enableNowUnits))
	return ExitOK
}

func join(s []string) string {
	var b bytes.Buffer
	for i, x := range s {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(x)
	}
	return b.String()
}
