package host

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
	"github.com/marinoscar/local-ai/agent/internal/router/migrate"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// openDir is a temporary directory that other users can walk through (the generators check that the
// path to a model file is world-readable, and t.TempDir is 0700), removed at the end of the test.
func openDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "migrate-host-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

var shaToken = regexp.MustCompile(`@sha:([0-9a-f])@`)

// copyCatalog copies the catalog fixture of internal/catalog into dir, expanding its @sha:N@ tokens
// the way that package's tests do (a digest-shaped literal never sits in the source).
func copyCatalog(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join("..", "..", "..", "catalog", "testdata", "valid")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		data = shaToken.ReplaceAllFunc(data, func(m []byte) []byte { return []byte(strings.Repeat(string(m[5:6]), 64)) })
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

type reloadManager struct {
	services.Manager
	reloads int
}

func (m *reloadManager) DaemonReload(context.Context) error { m.reloads++; return nil }

// TestRendererRunsTheRealGeneratorsAndWritesWhatTheMigrationChecks runs the preset generator and the unit
// generator of internal/router exactly as the migration wires them, in a temporary root, and proves that
// the models.ini they write has the section, the alias and load-on-startup that step M4 demands.
func TestRendererRunsTheRealGeneratorsAndWritesWhatTheMigrationChecks(t *testing.T) {
	root := openDir(t)
	etc := filepath.Join(root, "etc", "localai")
	paths := local.Paths{EtcDir: etc, StateDir: filepath.Join(root, "var", "lib", "localai"), InternalRoot: filepath.Join(root, "srv", "localai"),
		SystemdDir: filepath.Join(root, "etc", "systemd", "system")}
	for _, d := range []string{paths.CatalogDir(), paths.StateDir, paths.SystemdDir, filepath.Join(paths.InternalRoot, "models", "model-alpha-9b-q4")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyCatalog(t, paths.CatalogDir())
	nodeYAML := "schema_version: 1\nnode:\n  id: ai-n3\nprofile: small-8gb\nllama:\n  backend: cuda\n  legacy_aliases:\n    model-alpha-9b-q4: [legacy-name]\n"
	if err := os.WriteFile(paths.NodeYAML(), []byte(nodeYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.InternalRoot, "models", "model-alpha-9b-q4")
	if err := os.WriteFile(filepath.Join(dir, "Model-Alpha-9B-Q4_K_M.gguf"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: "model-alpha-9b-q4", ItemKind: manifest.KindModel, Engine: "llama", CatalogVersion: "x", Version: "adopted",
		SortKey: now.Format(time.RFC3339), State: manifest.StateVerified, Source: manifest.Source{Type: "adopted"},
		Files:        []manifest.File{{Kind: "weights", Path: "Model-Alpha-9B-Q4_K_M.gguf", SizeBytes: 7, SHA256: strings.Repeat("1", 64), SHA256Source: "adopted"}},
		DownloadedAt: now, VerifiedAt: now, LastReverifiedAt: now, Trust: manifest.TrustPinned, TotalBytes: 7,
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}

	cfg := config.Open(config.Options{Path: paths.NodeYAML()})
	st, err := state.Open(paths.StateFile(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := (State{Store: st}).SetDefaultModel("model-alpha-9b-q4"); err != nil {
		t.Fatal(err)
	}
	sd := &reloadManager{}
	r := &Renderer{
		Root: root, Paths: paths, Config: cfg, State: st, Systemd: sd, Version: "test",
		Hardware: func(context.Context) hardware.Snapshot {
			return hardware.Snapshot{Backend: hardware.BackendCUDA, RAM: hardware.RAM{TotalMiB: 14336, AvailableMiB: 11000},
				GPUs: []hardware.GPU{{Vendor: hardware.VendorNVIDIA, Backend: hardware.BackendCUDA, VRAMTotalMiB: 8192}}}
		},
	}
	res, err := r.Render(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ini, err := os.ReadFile(res.INIPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(ini)
	for _, want := range []string{"[model-alpha-9b-q4]", "alias = legacy-name", "load-on-startup = true", "model = " + filepath.Join(dir, "Model-Alpha-9B-Q4_K_M.gguf")} {
		if !strings.Contains(text, want) {
			t.Errorf("models.ini lacks %q:\n%s", want, text)
		}
	}
	unit, err := os.ReadFile(res.UnitPath)
	if err != nil || !strings.Contains(string(unit), "--models-max 1") {
		t.Fatalf("the unit: %v\n%s", err, unit)
	}
	if sd.reloads == 0 {
		t.Error("daemon-reload did not run after the unit was written")
	}
	// Nothing was enabled or started: the renderer has no way to (the Manager only reloads).
	// A second render is a no-op on the disk.
	again, err := r.Render(context.Background())
	if err != nil || again.INIPath != res.INIPath {
		t.Fatalf("%v %+v", err, again)
	}
	ini2, _ := os.ReadFile(res.INIPath)
	if string(ini2) != text {
		t.Error("a second render changed models.ini")
	}
}

func TestStateAdapterRecordsTheMigration(t *testing.T) {
	dir := openDir(t)
	st, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := State{Store: st}
	if _, ok := s.Migration(); ok {
		t.Fatal("a fresh state has no migration")
	}
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if err := s.SetMigration(state.MigrationEntry{State: migrate.StateInProgress, Step: "M3", At: at}); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Migration(); !ok || e.State != migrate.StateInProgress || e.Step != "M3" || !e.At.Equal(at) {
		t.Fatalf("%+v %v", e, ok)
	}
	if err := s.SetDefaultModel("model-one"); err != nil {
		t.Fatal(err)
	}
	if d := st.Snapshot().Models.Default; d == nil || *d != "model-one" {
		t.Fatalf("default = %v", d)
	}
	if err := s.RecordBuild("f3586e66b-cuda"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBuild("aaaaaaaaa-cuda"); err != nil {
		t.Fatal(err)
	}
	if l := st.Snapshot().Llama; l.Build != "aaaaaaaaa-cuda" || l.PreviousBuild == nil || *l.PreviousBuild != "f3586e66b-cuda" {
		t.Fatalf("llama = %+v", l)
	}
	if s.Connectivity() != "" {
		t.Fatalf("connectivity = %q", s.Connectivity())
	}
	// The record survives a reopen: it is on disk, not only in memory.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := state.Open(filepath.Join(dir, "state.json"), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if e, ok := (State{Store: st2}).Migration(); !ok || e.Step != "M3" {
		t.Fatalf("after a reopen: %+v %v", e, ok)
	}
}

func TestBuilderMapsTheBuildManagersErrorsToDoctorCodes(t *testing.T) {
	// A manager whose programs cannot be found: the prerequisites check fails with LAI-LLM-018 and the
	// code survives the interface.
	mgr := build.New(build.Options{
		Root: openDir(t), Exec: execx.Exec{}, Lookup: func(string) (string, error) { return "", execx.ErrNotFound },
	})
	b := Builder{M: mgr}
	err := b.CanBuild(context.Background(), "cuda")
	if err == nil || migrate.CodeOf(err) != doctor.LLM018 {
		t.Fatalf("err = %v code %q", err, migrate.CodeOf(err))
	}
	var coded interface{ Code() string }
	if !errors.As(err, &coded) {
		t.Fatal("the error does not carry a code")
	}
	// A build that cannot start (no source anywhere, offline) returns a coded error too.
	_, err = b.Build(context.Background(), migrate.BuildRequest{Commit: "f3586e66b", Backend: "cpu", SourceRepo: filepath.Join(openDir(t), "no-such-checkout")})
	if err == nil || migrate.CodeOf(err) == "" {
		t.Fatalf("err = %v code %q", err, migrate.CodeOf(err))
	}
	if fromBuildError(nil) != nil {
		t.Fatal("no error must stay no error")
	}
}

func TestAssembleWiresEveryPort(t *testing.T) {
	root := openDir(t)
	env := local.DefaultEnv("test")
	env.Paths = local.Paths{EtcDir: filepath.Join(root, "etc"), StateDir: filepath.Join(root, "var"), InternalRoot: filepath.Join(root, "srv"),
		SystemdDir: filepath.Join(root, "systemd"), Mountinfo: filepath.Join(root, "mountinfo")}
	if err := os.MkdirAll(env.Paths.EtcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.Paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := Assemble(context.Background(), env, "test", true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	o := m.Options
	for name, ok := range map[string]bool{
		"FS": o.FS != nil, "Exec": o.Exec != nil, "Systemd": o.Systemd != nil, "Agent": o.Agent != nil, "Users": o.Users != nil,
		"Probe": o.Probe != nil, "Builder": o.Builder != nil, "Config": o.Config != nil, "State": o.State != nil,
		"Renderer": o.Renderer != nil, "Clock": o.Clock != nil, "Audit": o.Audit != nil,
	} {
		if !ok {
			t.Errorf("%s is not wired", name)
		}
	}
	if !o.AcceptCatalogFlags || o.RestoreFiles || o.Paths.InternalRoot != env.Paths.InternalRoot || o.Paths.NodeYAML != env.Paths.NodeYAML() {
		t.Errorf("options: %+v", o)
	}
	// The probe asks the new key file first and the old one second.
	p, ok := o.Probe.(*Probe)
	if !ok || len(p.KeyFiles) != 2 || p.KeyFiles[0] != o.Paths.NewKey || p.KeyFiles[1] != o.Paths.LegacyKey {
		t.Errorf("probe: %+v", o.Probe)
	}
	// The engine can be built on it.
	_ = migrate.New(o)
}
