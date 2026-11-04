package harden

import (
	"context"
	"os"
	"path/filepath"
	"sort"
)

// Revert undoes the hardening for `install-node.sh --uninstall --revert-hardening` (installer.md section 4.4
// step 9): restore or remove the logind drop-in, unmask the four sleep targets, remove the dconf file and run
// `dconf update`, remove the NetworkManager conf and the dispatcher script. SSH, packages, users, groups, the
// power profile and the per-user gsettings are left. logind is not restarted: reboot_required is set instead.
func (h *Hardener) Revert(ctx context.Context) (Report, error) {
	h.report = Report{}
	if h.Opts.DryRun {
		h.logf("[dry-run] reverting the laptop hardening: nothing will be changed")
	}
	logindChanged := false

	// 1. logind drop-in: the oldest backup is what was there before Local AI; none means we created it.
	st := Step{ID: "logind", Name: "restore or remove the logind drop-in"}
	if orig, ok := h.oldestBackup(LogindDropin); ok {
		cur, _ := os.ReadFile(h.Paths.abs(LogindDropin))
		data, err := os.ReadFile(orig)
		switch {
		case err != nil:
			st.Status, st.Detail = StatusFailed, err.Error()
		case string(cur) == string(data):
			st.Status = StatusAlready
		case h.Opts.DryRun:
			h.logf("[dry-run] would restore %s from %s", LogindDropin, orig)
			st.Status = StatusDone
		default:
			if err := writeAtomic(h.Paths.abs(LogindDropin), data, 0o644); err != nil {
				st.Status, st.Detail = StatusFailed, err.Error()
			} else {
				h.logf("restored %s from %s", LogindDropin, orig)
				st.Status = StatusDone
			}
		}
	} else {
		changed, err := h.removeFile(LogindDropin)
		st.Status = statusOf(changed, err)
		if err != nil {
			st.Detail = err.Error()
		}
	}
	if st.Status == StatusDone {
		logindChanged = true
	}
	h.finish(&st)

	// 2. unmask.
	st = Step{ID: "mask", Name: "unmask the sleep targets"}
	masked := false
	for _, t := range SleepTargets {
		out, _, err := h.run(ctx, cmdOpts{}, "systemctl", "is-enabled", t)
		if err != nil {
			st.Status, st.Detail = StatusFailed, err.Error()
			break
		}
		if firstLine(out) == "masked" {
			masked = true
		}
	}
	if st.Status == "" {
		if !masked {
			st.Status = StatusAlready
		} else if err := h.change(ctx, cmdOpts{}, "systemctl", append([]string{"unmask"}, SleepTargets...)...); err != nil {
			st.Status, st.Detail = StatusFailed, err.Error()
		} else {
			st.Status = StatusDone
		}
	}
	h.finish(&st)

	// 3. dconf.
	st = Step{ID: "gnome", Name: "remove the dconf power default"}
	changed, err := h.removeFile(DconfPower)
	st.Status = statusOf(changed, err)
	if err != nil {
		st.Detail = err.Error()
	} else if changed && h.have("dconf") {
		if err := h.change(ctx, cmdOpts{}, "dconf", "update"); err != nil {
			st.Status, st.Detail = StatusFailed, err.Error()
		}
	}
	h.finish(&st)

	// 4. Wi-Fi.
	st = Step{ID: "wifi", Name: "remove the Wi-Fi power-save files"}
	c1, err1 := h.removeFile(NMConf)
	c2, err2 := h.removeFile(NMDispatcher)
	switch {
	case err1 != nil:
		st.Status, st.Detail = StatusFailed, err1.Error()
	case err2 != nil:
		st.Status, st.Detail = StatusFailed, err2.Error()
	default:
		st.Status = statusOf(c1 || c2, nil)
		if c1 && h.have("nmcli") {
			if err := h.change(ctx, cmdOpts{}, "nmcli", "general", "reload", "conf"); err != nil {
				st.Status, st.Detail = StatusFailed, err.Error()
			}
		}
	}
	h.finish(&st)

	prev, _ := LoadState(h.Paths, h.Boot)
	if logindChanged {
		h.report.RebootRequired, h.report.RebootReason = true, "the logind settings were removed; the old behaviour returns after a reboot"
	} else if prev.RebootRequired {
		h.report.RebootRequired, h.report.RebootReason = true, prev.RebootReason
	}
	if !h.Opts.DryRun {
		s := State{Schema: 1, Applied: false, AppliedAt: h.now().UTC(), RebootRequired: h.report.RebootRequired,
			RebootReason: h.report.RebootReason, DesktopUser: prev.DesktopUser, Failed: len(h.report.Failed()) > 0}
		if err := SaveState(h.Paths, s); err != nil {
			return h.report, err
		}
	}
	return h.report, nil
}

func (h *Hardener) finish(st *Step) {
	h.logf("revert: %s: %s%s", st.Name, st.Status, detailSuffix(*st))
	h.report.Steps = append(h.report.Steps, *st)
}

func statusOf(changed bool, err error) string {
	switch {
	case err != nil:
		return StatusFailed
	case changed:
		return StatusDone
	}
	return StatusAlready
}

// oldestBackup returns the copy of path in the oldest backup folder that has one.
func (h *Hardener) oldestBackup(path string) (string, bool) {
	root := filepath.Join(h.Paths.stateDir(), "backup")
	ents, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // UTC timestamps sort in time order
	for _, n := range names {
		p := filepath.Join(root, n, path)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}
