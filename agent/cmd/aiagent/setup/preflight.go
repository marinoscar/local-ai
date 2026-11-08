package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/setup/detect"
)

// Check statuses.
const (
	StatusOK   = "ok"
	StatusInfo = "info"
	StatusWarn = "warn"
	StatusFail = "fail"
)

const (
	// extraDiskBytes is the room the system needs on top of the profile's min_free_bytes (installer.md P1).
	extraDiskBytes = 20_000_000_000
	// lowRAMMiB is 8 GB (8e9 bytes): a "8 GB" computer reports about 7.6 GiB of MemTotal.
	lowRAMMiB = 7629
)

// Check is one row of the preflight table.
type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// System is what the machine is, for the later phases.
type System struct {
	Ubuntu        string `json:"ubuntu"`
	Codename      string `json:"codename"`
	Arch          string `json:"arch"`
	Laptop        bool   `json:"laptop"`
	OnBattery     bool   `json:"on_battery"`
	RAMMiB        int64  `json:"ram_mib"`
	DiskPath      string `json:"disk_path"`
	DiskAvail     uint64 `json:"disk_avail_bytes"`
	DesktopUser   string `json:"desktop_user,omitempty"`
	SecureBoot    string `json:"secure_boot,omitempty"` // enabled, disabled or ""
	LegacyService bool   `json:"legacy_llama_qwen"`
	LegacyExec    string `json:"legacy_exec_start,omitempty"`
}

// Report is the JSON the driver reads (`aiagent setup preflight --json`).
type Report struct {
	Schema string     `json:"schema"`
	Source string     `json:"source"` // online or ark
	OK     bool       `json:"ok"`     // no check failed
	Exit   int        `json:"exit"`   // what the step exits with: 0, 1 or 3
	System System     `json:"system"`
	GPU    detect.GPU `json:"gpu"`
	Checks []Check    `json:"checks"`
}

// PreflightOptions are the flags of the step.
type PreflightOptions struct {
	// Ark is the mount point of the AI-ARK drive for an offline install ("" = online).
	Ark string
	// DesktopUser is --desktop-user.
	DesktopUser string
	// Yes is --yes: a disk shortfall becomes a warning (the budget is capped later).
	Yes bool
	// MinFreeBytes is the chosen profile's storage.min_free_bytes (0 before a profile is known).
	MinFreeBytes int64
	// Backend is --backend, overriding the detected backend ("" = detected).
	Backend string
}

// refusal codes: a failure of one of these exits 3 before anything is changed.
func refusal(code string) bool {
	return code == doctor.INS001 || code == doctor.INS002 || code == doctor.INS003
}

type collector struct{ checks []Check }

func (c *collector) add(id, status, code, msg string) {
	ch := Check{ID: id, Status: status, Code: code, Message: msg}
	if code != "" && status != StatusOK {
		if _, fix, ok := doctor.Text(code, "en"); ok {
			ch.Fix = fix
		}
	}
	c.checks = append(c.checks, ch)
}

