package config_test

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file (run with -update to accept):\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func openStore(t testing.TB, content string) (*config.Store, string) {
	t.Helper()
	root, path := nodeRoot(t, content)
	return config.Open(config.Options{Root: root, Hooks: *catalogHooks()}), path
}

func TestOpenValidFile(t *testing.T) {
	s, path := openStore(t, example(t))
	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
	st := s.Status()
	if st.Code != "" || st.Severity != "" || st.Safe || len(st.Problems) != 0 || len(st.Pending) != 0 {
		t.Errorf("a valid file is healthy: %+v", st)
	}
	cfg := s.Get()
	if cfg.Node.ID != "ai-n3" || cfg.SafeDefaults || !cfg.CloudEnabled() || !cfg.Tailscale.Manage {
		t.Errorf("config not loaded: %+v", cfg)
	}
}

func TestMissingFileStartsOnSafeDefaultsWithAGT001(t *testing.T) {
	s, _ := openStore(t, "")
	st := s.Status()
	if st.Code != config.CodeNodeYAMLMissing || st.Severity != config.SeverityError || !st.Safe {
		t.Fatalf("status = %+v, want %s on safe defaults", st, config.CodeNodeYAMLMissing)
	}
	cfg := s.Get()
	want := config.SafeDefaults()
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("safe defaults differ:\n got  %+v\n want %+v", cfg, want)
	}
	if cfg.CloudEnabled() || cfg.Tailscale.Manage || cfg.Node.ID != "unconfigured" || cfg.Ports.Agent != 7070 {
		t.Errorf("safe defaults must mean no cloud, no tailscale management, canonical ports, id unconfigured: %+v", cfg)
	}
}

func TestMissingDirectoryIsAlsoMissingFile(t *testing.T) {
	s := config.Open(config.Options{Root: t.TempDir()})
	if st := s.Status(); st.Code != config.CodeNodeYAMLMissing || !st.Safe {
		t.Errorf("status = %+v", st)
	}
}

func TestInvalidFileAtStartUsesSafeDefaultsWithAGT002(t *testing.T) {
	in := swap(t, example(t), "mode: router ", "mode: both   ")
	s, _ := openStore(t, in)
	st := s.Status()
	if st.Code != config.CodeNodeYAMLInvalid || st.Severity != config.SeverityError || !st.Safe {
		t.Fatalf("status = %+v", st)
	}
	if p, ok := findProblem(st.Problems, "llama.mode"); !ok || p.Line == 0 {
		t.Errorf("the problem must name the path and the line: %+v", st.Problems)
	}
	if cfg := s.Get(); !cfg.SafeDefaults || cfg.CloudEnabled() {
		t.Errorf("an invalid file at start means safe defaults: %+v", cfg)
	}
}

func TestSyntaxErrorAtStartUsesSafeDefaults(t *testing.T) {
	s, _ := openStore(t, "schema_version: 1\nnode: [")
	if st := s.Status(); st.Code != config.CodeNodeYAMLInvalid || !st.Safe {
		t.Errorf("status = %+v", st)
	}
}

func TestUnreadableNodeYAMLIsInvalidNotFatal(t *testing.T) {
	root := t.TempDir()
	path := config.NodeYAMLPath(root)
	if err := os.MkdirAll(path, 0o755); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	s := config.Open(config.Options{Root: root})
	if st := s.Status(); st.Code != config.CodeNodeYAMLInvalid || !st.Safe || len(st.Problems) != 1 {
		t.Errorf("status = %+v", st)
	}
}

func TestOpenWithUnknownTopLevelKeyWarnsAndLoads(t *testing.T) {
	s, _ := openStore(t, example(t)+"\nfuture_feature: 1\n")
	st := s.Status()
	if st.Code != config.CodeNodeYAMLInvalid || st.Severity != config.SeverityWarning || st.Safe {
		t.Fatalf("status = %+v, want a LAI-AGT-002 warning on a loaded config", st)
	}
	if got := s.Get().Node.ID; got != "ai-n3" {
		t.Errorf("the config must load: node.id = %q", got)
	}
}

func TestProfileMissingFromTheCatalogLoadsWithTheCatalogCode(t *testing.T) {
	s, _ := openStore(t, swap(t, example(t), "profile: small-8gb ", "profile: no-such-pr"))
	st := s.Status()
	if st.Code != config.CodeProfileMissing || st.Severity != config.SeverityWarning || st.Safe {
		t.Errorf("status = %+v", st)
	}
	if s.Get().Profile != "no-such-pr" {
		t.Error("the config must load")
	}
}

