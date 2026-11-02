package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

const (
	verOld   = "2026.10.20-1"
	verKit   = "2026.10.25-1"
	verDraft = "2026.11.03-1"
	verDraf2 = "2026.11.04-1"
)

// liveCatalog installs a valid catalog with version as the node's current one.
func liveCatalog(t *testing.T, e Env, version string) {
	t.Helper()
	writeCatalog(t, e.Paths.CatalogDir(), version)
}

// kitCatalog puts a valid catalog in <root>/kit/catalog.
func kitCatalog(t *testing.T, root, version string) string {
	t.Helper()
	return writeCatalog(t, filepath.Join(root, "kit", "catalog"), version)
}

func liveVersion(t *testing.T, e Env) string {
	t.Helper()
	return strings.TrimSpace(read(t, filepath.Join(e.Paths.CatalogDir(), "CATALOG_VERSION")))
}

func TestRestoreCatalogFromTheInternalKit(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	// the live catalog is damaged: this is the LAI-CAT-001 case
	write(t, filepath.Join(e.Paths.CatalogDir(), "models.yaml"), "models: [unclosed", 0o640)
	kitCatalog(t, e.Paths.InternalRoot, verKit)

	res, err := RestoreCatalog(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if res.From != FromKit || res.Version != verKit || res.Root == nil || res.Root.Kind != KindInternal {
		t.Fatalf("result %+v", res)
	}
	if liveVersion(t, e) != verKit {
		t.Errorf("live version %q", liveVersion(t, e))
	}
	if rep := catalog.Lint(e.Paths.CatalogDir(), catalog.LintOptions{}); !rep.Valid() {
		t.Errorf("restored catalog does not lint: %v", rep.Problems)
	}
	// the replaced (damaged) catalog is kept as catalog.prev
	if !strings.Contains(read(t, filepath.Join(e.Paths.CatalogPrev(), "models.yaml")), "unclosed") {
		t.Error("catalog.prev is not the replaced catalog")
	}
	// modes and ownership: root:localai, 0750 directories, 0640 files
	for _, f := range listFiles(t, e.Paths.CatalogDir()) {
		p := filepath.Join(e.Paths.CatalogDir(), f)
		if m := modeOf(t, p); m != 0o640 {
			t.Errorf("%s mode %o, want 0640", f, m)
		}
		if uid, gid := ownerOf(mustStat(t, p)); uid != e.RootUID || gid != os.Getgid() {
			t.Errorf("%s owner %d:%d", f, uid, gid)
		}
	}
	for _, d := range []string{e.Paths.CatalogDir(), filepath.Join(e.Paths.CatalogDir(), "profiles")} {
		if m := modeOf(t, d); m != 0o750 {
			t.Errorf("%s mode %o, want 0750", d, m)
		}
	}
	if exists(e.Paths.CatalogDir() + ".new") {
		t.Error("catalog.new left behind")
	}
}

func TestRestoreCatalogWithNothingInstalledYet(t *testing.T) {
	e, _ := testEnv(t)
	kitCatalog(t, e.Paths.InternalRoot, verKit)
	res, err := RestoreCatalog(context.Background(), e)
	if err != nil || res.Version != verKit || res.Previous != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if exists(e.Paths.CatalogPrev()) {
		t.Error("a catalog.prev appeared from nothing")
	}
}

func TestRestoreCatalogWithoutAKitIsNotFound(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	before := read(t, filepath.Join(e.Paths.CatalogDir(), "models.yaml"))
	if _, err := RestoreCatalog(context.Background(), e); !errors.Is(err, ErrNoKit) {
		t.Fatalf("got %v, want ErrNoKit", err)
	}
	if read(t, filepath.Join(e.Paths.CatalogDir(), "models.yaml")) != before || exists(e.Paths.CatalogPrev()) {
		t.Error("the live catalog was touched")
	}
}

func TestRestoreCatalogLintFailureLeavesTheLiveCatalogUntouched(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	kit := kitCatalog(t, e.Paths.InternalRoot, verKit)
	write(t, filepath.Join(kit, "CATALOG_VERSION"), "main\n", 0o644) // fails lint
	before := snapshot(t, e.Paths.CatalogDir())

	_, err := RestoreCatalog(context.Background(), e)
	var rej *catalog.RejectedError
	if !errors.As(err, &rej) || rej.Code() != catalog.CodeRejected {
		t.Fatalf("got %v, want the LAI-CAT-002 rejection", err)
	}
	if len(rej.Report.Problems) == 0 {
		t.Error("no problems reported")
	}
	after := snapshot(t, e.Paths.CatalogDir())
	if len(before) != len(after) {
		t.Fatal("the live catalog changed")
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed", k)
		}
	}
	if exists(e.Paths.CatalogDir()+".new") || exists(e.Paths.CatalogPrev()) {
		t.Error("catalog.new or catalog.prev left behind")
	}
}

