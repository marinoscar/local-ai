package catalog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	v1 = "2026.11.02-1"
	v2 = "2026.11.03-1"
)

// versionedDir is a valid catalog with the given CATALOG_VERSION.
func versionedDir(t *testing.T, version string) string {
	t.Helper()
	dir := validDir(t)
	writeFile(t, dir, FileVersion, version+"\n")
	return dir
}

// snapshot reads every file under dir into a map, so a test can prove a directory is byte-identical.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		out[rel] = string(data)
		return err
	})
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var errCrash = errors.New("simulated crash")

// faultFS wraps a real FS and fails at the failAt-th operation. In crash mode every later operation
// fails too (the process is dead, so no rollback code runs); otherwise only that one operation fails.
// A failing WriteFile leaves half of the file behind, like a torn write.
type faultFS struct {
	inner  FS
	failAt int
	crash  bool
	only   string // when set, count and fail only operations of this name
	ops    int
	dead   bool
}

func (f *faultFS) step(name string) error {
	if f.dead {
		return errCrash
	}
	if f.only != "" && f.only != name {
		return nil
	}
	f.ops++
	if f.failAt > 0 && f.ops == f.failAt {
		f.dead = f.crash
		return errCrash
	}
	return nil
}

func (f *faultFS) MkdirAll(p string, perm os.FileMode) error {
	if err := f.step("MkdirAll"); err != nil {
		return err
	}
	return f.inner.MkdirAll(p, perm)
}

func (f *faultFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := f.step("WriteFile"); err != nil {
		_ = os.WriteFile(name, data[:len(data)/2], perm)
		return err
	}
	return f.inner.WriteFile(name, data, perm)
}

func (f *faultFS) Rename(a, b string) error {
	if err := f.step("Rename"); err != nil {
		return err
	}
	return f.inner.Rename(a, b)
}

func (f *faultFS) RemoveAll(p string) error {
	if err := f.step("RemoveAll"); err != nil {
		return err
	}
	return f.inner.RemoveAll(p)
}

func (f *faultFS) SyncDir(d string) error {
	if err := f.step("SyncDir"); err != nil {
		return err
	}
	return f.inner.SyncDir(d)
}

func (f *faultFS) Stat(n string) (os.FileInfo, error) {
	if f.dead {
		return nil, errCrash
	}
	return f.inner.Stat(n)
}

func liveDir(t *testing.T) string { return filepath.Join(t.TempDir(), "etc-localai", "catalog") }