func TestOpenSweepsTempFilesOfAnInterruptedWrite(t *testing.T) {
	root, path := nodeRoot(t, example(t))
	dir := filepath.Dir(path)
	for _, n := range []string{"node.yaml.tmp-0123456789ab", "node.yaml.bak.tmp-fedcba987654"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config.Open(config.Options{Root: root})
	if names := listDir(t, dir); len(names) != 1 || names[0] != "node.yaml" {
		t.Errorf("start must sweep leftover temp files, got %v", names)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s, _ := openStore(t, example(t))
	c := s.Get()
	c.Cloud.Allow[0] = "changed"
	c.Node.ID = "ai-n99"
	again := s.Get()
	if again.Cloud.Allow[0] == "changed" || again.Node.ID != "ai-n3" {
		t.Error("Get must hand out a snapshot the caller cannot use to change the store")
	}
}

// ---- Update ----

func TestUpdateRoundTripKeepsCommentsAndKeyOrder(t *testing.T) {
	s, path := openStore(t, example(t))
	err := s.Update(func(root *yaml.Node) error {
		return config.SetKey(root, "cpu-only", "profile")
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readString(t, path)
	golden(t, "update-profile.yaml", got)

	// Beyond the golden file: every comment of the original survives.
	if want, have := commentSet(example(t)), commentSet(got); !reflect.DeepEqual(want, have) {
		t.Errorf("comments changed:\n want %q\n have %q", want, have)
	}
	if s.Get().Profile != "cpu-only" {
		t.Error("Update must apply the new config at once")
	}
}

func TestUpdateAddsKeysAtTheEndOfTheirBlockAndKeepsOrder(t *testing.T) {
	s, path := openStore(t, example(t))
	err := s.Update(func(root *yaml.Node) error {
		if err := config.SetKey(root, "es", "ui", "language"); err != nil {
			return err
		}
		if err := config.SetKey(root, 30, "cloud", "poll", "heartbeat_seconds"); err != nil {
			return err
		}
		if err := config.SetKey(root, "4294967296", "llama", "extra_env", "GGML_VK_SUBALLOCATION_BLOCK_SIZE"); err != nil {
			return err
		}
		if err := config.SetKey(root, 1_000_000_000, "profile_overrides", "budget_bytes"); err != nil {
			return err
		}
		return config.SetKey(root, []string{"model.load", "status.refresh"}, "cloud", "allow")
	})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "update-add-keys.yaml", readString(t, path))
	cfg := s.Get()
	if cfg.UI.Language != "es" || cfg.Cloud.Poll.HeartbeatSeconds != 30 ||
		cfg.Llama.ExtraEnv["GGML_VK_SUBALLOCATION_BLOCK_SIZE"] != "4294967296" ||
		cfg.ProfileOverrides.BudgetBytes == nil || *cfg.ProfileOverrides.BudgetBytes != 1_000_000_000 ||
		!reflect.DeepEqual(cfg.Cloud.Allow, []string{"model.load", "status.refresh"}) {
		t.Errorf("config after update wrong: %+v", cfg)
	}
}

func TestUpdateKeepsTheMapKeyOrderOfTheFile(t *testing.T) {
	s, path := openStore(t, example(t))
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "info", "logging", "level") }); err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(readString(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	m := doc.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	want := "schema_version node profile profile_overrides catalog llama tailscale cloud sync kiwix whisper piper maps portal ensure update ui ports logging"
	if strings.Join(keys, " ") != want {
		t.Errorf("key order changed:\n got  %v\n want %s", keys, want)
	}
}

func TestUpdateSavesThePreviousVersionAsBak(t *testing.T) {
	s, path := openStore(t, example(t))
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "es", "ui", "language") }); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, path+".bak"); got != example(t) {
		t.Errorf("node.yaml.bak must hold the previous version byte for byte")
	}
	first := readString(t, path)
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "en", "ui", "language") }); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, path+".bak"); got != first {
		t.Error("each update moves the previous file into .bak")
	}
	if names := listDir(t, filepath.Dir(path)); len(names) != 2 {
		t.Errorf("only node.yaml and node.yaml.bak may exist, got %v", names)
	}
}

