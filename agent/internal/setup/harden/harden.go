// Package harden is `aiagent setup harden`: the laptop hardening of installer.md section 4.3 P4 (INST-03).
// A laptop that sleeps is a dead ark, so every step has an idempotency check, a verification and a doctor
// code (LAI-INS-040..044), and nothing here ever restarts, reloads or signals systemd-logind: the lid
// settings take effect at the next boot and the result says so (reboot_required).
//
// All commands go through an execx.Runner and every path is below Paths.Root, so the tests run on a temp
// folder with a fake runner. Nothing touches the real machine unless the caller builds the real thing.
package harden

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

//go:embed files/*
var embedded embed.FS

// The files the hardening writes (installer.md section 4.3 P4).
const (
	LogindDropin = "/etc/systemd/logind.conf.d/99-server.conf"
	DconfProfile = "/etc/dconf/profile/user"
	DconfPower   = "/etc/dconf/db/local.d/00-localai-power"
	NMConf       = "/etc/NetworkManager/conf.d/wifi-powersave-off.conf"
	NMDispatcher = "/etc/NetworkManager/dispatcher.d/90-wifi-powersave-off"

	// DefaultStateDir holds hardening.json and the backup folder (installer.md section 3.4).
	DefaultStateDir = "/var/lib/localai/install"

	// RebootReason is the reason recorded when the logind drop-in changed.
	RebootReason = "logind settings apply after a reboot"
)

// SleepTargets are masked by step 2.
var SleepTargets = []string{"sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target"}

const (
	gsSchema     = "org.gnome.settings-daemon.plugins.power"
	gsKeyAC      = "sleep-inactive-ac-type"
	gsKeyBattery = "sleep-inactive-battery-type"
)

// Doctor codes of this package (agent/internal/doctor/codes.go has the texts).
const (
	CodeLogind  = doctor.INS040
	CodeMasked  = doctor.INS041
	CodeGnome   = doctor.INS042
	CodeProfile = doctor.INS043
	CodeWifi    = doctor.INS044
	CodeReboot  = doctor.INS070
)

// Step statuses.
const (
	StatusDone    = "done"    // changed in this run
	StatusAlready = "already" // nothing to do
	StatusSkipped = "skipped" // not applicable here
	StatusFailed  = "failed"
)

// Paths locates the filesystem. Root is "" on a real node.
type Paths struct {
	Root     string
	StateDir string // default DefaultStateDir
}

func (p Paths) abs(path string) string { return filepath.Join(p.Root, path) }

func (p Paths) stateDir() string {
	if p.StateDir == "" {
		return p.abs(DefaultStateDir)
	}
	return p.StateDir
}

// StatePath is the file the result is recorded in (read by the doctor checks and the installer summary).
func (p Paths) StatePath() string { return filepath.Join(p.stateDir(), "hardening.json") }

// User is a local account.
type User struct {
	Name     string
	UID, GID uint32
	Home     string
}

// Options is what the caller asks for.
type Options struct {
	DryRun      bool
	Force       bool   // --hardening: harden even if this does not look like a laptop
	NoSSH       bool   // --no-ssh
	DesktopUser string // the user whose GNOME settings are set
}

// Step is the outcome of one step.
type Step struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Detail string   `json:"detail,omitempty"`
	Code   string   `json:"code,omitempty"` // doctor code when the step failed or could not be verified
	Notes  []string `json:"-"`
}

// Report is the result of Run and Revert.
type Report struct {
	Steps          []Step   `json:"steps"`
	RebootRequired bool     `json:"reboot_required"`
	RebootReason   string   `json:"reboot_reason,omitempty"`
	Backups        []string `json:"backups,omitempty"`
	Summary        []string `json:"summary,omitempty"`
	Skipped        string   `json:"skipped,omitempty"` // set when the whole thing was skipped (not a laptop)
}

// Failed lists the steps that failed.
func (r Report) Failed() []Step {
	var out []Step
	for _, s := range r.Steps {
		if s.Status == StatusFailed {
			out = append(out, s)
		}
	}
	return out
}