// Preflight runs every P1 check (they all run, so every failure is reported at once).
func Preflight(ctx context.Context, d Deps, o PreflightOptions) Report {
	e := d.Env
	c := &collector{}
	rep := Report{Schema: "localai.preflight/1", Source: "online"}
	if o.Ark != "" {
		rep.Source = "ark"
	}
	sys := &rep.System

	// root via sudo
	desktop := o.DesktopUser
	if desktop == "" {
		if su := getenv(e, "SUDO_USER"); su != "" && su != "root" {
			desktop = su
		}
	}
	switch {
	case e.EUID != 0:
		c.add("root", StatusFail, doctor.INS003, "This was not run with sudo. Run: sudo bash install-node.sh ...")
	case desktop == "":
		c.add("root", StatusFail, doctor.INS003, "Run as root without a desktop user. Pass --desktop-user <name> (the person who normally uses this computer).")
	default:
		c.add("root", StatusOK, "", "running as root for desktop user "+desktop)
		sys.DesktopUser = desktop
	}

	// Ubuntu version
	osr, ok := e.OS()
	switch {
	case !ok:
		c.add("ubuntu", StatusFail, doctor.INS001, "Cannot read /etc/os-release, so the system is not a supported Ubuntu.")
	case !osr.SupportedUbuntu():
		c.add("ubuntu", StatusFail, doctor.INS001, fmt.Sprintf("This is %s %s. Only Ubuntu 24.04 and 26.04 LTS are supported.", orUnknown(osr.ID), orUnknown(osr.VersionID)))
	default:
		c.add("ubuntu", StatusOK, "", fmt.Sprintf("Ubuntu %s (%s)", osr.VersionID, osr.Codename))
	}
	sys.Ubuntu, sys.Codename = osr.VersionID, osr.Codename

	// arch
	arch := e.Arch(ctx)
	if detect.SupportedArch(arch) {
		c.add("arch", StatusOK, "", arch)
	} else {
		c.add("arch", StatusFail, doctor.INS001, fmt.Sprintf("The processor type %q is not supported. Only amd64 and arm64 are.", arch))
	}
	sys.Arch = arch

	// container
	if kind := e.Container(ctx); kind != "" {
		c.add("container", StatusFail, doctor.INS002, "This is running inside a container ("+kind+"). Run the installer on the real machine.")
	} else {
		c.add("container", StatusOK, "", "not a container")
	}

	// GPU
	gpu := e.DetectGPU(ctx)
	switch {
	case gpu.DriverBroken:
		c.add("gpu", StatusWarn, doctor.INS010, "An NVIDIA graphics card is installed but its driver does not work. The installer can install the driver (a reboot is needed), or continue without the GPU.")
		gpu.Backend = detect.BackendCPU
	case gpu.Backend == detect.BackendCUDA:
		c.add("gpu", StatusOK, "", fmt.Sprintf("%s, %d MiB, compute %s, driver %s", gpu.Name, gpu.VRAMMiB, gpu.ComputeCap, gpu.Driver))
	case gpu.Backend == detect.BackendVulkan:
		c.add("gpu", StatusOK, "", fmt.Sprintf("%s GPU (vulkan), %d MiB usable", gpu.Vendor, gpu.VRAMMiB))
	default:
		c.add("gpu", StatusInfo, "", "no usable GPU: the cpu backend will be used")
	}
	if o.Backend != "" && o.Backend != gpu.Backend {
		c.add("backend", StatusInfo, "", fmt.Sprintf("backend forced to %s (detected %s)", o.Backend, gpu.Backend))
		gpu.Backend = o.Backend
	}
	rep.GPU = gpu

	// Secure Boot
	if out, code, err := runE(ctx, e, "mokutil", "--sb-state"); err == nil && code == 0 {
		switch {
		case strings.Contains(strings.ToLower(out), "secureboot enabled"):
			sys.SecureBoot = "enabled"
		case strings.Contains(strings.ToLower(out), "secureboot disabled"):
			sys.SecureBoot = "disabled"
		}
	}
	if sys.SecureBoot == "enabled" && gpu.NvidiaFound && !gpu.NvidiaLoaded {
		c.add("secure_boot", StatusWarn, doctor.INS011, "Secure Boot is on and the NVIDIA driver is not loaded. At the next boot choose \"Enroll MOK\" and type the password you set.")
	} else {
		c.add("secure_boot", StatusInfo, "", "secure boot: "+orUnknown(sys.SecureBoot))
	}

	// RAM
	sys.RAMMiB = e.MemTotalMiB()
	switch {
	case sys.RAMMiB == 0:
		c.add("ram", StatusInfo, "", "memory size unknown")
	case sys.RAMMiB < lowRAMMiB:
		c.add("ram", StatusWarn, doctor.INS012, fmt.Sprintf("%d MiB of RAM. Only the cpu-only profile will work well.", sys.RAMMiB))
	default:
		c.add("ram", StatusOK, "", fmt.Sprintf("%d MiB of RAM", sys.RAMMiB))
	}

	// disk
	need := uint64(o.MinFreeBytes) + extraDiskBytes
	if avail, path, ok := e.DiskAvail("/srv"); ok {
		sys.DiskPath, sys.DiskAvail = path, avail
		switch {
		case avail >= need:
			c.add("disk", StatusOK, "", fmt.Sprintf("%s free on %s", gb(avail), path))
		case o.Yes:
			c.add("disk", StatusWarn, doctor.INS013, fmt.Sprintf("only %s free on %s, %s wanted; --yes was given, so the content budget will be smaller.", gb(avail), path, gb(need)))
		default:
			c.add("disk", StatusFail, doctor.INS013, fmt.Sprintf("only %s free on %s, %s needed.", gb(avail), path, gb(need)))
		}
	} else {
		c.add("disk", StatusInfo, "", "free disk space unknown")
	}

	// ports
	if ls, ok := e.Listeners(ctx); !ok {
		c.add("ports", StatusInfo, "", "could not list the open ports (ss is missing)")
	} else if conf := detect.PortConflicts(ls); len(conf) > 0 {
		var parts []string
		blind := e.EUID != 0 // a normal user cannot see which program holds a port: only a root run can blame it
		fatal := false
		for _, p := range conf {
			parts = append(parts, fmt.Sprintf("port %d is used by %s", p.Port, p.Process))
			if !(blind && p.Process == "an unknown program") {
				fatal = true
			}
		}
		if fatal {
			c.add("ports", StatusFail, doctor.INS014, strings.Join(parts, "; ")+".")
		} else {
			c.add("ports", StatusWarn, doctor.INS014, strings.Join(parts, "; ")+" (run as root to see which program; it may be our own service).")
		}
	} else {
		c.add("ports", StatusOK, "", "ports 7070, 8080, 8081 and 8888 are free or ours")
	}

	// legacy llama units (llama-qwen, and llama-coder-next on ai-n1)
	var legacy []string
	for _, unit := range []string{services.UnitLlamaQwen, services.UnitLlamaCoderNext} {
		b, err := readFile(e, "/etc/systemd/system/"+unit)
		if err != nil {
			continue
		}
		legacy = append(legacy, unit)
		if sys.LegacyExec != "" {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
				sys.LegacyExec = v
				break
			}
		}
	}
	if len(legacy) > 0 {
		sys.LegacyService = true
		c.add("legacy", StatusInfo, "", "the old llama service is installed and will be migrated ("+strings.Join(legacy, ", ")+")")
	} else {
		c.add("legacy", StatusInfo, "", "no old llama service (llama-qwen or llama-coder-next)")
	}

	// internet (online) or ark (offline)
	if o.Ark == "" {
		if d.Internet == nil {
			c.add("internet", StatusInfo, "", "internet check skipped")
		} else if err := d.Internet(ctx); err != nil {
			c.add("internet", StatusFail, doctor.INS015, "No internet: "+err.Error()+". Connect, or use --from-ark <drive>.")
		} else {
			c.add("internet", StatusOK, "", "internet reachable")
		}
	} else {
		checkArk(ctx, d, c, o, osr, arch, gpu.Backend)
	}

	// clock (warning only, never blocks)
	clk := e.Clock(ctx)
	if clk.OK() {
		c.add("clock", StatusOK, "", fmt.Sprintf("clock says year %d", clk.Year))
	} else {
		c.add("clock", StatusWarn, doctor.INS017, fmt.Sprintf("the clock says year %d. Set the date and time, then re-run (the install continues).", clk.Year))
	}

	// power
	lap := e.DetectLaptop()
	sys.Laptop, sys.OnBattery = lap.IsLaptop, lap.OnBattery
	switch {
	case lap.IsLaptop && lap.OnBattery:
		c.add("power", StatusWarn, "", "This is a laptop running on battery. Plug in the charger before continuing.")
	case lap.IsLaptop:
		c.add("power", StatusInfo, "", "laptop on mains power")
	default:
		c.add("power", StatusInfo, "", "not a laptop")
	}

	rep.Checks = c.checks
	rep.OK = true
	for _, ch := range rep.Checks {
		if ch.Status != StatusFail {
			continue
		}
		rep.OK = false
		if refusal(ch.Code) {
			rep.Exit = ExitRefused
		} else if rep.Exit == 0 {
			rep.Exit = ExitFailed
		}
	}
	return rep
}

