package ark

import (
	"context"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// This file turns the detector's drives into the body of GET /ark/drives (ark-sync.md section 3.5).
// internal/api owns the route and asks the DrivesView below through api.ArkDrivesView, so that
// package imports nothing of the drive layer (the same seam as GET /ark and internal/ark/ready).

// ReadySummary is the "ready" member of a drive: the overall READY colour of its root and when the
// report was generated.
type ReadySummary struct {
	Overall     string `json:"overall"`
	GeneratedAt string `json:"generated_at"`
}

// ReadyLookup returns the READY summary of a root, or false when there is no report yet (the root
// is not mounted, or READY has not been computed for it). internal/app binds it to ready.Service.
type ReadyLookup func(rootID string) (ReadySummary, bool)

// ProblemView is one entry of a drive's "problems": the doctor code, how serious it is, and its title
// and fix in the language of the request. The fix mentions the drive's device when the code table's
// fix does ("sudo fsck.ext4 -f /dev/sdb1").
type ProblemView struct {
	Code     string          `json:"code"`
	Severity doctor.Severity `json:"severity"`
	Title    string          `json:"title"`
	Fix      string          `json:"fix"`
	Details  map[string]any  `json:"details,omitempty"`
}

// DriveView is a drive as GET /ark/drives shows it.
type DriveView struct {
	// RootID is null when the drive has no usable filesystem UUID.
	RootID *string `json:"root_id"`
	Device string  `json:"device"`
	// Mount is null for a drive that is not mounted.
	Mount       *string       `json:"mount"`
	FSType      string        `json:"fstype,omitempty"`
	Label       *string       `json:"label"`
	SizeBytes   *int64        `json:"size_bytes,omitempty"`
	FreeBytes   *int64        `json:"free_bytes,omitempty"`
	State       string        `json:"state"`
	ReadOnly    bool          `json:"read_only"`
	FSErrors    int           `json:"fs_errors"`
	Initialized bool          `json:"initialized"`
	Profile     *string       `json:"profile"`
	Ready       *ReadySummary `json:"ready"`
	Problems    []ProblemView `json:"problems"`
	// Detail is a plain-language note for a state that has no code of its own (a damaged ROOT.json).
	Detail string `json:"detail,omitempty"`
}

// DrivesView implements api.ArkDrivesView over a Detector.
type DrivesView struct {
	Detector Detector
	// Ready fills the "ready" member; nil leaves it null for every drive.
	Ready ReadyLookup
}

var _ api.ArkDrivesView = DrivesView{}

// Drives implements api.ArkDrivesView: one DriveView per drive, in the detector's order.
func (v DrivesView) Drives(_ context.Context, lang string) ([]any, error) {
	drives := v.Detector.Drives()
	out := make([]any, 0, len(drives))
	for _, d := range drives {
		out = append(out, v.view(d, lang))
	}
	return out, nil
}

// View renders one drive for lang. It is what GET /ark/drives returns for it, and what the init and
// mount routes return as "the drive object".
func (v DrivesView) View(d Drive, lang string) DriveView { return v.view(d, lang) }

func (v DrivesView) view(d Drive, lang string) DriveView {
	out := DriveView{
		Device: d.Device, FSType: d.FSType, State: string(d.State), ReadOnly: d.ReadOnly, FSErrors: d.FSErrors,
		Initialized: d.Initialized, Problems: make([]ProblemView, 0, len(d.Problems)), Detail: d.Detail,
	}
	if d.RootID != "" {
		out.RootID = &d.RootID
	}
	if d.Mount != "" {
		out.Mount = &d.Mount
	}
	if d.Label != "" {
		out.Label = &d.Label
	}
	if d.Mount != "" && d.SizeBytes > 0 {
		size, free := d.SizeBytes, d.FreeBytes
		out.SizeBytes, out.FreeBytes = &size, &free
	}
	if d.Profile != "" {
		out.Profile = &d.Profile
	}
	if v.Ready != nil && d.RootID != "" && d.Active() {
		if r, ok := v.Ready(d.RootID); ok {
			out.Ready = &r
		}
	}
	for _, p := range d.Problems {
		pv := ProblemView{Code: p.Code, Severity: p.Severity, Details: p.Details}
		if title, fix, ok := doctor.Text(p.Code, lang); ok {
			pv.Title, pv.Fix = title, fixFor(fix, d.Device)
		} else {
			pv.Title = p.Code
		}
		out.Problems = append(out.Problems, pv)
	}
	return out
}

// fixFor puts the drive's device into a fix that names "/dev/<device>" (or "/dev/<dispositivo>").
func fixFor(fix, device string) string {
	if device == "" {
		return fix
	}
	return strings.NewReplacer("/dev/<device>", device, "/dev/<dispositivo>", device).Replace(fix)
}