// Hardener runs the steps. Build it with New and replace the hooks in tests.
type Hardener struct {
	Paths Paths
	Opts  Options
	Exec  execx.Runner
	// Lookup finds a program in the fixed PATH (execx.Lookup).
	Lookup func(name string) (string, error)
	// User resolves a user name (os/user).
	User func(name string) (User, error)
	Now  func() time.Time
	// Boot returns the time the machine booted (from /proc/uptime).
	Boot func() time.Time
	Out  io.Writer

	backupDir string
	report    Report
}

// New returns a Hardener wired to the real exec, user database and clock.
func New(p Paths, o Options, out io.Writer) *Hardener {
	h := &Hardener{Paths: p, Opts: o, Exec: execx.Exec{}, Lookup: execx.Lookup, User: lookupUser, Now: time.Now, Out: out}
	h.Boot = func() time.Time { return bootTime(p, h.Now()) }
	return h
}

func (h *Hardener) logf(format string, a ...any) {
	if h.Out != nil {
		fmt.Fprintf(h.Out, format+"\n", a...)
	}
}

func (h *Hardener) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// ---------------------------------------------------------------------------------------------------------
// Running commands. Reads always run; changes are only printed under DryRun.
// ---------------------------------------------------------------------------------------------------------

// ErrLogind is returned for any command that names systemd-logind: the hard rule of this package.
var ErrLogind = errors.New("refusing to run a command that touches systemd-logind (reboot instead)")

func touchesLogind(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "systemd-logind") {
			return true
		}
	}
	return false
}

type cmdOpts struct {
	user    *User
	env     []string
	timeout time.Duration
}

// run runs a read-only command. A program that is missing is exec ErrNotFound; a non-zero exit is NOT an error.
func (h *Hardener) run(ctx context.Context, o cmdOpts, name string, args ...string) (string, int, error) {
	if touchesLogind(append([]string{name}, args...)) {
		return "", -1, ErrLogind
	}
	path, err := h.Lookup(name)
	if err != nil {
		return "", -1, err
	}
	c := execx.Cmd{Path: path, Args: args, Timeout: o.timeout, MaxOut: 1 << 20, Env: o.env}
	if c.Timeout == 0 {
		c.Timeout = 60 * time.Second
	}
	if o.user != nil {
		c.Credential = &execx.Credential{UID: o.user.UID, GID: o.user.GID}
	}
	res, err := h.Exec.Run(ctx, c)
	if err != nil {
		return "", -1, err
	}
	return strings.TrimSpace(string(res.Stdout)), res.ExitCode, nil
}

// have reports whether a program exists.
func (h *Hardener) have(name string) bool {
	_, err := h.Lookup(name)
	return err == nil
}

