package run

import (
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// isScheduled: the triggers that run unattended, which preflight skips instead of refusing. A
// mode_online run is the catch-up the mode engine schedules after a switch to online; it is as
// unattended as the timer.
func isScheduled(trigger string) bool {
	return trigger == TriggerScheduled || trigger == TriggerModeOnline
}

// usesNetwork: the triggers whose runs may fetch. Verify and import work on what is on disk and so
// are allowed in every mode (ark-sync.md section 3.5).
func usesNetwork(trigger string) bool { return trigger != TriggerVerify && trigger != TriggerImport }

// validTrigger reports whether t is one of the triggers of ark-sync.md section 3.5.
func validTrigger(t string) bool {
	switch t {
	case TriggerManual, TriggerScheduled, TriggerCloud, TriggerModeOnline, TriggerInstall, TriggerVerify, TriggerImport:
		return true
	}
	return false
}

// verdict is what preflight decided.
type verdict struct {
	// Skip is set for a scheduled run that ends skipped: Reason and Code say why.
	Skip   bool
	Reason string
	Code   string
	// Refuse is set for a manual run that is refused: Err is the typed error.
	Err error
	// Warnings are notes a manual run goes on with.
	Warnings []string
	// Offline: the network may not be used for this run (resolvers fail fast, the plan is local).
	Offline bool
	// Roots are the ids of the roots the run works on.
	Roots []string
}

// preflight is the table of ark-sync.md section 4.3, in its order; the first failure ends the run.
//
//	check                      scheduled              manual / cloud
//	sync.enabled is false      skip disabled          proceed
//	mode is survival           skip mode (SYN-002)    refuse 409 SYN-010
//	mode is offline            skip mode (SYN-002)    refuse 409 SYN-011 unless allow_offline_mode
//	on battery                 skip battery (SYN-003) proceed with a warning
//	connectivity none          skip offline (SYN-001) proceed (resolvers fail fast)
//	no writable roots          skip no_roots          refuse LAI-ARK-001
//
// A named root that is not mounted is refused whatever the trigger. The checks that use the network
// do not apply to verify and import runs.
func preflight(req StartRequest, s Settings, net NetState, onBattery bool, mounted []ark.Root) verdict {
	scheduled := isScheduled(req.Trigger)
	var v verdict
	skip := func(reason, code string) verdict { return verdict{Skip: true, Reason: reason, Code: code} }

	if !s.Enabled && scheduled {
		return skip(SkipDisabled, "")
	}
	if usesNetwork(req.Trigger) {
		switch net.Mode {
		case plan.ModeSurvival:
			if scheduled {
				return skip(SkipMode, doctor.SYN002)
			}
			return verdict{Err: &ModeError{Code: doctor.SYN010, Mode: net.Mode}}
		case plan.ModeOffline:
			if scheduled {
				return skip(SkipMode, doctor.SYN002)
			}
			if !req.AllowOfflineMode {
				return verdict{Err: &ModeError{Code: doctor.SYN011, Mode: net.Mode}}
			}
			// allow_offline_mode: a person asked for the network in offline mode, so it is used.
		}
		if onBattery && !s.AllowOnBattery {
			if scheduled {
				return skip(SkipBattery, doctor.SYN003)
			}
			v.Warnings = append(v.Warnings, doctor.SYN003+": the node is on battery")
		}
		if !net.Online {
			if scheduled {
				return skip(SkipOffline, doctor.SYN001)
			}
			v.Offline = true
		}
	} else {
		v.Offline = true
	}

	roots, err := selectRoots(req.Roots, s, mounted)
	if err != nil {
		return verdict{Err: err}
	}
	if len(roots) == 0 {
		if scheduled {
			return skip(SkipNoRoots, doctor.ARK001)
		}
		return verdict{Err: NoRootsError{}}
	}
	v.Roots = roots
	return v
}

// writable says whether the engine may write to a root: ready (not read-only, unclean, ejecting or
// unmounted) and, for a drive, sync.ark_drive.enabled.
func writable(r ark.Root, s Settings) bool {
	if r.ReadOnly || (r.State != ark.StateReady && r.State != "") {
		return false
	}
	if r.Kind == ark.KindArkDrive && !s.ArkDrive.Enabled {
		return false
	}
	return true
}

// selectRoots returns the ids of the roots a run works on: every writable mounted root when none is
// named, else the named ones (ids, root paths or mount points) that are writable. A name that is no
// mounted root is an *UnknownRootError; a named root that is mounted but not writable is left out.
func selectRoots(names []string, s Settings, mounted []ark.Root) ([]string, error) {
	var out []string
	if len(names) == 0 {
		for _, r := range mounted {
			if writable(r, s) {
				out = append(out, r.ID)
			}
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, name := range names {
		var found *ark.Root
		for i := range mounted {
			r := &mounted[i]
			if r.ID == name || (r.Path != "" && filepath.Clean(r.Path) == filepath.Clean(name)) || (r.Mount != "" && filepath.Clean(r.Mount) == filepath.Clean(name)) {
				found = r
				break
			}
		}
		if found == nil {
			return nil, &UnknownRootError{Root: name}
		}
		if seen[found.ID] || !writable(*found, s) {
			continue
		}
		seen[found.ID] = true
		out = append(out, found.ID)
	}
	return out, nil
}
