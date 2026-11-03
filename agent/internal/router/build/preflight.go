package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// The offline preflight (rebuild-kit.md section 4.3.1 step 1) is the whole of `--dry-run` and the
// first step of a --from-kit build: every check runs and is reported, whether or not an earlier one
// failed, so the operator (and the drill) sees all that is wrong at once. Nothing is changed and no
// root is needed: every check reads or runs a read-only program.

// Check ids of a Report.
const (
	CheckInputs   = "inputs"   // commit, backend, CUDA architecture
	CheckSource   = "source"   // LAI-KIT-001, LAI-KIT-002
	CheckTools    = "tools"    // LAI-KIT-010
	CheckBackend  = "backend"  // LAI-KIT-011 (cuda), LAI-KIT-012 (vulkan)
	CheckDriver   = "driver"   // LAI-KIT-013
	CheckDisk     = "disk"     // LAI-KIT-014
	CheckJobs     = "jobs"     // always passes
	CheckPackages = "packages" // the offline `apt-get -s install` of the missing packages
)

// Check is one line of a preflight: a title, a verdict and what was found.
type Check struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	OK    bool   `json:"ok"`
	// Code is the doctor code of a failed check.
	Code string `json:"code,omitempty"`
	// Detail says what was found, or why the check failed.
	Detail string `json:"detail"`
}