func TestUpdateKeepsModeAndOwner(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o600, 0o644} {
		s, path := openStore(t, example(t))
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "es", "ui", "language") }); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, path + ".bak"} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Errorf("%s mode = %04o, want %04o", filepath.Base(p), info.Mode().Perm(), mode)
			}
		}
	}
}

func TestUpdateRefusesAnInvalidResultAndWritesNothing(t *testing.T) {
	s, path := openStore(t, example(t))
	err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, 99, "cloud", "concurrency") })
	var verr *config.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a *ValidationError", err)
	}
	if p, ok := findProblem(verr.Problems, "cloud.concurrency"); !ok || p.Line == 0 {
		t.Errorf("problem must name the path and line: %+v", verr.Problems)
	}
	if got := readString(t, path); got != example(t) {
		t.Error("node.yaml must be untouched")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("no .bak for a refused update: %v", err)
	}
	if s.Get().Cloud.Concurrency != 1 {
		t.Error("the config must be unchanged")
	}
}

func TestUpdateCallbackErrorWritesNothing(t *testing.T) {
	s, path := openStore(t, example(t))
	boom := errors.New("boom")
	if err := s.Update(func(*yaml.Node) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if readString(t, path) != example(t) {
		t.Error("node.yaml must be untouched")
	}
}

func TestUpdateThatChangesNothingDoesNotRewrite(t *testing.T) {
	s, path := openStore(t, example(t))
	// First update normalises the formatting; the second one is then a no-op.
	set := func(root *yaml.Node) error { return config.SetKey(root, "es", "ui", "language") }
	if err := s.Update(set); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	bak := readString(t, path+".bak")
	bump(t, path)
	bumped, _ := os.Stat(path)
	if err := s.Update(set); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(bumped.ModTime()) || before.ModTime().Equal(bumped.ModTime()) {
		t.Error("an update that changes nothing must not rewrite the file")
	}
	if readString(t, path+".bak") != bak {
		t.Error(".bak must not move on a no-op")
	}
}

func TestUpdateFixesAnInvalidFileAndKeepsTheLastGoodBak(t *testing.T) {
	s, path := openStore(t, example(t))
	good := example(t)
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "es", "ui", "language") }); err != nil {
		t.Fatal(err)
	}
	// A human breaks the file; the store keeps the last good config and reports AGT-002.
	broken := swap(t, readString(t, path), "mode: router ", "mode: both   ")
	writeNode(t, path, broken)
	bump(t, path)
	s.CheckNow()
	if s.Status().Code != config.CodeNodeYAMLInvalid {
		t.Fatalf("status = %+v", s.Status())
	}
	// Fixing through Update must not push the broken file into .bak.
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "router", "llama", "mode") }); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, path+".bak"); got != good {
		t.Errorf(".bak must stay the last valid file, not the broken one:\n%s", got)
	}
	if st := s.Status(); st.Code != "" {
		t.Errorf("status after the fix = %+v", st)
	}
}

func TestUpdateOnUnparsableYAMLAsksForARepair(t *testing.T) {
	s, path := openStore(t, "schema_version: 1\nnode: [")
	err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "es", "ui", "language") })
	if err == nil || !strings.Contains(err.Error(), "not valid YAML") {
		t.Errorf("err = %v", err)
	}
	if readString(t, path) != "schema_version: 1\nnode: [" {
		t.Error("the file must be untouched")
	}
}

