package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/setup/harden"
)

// pwrHardening is a scripted Hardening.
type pwrHardening struct {
	laptop   bool
	state    harden.State
	stateErr error
	found    []string // codes the doctor checks report
	verified int
}

func (p *pwrHardening) Laptop() bool                 { return p.laptop }
func (p *pwrHardening) State() (harden.State, error) { return p.state, p.stateErr }
func (p *pwrHardening) Verify(context.Context) []doctor.Result {
	p.verified++
	var out []doctor.Result
	for _, c := range p.found {
		out = append(out, doctor.Result{Code: c})
	}
	return out
}

type pwrSource struct {
	st drill.BatteryStatus
	ok bool
}

func (p pwrSource) Battery() (drill.BatteryStatus, bool) { return p.st, p.ok }

func applied() harden.State { return harden.State{Applied: true} }

func powerOutcome(h Hardening, src drill.PowerSource) drill.Outcome {
	return Power(PowerDeps{Hardening: h, Power: src}).Run(context.Background(), &drill.Env{})
}

func TestPowerDesktopSkips(t *testing.T) {
	h := &pwrHardening{laptop: false, state: applied()}
	out := powerOutcome(h, pwrSource{ok: false})
	if out.Result != drill.ResultSkip || out.Details["reason"] != "not_a_laptop" || h.verified != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if out := powerOutcome(nil, nil); out.Result != drill.ResultSkip {
		t.Fatalf("no hardening wired at all is a skip: %+v", out)
	}
}

