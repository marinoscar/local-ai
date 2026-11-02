package state_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// rewrite opens a file with one fixture as state.json, makes one change, and returns the new bytes.
func rewrite(t *testing.T, name string, change func(*state.State) error) (before, after []byte) {
	t.Helper()
	before = fixture(t, name)
	m := seeded(map[string][]byte{testPath: before})
	s := open(t, m)
	if err := s.MutateSync(change); err != nil {
		t.Fatal(err)
	}
	after, _ = m.Contents(testPath)
	return before, after
}

// The rewritten document equals the original except for the fields the store owns (written_at,
// agent_version) and the ones the test changed.
func sameExcept(t *testing.T, before, after []byte, patch func(doc map[string]any)) {
	t.Helper()
	b, a := generic(t, before), generic(t, after)
	b["written_at"], b["agent_version"] = a["written_at"], a["agent_version"]
	if patch != nil {
		patch(b)
	}
	if !reflect.DeepEqual(b, a) {
		t.Fatalf("the rewrite changed more than expected.\nbefore (patched): %v\nafter:            %v", b, a)
	}
}

func TestUnknownKeysSurviveARewriteAtEveryLevel(t *testing.T) {
	before, after := rewrite(t, "state-unknown-keys.json", func(st *state.State) error {
		id := "changed"
		st.Models.Default = &id
		return nil
	})
	sameExcept(t, before, after, func(doc map[string]any) {
		doc["models"].(map[string]any)["default"] = "changed"
	})
	out := string(after)
	for _, key := range []string{
		"future_top_level", "future_agent_field", "future_models_field", "future_load_field", "future_llama_field",
		"future_key_field", "future_tailscale_field", "future_conn_field", "future_migration_field",
		"future_update_field", "future_update_info_field", "future_transition_field", "future_warning_field", "future_actor_field",
		"nested", "in_progress", "manual_location",
	} {
		if !strings.Contains(out, `"`+key+`"`) {
			t.Errorf("key %q was dropped by the rewrite", key)
		}
	}
	// Numbers inside opaque and unknown parts keep their exact text.
	for _, lit := range []string{"12345678901234567890", "1.50"} {
		if !strings.Contains(out, lit) {
			t.Errorf("number %s was altered", lit)
		}
	}
}