func TestRestoreCatalogSkipsADamagedKitForAnIntactOne(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	bad := kitCatalog(t, e.Paths.InternalRoot, verKit)
	write(t, filepath.Join(bad, "models.yaml"), "not: [valid", 0o644)
	drive := addDrive(t, e, "a", false)
	kitCatalog(t, drive.Path, verDraft)

	res, err := RestoreCatalog(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != verDraft || res.Root == nil || res.Root.Kind != KindDrive || res.Root.Mount != drive.Mount {
		t.Fatalf("%+v", res)
	}
}

func TestRestoreCatalogPrefersTheInternalRootThenDrivesByMountPath(t *testing.T) {
	e, _ := testEnv(t)
	d2 := addDrive(t, e, "b", false)
	d1 := addDrive(t, e, "a", false)
	kitCatalog(t, d2.Path, verDraft)
	kitCatalog(t, d1.Path, verKit)
	srcs := RestoreSources(context.Background(), e)
	if len(srcs) != 2 || srcs[0].Root.Mount != d1.Mount || srcs[1].Root.Mount != d2.Mount {
		t.Fatalf("order %+v", srcs)
	}
	kitCatalog(t, e.Paths.InternalRoot, verOld)
	srcs = RestoreSources(context.Background(), e)
	if len(srcs) != 3 || srcs[0].Root.Kind != KindInternal {
		t.Fatalf("internal root is not first: %+v", srcs)
	}
}

func TestUseTestThenRestorePutsThePreviousCatalogBack(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	kitCatalog(t, e.Paths.InternalRoot, verKit) // a kit copy that must NOT be used
	draft := writeCatalog(t, t.TempDir(), verDraft)

	res, err := UseTestCatalog(e, draft)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != verDraft || res.Previous != verOld {
		t.Fatalf("%+v", res)
	}
	live := e.Paths.CatalogDir()
	if liveVersion(t, e) != verDraft || !catalog.IsTestCatalog(live) {
		t.Fatal("the draft is not live with a TEST marker")
	}
	if m := modeOf(t, filepath.Join(live, "TEST")); m != 0o640 {
		t.Errorf("TEST mode %o", m)
	}
	if v := strings.TrimSpace(read(t, filepath.Join(e.Paths.CatalogPrev(), "CATALOG_VERSION"))); v != verOld {
		t.Errorf("catalog.prev is %q, want %q", v, verOld)
	}

	back, err := RestoreCatalog(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if back.From != FromPrevious || back.Version != verOld || liveVersion(t, e) != verOld {
		t.Fatalf("restore after use --test: %+v, live %q", back, liveVersion(t, e))
	}
	if catalog.IsTestCatalog(live) {
		t.Error("the TEST marker survived the restore")
	}
	if exists(e.Paths.CatalogPrev()) {
		t.Error("catalog.prev must be removed after the swap")
	}
}

func TestUseTestTwiceKeepsTheRealCatalogInPrev(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	if _, err := UseTestCatalog(e, writeCatalog(t, t.TempDir(), verDraft)); err != nil {
		t.Fatal(err)
	}
	if _, err := UseTestCatalog(e, writeCatalog(t, t.TempDir(), verDraf2)); err != nil {
		t.Fatal(err)
	}
	if liveVersion(t, e) != verDraf2 {
		t.Errorf("live %q", liveVersion(t, e))
	}
	if v := strings.TrimSpace(read(t, filepath.Join(e.Paths.CatalogPrev(), "CATALOG_VERSION"))); v != verOld {
		t.Fatalf("the real catalog was lost: catalog.prev is %q", v)
	}
	if _, err := RestoreCatalog(context.Background(), e); err != nil || liveVersion(t, e) != verOld {
		t.Fatalf("restore: %v, live %q", err, liveVersion(t, e))
	}
}

func TestUseTestRefusesAFailingLintAndChangesNothing(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	draft := writeCatalog(t, t.TempDir(), verDraft)
	write(t, filepath.Join(draft, "ark.yaml"), "schema_version: 1\ncontent: [", 0o644)
	_, err := UseTestCatalog(e, draft)
	var rej *catalog.RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("got %v", err)
	}
	if liveVersion(t, e) != verOld || exists(e.Paths.CatalogPrev()) || catalog.IsTestCatalog(e.Paths.CatalogDir()) {
		t.Error("the live catalog was touched")
	}
}

func TestRestoreIgnoresACatalogPrevThatIsNotFromUseTest(t *testing.T) {
	// catalog.prev also exists after a normal refresh; without a TEST marker on the live catalog it
	// is not what restore puts back.
	e, _ := testEnv(t)
	liveCatalog(t, e, verKit)
	writeCatalog(t, e.Paths.CatalogPrev(), verOld)
	kitCatalog(t, e.Paths.InternalRoot, verDraft)
	res, err := RestoreCatalog(context.Background(), e)
	if err != nil || res.From != FromKit || res.Version != verDraft {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCatalogCommandsNeedTheServiceGroup(t *testing.T) {
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	kitCatalog(t, e.Paths.InternalRoot, verKit)
	e.LookupGroup = func(string) (int, error) { return -1, errors.New("no such group") }
	var ge *GroupError
	if _, err := RestoreCatalog(context.Background(), e); !errors.As(err, &ge) || ge.Name != "localai" {
		t.Errorf("restore: %v", err)
	}
	if _, err := UseTestCatalog(e, writeCatalog(t, t.TempDir(), verDraft)); !errors.As(err, &ge) {
		t.Errorf("use: %v", err)
	}
	if liveVersion(t, e) != verOld {
		t.Error("the catalog changed")
	}
}

func TestRestoreCatalogFailedCopyLeavesTheLiveCatalogUntouched(t *testing.T) {
	// A source the copy cannot take (a symlink is never followed) is an interrupted install: the live
	// catalog stays as it was and no catalog.new is left behind.
	e, _ := testEnv(t)
	liveCatalog(t, e, verOld)
	before := snapshot(t, e.Paths.CatalogDir())
	kit := kitCatalog(t, e.Paths.InternalRoot, verKit)
	if err := os.Symlink(filepath.Join(kit, "models.yaml"), filepath.Join(kit, "extra.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreCatalog(context.Background(), e); err == nil {
		t.Fatal("no error for a source with a symlink")
	}
	after := snapshot(t, e.Paths.CatalogDir())
	if len(after) != len(before) || liveVersion(t, e) != verOld {
		t.Error("the live catalog changed")
	}
	if exists(e.Paths.CatalogDir()+".new") || exists(e.Paths.CatalogPrev()) {
		t.Error("catalog.new or catalog.prev left behind")
	}
}

// snapshot reads every file under dir into a map.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range listFiles(t, dir) {
		out[f] = read(t, filepath.Join(dir, f))
	}
	return out
}
