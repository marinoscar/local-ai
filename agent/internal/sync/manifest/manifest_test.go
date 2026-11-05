package manifest_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// sha is a stand-in sha256: the hash of a name, so no hash literal is ever written in the repo.
func sha(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

// fixture reads testdata/<name> and replaces every {{sha:NAME}} with sha(NAME).
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	for {
		s := string(b)
		i := strings.Index(s, "{{sha:")
		if i < 0 {
			return b
		}
		j := strings.Index(s[i:], "}}")
		b = []byte(s[:i] + sha(s[i+len("{{sha:"):i+j]) + s[i+j+2:])
	}
}

// at is the one clock reading the tests share between what they build and what they expect.
var at = time.Date(2026, 11, 2, 3, 41, 10, 0, time.UTC)

// valid is a manifest that passes Validate; cases mutate a copy.
func valid() manifest.Manifest {
	return manifest.Manifest{
		SchemaVersion: 1, ItemID: "model-example", ItemKind: manifest.KindModel, Engine: "llama",
		CatalogVersion: "2026.11.02-1", Version: "0123456789ab", SortKey: "2026-11-02T03:41:10Z", State: "verified",
		Source:     manifest.Source{Type: "huggingface"},
		Files:      []manifest.File{{Kind: "weights", Path: "m.gguf", SizeBytes: 5, SHA256: sha("m"), SHA256Source: "catalog"}},
		TotalBytes: 5, DownloadedAt: at, VerifiedAt: at, LastReverifiedAt: at, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T031500Z-a1f3"},
	}
}

func TestRoundTripIsCanonical(t *testing.T) {
	// The fixtures are in canonical form: parse then encode gives the same bytes back.
	for _, name := range []string{"model.json", "kiwix.json", "dir.json"} {
		t.Run(name, func(t *testing.T) {
			in := fixture(t, name)
			m, err := manifest.Parse(in)
			if err != nil {
				t.Fatal(err)
			}
			out, err := m.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(in, out) {
				t.Fatalf("not canonical\n--- in\n%s\n--- out\n%s", in, out)
			}
		})
	}
}

