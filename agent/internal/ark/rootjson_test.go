package ark_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
)

func TestParseRootJSONFixtures(t *testing.T) {
	tests := []struct {
		file    string
		wantErr error // nil: valid
		wantIs2 error // a second sentinel the error must NOT satisfy, when set
	}{
		{"valid-drive.json", nil, nil},
		{"valid-internal.json", nil, nil},
		{"extra-keys.json", nil, nil},
		{"schema-v2.json", ark.ErrRootJSONSchema, ark.ErrRootJSONCorrupt},
		{"schema-v0.json", ark.ErrRootJSONCorrupt, ark.ErrRootJSONSchema},
		{"schema-string.json", ark.ErrRootJSONCorrupt, nil},
		{"bad-kind.json", ark.ErrRootJSONCorrupt, nil},
		{"bad-root-id-case.json", ark.ErrRootJSONCorrupt, nil},
		{"bad-root-id-short.json", ark.ErrRootJSONCorrupt, nil},
		{"kind-mismatch.json", ark.ErrRootJSONCorrupt, nil},
		{"missing-created-at.json", ark.ErrRootJSONCorrupt, nil},
		{"bad-created-at.json", ark.ErrRootJSONCorrupt, nil},
		{"truncated.json", ark.ErrRootJSONCorrupt, nil},
		{"garbage.json", ark.ErrRootJSONCorrupt, nil},
		{"array.json", ark.ErrRootJSONCorrupt, nil},
		{"empty-object.json", ark.ErrRootJSONCorrupt, nil},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			r, err := ark.ParseRootJSON(fixture(t, tc.file))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ParseRootJSON: %v", err)
				}
				if r.SchemaVersion != 1 || r.RootID == "" {
					t.Fatalf("parsed = %+v", r)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantIs2 != nil && errors.Is(err, tc.wantIs2) {
				t.Fatalf("err = %v must not be %v", err, tc.wantIs2)
			}
		})
	}
}

func TestParseRootJSONFields(t *testing.T) {
	r := mustParse(t, "valid-drive.json")
	if !r.CreatedAt.Equal(epoch) {
		t.Errorf("created_at = %v, want %v", r.CreatedAt, epoch)
	}
	if r.SchemaVersion != 1 || r.RootID != "ark-3f9c0a12" || r.Kind != ark.KindArkDrive ||
		r.Profile != "ark-drive-full" || r.Label != "AI-ARK" || r.Notes != "" ||
		r.CreatedBy.NodeID != "ai-n3" || r.CreatedBy.AgentVersion != "0.4.0" ||
		r.Extra != nil || r.CreatedBy.Extra != nil {
		t.Fatalf("parsed = %+v", r)
	}
}

func TestParseRootJSONTooLarge(t *testing.T) {
	big := append([]byte(`{"notes": "`), []byte(strings.Repeat("x", 70<<10))...)
	big = append(big, `"}`...)
	if _, err := ark.ParseRootJSON(big); !errors.Is(err, ark.ErrRootJSONCorrupt) {
		t.Fatalf("err = %v, want ErrRootJSONCorrupt", err)
	}
}

func TestRoundTripIsByteIdentical(t *testing.T) {
	// The spec example (valid-drive.json) and the internal fixture are in canonical form, so reading
	// and writing them must reproduce the file exactly, and a second write the same again.
	for _, name := range []string{"valid-drive.json", "valid-internal.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "in.json")
			writeFile(t, src, string(fixture(t, name)))
			r, err := ark.ReadRootJSON(src)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, ark.RootJSONName)
			for i := 0; i < 2; i++ {
				if err := ark.WriteRootJSON(out, r); err != nil {
					t.Fatal(err)
				}
				if got := readFile(t, out); got != string(fixture(t, name)) {
					t.Fatalf("write %d differs from the fixture:\n%s", i+1, got)
				}
				if r, err = ark.ReadRootJSON(out); err != nil {
					t.Fatal(err)
				}
			}
			if m := mode(t, out); m != 0o644 {
				t.Errorf("mode = %o, want 644", m)
			}
		})
	}
}

