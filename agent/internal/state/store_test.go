package state_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

func TestFirstMutateWritesTheFile(t *testing.T) {
	m := fake.New()
	s := open(t, m, func(o *state.Options) { o.Owner = &state.Owner{UID: 0, GID: 4242} })
	if err := s.Mutate(setDefault("model-a")); err != nil {
		t.Fatal(err)
	}
	mode, uid, gid, ok := m.Mode(testPath)
	if !ok || mode != 0o640 || uid != 0 || gid != 4242 {
		t.Fatalf("state.json mode/owner = %v %d:%d (exists %v), want 0640 0:4242", mode, uid, gid, ok)
	}
	st := onDisk(t, m, testPath)
	if defaultModel(t, st) != "model-a" || st.Mode != state.ModeOnline {
		t.Fatalf("on disk: %+v", st)
	}
	if st.AgentVersion != testVersion {
		t.Fatalf("agent_version = %q", st.AgentVersion)
	}
	if want, _ := time.Parse(time.RFC3339, corruptStamp); !st.WrittenAt.Equal(want) {
		t.Fatalf("written_at = %v, want the clock's %v", st.WrittenAt, want)
	}
	if !s.Snapshot().WrittenAt.Equal(st.WrittenAt) {
		t.Fatal("Snapshot must show the same written_at as the file")
	}
	if _, has := m.Contents(bakPath); has {
		t.Fatal("no backup is made for the very first write (there is nothing to keep)")
	}
	// A new store sees the same state.
	s2 := open(t, m)
	if s2.Source() != state.SourceFile || defaultModel(t, s2.Snapshot()) != "model-a" {
		t.Fatalf("reopen: %v %+v", s2.Source(), s2.Snapshot())
	}
}

func TestWrittenAtAndVersionChangeOnEveryWrite(t *testing.T) {
	m := fake.New()
	s := open(t, m)
	_ = s.Mutate(setDefault("a"))
	first := onDisk(t, m, testPath).WrittenAt
	_ = s.Mutate(setDefault("b"))
	second := onDisk(t, m, testPath).WrittenAt
	if !second.After(first) {
		t.Fatalf("written_at did not advance: %v then %v", first, second)
	}
}

