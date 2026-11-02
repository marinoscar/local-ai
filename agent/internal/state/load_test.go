package state_test

import (
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/state/fake"
)

func TestFreshNode(t *testing.T) {
	m := fake.New()
	s := open(t, m)
	st := s.Snapshot()
	if s.Source() != state.SourceFresh {
		t.Fatalf("source = %v", s.Source())
	}
	if st.Mode != state.ModeOnline || st.Models.Default != nil || st.SchemaVersion != 1 {
		t.Fatalf("defaults wrong: mode=%v default=%v schema=%d", st.Mode, st.Models.Default, st.SchemaVersion)
	}
	if st.ModeTransition != nil || st.PendingMode != nil || len(st.Models.DesiredLoaded) != 0 || st.Models.LastUsed == nil || st.Migration == nil {
		t.Fatalf("everything else must be empty (and maps usable): %+v", st)
	}
	if f := s.Findings(); len(f) != 0 {
		t.Fatalf("a fresh node raises nothing, got %+v", f)
	}
	if n := m.Names(testDir); len(n) != 0 {
		t.Fatalf("Open must not write; dir holds %v", n)
	}
}

func TestLoadRules(t *testing.T) {
	valid := fixture(t, "state-valid.json")
	corrupt := fixture(t, "state-corrupt-truncated.json")
	noSchema := fixture(t, "state-no-schema.json")
	v2 := fixture(t, "state-v2.json")
	badMode := fixture(t, "state-bad-mode.json")

	cases := []struct {
		name         string
		main, bak    []byte // nil = the file does not exist
		wantSource   state.Source
		wantMode     state.Mode
		wantSeverity state.Severity // "" = no finding
		wantDetails  map[string]any
		wantCorrupt  int // files state.json.corrupt-* afterwards
		wantBakCorr  int // files state.json.bak.corrupt-* afterwards
		wantDefault  string
	}{
		{name: "valid main", main: valid, wantSource: state.SourceFile, wantMode: state.ModeOnline, wantDefault: "qwen3.5-9b-q4km"},
		{name: "valid main ignores a bad bak", main: valid, bak: corrupt, wantSource: state.SourceFile, wantMode: state.ModeOnline, wantDefault: "qwen3.5-9b-q4km"},
		{name: "corrupt main, good bak", main: corrupt, bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"source": "bak"}, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "corrupt main and bak", main: corrupt, bak: corrupt, wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantDetails: map[string]any{"source": "defaults"}, wantCorrupt: 1, wantBakCorr: 1},
		{name: "corrupt main, no bak", main: corrupt, wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantDetails: map[string]any{"source": "defaults"}, wantCorrupt: 1},
		{name: "missing main, good bak", bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"source": "bak"}, wantDefault: "qwen3.5-9b-q4km"},
		{name: "missing main, corrupt bak", bak: corrupt, wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantDetails: map[string]any{"source": "defaults"}, wantBakCorr: 1},
		{name: "main without schema_version, good bak", main: noSchema, bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "main without schema_version, no bak", main: noSchema, wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantCorrupt: 1},
		{name: "schema_version 0", main: []byte(`{"schema_version":0,"mode":"online"}`), wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantCorrupt: 1},
		{name: "empty file", main: []byte{}, bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "binary garbage", main: []byte{0, 0, 0, 0, 0xff, 0xfe}, bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "not an object", main: []byte(`["schema_version", 1]`), wantSource: state.SourceDefaults, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityCritical, wantCorrupt: 1},
		{name: "field of the wrong type", main: []byte(`{"schema_version":1,"mode":5}`), bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "trailing garbage", main: append(append([]byte(nil), valid...), "}}"...), bak: valid, wantSource: state.SourceBak, wantMode: state.ModeOnline,
			wantSeverity: state.SeverityWarning, wantCorrupt: 1, wantDefault: "qwen3.5-9b-q4km"},
		{name: "newer schema is loaded best-effort", main: v2, wantSource: state.SourceFile, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"source": "file", "schema_version": 2}, wantDefault: "model-x"},
		{name: "newer schema never makes a good main corrupt even with a bad bak", main: v2, bak: corrupt, wantSource: state.SourceFile, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"schema_version": 2}, wantDefault: "model-x"},
		{name: "corrupt main, newer bak", main: corrupt, bak: v2, wantSource: state.SourceBak, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"source": "bak", "schema_version": 2}, wantCorrupt: 1, wantDefault: "model-x"},
		{name: "unknown mode becomes offline", main: badMode, wantSource: state.SourceFile, wantMode: state.ModeOffline,
			wantSeverity: state.SeverityWarning, wantDetails: map[string]any{"source": "file", "invalid_mode": "hibernate"}, wantDefault: "model-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			if tc.main != nil {
				files[testPath] = tc.main
			}
			if tc.bak != nil {
				files[bakPath] = tc.bak
			}
			m := seeded(files)
			s := open(t, m)
			st := s.Snapshot()
			if s.Source() != tc.wantSource {
				t.Errorf("source = %v, want %v", s.Source(), tc.wantSource)
			}
			if st.Mode != tc.wantMode {
				t.Errorf("mode = %v, want %v", st.Mode, tc.wantMode)
			}
			if tc.wantDefault != "" && defaultModel(t, st) != tc.wantDefault {
				t.Errorf("models.default = %q, want %q", defaultModel(t, st), tc.wantDefault)
			}
			if tc.wantDefault == "" && st.Models.Default != nil {
				t.Errorf("models.default = %q, want nil", *st.Models.Default)
			}
			fs := s.Findings()
			if tc.wantSeverity == "" {
				if len(fs) != 0 {
					t.Errorf("unexpected findings %+v", fs)
				}
			} else {
				if len(fs) != 1 {
					t.Fatalf("findings = %+v, want exactly one", fs)
				}
				f := fs[0]
				if codeOf[f.Kind] != "LAI-AGT-003" || f.Severity != tc.wantSeverity {
					t.Errorf("finding = %s %s, want LAI-AGT-003 %s", codeOf[f.Kind], f.Severity, tc.wantSeverity)
				}
				for k, v := range tc.wantDetails {
					if f.Details[k] != v {
						t.Errorf("details[%q] = %v, want %v (all: %v)", k, f.Details[k], v, f.Details)
					}
				}
				if f.Message == "" {
					t.Error("finding has no message")
				}
			}
			if got := len(withPrefix(m, "state.json.corrupt-")); got != tc.wantCorrupt {
				t.Errorf("state.json.corrupt-* files = %d, want %d (%v)", got, tc.wantCorrupt, m.Names(testDir))
			}
			if got := len(withPrefix(m, "state.json.bak.corrupt-")); got != tc.wantBakCorr {
				t.Errorf("state.json.bak.corrupt-* files = %d, want %d (%v)", got, tc.wantBakCorr, m.Names(testDir))
			}
			if tc.wantCorrupt > 0 {
				if f := fs[0]; f.Details["corrupt_copy"] == nil {
					t.Errorf("details lack corrupt_copy: %v", f.Details)
				}
			}
		})
	}
}