// Report is the result of PreflightReport.
type Report struct {
	// Commit, Backend, CudaArch and Jobs are the resolved inputs (empty when they could not be
	// resolved: the first check says why).
	Commit   string `json:"commit"`
	Backend  string `json:"backend"`
	CudaArch string `json:"cuda_arch,omitempty"`
	Jobs     int    `json:"jobs"`
	// Source is the name of the source the build would use ("" when none can deliver the commit):
	// "tree" for a tree already extracted, "kit-tarball", "kit-mirror", "dir", "github".
	Source string  `json:"source,omitempty"`
	Checks []Check `json:"checks"`
	// MissingTools and Packages name what is not installed and the apt package for each.
	MissingTools []string `json:"missing_tools,omitempty"`
	Packages     []string `json:"packages,omitempty"`
	// InstallCommand is the exact command that installs Packages from the kit
	// ("sudo aictl kit install-debs --packages cmake,ninja-build"); "" when nothing is missing.
	InstallCommand string `json:"install_command,omitempty"`

	// Tools are the programs the build will run; Redist the CUDA redistributable set option B will
	// use (nil when the system nvcc is used or the backend is not cuda).
	Tools  Tools           `json:"-"`
	Redist *kit.CudaRedist `json:"-"`
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Failed returns the failed checks in report order.
func (r Report) Failed() []Check {
	var out []Check
	for _, c := range r.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// Err is the first failed check as the *Error a build ends with (stage preflight), nil when every
// check passed. details.checks lists every failed check, and details.packages and
// details.install_command say how to fix a missing tool.
func (r Report) Err() *Error {
	failed := r.Failed()
	if len(failed) == 0 {
		return nil
	}
	first := failed[0]
	e := newErr(first.Code, StagePreflight, first.Title+": "+first.Detail, nil, "cause", "preflight", "check", first.ID, "commit", r.Commit, "backend", r.Backend)
	ids := make([]string, 0, len(failed))
	for _, c := range failed {
		ids = append(ids, c.ID+" ("+c.Code+")")
	}
	e.Details["failed_checks"] = ids
	if len(r.MissingTools) > 0 {
		e.Details["missing"] = r.MissingTools
		e.Details["packages"] = r.Packages
		e.Details["install_command"] = r.InstallCommand
		e.Message += " Fix: " + r.InstallCommand
	}
	return e
}

// AptPlan is what an AptSimulator says would be installed.
type AptPlan struct {
	// Install lists the packages apt would install, the requested ones and their dependencies.
	Install []string
}

// AptSimulator proves that the missing packages can be installed offline: it runs `apt-get -s
// install` against the kit's apt repository, with the options of rebuild-kit.md section 4.1.4 and
// nothing written to the system, and returns an error naming what apt could not resolve.
// `aictl kit install-debs` (KIT-06) provides the implementation.
type AptSimulator interface {
	Simulate(ctx context.Context, packages []string) (AptPlan, error)
}

type preflight struct {
	m   *Manager
	ctx context.Context
	req Request
	in  Inputs
	rep *Report

	missing  []string
	packages []string
	seen     map[string]bool
	env      SourceEnv
}

func (p *preflight) add(c Check) { p.rep.Checks = append(p.rep.Checks, c) }

func (p *preflight) pass(id, title, detail string) {
	p.add(Check{ID: id, Title: title, OK: true, Detail: detail})
}

func (p *preflight) fail(id, title, code, detail string) {
	p.add(Check{ID: id, Title: title, Code: code, Detail: detail})
}

// need records a program that is not installed and the package that provides it.
func (p *preflight) need(name, pkg string) {
	if !p.seen["tool:"+name] {
		p.seen["tool:"+name] = true
		p.missing = append(p.missing, name)
	}
	if pkg != "" && !p.seen["pkg:"+pkg] {
		p.seen["pkg:"+pkg] = true
		p.packages = append(p.packages, pkg)
	}
}

// PreflightReport runs the offline preflight for a request and reports every check (see the file
// comment). It never returns an error: a request that cannot be resolved is the first failed check.
// Request.Sources, FromKit, SourceDir and Offline decide which sources the source check asks.
func (m *Manager) PreflightReport(ctx context.Context, req Request) Report {
	in, err := m.Resolve(ctx, req)
	return m.preflightFor(ctx, req, in, err)
}

// preflightFor is PreflightReport over inputs Build already resolved (err is the error of that
// resolution, nil when in is good).
func (m *Manager) preflightFor(ctx context.Context, req Request, in Inputs, err error) Report {
	return m.voiceReport(m.preflightRaw(ctx, req, in, err))
}

func (m *Manager) preflightRaw(ctx context.Context, req Request, in Inputs, err error) Report {
	rep := Report{Checks: []Check{}}
	p := &preflight{m: m, ctx: ctx, req: req, rep: &rep, seen: map[string]bool{},
		env: SourceEnv{Exec: m.opt.Exec, FS: m.opt.FS, Lookup: m.opt.Lookup, Log: m.log}}

	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = newErr(CodeBuildFailed, StageResolve, "The build inputs could not be resolved.", err)
		}
		p.fail(CheckInputs, "Build inputs (commit, backend, CUDA architecture)", e.Code, e.Message)
		// No answer from nvidia-smi is a driver that is not loaded: say so too (LAI-KIT-013).
		if c, _ := e.Details["cause"].(string); c == "no_nvidia_smi" || c == "nvidia_smi_failed" {
			p.fail(CheckDriver, "NVIDIA driver is loaded (nvidia-smi works)", CodeKitDriver, "nvidia-smi gave no answer. "+driverFix)
		}
		return rep
	}
	p.in = in
	rep.Commit, rep.Backend, rep.CudaArch, rep.Jobs = in.Commit, in.Backend, in.CudaArch, in.Jobs
	p.pass(CheckInputs, "Build inputs (commit, backend, CUDA architecture)",
		fmt.Sprintf("llama.cpp %s for %s%s", in.ShortCommit, in.Backend, archSuffix(in.CudaArch)))

	drv := ""
	if in.Backend == BackendCUDA {
		drv = p.driver()
	}
	p.source()
	p.tools()
	switch in.Backend {
	case BackendCUDA:
		p.cuda(drv)
	case BackendVulkan:
		p.vulkan()
	}
	p.disk()
	p.pass(CheckJobs, "Parallel jobs", fmt.Sprintf("%d (the smaller of --jobs, the processors, and half the free memory in GiB)", in.Jobs))
	p.packagesCheck()

	rep.MissingTools, rep.Packages = p.missing, p.packages
	if len(p.packages) > 0 {
		rep.InstallCommand = InstallDebsCommand(p.packages)
	}
	return rep
}

// InstallDebsCommand is the exact command that installs packages from the kit
// (aictl.md section 4.23c): "sudo aictl kit install-debs --packages cmake,ninja-build".
func InstallDebsCommand(packages []string) string {
	return "sudo aictl kit install-debs --packages " + strings.Join(packages, ",")
}

const driverFix = "Restart and choose the previous kernel in \"Advanced options\", or install the driver from the kit."

// run executes a read-only program of the preflight; a program that is not installed is
// execx.ErrNotFound.
func (p *preflight) run(name string, args ...string) (string, int, error) {
	path, err := p.m.opt.Lookup(name)
	if err != nil {
		return "", 0, err
	}
	res, err := p.m.opt.Exec.Run(p.ctx, execx.Cmd{Path: path, Args: args, Timeout: 30 * time.Second, MaxOut: 1 << 20, Env: toolEnv()})
	if err != nil {
		return "", 0, err
	}
	return string(res.Stdout) + string(res.Stderr), res.ExitCode, nil
}

