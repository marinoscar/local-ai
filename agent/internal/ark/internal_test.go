package ark_test

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

func internalOpts(path string) ark.InternalOptions {
	return ark.InternalOptions{
		Path: path, Profile: "small-8gb", NodeID: "ai-n1", AgentVersion: "0.4.0",
		Now: fixedNow, Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// assertLayout fails if any layout directory of root is missing.
func assertLayout(t testing.TB, root string) {
	t.Helper()
	for _, rel := range overviewLayout {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			t.Errorf("layout directory %s missing under %s (%v)", rel, root, err)
		}
	}
}

func TestEnsureInternalHealthyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "localai")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "models", "m1", "m1.gguf"), "weights")
	writeFile(t, filepath.Join(root, ark.RootJSONName), string(fixture(t, "valid-internal.json")))

	got, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
	if err != nil || len(problems) != 0 {
		t.Fatalf("EnsureInternal = %v, %v, %v", got, problems, err)
	}
	want := ark.Root{ID: "internal", Kind: ark.KindInternal, Path: root, Profile: "small-8gb", State: ark.StateReady}
	if got != want {
		t.Fatalf("root = %+v, want %+v", got, want)
	}
	if readFile(t, filepath.Join(root, ark.RootJSONName)) != string(fixture(t, "valid-internal.json")) {
		t.Error("a valid ROOT.json was rewritten")
	}
	if readFile(t, filepath.Join(root, "models", "m1", "m1.gguf")) != "weights" {
		t.Error("content changed")
	}
	assertLayout(t, root) // a partial layout is completed silently
}

func TestEnsureInternalWritesROOTJSONWhenAbsent(t *testing.T) {
	root := t.TempDir() // exists, empty: the installer made it, nothing else
	got, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
	if err != nil || len(problems) != 0 || got.State != ark.StateReady || got.ReadOnly {
		t.Fatalf("EnsureInternal = %+v, %v, %v", got, problems, err)
	}
	rj, err := ark.ReadRootJSON(filepath.Join(root, ark.RootJSONName))
	if err != nil {
		t.Fatal(err)
	}
	if rj.RootID != "internal" || rj.Kind != ark.KindInternal || rj.Profile != "small-8gb" ||
		!rj.CreatedAt.Equal(epoch) || rj.CreatedBy.NodeID != "ai-n1" || rj.CreatedBy.AgentVersion != "0.4.0" {
		t.Fatalf("defaults = %+v", rj)
	}
	assertLayout(t, root)
	// Repeating changes nothing, including the file's bytes.
	before := readFile(t, filepath.Join(root, ark.RootJSONName))
	if _, problems, err := ark.EnsureInternal(newFS(), internalOpts(root)); err != nil || len(problems) != 0 {
		t.Fatalf("second run: %v, %v", problems, err)
	}
	if readFile(t, filepath.Join(root, ark.RootJSONName)) != before {
		t.Fatal("second run rewrote ROOT.json")
	}
}

func TestEnsureInternalMissingRootIsRecreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "srv", "localai")
	fsys := newFS()
	opts := internalOpts(root)
	opts.Owner = &ark.Owner{UID: 981, GID: 982}

	got, problems, err := ark.EnsureInternal(fsys, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ark.StateReady || got.ReadOnly || got.ID != "internal" {
		t.Fatalf("root = %+v", got)
	}
	if len(problems) != 1 || problems[0].Code != doctor.ARK020 || problems[0].Severity != doctor.Warning {
		t.Fatalf("problems = %+v, want exactly LAI-ARK-020 as warning", problems)
	}
	if problems[0].Details["root_id"] != "internal" || problems[0].Details["path"] != root {
		t.Errorf("details = %v", problems[0].Details)
	}
	assertLayout(t, root)
	if !exists(filepath.Join(root, ark.RootJSONName)) {
		t.Error("no ROOT.json")
	}
	if want := []chownCall{{root, 981, 982}}; !reflect.DeepEqual(fsys.chowns, want) {
		t.Errorf("chowns = %v, want only the top directory %v", fsys.chowns, want)
	}
	// The next check finds a healthy root and raises nothing.
	if _, problems, err := ark.EnsureInternal(fsys, opts); err != nil || len(problems) != 0 {
		t.Fatalf("second run: %v, %v", problems, err)
	}
}

