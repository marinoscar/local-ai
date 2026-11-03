package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// What `aictl kit status` and `aictl kit verify` need to know about this computer and how they reach
// the build engine's dry run (rebuild-kit.md section 4.4). Everything goes through kitHostFn and
// localEnv, so a test replaces the machine.

// kitFacts is what the commands learn about this computer.
type kitFacts struct {
	// Node is nil when nothing could be read (the completeness for this computer is then skipped).
	Node *kit.NodeFacts
	// Config is node.yaml as far as it could be read (the safe defaults when it cannot).
	Config config.Config
	// ReverifyDays is sync.reverify_days of node.yaml, 0 for the default.
	ReverifyDays int
}

// nvidiaSmiTimeout bounds each nvidia-smi call.
const nvidiaSmiTimeout = 15 * time.Second

// kitReadFacts reads the facts of this computer: node.yaml (node id, llama commit, backend and CUDA
// architecture, whisper commit, the re-verify period), os-release and the architecture (codename), the
// running agent's version, and for a CUDA node the driver (nvidia-smi: the CUDA version of its header
// and the driver branch). A fact that cannot be read is left empty; nothing here fails.
func kitReadFacts(ctx context.Context, env local.Env, host kitHost) kitFacts {
	out := kitFacts{Config: config.Defaults()}
	f := &kit.NodeFacts{AgentVersion: env.AgentVersion}
	if data, err := os.ReadFile(env.Paths.NodeYAML()); err == nil && len(data) <= 1<<20 {
		cfg, _ := config.Parse(data, nil)
		out.Config = cfg
		f.NodeID = cfg.Node.ID
		f.LlamaCommit = cfg.Llama.Commit
		f.Backend = cfg.Llama.Backend
		if cfg.Llama.CudaArch != nil {
			f.CudaArch = *cfg.Llama.CudaArch
		}
		if raw, ok := cfg.Blocks["whisper"]; ok {
			var w struct {
				Commit string `yaml:"commit"`
			}
			if raw.Decode(&w) == nil {
				f.WhisperCommit = w.Commit
			}
		}
		if raw, ok := cfg.Blocks["sync"]; ok {
			var s struct {
				ReverifyDays int `yaml:"reverify_days"`
			}
			if raw.Decode(&s) == nil && s.ReverifyDays > 0 {
				out.ReverifyDays = s.ReverifyDays
			}
		}
	}
	if codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH); err == nil {
		f.Codename, f.Arch = codename, arch
	} else if host.GOARCH == "amd64" || host.GOARCH == "arm64" {
		f.Arch = host.GOARCH
	}
	if f.Backend == "cuda" {
		f.DriverCUDA, f.NvidiaBranch = kitDriver(ctx, host)
	}
	if f.NodeID == "" && f.Codename == "" && f.Arch == "" && f.LlamaCommit == "" {
		return out
	}
	out.Node = f
	return out
}

// kitDriver asks nvidia-smi for the CUDA version the driver supports (the "CUDA Version: X.Y" of its
// header) and the major version of the driver ("595", the branch the NVIDIA closure is for). Either is
// "" when nvidia-smi is missing or does not answer.
func kitDriver(ctx context.Context, host kitHost) (cuda, branch string) {
	if host.Runner == nil || host.LookPath == nil {
		return "", ""
	}
	bin, err := host.LookPath("nvidia-smi")
	if err != nil {
		return "", ""
	}
	run := func(args ...string) string {
		res, err := host.Runner.Run(ctx, execx.Cmd{Path: bin, Args: args, Timeout: nvidiaSmiTimeout, MaxOut: 64 << 10})
		if err != nil || res.ExitCode != 0 {
			return ""
		}
		return string(res.Stdout)
	}
	cuda = build.ParseDriverCUDA(run())
	if v := strings.TrimSpace(strings.SplitN(run("--query-gpu=driver_version", "--format=csv,noheader"), "\n", 2)[0]); v != "" {
		branch, _, _ = strings.Cut(v, ".")
	}
	return cuda, branch
}

// kitVerifyFS is the disk the verifier reads: the host's, with DropCache when it has one (kit.OSFS does).
func kitVerifyFS(fsys kit.FS) kit.VerifyFS {
	if v, ok := fsys.(kit.VerifyFS); ok {
		return v
	}
	return noDropFS{fsys}
}

// noDropFS is an FS without a page cache to drop (a test's).
type noDropFS struct{ kit.FS }

func (noDropFS) DropCache(string) error { return nil }

// statusRoots turns the roots a command looks at into the roots the verifier reports on.
func statusRoots(roots []kitRoot) []kit.StatusRoot {
	out := make([]kit.StatusRoot, 0, len(roots))
	for _, r := range roots {
		out = append(out, kit.StatusRoot{ID: r.ID, Path: r.Path, Internal: r.ID == "internal"})
	}
	return out
}

// ---- step 6: the dry run of rebuild-llama -----------------------------------------------------------

// kitBuildFn makes the buildability check of the verification (rebuild-kit.md section 4.4 step 6): the
// build engine's preflight, as `aictl rebuild-llama --dry-run --from-kit` runs it. Tests replace it.
var kitBuildFn = func(env local.Env, host kitHost, facts kitFacts, roots []kitRoot) func(context.Context) kit.BuildResult {
	return func(ctx context.Context) kit.BuildResult {
		opt := build.Options{
			Root:       filepath.Join(host.OptRoot, "llama.cpp"),
			Node:       func() config.LlamaBlock { return facts.Config.Llama },
			Exec:       host.Runner,
			Lookup:     host.LookPath,
			Arch:       host.GOARCH,
			ScratchDir: host.ScratchDir,
			KitRoots: func() []string {
				paths := make([]string, 0, len(roots))
				for _, r := range roots {
					paths = append(paths, r.Path)
				}
				return paths
			},
		}
		if host.ScratchDir == "" {
			opt.ScratchDir = build.DefaultScratchDir
		}
		if codename, arch, err := kit.MachineRelease(host.FS, host.OSRelease, host.GOARCH); err == nil {
			opt.Apt = &kitAptSim{apt: &kit.Apt{
				FS: host.FS, Runner: host.Runner, LookPath: host.LookPath, Codename: codename, Arch: arch,
				SourceList: host.SourceList, ScratchDir: host.ScratchDir,
			}, roots: roots}
		}
		mgr := build.New(opt)
		rep := mgr.PreflightReport(ctx, build.Request{FromKit: true, DryRun: true, Offline: true})
		res := kit.BuildResult{OK: rep.OK()}
		for _, c := range rep.Checks {
			res.Checks = append(res.Checks, kit.BuildCheck{ID: c.ID, Title: c.Title, OK: c.OK, Code: c.Code, Detail: c.Detail})
		}
		return res
	}
}

// kitAptSim is the build engine's AptSimulator over the offline apt of the kit: it proves that missing
// packages can be installed from a root's repositories (a private apt state, nothing written to the
// system).
type kitAptSim struct {
	apt   *kit.Apt
	roots []kitRoot
}

// Simulate implements build.AptSimulator.
func (s *kitAptSim) Simulate(ctx context.Context, packages []string) (build.AptPlan, error) {
	var last error
	for _, r := range s.roots {
		res, err := s.apt.Run(ctx, r.KitDir(), kit.Request{Packages: packages, DryRun: true})
		if err == nil {
			plan := build.AptPlan{}
			for _, p := range res.Plan {
				plan.Install = append(plan.Install, p.Name)
			}
			return plan, nil
		}
		last = err
	}
	return build.AptPlan{}, last
}
