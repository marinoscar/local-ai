package ark

import (
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// DriveState is the state of a drive in GET /ark/drives (ark-sync.md section 3.5). It is the same
// vocabulary as RootState: a drive that has a root carries that state, the others only exist here
// (not_mounted, ejecting).
type DriveState = RootState

// Drive is one AI-ARK drive as the detector sees it: mounted and classified, or plugged in and not
// mounted. A Drive is a value; the detector hands out copies.
type Drive struct {
	// RootID is "ark-<8 hex of the filesystem UUID>", or "" when there is no usable UUID (a
	// filesystem that is not ext4 is never given an id). Two drives that are clones of each other
	// have the same RootID; the second is unusable with LAI-ARK-006 and is not an active root.
	RootID string
	// Device is the block device node ("/dev/sdb1").
	Device string
	// Mount is the mount point, "" for a drive that is not mounted.
	Mount string
	// FSType is the filesystem type: blkid's TYPE, or the mounted type when blkid gave none.
	FSType string
	// Label is the filesystem label (always "AI-ARK" for a drive the detector reports).
	Label string
	// UUID is the filesystem UUID as blkid printed it. It is never sent over the API: only the root
	// id derived from it is.
	UUID string
	// DevNum is "major:minor" of a mounted drive, "" otherwise.
	DevNum string
	// MountID is the mountinfo mount id, the first key of the best-copy order (0 when not mounted).
	MountID int
	// SizeBytes and FreeBytes come from statfs on the mount (0 when not mounted or unknown).
	SizeBytes, FreeBytes int64
	// State is the drive state.
	State DriveState
	// ReadOnly is true when nothing may be written to the drive: a read-only mount, an unclean
	// filesystem, or a failed write probe.
	ReadOnly bool
	// FSErrors is ext4's errors_count.
	FSErrors int
	// Initialized is true when <mount>/localai/ROOT.json exists (or was recreated).
	Initialized bool
	// Profile is the profile of the drive's ROOT.json ("" when there is no root).
	Profile string
	// Problems are the doctor findings about this drive, in the order the checks found them.
	Problems []Problem
	// Detail is a short plain explanation when the state has no doctor code of its own (a damaged
	// ROOT.json). It is not translated and not for parsing.
	Detail string
}

// Active reports whether the drive is part of the active set: it has a root the agent may read.
func (d Drive) Active() bool { return d.activeState() && d.Initialized }

func (d Drive) activeState() bool {
	switch d.State {
	case StateReady, StateReadOnly, StateUnclean:
		return true
	}
	return false
}

// HasProblem reports whether the drive carries a finding with the given code.
func (d Drive) HasProblem(code string) bool { return hasCode(d.Problems, code) }

func (d Drive) clone() Drive {
	d.Problems = append([]Problem(nil), d.Problems...)
	return d
}

// IsArkLabel reports whether label marks an ark drive: exactly "AI-ARK", case-sensitive
// (ark-sync.md section 4.1.2 step 4).
func IsArkLabel(label string) bool { return label == ArkLabel }

// DriveRootID derives the root id of a drive from what blkid read: only an ext4 filesystem with a
// usable UUID gets one ("" otherwise, so GET /ark/drives shows null).
func DriveRootID(b BlockInfo) string {
	if b.Type != "ext4" {
		return ""
	}
	id, err := RootIDFromUUID(b.UUID)
	if err != nil {
		return ""
	}
	return id
}

// ClassifyInput is everything the first four checks of ark-sync.md section 4.1.2 need. All of it
// was read already; Classify touches nothing.
type ClassifyInput struct {
	// Entry is the drive's mount (for fstype and the read-only options).
	Entry MountEntry
	// Block is what blkid read for its device.
	Block BlockInfo
	// FSErrors is ext4's errors_count (0 when unknown).
	FSErrors int
	// DuplicateUUID: another mounted ark drive, mounted earlier, has this drive's UUID.
	DuplicateUUID bool
}

// Verdict is the outcome of Classify.
type Verdict struct {
	// State is ready, read_only, unclean or unusable. Ready only says that these checks passed: the
	// initialized and writable checks (which read the drive) come next.
	State DriveState
	// Never is true when the drive must not be read or written at all (not ext4, duplicate UUID).
	Never bool
	// ReadOnly: the mount is read-only, or the filesystem has errors (treated as read-only).
	ReadOnly bool
	// Unclean: errors_count is above zero, whatever the state says (a drive the kernel turned
	// read-only after an error is read_only with Unclean set, and both codes are raised).
	Unclean bool
	// Problems are the findings, in check order.
	Problems []Problem
}

// Classify runs the first four checks of ark-sync.md section 4.1.2, in order; the first failing
// check sets the state:
//
//  1. fstype: blkid TYPE and the mountinfo fstype must both be ext4, else unusable, LAI-ARK-002;
//  2. duplicate UUID: unusable, LAI-ARK-006;
//  3. read-only: "ro" in the mount options or the super options, read_only, LAI-ARK-003;
//  4. errors: errors_count above zero, unclean, LAI-ARK-004.
//
// A read-only mount that also has errors keeps the state read_only (the first failure) but raises
// LAI-ARK-004 too and sets Unclean, so the drive is still re-verified before it is served.
func Classify(in ClassifyInput) Verdict {
	base := map[string]any{"device": in.Block.Device, "mount": in.Entry.MountPoint}
	with := func(kv ...any) map[string]any {
		m := make(map[string]any, len(base)+len(kv)/2)
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	if id := DriveRootID(in.Block); id != "" {
		base["root_id"] = id
	}

	if in.Block.Type != "ext4" || in.Entry.FSType != "ext4" {
		return Verdict{State: StateUnusable, Never: true, Problems: []Problem{
			newProblem(doctor.ARK002, with("fs_type", in.Block.Type, "mounted_as", in.Entry.FSType)),
		}}
	}
	if in.DuplicateUUID {
		return Verdict{State: StateUnusable, Never: true, Problems: []Problem{newProblem(doctor.ARK006, with())}}
	}

	v := Verdict{State: StateReady}
	if in.Entry.ReadOnly() {
		v.State, v.ReadOnly = StateReadOnly, true
		v.Problems = append(v.Problems, newProblem(doctor.ARK003, with("mount_options", in.Entry.MountOptions, "super_options", in.Entry.SuperOptions)))
	}
	if in.FSErrors > 0 {
		v.Unclean, v.ReadOnly = true, true
		if v.State == StateReady {
			v.State = StateUnclean
		}
		v.Problems = append(v.Problems, newProblem(doctor.ARK004, with("errors_count", in.FSErrors)))
	}
	return v
}

// ClassifyUnmounted classifies a plugged-in AI-ARK device that is not mounted. An ext4 one is
// not_mounted with LAI-ARK-005 (the app offers Mount); any other filesystem is unusable with
// LAI-ARK-002, because mounting it would only expose it to a read or a write the spec forbids.
func ClassifyUnmounted(b BlockInfo) Verdict {
	details := map[string]any{"device": b.Device}
	if id := DriveRootID(b); id != "" {
		details["root_id"] = id
	}
	if b.Type != "ext4" {
		details["fs_type"] = b.Type
		return Verdict{State: StateUnusable, Never: true, Problems: []Problem{newProblem(doctor.ARK002, details)}}
	}
	return Verdict{State: StateNotMounted, Problems: []Problem{newProblem(doctor.ARK005, details)}}
}