// change runs a command that changes the system. Dry run: print it. A non-zero exit is an error.
func (h *Hardener) change(ctx context.Context, o cmdOpts, name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	if h.Opts.DryRun {
		h.logf("[dry-run] would run: %s", line)
		return nil
	}
	h.logf("run: %s", line)
	out, rc, err := h.run(ctx, o, name, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", line, err)
	}
	if rc != 0 {
		return fmt.Errorf("%s: exit %d: %s", line, rc, firstLine(out))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------------------------------------
// Files: byte-exact, tmp + rename, explicit mode, backup of what is replaced.
// ---------------------------------------------------------------------------------------------------------

func readEmbedded(name string) []byte {
	b, err := embedded.ReadFile("files/" + name)
	if err != nil {
		panic("harden: missing embedded file " + name) // a build error, caught by the tests
	}
	return b
}

// ensureFile makes path hold want with mode. It returns whether it changed anything. A replaced file is first
// copied to <state>/backup/<UTC-ts>/<original path>.
func (h *Hardener) ensureFile(path string, want []byte, mode os.FileMode) (bool, error) {
	real := h.Paths.abs(path)
	cur, err := os.ReadFile(real)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if exists && bytes.Equal(cur, want) {
		if st, e := os.Stat(real); e == nil && st.Mode().Perm() != mode {
			if h.Opts.DryRun {
				h.logf("[dry-run] would set mode %04o on %s", mode, path)
				return true, nil
			}
			if err := os.Chmod(real, mode); err != nil {
				return false, err
			}
			h.logf("fixed mode of %s to %04o", path, mode)
			return true, nil
		}
		h.logf("already up to date: %s", path)
		return false, nil
	}
	if h.Opts.DryRun {
		if exists {
			h.logf("[dry-run] would back up and replace %s (mode %04o)", path, mode)
		} else {
			h.logf("[dry-run] would create %s (mode %04o)", path, mode)
		}
		return true, nil
	}
	if exists {
		if err := h.backup(path, real, cur); err != nil {
			return false, err
		}
	}
	if err := writeAtomic(real, want, mode); err != nil {
		return false, err
	}
	h.logf("wrote %s (mode %04o)", path, mode)
	return true, nil
}

func (h *Hardener) backup(path, real string, data []byte) error {
	if h.backupDir == "" {
		h.backupDir = filepath.Join(h.Paths.stateDir(), "backup", h.now().UTC().Format("20060102T150405Z"))
	}
	dest := filepath.Join(h.backupDir, path)
	for d := filepath.Dir(dest); strings.HasPrefix(d, filepath.Join(h.Paths.stateDir(), "backup")); d = filepath.Dir(d) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(d, 0o700)
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(real); err == nil {
		mode = st.Mode().Perm()
	}
	if err := writeAtomic(dest, data, mode); err != nil {
		return err
	}
	h.report.Backups = append(h.report.Backups, dest)
	h.logf("backed up %s to %s", path, dest)
	return nil
}

// writeAtomic writes tmp next to path, sets mode (and root ownership when running as root), then renames.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".lai-harden.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if os.Geteuid() == 0 {
		if err := tmp.Chown(0, 0); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (h *Hardener) removeFile(path string) (bool, error) {
	real := h.Paths.abs(path)
	if _, err := os.Stat(real); errors.Is(err, os.ErrNotExist) {
		h.logf("already gone: %s", path)
		return false, nil
	}
	if h.Opts.DryRun {
		h.logf("[dry-run] would remove %s", path)
		return true, nil
	}
	if err := os.Remove(real); err != nil {
		return false, err
	}
	h.logf("removed %s", path)
	return true, nil
}

// ---------------------------------------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------------------------------------

// IsLaptop reports whether the machine looks like a laptop: a battery, or a laptop chassis type in DMI.
func IsLaptop(p Paths) bool {
	if m, _ := filepath.Glob(p.abs("/sys/class/power_supply/BAT*")); len(m) > 0 {
		return true
	}
	b, err := os.ReadFile(p.abs("/sys/class/dmi/id/chassis_type"))
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(b)) {
	case "8", "9", "10", "11", "14", "30", "31", "32":
		return true
	}
	return false
}

// Run applies steps 1 to 9 (the reboot, step 8, is the installer's job: only the flag is set here). A failed
// step does not stop the following ones; the caller exits non-zero when Report.Failed is not empty.
func (h *Hardener) Run(ctx context.Context) (Report, error) {
	h.report = Report{}
	if !h.Opts.Force && !IsLaptop(h.Paths) {
		h.report.Skipped = "this does not look like a laptop; use --hardening to harden it anyway"
		h.logf("Laptop hardening skipped: %s.", h.report.Skipped)
		return h.report, nil
	}
	if h.Opts.DryRun {
		h.logf("[dry-run] laptop hardening: nothing will be changed")
	}
	prev, _ := LoadState(h.Paths, h.Boot)

	steps := []func(context.Context) Step{h.stepLogind, h.stepMask, h.stepGnome, h.stepProfile, h.stepWifi, h.stepSSH, h.stepHousekeeping}
	for i, fn := range steps {
		st := fn(ctx)
		h.logf("step %d: %s: %s%s", i+1, st.Name, st.Status, detailSuffix(st))
		h.report.Steps = append(h.report.Steps, st)
		h.report.Summary = append(h.report.Summary, st.Notes...)
	}

	// Step 8: the reboot flag. A reboot already pending from an earlier run stays pending.
	if prev.RebootRequired && !h.report.RebootRequired {
		h.report.RebootRequired, h.report.RebootReason = true, prev.RebootReason
	}
	// Step 9: the lid test goes into the summary.
	h.report.Summary = append(h.report.Summary, LidTest...)

	if !h.Opts.DryRun {
		st := State{
			Schema: 1, Applied: true, AppliedAt: h.now().UTC(), RebootRequired: h.report.RebootRequired,
			RebootReason: h.report.RebootReason, DesktopUser: h.Opts.DesktopUser, Summary: h.report.Summary,
			Failed: len(h.report.Failed()) > 0,
		}
		if err := SaveState(h.Paths, st); err != nil {
			return h.report, err
		}
	}
	if h.report.RebootRequired {
		h.logf("A reboot is needed to finish (%s): %s. Reboot when convenient (LAI-INS-070).", "lid settings", h.report.RebootReason)
	}
	return h.report, nil
}

func detailSuffix(s Step) string {
	if s.Detail == "" {
		return ""
	}
	return " (" + s.Detail + ")"
}

// LidTest is the step 9 text (source/laptop-server-setup.md section 5.1), printed in the summary.
var LidTest = []string{
	"Lid test (proves the laptop does not sleep): run this, close the lid for 5 to 10 minutes, open it and read the log.",
	`  nohup bash -c 'while true; do echo "$(date +%T) up=$(cut -d" " -f1 /proc/uptime) lid=$(awk "{print \$2}" /proc/acpi/button/lid/*/state)"; sleep 10; done' >> ~/lid-test.log 2>&1 &`,
	"  Pass: rows with lid=closed, timestamps about 10 seconds apart with no gaps, and `uptime -s` unchanged.",
	"  Then: journalctl -b -g 'lid|suspend|Suspending' --no-pager | tail   (no \"Suspending\" line).",
}

// ---------------------------------------------------------------------------------------------------------
// Step 1: logind
// ---------------------------------------------------------------------------------------------------------

func (h *Hardener) stepLogind(_ context.Context) Step {
	st := Step{ID: "logind", Name: "ignore lid, power key and idle (logind drop-in)"}
	changed, err := h.ensureFile(LogindDropin, readEmbedded("logind.conf"), 0o644)
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeLogind, err.Error()
		return st
	}
	if !changed {
		st.Status = StatusAlready
		return st
	}
	st.Status = StatusDone
	st.Detail = "takes effect after a reboot; logind is not restarted"
	h.report.RebootRequired, h.report.RebootReason = true, RebootReason
	return st
}