// ---- source -----------------------------------------------------------------------------

func (p *preflight) source() {
	title := "Source of llama.cpp " + p.in.ShortCommit
	m, req, ref := p.m, p.req, p.in.Commit
	explicit := req.SourceDir != ""
	if explicit && !m.opt.Recipe.ScratchSource && filepath.Clean(req.SourceDir) == filepath.Join(m.root, srcDir, ref) && treeComplete(m.opt.FS, req.SourceDir) {
		explicit = false
	}
	if !explicit {
		if sha := m.existingTree(ref); sha != "" {
			p.rep.Source = "tree"
			p.pass(CheckSource, title, "already extracted in "+m.srcPath(sha))
			return
		}
	}
	sources := m.sourceChain(req)
	if explicit {
		sources = []Source{DirSource{Dir: req.SourceDir}}
	}
	var damaged, hard error
	var tool *MissingToolError
	for _, s := range sources {
		if req.Offline && s.Network() {
			continue
		}
		sc, checks := s.(SourceChecker)
		if !checks {
			p.rep.Source = s.Name()
			detail := s.Name() + " (not checked: it is only tried when the build runs)"
			if s.Network() {
				detail = "will be fetched from the network by " + s.Name()
			}
			p.pass(CheckSource, title, detail)
			return
		}
		probe, err := sc.Check(p.ctx, p.env, ref)
		var mt *MissingToolError
		switch {
		case err == nil:
			p.rep.Source = s.Name()
			p.pass(CheckSource, title, probe.Detail)
			return
		case errors.Is(err, ErrNotAvailable):
			continue
		case errors.Is(err, ErrSourceDamaged):
			damaged = fmt.Errorf("%s: %w", s.Name(), err)
		case errors.As(err, &mt):
			tool = mt
		default:
			hard = fmt.Errorf("%s: %w", s.Name(), err)
		}
	}
	switch {
	case damaged != nil:
		p.fail(CheckSource, title, CodeSourceDamaged, damaged.Error()+". Run `aictl kit verify --full`; sync the drive on a computer with internet.")
	case tool != nil:
		p.need(tool.Tool, tool.Package)
		p.fail(CheckSource, title, CodeKitTools, tool.Tool+" is needed to read the kit mirror and is not installed.")
	case hard != nil:
		p.fail(CheckSource, title, CodeBuildFailed, hard.Error())
	default:
		p.fail(CheckSource, title, CodeNoSource,
			"The kit and this computer do not have the llama.cpp version "+ref+". Sync the kit with internet, or choose a commit that is in the kit.")
	}
}

// ---- tools ------------------------------------------------------------------------------

var cmakeVerRe = regexp.MustCompile(`cmake version ([0-9]+)\.([0-9]+)`)

// toolGroup says which check a tool of the recipe belongs to: the compiler, cmake and ninja are
// "tools" (LAI-KIT-010), nvcc is the CUDA check (LAI-KIT-011), glslc and vulkaninfo the Vulkan one
// (LAI-KIT-012).
func toolGroup(name string) string {
	switch name {
	case "nvcc":
		return BackendCUDA
	case "glslc", "vulkaninfo":
		return BackendVulkan
	}
	return CheckTools
}

func (p *preflight) tools() {
	const title = "Build tools (gcc, g++, cmake 3.21 or newer, ninja, pkg-config)"
	var missing []string
	var found []string
	var notes []string
	t := &p.rep.Tools
	for _, tool := range p.m.opt.Recipe.Tools(p.in.Backend) {
		if toolGroup(tool.Name) != CheckTools {
			continue
		}
		path, err := p.m.opt.Lookup(tool.Name)
		if err != nil {
			missing = append(missing, tool.Name)
			p.need(tool.Name, tool.Package)
			continue
		}
		found = append(found, tool.Name)
		switch tool.Name {
		case "gcc":
			t.CC = path
		case "g++":
			t.CXX = path
		case "cmake":
			t.CMake = path
			if note := p.cmakeVersion(); note != "" {
				notes = append(notes, note)
				p.need("cmake", tool.Package)
			}
		case "ninja":
			t.Ninja = path
		}
	}
	switch {
	case len(missing) > 0:
		p.fail(CheckTools, title, CodeKitTools, "not installed: "+strings.Join(missing, ", ")+strings.Join(append([]string{""}, notes...), "; "))
	case len(notes) > 0:
		p.fail(CheckTools, title, CodeKitTools, strings.Join(notes, "; "))
	default:
		p.pass(CheckTools, title, "found "+strings.Join(found, ", "))
	}
}

