package app

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/drill/checks"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/setup/harden"
)

// The drill's kit, rebuild dry-run, power and clock checks (DRILL-05, drill.md section 4.2 checks 17,
// 18, 20 and 21), wired to the daemon. The checks see small interfaces; this file connects them to the
// kit verifier, the build manager's preflight, the installer's hardening checks, the hardware sampler
// and the content index.

// drillKit is checks.KitSource over the kit verifier the readiness service uses (kitready.Source): the
// same roots, the same facts about this computer, a quick verification (nothing is re-hashed: the
// hashing is check 4) and no build dry run (check 18 does that).
type drillKit struct{ d *Daemon }

var _ checks.KitSource = drillKit{}

// Roots implements checks.KitSource. A node whose ark stack could not be built has none, which the
// check reports as a missing kit.
func (k drillKit) Roots() []kit.StatusRoot {
	if k.d.ark == nil || k.d.ark.kit == nil || k.d.ark.kit.Roots == nil {
		return nil
	}
	return k.d.ark.kit.Roots()
}

// Verify implements checks.KitSource.
func (k drillKit) Verify(ctx context.Context, rootID string) (kit.VerifyReport, error) {
	if k.d.ark == nil || k.d.ark.kit == nil {
		return kit.VerifyReport{}, kit.ErrNoKit
	}
	return k.d.ark.kit.Verify(ctx, rootID, func(o *kit.Options) {
		o.Mode = kit.ModeQuick
		o.Build = nil
	})
}

// drillAptSim is build.AptSimulator over the offline apt of the kit: it proves that missing packages
// can be installed from a root's repositories (a private apt state; nothing is written to the system).
type drillAptSim struct {
	apt   *kit.Apt
	roots []string
}

// Simulate implements build.AptSimulator.
func (s drillAptSim) Simulate(ctx context.Context, packages []string) (build.AptPlan, error) {
	var last error
	for _, root := range s.roots {
		res, err := s.apt.Run(ctx, filepath.Join(root, kit.DirName), kit.Request{Packages: packages, DryRun: true})
		if err == nil {
			plan := build.AptPlan{}
			for _, p := range res.Plan {
				plan.Install = append(plan.Install, p.Name)
			}
			return plan, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no root with a kit to resolve the packages from")
	}
	return build.AptPlan{}, last
}

// kitPaths are the content roots that may hold the kit, internal first.
func (d *Daemon) drillKitPaths() []string {
	var out []string
	for _, r := range (drillKit{d}).Roots() {
		out = append(out, r.Path)
	}
	return out
}

// drillRunner is an execx.Runner that gives every program the drill's subprocess environment (no
// proxy, and a dead one: drill.md section 4.4). A command with no environment of its own would get the
// minimal fixed one, so that is the base.
type drillRunner struct {
	inner execx.Runner
	env   func(base []string) []string
}

// Run implements execx.Runner.
func (r drillRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	base := c.Env
	if base == nil {
		base = []string{"PATH=" + execx.FixedPath}
	}
	c.Env = r.env(base)
	return r.inner.Run(ctx, c)
}

// buildOptions are the seams of the build manager for a preflight: the programs it runs (the
// daemon's runner and lookup), this computer's architecture, the kit's roots and the offline apt
// simulation. A preflight reads and runs read-only programs; it compiles nothing and changes nothing.
func (d *Daemon) drillBuildOptions(paths []string, subEnv func([]string) []string) build.Options {
	env := d.env
	run, lookup := env.Runner, env.Lookup
	if run == nil {
		run = execx.Exec{}
	}
	if lookup == nil {
		lookup = execx.Lookup
	}
	if subEnv != nil {
		run = drillRunner{inner: run, env: subEnv}
	}
	opt := build.Options{
		Exec: run, Lookup: lookup, Arch: runtime.GOARCH, ScratchDir: build.DefaultScratchDir,
		KitRoots: func() []string { return paths }, Logger: d.log,
	}
	if codename, arch, err := kit.MachineRelease(config.OSFS{}, "/etc/os-release", runtime.GOARCH); err == nil && len(paths) > 0 {
		opt.Apt = drillAptSim{apt: &kit.Apt{
			FS: kit.OSFS{}, Runner: run, LookPath: lookup, Codename: codename, Arch: arch, ScratchDir: build.DefaultScratchDir,
		}, roots: paths}
	}
	return opt
}

// drillPreflightRequest is the request of `aictl rebuild-llama --dry-run --from-kit`: from the kit,
// offline, nothing built.
var drillPreflightRequest = build.Request{FromKit: true, DryRun: true, Offline: true}

// llamaPreflight is the dry run of rebuild-llama, in this process.
func (d *Daemon) llamaPreflight(ctx context.Context, subEnv func([]string) []string) build.Report {
	paths := d.drillKitPaths()
	opt := d.drillBuildOptions(paths, subEnv)
	opt.Root = d.c.Paths.Join(build.DefaultRoot)
	opt.Node = func() config.LlamaBlock { return d.c.Config.Get().Llama }
	return build.New(opt).PreflightReport(ctx, drillPreflightRequest)
}

// whisperPreflight is the dry run of rebuild-whisper, in this process.
func (d *Daemon) whisperPreflight(ctx context.Context, subEnv func([]string) []string) build.Report {
	paths := d.drillKitPaths()
	opt := d.drillBuildOptions(paths, subEnv)
	opt.Recipe = build.WhisperRecipe
	opt.Root = d.c.Paths.Join(build.DefaultWhisperRoot)
	opt.Defaults = func() build.Defaults {
		cfg := d.c.Config.Get()
		var w struct {
			Commit  string `yaml:"commit"`
			Backend string `yaml:"backend"`
		}
		if raw, ok := cfg.Blocks["whisper"]; ok {
			_ = raw.Decode(&w)
		}
		return build.WhisperDefaults(w.Commit, w.Backend, cfg.Llama)
	}
	return build.New(opt).PreflightReport(ctx, drillPreflightRequest)
}

// newestVerified is the newest verified_at of any content on the mounted roots, read from the content
// index (memory only): drill check 21 compares the clock with it.
func (d *Daemon) newestVerified() (time.Time, bool) {
	idx := d.c.ArkIndex
	if idx == nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, id := range idx.ItemIDs() {
		for _, cp := range idx.Copies(id) {
			if cp.Manifest.VerifiedAt.After(newest) {
				newest = cp.Manifest.VerifiedAt
			}
		}
	}
	return newest, !newest.IsZero()
}

// registerDrillKitChecks adds checks 17, 18, 20 and 21 to the drill registry.
func (d *Daemon) registerDrillKitChecks(reg *drill.Registry) error {
	hardening := checks.NewHardening(harden.New(harden.Paths{Root: d.env.Root}, harden.Options{}, nil))
	err := checks.RegisterKit(reg, drillKit{d},
		checks.BuildDeps{
			Llama: d.llamaPreflight, Whisper: d.whisperPreflight,
			WhisperEnabled: func() bool { return blockEnabled(d.c.Config.Get(), "whisper") },
		},
		checks.PowerDeps{Hardening: hardening, Power: powerSource{h: d.c.Hardware}},
		d.newestVerified)
	if err != nil {
		return fmt.Errorf("drill kit checks: %w", err)
	}
	return nil
}
