package ark_test

import (
	"reflect"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

const (
	uuidA = "3f9c0a12-7b4e-4c1d-9a55-0e1f2a3b4c5d" // root id ark-3f9c0a12
	uuidB = "b81d44e0-1c2a-4f6b-8d90-5a6b7c8d9e0f" // root id ark-b81d44e0
)

func ext4Entry(opts ...string) ark.MountEntry {
	mo, so := "rw", "rw"
	if len(opts) > 0 {
		mo = opts[0]
	}
	if len(opts) > 1 {
		so = opts[1]
	}
	e := ark.MountEntry{MountID: 120, Major: 8, Minor: 17, Root: "/", MountPoint: "/media/operator/AI-ARK", FSType: "ext4", Source: "/dev/sdb1"}
	e.MountOptions = append(e.MountOptions, mo)
	e.SuperOptions = append(e.SuperOptions, so)
	return e
}

func ext4Block() ark.BlockInfo {
	return ark.BlockInfo{Device: "/dev/sdb1", Label: "AI-ARK", UUID: uuidA, Type: "ext4"}
}

// TestClassify is the table of the first four checks of ark-sync.md section 4.1.2 (section 9.1 of the
// spec: "drive classification").
func TestClassify(t *testing.T) {
	exfat := ext4Block()
	exfat.Type, exfat.UUID = "exfat", "1A2B-3C4D"
	ntfsMountedAsFuse := ext4Entry()
	ntfsMountedAsFuse.FSType = "fuseblk"
	ntfsBlock := ext4Block()
	ntfsBlock.Type = "ntfs"
	ext3Block := ext4Block()
	ext3Block.Type = "ext3"
	mismatch := ext4Entry()
	mismatch.FSType = "ext3"

	for _, c := range []struct {
		name     string
		in       ark.ClassifyInput
		state    ark.DriveState
		never    bool
		readOnly bool
		unclean  bool
		codes    []string
	}{
		{name: "ext4 and clean is ready", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ext4Block()}, state: ark.StateReady},
		{name: "exfat is unusable and never touched", in: ark.ClassifyInput{Entry: func() ark.MountEntry { e := ext4Entry(); e.FSType = "exfat"; return e }(), Block: exfat},
			state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "ntfs through fuseblk", in: ark.ClassifyInput{Entry: ntfsMountedAsFuse, Block: ntfsBlock}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "blkid says ext3 even though it is mounted as ext4", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ext3Block}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "mounted as ext3 though blkid says ext4", in: ark.ClassifyInput{Entry: mismatch, Block: ext4Block()}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "no type at all", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ark.BlockInfo{Label: "AI-ARK"}}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "fstype wins over a duplicate", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ntfsBlock, DuplicateUUID: true}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK002}},
		{name: "duplicate uuid", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ext4Block(), DuplicateUUID: true}, state: ark.StateUnusable, never: true, codes: []string{doctor.ARK006}},
		{name: "duplicate wins over read-only and errors", in: ark.ClassifyInput{Entry: ext4Entry("ro"), Block: ext4Block(), DuplicateUUID: true, FSErrors: 4},
			state: ark.StateUnusable, never: true, codes: []string{doctor.ARK006}},
		{name: "read-only mount", in: ark.ClassifyInput{Entry: ext4Entry("ro", "rw"), Block: ext4Block()}, state: ark.StateReadOnly, readOnly: true, codes: []string{doctor.ARK003}},
		{name: "read-only superblock", in: ark.ClassifyInput{Entry: ext4Entry("rw", "ro"), Block: ext4Block()}, state: ark.StateReadOnly, readOnly: true, codes: []string{doctor.ARK003}},
		{name: "errors_count above zero", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ext4Block(), FSErrors: 3},
			state: ark.StateUnclean, readOnly: true, unclean: true, codes: []string{doctor.ARK004}},
		{name: "errors_count of one", in: ark.ClassifyInput{Entry: ext4Entry(), Block: ext4Block(), FSErrors: 1},
			state: ark.StateUnclean, readOnly: true, unclean: true, codes: []string{doctor.ARK004}},
		{name: "kernel remounted read-only after an error: read_only first, both codes, unclean flag", in: ark.ClassifyInput{Entry: ext4Entry("rw", "ro"), Block: ext4Block(), FSErrors: 2},
			state: ark.StateReadOnly, readOnly: true, unclean: true, codes: []string{doctor.ARK003, doctor.ARK004}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := ark.Classify(c.in)
			if v.State != c.state || v.Never != c.never || v.ReadOnly != c.readOnly || v.Unclean != c.unclean {
				t.Errorf("verdict = state %s never %v ro %v unclean %v; want %s %v %v %v", v.State, v.Never, v.ReadOnly, v.Unclean, c.state, c.never, c.readOnly, c.unclean)
			}
			if got := codes(v.Problems); !reflect.DeepEqual(got, c.codes) {
				t.Errorf("codes = %v, want %v", got, c.codes)
			}
		})
	}
}