func TestUpdateCreatesAMissingFileWhenTheResultIsValid(t *testing.T) {
	s, path := openStore(t, "")
	err := s.Update(func(root *yaml.Node) error {
		for _, kv := range []struct {
			v    any
			path []string
		}{
			{1, []string{"schema_version"}},
			{"ai-n5", []string{"node", "id"}},
			{"cpu-only", []string{"profile"}},
		} {
			if err := config.SetKey(root, kv.v, kv.path...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("created file: %v %v (want 0640)", info, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("nothing to back up")
	}
	// Safe defaults are pinned until restart (agent.md 3.4), but the file is healthy now.
	st := s.Status()
	if st.Code != "" || len(st.Pending) == 0 {
		t.Errorf("status = %+v, want healthy with restart pending", st)
	}
}

func TestUpdateOfAnEmptyMissingFileIsRefused(t *testing.T) {
	s, path := openStore(t, "")
	err := s.Update(func(*yaml.Node) error { return nil })
	var verr *config.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no file may be created")
	}
}

// The simulated crash between write and rename. Update renames twice: the previous version into
// .bak first, then the new node.yaml.
func TestUpdateFailureWhileSavingTheBakChangesNothing(t *testing.T) {
	root, path := nodeRoot(t, example(t))
	ffs := fake.New(config.OSFS{})
	s := config.Open(config.Options{Root: root, FS: ffs})
	ffs.FailNth("rename", 1)
	err := s.Update(func(r *yaml.Node) error { return config.SetKey(r, "es", "ui", "language") })
	if !errors.Is(err, fake.ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if readString(t, path) != example(t) {
		t.Error("node.yaml must be intact")
	}
	if names := listDir(t, filepath.Dir(path)); len(names) != 1 {
		t.Errorf("no temp file and no half-written .bak may be visible: %v", names)
	}
	if s.Get().UI.Language != "auto" {
		t.Error("the store must not apply a config that was not written")
	}
}

func TestUpdateCrashBetweenWriteAndRenameKeepsTheOldFileAndTheBak(t *testing.T) {
	root, path := nodeRoot(t, example(t))
	ffs := fake.New(config.OSFS{})
	ffs.Crash = true // the process dies: cleanup cannot run
	s := config.Open(config.Options{Root: root, FS: ffs})
	ffs.FailNth("rename", 2)
	err := s.Update(func(r *yaml.Node) error { return config.SetKey(r, "es", "ui", "language") })
	if err == nil {
		t.Fatal("the crash must surface")
	}
	if readString(t, path) != example(t) {
		t.Error("node.yaml must be intact after the crash")
	}
	if readString(t, path+".bak") != example(t) {
		t.Error(".bak already holds the previous version")
	}
	if names := listDir(t, filepath.Dir(path)); len(names) != 3 {
		t.Errorf("a dead process leaves exactly its temp file behind: %v", names)
	}
	// The agent restarts: the sweep clears the debris and the old file loads.
	reopened := config.Open(config.Options{Root: root})
	if names := listDir(t, filepath.Dir(path)); len(names) != 2 {
		t.Errorf("the next start must sweep the temp file, got %v", names)
	}
	if reopened.Get().UI.Language != "auto" || reopened.Status().Code != "" {
		t.Errorf("the old file loads fine: %+v", reopened.Status())
	}
}

func TestUpdateReportsANonDurableWriteButAppliesTheConfig(t *testing.T) {
	root, path := nodeRoot(t, example(t))
	ffs := fake.New(config.OSFS{})
	s := config.Open(config.Options{Root: root, FS: ffs})
	ffs.FailOn("syncdir", -1)
	err := s.Update(func(r *yaml.Node) error { return config.SetKey(r, "es", "ui", "language") })
	if !errors.Is(err, config.ErrNotDurable) {
		t.Fatalf("err = %v, want ErrNotDurable", err)
	}
	if s.Get().UI.Language != "es" || !strings.Contains(readString(t, path), "language: es") {
		t.Error("the file was replaced, so the new config must be live")
	}
}

func TestUpdateNotifiesSubscribersAndOurOwnWriteIsNotAnEdit(t *testing.T) {
	s, _ := openStore(t, example(t))
	ch, cancel := s.Subscribe()
	defer cancel()
	if err := s.Update(func(root *yaml.Node) error { return config.SetKey(root, "debug", "logging", "level") }); err != nil {
		t.Fatal(err)
	}
	c := expectChange(t, ch)
	if c.Source != config.SourceUpdate || !reflect.DeepEqual(c.Changed, []string{"logging.level"}) ||
		c.Old.Logging.Level != "info" || c.New.Logging.Level != "debug" {
		t.Errorf("change = %+v", c)
	}
	if s.CheckNow() {
		t.Error("the poll must not treat the store's own write as an external edit")
	}
	expectNoChange(t, ch)
}

func expectChange(t testing.TB, ch <-chan config.Change) config.Change {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed")
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no change delivered")
	}
	return config.Change{}
}

func expectNoChange(t testing.TB, ch <-chan config.Change) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("unexpected change: %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

// commentSet lists the comment text of every line, in order (a '#' starting a line or following
// a space; the example has no '#' inside a quoted string).
func commentSet(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "#"); i >= 0 && (i == 0 || l[i-1] == ' ') {
			out = append(out, strings.TrimSpace(l[i:]))
		}
	}
	return out
}