func TestEnsureInternalNotADirectoryIsMovedAsideNeverDeleted(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"regular file", func(t *testing.T, root string) { writeFile(t, root, "precious") }},
		{"symlink to a file", func(t *testing.T, root string) {
			target := filepath.Join(filepath.Dir(root), "target")
			writeFile(t, target, "precious")
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, root string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(root), "gone"), root); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "localai")
			tc.setup(t, root)
			got, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(codes(problems), []string{doctor.ARK020}) || got.State != ark.StateReady {
				t.Fatalf("got %+v, problems %v", got, codes(problems))
			}
			assertLayout(t, root)
			var aside string
			for _, n := range names(t, parent) {
				if strings.HasPrefix(n, "localai.replaced-20261102T180411Z") {
					aside = filepath.Join(parent, n)
				}
			}
			if aside == "" {
				t.Fatalf("nothing moved aside: %v", names(t, parent))
			}
			if _, err := os.Lstat(aside); err != nil {
				t.Fatal(err)
			}
			if tc.name == "regular file" && readFile(t, aside) != "precious" {
				t.Error("the file's content changed")
			}
		})
	}
}

func TestEnsureInternalAsideNameCollision(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "localai")
	writeFile(t, root, "second")
	writeFile(t, root+".replaced-20261102T180411Z", "first")
	if _, _, err := ark.EnsureInternal(newFS(), internalOpts(root)); err != nil {
		t.Fatal(err)
	}
	if readFile(t, root+".replaced-20261102T180411Z") != "first" || readFile(t, root+".replaced-20261102T180411Z-1") != "second" {
		t.Fatalf("aside files: %v", names(t, parent))
	}
}

func TestEnsureInternalSymlinkToADirectoryIsADirectory(t *testing.T) {
	parent := t.TempDir()
	big := filepath.Join(parent, "big-disk")
	if err := os.Mkdir(big, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(big, "kiwix", "x.zim"), "zim")
	root := filepath.Join(parent, "localai")
	if err := os.Symlink(big, root); err != nil {
		t.Fatal(err)
	}
	_, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems %v, err %v: an operator's symlink to a bigger disk is fine", problems, err)
	}
	if readFile(t, filepath.Join(big, "kiwix", "x.zim")) != "zim" || !exists(filepath.Join(big, "ROOT.json")) {
		t.Fatalf("symlink target: %v", names(t, big))
	}
}

func TestEnsureInternalNetworkFilesystemIsReadOnly(t *testing.T) {
	magics := []struct {
		name  string
		magic uint64
	}{{"nfs", ark.MagicNFS}, {"cifs", ark.MagicSMB}, {"smb2", ark.MagicSMB2}, {"fuse.sshfs", ark.MagicFUSE}}
	for _, m := range magics {
		t.Run("existing root on "+m.name, func(t *testing.T) {
			root := t.TempDir()
			fsys := newFS()
			fsys.magic[root] = m.magic
			got, problems, err := ark.EnsureInternal(fsys, internalOpts(root))
			if err != nil {
				t.Fatal(err)
			}
			if !got.ReadOnly || got.State != ark.StateReadOnly || got.ID != "internal" {
				t.Fatalf("root = %+v", got)
			}
			if len(problems) != 1 || problems[0].Code != doctor.ARK021 || problems[0].Severity != doctor.Warning {
				t.Fatalf("problems = %+v, want exactly LAI-ARK-021", problems)
			}
			if len(names(t, root)) != 0 {
				t.Fatalf("a read-only root was written: %v", names(t, root))
			}
		})
	}
	t.Run("missing root under a network parent is not created", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "deep", "localai")
		fsys := newFS()
		fsys.magic[parent] = ark.MagicNFS
		got, problems, err := ark.EnsureInternal(fsys, internalOpts(root))
		if err != nil || !got.ReadOnly || !reflect.DeepEqual(codes(problems), []string{doctor.ARK021}) {
			t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
		}
		if len(names(t, parent)) != 0 {
			t.Fatalf("something was created: %v", names(t, parent))
		}
	})
	t.Run("a local root under a network mount further up is judged by its own filesystem", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "localai")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		fsys := newFS()
		fsys.magic[parent] = ark.MagicNFS
		fsys.magic[root] = 0xEF53 // a local mount over it
		if got, problems, err := ark.EnsureInternal(fsys, internalOpts(root)); err != nil || got.ReadOnly || len(problems) != 0 {
			t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
		}
	})
	t.Run("unknown filesystem type counts as local", func(t *testing.T) {
		root := t.TempDir()
		fsys := newFS()
		fsys.magicErr = ark.ErrFSTypeUnsupported
		if got, problems, err := ark.EnsureInternal(fsys, internalOpts(root)); err != nil || got.ReadOnly || len(problems) != 0 {
			t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
		}
	})
}

