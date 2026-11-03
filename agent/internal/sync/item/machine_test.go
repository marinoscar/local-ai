package item

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

func v1() content { return content{"a.gguf": genData(3000, 1), "b.json": genData(100, 2)} }
func v2() content { return content{"a.gguf": genData(3500, 3), "c.bin": genData(64, 4)} }

func TestHappyPathEveryState(t *testing.T) {
	e := newEnv(t)
	job := e.fileJob("rev1", v1(), true)
	run := e.startRun()
	res := e.m.Run(bg, run, job)
	if res.State != journal.StateVerified || res.Err != nil || res.Bytes != 3100 || res.Version != "rev1" {
		t.Fatalf("result = %+v", res)
	}

	// Every state of the machine, in order, and the swap events in the right place.
	want := []string{"resolving", "downloading", "verifying", "staged", "swapping", "swap_plan", "swap_done", "swapped", "verified"}
	if got := e.events(run.ID, "qwen-x"); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// The item is where readers look for it, with the right bytes, and the best-copy index knows it.
	m, err := e.loadItem("models/qwen-x")
	if err != nil {
		t.Fatal(err)
	}
	assertContent(t, e.itemDir(), v1())
	if m.Version != "rev1" || m.ItemID != "qwen-x" || m.Engine != "llama" || m.State != "verified" || m.TotalBytes != 3100 ||
		m.CatalogVersion != "2026.11.02-1" || m.SyncedBy.RunID != run.ID || m.SyncedBy.NodeID != "ai-test" || m.Trust != manifest.TrustPinned {
		t.Fatalf("manifest = %+v", m)
	}
	for _, f := range m.Files {
		if f.SHA256 != sha(v1()[f.Path]) || f.SHA256Source != "catalog" || f.URL != "https://example.test/"+f.Path {
			t.Fatalf("file %+v", f)
		}
	}
	if _, err := e.idx.BestCopy("qwen-x"); err != nil {
		t.Fatalf("best copy: %v", err)
	}
	rec, _ := e.jr.Items.Get("internal", "qwen-x")
	if rec.State != journal.StateVerified || rec.Version != "rev1" || rec.Attempts != 0 || len(rec.Checkpoints) != 0 {
		t.Fatalf("record = %+v", rec)
	}
	// Staging is gone, files are readable by the services, nothing was put in the trash.
	if pathExists(filepath.Join(e.root.Path, ".staging", "qwen-x")) {
		t.Fatal("staging was not cleaned")
	}
	if pathExists(filepath.Join(e.root.Path, ".trash")) {
		t.Fatal("a first install put something in the trash")
	}
	if info, err := os.Stat(filepath.Join(e.itemDir(), "a.gguf")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", info, err)
	}
}

func TestEveryFileIsFetchedOnceAndVerifiedFromDisk(t *testing.T) {
	e := newEnv(t)
	e.install(e.fileJob("rev1", v1(), true))
	if got := e.fetch.fetched; !reflect.DeepEqual(got, []string{"a.gguf", "b.json"}) {
		t.Fatalf("fetched = %v", got)
	}
	if got := e.fetch.committed; !reflect.DeepEqual(got, []string{"a.gguf", "b.json"}) {
		t.Fatalf("committed = %v", got)
	}
}