func TestPowerLaptopAllInEffectPasses(t *testing.T) {
	h := &pwrHardening{laptop: true, state: applied()}
	out := powerOutcome(h, pwrSource{st: drill.BatteryStatus{Percent: 90}, ok: true})
	if out.Result != drill.ResultPass || out.Code != "" {
		t.Fatalf("outcome = %+v", out)
	}
	for _, code := range []string{doctor.INS040, doctor.INS041, doctor.INS042, doctor.INS043, doctor.INS044} {
		if out.Details[code] != "ok" {
			t.Errorf("%s = %v", code, out.Details[code])
		}
	}
	if out.Details["ac_online"] != true || out.Details["hardening"] != "applied" {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestPowerEachHardeningProblemWarnsWith080(t *testing.T) {
	for _, code := range []string{doctor.INS040, doctor.INS041, doctor.INS042, doctor.INS043, doctor.INS044} {
		t.Run(code, func(t *testing.T) {
			h := &pwrHardening{laptop: true, state: applied(), found: []string{code}}
			out := powerOutcome(h, pwrSource{ok: true})
			if out.Result != drill.ResultWarn || out.Code != doctor.DRL080 || out.Details[code] != "problem" {
				t.Fatalf("outcome = %+v", out)
			}
		})
	}
}

func TestPowerPendingRebootIsNotAFailureOf040(t *testing.T) {
	h := &pwrHardening{laptop: true, state: applied(), found: []string{doctor.INS070}}
	out := powerOutcome(h, pwrSource{ok: true})
	if out.Result != drill.ResultPass || out.Details[doctor.INS040] != "reboot_required" || out.Details[doctor.INS070] != "pending" {
		t.Fatalf("outcome = %+v: LAI-INS-070 is reported in details instead of failing LAI-INS-040", out)
	}
	// A real problem next to the pending reboot still warns.
	h.found = []string{doctor.INS070, doctor.INS044}
	if out := powerOutcome(h, pwrSource{ok: true}); out.Result != drill.ResultWarn || out.Details[doctor.INS040] != "reboot_required" || out.Details[doctor.INS044] != "problem" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestPowerOnBatteryWarns(t *testing.T) {
	h := &pwrHardening{laptop: true, state: applied()}
	out := powerOutcome(h, pwrSource{st: drill.BatteryStatus{Percent: 80, OnBattery: true}, ok: true})
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL080 || out.Details["ac_online"] != false {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestPowerWithoutAChargerReadingDoesNotJudgeIt(t *testing.T) {
	h := &pwrHardening{laptop: true, state: applied()}
	for _, src := range []drill.PowerSource{nil, pwrSource{ok: false}} {
		out := powerOutcome(h, src)
		if out.Result != drill.ResultPass || out.Details["ac_online"] != nil {
			t.Fatalf("outcome = %+v", out)
		}
	}
}

func TestPowerHardeningNeverAppliedWarns(t *testing.T) {
	h := &pwrHardening{laptop: true, state: harden.State{}}
	out := powerOutcome(h, pwrSource{ok: true})
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL080 || out.Details["hardening"] != "not_applied" || h.verified != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	h = &pwrHardening{laptop: true, stateErr: errors.New("damaged")}
	if out := powerOutcome(h, nil); out.Result != drill.ResultWarn || out.Details["hardening"] != "unreadable" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestPowerCheckIdentity(t *testing.T) {
	c := Power(PowerDeps{})
	if c.ID != "drl.power" || c.Order != 20 || c.Critical || c.Area != "power" {
		t.Fatalf("check = %+v", c)
	}
}

// ---- the real Hardener on a fake machine -------------------------------------------------------

// The drill reuses the doctor checks of the installer's hardening: a laptop whose Wi-Fi power save is
// on warns, a desktop skips.
type machine struct {
	root     string
	hardener *harden.Hardener
	run      *execfake.Runner
}

func newMachine(t *testing.T, chassis string, wifiPowerSave bool) *machine {
	t.Helper()
	root := t.TempDir()
	if chassis != "" {
		dir := filepath.Join(root, "sys/class/dmi/id")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "chassis_type"), []byte(chassis+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths := harden.Paths{Root: root, StateDir: filepath.Join(root, "state")}
	if err := harden.SaveState(paths, harden.State{Schema: 1, Applied: true, AppliedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var logind strings.Builder
	logind.WriteString("[Login]\n")
	for _, k := range []string{"HandleLidSwitch", "HandleLidSwitchExternalPower", "HandleLidSwitchDocked", "HandlePowerKey", "HandleSuspendKey", "HandleHibernateKey", "IdleAction"} {
		logind.WriteString(k + "=ignore\n")
	}
	run := execfake.New()
	ok := func(s string) execx.Result { return execx.Result{Stdout: []byte(s)} }
	run.Expect("/bin/systemd-analyze cat-config systemd/logind.conf", ok(logind.String()), nil)
	for _, target := range harden.SleepTargets {
		run.Expect("/bin/systemctl is-enabled "+target, ok("masked\n"), nil)
	}
	run.Expect("/bin/iw dev", ok("phy#0\n\tInterface wlan0\n\t\tifindex 3\n\t\ttype managed\n"), nil)
	ps := "Power save: off\n"
	if wifiPowerSave {
		ps = "Power save: on\n"
	}
	run.Expect("/bin/iw dev wlan0 get power_save", ok(ps), nil)

	h := harden.New(paths, harden.Options{}, nil)
	h.Exec = run
	h.Lookup = func(name string) (string, error) {
		switch name {
		case "systemd-analyze", "systemctl", "iw":
			return "/bin/" + name, nil
		}
		return "", execx.ErrNotFound
	}
	h.Boot = func() time.Time { return time.Now().Add(-24 * time.Hour) }
	return &machine{root: root, hardener: h, run: run}
}

func TestPowerRealHardenerLaptopWithWifiPowerSaveOnWarns(t *testing.T) {
	m := newMachine(t, "10", true)
	out := powerOutcome(NewHardening(m.hardener), pwrSource{st: drill.BatteryStatus{Percent: 95}, ok: true})
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL080 || out.Details[doctor.INS044] != "problem" || out.Details[doctor.INS040] != "ok" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestPowerRealHardenerLaptopInOrderPasses(t *testing.T) {
	m := newMachine(t, "9", false)
	out := powerOutcome(NewHardening(m.hardener), pwrSource{st: drill.BatteryStatus{Percent: 95}, ok: true})
	if out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestPowerRealHardenerDesktopChassisSkips(t *testing.T) {
	for _, chassis := range []string{"3", "7", ""} {
		m := newMachine(t, chassis, true)
		out := powerOutcome(NewHardening(m.hardener), nil)
		if out.Result != drill.ResultSkip {
			t.Fatalf("chassis %q: outcome = %+v", chassis, out)
		}
		if len(m.run.Calls()) != 0 {
			t.Fatalf("a desktop runs nothing: %v", m.run.Calls())
		}
	}
}

func TestPowerRealHardenerPendingRebootAfterAnInstall(t *testing.T) {
	m := newMachine(t, "10", false)
	st := harden.State{Schema: 1, Applied: true, AppliedAt: time.Now(), RebootRequired: true, RebootReason: harden.RebootReason}
	if err := harden.SaveState(m.hardener.Paths, st); err != nil {
		t.Fatal(err)
	}
	m.hardener.Boot = func() time.Time { return time.Now().Add(-time.Hour) } // booted before the install
	out := powerOutcome(NewHardening(m.hardener), pwrSource{ok: true})
	if out.Result != drill.ResultPass || out.Details[doctor.INS040] != "reboot_required" {
		t.Fatalf("outcome = %+v", out)
	}
}