// ---------------------------------------------------------------------------------------------------------
// Step 2: mask the sleep targets
// ---------------------------------------------------------------------------------------------------------

func (h *Hardener) maskedAll(ctx context.Context) (bool, error) {
	for _, t := range SleepTargets {
		out, _, err := h.run(ctx, cmdOpts{}, "systemctl", "is-enabled", t)
		if err != nil {
			return false, err
		}
		if firstLine(out) != "masked" {
			return false, nil
		}
	}
	return true, nil
}

func (h *Hardener) stepMask(ctx context.Context) Step {
	st := Step{ID: "mask", Name: "mask the sleep targets"}
	ok, err := h.maskedAll(ctx)
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeMasked, err.Error()
		return st
	}
	if ok {
		st.Status = StatusAlready
		return st
	}
	if err := h.change(ctx, cmdOpts{}, "systemctl", append([]string{"mask"}, SleepTargets...)...); err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeMasked, err.Error()
		return st
	}
	st.Status = StatusDone
	if !h.Opts.DryRun {
		if ok, _ := h.maskedAll(ctx); !ok {
			st.Status, st.Code, st.Detail = StatusFailed, CodeMasked, "the targets are still not masked after systemctl mask"
		}
	}
	return st
}

// ---------------------------------------------------------------------------------------------------------
// Step 3: GNOME idle suspend off
// ---------------------------------------------------------------------------------------------------------

// MergeDconfProfile returns the /etc/dconf/profile/user content that has user-db:user first and system-db:local
// in it, keeping every other line. cur is nil when the file does not exist.
func MergeDconfProfile(cur []byte) []byte {
	if cur == nil {
		return []byte("user-db:user\nsystem-db:local\n")
	}
	text := string(cur)
	has := func(want string) bool {
		for _, l := range strings.Split(text, "\n") {
			if strings.TrimSpace(l) == want {
				return true
			}
		}
		return false
	}
	if !has("user-db:user") {
		text = "user-db:user\n" + text
	}
	if !has("system-db:local") {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "system-db:local\n"
	}
	return []byte(text)
}