func TestUnknownKeysArePreservedWithAStableOrder(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.json")
	writeFile(t, src, string(fixture(t, "extra-keys.json")))
	r, err := ark.ReadRootJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Extra) != 2 || len(r.CreatedBy.Extra) != 1 {
		t.Fatalf("Extra = %v, CreatedBy.Extra = %v", r.Extra, r.CreatedBy.Extra)
	}
	out := filepath.Join(dir, ark.RootJSONName)
	if err := ark.WriteRootJSON(out, r); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, out), string(fixture(t, "extra-keys.canonical.json")); got != want {
		t.Fatalf("canonical form differs:\n%s\nwant:\n%s", got, want)
	}
	// The output is stable: reading it back and writing again changes nothing.
	r2, err := ark.ReadRootJSON(out)
	if err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(dir, "second.json")
	if err := ark.WriteRootJSON(out2, r2); err != nil {
		t.Fatal(err)
	}
	if readFile(t, out) != readFile(t, out2) {
		t.Fatal("a second round trip changed the file")
	}
	// And it is valid JSON that still has the unknown keys with their values.
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, out)), &m); err != nil {
		t.Fatal(err)
	}
	if string(m["alpha_future"]) != `"x"` {
		t.Errorf("alpha_future = %s", m["alpha_future"])
	}
}

func TestUpdateRootJSONKeepsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "extra-keys.json")))
	r, err := ark.UpdateRootJSON(newFS(), p, func(r *ark.RootJSON) { r.Profile = "ark-drive-lite" })
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile != "ark-drive-lite" {
		t.Fatalf("returned profile = %q", r.Profile)
	}
	got, err := ark.ReadRootJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "ark-drive-lite" || string(got.Extra["alpha_future"]) != `"x"` ||
		string(got.CreatedBy.Extra["host_hint"]) != `"placeholder-host"` || got.Label != "AI-ARK" {
		t.Fatalf("after update: %+v", got)
	}
	noTemps(t, dir)
}

func TestUpdateRootJSONRefusesMissingAndDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.json")
	if _, err := ark.UpdateRootJSON(newFS(), missing, func(*ark.RootJSON) {}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: err = %v", err)
	}
	if exists(missing) {
		t.Fatal("UpdateRootJSON created a missing file")
	}
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, string(fixture(t, "truncated.json")))
	if _, err := ark.UpdateRootJSON(newFS(), bad, func(r *ark.RootJSON) { r.Profile = "x" }); !errors.Is(err, ark.ErrRootJSONCorrupt) {
		t.Fatalf("damaged: err = %v", err)
	}
	if readFile(t, bad) != string(fixture(t, "truncated.json")) {
		t.Fatal("a damaged file was rewritten")
	}
}

func TestStringsAreNotHTMLEscaped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	if err := ark.WriteRootJSON(p, mustParse(t, "valid-internal.json")); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "<angle> & ampersand") || strings.Contains(got, `\u00`) {
		t.Fatalf("notes were escaped:\n%s", got)
	}
}

func mustParse(t testing.TB, name string) ark.RootJSON {
	t.Helper()
	r, err := ark.ParseRootJSON(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWriteRootJSONRefusesInvalidValues(t *testing.T) {
	good := mustParse(t, "valid-drive.json")
	tests := map[string]func(*ark.RootJSON){
		"no schema":     func(r *ark.RootJSON) { r.SchemaVersion = 0 },
		"newer schema":  func(r *ark.RootJSON) { r.SchemaVersion = 2 },
		"bad kind":      func(r *ark.RootJSON) { r.Kind = "usb" },
		"bad id":        func(r *ark.RootJSON) { r.RootID = "ark-XYZ" },
		"kind mismatch": func(r *ark.RootJSON) { r.Kind = ark.KindInternal },
		"no created_at": func(r *ark.RootJSON) { r.CreatedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, ark.RootJSONName)
			r := good
			mutate(&r)
			if err := ark.WriteRootJSON(p, r); err == nil {
				t.Fatal("want an error")
			}
			if len(names(t, dir)) != 0 {
				t.Fatalf("files were created: %v", names(t, dir))
			}
		})
	}
}

func TestReadRootJSONErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ark.ReadRootJSON(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := ark.ReadRootJSON(dir); err == nil || errors.Is(err, ark.ErrRootJSONCorrupt) {
		t.Errorf("a directory: err = %v, want an I/O error that is not 'damaged'", err)
	}
	// An unreadable file is an I/O error, not a damaged one: the caller must not "heal" it.
	p := filepath.Join(dir, ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "valid-drive.json")))
	ffs := newFS()
	ffs.FailOn("readfile", 1)
	if _, err := ark.ReadRootJSONFS(ffs, p); !errors.Is(err, fake.ErrInjected) || errors.Is(err, ark.ErrRootJSONCorrupt) {
		t.Errorf("unreadable: err = %v", err)
	}
	if _, err := ark.ReadRootJSONFS(ffs, p); err != nil {
		t.Errorf("after the fault: %v", err)
	}
}

func TestWriteRootJSONFailuresLeaveTheOldFileAndNoDebris(t *testing.T) {
	old := string(fixture(t, "valid-drive.json"))
	next := mustParse(t, "valid-drive.json")
	next.Profile = "ark-drive-lite"
	for _, op := range []string{"open", "write", "chmod", "sync", "close", "rename"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, ark.RootJSONName)
			writeFile(t, p, old)
			ffs := newFS()
			ffs.FailOn(op, 1)
			err := ark.WriteRootJSONFS(ffs, p, next)
			if !errors.Is(err, fake.ErrInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			if readFile(t, p) != old {
				t.Fatal("the old ROOT.json changed")
			}
			noTemps(t, dir)
		})
	}
}

func TestWriteRootJSONWriteFailure(t *testing.T) {
	// A failing write (the disk-full path): no ROOT.json appears, no temp file stays.
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	ffs := newFS()
	ffs.FailOn("write", 1)
	err := ark.WriteRootJSONFS(ffs, p, mustParse(t, "valid-drive.json"))
	if err == nil {
		t.Fatal("want an error")
	}
	if exists(p) || len(names(t, dir)) != 0 {
		t.Fatalf("debris after a failed write: %v", names(t, dir))
	}
}

func TestWriteRootJSONDirectorySyncFailureIsReportedAsNotDurable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	ffs := newFS()
	ffs.FailOn("syncdir", 1)
	err := ark.WriteRootJSONFS(ffs, p, mustParse(t, "valid-drive.json"))
	if !errors.Is(err, config.ErrNotDurable) {
		t.Fatalf("err = %v, want ErrNotDurable", err)
	}
	// The rename already happened: the new file is in place and valid.
	if _, rerr := ark.ReadRootJSON(p); rerr != nil {
		t.Fatalf("file after a directory sync failure: %v", rerr)
	}
}

func TestWriteRootJSONCrashLeavesATempThatTheNextRunSweeps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	crash := newFS()
	crash.Crash = true
	crash.FailNth("rename", 1) // killed between the fsync and the rename
	if err := ark.WriteRootJSONFS(crash, p, mustParse(t, "valid-drive.json")); err == nil {
		t.Fatal("want an error")
	}
	if exists(p) {
		t.Fatal("ROOT.json exists although the rename never happened")
	}
	left := 0
	for _, n := range names(t, dir) {
		if strings.HasPrefix(n, ark.RootJSONName+".tmp-") {
			left++
		}
	}
	if left != 1 {
		t.Fatalf("temp files after the crash = %d, want 1 (names %v)", left, names(t, dir))
	}
	if removed := config.SweepTemps(newFS(), p); len(removed) != 1 {
		t.Fatalf("SweepTemps removed %v", removed)
	}
	noTemps(t, dir)
}

func TestReadRootJSONOnAnUnreadableMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ark.RootJSONName)
	writeFile(t, p, string(fixture(t, "valid-drive.json")))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ark.ReadRootJSON(p); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want a permission error", err)
	}
}