func TestUpdateKeepsTheOldVersionInTrash(t *testing.T) {
	e := newEnv(t)
	e.install(e.fileJob("rev1", v1(), true))
	oldManifest := readFile(t, filepath.Join(e.itemDir(), "MANIFEST.json"))

	run := e.startRun()
	res := e.m.Run(bg, run, e.fileJob("rev2", v2(), true))
	if res.State != journal.StateVerified {
		t.Fatalf("result = %+v", res)
	}
	m, err := e.loadItem("models/qwen-x")
	if err != nil || m.Version != "rev2" {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	assertContent(t, e.itemDir(), v2())
	if len(m.History) != 1 || m.History[0].Version != "rev1" {
		t.Fatalf("history = %+v", m.History)
	}
	// b.json belonged to rev1 only: it left the item directory, for the trash.
	if pathExists(filepath.Join(e.itemDir(), "b.json")) {
		t.Fatal("a file of the old version is still in the item directory")
	}
	trash := filepath.Join(e.root.Path, ".trash", "qwen-x", "rev1")
	if string(readFile(t, filepath.Join(trash, "MANIFEST.json"))) != string(oldManifest) {
		t.Fatal("the old manifest is not in the trash as it was")
	}
	assertContent(t, trash, v1())
	if got := e.events(run.ID, "qwen-x"); got[len(got)-1] != "verified" {
		t.Fatalf("events = %v", got)
	}
	// The trash is a copy for R1 only through retention: the live copy is verified, so it may be purged.
	if ok, why := e.ret.CanDelete(trash); !ok {
		t.Fatalf("trash purge refused: %s", why)
	}
}

func TestResolveFailures(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{"transient", errors.New("directory listing failed"), CodeUnresolved, true},
		{"permanent with a code", &ResolveError{Code: doctor.SYN021, Permanent: true, Err: errors.New("oid differs")}, doctor.SYN021, false},
		{"transient with a code", &ResolveError{Code: doctor.SYN034, Err: errors.New("rate limited")}, doctor.SYN034, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			job := e.fileJob("rev1", v1(), true)
			job.Resolution = nil
			job.Resolve = func(context.Context) (Resolution, error) { return Resolution{}, tc.err }
			res := e.m.Run(bg, e.startRun(), job)
			if res.State != journal.StateFailed || res.Code != tc.code || res.Retryable != tc.retryable || res.Reason != "unresolved" {
				t.Fatalf("result = %+v", res)
			}
			if e.fetch.fetchCount() != 0 {
				t.Fatal("something was fetched")
			}
		})
	}
}

