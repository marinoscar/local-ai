package drill

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func docAt(i int, st State, trig Trigger) *Document {
	at := t0.Add(time.Duration(i) * time.Hour)
	d := &Document{
		SchemaVersion: SchemaVersion, RunID: fmt.Sprintf("drl-%s-%04x", at.Format("20060102T150405Z"), i),
		Trigger: trig, State: st, NodeID: "ai-n3", StartedAt: at, FinishedAt: &at, Overall: OverallPass, Checks: []CheckResult{},
	}
	if st == StateSkipped {
		d.Overall = OverallSkipped
	}
	return d
}

func newStore(keep int) (*Results, *memFS) {
	m := newMemFS()
	return NewResults(ResultsOptions{FS: m, Dir: testDir, Keep: keep}), m
}

func TestValidRunID(t *testing.T) {
	for id, want := range map[string]bool{
		"drl-20261101T043512Z-9c1e":      true,
		"drl-20261102T150000Z-manual":    true,
		"drl-20261101T043512Z-9C1E":      false,
		"drl-20261101T043512Z-9c1":       false,
		"drl-20261101T043512Z-9c1e/..":   false,
		"../drl-20261101T043512Z-9c1e":   false,
		"latest":                         false,
		"":                               false,
		"drl-20261101T043512Z-9c1e.json": false,
	} {
		if got := ValidRunID(id); got != want {
			t.Errorf("ValidRunID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestResultsSaveGetLatest(t *testing.T) {
	s, m := newStore(0)
	d := docAt(1, "", TriggerManual)
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Get(d.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var back Document
	if err := json.Unmarshal(raw, &back); err != nil || back.RunID != d.RunID {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	latest, err := s.Latest()
	if err != nil || string(latest) != string(raw) {
		t.Fatalf("Latest = %v, want the saved document (err %v)", string(latest), err)
	}
	target, err := m.Readlink(testDir + "/latest.json")
	if err != nil || target != d.RunID+".json" {
		t.Fatalf("latest.json -> %q, %v", target, err)
	}
	if _, ok := m.nodes[testDir+"/"+d.RunID+".json"]; !ok || m.nodes[testDir+"/"+d.RunID+".json"].mode != 0o640 {
		t.Fatal("the document must exist with mode 0640")
	}
	if _, err := s.Get("drl-20260101T000000Z-aaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a run that is not kept = %v, want ErrNotFound", err)
	}
	if _, err := s.Get("../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a path = %v, want ErrNotFound", err)
	}
}

func TestResultsLatestEmpty(t *testing.T) {
	s, _ := newStore(0)
	if _, err := s.Latest(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest on an empty store = %v, want ErrNotFound", err)
	}
}

func TestResultsRetentionKeepsLastTwentyFour(t *testing.T) {
	s, m := newStore(0)
	var last *Document
	for i := 1; i <= 30; i++ {
		last = docAt(i, "", TriggerScheduled)
		if err := s.Save(last); err != nil {
			t.Fatal(err)
		}
	}
	runs := hasPrefixName(m.listing(testDir), "drl-")
	if runs != DefaultKeep {
		t.Fatalf("%d documents kept, want %d", runs, DefaultKeep)
	}
	if _, err := s.Get(docAt(6, "", TriggerScheduled).RunID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the 6th run must have been pruned")
	}
	if _, err := s.Get(docAt(7, "", TriggerScheduled).RunID); err != nil {
		t.Fatalf("the 7th run must be kept: %v", err)
	}
	target, _ := m.Readlink(testDir + "/latest.json")
	if target != last.RunID+".json" {
		t.Fatalf("latest.json -> %q, want the last run", target)
	}
}

func TestResultsSkippedRunsDoNotMoveLatestAndKeepOne(t *testing.T) {
	s, m := newStore(3)
	real := docAt(1, "", TriggerScheduled)
	if err := s.Save(real); err != nil {
		t.Fatal(err)
	}
	var lastSkip *Document
	for i := 2; i <= 6; i++ {
		lastSkip = docAt(i, StateSkipped, TriggerScheduled)
		if err := s.Save(lastSkip); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := m.Readlink(testDir + "/latest.json")
	if target != real.RunID+".json" {
		t.Fatalf("a skipped run moved latest.json to %q", target)
	}
	names := m.listing(testDir)
	if hasPrefixName(names, "drl-") != 2 {
		t.Fatalf("want the real run and the newest skipped one, have %v", names)
	}
	if _, err := s.Get(lastSkip.RunID); err != nil {
		t.Fatalf("the newest skipped document must be kept: %v", err)
	}
	// A burst of skips never pushes a real result out of the retention.
	for i := 7; i <= 20; i++ {
		_ = s.Save(docAt(i, StateSkipped, TriggerScheduled))
	}
	if _, err := s.Get(real.RunID); err != nil {
		t.Fatalf("skipped runs pruned a real result: %v", err)
	}
}

func TestResultsManualPlugDoesNotMoveLatest(t *testing.T) {
	s, m := newStore(0)
	auto := docAt(1, "", TriggerScheduled)
	plug := docAt(2, "", TriggerManualPlug)
	for _, d := range []*Document{auto, plug} {
		if err := s.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := m.Readlink(testDir + "/latest.json")
	if target != auto.RunID+".json" {
		t.Fatalf("a manual_plug entry moved latest.json to %q", target)
	}
}

func TestResultsLatestFallsBackWhenLinkIsMissingOrDangling(t *testing.T) {
	s, m := newStore(0)
	a, b := docAt(1, "", TriggerScheduled), docAt(2, "", TriggerManual)
	skip := docAt(3, StateSkipped, TriggerScheduled)
	for _, d := range []*Document{a, b, skip} {
		if err := s.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(){
		"link removed": func() { delete(m.nodes, testDir+"/latest.json") },
		"link dangling": func() {
			m.nodes[testDir+"/latest.json"] = &memNode{link: "drl-20200101T000000Z-dead.json", isLink: true}
		},
	} {
		mutate()
		raw, err := s.Latest()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var d Document
		_ = json.Unmarshal(raw, &d)
		if d.RunID != b.RunID {
			t.Fatalf("%s: Latest = %s, want the newest finished run %s (not the skipped one)", name, d.RunID, b.RunID)
		}
	}
}

func TestResultsPruneNeverDropsTheLatestTarget(t *testing.T) {
	s, m := newStore(2)
	auto := docAt(1, "", TriggerScheduled)
	if err := s.Save(auto); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 5; i++ {
		if err := s.Save(docAt(i, "", TriggerManualPlug)); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := m.Readlink(testDir + "/latest.json")
	if target != auto.RunID+".json" {
		t.Fatalf("latest.json -> %q", target)
	}
	if _, err := s.Get(auto.RunID); err != nil {
		t.Fatalf("the document latest.json names was pruned: %v", err)
	}
}

func TestResultsCheckpointAndPartial(t *testing.T) {
	s, m := newStore(0)
	d := docAt(1, StateAborted, TriggerManual)
	if err := s.Checkpoint(d); err != nil {
		t.Fatal(err)
	}
	back, err := s.Partial(d.RunID)
	if err != nil || back.RunID != d.RunID || back.State != StateAborted {
		t.Fatalf("Partial = %+v, %v", back, err)
	}
	if _, err := s.Get(d.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a checkpoint must not be served as a result")
	}
	if _, err := s.Latest(); !errors.Is(err, ErrNotFound) {
		t.Fatal("a checkpoint must not be the latest result")
	}
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Partial(d.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatal("Save must remove the run's checkpoint")
	}
	if hasPrefixName(m.listing(testDir), "drl-") != 1 {
		t.Fatalf("unexpected files %v", m.listing(testDir))
	}
	if _, err := s.Partial("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("Partial of a bad id must be ErrNotFound")
	}
}

func TestResultsPruneSweepsTempFilesAndOrphanCheckpoints(t *testing.T) {
	s, m := newStore(0)
	m.seed(testDir+"/latest.json.tmp-abc", "x")
	m.seed(testDir+"/drl-20261101T000000Z-aaaa.json.tmp-abc", "x")
	done := docAt(1, "", TriggerManual)
	m.seed(testDir+"/"+done.RunID+".partial", "{}")
	live := docAt(9, StateAborted, TriggerManual)
	if err := s.Checkpoint(live); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(done); err != nil {
		t.Fatal(err)
	}
	left := m.listing(testDir)
	want := []string{done.RunID + ".json", live.RunID + ".partial", "latest.json"}
	if strings.Join(sortedCopy(left), ",") != strings.Join(sortedCopy(want), ",") {
		t.Fatalf("left %v, want %v (the checkpoint of a run still without a document stays)", left, want)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestResultsWriteFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   string
	}{{"disk full on write", "write"}, {"rename refused", "rename"}, {"mkdir refused", "mkdir"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := newStore(0)
			m.fail = func(op, _ string) error {
				if op == tc.op {
					return syscall.ENOSPC
				}
				return nil
			}
			err := s.Save(docAt(1, "", TriggerManual))
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("Save = %v, want ENOSPC", err)
			}
			if hasPrefixName(m.listing(testDir), "drl-") != 0 {
				t.Fatalf("a failed save left %v", m.listing(testDir))
			}
		})
	}
}

func TestResultsLinkFailureKeepsTheDocumentAndLatestFallsBack(t *testing.T) {
	s, m := newStore(0)
	m.fail = func(op, _ string) error {
		if op == "symlink" {
			return syscall.EACCES
		}
		return nil
	}
	d := docAt(1, "", TriggerManual)
	if err := s.Save(d); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("Save = %v, want EACCES from the link", err)
	}
	if _, err := s.Get(d.RunID); err != nil {
		t.Fatalf("the document must be on disk even when the link failed: %v", err)
	}
	if _, err := s.Latest(); err != nil {
		t.Fatalf("Latest must fall back to the directory: %v", err)
	}
}

func TestResultsRejectsBadRunID(t *testing.T) {
	s, _ := newStore(0)
	d := docAt(1, "", TriggerManual)
	d.RunID = "../escape"
	if err := s.Save(d); err == nil {
		t.Fatal("Save must refuse a run id that is not a run id")
	}
	if err := s.Checkpoint(d); err == nil {
		t.Fatal("Checkpoint must refuse a run id that is not a run id")
	}
}

// TestResultsRealFilesystem runs the store over the real filesystem: the document is mode 0640, the
// latest.json symlink is relative and resolves, and retention removes files.
func TestResultsRealFilesystem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "drill")
	s := NewResults(ResultsOptions{Dir: dir, Keep: 2})
	var ids []string
	for i := 1; i <= 3; i++ {
		d := docAt(i, "", TriggerScheduled)
		ids = append(ids, d.RunID)
		if err := s.Save(d); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(dir, ids[2]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("document mode = %v, want 0640", fi.Mode().Perm())
	}
	target, err := os.Readlink(filepath.Join(dir, LatestName))
	if err != nil || target != ids[2]+".json" {
		t.Fatalf("latest.json -> %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ids[0]+".json")); !os.IsNotExist(err) {
		t.Fatalf("the oldest document must be pruned: %v", err)
	}
	raw, err := s.Latest()
	if err != nil {
		t.Fatal(err)
	}
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil || d.RunID != ids[2] {
		t.Fatalf("Latest = %v, %v", d.RunID, err)
	}
}
