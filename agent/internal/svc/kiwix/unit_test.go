package kiwix

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/localai-kiwix.service.golden")

const goldenPath = "testdata/localai-kiwix.service.golden"

func golden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read the golden file: %v", err)
	}
	return b
}

func TestRenderUnitMatchesTheGoldenFile(t *testing.T) {
	got, err := RenderUnit(4)
	if err != nil {
		t.Fatal(err)
	}
	if *updateGolden {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := golden(t); !bytes.Equal(got, want) {
		t.Errorf("the unit differs from %s (run go test -update after changing the spec):\n--- got\n%s\n--- want\n%s", goldenPath, got, want)
	}
}

// The golden file is the text of companion-services.md section 3.3: a spec edit that is not mirrored
// in the generator (or the reverse) fails here.
func TestGoldenFileIsTheSpecText(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "specs", "companion-services.md"))
	if err != nil {
		if os.IsPermission(err) {
			t.Skipf("the spec is not readable by this user: %v", err)
		}
		t.Fatal(err)
	}
	start := bytes.Index(spec, []byte("`/etc/systemd/system/localai-kiwix.service`:\n```ini\n"))
	if start < 0 {
		t.Fatal("the unit block of section 3.3 was not found in the spec")
	}
	block := spec[start+len("`/etc/systemd/system/localai-kiwix.service`:\n```ini\n"):]
	end := bytes.Index(block, []byte("```"))
	if end < 0 {
		t.Fatal("the unit block of section 3.3 is not closed")
	}
	if !bytes.Equal(block[:end], golden(t)) {
		t.Errorf("the golden file and the unit of companion-services.md section 3.3 differ")
	}
}

func TestRenderUnitThreads(t *testing.T) {
	base := string(golden(t))
	cases := []struct {
		name    string
		threads int
		want    string // the value of --threads
	}{
		{"default when unset", 0, "4"},
		{"four", 4, "4"},
		{"eight", 8, "8"},
		{"one", 1, "1"},
		{"the maximum", MaxThreads, "64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderUnit(tc.threads)
			if err != nil {
				t.Fatal(err)
			}
			// Only the --threads line may differ from the golden file.
			want := strings.Replace(base, "--threads=4 ", "--threads="+tc.want+" ", 1)
			if string(got) != want {
				t.Errorf("rendering for %d threads differs from the golden file in more than --threads:\n%s", tc.threads, got)
			}
			if !strings.Contains(string(got), "  --threads="+tc.want+" \\\n") {
				t.Errorf("--threads=%s not rendered", tc.want)
			}
		})
	}
	t.Run("threads 8 changes exactly one line", func(t *testing.T) {
		got, _ := RenderUnit(8)
		g, w := strings.Split(string(got), "\n"), strings.Split(base, "\n")
		if len(g) != len(w) {
			t.Fatalf("line count %d vs %d", len(g), len(w))
		}
		var diff []string
		for i := range g {
			if g[i] != w[i] {
				diff = append(diff, g[i])
			}
		}
		if len(diff) != 1 || diff[0] != "  --threads=8 \\" {
			t.Errorf("changed lines = %q", diff)
		}
	})
}

func TestRenderUnitRefusesBadThreads(t *testing.T) {
	for _, n := range []int{-1, -100, MaxThreads + 1, 1 << 20} {
		_, err := RenderUnit(n)
		var ue *UnitError
		if !errors.As(err, &ue) {
			t.Fatalf("RenderUnit(%d) = %v, want a *UnitError", n, err)
		}
		if ue.Code() != doctor.AGT002 || ue.Subject != "threads" {
			t.Errorf("RenderUnit(%d): code %s subject %s", n, ue.Code(), ue.Subject)
		}
		if !doctor.Emittable(ue.Code()) {
			t.Error("the code is not emittable")
		}
	}
}

func TestRenderUnitIsDeterministic(t *testing.T) {
	a, _ := RenderUnit(6)
	b, _ := RenderUnit(6)
	if !bytes.Equal(a, b) {
		t.Error("two renderings of the same input differ")
	}
}

// What the spec requires of the unit, checked on the bytes (not only against the golden file).
func TestUnitInvariants(t *testing.T) {
	u := string(golden(t))
	mustHave := []string{
		"ConditionPathExists=/var/lib/localai/kiwix/library.xml\n",
		"ConditionPathExists=/opt/localai/kiwix-tools/current/kiwix-serve\n",
		"ExecStart=/opt/localai/kiwix-tools/current/kiwix-serve \\\n",
		"--address=127.0.0.1 --port=8888",
		"--urlRootLocation=/library",
		"--monitorLibrary --skipInvalid --blockexternal",
		"--library /var/lib/localai/kiwix/library.xml\n",
		"User=localai\n", "Group=localai\n",
		"NoNewPrivileges=yes\n", "PrivateDevices=yes\n", "ProtectSystem=strict\n", "ProtectHome=yes\n",
		"MemoryDenyWriteExecute=yes\n", "ReadOnlyPaths=/\n", "WantedBy=multi-user.target\n",
	}
	for _, s := range mustHave {
		if !strings.Contains(u, s) {
			t.Errorf("the unit lacks %q", s)
		}
	}
	// The program the unit runs is the one the installer maintains.
	if !strings.Contains(u, NewInstaller(Options{}).ServePath()) {
		t.Error("ExecStart is not the installer's current/kiwix-serve")
	}
	// Loopback only.
	if strings.Contains(u, "0.0.0.0") {
		t.Error("the unit must not listen on all interfaces")
	}
	if UnitName != "localai-kiwix.service" {
		t.Errorf("UnitName = %s", UnitName)
	}
	if got := DefaultUnitPath(""); got != "/etc/systemd/system/localai-kiwix.service" {
		t.Errorf("DefaultUnitPath = %s", got)
	}
	if got := DefaultUnitPath("/x"); got != "/x/etc/systemd/system/localai-kiwix.service" {
		t.Errorf("DefaultUnitPath(/x) = %s", got)
	}
}