func mustInstall(t *testing.T, live, src string, opts InstallOptions) *InstallResult {
	t.Helper()
	res, err := Install(live, src, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func versionOf(t *testing.T, dir string) string {
	t.Helper()
	cat, err := Load(dir)
	if err != nil {
		t.Fatalf("%s does not load: %v", dir, err)
	}
	return cat.Version
}

func TestInstallFresh(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	res := mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	if res.Version != v1 || res.Previous != "" {
		t.Errorf("%+v", res)
	}
	if versionOf(t, live) != v1 {
		t.Error("not installed")
	}
	if exists(live+".new") || exists(live+".prev") {
		t.Error("leftovers after a first install")
	}
}

func TestInstallReplacesAndKeepsPrevious(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	res := mustInstall(t, live, versionedDir(t, v2), InstallOptions{})
	if res.Version != v2 || res.Previous != v1 {
		t.Errorf("%+v", res)
	}
	if versionOf(t, live) != v2 || versionOf(t, live+".prev") != v1 {
		t.Errorf("live %s prev %s", versionOf(t, live), versionOf(t, live+".prev"))
	}
	if exists(live + ".new") {
		t.Error("catalog.new left behind")
	}
	// the installed copy is byte-identical to the source
	if !reflect.DeepEqual(snapshot(t, live), snapshot(t, versionedDir(t, v2))) {
		t.Error("installed files differ from the source")
	}
}

// catalog.md section 9: a rejected catalog leaves /etc/localai/catalog/ byte-identical (LAI-CAT-002).
func TestInstallRejectedLeavesTheLiveCatalogUntouched(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, "2026.11.01-1"), InstallOptions{})
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	liveBefore, prevBefore := snapshot(t, live), snapshot(t, live+".prev")

	bad := versionedDir(t, v2)
	applyEdits(t, bad, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	res, err := Install(live, bad, InstallOptions{})
	if res != nil || !errors.Is(err, ErrRejected) {
		t.Fatalf("res %v err %v", res, err)
	}
	var re *RejectedError
	if !errors.As(err, &re) || re.Code() != CodeRejected || !has(re.Report, CodePinning, fM, "models[1].source.revision") {
		t.Fatalf("rejection: %v", err)
	}
	if !reflect.DeepEqual(snapshot(t, live), liveBefore) || !reflect.DeepEqual(snapshot(t, live+".prev"), prevBefore) {
		t.Error("a rejected install changed the live catalog or its previous copy")
	}
	if exists(live + ".new") {
		t.Error("catalog.new left behind")
	}
}

func TestInstallRejectedWithNothingInstalledYet(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Install(live, t.TempDir(), InstallOptions{}) // an empty directory is not a catalog
	if !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	if exists(live) || exists(live+".new") {
		t.Error("created something for a rejected catalog")
	}
}

func TestInstallTestMarker(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	mustInstall(t, live, versionedDir(t, v2), InstallOptions{Test: true})
	if !IsTestCatalog(live) || versionOf(t, live) != v2 || versionOf(t, live+".prev") != v1 {
		t.Fatal("test catalog not installed as a draft next to the previous one")
	}
	// a marker in the source is never copied; a normal install ends the test
	src := versionedDir(t, "2026.11.04-1")
	writeFile(t, src, FileTestMarker, "x")
	mustInstall(t, live, src, InstallOptions{})
	if IsTestCatalog(live) {
		t.Error("a normal install kept the TEST marker")
	}
}

func TestInstallKeepPreviousKeepsTheRealCatalogUnderASecondDraft(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	mustInstall(t, live, versionedDir(t, v2), InstallOptions{Test: true})
	v3 := "2026.11.05-1"
	mustInstall(t, live, versionedDir(t, v3), InstallOptions{Test: true, KeepPrevious: true})
	if versionOf(t, live) != v3 || !IsTestCatalog(live) {
		t.Fatalf("live is %q (test=%v), want the second draft", versionOf(t, live), IsTestCatalog(live))
	}
	if versionOf(t, live+".prev") != v1 || IsTestCatalog(live+".prev") {
		t.Errorf(".prev is %q, want the real catalog %q kept", versionOf(t, live+".prev"), v1)
	}
	if _, err := os.Stat(live + ".discard"); err == nil {
		t.Error("the replaced draft was left behind")
	}
}

func TestInstallKeepPreviousWithoutAPreviousBehavesAsUsual(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{Test: true})
	mustInstall(t, live, versionedDir(t, v2), InstallOptions{Test: true, KeepPrevious: true})
	if versionOf(t, live) != v2 || versionOf(t, live+".prev") != v1 {
		t.Errorf("live %q, prev %q: the old catalog should have become .prev", versionOf(t, live), versionOf(t, live+".prev"))
	}
}

func TestInstallCopiesExtraFilesButNotSymlinks(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	src := versionedDir(t, v1)
	writeFile(t, src, "pins.json", "{}")
	mustInstall(t, live, src, InstallOptions{})
	if readFile(t, live, "pins.json") != "{}" {
		t.Error("pins.json (written by the repo_docs resolver) was not carried over")
	}

	bad := versionedDir(t, v2)
	if err := os.Symlink("/etc/hostname", filepath.Join(bad, "pins.json")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	before := snapshot(t, live)
	if _, err := Install(live, bad, InstallOptions{}); err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("a symlink in the source must be an install error, got %v", err)
	}
	if !reflect.DeepEqual(snapshot(t, live), before) || exists(live+".new") {
		t.Error("a failed copy changed the live catalog or left catalog.new behind")
	}
}