// cmakeVersion returns a problem description when cmake is older than MinCMake, "" when it is new
// enough or its version cannot be read (cmake itself then decides).
func (p *preflight) cmakeVersion() string {
	out, _, err := p.run("cmake", "--version")
	if err != nil {
		return ""
	}
	m := cmakeVerRe.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > 3 || (major == 3 && minor >= 21) {
		return ""
	}
	return fmt.Sprintf("cmake %s.%s is older than %s", m[1], m[2], MinCMake)
}

// ---- cuda -------------------------------------------------------------------------------

var (
	driverCUDARe = regexp.MustCompile(`CUDA Version:\s*([0-9]+\.[0-9]+)`)
	majorMinorRe = regexp.MustCompile(`^([0-9]+)\.([0-9]+)`)
)

// ParseDriverCUDA returns the CUDA version in the header of nvidia-smi ("12.8"), "" when absent.
func ParseDriverCUDA(out string) string {
	if m := driverCUDARe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// cudaVersionLE reports whether version a (major.minor[.patch]) is not newer than b in major.minor.
func cudaVersionLE(a, b string) bool {
	pa, pb := majorMinorRe.FindStringSubmatch(a), majorMinorRe.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return false
	}
	am, _ := strconv.Atoi(pa[1])
	an, _ := strconv.Atoi(pa[2])
	bm, _ := strconv.Atoi(pb[1])
	bn, _ := strconv.Atoi(pb[2])
	return am < bm || (am == bm && an <= bn)
}

// driver checks that nvidia-smi works (LAI-KIT-013) and returns the CUDA version of its header.
func (p *preflight) driver() string {
	const title = "NVIDIA driver is loaded (nvidia-smi works)"
	out, code, err := p.run("nvidia-smi")
	if err != nil || code != 0 {
		p.fail(CheckDriver, title, CodeKitDriver, "nvidia-smi did not answer. "+driverFix)
		return ""
	}
	v := ParseDriverCUDA(out)
	if v == "" {
		p.pass(CheckDriver, title, "nvidia-smi answers (its header names no CUDA version)")
		return ""
	}
	p.pass(CheckDriver, title, "nvidia-smi answers; the driver supports CUDA "+v)
	return v
}

// cuda checks nvcc: present and not newer than the driver's CUDA, or, when nvcc is not installed
// and the kit has a CUDA redistributable set (option B, rebuild-kit.md section 4.3.2) the driver can
// run, that set.
func (p *preflight) cuda(driverCUDA string) {
	const title = "CUDA compiler (nvcc, not newer than the driver's CUDA)"
	path, err := p.m.opt.Lookup("nvcc")
	if err == nil {
		p.rep.Tools.NVCC = path
		out, _, _ := p.run("nvcc", "--version")
		v := ParseNVCC(out)
		switch {
		case v == "":
			p.pass(CheckBackend, title, "nvcc found at "+path+" (its version could not be read)")
		case driverCUDA == "":
			p.pass(CheckBackend, title, "nvcc "+v+" (not compared with the driver: its CUDA version is unknown)")
		case !cudaVersionLE(v, driverCUDA):
			p.fail(CheckBackend, title, CodeKitCUDA, "nvcc "+v+" is newer than the CUDA "+driverCUDA+" the driver supports. Run `sudo aictl kit install-debs`, or rebuild with `--backend vulkan`.")
		default:
			p.pass(CheckBackend, title, "nvcc "+v+" is not newer than the driver's CUDA "+driverCUDA)
		}
		return
	}
	finder := kit.SourceFinder{FS: kitFS{p.m.opt.FS}, Roots: p.m.kitRoots()}
	for _, set := range finder.CudaRedists(p.m.opt.Arch) {
		if driverCUDA != "" && !cudaVersionLE(set.Version, driverCUDA) {
			continue
		}
		s := set
		p.rep.Redist = &s
		p.pass(CheckBackend, title, "nvcc is not installed; the kit's CUDA redistributable "+set.Version+" ("+set.Dir+") will be used")
		return
	}
	p.need("nvcc", "nvidia-cuda-toolkit")
	p.fail(CheckBackend, title, CodeKitCUDA, "nvcc is not installed and the kit has no CUDA redistributable the driver can run. Run `sudo aictl kit install-debs`, or rebuild with `--backend vulkan`.")
}

// ---- vulkan -----------------------------------------------------------------------------

var vkDeviceRe = regexp.MustCompile(`(?m)deviceName\s*=\s*(.+)$`)

