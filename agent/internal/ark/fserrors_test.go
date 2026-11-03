package ark_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
)

// sysTree is a fake /sys under a temp directory.
type sysTree struct {
	t    testing.TB
	root string
}

func newSysTree(t testing.TB) sysTree { return sysTree{t: t, root: t.TempDir()} }

func (s sysTree) ext4(name, errorsCount string) {
	writeFile(s.t, filepath.Join(s.root, "fs", "ext4", name, "errors_count"), errorsCount)
}

func (s sysTree) block(devNum, rel, content string) {
	writeFile(s.t, filepath.Join(s.root, "dev", "block", devNum, rel), content)
}

func (s sysTree) counter() ark.SysErrors { return ark.SysErrors{FS: newFS(), Root: s.root} }

func TestSysErrorsCount(t *testing.T) {
	for _, c := range []struct {
		name     string
		setup    func(s sysTree)
		source   string
		devNum   string
		want     int
		wantErr  error
		wantText string
	}{
		{name: "plain partition", setup: func(s sysTree) { s.ext4("sdb1", "0\n") }, source: "/dev/sdb1", devNum: "8:17", want: 0},
		{name: "errors recorded", setup: func(s sysTree) { s.ext4("sdb1", "3\n") }, source: "/dev/sdb1", devNum: "8:17", want: 3},
		{name: "nvme partition", setup: func(s sysTree) { s.ext4("nvme1n1p1", "12") }, source: "/dev/nvme1n1p1", devNum: "259:3", want: 12},
		{name: "mapper name found through the kernel name", setup: func(s sysTree) {
			s.block("253:0", "uevent", "MAJOR=253\nMINOR=0\nDEVNAME=dm-0\nDEVTYPE=disk\n")
			s.ext4("dm-0", "2\n")
		}, source: "/dev/mapper/ark_crypt", devNum: "253:0", want: 2},
		{name: "mapper name found through dm/name", setup: func(s sysTree) {
			s.block("253:1", "dm/name", "ark_crypt\n")
			s.ext4("ark_crypt", "5\n")
		}, source: "/dev/dm-1", devNum: "253:1", want: 5},
		{name: "no ext4 entry", setup: func(s sysTree) { s.ext4("sdc1", "0") }, source: "/dev/sdb1", devNum: "8:17", wantErr: ark.ErrErrorsUnknown},
		{name: "bad devnum is ignored", setup: func(s sysTree) {}, source: "/dev/sdb1", devNum: "../../x", wantErr: ark.ErrErrorsUnknown},
		{name: "garbage counter", setup: func(s sysTree) { s.ext4("sdb1", "many\n") }, source: "/dev/sdb1", devNum: "8:17", wantText: "errors_count"},
		{name: "negative counter", setup: func(s sysTree) { s.ext4("sdb1", "-1\n") }, source: "/dev/sdb1", devNum: "8:17", wantText: "errors_count"},
		{name: "a name with a slash is never followed", setup: func(s sysTree) {
			s.block("8:17", "dm/name", "../../../etc")
			s.ext4("sdc1", "9")
		}, source: "/dev/sdb1", devNum: "8:17", wantErr: ark.ErrErrorsUnknown},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSysTree(t)
			c.setup(s)
			got, err := s.counter().ErrorsCount(c.source, c.devNum)
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case c.wantText != "":
				if err == nil || errors.Is(err, ark.ErrErrorsUnknown) {
					t.Fatalf("err = %v, want a read error mentioning %q", err, c.wantText)
				}
			case err != nil || got != c.want:
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}
