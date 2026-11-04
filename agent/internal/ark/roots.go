// Package ark is the content-root layer of ark sync (docs/specs/ark-sync.md section 2, 3.1, 3.2
// and 4.1): what a content root is, how its stable id is derived, the ROOT.json identity file, the
// directory layout every root has, and the self-healing checks of the internal root.
//
// Every other ark-sync part (the manifest and journal, the planner, the downloader, READY) and the
// router address content through the root ids defined here. The package never touches the network,
// systemd or a mount table: every filesystem call goes through the small FS interface (fs.go), so
// tests run on a temp directory with injected faults, and detecting drives from mountinfo is the
// next layer's job (it builds a DriveInfo and calls OpenDriveRoot).
//
// What it never does: delete anything. A regular file where the internal root should be is renamed
// aside, a corrupt ROOT.json is renamed aside, and no function here creates a root on a drive (only
// the explicit init action may, through CreateLayout and WriteRootJSON).
package ark

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// RootKind is the kind of a content root (the kind field of ROOT.json).
type RootKind string

// The kinds.
const (
	// KindInternal is the node's own disk, /srv/localai.
	KindInternal RootKind = "internal"
	// KindArkDrive is a mounted AI-ARK drive, <mount>/localai.
	KindArkDrive RootKind = "ark_drive"
)

// Valid reports whether k is one of the two kinds.
func (k RootKind) Valid() bool { return k == KindInternal || k == KindArkDrive }

// RootState is the state of a root. The values are the drive states of the GET /ark/drives shape
// (ark-sync.md section 3.5), so the drive layer maps one to one; the internal root only ever is
// ready, read_only or unusable.
type RootState string

// The states.
const (
	StateReady         RootState = "ready"
	StateReadOnly      RootState = "read_only"
	StateUnclean       RootState = "unclean"
	StateNotMounted    RootState = "not_mounted"
	StateUnusable      RootState = "unusable"
	StateEjecting      RootState = "ejecting"
	StateUninitialized RootState = "uninitialized"
)

// Fixed names (ark-sync.md section 2 and overview.md section 3.2).
const (
	// InternalID is the root id of the internal root.
	InternalID = "internal"
	// DefaultInternalPath is where the internal root lives.
	DefaultInternalPath = "/srv/localai"
	// DriveRootDir is the directory under a drive's mount point that is its content root.
	DriveRootDir = "localai"
	// DefaultDriveProfile is the profile of a drive whose ROOT.json says nothing
	// (node.yaml sync.ark_drive.default_profile has the same default).
	DefaultDriveProfile = "ark-drive-full"

	drivePrefix = "ark-"
	idHexLen    = 8
)

// The doctor codes this package raises. They alias the doctor table (the only place a code string
// is written); the title and the fix in en and es come from doctor.Text.
const (
	// CodeNewDrive: a drive with no ROOT.json needs setting up (info).
	CodeNewDrive = doctor.ARK010
	// CodeInternalRecreated: the internal root was missing and was recreated empty (warning).
	CodeInternalRecreated = doctor.ARK020
	// CodeNetworkFS: the root is on a network filesystem and is used read-only (warning).
	CodeNetworkFS = doctor.ARK021

	// The drive layer's codes (detect.go, classify.go).
	// CodeNotExt4: a drive labelled AI-ARK is not ext4; it is never read or written (error).
	CodeNotExt4 = doctor.ARK002
	// CodeDriveReadOnly: the drive is mounted read-only, or a write probe failed (warning).
	CodeDriveReadOnly = doctor.ARK003
	// CodeDriveUnclean: the drive's filesystem has recorded errors (error).
	CodeDriveUnclean = doctor.ARK004
	// CodeNotMounted: an AI-ARK drive is plugged in but not mounted (warning).
	CodeNotMounted = doctor.ARK005
	// CodeDuplicateUUID: two mounted drives have the same filesystem UUID (error).
	CodeDuplicateUUID = doctor.ARK006
	// CodeDriveUnplugged: a drive was pulled without ejecting (warning, cleared after 24 h or when
	// the drive comes back clean).
	CodeDriveUnplugged = doctor.ARK007
	// CodeEjectBlocked: the drive cannot be ejected because something still uses it (warning). Raised
	// as an alert when an accepted eject failed to unmount; the eject route answers it with the blockers.
	CodeEjectBlocked = doctor.ARK009
)

// Root is one content root: the internal disk or an ark drive's localai directory.
type Root struct {
	// ID is "internal" or "ark-<8 hex>".
	ID string
	// Kind is KindInternal or KindArkDrive.
	Kind RootKind
	// Path is "/srv/localai" or "<mount>/localai".
	Path string
	// Mount is the drive's mount point; empty for the internal root.
	Mount string
	// Profile is the profile the root is planned with. For the internal root it is node.yaml's
	// profile (ROOT.json's field is informational); for a drive it is ROOT.json's.
	Profile string
	// ReadOnly: nothing may be written under Path (network filesystem, read-only mount, failed
	// layout). Reads and serving still work.
	ReadOnly bool
	// MountID is the mountinfo mount id, the first key of the best-copy order (0 for internal).
	MountID int
	// State is the root's state; see RootState.
	State RootState
	// Unclean: the drive's filesystem has recorded errors (ext4 errors_count above zero), whatever
	// State says. See NeedsServeVerify.
	Unclean bool
}

// NeedsServeVerify reports whether the files of this root must be re-hashed before they are served
// (ark-sync.md section 4.1.3 step 4): the drive's filesystem has errors. The hashing itself is the
// verifier's job (section 4.13); the drive layer only raises the flag.
func (r Root) NeedsServeVerify() bool { return r.Unclean || r.State == StateUnclean }

