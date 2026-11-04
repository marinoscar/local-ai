package ark_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

func mountinfoFixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mountinfo", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseFixture(t testing.TB, name string) []ark.MountEntry {
	t.Helper()
	entries, err := ark.ParseMountInfo(strings.NewReader(mountinfoFixture(t, name)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return entries
}

func byMountID(t testing.TB, entries []ark.MountEntry, id int) ark.MountEntry {
	t.Helper()
	for _, e := range entries {
		if e.MountID == id {
			return e
		}
	}
	t.Fatalf("no mount %d", id)
	return ark.MountEntry{}
}

func TestParseMountInfoAllFields(t *testing.T) {
	entries, err := ark.ParseMountInfo(strings.NewReader(
		"36 35 98:0 /mnt1 /mnt2 rw,noatime shared:1 master:2 - ext3 /dev/root rw,errors=continue\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := ark.MountEntry{
		MountID: 36, ParentID: 35, Major: 98, Minor: 0, Root: "/mnt1", MountPoint: "/mnt2",
		MountOptions: []string{"rw", "noatime"}, Optional: []string{"shared:1", "master:2"},
		FSType: "ext3", Source: "/dev/root", SuperOptions: []string{"rw", "errors=continue"},
	}
	if len(entries) != 1 || !reflect.DeepEqual(entries[0], want) {
		t.Fatalf("got %+v\nwant %+v", entries, want)
	}
	if entries[0].DevNum() != "98:0" {
		t.Errorf("DevNum = %q", entries[0].DevNum())
	}
}

func TestParseMountInfoDesktopFixture(t *testing.T) {
	entries := parseFixture(t, "desktop.txt")
	if len(entries) != 14 {
		t.Fatalf("entries = %d, want 14", len(entries))
	}
	ark1 := byMountID(t, entries, 120)
	if ark1.MountPoint != "/media/operator/AI-ARK" || ark1.FSType != "ext4" || ark1.Source != "/dev/sdb1" || ark1.DevNum() != "8:17" {
		t.Errorf("ark mount = %+v", ark1)
	}
	if !reflect.DeepEqual(ark1.Optional, []string{"shared:80"}) {
		t.Errorf("optional = %v", ark1.Optional)
	}
	// An overlay mount has no optional fields at all.
	ov := byMountID(t, entries, 130)
	if ov.Optional != nil || ov.FSType != "overlay" || ov.Source != "overlay" {
		t.Errorf("overlay = %+v", ov)
	}
}

func TestParseMountInfoOctalEscapes(t *testing.T) {
	entries := parseFixture(t, "escapes.txt")
	want := map[int]string{
		201: "/media/operator/AI ARK\tdrive", // \040 and \011
		202: `/mnt/back\slash`,               // \134
		203: "/mnt/line\nfeed",               // \012
		204: `/mnt/not\04octal`,              // two digits and a letter: not an escape
		205: "/mnt/utf8é",                    // two escapes make one UTF-8 character
		206: "/mnt/bind mount",
		207: `/mnt/big\477`, // 4 is above 3: a byte cannot be 0x13f
	}
	for id, mp := range want {
		if got := byMountID(t, entries, id).MountPoint; got != mp {
			t.Errorf("mount %d: %q, want %q", id, got, mp)
		}
	}
	if got := byMountID(t, entries, 206).Root; got != "/sub dir" {
		t.Errorf("escaped root = %q", got)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"/plain":            "/plain",
		`/a\040b`:           "/a b",
		`\040`:              " ",
		`\134040`:           `\040`, // an escaped backslash followed by digits stays literal
		`/trailing\`:        `/trailing\`,
		`/short\04`:         `/short\04`,
		`/a\040b\040c\011d`: "/a b c\td",
		`\0`:                `\0`,
	} {
		if got := ark.UnescapeMountPath(in); got != want {
			t.Errorf("Unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMountInfoOptionalFields(t *testing.T) {
	entries := parseFixture(t, "optional.txt")
	for id, want := range map[int][]string{
		301: {"shared:1", "master:2"},
		302: {"shared:3", "master:4", "propagate_from:5", "unbindable"},
		303: nil,
	} {
		e := byMountID(t, entries, id)
		if !reflect.DeepEqual(e.Optional, want) {
			t.Errorf("mount %d optional = %v, want %v", id, e.Optional, want)
		}
		if e.FSType != "ext4" || e.Source == "" || len(e.SuperOptions) == 0 {
			t.Errorf("mount %d: fields after the separator were lost: %+v", id, e)
		}
	}
}

func TestMountReadOnlyMountVersusSuperOptions(t *testing.T) {
	entries := parseFixture(t, "readonly.txt")
	for _, c := range []struct {
		id                 int
		mount, super, both bool
	}{
		{501, false, false, false},
		{502, true, false, true},
		{503, false, true, true},
		{504, true, true, true},
		{505, false, false, false},
	} {
		e := byMountID(t, entries, c.id)
		if e.MountReadOnly() != c.mount || e.SuperReadOnly() != c.super || e.ReadOnly() != c.both {
			t.Errorf("mount %d: mount=%v super=%v any=%v, want %v %v %v", c.id, e.MountReadOnly(), e.SuperReadOnly(), e.ReadOnly(), c.mount, c.super, c.both)
		}
	}
}

func TestParseMountInfoMalformedLinesAreSkippedAndReported(t *testing.T) {
	entries, err := ark.ParseMountInfo(strings.NewReader(mountinfoFixture(t, "malformed.txt")))
	if len(entries) != 2 || entries[0].MountID != 601 || entries[1].MountID != 605 {
		t.Fatalf("entries = %+v", entries)
	}
	if err == nil {
		t.Fatal("skipped lines must be reported")
	}
	var lines []int
	for _, e := range unwrapAll(err) {
		var pe *ark.ParseError
		if !errors.As(e, &pe) {
			t.Fatalf("%v is not a *ParseError", e)
		}
		lines = append(lines, pe.Line)
	}
	if want := []int{3, 4, 5, 6, 7}; !reflect.DeepEqual(lines, want) {
		t.Errorf("bad lines = %v, want %v", lines, want)
	}
}

func unwrapAll(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

func TestParseMountInfoEmptyAndBlank(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n\n"} {
		entries, err := ark.ParseMountInfo(strings.NewReader(in))
		if len(entries) != 0 || err != nil {
			t.Errorf("%q: %v %v", in, entries, err)
		}
	}
}

func TestParseMountInfoReadFailureKeepsWhatWasParsed(t *testing.T) {
	r := io2{data: "601 29 8:17 / /mnt/good rw,relatime - ext4 /dev/sdb1 rw\n"}
	entries, err := ark.ParseMountInfo(&r)
	if len(entries) != 1 || err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

// io2 yields its data, then fails.
type io2 struct {
	data string
	done bool
}

func (r *io2) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("boom")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestIsCandidateMount(t *testing.T) {
	for mp, want := range map[string]bool{
		"/media/operator/AI-ARK":            true,
		"/mnt/usb":                          true,
		"/":                                 true,
		"/run/media/operator/AI-ARK":        true,
		"/run/user/1000":                    true,
		"/run/user/1000/other":              true,
		"/run/user/1000/gvfs":               false,
		"/run/user/1000/gvfs/mtp:host=x":    false,
		"/run/user/gvfs":                    true, // no uid element before gvfs
		"/proc":                             false,
		"/proc/sys/fs/binfmt_misc":          false,
		"/sys/fs/cgroup":                    false,
		"/dev":                              false,
		"/dev/pts":                          false,
		"/devices":                          true, // a different directory that starts with /dev
		"/snap/core22/1234":                 false,
		"/snapshots/usb":                    true,
		"/media/operator/proc":              true,
		"/home/operator/mnt/dev/AI-ARK":     true,
		"/media/operator/AI-ARK/../../proc": true, // mountinfo paths are canonical; not our job to resolve
	} {
		if got := ark.IsCandidateMount(mp); got != want {
			t.Errorf("IsCandidateMount(%q) = %v, want %v", mp, got, want)
		}
	}
}

func TestCandidatesOfTheDesktopFixture(t *testing.T) {
	var got []string
	for _, e := range ark.Candidates(parseFixture(t, "desktop.txt")) {
		got = append(got, e.MountPoint)
	}
	// Pseudo filesystems, /dev, /sys, /proc, the snap loop device, gvfs and the overlay are out; the
	// root disk, the EFI partition and the two USB drives are in, in mount id order.
	want := []string{"/", "/boot/efi", "/media/operator/AI-ARK", "/media/operator/AI-ARK1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestCandidatesBindMounts(t *testing.T) {
	cands := ark.Candidates(parseFixture(t, "bind.txt"))
	var ids []int
	for _, e := range cands {
		ids = append(ids, e.MountID)
	}
	// 401 is the drive. 402 is a bind mount of a subdirectory and 403 a second mount of the same
	// device: neither is another drive. 404 mounts only a subdirectory, so it is not a drive either.
	if want := []int{401}; !reflect.DeepEqual(ids, want) {
		t.Errorf("candidates = %v, want %v", ids, want)
	}
}

func TestCandidatesOrderByMountID(t *testing.T) {
	in := []ark.MountEntry{
		{MountID: 30, Major: 8, Minor: 33, Root: "/", MountPoint: "/mnt/b", Source: "/dev/sdc1", FSType: "ext4"},
		{MountID: 10, Major: 8, Minor: 17, Root: "/", MountPoint: "/mnt/a", Source: "/dev/sdb1", FSType: "ext4"},
	}
	got := ark.Candidates(in)
	if len(got) != 2 || got[0].MountID != 10 || got[1].MountID != 30 {
		t.Errorf("candidates = %+v", got)
	}
	if in[0].MountID != 30 {
		t.Error("Candidates must not reorder its input")
	}
}

func TestIsDevice(t *testing.T) {
	for src, want := range map[string]bool{
		"/dev/sdb1": true, "/dev/mapper/vg-root": true, "/dev/": false, "": false,
		"tmpfs": false, "overlay": false, "server:/export": false, "gvfsd-fuse": false,
	} {
		if got := (ark.MountEntry{Source: src}).IsDevice(); got != want {
			t.Errorf("IsDevice(%q) = %v", src, got)
		}
	}
}

// TestProcMountsReadsTheRealTable: the real /proc/self/mountinfo parses, and every line becomes an
// entry (a kernel never writes a line this parser rejects).
func TestProcMountsReadsTheRealTable(t *testing.T) {
	if _, err := os.Stat(ark.DefaultMountInfoPath); err != nil {
		t.Skip("no /proc/self/mountinfo here")
	}
	entries, err := ark.ProcMounts{}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("the real mount table is empty")
	}
	for _, e := range entries {
		if e.MountPoint == "" || e.FSType == "" {
			t.Errorf("incomplete entry %+v", e)
		}
	}
}