func TestUnknownKeysSurviveTwoRewritesAndAMutationOfTheirNeighbours(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-unknown-keys.json")})
	s := open(t, m)
	for i := 0; i < 2; i++ {
		err := s.Mutate(func(st *state.State) error {
			st.ModeTransition.Attempts["tailscale_off"]++
			st.Models.LastLoad.LoadMS += 10
			st.Key.Fingerprint = "ab12…9f3c"
			st.Migration["llama_qwen"] = state.MigrationEntry{State: "done", Step: "M10", At: time.Now(), Extra: st.Migration["llama_qwen"].Extra}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := onDisk(t, m, testPath)
	if string(got.Models.LastLoad.Extra["future_load_field"]) != "1.50" ||
		string(got.Extra["future_top_level"]) == "" ||
		string(got.ModeTransition.Warnings[0].Extra["future_warning_field"]) == "" ||
		string(got.Migration["llama_qwen"].Extra["future_migration_field"]) != `"m"` {
		t.Fatalf("unknown keys lost or altered: %+v", got.Extra)
	}
	if got.ModeTransition.Attempts["tailscale_off"] != 3 || got.Models.LastLoad.LoadMS != 20 {
		t.Fatalf("typed changes lost: %+v", got.ModeTransition.Attempts)
	}
}

func TestEveryKnownFieldRoundTripsUnchanged(t *testing.T) {
	for _, name := range []string{"state-valid.json", "state-transition.json"} {
		t.Run(name, func(t *testing.T) {
			before, after := rewrite(t, name, func(*state.State) error { return nil })
			sameExcept(t, before, after, nil)
		})
	}
}

func TestNewerSchemaIsRewrittenAsNewer(t *testing.T) {
	before, after := rewrite(t, "state-v2.json", func(st *state.State) error {
		id := "changed"
		st.Models.Default = &id
		return nil
	})
	sameExcept(t, before, after, func(doc map[string]any) {
		doc["models"].(map[string]any)["default"] = "changed"
	})
	doc := generic(t, after)
	if doc["schema_version"] != float64(2) {
		t.Fatalf("schema_version = %v: rewriting must not downgrade the number, or the newer agent misreads its own fields", doc["schema_version"])
	}
	if _, ok := doc["power"]; !ok {
		t.Fatal("the newer agent's top-level key was dropped")
	}
	if _, ok := doc["models"].(map[string]any)["pinned"]; !ok {
		t.Fatal("the newer agent's nested key was dropped")
	}
}

func TestExtraKeyCollidingWithAKnownFieldDoesNotDuplicate(t *testing.T) {
	m := fake.New()
	s := open(t, m)
	err := s.MutateSync(func(st *state.State) error {
		st.Extra = map[string]rawJSON{"mode": rawJSON(`"offline"`), "zz_extra": rawJSON(`1`)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := m.Contents(testPath)
	if strings.Count(string(b), `"mode"`) != 1 {
		t.Fatalf("duplicate key in output:\n%s", b)
	}
	if !strings.Contains(string(b), `"zz_extra": 1`) {
		t.Fatalf("extra key missing:\n%s", b)
	}
	if got := open(t, m).Snapshot().Mode; got != state.ModeOnline {
		t.Fatalf("typed value must win, mode = %v", got)
	}
}

func TestKeysMatchCaseInsensitivelyLikeEncodingJSON(t *testing.T) {
	m := seeded(map[string][]byte{testPath: []byte(`{"schema_version":1,"Mode":"survival"}`)})
	s := open(t, m)
	if s.Snapshot().Mode != state.ModeSurvival {
		t.Fatalf("mode = %v", s.Snapshot().Mode)
	}
	if len(s.Snapshot().Extra) != 0 {
		t.Fatalf("a case variant of a known key must not be kept as an unknown key: %v", s.Snapshot().Extra)
	}
}

func TestSnapshotSharesNothingWithTheStore(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-unknown-keys.json")})
	s := open(t, m)
	pristine := s.Snapshot()

	snap := s.Snapshot()
	snap.Mode = state.ModeOnline
	snap.Models.LastUsed["injected"] = time.Now()
	snap.Models.DesiredLoaded = append(snap.Models.DesiredLoaded, "injected")
	snap.Models.LastLoad.ID = "injected"
	snap.Models.LastLoad.Extra["future_load_field"] = []byte("0")
	snap.Models.Extra["future_models_field"] = []byte("0")
	snap.ModeTransition.Attempts["injected"] = 9
	snap.ModeTransition.Warnings[0].Message = "injected"
	snap.ModeTransition.Warnings[0].Extra["future_warning_field"] = []byte("0")
	snap.ModeTransition.Extra["future_transition_field"] = []byte("0")
	snap.Migration["llama_qwen"].Extra["future_migration_field"] = []byte("0")
	snap.Update.Last.Result = "injected"
	snap.Sync[0] = 'X'
	snap.Drill[0] = 'X'
	snap.Extra["future_top_level"] = []byte("0")
	snap.Extra["injected"] = []byte("0")
	snap.WrittenAt = time.Time{}

	if after := s.Snapshot(); !reflect.DeepEqual(pristine, after) {
		t.Fatalf("modifying a snapshot changed the store:\nbefore %+v\nafter  %+v", pristine, after)
	}

	// And the other direction: a mutation does not reach snapshots taken earlier.
	_ = s.Mutate(func(st *state.State) error {
		st.Models.LastUsed["later"] = time.Now()
		st.ModeTransition.Attempts["later"] = 1
		return nil
	})
	if _, ok := pristine.Models.LastUsed["later"]; ok {
		t.Fatal("a mutation leaked into an earlier snapshot")
	}
	if _, ok := pristine.ModeTransition.Attempts["later"]; ok {
		t.Fatal("a mutation leaked into an earlier snapshot")
	}
}

func TestMutationCallbackGetsACopy(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	s := open(t, m)
	before := s.Snapshot()
	_ = s.Mutate(func(st *state.State) error {
		st.Models.LastUsed["x"] = time.Now()
		st.Sync[0] = 'X'
		return errBoom
	})
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("a failed mutation reached the live state through shared maps or slices")
	}
}

// Clone must copy every field. State and everything under it may only have exported fields (the deep
// copy walks them with reflection) and every map or slice must be copied, never shared.
func TestStateTypesAreDeepCopyable(t *testing.T) {
	var walk func(path string, ty reflect.Type, seen map[reflect.Type]bool)
	walk = func(path string, ty reflect.Type, seen map[reflect.Type]bool) {
		switch ty.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(path+"[]", ty.Elem(), seen)
		case reflect.Map:
			walk(path+"[k]", ty.Elem(), seen)
		case reflect.Struct:
			if ty == reflect.TypeOf(time.Time{}) || seen[ty] {
				return
			}
			seen[ty] = true
			for i := 0; i < ty.NumField(); i++ {
				f := ty.Field(i)
				if !f.IsExported() {
					t.Errorf("%s.%s is unexported: Clone cannot copy it", path, f.Name)
					continue
				}
				walk(path+"."+f.Name, f.Type, seen)
			}
		case reflect.Chan, reflect.Func, reflect.UnsafePointer:
			t.Errorf("%s has kind %v, which Clone cannot copy", path, ty.Kind())
		}
	}
	walk("State", reflect.TypeOf(state.State{}), map[reflect.Type]bool{})
}