// ParseVulkanDevices returns the device names `vulkaninfo --summary` lists.
func ParseVulkanDevices(out string) []string {
	var names []string
	for _, m := range vkDeviceRe.FindAllStringSubmatch(out, -1) {
		names = append(names, strings.TrimSpace(m[1]))
	}
	return names
}

func (p *preflight) vulkan() {
	const title = "Vulkan (glslc, libvulkan headers, a real graphics device)"
	var problems []string
	for _, tool := range p.m.opt.Recipe.Tools(BackendVulkan) {
		if toolGroup(tool.Name) != BackendVulkan {
			continue
		}
		path, err := p.m.opt.Lookup(tool.Name)
		if err != nil {
			problems = append(problems, tool.Name+" is not installed")
			p.need(tool.Name, tool.Package)
			continue
		}
		if tool.Name == "glslc" {
			p.rep.Tools.GLSLC = path
		}
	}
	if _, code, err := p.run("pkg-config", "--exists", "vulkan"); err != nil || code != 0 {
		problems = append(problems, "the libvulkan headers are not installed")
		p.need("libvulkan-dev", "libvulkan-dev")
	}
	var devices []string
	if out, code, err := p.run("vulkaninfo", "--summary"); err == nil && code == 0 {
		devices = ParseVulkanDevices(out)
		real := false
		for _, d := range devices {
			real = real || !strings.Contains(strings.ToLower(d), "llvmpipe")
		}
		if !real {
			problems = append(problems, "vulkaninfo lists no graphics device but the software renderer (llvmpipe)")
		}
	} else if err == nil {
		problems = append(problems, "vulkaninfo --summary failed")
	}
	if len(problems) > 0 {
		p.fail(CheckBackend, title, CodeKitVulkan, strings.Join(problems, "; ")+". Install the graphics driver from the kit, or use `--backend cpu`.")
		return
	}
	p.pass(CheckBackend, title, "device "+strings.Join(devices, ", "))
}

// ---- disk -------------------------------------------------------------------------------

// freeBytes reads the free space of path, or of its nearest existing parent.
func (m *Manager) freeBytes(path string) (uint64, error) {
	var err error
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		var free uint64
		if free, err = m.opt.FreeBytes(dir); err == nil {
			return free, nil
		}
		if dir == filepath.Dir(dir) {
			return 0, err
		}
	}
}

func (p *preflight) disk() {
	const title = "Disk space (5 GB in /opt/localai, 1 GB in /var/lib/localai/build)"
	opt := filepath.Dir(p.m.root)
	var problems []string
	var found []string
	for _, w := range []struct {
		dir  string
		need uint64
	}{{opt, KitMinFreeBytes}, {p.m.opt.ScratchDir, KitMinScratchBytes}} {
		free, err := p.m.freeBytes(w.dir)
		switch {
		case err != nil:
			// A filesystem that cannot be read does not block the build (the same rule as Build's own check).
			found = append(found, w.dir+": free space unknown")
		case free < w.need:
			problems = append(problems, fmt.Sprintf("%s has %.1f GB free, %.0f GB are needed", w.dir, float64(free)/1e9, float64(w.need)/(1<<30)))
		default:
			found = append(found, fmt.Sprintf("%s: %.1f GB free", w.dir, float64(free)/1e9))
		}
	}
	if len(problems) > 0 {
		p.fail(CheckDisk, title, CodeKitDiskLow, strings.Join(problems, "; ")+". Free the space and try again.")
		return
	}
	p.pass(CheckDisk, title, strings.Join(found, "; "))
}

// ---- packages ---------------------------------------------------------------------------

func (p *preflight) packagesCheck() {
	if len(p.packages) == 0 {
		return
	}
	const title = "Missing packages can be installed from the kit"
	sorted := append([]string(nil), p.packages...)
	sort.Strings(sorted)
	if p.m.opt.Apt == nil {
		p.pass(CheckPackages, title, "not simulated (no kit apt repository is configured here); install them with: "+InstallDebsCommand(p.packages))
		return
	}
	plan, err := p.m.opt.Apt.Simulate(p.ctx, p.packages)
	if err != nil {
		p.fail(CheckPackages, title, CodeKitTools, "apt cannot install "+strings.Join(sorted, ", ")+" from the kit: "+err.Error())
		return
	}
	p.pass(CheckPackages, title, fmt.Sprintf("apt would install %d package(s); run: %s", len(plan.Install), InstallDebsCommand(p.packages)))
}
