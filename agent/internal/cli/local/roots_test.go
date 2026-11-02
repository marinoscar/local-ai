package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// addDrive creates <root>/mnt/<name> as a mounted AI-ARK drive: it appends a mountinfo line and
// creates <mount>/localai with a ROOT.json of kind ark_drive.
func addDrive(t *testing.T, e Env, name string, readOnly bool) Root {
	t.Helper()
	mount := filepath.Join(filepath.Dir(e.Paths.Mountinfo), "mnt", name)
	rootPath := filepath.Join(mount, "localai")
	write(t, filepath.Join(rootPath, "ROOT.json"), `{"schema_version":1,"kind":"ark_drive","label":"AI-ARK"}`, 0o644)
	opts := "rw,relatime"
	if readOnly {
		opts = "ro,relatime"
	}
	line := fmt.Sprintf("40 25 8:%d / %s %s shared:1 - ext4 /dev/sd%s1 rw\n", len(name), mount, opts, name)
	f, err := os.OpenFile(e.Paths.Mountinfo, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	return Root{Kind: KindDrive, Path: rootPath, Mount: mount, ReadOnly: readOnly}
}

func TestParseMountinfo(t *testing.T) {
	in := strings.Join([]string{
		`36 35 98:0 /mnt1 /mnt/with\040space rw,noatime master:1 - ext4 /dev/sdb1 rw,errors=continue`,
		`37 35 98:1 / /srv ro,relatime - ext4 /dev/sdc1 rw`,
		`38 35 98:2 / /data rw,relatime shared:2 shared:3 - ext4 /dev/sdd1 ro`,
		`garbage`,
		`39 35 98:3 / /proc rw - proc proc rw`,
	}, "\n")
	got := ParseMountinfo(strings.NewReader(in))
	want := []Mount{
		{Point: "/mnt/with space", FSType: "ext4", Source: "/dev/sdb1"},
		{Point: "/srv", FSType: "ext4", Source: "/dev/sdc1", ReadOnly: true},
		{Point: "/data", FSType: "ext4", Source: "/dev/sdd1", ReadOnly: true},
		{Point: "/proc", FSType: "proc", Source: "proc"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestFindRootsOnlyArkDrives(t *testing.T) {
	e, run := testEnv(t)
	if err := os.MkdirAll(e.Paths.InternalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	ark := addDrive(t, e, "a", false)
	ro := addDrive(t, e, "b", true)

	base := filepath.Dir(e.Paths.Mountinfo)
	// an ext4 mount with a localai folder that is NOT an ark drive (internal kind, no label)
	other := filepath.Join(base, "mnt", "other")
	write(t, filepath.Join(other, "localai", "ROOT.json"), `{"kind":"internal"}`, 0o644)
	// an ark drive known only by its label (no ROOT.json)
	byLabel := filepath.Join(base, "mnt", "bylabel")
	if err := os.MkdirAll(filepath.Join(byLabel, "localai"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a labelled but uninitialised drive: no localai folder
	bare := filepath.Join(base, "mnt", "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	// a drive that is not ext4
	vfat := filepath.Join(base, "mnt", "vfat")
	write(t, filepath.Join(vfat, "localai", "ROOT.json"), `{"kind":"ark_drive"}`, 0o644)

	f, _ := os.OpenFile(e.Paths.Mountinfo, os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, "50 25 9:1 / %s rw - ext4 /dev/sdx1 rw\n", other)
	fmt.Fprintf(f, "51 25 9:2 / %s rw - ext4 /dev/sdy1 rw\n", byLabel)
	fmt.Fprintf(f, "52 25 9:3 / %s rw - ext4 /dev/sdz1 rw\n", bare)
	fmt.Fprintf(f, "53 25 9:4 / %s rw - vfat /dev/sdw1 rw\n", vfat)
	fmt.Fprintf(f, "54 25 9:5 / /proc/odd rw - ext4 /dev/sdv1 rw\n")
	f.Close()
	run.Expect(fake.Key("/usr/bin/blkid", "-p", "-o", "export", "/dev/sdy1"), execx.Result{Stdout: []byte("DEVNAME=/dev/sdy1\nLABEL=AI-ARK\nTYPE=ext4\n")}, nil)
	run.Expect(fake.Key("/usr/bin/blkid", "-p", "-o", "export", "/dev/sdx1"), execx.Result{Stdout: []byte("LABEL=AI-ARK2\n")}, nil)

	roots := FindRoots(context.Background(), e)
	var got []string
	for _, r := range roots {
		got = append(got, string(r.Kind)+":"+r.Path)
	}
	want := []string{
		"internal:" + e.Paths.InternalRoot,
		"drive:" + ark.Path,
		"drive:" + ro.Path,
		"drive:" + filepath.Join(byLabel, "localai"),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if !roots[2].ReadOnly || roots[1].ReadOnly {
		t.Error("read-only flag wrong")
	}
}

func TestFindRootsWithoutBlkidOrMountinfo(t *testing.T) {
	e, _ := testEnv(t)
	if err := os.MkdirAll(e.Paths.InternalRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	e.Runner = nil
	if r := FindRoots(context.Background(), e); len(r) != 1 || r[0].Kind != KindInternal {
		t.Errorf("no mountinfo: %+v", r)
	}
	if err := os.WriteFile(e.Paths.Mountinfo, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	e2, _ := testEnv(t)
	e2.LookPath = func(string) (string, error) { return "", errors.New("blkid is missing") }
	if r := FindRoots(context.Background(), e2); len(r) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestInternalRootThatIsItsOwnMountIsNotADrive(t *testing.T) {
	e, _ := testEnv(t)
	base := filepath.Dir(e.Paths.Mountinfo)
	e.Paths.InternalRoot = filepath.Join(base, "srv", "localai")
	write(t, filepath.Join(e.Paths.InternalRoot, "ROOT.json"), `{"kind":"ark_drive"}`, 0o644) // even so
	if err := os.WriteFile(e.Paths.Mountinfo, []byte(fmt.Sprintf("1 0 8:1 / %s rw - ext4 /dev/sda1 rw\n", filepath.Join(base, "srv"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := FindRoots(context.Background(), e); len(r) != 1 || r[0].Kind != KindInternal {
		t.Errorf("%+v", r)
	}
}
