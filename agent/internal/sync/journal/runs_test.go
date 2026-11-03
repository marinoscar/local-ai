package journal_test

import (
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
	"github.com/marinoscar/local-ai/agent/internal/sync/journal"
)

func TestNewRunID(t *testing.T) {
	at := time.Date(2026, 11, 2, 3, 15, 0, 123, time.FixedZone("x", -6*3600))
	id, err := journal.NewRunID(at, (&counter{n: 0xa1f2}).Read)
	if err != nil {
		t.Fatal(err)
	}
	if id != "run-20261102T091500Z-a1f3" || !journal.ValidRunID(id) {
		t.Fatalf("id %q", id)
	}
	if _, err := journal.NewRunID(at, func([]byte) (int, error) { return 0, errors.New("no entropy") }); err == nil {
		t.Fatal("a failing random source must fail")
	}
	real, err := journal.NewRunID(at, nil)
	if err != nil || !journal.ValidRunID(real) || !strings.HasPrefix(real, "run-20261102T091500Z-") {
		t.Fatalf("crypto/rand id %q %v", real, err)
	}
	for _, bad := range []string{"", "run-1", "run-20261102T091500Z-a1f", "run-20261102T091500Z-A1F3", "run-20261102T091500-a1f3", "x-20261102T091500Z-a1f3", "run-20261102T091500Z-a1f3.jsonl", "../run-20261102T091500Z-a1f3"} {
		if journal.ValidRunID(bad) {
			t.Errorf("ValidRunID(%q) = true", bad)
		}
	}
}

func state(root, item, version string, from, to journal.ItemState) journal.Event {
	return journal.Event{Ev: journal.EvState, Root: root, Item: item, Version: version, From: from, To: to}
}