func (h *Hardener) stepGnome(ctx context.Context) Step {
	st := Step{ID: "gnome", Name: "GNOME idle suspend off"}
	if !h.have("gsettings") {
		st.Status, st.Detail = StatusSkipped, "GNOME is not installed"
		return st
	}
	changed := false

	// Per-user setting, only when that user's session bus exists.
	var user *User
	busOK := false
	if h.Opts.DesktopUser != "" {
		if u, err := h.User(h.Opts.DesktopUser); err == nil {
			user = &u
			busOK = fileExists(h.Paths.abs(fmt.Sprintf("/run/user/%d/bus", u.UID)))
		}
	}
	if user != nil && busOK {
		o := gsOpts(*user)
		for _, key := range []string{gsKeyAC, gsKeyBattery} {
			out, rc, err := h.run(ctx, o, "gsettings", "get", gsSchema, key)
			if err == nil && rc == 0 && out == "'nothing'" {
				h.logf("already set: %s %s", gsSchema, key)
				continue
			}
			if err := h.change(ctx, o, "gsettings", "set", gsSchema, key, "nothing"); err != nil {
				st.Status, st.Code, st.Detail = StatusFailed, CodeGnome, err.Error()
				return st
			}
			changed = true
		}
	} else {
		who := h.Opts.DesktopUser
		if who == "" {
			who = "the desktop user"
		}
		st.Notes = append(st.Notes, fmt.Sprintf("Log in once as %s; GNOME idle suspend is off by default, but check Settings -> Power.", who))
		h.logf("no session bus for %s: only the system default is written", who)
	}

	// System default for new users and sessions without a bus. Not locked: the user can still change it.
	real := h.Paths.abs(DconfProfile)
	cur, err := os.ReadFile(real)
	if err != nil {
		cur = nil
	}
	c1, err := h.ensureFile(DconfProfile, MergeDconfProfile(cur), 0o644)
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeGnome, err.Error()
		return st
	}
	c2, err := h.ensureFile(DconfPower, readEmbedded("dconf-power"), 0o644)
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeGnome, err.Error()
		return st
	}
	if c1 || c2 {
		changed = true
		if h.have("dconf") {
			if err := h.change(ctx, cmdOpts{}, "dconf", "update"); err != nil {
				st.Status, st.Code, st.Detail = StatusFailed, CodeGnome, err.Error()
				return st
			}
		} else {
			st.Notes = append(st.Notes, "dconf is not installed: the system default will apply after it is.")
		}
	}
	st.Status = StatusAlready
	if changed {
		st.Status = StatusDone
	}
	if user != nil && busOK && !h.Opts.DryRun {
		for _, key := range []string{gsKeyAC, gsKeyBattery} {
			if out, _, _ := h.run(ctx, gsOpts(*user), "gsettings", "get", gsSchema, key); out != "'nothing'" {
				st.Status, st.Code, st.Detail = StatusFailed, CodeGnome, key+" is "+out+", not 'nothing'"
			}
		}
	}
	return st
}