// checkArk is the "ark (offline)" row: ROOT.json, kit/, the catalog version, and the kit items this
// computer needs (rebuild-kit.md sections 3.2 and 4.4). Everything missing is listed at once.
func checkArk(ctx context.Context, d Deps, c *collector, o PreflightOptions, osr detect.OSRelease, arch, backend string) {
	root := filepath.Join(o.Ark, "localai")
	var missing []string
	if _, err := readFile(d.Env, filepath.Join(root, "ROOT.json")); err != nil {
		missing = append(missing, "localai/ROOT.json")
	}
	if _, err := readFile(d.Env, filepath.Join(root, "kit", "catalog", "CATALOG_VERSION")); err != nil {
		missing = append(missing, "localai/kit/catalog/CATALOG_VERSION")
	}
	if d.KitCheck != nil && len(missing) == 0 {
		facts := kit.NodeFacts{Codename: osr.Codename, Arch: arch, Backend: backend}
		more, err := d.KitCheck(ctx, root, facts)
		if err != nil {
			missing = append(missing, "the kit could not be checked: "+err.Error())
		}
		missing = append(missing, more...)
	}
	if len(missing) > 0 {
		c.add("ark", StatusFail, doctor.INS016, "The ark drive at "+o.Ark+" is missing: "+strings.Join(missing, ", ")+". Sync the drive on a computer with internet first.")
		return
	}
	c.add("ark", StatusOK, "", "ark drive "+o.Ark+" has the kit for "+orUnknown(osr.Codename)+"/"+orUnknown(arch)+"/"+backend)
}