func TestParseFields(t *testing.T) {
	m, err := manifest.Parse(fixture(t, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.ItemID != "model-example" || m.Engine != "llama" || m.Version != "0123456789ab" || m.Trust != manifest.TrustPinned {
		t.Fatalf("fields: %+v", m)
	}
	if m.Source.Type != "huggingface" || m.Source.String("repo") != "org-alpha/Model-X-GGUF" || m.Source.String("missing") != "" {
		t.Fatalf("source: %+v", m.Source)
	}
	if len(m.Files) != 1 || m.Files[0].SizeBytes != 5870000000 || !strings.Contains(m.Files[0].URL, "a=1&b=2") {
		t.Fatalf("files: %+v", m.Files)
	}
	if !m.VerifiedAt.Equal(at) || m.VerifiedAt.Location() != time.UTC {
		t.Fatalf("verified_at = %v", m.VerifiedAt)
	}
	if len(m.History) != 1 || m.History[0].Version != "fedcba987654" {
		t.Fatalf("history: %+v", m.History)
	}

	k, err := manifest.Parse(fixture(t, "kiwix.json"))
	if err != nil {
		t.Fatal(err)
	}
	if k.Files[0].ZimMD5OK == nil || !*k.Files[0].ZimMD5OK || k.Engine != "" {
		t.Fatalf("kiwix: %+v", k)
	}

	d, err := manifest.Parse(fixture(t, "dir.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsDir() || d.FileCount != 42 || len(d.Files) != 0 {
		t.Fatalf("dir: %+v", d)
	}
}

func TestUnknownFieldsArePreserved(t *testing.T) {
	m, err := manifest.Parse(fixture(t, "future.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture(t, "future.json"), &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"zz_last", "aa_first"} {
		if compact(got[key]) != compact(want[key]) {
			t.Errorf("top-level %s: got %s want %s", key, got[key], want[key])
		}
	}
	// Unknown keys are written after the known ones, sorted by name.
	if i, j := bytes.Index(out, []byte(`"aa_first"`)), bytes.Index(out, []byte(`"zz_last"`)); i < 0 || j < i || i < bytes.Index(out, []byte(`"history"`)) {
		t.Errorf("unknown keys are not last and sorted:\n%s", out)
	}
	for _, frag := range []string{`"mirror": {"rank": 2}`, `"signature": "later-feature"`, `"hostname_hash": "later"`, `"reason": "later"`} {
		// Nested raw values are re-indented by Indent, so compare on a whitespace-free copy.
		if !strings.Contains(compact(out), compact([]byte(frag))) {
			t.Errorf("lost %s", frag)
		}
	}
	// And a second round is byte-stable.
	again, err := manifest.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := again.Encode()
	if err != nil || !bytes.Equal(out, out2) {
		t.Fatalf("second encode differs (err %v)", err)
	}
}

func compact(b []byte) string {
	var out bytes.Buffer
	if err := json.Compact(&out, b); err != nil {
		return strings.ReplaceAll(string(b), " ", "")
	}
	return out.String()
}

func TestHistoryIsCappedAtTen(t *testing.T) {
	m := valid()
	for i := 0; i < 13; i++ {
		m.History = append(m.History, manifest.HistoryEntry{Version: fmt.Sprintf("v%02d", i), VerifiedAt: at, SupersededAt: at})
	}
	out, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := manifest.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.History) != manifest.MaxHistory || back.History[0].Version != "v00" || back.History[9].Version != "v09" {
		t.Fatalf("history on disk: %+v", back.History)
	}
	if len(m.History) != 13 {
		t.Fatal("Encode must not change the value it was called on")
	}

	// A file written elsewhere with more than ten entries is still readable (forward compatibility).
	long := m
	raw, err := long.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := manifest.Parse(raw); err != nil || len(parsed.History) != 13 {
		t.Fatalf("Parse of a long history: %v, %d entries", err, len(parsed.History))
	}
}

func TestSupersededBy(t *testing.T) {
	old := valid()
	old.Version = "v-old"
	old.VerifiedAt = at.Add(-48 * time.Hour)
	for i := 0; i < 10; i++ {
		old.History = append(old.History, manifest.HistoryEntry{Version: fmt.Sprintf("h%d", i)})
	}
	h := old.SupersededBy(at)
	if len(h) != manifest.MaxHistory {
		t.Fatalf("len = %d", len(h))
	}
	if h[0].Version != "v-old" || !h[0].VerifiedAt.Equal(old.VerifiedAt) || !h[0].SupersededAt.Equal(at) || h[1].Version != "h0" || h[9].Version != "h8" {
		t.Fatalf("history: %+v", h)
	}
	if len(old.History) != 10 || old.History[0].Version != "h0" {
		t.Fatal("SupersededBy must not change m")
	}
}

func TestValidate(t *testing.T) {
	dirItem := func(m *manifest.Manifest) {
		m.Files = nil
		m.TreeSHA256 = sha("tree")
		m.FileCount = 3
	}
	tests := []struct {
		name   string
		mutate func(*manifest.Manifest)
		want   error // nil = valid
	}{
		{"valid file item", func(m *manifest.Manifest) {}, nil},
		{"valid directory item", dirItem, nil},
		{"schema 2", func(m *manifest.Manifest) { m.SchemaVersion = 2 }, manifest.ErrSchema},
		{"schema 0", func(m *manifest.Manifest) { m.SchemaVersion = 0 }, manifest.ErrSchema},
		{"state swapped", func(m *manifest.Manifest) { m.State = "swapped" }, manifest.ErrNotVerified},
		{"state empty", func(m *manifest.Manifest) { m.State = "" }, manifest.ErrNotVerified},
		{"item id with slash", func(m *manifest.Manifest) { m.ItemID = "a/b" }, manifest.ErrCorrupt},
		{"item id dotdot", func(m *manifest.Manifest) { m.ItemID = ".." }, manifest.ErrCorrupt},
		{"item id empty", func(m *manifest.Manifest) { m.ItemID = "" }, manifest.ErrCorrupt},
		{"kind empty", func(m *manifest.Manifest) { m.ItemKind = "" }, manifest.ErrCorrupt},
		{"model without engine", func(m *manifest.Manifest) { m.Engine = "" }, manifest.ErrCorrupt},
		{"content without engine is fine", func(m *manifest.Manifest) { m.ItemKind = manifest.KindKiwix; m.Engine = "" }, nil},
		{"version with slash", func(m *manifest.Manifest) { m.Version = "a/b" }, manifest.ErrCorrupt},
		{"version empty", func(m *manifest.Manifest) { m.Version = "" }, manifest.ErrCorrupt},
		{"sort key empty", func(m *manifest.Manifest) { m.SortKey = "" }, manifest.ErrCorrupt},
		{"source type empty", func(m *manifest.Manifest) { m.Source.Type = "" }, manifest.ErrCorrupt},
		{"trust unknown", func(m *manifest.Manifest) { m.Trust = "trusted" }, manifest.ErrCorrupt},
		{"trust empty", func(m *manifest.Manifest) { m.Trust = "" }, manifest.ErrCorrupt},
		{"every trust level is valid", func(m *manifest.Manifest) { m.Trust = manifest.TrustSignedIndex }, nil},
		{"negative total", func(m *manifest.Manifest) { m.TotalBytes = -1 }, manifest.ErrCorrupt},
		{"downloaded_at missing", func(m *manifest.Manifest) { m.DownloadedAt = time.Time{} }, manifest.ErrCorrupt},
		{"verified_at missing", func(m *manifest.Manifest) { m.VerifiedAt = time.Time{} }, manifest.ErrCorrupt},
		{"last_reverified_at missing", func(m *manifest.Manifest) { m.LastReverifiedAt = time.Time{} }, manifest.ErrCorrupt},
		{"no files and no tree", func(m *manifest.Manifest) { m.Files = nil }, manifest.ErrCorrupt},
		{"file kind empty", func(m *manifest.Manifest) { m.Files[0].Kind = "" }, manifest.ErrCorrupt},
		{"file path absolute", func(m *manifest.Manifest) { m.Files[0].Path = "/etc/passwd" }, manifest.ErrCorrupt},
		{"file path climbs", func(m *manifest.Manifest) { m.Files[0].Path = "../x" }, manifest.ErrCorrupt},
		{"file path unclean", func(m *manifest.Manifest) { m.Files[0].Path = "a//b" }, manifest.ErrCorrupt},
		{"file path backslash", func(m *manifest.Manifest) { m.Files[0].Path = `a\b` }, manifest.ErrCorrupt},
		{"file path is the manifest", func(m *manifest.Manifest) { m.Files[0].Path = "MANIFEST.json" }, manifest.ErrCorrupt},
		{"file path is SHA256SUMS", func(m *manifest.Manifest) { m.Files[0].Path = "SHA256SUMS" }, manifest.ErrCorrupt},
		{"file in a subdirectory", func(m *manifest.Manifest) { m.Files[0].Path = "sub/dir/f.bin" }, nil},
		{"file path twice", func(m *manifest.Manifest) { m.Files = append(m.Files, m.Files[0]) }, manifest.ErrCorrupt},
		{"file size negative", func(m *manifest.Manifest) { m.Files[0].SizeBytes = -1 }, manifest.ErrCorrupt},
		{"sha too short", func(m *manifest.Manifest) { m.Files[0].SHA256 = sha("x")[:63] }, manifest.ErrCorrupt},
		{"sha uppercase", func(m *manifest.Manifest) { m.Files[0].SHA256 = strings.ToUpper(sha("x")) }, manifest.ErrCorrupt},
		{"sha not hex", func(m *manifest.Manifest) { m.Files[0].SHA256 = strings.Repeat("g", 64) }, manifest.ErrCorrupt},
		{"file_count on a file item", func(m *manifest.Manifest) { m.FileCount = 2 }, manifest.ErrCorrupt},
		{"directory item with files", func(m *manifest.Manifest) { dirItem(m); m.Files = valid().Files }, manifest.ErrCorrupt},
		{"directory item bad tree hash", func(m *manifest.Manifest) { dirItem(m); m.TreeSHA256 = "abc" }, manifest.ErrCorrupt},
		{"directory item without count", func(m *manifest.Manifest) { dirItem(m); m.FileCount = 0 }, manifest.ErrCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			tc.mutate(&m)
			err := m.Validate()
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				if _, err := m.Encode(); err != nil {
					t.Fatalf("Encode of a valid manifest: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !manifest.IsInvalid(err) {
				t.Fatalf("IsInvalid(%v) = false", err)
			}
			if _, err := m.Encode(); !errors.Is(err, tc.want) {
				t.Fatalf("Encode of an invalid manifest must fail with %v, got %v", tc.want, err)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	good := string(fixture(t, "model.json"))
	swap := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", ``, manifest.ErrCorrupt},
		{"truncated", good[:len(good)/2], manifest.ErrCorrupt},
		{"array", `[]`, manifest.ErrCorrupt},
		{"null", `null`, manifest.ErrCorrupt},
		{"empty object", `{}`, manifest.ErrCorrupt},
		{"schema missing", swap(`"schema_version": 1,`, ``), manifest.ErrCorrupt},
		{"schema as string", swap(`"schema_version": 1`, `"schema_version": "1"`), manifest.ErrCorrupt},
		{"schema 0", swap(`"schema_version": 1`, `"schema_version": 0`), manifest.ErrCorrupt},
		{"newer schema", swap(`"schema_version": 1`, `"schema_version": 2`), manifest.ErrSchema},
		{"newer schema with changed field types", `{"schema_version": 7, "files": "now a string", "source": 5}`, manifest.ErrSchema},
		{"state not verified", swap(`"state": "verified"`, `"state": "staged"`), manifest.ErrNotVerified},
		{"size as string", swap(`"size_bytes": 5870000000`, `"size_bytes": "big"`), manifest.ErrCorrupt},
		{"files as object", swap(`"files": [`, `"files": {"x":[`), manifest.ErrCorrupt},
		{"timestamp garbage", swap(`"2026-11-02T03:41:10Z",
  "last`, `"yesterday",
  "last`), manifest.ErrCorrupt},
		{"trailing garbage", good + `{`, manifest.ErrCorrupt},
		{"too large", `{"schema_version":1,"pad":"` + strings.Repeat("x", 5<<20) + `"}`, manifest.ErrCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(tc.in))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if !manifest.IsInvalid(err) {
				t.Fatalf("IsInvalid(%v) = false", err)
			}
		})
	}
}

func TestCodeIsSYN052(t *testing.T) {
	if manifest.Code != "LAI-SYN-052" {
		t.Fatalf("Code = %s", manifest.Code)
	}
}

func TestSourceWith(t *testing.T) {
	s := manifest.Source{Type: "kiwix"}
	s2, err := s.With("book", "x&y")
	if err != nil {
		t.Fatal(err)
	}
	if s.Fields != nil || s2.String("book") != "x&y" || s2.Type != "kiwix" {
		t.Fatalf("s=%+v s2=%+v", s, s2)
	}
}

// --- writing: atomic, crash-safe ---------------------------------------------------------------

func newItemDir(t *testing.T) (dir string, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, manifest.Path(dir)
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != manifest.FileName {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir, p := newItemDir(t)
	m := valid()
	if err := manifest.Write(config.OSFS{}, p, m); err != nil {
		t.Fatal(err)
	}
	back, err := manifest.Read(config.OSFS{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if back.ItemID != m.ItemID || back.Files[0].SHA256 != m.Files[0].SHA256 || !back.VerifiedAt.Equal(m.VerifiedAt) {
		t.Fatalf("round trip: %+v", back)
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode: %v %v", info, err)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Fatalf("leftovers %v", l)
	}
}

func TestWriteRefusesInvalid(t *testing.T) {
	dir, p := newItemDir(t)
	m := valid()
	m.State = "swapped"
	if err := manifest.Write(config.OSFS{}, p, m); !errors.Is(err, manifest.ErrNotVerified) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an invalid manifest must not be written")
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Fatalf("leftovers %v", l)
	}
}

// TestWriteFaults fails every step of the atomic write, once with a clean failure and once as a
// killed process (the temp file stays), over an existing manifest and over none. The manifest on
// disk is always the old one or the new one, never anything else; and a directory sync that fails
// after the rename leaves the new one in place and says so.
func TestWriteFaults(t *testing.T) {
	steps := []struct {
		op       string
		replaced bool // the rename already happened when this step fails
	}{
		{"open", false}, {"write", false}, {"chmod", false}, {"sync", false}, {"close", false}, {"rename", false}, {"syncdir", true},
	}
	for _, step := range steps {
		for _, crash := range []bool{false, true} {
			for _, hadOld := range []bool{true, false} {
				name := fmt.Sprintf("%s/crash=%v/old=%v", step.op, crash, hadOld)
				t.Run(name, func(t *testing.T) {
					dir, p := newItemDir(t)
					old := valid()
					old.Version = "old-version"
					if hadOld {
						if err := manifest.Write(config.OSFS{}, p, old); err != nil {
							t.Fatal(err)
						}
					}
					next := valid()
					next.Version = "new-version"

					ffs := fake.New(config.OSFS{})
					ffs.Crash = crash
					ffs.FailOn(step.op, 1)
					err := manifest.Write(ffs, p, next)
					if err == nil {
						t.Fatal("the injected fault must surface")
					}
					if step.replaced != errors.Is(err, config.ErrNotDurable) {
						t.Fatalf("ErrNotDurable = %v for %v", errors.Is(err, config.ErrNotDurable), err)
					}

					got, rerr := manifest.Read(config.OSFS{}, p)
					switch {
					case step.replaced:
						if rerr != nil || got.Version != "new-version" {
							t.Fatalf("after a failed dir sync the new manifest is in place: %v %+v", rerr, got.Version)
						}
					case hadOld:
						if rerr != nil || got.Version != "old-version" {
							t.Fatalf("the old manifest must survive: %v %v", rerr, got.Version)
						}
					default:
						if !errors.Is(rerr, fs.ErrNotExist) {
							t.Fatalf("no manifest may appear: %v", rerr)
						}
					}
					if !crash && !step.replaced {
						if l := leftovers(t, dir); len(l) != 0 {
							t.Fatalf("a clean failure leaves no temp file, found %v", l)
						}
					}
					// The restarted process writes again and sweeps the debris.
					ffs.Reset()
					config.SweepTemps(ffs, p)
					if err := manifest.Write(ffs, p, next); err != nil {
						t.Fatal(err)
					}
					if got, err := manifest.Read(config.OSFS{}, p); err != nil || got.Version != "new-version" {
						t.Fatalf("retry: %v %v", err, got.Version)
					}
					if l := leftovers(t, dir); len(l) != 0 {
						t.Fatalf("leftovers after the retry: %v", l)
					}
				})
			}
		}
	}
}

// failingFS makes every write open fail with err (a full disk, a read-only drive).
type failingFS struct {
	config.FS
	err error
}

func (f failingFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: f.err}
	}
	return f.FS.OpenFile(name, flag, perm)
}

// fullFile fails every Write with ENOSPC.
type fullFS struct{ config.FS }

type fullFile struct{ config.File }

func (fullFile) Write([]byte) (int, error) { return 0, syscall.ENOSPC }

func (f fullFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	file, err := f.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return fullFile{file}, nil
}

func TestWriteDiskFullAndReadOnly(t *testing.T) {
	tests := []struct {
		name string
		fsys config.FS
		want error
	}{
		{"disk full", fullFS{config.OSFS{}}, syscall.ENOSPC},
		{"read-only drive", failingFS{config.OSFS{}, syscall.EROFS}, syscall.EROFS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, p := newItemDir(t)
			old := valid()
			if err := manifest.Write(config.OSFS{}, p, old); err != nil {
				t.Fatal(err)
			}
			next := valid()
			next.Version = "new-version"
			if err := manifest.Write(tc.fsys, p, next); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			got, err := manifest.Read(tc.fsys, p) // reading still works on a read-only drive
			if err != nil || got.Version != old.Version {
				t.Fatalf("the old manifest must be served: %v %v", err, got.Version)
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Fatalf("leftovers %v", l)
			}
		})
	}
}

// --- loading: the cheap check ------------------------------------------------------------------

func writeItem(t *testing.T, dir string, m manifest.Manifest, contents map[string]string) {
	t.Helper()
	for rel, c := range contents {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	m := valid()
	m.Files = []manifest.File{
		{Kind: "weights", Path: "m.gguf", SizeBytes: 5, SHA256: sha("m"), SHA256Source: "catalog"},
		{Kind: "mmproj", Path: "sub/p.gguf", SizeBytes: 3, SHA256: sha("p"), SHA256Source: "catalog"},
	}
	m.TotalBytes = 8
	both := map[string]string{"m.gguf": "12345", "sub/p.gguf": "abc"}

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  error // nil = loads
	}{
		{"complete", func(t *testing.T, dir string) { writeItem(t, dir, m, both) }, nil},
		{"file missing", func(t *testing.T, dir string) { writeItem(t, dir, m, map[string]string{"m.gguf": "12345"}) }, manifest.ErrIncomplete},
		{"size differs", func(t *testing.T, dir string) {
			writeItem(t, dir, m, map[string]string{"m.gguf": "1234", "sub/p.gguf": "abc"})
		}, manifest.ErrIncomplete},
		{"a directory where the file should be", func(t *testing.T, dir string) {
			writeItem(t, dir, m, map[string]string{"m.gguf": "12345", "sub/p.gguf/x": "abc"})
		}, manifest.ErrIncomplete},
		{"manifest damaged", func(t *testing.T, dir string) {
			writeItem(t, dir, m, both)
			if err := os.WriteFile(manifest.Path(dir), []byte(`{"schema_version":1,`), 0o644); err != nil {
				t.Fatal(err)
			}
		}, manifest.ErrCorrupt},
		{"manifest not verified", func(t *testing.T, dir string) {
			writeItem(t, dir, m, both)
			b, _ := os.ReadFile(manifest.Path(dir))
			if err := os.WriteFile(manifest.Path(dir), bytes.Replace(b, []byte(`"verified"`), []byte(`"swapped"`), 1), 0o644); err != nil {
				t.Fatal(err)
			}
		}, manifest.ErrNotVerified},
		{"manifest from a newer agent", func(t *testing.T, dir string) {
			writeItem(t, dir, m, both)
			b, _ := os.ReadFile(manifest.Path(dir))
			if err := os.WriteFile(manifest.Path(dir), bytes.Replace(b, []byte(`"schema_version": 1`), []byte(`"schema_version": 2`), 1), 0o644); err != nil {
				t.Fatal(err)
			}
		}, manifest.ErrSchema},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			got, err := manifest.Load(config.OSFS{}, dir)
			if tc.want == nil {
				if err != nil || got.ItemID != m.ItemID {
					t.Fatalf("Load: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || !manifest.IsInvalid(err) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("no manifest is not invalid", func(t *testing.T) {
		_, err := manifest.Load(config.OSFS{}, t.TempDir())
		if !errors.Is(err, fs.ErrNotExist) || manifest.IsInvalid(err) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("read error is not invalid", func(t *testing.T) {
		dir := t.TempDir()
		writeItem(t, dir, m, both)
		ffs := fake.New(config.OSFS{}).FailOn("readfile", 1)
		_, err := manifest.Load(ffs, dir)
		if !errors.Is(err, fake.ErrInjected) || manifest.IsInvalid(err) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestLoadDirectoryItem(t *testing.T) {
	sums := []manifest.Sum{{SHA256: sha("a"), Path: "a.html"}, {SHA256: sha("b"), Path: "sub/b.html"}}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		t.Fatal(err)
	}
	m := valid()
	m.ItemKind, m.Engine = manifest.KindDocs, ""
	m.Files, m.TreeSHA256, m.FileCount = nil, tree, len(sums)

	dir := t.TempDir()
	writeItem(t, dir, m, nil)
	if _, err := manifest.Load(config.OSFS{}, dir); !errors.Is(err, manifest.ErrIncomplete) {
		t.Fatalf("without SHA256SUMS: %v", err)
	}
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Load(config.OSFS{}, dir); err != nil {
		t.Fatalf("with SHA256SUMS: %v", err)
	}
}

// --- SHA256SUMS and the tree hash --------------------------------------------------------------

func TestTreeSHA256(t *testing.T) {
	sums := []manifest.Sum{{SHA256: sha("b"), Path: "b.txt"}, {SHA256: sha("a"), Path: "z/a.txt"}, {SHA256: sha("c"), Path: "a.txt"}}
	got, err := manifest.TreeSHA256(sums)
	if err != nil {
		t.Fatal(err)
	}
	// The spec: sha256 of the sorted "<sha256>  <relpath>\n" lines. Sorted as lines, so by hash first.
	lines := []string{sha("b") + "  b.txt\n", sha("a") + "  z/a.txt\n", sha("c") + "  a.txt\n"}
	for i := range lines {
		for j := i + 1; j < len(lines); j++ {
			if lines[j] < lines[i] {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "")))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("got %s want %s", got, want)
	}

	// Order of the input does not matter; content does.
	rev := []manifest.Sum{sums[2], sums[1], sums[0]}
	if again, _ := manifest.TreeSHA256(rev); again != got {
		t.Fatal("the tree hash depends on the order of the entries")
	}
	changed := append([]manifest.Sum(nil), sums...)
	changed[0].Path = "other.txt"
	if other, _ := manifest.TreeSHA256(changed); other == got {
		t.Fatal("a renamed file must change the tree hash")
	}
	if _, err := manifest.TreeSHA256([]manifest.Sum{{SHA256: "zz", Path: "a"}}); !errors.Is(err, manifest.ErrCorrupt) {
		t.Fatalf("invalid entry: %v", err)
	}
}

func TestSums(t *testing.T) {
	sums := []manifest.Sum{{SHA256: sha("b"), Path: "sub/b.txt"}, {SHA256: sha("a"), Path: "a.txt"}}
	data, err := manifest.FormatSums(sums)
	if err != nil {
		t.Fatal(err)
	}
	want := sha("a") + "  a.txt\n" + sha("b") + "  sub/b.txt\n"
	if string(data) != want {
		t.Fatalf("got %q", data)
	}
	back, err := manifest.ParseSums(data)
	if err != nil || len(back) != 2 || back[0].Path != "a.txt" || back[1].SHA256 != sha("b") {
		t.Fatalf("parse: %v %+v", err, back)
	}
	// sha256sum's binary mode marker is accepted, blank lines are skipped.
	bin, err := manifest.ParseSums([]byte("\n" + sha("a") + " *a.txt\n\n"))
	if err != nil || len(bin) != 1 || bin[0].Path != "a.txt" {
		t.Fatalf("binary mode: %v %+v", err, bin)
	}

	bad := map[string]string{
		"empty":            ``,
		"only blank lines": "\n\n",
		"short hash":       sha("a")[:63] + "  a.txt\n",
		"one space":        sha("a") + " a.txt\n",
		"uppercase":        strings.ToUpper(sha("a")) + "  a.txt\n",
		"no path":          sha("a") + "  \n",
		"escaped name":     `\` + sha("a") + "  a.txt\n",
		"climbing":         sha("a") + "  ../a.txt\n",
		"absolute":         sha("a") + "  /a.txt\n",
		"twice":            sha("a") + "  a.txt\n" + sha("b") + "  a.txt\n",
		"the manifest":     sha("a") + "  MANIFEST.json\n",
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.ParseSums([]byte(in)); !errors.Is(err, manifest.ErrCorrupt) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := manifest.FormatSums([]manifest.Sum{{SHA256: sha("a"), Path: "a\nb"}}); !errors.Is(err, manifest.ErrCorrupt) {
		t.Fatalf("newline in a name: %v", err)
	}
}

func TestWriteReadSums(t *testing.T) {
	dir := t.TempDir()
	if _, err := manifest.ReadSums(config.OSFS{}, dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	sums := []manifest.Sum{{SHA256: sha("a"), Path: "a.txt"}}
	ffs := fake.New(config.OSFS{}).FailOn("rename", 1)
	if err := manifest.WriteSums(ffs, dir, sums); err == nil {
		t.Fatal("fault must surface")
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.SumsName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a failed write must not leave SHA256SUMS")
	}
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		t.Fatal(err)
	}
	back, err := manifest.ReadSums(config.OSFS{}, dir)
	if err != nil || len(back) != 1 || back[0] != sums[0] {
		t.Fatalf("%v %+v", err, back)
	}
}

func TestNames(t *testing.T) {
	for _, s := range []string{"a", "2026-10", "v1.2.3", "Model_X"} {
		if !manifest.SafeName(s) {
			t.Errorf("SafeName(%q) = false", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, " a", "a ", "a\x00b", "a\nb"} {
		if manifest.SafeName(s) {
			t.Errorf("SafeName(%q) = true", s)
		}
	}
}

func TestSafePath(t *testing.T) {
	good := []string{"a.bin", "release/SHA256SUMS", "release/SHA256SUMS.minisig", "a/b/c.txt", "SHA256SUMS.minisig"}
	bad := []string{"", ".", "..", "../x", "a/../b", "/abs", "a//b", "a/", "SHA256SUMS", "MANIFEST.json", "a\\b", "a/ b", "a /b", "a\nb", "a/\x00"}
	for _, p := range good {
		if !manifest.SafePath(p) {
			t.Errorf("SafePath(%q) = false, want true", p)
		}
	}
	for _, p := range bad {
		if manifest.SafePath(p) {
			t.Errorf("SafePath(%q) = true, want false", p)
		}
	}
	// ValidFilePath alone accepts a folder name with a space; SafePath is the stricter rule a catalog name needs.
	if !manifest.ValidFilePath("a/ b") || manifest.SafePath("a/ b") {
		t.Error("SafePath must be stricter than ValidFilePath on an element with a leading space")
	}
}