func TestMutateErrorKeepsStateAndWritesNothing(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.ResetOps()
	boom := errors.New("boom")
	err := s.Mutate(func(st *state.State) error {
		st.Mode = state.ModeSurvival // must be discarded
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if s.Snapshot().Mode != state.ModeOnline {
		t.Fatal("a failed mutation changed the state")
	}
	if len(m.Ops()) != 0 {
		t.Fatalf("a failed mutation touched the disk: %v", m.Ops())
	}
}

func TestMutatePanicIsRecovered(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	err := s.Mutate(func(st *state.State) error {
		st.Mode = state.ModeSurvival
		panic("oops")
	})
	if err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("err = %v", err)
	}
	if s.Snapshot().Mode != state.ModeOnline {
		t.Fatal("a panicking mutation changed the state")
	}
	if err := s.Mutate(setDefault("still-works")); err != nil {
		t.Fatalf("the writer must survive a panic: %v", err)
	}
}

func TestMutateRejectsStateThatCannotBeSerialised(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	err := s.Mutate(func(st *state.State) error {
		st.Sync = []byte(`{not json`)
		return nil
	})
	if err == nil {
		t.Fatal("want an error for invalid raw JSON")
	}
	if err := s.Mutate(setDefault("fine")); err != nil || !s.Writable() {
		t.Fatalf("a rejected mutation must not poison later writes: %v writable=%v", err, s.Writable())
	}
}

func TestNoopMutationWritesNothingButSyncDoes(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.ResetOps()
	if err := s.Mutate(func(*state.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(m.Ops()) != 0 {
		t.Fatalf("a mutation that changes nothing wrote: %v", m.Ops())
	}
	if err := s.MutateSync(func(*state.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !contains(m.Kinds("state.json"), fake.KindRename) {
		t.Fatalf("MutateSync must write even without a change: %v", m.Ops())
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestBakHoldsThePreviousVersion(t *testing.T) {
	for _, noLink := range []bool{false, true} {
		name := "hard link"
		if noLink {
			name = "filesystem without hard links"
		}
		t.Run(name, func(t *testing.T) {
			m := fake.New()
			m.NoLink = noLink
			s := open(t, m)
			for _, id := range []string{"v1", "v2", "v3"} {
				if err := s.Mutate(setDefault(id)); err != nil {
					t.Fatal(err)
				}
			}
			if got := defaultModel(t, onDisk(t, m, testPath)); got != "v3" {
				t.Fatalf("state.json = %q", got)
			}
			if got := defaultModel(t, onDisk(t, m, bakPath)); got != "v2" {
				t.Fatalf("state.json.bak = %q, want the previous good version v2", got)
			}
			if n := tempNames(m); len(n) != 0 {
				t.Fatalf("temp files left: %v", n)
			}
		})
	}
}

func TestBakSurvivesWhenStateWasLoadedFromIt(t *testing.T) {
	good := fixture(t, "state-valid.json")
	m := seeded(map[string][]byte{testPath: fixture(t, "state-corrupt-truncated.json"), bakPath: good})
	s := open(t, m)
	if err := s.Mutate(setDefault("after-recovery")); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Contents(bakPath); string(got) != string(good) {
		t.Fatal("the first write after a recovery must not overwrite the good backup with anything else")
	}
	if got := defaultModel(t, onDisk(t, m, testPath)); got != "after-recovery" {
		t.Fatalf("state.json = %q", got)
	}
	if err := s.Mutate(setDefault("second")); err != nil {
		t.Fatal(err)
	}
	if got := defaultModel(t, onDisk(t, m, bakPath)); got != "after-recovery" {
		t.Fatalf("from the second write on, .bak is the previous write: %q", got)
	}
}

func TestRecoveredDefaultsAreWrittenOnFlush(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-corrupt-truncated.json")})
	s := open(t, m)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	st := onDisk(t, m, testPath)
	if st.Mode != state.ModeOffline {
		t.Fatalf("the offline fallback must be persisted, got %v", st.Mode)
	}
	// A later restart is then a clean load of the offline state (the finding is gone, the corrupt copy stays).
	s2 := open(t, m)
	if len(s2.Findings()) != 0 || s2.Snapshot().Mode != state.ModeOffline {
		t.Fatalf("second start: %v %+v", s2.Snapshot().Mode, s2.Findings())
	}
	if len(withPrefix(m, "state.json.corrupt-")) != 1 {
		t.Fatalf("the corrupt copy must be kept: %v", m.Names(testDir))
	}
}

func TestFlushWithNothingPendingWritesNothing(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.ResetOps()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(m.Ops()) != 0 {
		t.Fatalf("Flush wrote with nothing pending: %v", m.Ops())
	}
}

// MutateSync returns only after the rename and the directory fsync: that is the guarantee the mode
// engine relies on when it records a transition step.
func TestMutateSyncReturnsAfterFsync(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.ResetOps()
	err := s.MutateSync(func(st *state.State) error {
		st.ModeTransition = &state.ModeTransition{OpID: "op_3h8x2k1m0p7w", From: state.ModeOnline, Target: state.ModeSurvival, Step: "tailscale_off"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, o := range m.Ops() {
		if o.Kind == fake.KindSyncDir || (strings.Contains(o.Path, "state.json") && !strings.Contains(o.Path, ".bak")) {
			kinds = append(kinds, o.Kind)
		}
	}
	idx := func(k string) int {
		for i, v := range kinds {
			if v == k {
				return i
			}
		}
		return -1
	}
	if !(idx(fake.KindWrite) < idx(fake.KindSync) && idx(fake.KindSync) < idx(fake.KindRename) && idx(fake.KindRename) < idx(fake.KindSyncDir)) || idx(fake.KindSyncDir) < 0 {
		t.Fatalf("order must be write, fsync, rename, directory fsync; got %v", kinds)
	}
	st := onDisk(t, m, testPath)
	if st.ModeTransition == nil || st.ModeTransition.Step != "tailscale_off" {
		t.Fatalf("on disk: %+v", st.ModeTransition)
	}
}

func TestMutateAfterClose(t *testing.T) {
	m := fake.New()
	s := open(t, m)
	if err := s.Mutate(setDefault("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Mutate(setDefault("b")); !errors.Is(err, state.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if err := s.MutateSync(setDefault("b")); !errors.Is(err, state.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if err := s.Flush(); !errors.Is(err, state.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if defaultModel(t, s.Snapshot()) != "a" {
		t.Fatal("Snapshot must keep working after Close")
	}
}

// Unwritable disk: the API keeps working from memory (agent.md section 4.1 step 4, section 5).
func TestUnwritableDirectoryKeepsStateInMemory(t *testing.T) {
	cases := []struct {
		name string
		kind string
		err  error
		text string
	}{
		{"read-only filesystem", fake.KindOpen, syscall.EROFS, "read-only"},
		{"disk full on write", fake.KindWrite, syscall.ENOSPC, "no space"},
		{"disk full on create", fake.KindOpen, syscall.ENOSPC, "no space"},
		{"permission denied on rename", fake.KindRename, syscall.EACCES, "permission denied"},
		{"io error on fsync", fake.KindSync, syscall.EIO, "input/output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := fixture(t, "state-valid.json")
			m := seeded(map[string][]byte{testPath: orig})
			var notified [][]state.Finding
			s := open(t, m, func(o *state.Options) {
				o.OnFindings = func(f []state.Finding) { notified = append(notified, f) }
			})
			m.Hook = failOn(tc.kind, tc.err)

			// The caller sees no error: readers and the API keep working.
			if err := s.Mutate(setDefault("in-memory-only")); err != nil {
				t.Fatalf("Mutate returned %v; an unwritable disk must not surface as an error", err)
			}
			if err := s.MutateSync(func(st *state.State) error { st.Mode = state.ModeSurvival; return nil }); err != nil {
				t.Fatalf("MutateSync returned %v", err)
			}
			st := s.Snapshot()
			if defaultModel(t, st) != "in-memory-only" || st.Mode != state.ModeSurvival {
				t.Fatalf("the change is not visible in memory: %+v", st)
			}
			if s.Writable() {
				t.Fatal("Writable() = true after a failed write")
			}
			fs := s.Findings()
			if len(fs) != 1 || codeOf[fs[0].Kind] != "LAI-AGT-004" || fs[0].Severity != state.SeverityCritical {
				t.Fatalf("findings = %+v, want one LAI-AGT-004 critical", fs)
			}
			if got := strings.ToLower(fs[0].Details["error"].(string)); !strings.Contains(got, tc.text) {
				t.Errorf("details.error = %q, want it to mention %q", got, tc.text)
			}
			if fs[0].Details["path"] != testPath {
				t.Errorf("details.path = %v", fs[0].Details["path"])
			}
			if got, _ := m.Contents(testPath); string(got) != string(orig) {
				t.Fatal("state.json changed although every write failed")
			}
			if n := tempNames(m); len(n) != 0 {
				t.Fatalf("temp files left: %v", n)
			}
			if len(notified) != 1 || len(notified[0]) != 1 {
				t.Fatalf("OnFindings calls = %d (%v), want exactly one when the condition starts", len(notified), notified)
			}

			// The disk recovers: the next mutation retries and writes everything.
			m.Hook = nil
			if err := s.Mutate(setDefault("recovered")); err != nil {
				t.Fatal(err)
			}
			if !s.Writable() || len(s.Findings()) != 0 {
				t.Fatalf("after recovery: writable=%v findings=%+v", s.Writable(), s.Findings())
			}
			disk := onDisk(t, m, testPath)
			if defaultModel(t, disk) != "recovered" || disk.Mode != state.ModeSurvival {
				t.Fatalf("on disk after recovery: default=%v mode=%v (the earlier in-memory change must be written too)", disk.Models.Default, disk.Mode)
			}
			if len(notified) != 2 || len(notified[1]) != 0 {
				t.Fatalf("OnFindings must be told when the condition clears: %v", notified)
			}
		})
	}
}

func TestUnwritableOnFreshNode(t *testing.T) {
	m := fake.New()
	m.Hook = failOn(fake.KindOpen, syscall.EROFS)
	s := open(t, m)
	if err := s.Mutate(setDefault("x")); err != nil {
		t.Fatal(err)
	}
	if defaultModel(t, s.Snapshot()) != "x" || len(s.Findings()) != 1 {
		t.Fatalf("snapshot %+v findings %+v", s.Snapshot().Models, s.Findings())
	}
}

func TestRetryOnNextMutationEvenWithoutChange(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.Hook = failOn(fake.KindWrite, syscall.ENOSPC)
	_ = s.Mutate(setDefault("pending"))
	m.Hook = nil
	if err := s.Mutate(func(*state.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if defaultModel(t, onDisk(t, m, testPath)) != "pending" {
		t.Fatal("a no-op mutation after a failed write must retry the write")
	}
}

func TestFlushReportsTheWriteError(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.Hook = failOn(fake.KindWrite, syscall.ENOSPC)
	_ = s.Mutate(setDefault("pending"))
	if err := s.Flush(); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Flush err = %v, want ENOSPC", err)
	}
	m.Hook = nil
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush after recovery: %v", err)
	}
}

func TestCloseMakesALastAttempt(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	m.Hook = failOn(fake.KindWrite, syscall.ENOSPC)
	_ = s.Mutate(setDefault("pending"))
	m.Hook = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if defaultModel(t, onDisk(t, m, testPath)) != "pending" {
		t.Fatal("Close must flush what is still pending")
	}
}

// The kill test: stop the process after every possible number of file operations of a write. After a
// reboot (unfsynced bytes gone) the store must load a complete state, with no finding, and sweep the
// temp file. It must never fall back to defaults while a good file existed.
func TestKilledMidWriteNeverLosesTheState(t *testing.T) {
	scenarios := []struct {
		name  string
		files map[string][]byte
		sync  bool
	}{
		{"mutate on an existing file", map[string][]byte{testPath: fixtureBytes("state-valid.json")}, false},
		{"sync mutate on an existing file", map[string][]byte{testPath: fixtureBytes("state-valid.json")}, true},
		{"mutate with a backup present", map[string][]byte{testPath: fixtureBytes("state-valid.json"), bakPath: fixtureBytes("state-transition.json")}, true},
		{"first write on a fresh node", nil, true},
		{"first write after a recovery from the backup", map[string][]byte{testPath: fixtureBytes("state-corrupt-truncated.json"), bakPath: fixtureBytes("state-valid.json")}, true},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			run := func(m *fake.MemFS) {
				s, err := state.Open(testPath, state.Options{FS: m, Now: newClock().Now})
				if err != nil {
					t.Fatal(err)
				}
				mutate := s.Mutate
				if sc.sync {
					mutate = s.MutateSync
				}
				_ = mutate(func(st *state.State) error {
					st.Mode = state.ModeSurvival
					id := "after-write"
					st.Models.Default = &id
					return nil
				})
				_ = s.Close()
			}
			probe := seeded(sc.files)
			run(probe)
			steps := probe.MutatingOps()
			if steps < 6 {
				t.Fatalf("probe ran only %d mutating operations", steps)
			}
			for n := 0; n <= steps; n++ {
				m := seeded(sc.files)
				// The load itself may quarantine a bad file; only count operations from the write on.
				pre, err := state.Open(testPath, state.Options{FS: m, Now: newClock().Now})
				if err != nil {
					t.Fatal(err)
				}
				_ = pre.Close()
				m.CrashAfter(n)
				run(m)
				m.Reboot()

				s := open(t, m)
				if s.Source() == state.SourceDefaults {
					t.Fatalf("killed after %d operations: the store fell back to defaults; files: %s", n, m)
				}
				st := s.Snapshot()
				switch {
				case st.Mode == state.ModeSurvival && st.Models.Default != nil && *st.Models.Default == "after-write":
				case sc.files == nil && s.Source() == state.SourceFresh:
				case st.Mode == state.ModeOnline && st.Models.Default != nil && *st.Models.Default == "qwen3.5-9b-q4km":
				default:
					t.Fatalf("killed after %d operations: loaded neither the old nor the new state: %v %+v", n, s.Source(), st.Models)
				}
				if left := tempNames(m); len(left) != 0 {
					t.Fatalf("killed after %d operations: temp files remain after Open: %v", n, left)
				}
			}
		})
	}
}

func fixtureBytes(name string) []byte {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		panic(err)
	}
	return b
}

func TestTypedFieldsSurviveWriteAndReload(t *testing.T) {
	m := fake.New()
	s := open(t, m)
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	want := state.ModeTransition{
		OpID: "op_3h8x2k1m0p7w", From: state.ModeOnline, Target: state.ModeSurvival, Step: "survival_models", StartedAt: at,
		Attempts: map[string]int{"tailscale_off": 1, "survival_models": 2},
		Warnings: []state.TransitionWarning{{Code: "LAI-LLM-020", Message: "The preferred model failed to start; using the next one."}},
		Actor:    state.Actor{Kind: "anonymous", Via: "portal"},
	}
	pending := state.ModeSurvival
	err := s.MutateSync(func(st *state.State) error {
		tr := want
		st.ModeTransition = &tr
		st.PendingMode = &pending
		st.Models.LastLoad = &state.LastLoad{ID: "model-x", OpID: "op_7k2m9q4r8t1c", Result: state.LoadInterrupted, LoadMS: 7100, At: at}
		st.Models.LastUsed["model-x"] = at
		st.Agent.RecentStarts = append(st.Agent.RecentStarts, at)
		st.Sync = []byte(`{"_owner":"ark-sync.md","last_result":"ok"}`)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := open(t, m).Snapshot()
	if !reflect.DeepEqual(*got.ModeTransition, want) {
		t.Fatalf("transition = %+v\nwant %+v", *got.ModeTransition, want)
	}
	if got.PendingMode == nil || *got.PendingMode != state.ModeSurvival {
		t.Fatalf("pending_mode = %v", got.PendingMode)
	}
	if got.Models.LastLoad == nil || got.Models.LastLoad.Result != state.LoadInterrupted || got.Models.LastLoad.LoadMS != 7100 {
		t.Fatalf("last_load = %+v", got.Models.LastLoad)
	}
	if !got.Models.LastUsed["model-x"].Equal(at) || len(got.Agent.RecentStarts) != 1 {
		t.Fatalf("last_used/recent_starts = %v %v", got.Models.LastUsed, got.Agent.RecentStarts)
	}
	var compact bytes.Buffer // the file is indented, so compare the JSON, not the bytes
	if err := json.Compact(&compact, got.Sync); err != nil || compact.String() != `{"_owner":"ark-sync.md","last_result":"ok"}` {
		t.Fatalf("sync = %s (%v)", got.Sync, err)
	}
}

func TestOnRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	opts := state.Options{AgentVersion: testVersion}
	s, err := state.Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := s.MutateSync(setDefault(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("state.json: %v %v", st, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"state.json", "state.json.bak"}) {
		t.Fatalf("directory = %v", names)
	}
	// Damage the file and start again: the backup (version "one") is used and the bad file is kept.
	if err := os.WriteFile(path, []byte("{ not json"), 0o640); err != nil {
		t.Fatal(err)
	}
	s2, err := state.Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if s2.Source() != state.SourceBak || defaultModel(t, s2.Snapshot()) != "one" {
		t.Fatalf("source %v default %q", s2.Source(), defaultModel(t, s2.Snapshot()))
	}
	matches, _ := os.ReadDir(dir)
	corrupt := 0
	for _, e := range matches {
		if strings.HasPrefix(e.Name(), "state.json.corrupt-") {
			corrupt++
		}
	}
	if corrupt != 1 {
		t.Fatalf("corrupt copies = %d", corrupt)
	}
}

func TestOnReadOnlyRealDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s, err := state.Open(dir+"/state.json", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Mutate(setDefault("x")); err != nil {
		t.Fatal(err)
	}
	if fs := s.Findings(); len(fs) != 1 || codeOf[fs[0].Kind] != "LAI-AGT-004" {
		t.Fatalf("findings = %+v", fs)
	}
}