func TestCorruptCopyIsTheBadFileByteForByte(t *testing.T) {
	corrupt := fixture(t, "state-corrupt-truncated.json")
	m := seeded(map[string][]byte{testPath: corrupt, bakPath: fixture(t, "state-valid.json")})
	_ = open(t, m)
	names := withPrefix(m, "state.json.corrupt-")
	if len(names) != 1 {
		t.Fatalf("corrupt copies = %v", names)
	}
	if want := "state.json.corrupt-" + corruptStamp; names[0] != want {
		t.Fatalf("name = %q, want %q (RFC 3339, UTC)", names[0], want)
	}
	got, _ := m.Contents(testDir + "/" + names[0])
	if string(got) != string(corrupt) {
		t.Fatal("the quarantined file differs from the bad file")
	}
	if _, ok := m.Contents(testPath); ok {
		t.Fatal("the bad state.json must be moved away")
	}
}

func TestCorruptCopyNameCollision(t *testing.T) {
	m := seeded(map[string][]byte{
		testPath: fixture(t, "state-corrupt-truncated.json"),
		testDir + "/state.json.corrupt-" + corruptStamp: []byte("an older bad file"),
	})
	fixed := func(o *state.Options) {
		c, _ := time.Parse(time.RFC3339, corruptStamp)
		o.Now = func() time.Time { return c }
	}
	_ = open(t, m, fixed)
	older, _ := m.Contents(testDir + "/state.json.corrupt-" + corruptStamp)
	if string(older) != "an older bad file" {
		t.Fatal("an earlier corrupt copy was overwritten")
	}
	if _, ok := m.Contents(testDir + "/state.json.corrupt-" + corruptStamp + "-2"); !ok {
		t.Fatalf("expected a -2 name, got %v", m.Names(testDir))
	}
}

func TestOpenRemovesStrayTempFiles(t *testing.T) {
	m := seeded(map[string][]byte{
		testPath:                             fixture(t, "state-valid.json"),
		testPath + ".tmp-0a1b2c3d4e5f":       []byte("half a write"),
		testPath + ".bak.tmp-ffffffffffff":   []byte("half a link"),
		testDir + "/other.json.tmp-123456ab": []byte("not ours"),
	})
	_ = open(t, m)
	got := strings.Join(m.Names(testDir), " ")
	if strings.Contains(got, "state.json.tmp-") || strings.Contains(got, "state.json.bak.tmp-") {
		t.Fatalf("stray temp files remain: %s", got)
	}
	if !strings.Contains(got, "other.json.tmp-123456ab") {
		t.Fatalf("a file of another owner was removed: %s", got)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := state.Open("", state.Options{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestOpenSurvivesUnreadableFiles(t *testing.T) {
	m := seeded(map[string][]byte{testPath: fixture(t, "state-valid.json")})
	m.Hook = func(op fake.Op) error {
		if op.Kind == fake.KindRead {
			return errEIO
		}
		return nil
	}
	s := open(t, m)
	if s.Snapshot().Mode != state.ModeOffline || s.Source() != state.SourceDefaults {
		t.Fatalf("an unreadable state must fall back to offline defaults, got %v %v", s.Snapshot().Mode, s.Source())
	}
	if f := s.Findings(); len(f) != 1 || f[0].Severity != state.SeverityCritical {
		t.Fatalf("findings = %+v", f)
	}
	// The unreadable file is not renamed away: it may be fine once the disk recovers.
	if len(withPrefix(m, "state.json.corrupt-")) != 0 {
		t.Fatal("an unreadable file must not be quarantined")
	}
}