// catalog.md section 9: an interrupted install (killed between write and rename) leaves the
// previous catalog. This kills the install at every single filesystem operation, then runs the
// recovery the agent runs at start and demands a loadable catalog that is the old or the new one.
func TestInstallCrashAtEveryStep(t *testing.T) {
	crashPoints := 0
	for k := 1; k < 500; k++ {
		live := liveDir(t)
		if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			t.Fatal(err)
		}
		mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
		ffs := &faultFS{inner: OSFS{}, failAt: k, crash: true}
		_, err := Install(live, versionedDir(t, v2), InstallOptions{FS: ffs})

		// the machine restarts: nothing but Recover and Load run
		if _, rerr := Recover(OSFS{}, live); rerr != nil {
			t.Fatalf("k=%d: recover: %v", k, rerr)
		}
		cat, lerr := Load(live)
		if lerr != nil {
			t.Fatalf("k=%d: no usable catalog after a crash: %v", k, lerr)
		}
		if cat.Version != v1 && cat.Version != v2 {
			t.Fatalf("k=%d: version %q is neither the old nor the new one", k, cat.Version)
		}
		if err == nil && cat.Version != v2 {
			t.Fatalf("k=%d: install reported success but the version is %s", k, cat.Version)
		}
		if exists(live + ".new") {
			t.Fatalf("k=%d: catalog.new survived recovery", k)
		}
		if !ffs.dead {
			if err != nil {
				t.Fatalf("k=%d: no fault was injected but Install failed: %v", k, err)
			}
			break // every operation has now been the crash point once
		}
		crashPoints++
	}
	if crashPoints < 10 {
		t.Errorf("only %d crash points exercised", crashPoints)
	}
}

// A failure that is not a crash: the install rolls back by itself.
func TestInstallRollsBackWhenTheFinalRenameFails(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	ffs := &faultFS{inner: OSFS{}, failAt: 2, only: "Rename"} // 1: live -> prev, 2: new -> live
	if _, err := Install(live, versionedDir(t, v2), InstallOptions{FS: ffs}); err == nil {
		t.Fatal("expected an error")
	}
	if versionOf(t, live) != v1 {
		t.Error("the previous catalog was not put back")
	}
	if exists(live + ".new") {
		t.Error("catalog.new left behind")
	}
}

func TestInstallFirstRenameFailureChangesNothing(t *testing.T) {
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, live, versionedDir(t, v1), InstallOptions{})
	before := snapshot(t, live)
	ffs := &faultFS{inner: OSFS{}, failAt: 1, only: "Rename"}
	if _, err := Install(live, versionedDir(t, v2), InstallOptions{FS: ffs}); err == nil {
		t.Fatal("expected an error")
	}
	if !reflect.DeepEqual(snapshot(t, live), before) || exists(live+".new") {
		t.Error("live catalog changed")
	}
}

func TestRecover(t *testing.T) {
	t.Run("restores the previous catalog when the live one is missing", func(t *testing.T) {
		live := liveDir(t)
		if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			t.Fatal(err)
		}
		copyTree(t, versionedDir(t, v1), live+".prev")
		copyTree(t, versionedDir(t, v2), live+".new")
		ok, err := Recover(nil, live)
		if err != nil || !ok {
			t.Fatalf("restored %v err %v", ok, err)
		}
		if versionOf(t, live) != v1 || exists(live+".new") || exists(live+".prev") {
			t.Error("recovery result wrong")
		}
	})
	t.Run("leaves a present live catalog alone and removes a stale catalog.new", func(t *testing.T) {
		live := liveDir(t)
		if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			t.Fatal(err)
		}
		copyTree(t, versionedDir(t, v2), live)
		copyTree(t, versionedDir(t, v1), live+".prev")
		copyTree(t, versionedDir(t, v1), live+".new")
		ok, err := Recover(OSFS{}, live)
		if err != nil || ok {
			t.Fatalf("restored %v err %v", ok, err)
		}
		if versionOf(t, live) != v2 || versionOf(t, live+".prev") != v1 || exists(live+".new") {
			t.Error("recovery touched the wrong directories")
		}
	})
	t.Run("nothing to do", func(t *testing.T) {
		if ok, err := Recover(OSFS{}, liveDir(t)); ok || err != nil {
			t.Errorf("%v %v", ok, err)
		}
	})
}