func TestStartAppendRead(t *testing.T) {
	o := opts(t, newFS())
	runs := journal.NewRuns(o)
	if ids, err := runs.List(); err != nil || len(ids) != 0 {
		t.Fatalf("empty: %v %v", ids, err)
	}
	if _, ok, err := runs.Latest(); ok || err != nil {
		t.Fatalf("no latest: %v %v", ok, err)
	}
	run, err := runs.Start()
	if err != nil {
		t.Fatal(err)
	}
	if !journal.ValidRunID(run.ID) || !exists(filepath.Join(runs.Dir(), run.ID+".jsonl")) {
		t.Fatalf("run %q", run.ID)
	}
	events := []journal.Event{
		state("internal", "qwen-x", "v1", "", journal.StatePlanned),
		{Ev: journal.EvCkpt, Root: "internal", Item: "qwen-x", Version: "v1", File: "qwen-x.gguf", Offset: 0, ShaState: "c3RhdGU=", ETag: `"e1"`, LastModified: "Mon, 02 Nov 2026 03:00:00 GMT", URL: "https://example.test/a?x=1&y=2"},
		{Ev: journal.EvSwapPlan, Root: "internal", Item: "qwen-x", Version: "v1", Renames: [][2]string{{"/s/a", "/d/a"}}, Trash: [][2]string{{"/d/old", "/t/old"}}},
		{Ev: journal.EvSwapDone, Root: "internal", Item: "qwen-x", Version: "v1"},
		{Ev: "future_event", Item: "x"}, // an event of a part that is not written yet
	}
	for _, ev := range events {
		if err := run.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run.Append(events[0]); err == nil {
		t.Fatal("append after Close must fail")
	}
	if err := run.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}

	log, err := runs.Read(run.ID)
	if err != nil || log.Skipped != 0 || len(log.Events) != len(events) {
		t.Fatalf("%v %+v", err, log)
	}
	for i, ev := range log.Events {
		want := events[i]
		if ev.Ev != want.Ev || ev.Root != want.Root || ev.Item != want.Item || ev.Version != want.Version || ev.From != want.From || ev.To != want.To ||
			ev.File != want.File || ev.Offset != want.Offset || ev.ShaState != want.ShaState || ev.ETag != want.ETag || ev.URL != want.URL ||
			len(ev.Renames) != len(want.Renames) || len(ev.Trash) != len(want.Trash) {
			t.Errorf("event %d: got %+v want %+v", i, ev, want)
		}
		if ev.At.IsZero() {
			t.Errorf("event %d has no timestamp", i)
		}
	}
	raw := readFile(t, filepath.Join(runs.Dir(), run.ID+".jsonl"))
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != len(events) || !strings.HasPrefix(lines[0], `{"ev":"state","at":"`) {
		t.Fatalf("one compact line per event:\n%s", raw)
	}
	if !strings.Contains(lines[1], `"offset":0`) || !strings.Contains(lines[1], `x=1&y=2`) {
		t.Fatalf("ckpt line: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"renames":[["/s/a","/d/a"]]`) || !strings.Contains(lines[2], `"trash":[["/d/old","/t/old"]]`) {
		t.Fatalf("swap_plan line: %s", lines[2])
	}
	if id, ok, err := runs.Latest(); err != nil || !ok || id != run.ID {
		t.Fatalf("latest %q %v %v", id, ok, err)
	}
}

func TestAppendRefusesBadEvents(t *testing.T) {
	runs := journal.NewRuns(opts(t, newFS()))
	run, err := runs.Start()
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]journal.Event{
		"no ev":             {},
		"state no item":     {Ev: journal.EvState, Root: "internal", Version: "v1", To: journal.StatePlanned},
		"state bad root":    {Ev: journal.EvState, Root: "disk", Item: "a-a", Version: "v1", To: journal.StatePlanned},
		"state bad to":      {Ev: journal.EvState, Root: "internal", Item: "a-a", Version: "v1", To: "running"},
		"state bad from":    {Ev: journal.EvState, Root: "internal", Item: "a-a", Version: "v1", From: "x", To: journal.StatePlanned},
		"state no version":  {Ev: journal.EvState, Root: "internal", Item: "a-a", To: journal.StatePlanned},
		"ckpt no file":      {Ev: journal.EvCkpt, Root: "internal", Item: "a-a", Version: "v1"},
		"ckpt climbing":     {Ev: journal.EvCkpt, Root: "internal", Item: "a-a", Version: "v1", File: "../x"},
		"ckpt negative":     {Ev: journal.EvCkpt, Root: "internal", Item: "a-a", Version: "v1", File: "f", Offset: -1},
		"swap_done no root": {Ev: journal.EvSwapDone, Item: "a-a", Version: "v1"},
	}
	for name, ev := range bad {
		t.Run(name, func(t *testing.T) {
			if err := run.Append(ev); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if log, _ := runs.Read(run.ID); len(log.Events) != 0 {
		t.Fatalf("refused events must not be written: %+v", log.Events)
	}
}

// opLog records the order of file operations, to prove a line is synced before Append returns.
type opLog struct {
	*tfs
	ops *[]string
}

type opFile struct {
	config.File
	ops *[]string
}

func (f opFile) Write(p []byte) (int, error) {
	*f.ops = append(*f.ops, "write")
	return f.File.Write(p)
}
func (f opFile) Sync() error { *f.ops = append(*f.ops, "sync"); return f.File.Sync() }

func (o opLog) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	f, err := o.tfs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return opFile{f, o.ops}, nil
}

func TestAppendSyncsBeforeReturning(t *testing.T) {
	var ops []string
	runs := journal.NewRuns(opts(t, opLog{newFS(), &ops}))
	run, err := runs.Start()
	if err != nil {
		t.Fatal(err)
	}
	ops = nil
	if err := run.Append(state("internal", "a-a", "v1", "", journal.StatePlanned)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ops, ",") != "write,sync" {
		t.Fatalf("ops %v: one write of the whole line, then the fsync, then return", ops)
	}
}

func TestAppendFailureAndTornLine(t *testing.T) {
	for _, step := range []string{"write", "sync"} {
		t.Run(step, func(t *testing.T) {
			fsys := newFS()
			runs := journal.NewRuns(opts(t, fsys))
			run, err := runs.Start()
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Append(state("internal", "a-a", "v1", "", journal.StatePlanned)); err != nil {
				t.Fatal(err)
			}
			fsys.FailOn(step, 1)
			if err := run.Append(state("internal", "a-a", "v1", journal.StatePlanned, journal.StateResolving)); err == nil {
				t.Fatal("the failure must surface: the caller must not do the side effect")
			}
			if err := run.Append(state("internal", "a-a", "v1", journal.StatePlanned, journal.StateResolving)); err != nil {
				t.Fatal(err)
			}
			log, _ := runs.Read(run.ID)
			if len(log.Events) < 2 || log.Events[len(log.Events)-1].To != journal.StateResolving {
				t.Fatalf("the event after a failure must be readable: %+v", log)
			}
		})
	}
}

// tornFS writes only the first half of a line, then fails with ENOSPC (a full disk mid-write).
type tornFS struct{ *tfs }

type tornFile struct{ config.File }

func (f tornFile) Write(p []byte) (int, error) {
	n, _ := f.File.Write(p[:len(p)/2])
	return n, syscall.ENOSPC
}

func (t tornFS) OpenFile(name string, flag int, perm os.FileMode) (config.File, error) {
	f, err := t.tfs.OpenFile(name, flag, perm)
	if err != nil || flag&os.O_APPEND == 0 || !strings.HasSuffix(name, ".jsonl") {
		return f, err
	}
	return tornFile{f}, nil
}

func TestTornLineDoesNotSwallowTheNextEvent(t *testing.T) {
	o := opts(t, newFS())
	runs := journal.NewRuns(o)
	run, err := runs.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Append(state("internal", "a-a", "v1", "", journal.StatePlanned)); err != nil {
		t.Fatal(err)
	}
	// Reopen through a file system that tears the next line, then reopen normally.
	o2 := o
	o2.FS = tornFS{newFS()}
	torn, err := journal.NewRuns(o2).Open(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := torn.Append(state("internal", "a-a", "v1", journal.StatePlanned, journal.StateResolving)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("got %v", err)
	}
	again, err := runs.Open(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Append(state("internal", "a-a", "v1", journal.StatePlanned, journal.StateDownloading)); err != nil {
		t.Fatal(err)
	}
	log, err := runs.Read(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if log.Skipped != 1 || len(log.Events) != 2 || log.Events[1].To != journal.StateDownloading {
		t.Fatalf("the torn half line is skipped and the next event survives: %+v", log)
	}
}

func TestParseRunLogTolerance(t *testing.T) {
	good := `{"ev":"state","at":"2026-11-02T18:00:00Z","root":"internal","item":"a-a","version":"v1","to":"planned"}`
	log, err := journal.ParseRunLog([]byte(good + "\n\n  \n{not json\n" + `{"no_ev":1}` + "\n" + good + "\n" + `{"ev":"state","at":"2026-11`))
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Events) != 2 || log.Skipped != 3 {
		t.Fatalf("%+v", log)
	}
	// Unknown keys of an event are ignored, not errors.
	log, err = journal.ParseRunLog([]byte(`{"ev":"x","later":{"a":1}}` + "\n"))
	if err != nil || len(log.Events) != 1 || log.Events[0].Ev != "x" {
		t.Fatalf("%v %+v", err, log)
	}
}

func TestRunsReadErrors(t *testing.T) {
	runs := journal.NewRuns(opts(t, newFS()))
	if _, err := runs.Read("../x"); err == nil {
		t.Fatal("bad id")
	}
	if _, err := runs.Read("run-20261102T091500Z-a1f3"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := runs.Open("../x"); err == nil {
		t.Fatal("bad id")
	}
}

func TestStartPrunesToSixty(t *testing.T) {
	o := opts(t, newFS())
	runs := journal.NewRuns(o)
	var first string
	for i := 0; i < journal.KeepRuns+5; i++ {
		run, err := runs.Start()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = run.ID
		}
		_ = run.Close()
	}
	ids, err := runs.List()
	if err != nil || len(ids) != journal.KeepRuns {
		t.Fatalf("%d runs kept (%v)", len(ids), err)
	}
	if exists(filepath.Join(runs.Dir(), first+".jsonl")) {
		t.Fatal("the oldest run must be gone")
	}
	// Sorted oldest first; the newest is what Latest says.
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("not sorted: %v", ids)
		}
	}
	// Foreign files in the directory are left alone and not counted.
	writeFile(t, filepath.Join(runs.Dir(), "notes.txt"), "x")
	writeFile(t, filepath.Join(runs.Dir(), "run-bad.jsonl"), "x")
	if _, err := runs.Start(); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(runs.Dir(), "notes.txt")) || !exists(filepath.Join(runs.Dir(), "run-bad.jsonl")) {
		t.Fatal("pruning must touch only run logs")
	}
}

func TestStartFaults(t *testing.T) {
	for _, op := range []string{"open", "syncdir"} {
		t.Run(op, func(t *testing.T) {
			fsys := newFS()
			runs := journal.NewRuns(opts(t, fsys))
			fsys.FailOn(op, 1)
			if _, err := runs.Start(); err == nil {
				t.Fatal("the fault must surface")
			}
			if _, err := runs.Start(); err != nil {
				t.Fatalf("a later Start works: %v", err)
			}
		})
	}
	t.Run("id collision retries", func(t *testing.T) {
		o := opts(t, newFS())
		o.Rand = func(p []byte) (int, error) { p[0], p[1] = 0xab, 0xcd; return 2, nil } // always the same id
		o.Now = func() time.Time { return epoch }
		runs := journal.NewRuns(o)
		if _, err := runs.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Start(); err == nil {
			t.Fatal("eight collisions in a row must fail, not overwrite")
		}
		if ids, _ := runs.List(); len(ids) != 1 {
			t.Fatalf("%v", ids)
		}
	})
}

func TestStartPruneFailureIsNotFatal(t *testing.T) {
	fsys := newFS()
	o := opts(t, fsys)
	runs := journal.NewRuns(o)
	for i := 0; i < journal.KeepRuns; i++ {
		r, err := runs.Start()
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
	}
	fsys.FailOn("remove", -1)
	if _, err := runs.Start(); err != nil {
		t.Fatalf("a run log that cannot be removed must not stop the run: %v", err)
	}
	if ids, _ := runs.List(); len(ids) != journal.KeepRuns+1 {
		t.Fatalf("%d", len(ids))
	}
	_ = fake.ErrInjected
}

// --- Replay ---------------------------------------------------------------------------------------

func TestReplay(t *testing.T) {
	ck := func(item, version, file string, off int64, sha string) journal.Event {
		return journal.Event{Ev: journal.EvCkpt, Root: "internal", Item: item, Version: version, File: file, Offset: off, ShaState: sha}
	}
	plan := journal.Event{Ev: journal.EvSwapPlan, Root: "internal", Item: "a-a", Version: "v1", Renames: [][2]string{{"s", "d"}}, Trash: [][2]string{}}
	tests := []struct {
		name   string
		events []journal.Event
		check  func(t *testing.T, got map[string]journal.Intent)
	}{
		{"the last state event is where the item was going", []journal.Event{
			state("internal", "a-a", "v1", "", journal.StatePlanned),
			state("internal", "a-a", "v1", journal.StatePlanned, journal.StateResolving),
			state("internal", "a-a", "v1", journal.StateResolving, journal.StateDownloading),
		}, func(t *testing.T, got map[string]journal.Intent) {
			if in := got["internal/a-a"]; in.State != journal.StateDownloading || in.Version != "v1" || in.RootID != "internal" || in.ItemID != "a-a" {
				t.Fatalf("%+v", in)
			}
		}},
		{"latest checkpoint per file", []journal.Event{
			state("internal", "a-a", "v1", "", journal.StateDownloading),
			ck("a-a", "v1", "f1", 100, "s1"), ck("a-a", "v1", "f2", 50, "t1"), ck("a-a", "v1", "f1", 200, "s2"),
		}, func(t *testing.T, got map[string]journal.Intent) {
			in := got["internal/a-a"]
			if len(in.Checkpoints) != 2 || in.Checkpoints["f1"].Offset != 200 || in.Checkpoints["f1"].ShaState != "s2" || in.Checkpoints["f2"].Offset != 50 {
				t.Fatalf("%+v", in.Checkpoints)
			}
		}},
		{"a checkpoint with no state event still yields an intent without a state", []journal.Event{ck("a-a", "v1", "f", 5, "s")},
			func(t *testing.T, got map[string]journal.Intent) {
				if in := got["internal/a-a"]; in.State != "" || in.Checkpoints["f"].Offset != 5 {
					t.Fatalf("%+v", in)
				}
			}},
		{"a swap in progress", []journal.Event{state("internal", "a-a", "v1", journal.StateStaged, journal.StateSwapping), plan},
			func(t *testing.T, got map[string]journal.Intent) {
				in := got["internal/a-a"]
				if in.State != journal.StateSwapping || in.Swap == nil || len(in.Swap.Renames) != 1 || in.Swap.Renames[0] != [2]string{"s", "d"} {
					t.Fatalf("%+v", in)
				}
			}},
		{"a finished swap has no plan to resume", []journal.Event{
			state("internal", "a-a", "v1", journal.StateStaged, journal.StateSwapping), plan,
			{Ev: journal.EvSwapDone, Root: "internal", Item: "a-a", Version: "v1"},
			state("internal", "a-a", "v1", journal.StateSwapping, journal.StateSwapped),
		}, func(t *testing.T, got map[string]journal.Intent) {
			if in := got["internal/a-a"]; in.State != journal.StateSwapped || in.Swap != nil {
				t.Fatalf("%+v", in)
			}
		}},
		{"a new version starts the record afresh", []journal.Event{
			state("internal", "a-a", "v1", "", journal.StateDownloading), ck("a-a", "v1", "f", 100, "s"), plan,
			state("internal", "a-a", "v2", journal.StateVerified, journal.StatePlanned),
		}, func(t *testing.T, got map[string]journal.Intent) {
			if in := got["internal/a-a"]; in.Version != "v2" || in.State != journal.StatePlanned || len(in.Checkpoints) != 0 || in.Swap != nil {
				t.Fatalf("%+v", in)
			}
		}},
		{"planned again for the same version restarts it", []journal.Event{
			state("internal", "a-a", "v1", "", journal.StateDownloading), ck("a-a", "v1", "f", 100, "s"),
			state("internal", "a-a", "v1", journal.StateQuarantined, journal.StatePlanned),
		}, func(t *testing.T, got map[string]journal.Intent) {
			if in := got["internal/a-a"]; in.State != journal.StatePlanned || len(in.Checkpoints) != 0 {
				t.Fatalf("%+v", in)
			}
		}},
		{"items are independent, other events are ignored", []journal.Event{
			state("internal", "a-a", "v1", "", journal.StateDownloading),
			state("ark-aaaa0001", "a-a", "v1", "", journal.StateStaged),
			{Ev: "future", Root: "internal", Item: "a-a", Version: "v9"},
			{Ev: "run_note"},
		}, func(t *testing.T, got map[string]journal.Intent) {
			if len(got) != 2 || got["internal/a-a"].State != journal.StateDownloading || got["ark-aaaa0001/a-a"].State != journal.StateStaged || got["internal/a-a"].Version != "v1" {
				t.Fatalf("%+v", got)
			}
		}},
		{"nothing", nil, func(t *testing.T, got map[string]journal.Intent) {
			if len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, journal.Replay(tc.events)) })
	}
}