func TestResolutionIsChecked(t *testing.T) {
	e := newEnv(t)
	for name, mutate := range map[string]func(*Resolution){
		"unsafe version":   func(r *Resolution) { r.Version = "../x" },
		"unsafe path":      func(r *Resolution) { r.Files[0].Path = "../x" },
		"duplicate path":   func(r *Resolution) { r.Files = append(r.Files, r.Files[0]) },
		"bad sha":          func(r *Resolution) { r.Files[0].SHA256 = "XYZ" },
		"no url":           func(r *Resolution) { r.Files[0].URL = "" },
		"no files":         func(r *Resolution) { r.Files = nil },
		"dir with nothing": func(r *Resolution) { r.Dir, r.Files = true, nil },
	} {
		job := e.fileJob("rev1", v1(), true)
		mutate(job.Resolution)
		res := e.m.Run(bg, e.startRun(), job)
		if res.State != journal.StateFailed || res.Code != CodeInternal {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestStreamingMismatchIsQuarantinedWithAReason(t *testing.T) {
	e := newEnv(t)
	job := e.fileJob("rev1", v1(), true)
	job.Resolution.Files[0].SHA256 = sha([]byte("something else")) // the catalog pin does not match the bytes
	res := e.m.Run(bg, e.startRun(), job)
	if res.State != journal.StateQuarantined || res.Code != doctor.SYN020 || res.Quarantined == "" {
		t.Fatalf("result = %+v", res)
	}
	// reason.json says what failed, with expected and actual, url, bytes and resolver.
	var reason Reason
	if err := json.Unmarshal(readFile(t, filepath.Join(res.Quarantined, ReasonFile)), &reason); err != nil {
		t.Fatal(err)
	}
	if reason.Item != "qwen-x" || reason.Root != "internal" || reason.Version != "rev1" || reason.Code != doctor.SYN020 || reason.Resolver != "test" || len(reason.Files) != 1 {
		t.Fatalf("reason = %+v", reason)
	}
	f := reason.Files[0]
	if f.Path != "a.gguf.part" || f.ExpectedSHA256 != sha([]byte("something else")) || f.ActualSHA256 != sha(v1()["a.gguf"]) || f.Bytes != 3000 || f.URL != "https://example.test/a.gguf" {
		t.Fatalf("file entry = %+v", f)
	}
	// The bad file is in quarantine, under .staging/quarantine/<item>/<UTC ts>/, not deleted and not in staging.
	if !strings.HasPrefix(res.Quarantined, filepath.Join(e.root.Path, ".staging", "quarantine", "qwen-x")+string(filepath.Separator)) {
		t.Fatalf("quarantine dir = %s", res.Quarantined)
	}
	if !pathExists(filepath.Join(res.Quarantined, "a.gguf.part")) || pathExists(filepath.Join(e.root.Path, ".staging", "qwen-x", "rev1", "a.gguf.part")) {
		t.Fatal("the offending file was not moved to quarantine")
	}
	// Nothing was installed: the item is not present.
	if _, err := e.loadItem("models/qwen-x"); err == nil {
		t.Fatal("a quarantined item is present")
	}
	rec, _ := e.jr.Items.Get("internal", "qwen-x")
	if rec.State != journal.StateQuarantined || rec.Code != doctor.SYN020 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSecondQuarantineBlocksTheVersionUntilTheCatalogChanges(t *testing.T) {
	e := newEnv(t)
	bad := func(ver string) Job {
		j := e.fileJob(ver, v1(), true)
		j.Resolution.Files[0].SHA256 = sha([]byte("pin that never matches"))
		return j
	}
	if res := e.m.Run(bg, e.startRun(), bad("rev1")); res.State != journal.StateQuarantined {
		t.Fatalf("first: %+v", res)
	}
	rec, _ := e.jr.Items.Get("internal", "qwen-x")
	if Blocked(rec, "rev1") {
		t.Fatal("blocked after one quarantine")
	}
	if res := e.m.Run(bg, e.startRun(), bad("rev1")); res.State != journal.StateQuarantined {
		t.Fatalf("second: %+v", res)
	}
	rec, _ = e.jr.Items.Get("internal", "qwen-x")
	if !Blocked(rec, "rev1") {
		t.Fatal("not blocked after two quarantines in a row")
	}
	before := e.fetch.fetchCount()
	res := e.m.Run(bg, e.startRun(), bad("rev1"))
	if res.State != journal.StateSkipped || res.Reason != ReasonBlocked || res.Code != doctor.SYN020 || e.fetch.fetchCount() != before {
		t.Fatalf("third: %+v (fetches %d -> %d)", res, before, e.fetch.fetchCount())
	}
	// A new version in the catalog unblocks it, and a good download clears the counter.
	if res := e.m.Run(bg, e.startRun(), e.fileJob("rev2", v2(), true)); res.State != journal.StateVerified {
		t.Fatalf("new version: %+v", res)
	}
	rec, _ = e.jr.Items.Get("internal", "qwen-x")
	if Blocked(rec, "rev1") || len(rec.Extras) != 0 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestRereadCatchesWhatTheDownloaderDidNotSee(t *testing.T) {
	t.Run("a fresh download that reads back differently is quarantined", func(t *testing.T) {
		e := newEnv(t)
		e.fetch.corrupt["a.gguf"] = true
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateQuarantined || res.Code != doctor.SYN020 {
			t.Fatalf("result = %+v", res)
		}
		if got := e.fetch.fetched; !reflect.DeepEqual(got, []string{"a.gguf", "b.json"}) {
			t.Fatalf("fetched = %v: a file that was not resumed restarts nothing", got)
		}
	})
	t.Run("after a resumed download it restarts from zero once, and then succeeds", func(t *testing.T) {
		e := newEnv(t)
		e.fetch.corrupt["a.gguf"] = true
		e.fetch.resumedFrom["a.gguf"] = 1000
		var fetches int32
		e.fetch.onFetch = func(name string) error {
			if name == "a.gguf" && atomic.AddInt32(&fetches, 1) == 2 {
				e.fetch.mu.Lock()
				e.fetch.corrupt["a.gguf"] = false // the restart reads a clean file
				e.fetch.resumedFrom["a.gguf"] = 0
				e.fetch.mu.Unlock()
			}
			return nil
		}
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateVerified {
			t.Fatalf("result = %+v", res)
		}
		if got := e.fetch.fetched; !reflect.DeepEqual(got, []string{"a.gguf", "b.json", "a.gguf"}) {
			t.Fatalf("fetched = %v", got)
		}
		if !reflect.DeepEqual(res.Notices, []string{CodeRestarted}) {
			t.Fatalf("notices = %v", res.Notices)
		}
		assertContent(t, e.itemDir(), v1())
	})
	t.Run("a restart that still reads back differently is quarantined", func(t *testing.T) {
		e := newEnv(t)
		e.fetch.corrupt["a.gguf"] = true
		e.fetch.resumedFrom["a.gguf"] = 1000
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateQuarantined {
			t.Fatalf("result = %+v", res)
		}
		if got := e.fetch.fetched; !reflect.DeepEqual(got, []string{"a.gguf", "b.json", "a.gguf"}) {
			t.Fatalf("fetched = %v, want exactly one restart", got)
		}
	})
	t.Run("with verify_reread off the downloader's digest stands", func(t *testing.T) {
		e := newEnv(t)
		e.reread = false
		e.fetch.corrupt["a.gguf"] = true
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateVerified {
			t.Fatalf("result = %+v", res)
		}
	})
}

func TestComputedChecksumRecordsTheDigestFromDisk(t *testing.T) {
	e := newEnv(t)
	job := e.fileJob("rev1", v1(), false) // no pinned digest: trust computed
	job.Resolution.Trust = manifest.TrustComputed
	res := e.m.Run(bg, e.startRun(), job)
	if res.State != journal.StateVerified {
		t.Fatalf("result = %+v", res)
	}
	m, _ := e.loadItem("models/qwen-x")
	for _, f := range m.Files {
		if f.SHA256 != sha(v1()[f.Path]) || f.SHA256Source != "computed" {
			t.Fatalf("file %+v", f)
		}
	}
	if m.Trust != manifest.TrustComputed {
		t.Fatalf("trust = %s", m.Trust)
	}
}

func TestValidators(t *testing.T) {
	match := func(Meta) bool { return true }
	t.Run("a failing validator quarantines everything with its own code", func(t *testing.T) {
		e := newEnv(t)
		var sawFiles []string
		if err := e.vals.Register("pmtiles-verify", match, func(ctx context.Context, tg Target) error {
			for _, f := range tg.Files {
				if _, err := os.Stat(filepath.Join(tg.StagedDir, f.Path)); err != nil {
					t.Errorf("validator cannot read the staged file: %v", err)
				}
				sawFiles = append(sawFiles, f.Path)
			}
			return codeErr{doctor.SYN023}
		}); err != nil {
			t.Fatal(err)
		}
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateQuarantined || res.Code != doctor.SYN023 || res.Reason != "validator" {
			t.Fatalf("result = %+v", res)
		}
		if !reflect.DeepEqual(sawFiles, []string{"a.gguf", "b.json"}) {
			t.Fatalf("validator saw %v", sawFiles)
		}
		var reason Reason
		if err := json.Unmarshal(readFile(t, filepath.Join(res.Quarantined, ReasonFile)), &reason); err != nil {
			t.Fatal(err)
		}
		if reason.Validator != "pmtiles-verify" || reason.Error == "" || reason.Code != doctor.SYN023 {
			t.Fatalf("reason = %+v", reason)
		}
		assertContent(t, res.Quarantined, v1())
		if _, err := e.loadItem("models/qwen-x"); err == nil {
			t.Fatal("an item that failed its validator is present")
		}
	})
	t.Run("a validator that does not match is not called, a passing one is", func(t *testing.T) {
		e := newEnv(t)
		called := 0
		_ = e.vals.Register("never", func(Meta) bool { return false }, func(context.Context, Target) error { called += 100; return errors.New("no") })
		_ = e.vals.Register("always", match, func(context.Context, Target) error { called++; return nil })
		res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
		if res.State != journal.StateVerified || called != 1 {
			t.Fatalf("result = %+v, called = %d", res, called)
		}
	})
	t.Run("registration is checked", func(t *testing.T) {
		v := NewValidators()
		if v.Register("", match, func(context.Context, Target) error { return nil }) == nil || v.Register("x", nil, nil) == nil {
			t.Fatal("an incomplete registration was accepted")
		}
		_ = v.Register("x", match, func(context.Context, Target) error { return nil })
		if v.Register("x", match, func(context.Context, Target) error { return nil }) == nil {
			t.Fatal("a duplicate name was accepted")
		}
	})
}

type codeErr struct{ code string }

func (e codeErr) Error() string      { return "content check failed" }
func (e codeErr) DoctorCode() string { return e.code }

func TestDownloadErrorsMapToOutcomes(t *testing.T) {
	mk := func(kind download.Kind, code string, permanent bool) error {
		return &download.Error{Kind: kind, Code: code, Permanent: permanent, Err: errors.New("boom")}
	}
	cases := []struct {
		name      string
		err       error
		state     journal.ItemState
		code      string
		reason    string
		retryable bool
		staging   bool // is the staging directory kept
		fault     bool
	}{
		{"permanent", mk(download.KindPermanent, download.CodeRefused, true), journal.StateFailed, download.CodeRefused, "permanent", false, false, false},
		{"retries exhausted", mk(download.KindExhausted, download.CodeRetries, false), journal.StateFailed, download.CodeRetries, "exhausted", true, true, false},
		{"disk", mk(download.KindDisk, download.CodeDisk, false), journal.StateSkipped, download.CodeDisk, ReasonDisk, true, true, false},
		{"root fault", mk(download.KindRootFault, download.CodeIO, false), journal.StateFailed, download.CodeIO, "root_fault", true, true, true},
		{"a plain error", errors.New("bug"), journal.StateFailed, CodeInternal, "internal", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.fetch.err["a.gguf"] = tc.err
			// a.gguf is the first file; the staging directory exists from an earlier .part of b.json.
			stage := filepath.Join(e.root.Path, ".staging", "qwen-x", "rev1")
			_ = os.MkdirAll(stage, 0o700)
			_ = os.WriteFile(filepath.Join(stage, "a.gguf.part"), []byte("partial"), 0o600)
			res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
			if res.State != tc.state || res.Code != tc.code || res.Reason != tc.reason || res.Retryable != tc.retryable || (res.RootFault != nil) != tc.fault {
				t.Fatalf("result = %+v", res)
			}
			if kept := pathExists(filepath.Join(stage, "a.gguf.part")); kept != tc.staging {
				t.Fatalf("staging kept = %v, want %v", kept, tc.staging)
			}
			rec, _ := e.jr.Items.Get("internal", "qwen-x")
			if rec.State != tc.state || rec.Code != tc.code {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

func TestEnsureSpaceDiskSkipAndPurgeNotice(t *testing.T) {
	e := newEnv(t)
	e.fetch.ensureErr = &download.Error{Kind: download.KindDisk, Code: download.CodeDisk, Err: errors.New("short")}
	res := e.m.Run(bg, e.startRun(), e.fileJob("rev1", v1(), true))
	if res.State != journal.StateSkipped || res.Reason != ReasonDisk || e.fetch.fetchCount() != 0 {
		t.Fatalf("result = %+v", res)
	}
	e2 := newEnv(t)
	e2.fetch.purged = 4096
	res = e2.m.Run(bg, e2.startRun(), e2.fileJob("rev1", v1(), true))
	if res.State != journal.StateVerified || !reflect.DeepEqual(res.Notices, []string{CodePurged}) {
		t.Fatalf("result = %+v", res)
	}
}

// cancelAt cancels the context at a named point and then resumes with a fresh context.
func TestCancelAtEveryStateLeavesWorkAndResumes(t *testing.T) {
	big := content{"a.gguf": genData(3<<20+17, 9), "b.json": genData(50, 10)}
	cases := []struct {
		name      string
		arm       func(e *env, cancel context.CancelFunc) func(Progress)
		wantState journal.ItemState
		wantPart  bool
	}{
		{"downloading", func(e *env, cancel context.CancelFunc) func(Progress) {
			e.fetch.onFetch = func(name string) error {
				if name == "b.json" {
					cancel()
					return context.Canceled
				}
				return nil
			}
			return nil
		}, journal.StateDownloading, true},
		{"verifying", func(e *env, cancel context.CancelFunc) func(Progress) {
			return func(p Progress) {
				if p.State == journal.StateVerifying && p.Done > 1<<20 {
					cancel()
				}
			}
		}, journal.StateVerifying, true},
		{"staged", func(e *env, cancel context.CancelFunc) func(Progress) {
			_ = e.vals.Register("cancel-after", func(Meta) bool { return true }, func(context.Context, Target) error { cancel(); return nil })
			return nil
		}, journal.StateStaged, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx, cancel := context.WithCancel(bg)
			defer cancel()
			job := e.fileJob("rev1", big, true)
			job.Progress = tc.arm(e, cancel)
			res := e.m.Run(ctx, e.startRun(), job)
			if !res.Cancelled || res.State != tc.wantState {
				t.Fatalf("result = %+v", res)
			}
			rec, _ := e.jr.Items.Get("internal", "qwen-x")
			if rec.State != tc.wantState {
				t.Fatalf("record = %+v", rec)
			}
			if _, err := e.loadItem("models/qwen-x"); err == nil {
				t.Fatal("a cancelled item is present")
			}
			stage := filepath.Join(e.root.Path, ".staging", "qwen-x", "rev1")
			if !pathExists(stage) {
				t.Fatal("a cancel removed the staging directory")
			}
			// Resume with a live context: the work that is done is not done again.
			e.fetch.onFetch = nil
			e.vals = NewValidators()
			e.m.d.Validators = e.vals
			before := e.fetch.fetchCount()
			job2 := e.fileJob("rev1", big, true)
			res = e.m.Run(bg, e.startRun(), job2)
			if res.State != journal.StateVerified {
				t.Fatalf("resumed: %+v", res)
			}
			assertContent(t, e.itemDir(), big)
			if tc.wantState == journal.StateStaged && e.fetch.fetchCount() != before {
				t.Fatalf("a staged item was fetched again (%d -> %d)", before, e.fetch.fetchCount())
			}
		})
	}
}

func TestCancelBeforeAnythingStarts(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	res := e.m.Run(ctx, e.startRun(), e.fileJob("rev1", v1(), true))
	if !res.Cancelled {
		t.Fatalf("result = %+v", res)
	}
	if _, err := e.loadItem("models/qwen-x"); err == nil {
		t.Fatal("present")
	}
}

func TestProgressIsReported(t *testing.T) {
	e := newEnv(t)
	var got []Progress
	job := e.fileJob("rev1", v1(), true)
	job.Progress = func(p Progress) { got = append(got, p) }
	e.install(job)
	var dl, vf int
	var last Progress
	for _, p := range got {
		switch p.State {
		case journal.StateDownloading:
			dl++
		case journal.StateVerifying:
			vf++
		}
		last = p
	}
	if dl == 0 || vf == 0 || last.Total != 3100 {
		t.Fatalf("progress = %+v", got)
	}
}

func TestRootFaultFromTheDiskWhileVerifying(t *testing.T) {
	e := newEnv(t)
	// A file the fetcher "wrote" but that cannot be read back: EIO is a root fault, not a mismatch.
	job := e.fileJob("rev1", v1(), true)
	e.m.d.FS = eioReads{e.m.d.FS}
	res := e.m.Run(bg, e.startRun(), job)
	if res.State != journal.StateFailed || res.RootFault == nil || res.RootFault.Code != download.CodeIO {
		t.Fatalf("result = %+v", res)
	}
}

type eioReads struct{ FS }

func (eioReads) OpenRead(string) (io.ReadCloser, error) { return nil, syscall.EIO }
