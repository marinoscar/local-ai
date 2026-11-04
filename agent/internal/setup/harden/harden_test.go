package harden

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// sim is a fake machine behind execx.Runner: it answers the read commands from its state, applies the
// change commands to it, and records every command line.
type sim struct {
	calls     []string
	progs     map[string]bool
	masked    bool
	gs        map[string]string
	profile   string
	ps        map[string]string // wifi iface -> "on" | "off"
	iwDev     string
	sshOK     bool
	tsRunSSH  bool
	logindCfg string // what systemd-analyze cat-config prints
	failOn    string // a command fragment that exits 1
}

func newSim() *sim {
	s := &sim{
		progs: map[string]bool{"systemctl": true, "gsettings": true, "dconf": true, "powerprofilesctl": true, "iw": true,
			"nmcli": true, "dpkg-query": true, "apt-get": true, "tailscale": true, "ufw": true, "systemd-analyze": true},
		gs:        map[string]string{gsKeyAC: "'suspend'", gsKeyBattery: "'suspend'"},
		profile:   "balanced",
		ps:        map[string]string{"wlp1s0": "on"},
		iwDev:     "phy#0\n\tInterface wlp1s0\n\t\tifindex 3\n",
		tsRunSSH:  true,
		logindCfg: "[Login]\n",
	}
	return s
}

func (s *sim) lookup(name string) (string, error) {
	if s.progs[name] {
		return "/usr/bin/" + name, nil
	}
	return "", execx.ErrNotFound
}

func (s *sim) mutating() []string {
	var out []string
	for _, c := range s.calls {
		for _, m := range []string{"systemctl mask", "systemctl unmask", "systemctl enable", "gsettings set", "dconf update", "powerprofilesctl set",
			"nmcli general reload", "set power_save", "apt-get", "tailscale set"} {
			if strings.Contains(c, m) {
				out = append(out, c)
			}
		}
	}
	return out
}

func (s *sim) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	line := strings.TrimPrefix(strings.Join(append([]string{c.Path}, c.Args...), " "), "/usr/bin/")
	s.calls = append(s.calls, line)
	if s.failOn != "" && strings.Contains(line, s.failOn) {
		return execx.Result{ExitCode: 1, Stdout: []byte("boom\n")}, nil
	}
	out := func(v string) (execx.Result, error) { return execx.Result{Stdout: []byte(v + "\n")}, nil }
	switch {
	case strings.HasPrefix(line, "systemctl is-enabled ssh"):
		if s.sshOK {
			return out("enabled")
		}
		return execx.Result{Stdout: []byte("disabled\n"), ExitCode: 1}, nil
	case strings.HasPrefix(line, "systemctl is-active ssh"):
		if s.sshOK {
			return out("active")
		}
		return execx.Result{Stdout: []byte("inactive\n"), ExitCode: 3}, nil
	case strings.HasPrefix(line, "systemctl is-enabled"):
		if s.masked {
			return execx.Result{Stdout: []byte("masked\n"), ExitCode: 1}, nil
		}
		return out("static")
	case strings.HasPrefix(line, "systemctl mask"):
		s.masked = true
	case strings.HasPrefix(line, "systemctl unmask"):
		s.masked = false
	case strings.HasPrefix(line, "systemctl enable --now ssh"):
		s.sshOK = true
	case strings.HasPrefix(line, "gsettings get"):
		return out(s.gs[c.Args[2]])
	case strings.HasPrefix(line, "gsettings set"):
		s.gs[c.Args[2]] = "'" + c.Args[3] + "'"
	case line == "powerprofilesctl get":
		return out(s.profile)
	case line == "powerprofilesctl set performance":
		s.profile = "performance"
	case line == "iw dev":
		return out(s.iwDev)
	case strings.HasPrefix(line, "iw dev ") && strings.HasSuffix(line, "get power_save"):
		return out("Power save: " + s.ps[c.Args[1]])
	case strings.HasPrefix(line, "iw dev ") && strings.HasSuffix(line, "set power_save off"):
		s.ps[c.Args[1]] = "off"
	case strings.HasPrefix(line, "dpkg-query"):
		if s.sshOK {
			return out("install ok installed")
		}
		return execx.Result{ExitCode: 1}, nil
	case strings.HasPrefix(line, "apt-get install"):
		s.sshOK = false // installed, not yet enabled
	case strings.HasPrefix(line, "tailscale debug prefs"):
		if s.tsRunSSH {
			return out(`{"RunSSH": true}`)
		}
		return out(`{"RunSSH": false}`)
	case strings.HasPrefix(line, "tailscale set --ssh=false"):
		s.tsRunSSH = false
	case line == "ufw status":
		return out("Status: inactive")
	case strings.HasPrefix(line, "systemd-analyze cat-config"):
		return out(s.logindCfg)
	}
	return execx.Result{}, nil
}