func gsOpts(u User) cmdOpts {
	return cmdOpts{user: &u, env: []string{
		"PATH=" + execx.FixedPath,
		"HOME=" + u.Home,
		"XDG_RUNTIME_DIR=" + fmt.Sprintf("/run/user/%d", u.UID),
		"DBUS_SESSION_BUS_ADDRESS=" + fmt.Sprintf("unix:path=/run/user/%d/bus", u.UID),
	}}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------------------------------------------------------------------------------------------------------
// Step 4: performance power profile
// ---------------------------------------------------------------------------------------------------------

const eppPath = "/sys/devices/system/cpu/cpu0/cpufreq/energy_performance_preference"

func (h *Hardener) stepProfile(ctx context.Context) Step {
	st := Step{ID: "profile", Name: "performance power profile"}
	if !h.have("powerprofilesctl") {
		st.Status, st.Detail = StatusSkipped, "powerprofilesctl is not installed"
		st.Notes = append(st.Notes, "powerprofilesctl is missing: install power-profiles-daemon to use the performance profile.")
		return st
	}
	cur, _, err := h.run(ctx, cmdOpts{}, "powerprofilesctl", "get")
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeProfile, err.Error()
		return st
	}
	if cur == "performance" {
		st.Status = StatusAlready
	} else {
		if err := h.change(ctx, cmdOpts{}, "powerprofilesctl", "set", "performance"); err != nil {
			st.Status, st.Code, st.Detail = StatusFailed, CodeProfile, err.Error()
			return st
		}
		st.Status = StatusDone
		if !h.Opts.DryRun {
			if now, _, _ := h.run(ctx, cmdOpts{}, "powerprofilesctl", "get"); now != "performance" {
				st.Status, st.Code, st.Detail = StatusFailed, CodeProfile, "the profile is "+now+" after the change"
				return st
			}
		}
	}
	// Intel only: the energy preference follows the profile. scaling_governor=powersave is normal; not touched.
	if b, err := os.ReadFile(h.Paths.abs(eppPath)); err == nil && !h.Opts.DryRun {
		if v := strings.TrimSpace(string(b)); v != "performance" {
			st.Notes = append(st.Notes, "The CPU energy preference reads "+v+", not performance; check powerprofilesctl.")
		}
	}
	return st
}

// ---------------------------------------------------------------------------------------------------------
// Step 5: Wi-Fi power save off, three layers
// ---------------------------------------------------------------------------------------------------------

// WifiInterfaces parses `iw dev` output and returns the wl* interface names, sorted.
func WifiInterfaces(iwDev string) []string {
	var out []string
	for _, l := range strings.Split(iwDev, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "Interface" && strings.HasPrefix(f[1], "wl") {
			out = append(out, f[1])
		}
	}
	sort.Strings(out)
	return out
}

func (h *Hardener) stepWifi(ctx context.Context) Step {
	st := Step{ID: "wifi", Name: "Wi-Fi power save off"}
	if !h.have("iw") {
		st.Status, st.Detail = StatusSkipped, "no Wi-Fi tools (iw) found"
		return st
	}
	out, _, err := h.run(ctx, cmdOpts{}, "iw", "dev")
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeWifi, err.Error()
		return st
	}
	ifaces := WifiInterfaces(out)
	if len(ifaces) == 0 {
		st.Status, st.Detail = StatusSkipped, "no Wi-Fi interface"
		return st
	}
	st.Notes = append(st.Notes, "Wi-Fi can still stall; an Ethernet cable is strongly recommended for a server laptop.")
	changed := false
	c1, err := h.ensureFile(NMConf, readEmbedded("wifi-powersave-off.conf"), 0o644)
	if err == nil {
		var c2 bool
		c2, err = h.ensureFile(NMDispatcher, readEmbedded("90-wifi-powersave-off"), 0o755)
		changed = c1 || c2
		if c1 && err == nil {
			if h.have("nmcli") {
				err = h.change(ctx, cmdOpts{}, "nmcli", "general", "reload", "conf")
			} else {
				st.Notes = append(st.Notes, "NetworkManager (nmcli) was not found: the Wi-Fi setting applies at the next network restart.")
			}
		}
	}
	if err != nil {
		st.Status, st.Code, st.Detail = StatusFailed, CodeWifi, err.Error()
		return st
	}
	for _, ifc := range ifaces {
		got, _, _ := h.run(ctx, cmdOpts{}, "iw", "dev", ifc, "get", "power_save")
		if strings.Contains(got, "Power save: off") {
			h.logf("already off: %s", ifc)
			continue
		}
		if err := h.change(ctx, cmdOpts{}, "iw", "dev", ifc, "set", "power_save", "off"); err != nil {
			st.Status, st.Code, st.Detail = StatusFailed, CodeWifi, err.Error()
			return st
		}
		changed = true
		if !h.Opts.DryRun {
			if got, _, _ := h.run(ctx, cmdOpts{}, "iw", "dev", ifc, "get", "power_save"); !strings.Contains(got, "Power save: off") {
				st.Status, st.Code, st.Detail = StatusFailed, CodeWifi, ifc+" still reports: "+got
				return st
			}
		}
	}
	st.Status = StatusAlready
	if changed {
		st.Status = StatusDone
	}
	st.Detail = strings.Join(ifaces, ", ")
	return st
}

