package ark_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	execfake "github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

const blkidPath = "/usr/sbin/blkid"

func newProber(r execx.Runner) *ark.BlkidProber { return &ark.BlkidProber{Runner: r, Path: blkidPath} }

const exportExt4 = `DEVNAME=/dev/sdb1
LABEL=AI-ARK
UUID=3f9c0a12-7b4e-4c1d-9a55-0e1f2a3b4c5d
BLOCK_SIZE=4096
TYPE=ext4
USAGE=filesystem
`

func TestBlkidProbe(t *testing.T) {
	probe := execfake.Key(blkidPath, "-p", "-o", "export", "/dev/sdb1")
	for _, c := range []struct {
		name    string
		res     execx.Result
		err     error
		want    ark.BlockInfo
		wantErr error
		errText string
	}{
		{name: "ext4 with a label", res: execx.Result{Stdout: []byte(exportExt4)},
			want: ark.BlockInfo{Device: "/dev/sdb1", Label: "AI-ARK", UUID: "3f9c0a12-7b4e-4c1d-9a55-0e1f2a3b4c5d", Type: "ext4"}},
		{name: "no label", res: execx.Result{Stdout: []byte("UUID=1234-ABCD\nTYPE=vfat\n")},
			want: ark.BlockInfo{Device: "/dev/sdb1", UUID: "1234-ABCD", Type: "vfat"}},
		{name: "no signature (exit 2) is an empty answer, not an error", res: execx.Result{ExitCode: 2},
			want: ark.BlockInfo{Device: "/dev/sdb1"}},
		{name: "other exit code", res: execx.Result{ExitCode: 4, Stderr: []byte("blkid: error: /dev/sdb1: No such file or directory\nmore")},
			errText: "blkid exited 4: blkid: error: /dev/sdb1: No such file or directory"},
		{name: "timeout", err: execx.ErrTimeout, wantErr: ark.ErrBlkidTimeout},
		{name: "cannot start", err: execx.ErrInvalid, wantErr: ark.ErrBlkidUnavailable},
		{name: "runner error", err: errors.New("fork failed"), errText: "fork failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProber(execfake.New().Expect(probe, c.res, c.err))
			got, err := p.Probe(context.Background(), "/dev/sdb1")
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case c.errText != "":
				if err == nil || !strings.Contains(err.Error(), c.errText) {
					t.Fatalf("err = %v, want %q", err, c.errText)
				}
			case err != nil:
				t.Fatal(err)
			default:
				if got != c.want {
					t.Errorf("got %+v, want %+v", got, c.want)
				}
			}
		})
	}
}

// TestBlkidRunsWithAFixedArgvAndALimit: the command is exactly `blkid -p -o export <device>`, with the
// 5 s timeout and an output cap, and no shell.
func TestBlkidRunsWithAFixedArgvAndALimit(t *testing.T) {
	r := execfake.New().Expect(execfake.Key(blkidPath, "-p", "-o", "export", "/dev/sdb1"), execx.Result{Stdout: []byte(exportExt4)}, nil)
	if _, err := newProber(r).Probe(context.Background(), "/dev/sdb1"); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.Path != blkidPath || !reflect.DeepEqual(c.Args, []string{"-p", "-o", "export", "/dev/sdb1"}) {
		t.Errorf("command = %s %v", c.Path, c.Args)
	}
	if c.Timeout != 5*time.Second || c.MaxOut <= 0 {
		t.Errorf("timeout %v, cap %d", c.Timeout, c.MaxOut)
	}
}

func TestBlkidRefusesAnythingButADevicePath(t *testing.T) {
	r := execfake.New()
	p := newProber(r)
	for _, dev := range []string{"", "sdb1", "-x", "/dev/", "/etc/passwd", "/dev/../etc/passwd", "/dev/sd\nb1", "/dev/sd\x00b1", "../dev/sdb1"} {
		if _, err := p.Probe(context.Background(), dev); !errors.Is(err, ark.ErrBadDevice) {
			t.Errorf("Probe(%q) = %v, want ErrBadDevice", dev, err)
		}
	}
	if len(r.Calls()) != 0 {
		t.Errorf("a program was run for a bad device: %v", r.Calls())
	}
}

func TestBlkidFindByLabel(t *testing.T) {
	find := execfake.Key(blkidPath, "-t", "LABEL=AI-ARK", "-o", "device")
	for _, c := range []struct {
		name    string
		res     execx.Result
		err     error
		want    []string
		wantErr error
		failed  bool
	}{
		{name: "two devices", res: execx.Result{Stdout: []byte("/dev/sdb1\n/dev/sdc1\n")}, want: []string{"/dev/sdb1", "/dev/sdc1"}},
		{name: "no match (exit 2)", res: execx.Result{ExitCode: 2}, want: nil},
		{name: "junk lines are dropped", res: execx.Result{Stdout: []byte("\n/dev/sdb1\nnot a device\n../x\n")}, want: []string{"/dev/sdb1"}},
		{name: "timeout", err: execx.ErrTimeout, wantErr: ark.ErrBlkidTimeout},
		{name: "failure", res: execx.Result{ExitCode: 4, Stderr: []byte("usage")}, failed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := newProber(execfake.New().Expect(find, c.res, c.err)).FindByLabel(context.Background(), ark.ArkLabel)
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v", err)
				}
			case c.failed:
				if err == nil {
					t.Fatal("want an error")
				}
			default:
				if err != nil || !reflect.DeepEqual(got, c.want) {
					t.Errorf("got %v, %v; want %v", got, err, c.want)
				}
			}
		})
	}
	if _, err := newProber(execfake.New()).FindByLabel(context.Background(), ""); err == nil {
		t.Error("an empty label must be refused")
	}
}

func TestParseBlkidExport(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		want ark.BlockInfo
	}{
		"empty":            {"", ark.BlockInfo{}},
		"first one wins":   {"LABEL=AI-ARK\nLABEL=other\nTYPE=ext4\nTYPE=xfs\n", ark.BlockInfo{Label: "AI-ARK", Type: "ext4"}},
		"value with =":     {"LABEL=a=b\n", ark.BlockInfo{Label: "a=b"}},
		"label with space": {"LABEL=AI ARK\n", ark.BlockInfo{Label: "AI ARK"}},
		"empty value":      {"LABEL=\nTYPE=ext4\n", ark.BlockInfo{Type: "ext4"}},
		"no equals":        {"garbage\nTYPE=ext4\n", ark.BlockInfo{Type: "ext4"}},
		"no trailing nl":   {"UUID=abcd1234-0000", ark.BlockInfo{UUID: "abcd1234-0000"}},
		"unknown keys":     {"DEVNAME=/dev/x\nPTTYPE=gpt\nLABEL_FATBOOT=X\n", ark.BlockInfo{}},
	} {
		if got := ark.ParseBlkidExport([]byte(c.in)); got != c.want {
			t.Errorf("%s: got %+v, want %+v", name, got, c.want)
		}
	}
}

func TestIsArkLabelIsExactAndCaseSensitive(t *testing.T) {
	for label, want := range map[string]bool{
		"AI-ARK": true, "AI-ARK2": false, "ai-ark": false, "Ai-Ark": false, "AI-ARK ": false, " AI-ARK": false, "AI-ARK1": false, "": false, "AI_ARK": false,
	} {
		if got := ark.IsArkLabel(label); got != want {
			t.Errorf("IsArkLabel(%q) = %v", label, got)
		}
	}
}
