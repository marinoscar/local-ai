package ark_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func TestRootIDFromUUID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"upper case with dashes (the issue example)", "3F9C0A12-1234-5678-9ABC-DEF012345678", "ark-3f9c0a12", false},
		{"lower case with dashes", "3f9c0a12-1234-5678-9abc-def012345678", "ark-3f9c0a12", false},
		{"mixed case", "3f9C0a12-1234-5678-9aBc-dEf012345678", "ark-3f9c0a12", false},
		{"no dashes", "3F9C0A1212345678", "ark-3f9c0a12", false},
		{"exactly 8 hex (a FAT serial)", "3F9C-0A12", "ark-3f9c0a12", false},
		{"dash after the first 8 is dropped too", "3f9c0a12-", "ark-3f9c0a12", false},
		{"surrounding whitespace is trimmed", " 3f9c0a12-1234-5678-9abc-def012345678\n", "ark-3f9c0a12", false},
		{"leading zeros are kept", "00000a12-1234-5678-9abc-def012345678", "ark-00000a12", false},
		{"short: 7 hex", "3f9c0a1", "", true},
		{"short: 4 hex", "3f9c", "", true},
		{"empty", "", "", true},
		{"only dashes", "----", "", true},
		{"non hex in the first 8", "3f9c0g12-1234-5678-9abc-def012345678", "", true},
		{"non hex after the first 8", "3f9c0a12-1234-5678-9abc-def01234567z", "", true},
		{"too long: 33 hex", strings.Repeat("a", 33), "", true},
		{"the null UUID would collide", "00000000-0000-0000-0000-000000000000", "", true},
		{"unicode digits are not hex", "３f9c0a12", "", true},
		{"a root id is not a UUID", "ark-3f9c0a12", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ark.RootIDFromUUID(tc.in)
			if tc.wantErr {
				if err == nil || !errors.Is(err, ark.ErrBadUUID) {
					t.Fatalf("RootIDFromUUID(%q) = %q, %v; want ErrBadUUID", tc.in, got, err)
				}
				if got != "" {
					t.Errorf("a malformed id was returned with the error: %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("RootIDFromUUID(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
			if !ark.ValidRootID(got) {
				t.Errorf("derived id %q is not a valid root id", got)
			}
		})
	}
}

func TestValidRootID(t *testing.T) {
	tests := map[string]bool{
		"internal":       true,
		"ark-3f9c0a12":   true,
		"ark-00000000":   true,
		"ark-3F9C0A12":   false,
		"ark-3f9c0a1":    false,
		"ark-3f9c0a123":  false,
		"ark-3f9c0a1g":   false,
		"ark-":           false,
		"Internal":       false,
		"":               false,
		"drive-3f9c0a12": false,
	}
	for id, want := range tests {
		if got := ark.ValidRootID(id); got != want {
			t.Errorf("ValidRootID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct{ id, lang, want string }{
		{"internal", "en", "Internal disk"},
		{"internal", "es", "Disco interno"},
		{"internal", "fr", "Internal disk"},
		{"internal", "", "Internal disk"},
		{"ark-3f9c0a12", "en", "Ark drive (3f9c0a12)"},
		{"ark-3f9c0a12", "es", "Disco del arca (3f9c0a12)"},
		{"ark-3f9c0a12", "de", "Ark drive (3f9c0a12)"},
	}
	for _, tc := range tests {
		if got := ark.DisplayName(tc.id, tc.lang); got != tc.want {
			t.Errorf("DisplayName(%q, %q) = %q, want %q", tc.id, tc.lang, got, tc.want)
		}
		if got := (ark.Root{ID: tc.id}).DisplayName(tc.lang); got != tc.want {
			t.Errorf("Root.DisplayName(%q, %q) = %q, want %q", tc.id, tc.lang, got, tc.want)
		}
	}
}

func TestNewDriveRoot(t *testing.T) {
	const uuid = "3F9C0A12-1234-5678-9ABC-DEF012345678"
	tests := []struct {
		name      string
		d         ark.DriveInfo
		profile   string
		want      ark.Root
		wantErr   bool
		wantErrIs error
	}{
		{
			name:    "ready drive with the default profile",
			d:       ark.DriveInfo{Mount: "/media/op/AI-ARK", FSUUID: uuid, MountID: 41},
			profile: "",
			want: ark.Root{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/media/op/AI-ARK/localai",
				Mount: "/media/op/AI-ARK", Profile: "ark-drive-full", MountID: 41, State: ark.StateReady},
		},
		{
			name:    "read-only drive keeps the given profile",
			d:       ark.DriveInfo{Mount: "/mnt/a", FSUUID: uuid, MountID: 7, ReadOnly: true},
			profile: "ark-drive-lite",
			want: ark.Root{ID: "ark-3f9c0a12", Kind: ark.KindArkDrive, Path: "/mnt/a/localai",
				Mount: "/mnt/a", Profile: "ark-drive-lite", ReadOnly: true, MountID: 7, State: ark.StateReadOnly},
		},
		{name: "bad uuid", d: ark.DriveInfo{Mount: "/mnt/a", FSUUID: "xyz"}, wantErr: true, wantErrIs: ark.ErrBadUUID},
		{name: "no mount point", d: ark.DriveInfo{FSUUID: uuid}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ark.NewDriveRoot(tc.d, tc.profile)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err = %v, want %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NewDriveRoot = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestNewInternalRoot(t *testing.T) {
	r := ark.NewInternalRoot("", "small-8gb")
	want := ark.Root{ID: "internal", Kind: ark.KindInternal, Path: "/srv/localai", Profile: "small-8gb", State: ark.StateReady}
	if r != want {
		t.Fatalf("NewInternalRoot = %+v, want %+v", r, want)
	}
	if r := ark.NewInternalRoot("/tmp/x", ""); r.Path != "/tmp/x" || r.MountID != 0 || r.Mount != "" {
		t.Fatalf("custom path root = %+v", r)
	}
}

func TestRootJoin(t *testing.T) {
	r := ark.Root{ID: "internal", Path: "/srv/localai"}
	tests := []struct {
		elems   []string
		want    string
		wantErr bool
	}{
		{[]string{"models", "qwen"}, "/srv/localai/models/qwen", false},
		{[]string{"a/../models"}, "/srv/localai/models", false},
		{[]string{}, "/srv/localai", false},
		{[]string{".."}, "", true},
		{[]string{"models", "../../etc"}, "", true},
		{[]string{"../localai2/x"}, "", true},
	}
	for _, tc := range tests {
		got, err := r.Join(tc.elems...)
		if (err != nil) != tc.wantErr || got != filepath.FromSlash(tc.want) {
			t.Errorf("Join(%q) = %q, %v; want %q (err %v)", tc.elems, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestIsNetworkFS(t *testing.T) {
	tests := []struct {
		name  string
		magic uint64
		want  bool
	}{
		{"nfs", 0x6969, true},
		{"cifs", 0xFF534D42, true},
		{"smb2", 0xFE534D42, true},
		{"fuse (covers fuse.sshfs)", 0x65735546, true},
		{"cifs sign-extended from a 32-bit f_type", 0xFFFFFFFFFF534D42, true},
		{"ext4", 0xEF53, false},
		{"xfs", 0x58465342, false},
		{"btrfs", 0x9123683E, false},
		{"tmpfs", 0x01021994, false},
		{"overlayfs", 0x794C7630, false},
		{"zero", 0, false},
	}
	for _, tc := range tests {
		if got := ark.IsNetworkFS(tc.magic); got != tc.want {
			t.Errorf("IsNetworkFS(%s 0x%X) = %v, want %v", tc.name, tc.magic, got, tc.want)
		}
	}
}

func TestProblemSeverityComesFromTheDoctorTable(t *testing.T) {
	tests := []struct {
		code string
		want doctor.Severity
	}{
		{ark.CodeNewDrive, doctor.Info},
		{ark.CodeInternalRecreated, doctor.Warning},
		{ark.CodeNetworkFS, doctor.Warning},
	}
	for _, tc := range tests {
		c, ok := doctor.Lookup(tc.code)
		if !ok || c.Severity != tc.want {
			t.Errorf("%s: table severity = %v (found %v), want %v", tc.code, c.Severity, ok, tc.want)
		}
		if _, fix, ok := doctor.Text(tc.code, "es"); !ok || fix == "" {
			t.Errorf("%s has no Spanish fix", tc.code)
		}
	}
}