// ---------------------------------------------------------------------------------------------------------
// Step 6: remote access
// ---------------------------------------------------------------------------------------------------------

func (h *Hardener) stepSSH(ctx context.Context) Step {
	st := Step{ID: "ssh", Name: "remote access (OpenSSH)"}
	if h.Opts.NoSSH {
		st.Status, st.Detail = StatusSkipped, "--no-ssh"
		return st
	}
	changed := false
	installed := false
	if out, _, err := h.run(ctx, cmdOpts{}, "dpkg-query", "-W", "-f=${Status}", "openssh-server"); err == nil && strings.Contains(out, "install ok installed") {
		installed = true
	}
	if !installed {
		o := cmdOpts{env: []string{"PATH=" + execx.FixedPath, "DEBIAN_FRONTEND=noninteractive"}, timeout: 10 * time.Minute}
		if err := h.change(ctx, o, "apt-get", "install", "-y", "openssh-server"); err != nil {
			st.Status, st.Detail = StatusFailed, err.Error()
			return st
		}
		changed = true
	}
	en, _, _ := h.run(ctx, cmdOpts{}, "systemctl", "is-enabled", "ssh")
	ac, _, _ := h.run(ctx, cmdOpts{}, "systemctl", "is-active", "ssh")
	if en != "enabled" || ac != "active" {
		if err := h.change(ctx, cmdOpts{}, "systemctl", "enable", "--now", "ssh"); err != nil {
			st.Status, st.Detail = StatusFailed, err.Error()
			return st
		}
		changed = true
	}
	// Plain OpenSSH is used, not Tailscale SSH.
	if h.have("tailscale") {
		prefs, _, _ := h.run(ctx, cmdOpts{}, "tailscale", "debug", "prefs")
		if !strings.Contains(prefs, `"RunSSH": false`) {
			if err := h.change(ctx, cmdOpts{}, "tailscale", "set", "--ssh=false"); err != nil {
				st.Status, st.Detail = StatusFailed, err.Error()
				return st
			}
			changed = true
		}
	}
	st.Status = StatusAlready
	if changed {
		st.Status = StatusDone
	}
	return st
}

// ---------------------------------------------------------------------------------------------------------
// Step 7: housekeeping, report only
// ---------------------------------------------------------------------------------------------------------

func (h *Hardener) stepHousekeeping(ctx context.Context) Step {
	st := Step{ID: "housekeeping", Name: "housekeeping checks (report only)", Status: StatusAlready}
	if h.have("ufw") {
		out, _, _ := h.run(ctx, cmdOpts{}, "ufw", "status")
		state := "inactive"
		if strings.Contains(out, "Status: active") {
			state = "active"
		}
		st.Notes = append(st.Notes, "Firewall (ufw) is "+state+". It was left as it is; decide on purpose whether you want it.")
	} else {
		st.Notes = append(st.Notes, "Firewall (ufw) is not installed. It was left as it is; decide on purpose whether you want it.")
	}
	if b, err := os.ReadFile(h.Paths.abs("/etc/apt/apt.conf.d/50unattended-upgrades")); err == nil {
		on := false
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "Unattended-Upgrade::Automatic-Reboot ") && strings.Contains(l, `"true"`) {
				on = true
			}
		}
		if on {
			st.Notes = append(st.Notes, "Unattended upgrades may reboot the computer by themselves (Automatic-Reboot is true). Keeping it off is recommended: reboot kernels by hand.")
		}
	}
	ac, _ := filepath.Glob(h.Paths.abs("/sys/class/power_supply/AC*/online"))
	if len(ac) > 0 {
		plugged := false
		for _, f := range ac {
			if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) == "1" {
				plugged = true
			}
		}
		if !plugged {
			st.Notes = append(st.Notes, "Plug in: a discharging battery means the server dies when it empties.")
		}
	}
	st.Notes = append(st.Notes,
		"Set in the BIOS: \"Power on after AC restore\" (the laptop comes back after a power cut).",
		"Set a battery charge limit in the BIOS if it has one.",
		"Use an Ethernet cable if you can: Wi-Fi can still stall.")
	return st
}