func TestEnsureInternalROOTJSONHandling(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantAside   bool   // the old file is moved to ROOT.json.damaged-*
		wantKept    bool   // the file is left byte for byte
		wantProfile string // of the resulting file
	}{
		{"damaged (truncated)", "truncated.json", true, false, "small-8gb"},
		{"garbage", "garbage.json", true, false, "small-8gb"},
		{"a drive's file copied here", "valid-drive.json", true, false, "small-8gb"},
		{"newer schema is left alone", "schema-v2.json", false, true, "ark-drive-full"},
		{"valid", "valid-internal.json", false, true, "small-8gb"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := filepath.Join(root, ark.RootJSONName)
			writeFile(t, p, string(fixture(t, tc.fixture)))
			got, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
			if err != nil || len(problems) != 0 || got.State != ark.StateReady {
				t.Fatalf("got %+v, %v, %v: the informational file never raises a code", got, codes(problems), err)
			}
			var aside []string
			for _, n := range names(t, root) {
				if strings.HasPrefix(n, ark.RootJSONName+".damaged-") {
					aside = append(aside, n)
				}
			}
			if tc.wantAside != (len(aside) == 1) {
				t.Fatalf("aside files = %v, want aside=%v", aside, tc.wantAside)
			}
			if tc.wantAside && readFile(t, filepath.Join(root, aside[0])) != string(fixture(t, tc.fixture)) {
				t.Error("the moved-aside file changed")
			}
			if tc.wantKept && readFile(t, p) != string(fixture(t, tc.fixture)) {
				t.Error("the file was rewritten")
			}
			if !tc.wantKept {
				rj, err := ark.ReadRootJSON(p)
				if err != nil || rj.Kind != ark.KindInternal || rj.Profile != tc.wantProfile {
					t.Fatalf("replacement = %+v, %v", rj, err)
				}
			}
		})
	}
}

func TestEnsureInternalUnreadableROOTJSONIsNotHealed(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "valid-internal.json")))
	fsys := newFS()
	fsys.FailOn("readfile", -1) // EIO on every read
	got, _, err := ark.EnsureInternal(fsys, internalOpts(root))
	if !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("err = %v, want the read error", err)
	}
	if !got.ReadOnly || got.State != ark.StateReadOnly {
		t.Fatalf("root = %+v", got)
	}
	if readFile(t, p) != string(fixture(t, "valid-internal.json")) || len(names(t, root)) == 0 {
		t.Fatal("ROOT.json was touched")
	}
	for _, n := range names(t, root) {
		if strings.Contains(n, "damaged") {
			t.Fatalf("an unreadable (not damaged) file was moved aside: %s", n)
		}
	}
}