func TestApplyIntents(t *testing.T) {
	o := opts(t, newFS())
	it, _ := openItems(t, o)
	put := func(item string, st journal.ItemState, version string) {
		if _, err := it.Update("internal", item, func(i *journal.Item) {
			i.State, i.Version, i.Attempts, i.LastError = st, version, 2, "boom"
			i.Checkpoints = map[string]journal.Checkpoint{"f": {Offset: 10, ShaState: "old"}}
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("behind", journal.StateResolving, "v1")
	put("verified-same", journal.StateVerified, "v1")
	put("verified-newer-plan", journal.StateVerified, "v1")
	put("in-sync", journal.StateDownloading, "v1")

	intents := map[string]journal.Intent{
		"internal/behind":              {RootID: "internal", ItemID: "behind", Version: "v1", State: journal.StateDownloading, Checkpoints: map[string]journal.Checkpoint{"f": {Offset: 99, ShaState: "new"}}},
		"internal/verified-same":       {RootID: "internal", ItemID: "verified-same", Version: "v1", State: journal.StateSwapping},
		"internal/verified-newer-plan": {RootID: "internal", ItemID: "verified-newer-plan", Version: "v2", State: journal.StatePlanned},
		"internal/in-sync":             {RootID: "internal", ItemID: "in-sync", Version: "v1", State: journal.StateDownloading, Checkpoints: map[string]journal.Checkpoint{"f": {Offset: 10, ShaState: "old"}}},
		"internal/brand-new":           {RootID: "internal", ItemID: "brand-new", Version: "v1", State: journal.StateStaged},
		"internal/no-state":            {RootID: "internal", ItemID: "no-state", Version: "v1"},
		"internal/bad-state":           {RootID: "internal", ItemID: "bad-state", Version: "v1", State: "weird"},
	}
	n, err := it.ApplyIntents(intents)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // behind, verified-newer-plan, brand-new
		t.Fatalf("changed %d", n)
	}
	if r, _ := it.Get("internal", "behind"); r.State != journal.StateDownloading || r.Checkpoints["f"].Offset != 99 || r.Attempts != 2 {
		t.Fatalf("behind %+v", r)
	}
	if r, _ := it.Get("internal", "verified-same"); r.State != journal.StateVerified {
		t.Fatalf("verified is terminal for its version: %+v", r)
	}
	if r, _ := it.Get("internal", "verified-newer-plan"); r.State != journal.StatePlanned || r.Version != "v2" || r.Attempts != 0 || r.LastError != "" || r.Checkpoints != nil {
		t.Fatalf("a new version resets the counters: %+v", r)
	}
	if r, ok := it.Get("internal", "brand-new"); !ok || r.State != journal.StateStaged {
		t.Fatalf("brand-new %+v", r)
	}
	for _, k := range []string{"no-state", "bad-state"} {
		if _, ok := it.Get("internal", k); ok {
			t.Fatalf("%s must not be created", k)
		}
	}
	if n, err := it.ApplyIntents(intents); err != nil || n != 0 {
		t.Fatalf("applying twice: %d %v", n, err)
	}
}

// --- Crash-replay: the acceptance test ------------------------------------------------------------

// TestCrashBetweenAppendAndSideEffect kills the "agent" at every point of a scripted item lifetime,
// including between the journal append and the side effect (the items.json update), with the faults
// of the file system on top, and checks the write-ahead guarantee: after a restart the journal
// reconstructs a state that is exactly the last state whose append succeeded, never one ahead of
// the log, never one behind it.
func TestCrashBetweenAppendAndSideEffect(t *testing.T) {
	script := []journal.ItemState{
		journal.StatePlanned, journal.StateResolving, journal.StateDownloading, journal.StateVerifying,
		journal.StateStaged, journal.StateSwapping, journal.StateSwapped, journal.StateVerified,
	}
	// where: "none" (clean kill after n steps), "after-append" (appended, killed before the side
	// effect), "append-fails-*" (the append itself fails at its write or its fsync), "update-fails"
	// (append ok, the items.json write fails).
	for n := range script {
		for _, where := range []string{"none", "after-append", "append-fails-write", "append-fails-sync", "update-fails"} {
			t.Run(fmt.Sprintf("step=%d/%s", n, where), func(t *testing.T) {
				fsys := newFS()
				o := opts(t, fsys)
				j, rec, err := journal.Open(bg, o)
				if err != nil || rec.ReplayedRun != "" {
					t.Fatalf("%v %+v", err, rec)
				}
				run, err := j.Runs.Start()
				if err != nil {
					t.Fatal(err)
				}
				lastAppended := journal.ItemState("")
				step := func(to journal.ItemState) (error, bool) {
					if err := run.Append(state("internal", "qwen-x", "v1", lastAppended, to)); err != nil {
						return err, false
					}
					lastAppended = to
					return nil, true
				}
				for i := 0; i < n; i++ { // n clean steps: append, then side effect
					if _, ok := step(script[i]); !ok {
						t.Fatal("clean step failed")
					}
					if _, err := j.Items.Update("internal", "qwen-x", setState(script[i], "v1")); err != nil {
						t.Fatal(err)
					}
				}
				to := script[n]
				switch where {
				case "after-append":
					if _, ok := step(to); !ok {
						t.Fatal("append failed")
					}
					// killed here: the side effect never happens
				case "append-fails-write", "append-fails-sync":
					fsys.FailOn(strings.TrimPrefix(where, "append-fails-"), 1)
					if err, ok := step(to); ok || err == nil {
						t.Fatal("the append must fail")
					}
					// the caller must not perform the side effect. After a failed write the line is not
					// in the log; after a failed fsync it may be (written, not yet durable).
				case "update-fails":
					if _, ok := step(to); !ok {
						t.Fatal("append failed")
					}
					fsys.Crash = true
					fsys.FailOn("rename", -1) // the .bak copy and the real write both fail
					if _, err := j.Items.Update("internal", "qwen-x", setState(to, "v1")); err == nil {
						t.Fatal("the update must fail")
					}
				}

				// Restart.
				restarted := o
				restarted.FS = newFS()
				j2, rec2, err := journal.Open(bg, restarted)
				if err != nil {
					t.Fatal(err)
				}
				got, ok := j2.Items.Get("internal", "qwen-x")
				want := lastAppended // the log's last state
				if where == "append-fails-sync" {
					want = to // the line was written and may be durable: the intent is then the failed append's state
				}
				if where == "append-fails-sync" && !ok {
					t.Fatalf("no record after a written line: %+v", rec2)
				}
				if want == "" { // nothing was ever journaled: nothing to reconstruct
					if ok && got.State != "" {
						t.Fatalf("record without a journal event: %+v", got)
					}
					return
				}
				if !ok || got.State != want {
					t.Fatalf("recovered %q, the journal's last state is %q (%+v)", got.State, want, rec2)
				}
				if in := rec2.Intents["internal/qwen-x"]; in.State != want || in.Version != "v1" {
					t.Fatalf("intent %+v", in)
				}
				lastAppended = want
				// Recovering again changes nothing.
				j3, rec3, err := journal.Open(bg, restarted)
				if err != nil || rec3.Applied != 0 {
					t.Fatalf("second recovery: %v %+v", err, rec3)
				}
				if again, _ := j3.Items.Get("internal", "qwen-x"); again.State != lastAppended {
					t.Fatalf("%+v", again)
				}
			})
		}
	}
}

func TestOpenReplaysCheckpointsAndTheSwapPlan(t *testing.T) {
	o := opts(t, newFS())
	j, _, err := journal.Open(bg, o)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := j.Runs.Start()
	for _, ev := range []journal.Event{
		state("internal", "qwen-x", "v1", "", journal.StateDownloading),
		{Ev: journal.EvCkpt, Root: "internal", Item: "qwen-x", Version: "v1", File: "qwen-x.gguf", Offset: journal.CheckpointEvery, ShaState: "c3RhdGU="},
		{Ev: journal.EvCkpt, Root: "internal", Item: "qwen-x", Version: "v1", File: "qwen-x.gguf", Offset: 2 * journal.CheckpointEvery, ShaState: "bmV3ZXI="},
		state("internal", "wiki-x", "2026-10", journal.StateStaged, journal.StateSwapping),
		{Ev: journal.EvSwapPlan, Root: "internal", Item: "wiki-x", Version: "2026-10", Renames: [][2]string{{"/s/w.zim", "/d/w.zim"}}, Trash: [][2]string{}},
	} {
		if err := run.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	// A torn last line, as after a power cut.
	f, err := os.OpenFile(filepath.Join(j.Runs.Dir(), run.ID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"ev":"ckpt","at":"2026-11-02T18:0`)
	_ = f.Close()

	_, rec, err := journal.Open(bg, o)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ReplayedRun != run.ID || rec.Skipped != 1 || rec.Applied != 2 {
		t.Fatalf("%+v", rec)
	}
	q := rec.Intents["internal/qwen-x"]
	if q.State != journal.StateDownloading || q.Checkpoints["qwen-x.gguf"].Offset != 2*journal.CheckpointEvery || q.Checkpoints["qwen-x.gguf"].ShaState != "bmV3ZXI=" {
		t.Fatalf("%+v", q)
	}
	w := rec.Intents["internal/wiki-x"]
	if w.State != journal.StateSwapping || w.Swap == nil || w.Swap.Renames[0] != [2]string{"/s/w.zim", "/d/w.zim"} {
		t.Fatalf("%+v", w)
	}
	j2, _, _ := journal.Open(bg, o)
	if r, _ := j2.Items.Get("internal", "qwen-x"); r.Checkpoints["qwen-x.gguf"].Offset != 2*journal.CheckpointEvery {
		t.Fatalf("checkpoints must reach items.json: %+v", r)
	}
}

func TestTransition(t *testing.T) {
	fsys := newFS()
	o := opts(t, fsys)
	j, _, err := journal.Open(bg, o)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := j.Runs.Start()
	rec, err := j.Transition(run, "internal", "qwen-x", "v1", journal.StatePlanned, nil)
	if err != nil || rec.State != journal.StatePlanned {
		t.Fatalf("%v %+v", err, rec)
	}
	rec, err = j.Transition(run, "internal", "qwen-x", "v1", journal.StateFailed, func(i *journal.Item) {
		i.Attempts++
		i.Code, i.LastError = "LAI-SYN-031", "boom"
	})
	if err != nil || rec.State != journal.StateFailed || rec.Attempts != 1 || rec.Code != "LAI-SYN-031" {
		t.Fatalf("%v %+v", err, rec)
	}
	log, _ := j.Runs.Read(run.ID)
	if len(log.Events) != 2 || log.Events[1].From != journal.StatePlanned || log.Events[1].To != journal.StateFailed {
		t.Fatalf("the event carries both ends of the transition: %+v", log.Events)
	}

	// When the append fails the record is not touched.
	fsys.FailOn("sync", 1)
	if _, err := j.Transition(run, "internal", "qwen-x", "v1", journal.StateDownloading, nil); err == nil {
		t.Fatal("the append failure must surface")
	}
	if cur, _ := j.Items.Get("internal", "qwen-x"); cur.State != journal.StateFailed {
		t.Fatalf("write-ahead: the record must not move when the journal could not be written: %+v", cur)
	}
}

func TestOpenFailsWhenTheLogCannotBeRead(t *testing.T) {
	fsys := newFS()
	o := opts(t, fsys)
	j, _, err := journal.Open(bg, o)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := j.Runs.Start()
	_ = run.Append(state("internal", "a-a", "v1", "", journal.StatePlanned))
	if _, err := j.Items.Update("internal", "a-a", setState(journal.StatePlanned, "v1")); err != nil {
		t.Fatal(err)
	}
	bad := o
	f := newFS()
	f.FailNth("readfile", 2) // 1st: items.json, 2nd: the run log
	bad.FS = f
	if _, _, err := journal.Open(bg, bad); err == nil {
		t.Fatal("an unreadable run log must fail Open: replaying nothing would lose the recovery")
	}
}
