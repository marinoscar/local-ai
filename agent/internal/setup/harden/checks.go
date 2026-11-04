package harden

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// logindWant are the settings the drop-in must leave in effect.
var logindWant = []string{
	"HandleLidSwitch", "HandleLidSwitchExternalPower", "HandleLidSwitchDocked", "HandlePowerKey",
	"HandleSuspendKey", "HandleHibernateKey", "IdleAction",
}

// EffectiveLogind parses `systemd-analyze cat-config systemd/logind.conf` output: the last assignment of a
// key wins, as in systemd.
func EffectiveLogind(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' || l[0] == ';' || l[0] == '[' {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

// Check ids registered with the doctor.
const (
	CheckLogind  = "ins.logind"
	CheckSleep   = "ins.sleep_targets"
	CheckGnome   = "ins.gnome_idle"
	CheckProfile = "ins.power_profile"
	CheckWifi    = "ins.wifi_powersave"
)

// Checks returns the doctor checks LAI-INS-040..044 and 070. They report nothing unless the hardening was
// applied on this node (a desktop or a reverted laptop has nothing to verify).
func (h *Hardener) Checks() []doctor.Check {
	mk := func(id string, f func(context.Context, State) []doctor.Result) doctor.Check {
		return doctor.Check{ID: id, Area: "INS", Run: func(ctx context.Context) []doctor.Result {
			st, err := LoadState(h.Paths, h.Boot)
			if err != nil || !st.Applied {
				return nil
			}
			return f(ctx, st)
		}}
	}
	return []doctor.Check{
		mk(CheckLogind, h.verifyLogind),
		mk(CheckSleep, h.verifySleep),
		mk(CheckGnome, h.verifyGnome),
		mk(CheckProfile, h.verifyProfile),
		mk(CheckWifi, h.verifyWifi),
	}
}

// RegisterChecks adds Checks to reg (doctor.DefaultRegistry when nil).
func RegisterChecks(reg *doctor.Registry, h *Hardener) error {
	if reg == nil {
		reg = doctor.DefaultRegistry
	}
	for _, c := range h.Checks() {
		if err := reg.Add(c); err != nil {
			return err
		}
	}
	return nil
}

// Verify runs every check and returns the findings (empty = everything verified). `aiagent setup harden --check`
// and the installer's repair use it; the drill's power check reuses the doctor checks.
func (h *Hardener) Verify(ctx context.Context) []doctor.Result {
	var out []doctor.Result
	for _, c := range h.Checks() {
		out = append(out, c.Run(ctx)...)
	}
	return out
}

func problem(check, code string, details map[string]any) []doctor.Result {
	return []doctor.Result{{Check: check, Code: code, Details: details}}
}

// verifyLogind: LAI-INS-040, only meaningful after a reboot, so a pending reboot is LAI-INS-070 instead.
func (h *Hardener) verifyLogind(ctx context.Context, st State) []doctor.Result {
	if st.RebootRequired {
		return problem(CheckLogind, CodeReboot, map[string]any{"reason": st.RebootReason})
	}
	out, _, err := h.run(ctx, cmdOpts{}, "systemd-analyze", "cat-config", "systemd/logind.conf")
	if err != nil {
		return nil // cannot look: say nothing rather than a false alarm
	}
	eff := EffectiveLogind(out)
	var wrong []string
	for _, k := range logindWant {
		if eff[k] != "ignore" {
			wrong = append(wrong, k)
		}
	}
	if len(wrong) > 0 {
		return problem(CheckLogind, CodeLogind, map[string]any{"not_ignored": wrong})
	}
	return nil
}

func (h *Hardener) verifySleep(ctx context.Context, _ State) []doctor.Result {
	var bad []string
	for _, t := range SleepTargets {
		out, _, err := h.run(ctx, cmdOpts{}, "systemctl", "is-enabled", t)
		if err != nil {
			return nil
		}
		if firstLine(out) != "masked" {
			bad = append(bad, t)
		}
	}
	if len(bad) > 0 {
		return problem(CheckSleep, CodeMasked, map[string]any{"not_masked": bad})
	}
	return nil
}

func (h *Hardener) verifyGnome(ctx context.Context, st State) []doctor.Result {
	if !h.have("gsettings") {
		return nil
	}
	if st.DesktopUser != "" && h.User != nil {
		if u, err := h.User(st.DesktopUser); err == nil && fileExists(h.Paths.abs(fmt.Sprintf("/run/user/%d/bus", u.UID))) {
			var bad []string
			for _, key := range []string{gsKeyAC, gsKeyBattery} {
				if out, rc, err := h.run(ctx, gsOpts(u), "gsettings", "get", gsSchema, key); err == nil && (rc != 0 || out != "'nothing'") {
					bad = append(bad, key)
				}
			}
			if len(bad) > 0 {
				return problem(CheckGnome, CodeGnome, map[string]any{"keys": bad})
			}
			return nil
		}
	}
	// No session bus: the system default must be in place.
	b, err := os.ReadFile(h.Paths.abs(DconfPower))
	if err != nil || string(b) != string(readEmbedded("dconf-power")) {
		return problem(CheckGnome, CodeGnome, map[string]any{"reason": "system default missing"})
	}
	return nil
}

func (h *Hardener) verifyProfile(ctx context.Context, _ State) []doctor.Result {
	if !h.have("powerprofilesctl") {
		return nil
	}
	out, _, err := h.run(ctx, cmdOpts{}, "powerprofilesctl", "get")
	if err != nil {
		return nil
	}
	if out != "performance" {
		return problem(CheckProfile, CodeProfile, map[string]any{"profile": out})
	}
	if b, err := os.ReadFile(h.Paths.abs(eppPath)); err == nil {
		if v := strings.TrimSpace(string(b)); v != "performance" {
			return problem(CheckProfile, CodeProfile, map[string]any{"energy_performance_preference": v})
		}
	}
	return nil
}

func (h *Hardener) verifyWifi(ctx context.Context, _ State) []doctor.Result {
	if !h.have("iw") {
		return nil
	}
	out, _, err := h.run(ctx, cmdOpts{}, "iw", "dev")
	if err != nil {
		return nil
	}
	var bad []string
	for _, ifc := range WifiInterfaces(out) {
		got, _, err := h.run(ctx, cmdOpts{}, "iw", "dev", ifc, "get", "power_save")
		if err == nil && !strings.Contains(got, "Power save: off") {
			bad = append(bad, ifc)
		}
	}
	if len(bad) > 0 {
		return problem(CheckWifi, CodeWifi, map[string]any{"interfaces": bad})
	}
	return nil
}