func TestClassifyProblemsCarryTheirFacts(t *testing.T) {
	v := ark.Classify(ark.ClassifyInput{Entry: ext4Entry(), Block: ext4Block(), FSErrors: 3})
	p := v.Problems[0]
	if p.Severity != doctor.Error || p.Details["root_id"] != "ark-3f9c0a12" || p.Details["device"] != "/dev/sdb1" || p.Details["errors_count"] != 3 {
		t.Errorf("problem = %+v", p)
	}
	v = ark.Classify(ark.ClassifyInput{Entry: ext4Entry("ro"), Block: ext4Block()})
	if v.Problems[0].Severity != doctor.Warning {
		t.Errorf("LAI-ARK-003 is a warning, got %s", v.Problems[0].Severity)
	}
}

func TestClassifyUnmounted(t *testing.T) {
	exfat := ark.BlockInfo{Device: "/dev/sdc1", Label: "AI-ARK", UUID: "1A2B-3C4D", Type: "exfat"}
	for _, c := range []struct {
		name  string
		b     ark.BlockInfo
		state ark.DriveState
		code  string
	}{
		{"ext4 is not_mounted with LAI-ARK-005", ext4Block(), ark.StateNotMounted, doctor.ARK005},
		{"exfat is unusable, not offered for mounting", exfat, ark.StateUnusable, doctor.ARK002},
		{"no type", ark.BlockInfo{Device: "/dev/sdc1", Label: "AI-ARK"}, ark.StateUnusable, doctor.ARK002},
	} {
		v := ark.ClassifyUnmounted(c.b)
		if v.State != c.state || len(v.Problems) != 1 || v.Problems[0].Code != c.code {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

func TestDriveRootID(t *testing.T) {
	for _, c := range []struct {
		name string
		b    ark.BlockInfo
		want string
	}{
		{"ext4", ext4Block(), "ark-3f9c0a12"},
		{"upper-case uuid", ark.BlockInfo{Type: "ext4", UUID: "3F9C0A12-7B4E-4C1D-9A55-0E1F2A3B4C5D"}, "ark-3f9c0a12"},
		{"exfat gets none even with a uuid", ark.BlockInfo{Type: "exfat", UUID: "1A2B-3C4D"}, ""},
		{"ext4 without a uuid", ark.BlockInfo{Type: "ext4"}, ""},
		{"ext4 with the null uuid", ark.BlockInfo{Type: "ext4", UUID: "00000000-0000-0000-0000-000000000000"}, ""},
		{"ext4 with a short uuid", ark.BlockInfo{Type: "ext4", UUID: "abc"}, ""},
	} {
		if got := ark.DriveRootID(c.b); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRootNeedsServeVerify(t *testing.T) {
	for _, c := range []struct {
		r    ark.Root
		want bool
	}{
		{ark.Root{State: ark.StateReady}, false},
		{ark.Root{State: ark.StateReadOnly}, false},
		{ark.Root{State: ark.StateUnclean}, true},
		{ark.Root{State: ark.StateReadOnly, Unclean: true}, true},
		{ark.Root{State: ark.StateReady, Unclean: true}, true},
	} {
		if got := c.r.NeedsServeVerify(); got != c.want {
			t.Errorf("%+v: %v", c.r, got)
		}
	}
}