type rig struct {
	t     *testing.T
	root  string
	state string
	sim   *sim
	h     *Hardener
	out   *bytes.Buffer
	now   time.Time
	boot  time.Time
}

// newRig builds a laptop: a battery, a desktop user with a session bus, all under a temp root.
func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, root: root, state: filepath.Join(root, "var/lib/localai/install"), sim: newSim(), out: &bytes.Buffer{},
		now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	r.boot = r.now.Add(-time.Hour)
	r.write("/sys/class/power_supply/BAT0/type", "Battery\n")
	r.mkdir("/run/user/1000")
	r.write("/run/user/1000/bus", "")
	if opts.DesktopUser == "" {
		opts.DesktopUser = "alice"
	}
	r.h = &Hardener{
		Paths: Paths{Root: root}, Opts: opts, Exec: r.sim, Lookup: r.sim.lookup, Out: r.out,
		User: func(n string) (User, error) {
			if n != "alice" {
				return User{}, errors.New("no such user")
			}
			return User{Name: n, UID: 1000, GID: 1000, Home: "/home/alice"}, nil
		},
		Now:  func() time.Time { return r.now },
		Boot: func() time.Time { return r.boot },
	}
	return r
}

func (r *rig) mkdir(p string) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(r.root, p), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) write(p, s string) {
	r.t.Helper()
	r.mkdir(filepath.Dir(p))
	if err := os.WriteFile(filepath.Join(r.root, p), []byte(s), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(p string) string {
	b, err := os.ReadFile(filepath.Join(r.root, p))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (r *rig) exists(p string) bool { return fileExists(filepath.Join(r.root, p)) }

func (r *rig) run() Report {
	r.t.Helper()
	rep, err := r.h.Run(context.Background())
	if err != nil {
		r.t.Fatalf("Run: %v\n%s", err, r.out)
	}
	return rep
}

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func stepOf(rep Report, id string) Step {
	for _, s := range rep.Steps {
		if s.ID == id {
			return s
		}
	}
	return Step{ID: id, Status: "<absent>"}
}

func TestFilesAreByteExact(t *testing.T) {
	r := newRig(t, Options{})
	rep := r.run()
	if f := rep.Failed(); len(f) > 0 {
		t.Fatalf("failed steps: %+v\n%s", f, r.out)
	}
	for _, c := range []struct {
		path, golden string
		mode         os.FileMode
	}{
		{LogindDropin, "logind.conf", 0o644},
		{DconfPower, "dconf-power", 0o644},
		{DconfProfile, "dconf-profile", 0o644},
		{NMConf, "wifi-powersave-off.conf", 0o644},
		{NMDispatcher, "90-wifi-powersave-off", 0o755},
	} {
		if got := r.read(c.path); got != golden(t, c.golden) {
			t.Errorf("%s differs from golden %s:\n%q", c.path, c.golden, got)
		}
		st, err := os.Stat(filepath.Join(r.root, c.path))
		if err != nil || st.Mode().Perm() != c.mode {
			t.Errorf("%s mode = %v, want %04o (%v)", c.path, st.Mode().Perm(), c.mode, err)
		}
	}
	if !rep.RebootRequired || rep.RebootReason != RebootReason {
		t.Errorf("reboot flag = %v %q", rep.RebootRequired, rep.RebootReason)
	}
	st, _ := LoadState(r.h.Paths, func() time.Time { return r.boot })
	if !st.Applied || !st.RebootRequired || st.DesktopUser != "alice" {
		t.Errorf("state = %+v", st)
	}
	if len(rep.Backups) != 0 {
		t.Errorf("fresh install made backups: %v", rep.Backups)
	}
	for _, id := range []string{"logind", "mask", "gnome", "profile", "wifi", "ssh"} {
		if s := stepOf(rep, id); s.Status != StatusDone {
			t.Errorf("step %s = %s (%s)", id, s.Status, s.Detail)
		}
	}
	text := strings.Join(rep.Summary, "\n")
	for _, want := range []string{"Lid test", "Ethernet cable", "Power on after AC restore", "Firewall (ufw) is inactive"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
}

func TestIdempotentRerunChangesNothing(t *testing.T) {
	r := newRig(t, Options{})
	r.run()
	r.h.backupDir = ""
	r.sim.calls = nil
	rep := r.run()
	for _, s := range rep.Steps {
		if s.Status != StatusAlready {
			t.Errorf("step %s = %s on re-run (%s)", s.ID, s.Status, s.Detail)
		}
	}
	if m := r.sim.mutating(); len(m) > 0 {
		t.Errorf("re-run changed things: %v", m)
	}
	if len(rep.Backups) != 0 || r.exists("/var/lib/localai/install/backup") {
		t.Errorf("re-run created backups: %v", rep.Backups)
	}
	if !rep.RebootRequired {
		t.Error("a reboot that has not happened yet must stay required")
	}
}

func TestDifferentLogindDropinIsBackedUpThenReplaced(t *testing.T) {
	r := newRig(t, Options{})
	r.write(LogindDropin, "[Login]\nHandleLidSwitch=suspend\n")
	rep := r.run()
	if len(rep.Backups) != 1 {
		t.Fatalf("backups = %v", rep.Backups)
	}
	b := rep.Backups[0]
	want := filepath.Join(r.state, "backup", "20261104T120000Z", LogindDropin)
	if b != want {
		t.Errorf("backup path %s, want %s", b, want)
	}
	if got, _ := os.ReadFile(b); string(got) != "[Login]\nHandleLidSwitch=suspend\n" {
		t.Errorf("backup content %q", got)
	}
	if r.read(LogindDropin) != golden(t, "logind.conf") {
		t.Error("the drop-in was not replaced")
	}
	if st, _ := os.Stat(filepath.Dir(b)); st.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %v", st.Mode().Perm())
	}
}

func TestEarlierManualSetupIsAlreadyDone(t *testing.T) {
	r := newRig(t, Options{})
	r.write(LogindDropin, golden(t, "logind.conf"))
	r.sim.masked = true
	rep := r.run()
	if s := stepOf(rep, "logind"); s.Status != StatusAlready {
		t.Errorf("logind = %s", s.Status)
	}
	if rep.RebootRequired {
		t.Error("an identical drop-in needs no reboot")
	}
	if s := stepOf(rep, "mask"); s.Status != StatusAlready {
		t.Errorf("mask = %s", s.Status)
	}
}

func TestLaptopVersusDesktop(t *testing.T) {
	r := newRig(t, Options{})
	if err := os.RemoveAll(filepath.Join(r.root, "sys")); err != nil {
		t.Fatal(err)
	}
	rep := r.run()
	if rep.Skipped == "" || len(rep.Steps) != 0 || len(r.sim.calls) != 0 || r.exists(LogindDropin) {
		t.Errorf("a desktop was hardened: %+v", rep)
	}
	r.h.Opts.Force = true
	rep = r.run()
	if rep.Skipped != "" || !r.exists(LogindDropin) {
		t.Error("--hardening did not force it")
	}
	// Chassis type also counts.
	r2 := newRig(t, Options{})
	os.RemoveAll(filepath.Join(r2.root, "sys"))
	r2.write("/sys/class/dmi/id/chassis_type", "10\n")
	if !IsLaptop(r2.h.Paths) {
		t.Error("chassis 10 is a laptop")
	}
	r2.write("/sys/class/dmi/id/chassis_type", "3\n")
	if IsLaptop(r2.h.Paths) {
		t.Error("chassis 3 is a desktop")
	}
}

func TestGnomeAbsent(t *testing.T) {
	r := newRig(t, Options{})
	delete(r.sim.progs, "gsettings")
	rep := r.run()
	if s := stepOf(rep, "gnome"); s.Status != StatusSkipped {
		t.Errorf("gnome = %s", s.Status)
	}
	if r.exists(DconfPower) || r.exists(DconfProfile) {
		t.Error("dconf files written without GNOME")
	}
}

func TestNoSessionBus(t *testing.T) {
	r := newRig(t, Options{})
	os.Remove(filepath.Join(r.root, "run/user/1000/bus"))
	rep := r.run()
	for _, c := range r.sim.calls {
		if strings.HasPrefix(c, "gsettings set") {
			t.Errorf("gsettings set without a bus: %s", c)
		}
	}
	if !strings.Contains(strings.Join(rep.Summary, "\n"), "Log in once as alice; GNOME idle suspend is off by default, but check Settings -> Power.") {
		t.Errorf("no log-in-once note: %v", rep.Summary)
	}
	if r.read(DconfPower) != golden(t, "dconf-power") {
		t.Error("the system default was not written")
	}
	if s := stepOf(rep, "gnome"); s.Status != StatusDone {
		t.Errorf("gnome = %s", s.Status)
	}
}

func TestGsettingsRunsAsTheDesktopUser(t *testing.T) {
	r := newRig(t, Options{})
	var seen *execx.Cmd
	r.h.Exec = runnerFunc(func(c execx.Cmd) (execx.Result, error) {
		if len(c.Args) > 0 && c.Args[0] == "set" && strings.HasSuffix(c.Path, "gsettings") {
			cc := c
			seen = &cc
		}
		return r.sim.Run(context.Background(), c)
	})
	r.run()
	if seen == nil || seen.Credential == nil || seen.Credential.UID != 1000 {
		t.Fatalf("gsettings set not run as uid 1000: %+v", seen)
	}
	env := strings.Join(seen.Env, " ")
	if !strings.Contains(env, "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus") {
		t.Errorf("env = %s", env)
	}
	if r.sim.gs[gsKeyAC] != "'nothing'" || r.sim.gs[gsKeyBattery] != "'nothing'" {
		t.Errorf("gs = %v", r.sim.gs)
	}
}

type runnerFunc func(execx.Cmd) (execx.Result, error)

func (f runnerFunc) Run(_ context.Context, c execx.Cmd) (execx.Result, error) { return f(c) }

func TestDconfProfileMerge(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"missing file", "\x00", "user-db:user\nsystem-db:local\n"},
		{"already right", "user-db:user\nsystem-db:local\n", "user-db:user\nsystem-db:local\n"},
		{"only user", "user-db:user\n", "user-db:user\nsystem-db:local\n"},
		{"only user no newline", "user-db:user", "user-db:user\nsystem-db:local\n"},
		{"only system", "system-db:local\n", "user-db:user\nsystem-db:local\n"},
		{"other lines kept", "user-db:user\nsystem-db:site\n", "user-db:user\nsystem-db:site\nsystem-db:local\n"},
		{"empty", "", "user-db:user\nsystem-db:local\n"},
	} {
		var in []byte
		if c.in != "\x00" {
			in = []byte(c.in)
			if in == nil {
				in = []byte{}
			}
		}
		if got := string(MergeDconfProfile(in)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExistingDconfProfileIsBackedUp(t *testing.T) {
	r := newRig(t, Options{})
	r.write(DconfProfile, "system-db:site\n")
	rep := r.run()
	if r.read(DconfProfile) != "user-db:user\nsystem-db:site\nsystem-db:local\n" || len(rep.Backups) != 1 {
		t.Errorf("profile = %q backups = %v", r.read(DconfProfile), rep.Backups)
	}
}

func TestWifiInterfaces(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"none", "", nil},
		{"ethernet only", "phy#0\n\tInterface p2p-dev-x\n", nil},
		{"one", "\tInterface wlp58s0\n", []string{"wlp58s0"}},
		{"two", "\tInterface wlx2\n\tInterface wlan0\n\tInterface p2p-dev-wlan0\n", []string{"wlan0", "wlx2"}},
	} {
		got := WifiInterfaces(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWifiCases(t *testing.T) {
	t.Run("no wl interface", func(t *testing.T) {
		r := newRig(t, Options{})
		r.sim.iwDev = "phy#0\n\tInterface eth0\n"
		rep := r.run()
		if s := stepOf(rep, "wifi"); s.Status != StatusSkipped {
			t.Errorf("wifi = %s", s.Status)
		}
		if r.exists(NMConf) || r.exists(NMDispatcher) {
			t.Error("wifi files written without a wl interface")
		}
	})
	t.Run("iw missing", func(t *testing.T) {
		r := newRig(t, Options{})
		delete(r.sim.progs, "iw")
		if s := stepOf(r.run(), "wifi"); s.Status != StatusSkipped {
			t.Errorf("wifi = %s", s.Status)
		}
	})
	t.Run("two interfaces", func(t *testing.T) {
		r := newRig(t, Options{})
		r.sim.iwDev = "\tInterface wlan0\n\tInterface wlan1\n"
		r.sim.ps = map[string]string{"wlan0": "on", "wlan1": "off"}
		rep := r.run()
		if r.sim.ps["wlan0"] != "off" {
			t.Error("wlan0 still on")
		}
		sets := 0
		for _, c := range r.sim.calls {
			if strings.HasSuffix(c, "set power_save off") {
				sets++
			}
		}
		if sets != 1 {
			t.Errorf("set power_save ran %d times, want 1 (wlan1 was already off)", sets)
		}
		if s := stepOf(rep, "wifi"); s.Detail != "wlan0, wlan1" {
			t.Errorf("detail %q", s.Detail)
		}
	})
	t.Run("the set does not stick", func(t *testing.T) {
		r := newRig(t, Options{})
		r.h.Exec = runnerFunc(func(c execx.Cmd) (execx.Result, error) {
			if strings.HasSuffix(strings.Join(c.Args, " "), "set power_save off") {
				return execx.Result{}, nil
			}
			return r.sim.Run(context.Background(), c)
		})
		rep := r.run()
		s := stepOf(rep, "wifi")
		if s.Status != StatusFailed || s.Code != CodeWifi {
			t.Errorf("wifi = %+v", s)
		}
		if len(rep.Failed()) != 1 {
			t.Errorf("only wifi should fail: %+v", rep.Failed())
		}
	})
	t.Run("nm reload only when the conf changed", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		r.sim.calls = nil
		r.run()
		for _, c := range r.sim.calls {
			if strings.Contains(c, "nmcli") {
				t.Errorf("nmcli ran on a re-run: %s", c)
			}
		}
	})
}

func TestPowerProfile(t *testing.T) {
	t.Run("sets performance", func(t *testing.T) {
		r := newRig(t, Options{})
		if s := stepOf(r.run(), "profile"); s.Status != StatusDone || r.sim.profile != "performance" {
			t.Errorf("profile = %+v", s)
		}
	})
	t.Run("powerprofilesctl missing", func(t *testing.T) {
		r := newRig(t, Options{})
		delete(r.sim.progs, "powerprofilesctl")
		rep := r.run()
		if s := stepOf(rep, "profile"); s.Status != StatusSkipped {
			t.Errorf("profile = %s", s.Status)
		}
		if len(rep.Failed()) != 0 {
			t.Errorf("failed: %+v", rep.Failed())
		}
	})
	t.Run("intel EPP present and wrong is reported, not changed", func(t *testing.T) {
		r := newRig(t, Options{})
		r.write(eppPath, "balance_performance\n")
		rep := r.run()
		if !strings.Contains(strings.Join(rep.Summary, "\n"), "energy preference reads balance_performance") {
			t.Errorf("no EPP note: %v", rep.Summary)
		}
		if r.read(eppPath) != "balance_performance\n" {
			t.Error("EPP file was touched")
		}
	})
	t.Run("EPP absent is fine", func(t *testing.T) {
		r := newRig(t, Options{})
		if rep := r.run(); len(rep.Failed()) != 0 {
			t.Errorf("failed: %+v", rep.Failed())
		}
	})
}

func TestSSH(t *testing.T) {
	t.Run("no-ssh", func(t *testing.T) {
		r := newRig(t, Options{NoSSH: true})
		rep := r.run()
		if s := stepOf(rep, "ssh"); s.Status != StatusSkipped {
			t.Errorf("ssh = %s", s.Status)
		}
		for _, c := range r.sim.calls {
			if strings.Contains(c, "apt-get") || strings.Contains(c, "ssh") || strings.Contains(c, "tailscale") {
				t.Errorf("--no-ssh ran %s", c)
			}
		}
	})
	t.Run("installs, enables, turns tailscale ssh off", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		for _, want := range []string{"apt-get install -y openssh-server", "systemctl enable --now ssh", "tailscale set --ssh=false"} {
			found := false
			for _, c := range r.sim.calls {
				found = found || c == want
			}
			if !found {
				t.Errorf("missing call %q in %v", want, r.sim.calls)
			}
		}
	})
	t.Run("no tailscale", func(t *testing.T) {
		r := newRig(t, Options{})
		delete(r.sim.progs, "tailscale")
		if rep := r.run(); len(rep.Failed()) != 0 {
			t.Errorf("failed: %+v", rep.Failed())
		}
	})
}

func TestMaskFailureCarriesItsCode(t *testing.T) {
	r := newRig(t, Options{})
	r.sim.failOn = "systemctl mask"
	rep := r.run()
	s := stepOf(rep, "mask")
	if s.Status != StatusFailed || s.Code != CodeMasked {
		t.Errorf("mask = %+v", s)
	}
	if stepOf(rep, "profile").Status != StatusDone {
		t.Error("a failed step must not stop the others")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	r := newRig(t, Options{DryRun: true})
	r.write(LogindDropin, "old\n")
	rep := r.run()
	if m := r.sim.mutating(); len(m) > 0 {
		t.Errorf("dry run mutated: %v", m)
	}
	if r.read(LogindDropin) != "old\n" {
		t.Error("dry run replaced a file")
	}
	for _, p := range []string{DconfPower, NMConf, NMDispatcher, "/var/lib/localai/install"} {
		if r.exists(p) {
			t.Errorf("dry run created %s", p)
		}
	}
	if !strings.Contains(r.out.String(), "[dry-run] would back up and replace "+LogindDropin) ||
		!strings.Contains(r.out.String(), "[dry-run] would run: systemctl mask sleep.target") {
		t.Errorf("dry run output:\n%s", r.out)
	}
	if rep.RebootRequired != true {
		t.Error("dry run should still say a reboot would be needed")
	}
}

func TestNeverTouchesLogind(t *testing.T) {
	r := newRig(t, Options{})
	r.run()
	r.h.Opts.DryRun = false
	if _, err := r.h.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.sim.calls {
		if strings.Contains(c, "logind") {
			t.Errorf("a command touched logind: %s", c)
		}
	}
	for _, args := range [][]string{{"restart", "systemd-logind"}, {"kill", "-s", "HUP", "systemd-logind.service"}, {"reload", "systemd-logind"}} {
		if _, _, err := r.h.run(context.Background(), cmdOpts{}, "systemctl", args...); !errors.Is(err, ErrLogind) {
			t.Errorf("%v was not refused: %v", args, err)
		}
	}
	if _, _, err := r.h.run(context.Background(), cmdOpts{}, "kill", "-HUP", "systemd-logind"); !errors.Is(err, ErrLogind) {
		t.Error("kill was not refused")
	}
}

func TestRevert(t *testing.T) {
	t.Run("removes what it created", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		r.boot = r.now.Add(time.Hour) // pretend a reboot happened, so only the revert asks for one
		r.now = r.now.Add(2 * time.Hour)
		r.boot = r.now.Add(-time.Minute)
		rep, err := r.h.Revert(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{LogindDropin, DconfPower, NMConf, NMDispatcher} {
			if r.exists(p) {
				t.Errorf("%s still exists", p)
			}
		}
		if r.sim.masked {
			t.Error("targets still masked")
		}
		if !rep.RebootRequired {
			t.Error("removing the logind drop-in needs a reboot")
		}
		if f := rep.Failed(); len(f) > 0 {
			t.Errorf("failed: %+v", f)
		}
		if !r.exists("/etc/dconf/profile/user") {
			t.Error("the dconf profile must be left alone")
		}
		if r.sim.profile != "performance" || !r.sim.sshOK {
			t.Error("revert must leave the profile and SSH alone")
		}
		st, _ := LoadState(r.h.Paths, nil)
		if st.Applied {
			t.Error("state still says applied")
		}
		if fs := r.h.Verify(context.Background()); len(fs) != 0 {
			t.Errorf("doctor reports on a reverted node: %v", fs)
		}
	})
	t.Run("restores the original from the oldest backup", func(t *testing.T) {
		r := newRig(t, Options{})
		r.write(LogindDropin, "[Login]\nHandleLidSwitch=suspend\n")
		r.run()
		// A later run with a changed file makes a newer backup of OUR text.
		r.write(LogindDropin, "[Login]\nHandleLidSwitch=lock\n")
		r.now = r.now.Add(time.Minute)
		r.h.backupDir = ""
		r.run()
		if _, err := r.h.Revert(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := r.read(LogindDropin); got != "[Login]\nHandleLidSwitch=suspend\n" {
			t.Errorf("restored %q", got)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		r.h.Revert(context.Background())
		r.sim.calls = nil
		rep, _ := r.h.Revert(context.Background())
		for _, s := range rep.Steps {
			if s.Status != StatusAlready {
				t.Errorf("%s = %s on the second revert", s.ID, s.Status)
			}
		}
		if m := r.sim.mutating(); len(m) > 0 {
			t.Errorf("second revert changed things: %v", m)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		r.h.Opts.DryRun = true
		r.sim.calls = nil
		r.h.Revert(context.Background())
		if !r.exists(LogindDropin) || !r.sim.masked || len(r.sim.mutating()) > 0 {
			t.Error("dry-run revert changed things")
		}
	})
}

func TestDoctorChecks(t *testing.T) {
	ctx := context.Background()
	codes := func(r *rig) string {
		var out []string
		for _, f := range r.h.Verify(ctx) {
			out = append(out, f.Code)
		}
		return strings.Join(out, ",")
	}
	healthy := func(t *testing.T) *rig {
		r := newRig(t, Options{})
		r.run()
		r.boot = r.now.Add(time.Minute) // rebooted after the hardening
		r.sim.logindCfg = "[Login]\n" + golden(t, "logind.conf")
		return r
	}
	t.Run("nothing on a node that was never hardened", func(t *testing.T) {
		r := newRig(t, Options{})
		if c := codes(r); c != "" {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("pending reboot is 070 and not 040", func(t *testing.T) {
		r := newRig(t, Options{})
		r.run()
		if c := codes(r); c != CodeReboot {
			t.Errorf("codes %q, want %s", c, CodeReboot)
		}
	})
	t.Run("all good after the reboot", func(t *testing.T) {
		if c := codes(healthy(t)); c != "" {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("040 when logind ignores the file", func(t *testing.T) {
		r := healthy(t)
		r.sim.logindCfg = "[Login]\nHandleLidSwitch=suspend\n"
		if c := codes(r); c != CodeLogind {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("041 043 044 042", func(t *testing.T) {
		r := healthy(t)
		r.sim.masked = false
		r.sim.profile = "balanced"
		r.sim.ps["wlp1s0"] = "on"
		r.sim.gs[gsKeyAC] = "'suspend'"
		if c := codes(r); c != strings.Join([]string{CodeMasked, CodeGnome, CodeProfile, CodeWifi}, ",") {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("EPP wrong is 043", func(t *testing.T) {
		r := healthy(t)
		r.write(eppPath, "power\n")
		if c := codes(r); c != CodeProfile {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("gnome without a bus checks the system default", func(t *testing.T) {
		r := healthy(t)
		os.Remove(filepath.Join(r.root, "run/user/1000/bus"))
		if c := codes(r); c != "" {
			t.Errorf("codes %q", c)
		}
		os.Remove(filepath.Join(r.root, DconfPower))
		if c := codes(r); c != CodeGnome {
			t.Errorf("codes %q", c)
		}
	})
	t.Run("registered with the doctor", func(t *testing.T) {
		r := healthy(t)
		for _, c := range r.h.Checks() {
			if c.Area != "INS" || !strings.HasPrefix(c.ID, "ins.") {
				t.Errorf("check %+v", c)
			}
		}
		if len(r.h.Checks()) != 5 {
			t.Errorf("%d checks", len(r.h.Checks()))
		}
	})
}

func TestEffectiveLogind(t *testing.T) {
	m := EffectiveLogind("# /etc/systemd/logind.conf\n[Login]\n#HandlePowerKey=poweroff\nHandleLidSwitch=suspend\n\n# /etc/systemd/logind.conf.d/99-server.conf\n[Login]\nHandleLidSwitch=ignore\nIdleAction = ignore\n")
	if m["HandleLidSwitch"] != "ignore" || m["IdleAction"] != "ignore" || m["HandlePowerKey"] != "" {
		t.Errorf("%v", m)
	}
}

func TestBootTime(t *testing.T) {
	r := newRig(t, Options{})
	r.write("/proc/uptime", "3600.50 12345.6\n")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	got := bootTime(r.h.Paths, now)
	if d := now.Sub(got); d < 3600*time.Second || d > 3601*time.Second {
		t.Errorf("boot = %v", got)
	}
	if !bootTime(Paths{Root: t.TempDir()}, now).IsZero() {
		t.Error("missing /proc/uptime should give the zero time")
	}
}

func TestStateRebootFlagClearsAfterBoot(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := SaveState(p, State{Schema: 1, Applied: true, AppliedAt: at, RebootRequired: true, RebootReason: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		boot time.Time
		want bool
	}{{at.Add(-time.Hour), true}, {at.Add(time.Hour), false}, {time.Time{}, true}} {
		s, err := LoadState(p, func() time.Time { return c.boot })
		if err != nil || s.RebootRequired != c.want {
			t.Errorf("boot %v: %v %v", c.boot, s.RebootRequired, err)
		}
	}
	if st, _ := os.Stat(p.StatePath()); st.Mode().Perm() != 0o600 {
		t.Errorf("state mode %v", st.Mode().Perm())
	}
}

func TestEmbeddedMatchesSpecBlocks(t *testing.T) {
	for _, n := range []string{"logind.conf", "dconf-power", "wifi-powersave-off.conf", "90-wifi-powersave-off"} {
		if string(readEmbedded(n)) != golden(t, n) {
			t.Errorf("%s differs from its golden", n)
		}
	}
	if !strings.Contains(fmt.Sprint(LidTest), "no gaps") && !strings.Contains(strings.Join(LidTest, " "), "no gaps") {
		t.Error("lid test text lost its pass criterion")
	}
}