func runPreflight(ctx context.Context, d Deps, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("preflight", stderr)
	var o PreflightOptions
	var asJSON, dry bool
	fs.BoolVar(&asJSON, "json", false, "print the report as JSON")
	fs.BoolVar(&dry, "dry-run", false, "accepted for symmetry: preflight never changes anything")
	fs.StringVar(&o.Ark, "from-ark", "", "mount point of the AI-ARK drive (offline install)")
	fs.StringVar(&o.DesktopUser, "desktop-user", "", "primary desktop user")
	fs.BoolVar(&o.Yes, "yes", false, "a disk shortfall only warns")
	fs.Int64Var(&o.MinFreeBytes, "min-free-bytes", 0, "profile storage.min_free_bytes")
	fs.StringVar(&o.Backend, "backend", "", "force the backend (cuda, vulkan, cpu)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 || (o.Backend != "" && o.Backend != "cuda" && o.Backend != "vulkan" && o.Backend != "cpu") {
		fmt.Fprint(stderr, inst02Usage)
		return ExitUsage
	}
	rep := Preflight(ctx, d, o)
	if asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "aiagent setup preflight:", err)
			return ExitFailed
		}
		fmt.Fprintln(stdout, string(b))
	} else {
		printReport(stdout, rep)
	}
	return rep.Exit
}

func printReport(w io.Writer, rep Report) {
	fmt.Fprintf(w, "Preflight (%s install)\n", rep.Source)
	for _, ch := range rep.Checks {
		tag := map[string]string{StatusOK: "ok  ", StatusInfo: "info", StatusWarn: "WARN", StatusFail: "FAIL"}[ch.Status]
		code := ""
		if ch.Code != "" && ch.Status != StatusOK {
			code = " " + ch.Code
		}
		fmt.Fprintf(w, "  [%s]%s %s: %s\n", tag, code, ch.ID, ch.Message)
		if ch.Status == StatusFail && ch.Fix != "" {
			fmt.Fprintf(w, "         fix: %s\n", ch.Fix)
		}
	}
	if rep.OK {
		fmt.Fprintf(w, "Result: this computer can be installed (backend %s).\n", rep.GPU.Backend)
	} else {
		fmt.Fprintln(w, "Result: not ready. Fix every FAIL above and run again.")
	}
}

// helpers

func getenv(e detect.Env, k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

func readFile(e detect.Env, name string) ([]byte, error) {
	if e.ReadFile == nil {
		return nil, fmt.Errorf("no filesystem")
	}
	return e.ReadFile(name)
}

func runE(ctx context.Context, e detect.Env, name string, args ...string) (string, int, error) {
	if e.Exec == nil {
		return "", -1, detect.ErrNotFound
	}
	return e.Exec(ctx, name, args...)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func gb(n uint64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
