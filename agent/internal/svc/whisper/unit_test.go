package whisper

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
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/localai-whisper.service.golden")

const goldenPath = "testdata/localai-whisper.service.golden"

func golden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read the golden file: %v", err)
	}
	return b
}

func TestRenderUnitMatchesTheGoldenFile(t *testing.T) {
	got := RenderUnit()
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
	const head = "`/etc/systemd/system/localai-whisper.service`:\n```ini\n"
	start := bytes.Index(spec, []byte(head))
	if start < 0 {
		t.Fatal("the unit block of section 3.3 was not found in the spec")
	}
	block := spec[start+len(head):]
	end := bytes.Index(block, []byte("```"))
	if end < 0 {
		t.Fatal("the unit block of section 3.3 is not closed")
	}
	if !bytes.Equal(block[:end], golden(t)) {
		t.Errorf("the golden file and the unit of companion-services.md section 3.3 differ")
	}
}

// What the spec requires of the unit, checked on the bytes (not only against the golden file).
func TestUnitInvariants(t *testing.T) {
	u := string(RenderUnit())
	mustHave := []string{
		"ConditionPathExists=/etc/localai/whisper.env\n",
		"ConditionPathExists=/opt/localai/whisper.cpp/current/bin/whisper-server\n",
		"EnvironmentFile=/etc/localai/whisper.env\n",
		"ExecStart=/opt/localai/whisper.cpp/current/bin/whisper-server \\\n",
		"  --host 127.0.0.1 --port 8081 \\\n",
		"  --model ${WHISPER_MODEL} \\\n",
		"  --threads ${WHISPER_THREADS} \\\n",
		"  --language auto \\\n",
		"  $WHISPER_DEVICE_ARGS\n", // unbraced on purpose: systemd splits it, an empty value adds nothing
		"User=localai\n", "Group=localai\n", "SupplementaryGroups=video render\n",
		"WorkingDirectory=/var/lib/localai\n", "LimitMEMLOCK=infinity\n",
		"NoNewPrivileges=yes\n", "PrivateTmp=yes\n", "ProtectSystem=strict\n", "ProtectHome=yes\n",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n", "WantedBy=multi-user.target\n",
	}
	for _, s := range mustHave {
		if !strings.Contains(u, s) {
			t.Errorf("the unit lacks %q", s)
		}
	}
	for _, bad := range []string{"${WHISPER_DEVICE_ARGS}", "PrivateDevices", "0.0.0.0", "ReadOnlyPaths"} {
		if strings.Contains(u, bad) {
			t.Errorf("the unit must not contain %q (the GPU device nodes are needed, the server is loopback only)", bad)
		}
	}
	if !strings.Contains(u, ServerPath) || !strings.Contains(u, EnvPath) {
		t.Error("ExecStart or EnvironmentFile is not the path of this package")
	}
	if UnitName != "localai-whisper.service" {
		t.Errorf("UnitName = %s", UnitName)
	}
	if got := DefaultUnitPath(""); got != "/etc/systemd/system/localai-whisper.service" {
		t.Errorf("DefaultUnitPath = %s", got)
	}
	if got := DefaultUnitPath("/x"); got != "/x/etc/systemd/system/localai-whisper.service" {
		t.Errorf("DefaultUnitPath(/x) = %s", got)
	}
	if !bytes.Equal(RenderUnit(), RenderUnit()) {
		t.Error("two renderings differ")
	}
}

// ---- the writer

type fakeReload struct {
	calls  int
	err    error
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
	ctx := context.Background()
	t.Run("writes the file 0644 and reloads after it", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		rl.onCall = func() {
			b, err := os.ReadFile(p)
			if err != nil || !bytes.Equal(b, golden(t)) {
				t.Errorf("the file was not in place at daemon-reload: %v", err)
			}
		}
		res, err := w.Write(ctx)
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
		if left, _ := filepath.Glob(p + ".tmp-*"); len(left) != 0 {
			t.Errorf("temp files left: %v", left)
		}
	})
	t.Run("an unchanged file is not rewritten but is still reloaded", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if _, err := w.Write(ctx); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(p)
		res, err := w.Write(ctx)
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
	t.Run("an edited unit is put back", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if err := os.WriteFile(p, []byte("[Service]\nExecStart=/bin/true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if res, err := w.Write(ctx); err != nil || !res.Written {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, golden(t)) {
			t.Error("the unit was not replaced")
		}
	})
	t.Run("a failed daemon-reload is reported; the file is in place", func(t *testing.T) {
		rl := &fakeReload{err: errors.New("bus unavailable")}
		w, p := newWriter(t, nil, rl)
		res, err := w.Write(ctx)
		if err == nil || !strings.Contains(err.Error(), "bus unavailable") || !res.Written {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		if b, _ := os.ReadFile(p); !bytes.Equal(b, golden(t)) {
			t.Error("the unit is not in place")
		}
		rl.err = nil
		if _, err := w.Write(ctx); err != nil || rl.calls != 2 {
			t.Errorf("retry: err = %v reloads = %d", err, rl.calls)
		}
	})
	t.Run("a full disk leaves the old unit and does not reload", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if err := os.WriteFile(p, []byte("[Unit]\nDescription=old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w.FS = fullDiskFS{config.OSFS{}}
		_, err := w.Write(ctx)
		if err == nil || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("err = %v, want ENOSPC", err)
		}
		if b, _ := os.ReadFile(p); string(b) != "[Unit]\nDescription=old\n" {
			t.Error("the old unit was damaged")
		}
		if rl.calls != 0 {
			t.Error("daemon-reload ran after a failed write")
		}
		if left, _ := filepath.Glob(p + ".tmp-*"); len(left) != 0 {
			t.Errorf("temp files left: %v", left)
		}
	})
	t.Run("a directory in place of the unit is an error", func(t *testing.T) {
		rl := &fakeReload{}
		w, p := newWriter(t, nil, rl)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(ctx); err == nil || rl.calls != 0 {
			t.Errorf("err = %v reloads = %d", err, rl.calls)
		}
	})
	t.Run("no Systemd is a programming error", func(t *testing.T) {
		w, _ := newWriter(t, nil, nil)
		if _, err := w.Write(ctx); err == nil {
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