func TestEnsureInternalFailures(t *testing.T) {
	t.Run("read-only filesystem, root missing", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "localai")
		fsys := newFS()
		fsys.mkdirErr[root] = &os.PathError{Op: "mkdir", Path: root, Err: syscall.EROFS}
		got, problems, err := ark.EnsureInternal(fsys, internalOpts(root))
		if !errors.Is(err, syscall.EROFS) {
			t.Fatalf("err = %v", err)
		}
		if !got.ReadOnly || got.State != ark.StateUnusable || len(problems) != 0 {
			t.Fatalf("got %+v, %v: nothing was recreated, so no LAI-ARK-020", got, codes(problems))
		}
	})
	t.Run("read-only filesystem, root there but a directory missing", func(t *testing.T) {
		root := t.TempDir()
		fsys := newFS()
		fsys.mkdirErr[filepath.Join(root, "docs")] = &os.PathError{Op: "mkdir", Err: syscall.EROFS}
		got, _, err := ark.EnsureInternal(fsys, internalOpts(root))
		if !errors.Is(err, syscall.EROFS) || !got.ReadOnly || got.State != ark.StateReadOnly {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("disk full while recreating: the root exists, so ARK-020 is raised, and a retry completes", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "localai")
		fsys := newFS()
		fsys.mkdirErr[filepath.Join(root, "kit")] = &os.PathError{Op: "mkdir", Err: syscall.ENOSPC}
		got, problems, err := ark.EnsureInternal(fsys, internalOpts(root))
		if !errors.Is(err, syscall.ENOSPC) || !got.ReadOnly {
			t.Fatalf("got %+v, %v", got, err)
		}
		if !reflect.DeepEqual(codes(problems), []string{doctor.ARK020}) {
			t.Fatalf("problems = %v", codes(problems))
		}
		delete(fsys.mkdirErr, filepath.Join(root, "kit"))
		got, problems, err = ark.EnsureInternal(fsys, internalOpts(root))
		if err != nil || got.ReadOnly || len(problems) != 0 {
			t.Fatalf("retry: %+v, %v, %v", got, codes(problems), err)
		}
		assertLayout(t, root)
	})
	t.Run("disk full writing ROOT.json leaves no file and no temp", func(t *testing.T) {
		root := t.TempDir()
		fsys := newFS()
		fsys.FailOn("write", 1)
		got, _, err := ark.EnsureInternal(fsys, internalOpts(root))
		if err == nil || !got.ReadOnly {
			t.Fatalf("got %+v, %v", got, err)
		}
		if exists(filepath.Join(root, ark.RootJSONName)) {
			t.Fatal("a half-written ROOT.json is visible")
		}
		noTemps(t, root)
	})
	t.Run("killed before the rename: the next start sweeps the temp and finishes", func(t *testing.T) {
		root := t.TempDir()
		crash := newFS()
		crash.Crash = true
		crash.FailNth("rename", 1)
		if _, _, err := ark.EnsureInternal(crash, internalOpts(root)); err == nil {
			t.Fatal("want an error")
		}
		temps := 0
		for _, n := range names(t, root) {
			if strings.HasPrefix(n, ark.RootJSONName+".tmp-") {
				temps++
			}
		}
		if temps != 1 {
			t.Fatalf("temp files after the kill = %d (%v)", temps, names(t, root))
		}
		got, problems, err := ark.EnsureInternal(newFS(), internalOpts(root))
		if err != nil || len(problems) != 0 || got.ReadOnly {
			t.Fatalf("restart: %+v, %v, %v", got, codes(problems), err)
		}
		noTemps(t, root)
		if _, err := ark.ReadRootJSON(filepath.Join(root, ark.RootJSONName)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a file named models in the root", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "models"), "oops")
		got, _, err := ark.EnsureInternal(newFS(), internalOpts(root))
		if !errors.Is(err, ark.ErrLayoutConflict) || !got.ReadOnly {
			t.Fatalf("got %+v, %v", got, err)
		}
		if readFile(t, filepath.Join(root, "models")) != "oops" {
			t.Fatal("the file was replaced")
		}
	})
	t.Run("the old root cannot be moved aside", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "localai")
		writeFile(t, root, "precious")
		fsys := newFS()
		fsys.FailOn("rename", 1)
		got, problems, err := ark.EnsureInternal(fsys, internalOpts(root))
		if err == nil || got.State != ark.StateUnusable || !got.ReadOnly || len(problems) != 0 {
			t.Fatalf("got %+v, %v, %v", got, codes(problems), err)
		}
		if readFile(t, root) != "precious" {
			t.Fatal("the file was lost")
		}
	})
	t.Run("stat fails with something other than not-exist", func(t *testing.T) {
		fsys := newFS()
		fsys.FailOn("stat", 1)
		got, _, err := ark.EnsureInternal(fsys, internalOpts(t.TempDir()))
		if !errors.Is(err, fake.ErrInjected) || !got.ReadOnly {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestEnsureInternalDefaults(t *testing.T) {
	// No options but a path: the profile is empty, the clock and logger default.
	root := t.TempDir()
	got, _, err := ark.EnsureInternal(newFS(), ark.InternalOptions{Path: root})
	if err != nil || got.Path != root || got.Profile != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ark.RootJSONName)); errors.Is(err, fs.ErrNotExist) {
		t.Fatal("no ROOT.json")
	}
}

func TestMonitor(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "localai")
	clock := fake.NewClock(epoch)
	results := make(chan []string, 8)
	m := ark.NewMonitor(newFS(), internalOpts(root), clock, func(_ ark.Root, ps []ark.Problem, _ error) {
		results <- codes(ps)
	})
	if m.Root().Path != root || m.Root().State != ark.StateReady {
		t.Fatalf("root before the first check = %+v", m.Root())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	// Startup check: the root is missing, so it is recreated and ARK-020 is raised.
	if got := <-results; !reflect.DeepEqual(got, []string{doctor.ARK020}) {
		t.Fatalf("startup problems = %v", got)
	}
	if clock.Tickers() != 1 {
		t.Fatalf("tickers = %d, want 1", clock.Tickers())
	}

	// Nothing is due before 6 h.
	clock.Advance(ark.CheckInterval - 1)
	select {
	case got := <-results:
		t.Fatalf("a check ran early: %v", got)
	default:
	}

	// At 6 h the root is healthy, but ARK-020 stays latched until the sync engine clears it.
	clock.Advance(1)
	if got := <-results; !reflect.DeepEqual(got, []string{doctor.ARK020}) {
		t.Fatalf("second check problems = %v, want the latched ARK-020", got)
	}
	if !reflect.DeepEqual(codes(m.Problems()), []string{doctor.ARK020}) {
		t.Fatalf("Problems = %v", codes(m.Problems()))
	}
	m.ClearRecreated()
	if len(m.Problems()) != 0 {
		t.Fatalf("Problems after ClearRecreated = %v", codes(m.Problems()))
	}

	// The root goes missing again between checks: a second recreation is a new alert.
	if err := os.Rename(root, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(ark.CheckInterval)
	if got := <-results; !reflect.DeepEqual(got, []string{doctor.ARK020}) {
		t.Fatalf("third check problems = %v", got)
	}
	if m.Err() != nil || m.Root().State != ark.StateReady {
		t.Fatalf("err %v, root %+v", m.Err(), m.Root())
	}

	cancel()
	<-done
	if clock.Tickers() != 0 {
		t.Fatalf("ticker still running after Run returned: %d", clock.Tickers())
	}
}

func TestMonitorCheckKeepsAnErrorAndTheReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	fsys := newFS()
	fsys.mkdirErr[filepath.Join(root, "docs")] = &os.PathError{Op: "mkdir", Err: syscall.EROFS}
	m := ark.NewMonitor(fsys, internalOpts(root), fake.NewClock(epoch), nil)
	if _, _, err := m.Check(); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(m.Err(), syscall.EROFS) || !m.Root().ReadOnly {
		t.Fatalf("Err %v, Root %+v", m.Err(), m.Root())
	}
	delete(fsys.mkdirErr, filepath.Join(root, "docs"))
	if _, _, err := m.Check(); err != nil || m.Err() != nil || m.Root().ReadOnly {
		t.Fatalf("after the fault is gone: err %v, Err %v, Root %+v", err, m.Err(), m.Root())
	}
}