// ---- the writer

type fakeReload struct {
	calls int
	err   error
	// onCall runs at each reload (to look at the disk at that moment).
	onCall func()
}

func (f *fakeReload) DaemonReload(context.Context) error {
	f.calls++
	if f.onCall != nil {
		f.onCall()
	}
	return f.err
}

func newWriter(t *testing.T, fsys config.FS, rl DaemonReloader) (*UnitWriter, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "etc", "systemd", "system", UnitName)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	// UID/GID -1: the test may not be root (CI runs unprivileged); production leaves 0, 0.
	return &UnitWriter{Path: p, FS: fsys, UID: -1, GID: -1, Systemd: rl}, p
}

func TestWriteUnit(t *testing.T) {
	t.Run("writes the file 0644 and reloads after it", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		rl.onCall = func() {
			b, err := os.ReadFile(p)
			if err != nil || !bytes.Equal(b, golden(t)) {
				t.Errorf("the file was not in place at daemon-reload: %v", err)
			}
		}
		res, err := w.Write(context.Background(), 4)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Written || res.Path != p || rl.calls != 1 {
			t.Errorf("res = %+v reloads = %d", res, rl.calls)
		}
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v %v", fi, err)
		}
		left, _ := filepath.Glob(p + ".tmp-*")
		if len(left) != 0 {
			t.Errorf("temp files left: %v", left)
		}
	})
	t.Run("an unchanged file is not rewritten but is still reloaded", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if _, err := w.Write(context.Background(), 4); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(p)
		res, err := w.Write(context.Background(), 4)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(p)
		if res.Written || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Errorf("the file was rewritten: %+v", res)
		}
		if rl.calls != 2 {
			t.Errorf("reloads = %d, want 2", rl.calls)
		}
	})
	t.Run("a new thread count rewrites only --threads", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if _, err := w.Write(context.Background(), 4); err != nil {
			t.Fatal(err)
		}
		res, err := w.Write(context.Background(), 8)
		if err != nil || !res.Written {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), "--threads=8 ") || strings.Contains(string(b), "--threads=4 ") {
			t.Errorf("unit = %s", b)
		}
	})
	t.Run("an edited unit is put back", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if err := os.WriteFile(p, []byte("[Service]\nExecStart=/bin/true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if res, err := w.Write(context.Background(), 4); err != nil || !res.Written {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, golden(t)) {
			t.Error("the unit was not replaced")
		}
	})
	t.Run("a failed daemon-reload is reported; the file is in place", func(t *testing.T) {
		rl := &fakeReload{err: errors.New("bus unavailable")}
		w, p := newWriter(t, nil, rl)
		res, err := w.Write(context.Background(), 4)
		if err == nil || !strings.Contains(err.Error(), "bus unavailable") || !res.Written {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, golden(t)) {
			t.Error("the unit is not in place")
		}
		// The retry reloads again although the file is already right.
		rl.err = nil
		if _, err := w.Write(context.Background(), 4); err != nil || rl.calls != 2 {
			t.Errorf("retry: err = %v reloads = %d", err, rl.calls)
		}
	})
	t.Run("a bad thread count writes nothing and reloads nothing", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		_, err := w.Write(context.Background(), -3)
		var ue *UnitError
		if !errors.As(err, &ue) {
			t.Fatalf("err = %v", err)
		}
		if _, serr := os.Stat(p); !os.IsNotExist(serr) || rl.calls != 0 {
			t.Error("something was written or reloaded")
		}
	})
	t.Run("a full disk leaves the old unit and does not reload", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if _, err := w.Write(context.Background(), 4); err != nil {
			t.Fatal(err)
		}
		rl.calls = 0
		w.FS = fullDiskFS{config.OSFS{}}
		_, err := w.Write(context.Background(), 8)
		if err == nil || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("err = %v, want ENOSPC", err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, golden(t)) {
			t.Error("the old unit was damaged")
		}
		if rl.calls != 0 {
			t.Error("daemon-reload ran after a failed write")
		}
		left, _ := filepath.Glob(p + ".tmp-*")
		if len(left) != 0 {
			t.Errorf("temp files left: %v", left)
		}
	})
	t.Run("an unreadable unit path is an error", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if err := os.MkdirAll(p, 0o755); err != nil { // a directory where the file should be
			t.Fatal(err)
		}
		if _, err := w.Write(context.Background(), 4); err == nil {
			t.Error("a directory in place of the unit was accepted")
		}
		if rl.calls != 0 {
			t.Error("reloaded after a failure")
		}
	})
	t.Run("no Systemd is a programming error", func(t *testing.T) {
		w, _ := newWriter(t, nil, nil)
		if _, err := w.Write(context.Background(), 4); err == nil {
			t.Error("a writer without Systemd wrote a unit")
		}
	})
}

// fullDiskFS fails every write with ENOSPC.
type fullDiskFS struct{ config.OSFS }

func (f fullDiskFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	fl, err := f.OSFS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return fullFile{fl}, nil
}

type fullFile struct{ config.File }

func (fullFile) Write([]byte) (int, error) { return 0, syscall.ENOSPC }
