package app

import (
	"context"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// This file wires the AI-ARK drive detector (ark-sync.md section 4.1.2, SYNC-02): the real mount table,
// blkid and sysfs behind the interfaces of internal/ark, the active roots into ArkRoots (which is
// updated before drive.mounted or drive.unmounted is published, so the handler of those events sees the
// new list) and the readiness summary into GET /ark/drives.

// buildDrives builds the detector of the stack. It never fails: a node without blkid gets a detector
// whose every blkid call says so, which is logged once per pass and shows no drive.
func (s *arkStack) buildDrives() {
	d := s.d
	env := d.env

	var block ark.BlockProber = ark.UnavailableProber{Err: ark.ErrBlkidUnavailable}
	if path, err := env.Lookup("blkid"); err == nil {
		block = &ark.BlkidProber{Runner: env.Runner, Path: path}
	} else {
		d.log.Warn("blkid is not installed: AI-ARK drives cannot be detected", "error", err.Error())
	}

	opts := ark.Options{
		Mounts: ark.ProcMounts{}, Block: block, FS: ark.OSFS{}, Errors: ark.SysErrors{FS: ark.OSFS{}},
		Clock: env.Clock, Events: d.c.Bus, Alerts: d.c.Doctor, Logger: d.log,
		Drive: ark.DriveOptions{
			DefaultProfile: d.syncSettings().ArkDrive.DefaultProfile,
			NodeID:         d.c.Config.Get().Node.ID, AgentVersion: env.Version, Logger: d.log.With("component", "ark"),
		},
		OnActive: s.roots.SetDrives,
	}
	if env.ArkDrives != nil {
		env.ArkDrives(&opts)
	}
	m, err := ark.NewManager(opts)
	if err != nil {
		d.log.Error("the AI-ARK drive detector could not be built; drives are not detected", "error", err.Error())
		return
	}
	s.drives = m
	// An unprivileged run is a test or a development machine: it must not read the real mount table
	// and run blkid unless a test said where the fakes are.
	s.watchDrives = !env.Unprivileged || env.ArkDrives != nil
}

// startDrives starts the detector as a worker. It runs after the worker of the drive events has
// subscribed, so the drive.mounted of a drive that is already plugged in is not missed.
func (s *arkStack) startDrives() {
	if s == nil || s.drives == nil || !s.watchDrives {
		return
	}
	s.d.startWorker(s.drives)
}

// driveReady gives GET /ark/drives the readiness summary of a root.
func (s *arkStack) driveReady(rootID string) (ark.ReadySummary, bool) {
	if s.ready == nil {
		return ark.ReadySummary{}, false
	}
	r, ok := s.ready.Report(rootID)
	if !ok {
		return ark.ReadySummary{}, false
	}
	return ark.ReadySummary{Overall: string(r.Overall), GeneratedAt: r.GeneratedAt}, true
}

// drivesView is the view of GET /ark/drives: the detector's drives with the readiness summaries, or an
// empty list for a daemon without a detector.
func (s *arkStack) drivesView() ark.DrivesView {
	return ark.DrivesView{Detector: s.drives, Ready: s.driveReady}
}

// noDrives is the view of a daemon whose sync stack did not come up and so has no detector: the route
// answers like GET /ark does, 503, rather than an empty list that would read as "no drive plugged in".
type noDrives struct{ arkUnavailable }

// Drives implements api.ArkDrivesView.
func (u noDrives) Drives(context.Context, string) ([]any, error) { return nil, u.err() }
