package download

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

// The resumed digest equals the uninterrupted one: the hash state marshalled at an offset, restored
// and continued, ends at the same sum (acceptance criterion).
func TestResumedDigestEqualsUninterrupted(t *testing.T) {
	data := genData(1<<20+123, 60)
	want := sha256.Sum256(data)
	for _, cut := range []int{1, 63, 64, 65, 4096, 100000, len(data) - 1, len(data)} {
		h := sha256.New()
		h.Write(data[:cut])
		state, err := encodeSHA(h)
		if err != nil {
			t.Fatal(err)
		}
		h2, err := decodeSHA(state, int64(cut))
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		h2.Write(data[cut:])
		if got := sumOf(h2.Sum(nil)); got != sumOf(want[:]) {
			t.Errorf("cut %d: resumed digest differs from the uninterrupted one", cut)
		}
	}
}

func TestDecodeSHARefusesWhatItCannotTrust(t *testing.T) {
	h := sha256.New()
	h.Write(make([]byte, 1000))
	good, _ := encodeSHA(h)
	short := base64.StdEncoding.EncodeToString(make([]byte, 10))
	garbled := []byte(base64.StdEncoding.EncodeToString(make([]byte, shaLen)))
	for name, tc := range map[string]struct {
		state  string
		offset int64
	}{
		"not base64":             {"!!!", 1000},
		"wrong length":           {short, 1000},
		"covers another offset":  {good, 999},
		"offset zero":            {good, 0},
		"magic bytes are absent": {string(garbled), 0},
	} {
		if _, err := decodeSHA(tc.state, tc.offset); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := decodeSHA(good, 1000); err != nil {
		t.Errorf("the real state was refused: %v", err)
	}
}

func newJournalCP(t *testing.T) (*journal.Journal, *journal.Run, *JournalCheckpointer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "journal")
	j, cp := openJournal(t, dir)
	runs, err := j.Runs.List()
	if err != nil || len(runs) == 0 {
		t.Fatal("no run", err)
	}
	return j, cp.Run, cp, dir
}

func TestJournalCheckpointerSaveLoadClear(t *testing.T) {
	j, run, cp, dir := newJournalCP(t)
	if _, ok := cp.Load("a.bin"); ok {
		t.Fatal("a checkpoint exists before any Save")
	}
	h := sha256.New()
	h.Write(genData(5000, 61))
	st, _ := encodeSHA(h)
	a := Checkpoint{Offset: 5000, ShaState: st, ETag: `"v1"`, LastModified: "Mon, 02 Nov 2026 17:04:11 GMT", URL: "https://files.example.test/a.bin"}
	if err := cp.Save("a.bin", a); err != nil {
		t.Fatal(err)
	}
	b := Checkpoint{Offset: 7000, ShaState: st, URL: "https://files.example.test/b.bin"}
	if err := cp.Save("sub/b.bin", b); err != nil {
		t.Fatal(err)
	}
	got, ok := cp.Load("a.bin")
	if !ok || got.Offset != 5000 || got.ETag != a.ETag || got.URL != a.URL {
		t.Errorf("Load = %+v, %v", got, ok)
	}
	rec, _ := j.Items.Get(testRoot, testItem)
	if rec.PartialBytes != 12000 || len(rec.Checkpoints) != 2 || rec.State != journal.StateDownloading {
		t.Errorf("record = %+v", rec)
	}

	// The write-ahead event is in the run log, and replay reads the same point back.
	log, err := j.Runs.Read(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	intents := journal.Replay(log.Events)
	in := intents[journal.Key(testRoot, testItem)]
	if in.Checkpoints["a.bin"].Offset != 5000 || in.Checkpoints["sub/b.bin"].Offset != 7000 || in.Checkpoints["a.bin"].ShaState != st {
		t.Errorf("replayed checkpoints = %+v", in.Checkpoints)
	}

	// Headers are never stored: only the five checkpoint fields exist in the file and the log.
	for _, f := range []string{filepath.Join(dir, "items.json"), filepath.Join(dir, "runs", run.ID+".jsonl")} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"Authorization", "Bearer", "Retry-After", "User-Agent", "Set-Cookie"} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s contains %q", filepath.Base(f), banned)
			}
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "items.json"))
	var parsed struct {
		Items map[string]struct {
			Checkpoints map[string]map[string]any `json:"checkpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	for file, c := range parsed.Items[journal.Key(testRoot, testItem)].Checkpoints {
		for k := range c {
			switch k {
			case "offset", "sha_state", "etag", "last_modified", "url":
			default:
				t.Errorf("checkpoint of %s has the field %q", file, k)
			}
		}
	}

	if err := cp.Clear("a.bin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cp.Load("a.bin"); ok {
		t.Error("Load finds a cleared checkpoint")
	}
	rec, _ = j.Items.Get(testRoot, testItem)
	if rec.PartialBytes != 7000 || len(rec.Checkpoints) != 1 {
		t.Errorf("after Clear: %+v", rec)
	}
	log, _ = j.Runs.Read(run.ID)
	n := len(log.Events)
	if err := cp.Clear("a.bin"); err != nil {
		t.Fatal(err)
	}
	if log, _ = j.Runs.Read(run.ID); len(log.Events) != n {
		t.Error("clearing a point that does not exist must not write an event")
	}
	in = journal.Replay(log.Events)[journal.Key(testRoot, testItem)]
	if c, ok := in.Checkpoints["a.bin"]; !ok || c.Offset != 0 || c.ShaState != "" {
		t.Errorf("a cleared point must replay as offset 0, got %+v", c)
	}
}

func TestJournalCheckpointerNeedsARecordAtTheVersion(t *testing.T) {
	j, run, cp, _ := newJournalCP(t)
	st, _ := encodeSHA(sha256.New())
	other := &JournalCheckpointer{Items: j.Items, Run: run, RootID: testRoot, ItemID: testItem, Version: "v2"}
	if err := other.Save("a.bin", Checkpoint{Offset: 1, ShaState: st}); !errors.Is(err, ErrNoRecord) {
		t.Errorf("Save at another version: %v", err)
	}
	missing := &JournalCheckpointer{Items: j.Items, Run: run, RootID: testRoot, ItemID: "item-b", Version: testVer}
	if err := missing.Save("a.bin", Checkpoint{Offset: 1, ShaState: st}); !errors.Is(err, ErrNoRecord) {
		t.Errorf("Save without a record: %v", err)
	}
	_ = cp.Save("a.bin", Checkpoint{Offset: 1, ShaState: st})
	if _, ok := other.Load("a.bin"); ok {
		t.Error("a checkpoint of version v1 was offered to v2")
	}
}

// The journal that lost its record (ErrNoRecord) must not stop a download: it still completes, it
// just cannot resume across a restart.
func TestFetchSurvivesAJournalThatCannotSave(t *testing.T) {
	data := genData(testSize, 62)
	e := newEnv(t, nil)
	srv := newTestServer(t, data)
	j, run, _, _ := newJournalCP(t)
	broken := &JournalCheckpointer{Items: j.Items, Run: run, RootID: testRoot, ItemID: "item-zzz", Version: testVer}
	res, err := e.dl.Fetch(context.Background(), e.job(srv.URL+"/f", data), broken)
	if err != nil || res.SHA256 != sum(data) {
		t.Fatalf("Fetch: %v, %+v", err, res)
	}
	if !strings.Contains(e.logs.String(), "checkpoint not saved") {
		t.Error("the failed save must be logged")
	}
}