// Problem is one doctor finding about a root. Severity is the code's default from the doctor table.
type Problem struct {
	Code     string
	Severity doctor.Severity
	// Details carries the facts a UI or log shows next to the code (root_id, path, fs_type, ...).
	Details map[string]any
}

func newProblem(code string, details map[string]any) Problem {
	p := Problem{Code: code, Severity: doctor.Warning, Details: details}
	if c, ok := doctor.Lookup(code); ok {
		p.Severity = c.Severity
	}
	return p
}

// ErrBadUUID is returned by RootIDFromUUID for an input that cannot give a stable id.
var ErrBadUUID = errors.New("ark: not a usable filesystem UUID")

// RootIDFromUUID derives the root id of an ark drive from its filesystem UUID: lowercase, dashes
// removed, first 8 hex digits, prefixed "ark-" (ark-sync.md section 2). blkid prints the 36-char
// dashed form for ext4, but any 8 to 32 hex digits are accepted (dashes anywhere). Anything else
// (empty, shorter than 8 digits, a non-hex character) and the all-zero UUID are errors: a malformed
// or colliding id is never produced.
func RootIDFromUUID(fsUUID string) (string, error) {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fsUUID), "-", ""))
	if len(s) < idHexLen || len(s) > 32 {
		return "", fmt.Errorf("%w: %q has %d hex digits, want 8 to 32", ErrBadUUID, fsUUID, len(s))
	}
	zero := true
	for _, c := range s {
		if !isLowerHex(c) {
			return "", fmt.Errorf("%w: %q has a non-hex character", ErrBadUUID, fsUUID)
		}
		if c != '0' {
			zero = false
		}
	}
	if zero {
		return "", fmt.Errorf("%w: %q is the null UUID", ErrBadUUID, fsUUID)
	}
	return drivePrefix + s[:idHexLen], nil
}

func isLowerHex(c rune) bool { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') }

// ValidRootID reports whether id is "internal" or "ark-" plus exactly 8 lowercase hex digits.
func ValidRootID(id string) bool {
	if id == InternalID {
		return true
	}
	hex, ok := strings.CutPrefix(id, drivePrefix)
	if !ok || len(hex) != idHexLen {
		return false
	}
	for _, c := range hex {
		if !isLowerHex(c) {
			return false
		}
	}
	return true
}

// DisplayName is the name a UI shows for a root id: "Internal disk" or "Ark drive (3f9c0a12)" in
// English, "Disco interno" or "Disco del arca (3f9c0a12)" in Spanish (any other lang gets English).
func DisplayName(rootID, lang string) string {
	es := lang == doctor.LangES
	if rootID == InternalID {
		if es {
			return "Disco interno"
		}
		return "Internal disk"
	}
	short := strings.TrimPrefix(rootID, drivePrefix)
	if es {
		return "Disco del arca (" + short + ")"
	}
	return "Ark drive (" + short + ")"
}

// DisplayName is DisplayName(r.ID, lang).
func (r Root) DisplayName(lang string) string { return DisplayName(r.ID, lang) }

// Join returns the path of elems under the root. Elements that climb out of the root are refused.
func (r Root) Join(elems ...string) (string, error) {
	p := filepath.Join(append([]string{r.Path}, elems...)...)
	rel, err := filepath.Rel(r.Path, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("ark: %q is outside root %s", filepath.Join(elems...), r.ID)
	}
	return p, nil
}

// DriveInfo is what the drive layer knows about a mounted AI-ARK drive: the facts OpenDriveRoot
// and NewDriveRoot need, with no blkid or mountinfo parsing here.
type DriveInfo struct {
	// Mount is the mount point.
	Mount string
	// FSUUID is the filesystem UUID (blkid UUID=), dashed or not.
	FSUUID string
	// MountID is the mountinfo mount id.
	MountID int
	// Label is the filesystem label, recorded in a recreated ROOT.json.
	Label string
	// ReadOnly: the drive is read-only (mount options, super options, ext4 errors, failed probe).
	// Nothing is written to it, not even a recreated ROOT.json.
	ReadOnly bool
}

// NewDriveRoot builds the Root of a drive: id from the UUID, path <mount>/localai. It touches no
// filesystem; the State is StateReady, or StateReadOnly when d.ReadOnly. An empty profile means
// DefaultDriveProfile.
func NewDriveRoot(d DriveInfo, profile string) (Root, error) {
	id, err := RootIDFromUUID(d.FSUUID)
	if err != nil {
		return Root{}, err
	}
	if d.Mount == "" {
		return Root{}, errors.New("ark: drive has no mount point")
	}
	if profile == "" {
		profile = DefaultDriveProfile
	}
	r := Root{
		ID:       id,
		Kind:     KindArkDrive,
		Path:     filepath.Join(d.Mount, DriveRootDir),
		Mount:    d.Mount,
		Profile:  profile,
		ReadOnly: d.ReadOnly,
		MountID:  d.MountID,
		State:    StateReady,
	}
	if d.ReadOnly {
		r.State = StateReadOnly
	}
	return r, nil
}

// NewInternalRoot builds the Root of the internal disk without checking it (EnsureInternal does
// that). An empty path means DefaultInternalPath.
func NewInternalRoot(path, profile string) Root {
	if path == "" {
		path = DefaultInternalPath
	}
	return Root{ID: InternalID, Kind: KindInternal, Path: path, Profile: profile, State: StateReady}
}
