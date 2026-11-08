package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/sync/verify"
)

func (r *contentRig) manifestCheck() drill.Check {
	return NewManifestCheck(ManifestDeps{Roots: r.rootList, FS: config.OSFS{}, Ready: r.ready})
}

func detailList(t *testing.T, d map[string]any, key string) []string {
	t.Helper()
	v, ok := d[key].([]string)
	if !ok {
		t.Fatalf("details[%q] = %#v, want a []string (details %v)", key, d[key], d)
	}
	return v
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- check 3 ---------------------------------------------------------------------------------------

func TestManifestPassesWhenEveryWantedItemIsThere(t *testing.T) {
	r := newContentRig(t)
	r.installNode()
	out := run(t, r.manifestCheck())
	if out.Result != drill.ResultPass || out.Code != "" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestManifestIsCriticalAndNumbered(t *testing.T) {
	c := newContentRig(t).manifestCheck()
	if c.ID != "drl.files.manifest" || !c.Critical || c.Order != 3 || c.FailCode != doctor.DRL011 {
		t.Errorf("check = %+v", c)
	}
}

func TestManifestFaults(t *testing.T) {
	cases := []struct {
		name     string
		fault    func(t *testing.T, r *contentRig)
		result   drill.Result
		code     string
		tier1    []string // missing_tier1 entries
		detailsK string   // a details key that must be present
	}{
		{
			name: "a tier-1 item's file is gone",
			fault: func(t *testing.T, r *contentRig) {
				mustRemove(t, filepath.Join(r.root(ark.InternalID).Path, "kiwix", refZimTier1.ID, "data.bin"))
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/" + refZimTier1.ID},
		},
		{
			name: "a tier-1 item has the wrong size",
			fault: func(t *testing.T, r *contentRig) {
				mustWrite(t, filepath.Join(r.root(ark.InternalID).Path, "speech", "whisper", refWhisper.ID, "data.bin"), []byte("short"))
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/" + refWhisper.ID},
		},
		{
			name: "a tier-1 item's directory is gone",
			fault: func(t *testing.T, r *contentRig) {
				mustRemoveAll(t, filepath.Join(r.root(ark.InternalID).Path, "maps", refMap.ID))
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/" + refMap.ID},
		},
		{
			name: "a tier-1 item's manifest is damaged",
			fault: func(t *testing.T, r *contentRig) {
				mustWrite(t, filepath.Join(r.root(ark.InternalID).Path, "models", refSurvivalModel.ID, "MANIFEST.json"), []byte("{not json"))
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/" + refSurvivalModel.ID},
		},
		{
			name: "a tier-1 item the profile wants was never downloaded",
			fault: func(_ *testing.T, r *contentRig) {
				r.declare(ark.InternalID, ready.Item{ID: "zim-first-aid", Kind: "kiwix", Tier: 1, Status: ready.StatusMissing})
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/zim-first-aid"},
		},
		{
			name: "a tier-1 item was skipped for lack of space",
			fault: func(_ *testing.T, r *contentRig) {
				r.declare(ark.InternalID, ready.Item{ID: "zim-big", Kind: "kiwix", Tier: 1, Status: ready.StatusSkipped, Reason: ready.ReasonDisk})
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/zim-big"},
		},
		{
			name: "a tier-1 item was quarantined",
			fault: func(_ *testing.T, r *contentRig) {
				r.declare(ark.InternalID, ready.Item{ID: refZimTier1.ID, Kind: "kiwix", Tier: 1, Status: ready.StatusQuarantined})
			},
			result: drill.ResultFail, code: doctor.DRL011, tier1: []string{"internal/" + refZimTier1.ID},
		},
		{
			name: "a tier-2 item is missing",
			fault: func(t *testing.T, r *contentRig) {
				mustRemoveAll(t, filepath.Join(r.root(ark.InternalID).Path, "kiwix", refZimTier2.ID))
			},
			result: drill.ResultWarn, detailsK: "roots",
		},
		{
			name: "an item is stale",
			fault: func(_ *testing.T, r *contentRig) {
				r.declare(ark.InternalID, ready.Item{ID: refZimTier1.ID, Kind: "kiwix", Tier: 1, Status: ready.StatusStale, Version: "v0"})
			},
			result: drill.ResultWarn, detailsK: "roots",
		},
		{
			name: "an orphan is on the root",
			fault: func(_ *testing.T, r *contentRig) {
				r.declare(ark.InternalID, ready.Item{ID: "old-model", Kind: "model", Tier: 3, Status: ready.StatusOrphan})
			},
			result: drill.ResultWarn, detailsK: "roots",
		},
		{
			name: "a directory that is no item (damaged manifest of an unwanted item)",
			fault: func(t *testing.T, r *contentRig) {
				dir := filepath.Join(r.root(ark.InternalID).Path, "kiwix", "junk")
				mustMkdir(t, dir)
				mustWrite(t, filepath.Join(dir, "MANIFEST.json"), []byte("{"))
			},
			result: drill.ResultWarn, detailsK: "roots",
		},
		{
			name: "the internal root is not mounted",
			fault: func(_ *testing.T, r *contentRig) {
				r.roots[0].State = ark.StateNotMounted
			},
			result: drill.ResultFail, code: doctor.DRL011,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newContentRig(t)
			r.installNode()
			tc.fault(t, r)
			out := run(t, r.manifestCheck())
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if tc.tier1 != nil {
				got := detailList(t, out.Details, "missing_tier1")
				for _, want := range tc.tier1 {
					if !contains(got, want) {
						t.Errorf("missing_tier1 = %v, want it to contain %s", got, want)
					}
				}
			}
			if tc.detailsK != "" {
				if _, ok := out.Details[tc.detailsK]; !ok {
					t.Errorf("details = %v, want key %q", out.Details, tc.detailsK)
				}
			}
		})
	}
}

// TestManifestDriveAbsentIsNotAFailure: the internal root stands alone. A drive that is not plugged in
// is not read, so nothing it would hold can fail the check; a plugged drive is read the same way.
func TestManifestDrives(t *testing.T) {
	t.Run("drive absent", func(t *testing.T) {
		r := newContentRig(t)
		r.installNode()
		r.unplug()
		out := run(t, r.manifestCheck())
		if out.Result != drill.ResultPass || out.Details["drive_roots"] != 0 {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("drive not mounted is left out", func(t *testing.T) {
		r := newContentRig(t)
		r.installNode()
		r.roots[1].State = ark.StateNotMounted
		if out := run(t, r.manifestCheck()); out.Result != drill.ResultPass || out.Details["drive_roots"] != 0 {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("drive present and complete", func(t *testing.T) {
		r := newContentRig(t)
		r.installNode()
		r.install(driveID, itemSpec{ref: refZimTier1, tier: 1})
		if out := run(t, r.manifestCheck()); out.Result != drill.ResultPass || out.Details["drive_roots"] != 1 {
			t.Fatalf("outcome = %+v", out)
		}
	})
	t.Run("drive present with a tier-1 item gone", func(t *testing.T) {
		r := newContentRig(t)
		r.installNode()
		dir := r.install(driveID, itemSpec{ref: refZimTier1, tier: 1})
		mustRemove(t, filepath.Join(dir, "data.bin"))
		out := run(t, r.manifestCheck())
		if out.Result != drill.ResultFail || out.Code != doctor.DRL011 {
			t.Fatalf("outcome = %+v", out)
		}
		if got := detailList(t, out.Details, "missing_tier1"); !contains(got, driveID+"/"+refZimTier1.ID) {
			t.Errorf("missing_tier1 = %v", got)
		}
	})
}

func TestManifestWithoutReadinessStillFindsADamagedDirectory(t *testing.T) {
	r := newContentRig(t)
	r.installNode()
	r.ready.reports = map[string]ready.Report{}
	if out := run(t, r.manifestCheck()); out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
	mustWrite(t, filepath.Join(r.root(ark.InternalID).Path, "models", refOtherModel.ID, "MANIFEST.json"), []byte("{"))
	out := run(t, r.manifestCheck())
	if out.Result != drill.ResultWarn {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestManifestScanErrorAndNoRoots(t *testing.T) {
	r := newContentRig(t)
	boom := errors.New("drive hung")
	c := NewManifestCheck(ManifestDeps{
		Roots: r.rootList, Ready: r.ready,
		Scan: func(context.Context, config.FS, ark.Root) ([]ark.ScannedItem, []ark.Problem, error) { return nil, nil, boom },
	})
	if out := run(t, c); out.Result != drill.ResultFail || out.Code != doctor.DRL011 {
		t.Errorf("scan error: %+v", out)
	}
	if out := run(t, NewManifestCheck(ManifestDeps{})); out.Result != drill.ResultSkip {
		t.Errorf("no roots port: %+v", out)
	}
}

// ---- check 4 ---------------------------------------------------------------------------------------

func (r *contentRig) hashCheck(d HashDeps) drill.Check {
	if d.Verifier == nil {
		d.Verifier = r.rv
	}
	d.Roots, d.Ready = r.rootList, r.ready
	if d.ModelEngine == nil {
		d.ModelEngine = engineOf
	}
	if d.NodeKit == nil {
		d.NodeKit = func(context.Context) []string { return []string{refKit.ID} }
	}
	d.Now = r.clk.Now
	return NewHashCheck(d)
}

func TestHashIsCriticalSkippedInQuickAndBudgeted(t *testing.T) {
	c := newContentRig(t).hashCheck(HashDeps{})
	if c.ID != "drl.files.hash" || !c.Critical || !c.SkipInQuick || c.Order != 4 || c.FailCode != doctor.DRL012 {
		t.Errorf("check = %+v", c)
	}
	if c.Timeout <= 90*time.Minute {
		t.Errorf("timeout = %v, want more than the 90 minute budget", c.Timeout)
	}
}

func TestMustHashItems(t *testing.T) {
	rep := ready.Report{Items: []ready.Item{
		{ID: "a-survival", Kind: "model", Tier: 2, Survival: true, Status: ready.StatusVerified},
		{ID: "a-spare", Kind: "model", Tier: 2, Status: ready.StatusVerified},
		{ID: "a-whisper", Kind: "model", Tier: 3, Status: ready.StatusVerified},
		{ID: "a-voice", Kind: "model", Tier: 3, Status: ready.StatusStale},
		{ID: "a-map", Kind: "maps", Tier: 1, Status: ready.StatusVerified},
		{ID: "a-map-t2", Kind: "maps", Tier: 2, Status: ready.StatusVerified},
		{ID: ark.MapAssetsID, Kind: "maps", Tier: 2, Status: ready.StatusVerified},
		{ID: "a-zim", Kind: "kiwix", Tier: 1, Status: ready.StatusVerified},
		{ID: "a-zim-t2", Kind: "kiwix", Tier: 2, Status: ready.StatusVerified},
		{ID: "a-kit", Kind: "kit", Tier: 1, Status: ready.StatusVerified},
		{ID: "a-kit-other", Kind: "kit", Tier: 1, Status: ready.StatusVerified},
		{ID: "a-gone-model", Kind: "model", Tier: 1, Survival: true, Status: ready.StatusMissing},
		{ID: "an-orphan", Kind: "kiwix", Tier: 1, Status: ready.StatusOrphan},
	}}
	engine := func(id string) string {
		return map[string]string{"a-whisper": "whisper", "a-voice": "piper"}[id]
	}
	got := MustHashItems(rep, engine, []string{"a-kit"})
	want := []string{"a-kit", "a-map", "a-survival", "a-voice", "a-whisper", "a-zim", ark.MapAssetsID}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("%s is not in %v", w, got)
		}
	}
}

func TestHashPassesAndReadsWhatMustBeIntact(t *testing.T) {
	r := newContentRig(t)
	r.installNode()
	out := run(t, r.hashCheck(HashDeps{}))
	if out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
	// Everything but the spare model and the tier-2 ZIM is always hashed; those two are older than
	// reverify_days, so they are hashed too: nine items.
	if out.Details["verified"] != int64(9) {
		t.Errorf("verified = %v, want 9 (%v)", out.Details["verified"], out.Details)
	}
}

func TestHashOtherItemsOnlyWhenDue(t *testing.T) {
	r := newContentRig(t)
	recent := epoch.Add(-24 * time.Hour)
	r.install(ark.InternalID, itemSpec{ref: refSurvivalModel, tier: 1, survival: true, lastRe: recent})
	r.install(ark.InternalID, itemSpec{ref: refOtherModel, tier: 2, lastRe: recent})
	r.install(ark.InternalID, itemSpec{ref: refZimTier2, tier: 2, lastRe: epoch.Add(-90 * 24 * time.Hour)})
	out := run(t, r.hashCheck(HashDeps{ReverifyDays: func() int { return 30 }}))
	if out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
	// The survival model whatever its age, the stale tier-2 ZIM because it is due, not the spare model.
	if out.Details["verified"] != int64(2) {
		t.Errorf("verified = %v, want 2", out.Details["verified"])
	}
}

func TestHashFindsACorruptByteAndArkSyncQuarantinesIt(t *testing.T) {
	cases := []struct {
		name string
		ref  ark.ItemRef
		dir  string
	}{
		{"a survival model", refSurvivalModel, "models"},
		{"a whisper model", refWhisper, filepath.Join("speech", "whisper")},
		{"a voice", refPiper, filepath.Join("speech", "piper")},
		{"the map", refMap, "maps"},
		{"the map assets", refMapAssets, "maps"},
		{"a tier-1 ZIM", refZimTier1, "kiwix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newContentRig(t)
			r.installNode()
			rel, _ := ark.ItemDir(tc.ref)
			file := filepath.Join(r.root(ark.InternalID).Path, filepath.FromSlash(rel), "data.bin")
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			b[len(b)/2] ^= 0x01 // one flipped bit; the size is unchanged
			mustWrite(t, file, b)

			out := run(t, r.hashCheck(HashDeps{}))
			if out.Result != drill.ResultFail || out.Code != doctor.DRL012 {
				t.Fatalf("outcome = %+v", out)
			}
			damaged, _ := out.Details["damaged"].([]map[string]any)
			if len(damaged) != 1 || damaged[0]["item"] != tc.ref.ID {
				t.Fatalf("damaged = %v", out.Details["damaged"])
			}
			// ark-sync's own rules set the copy aside: the drill moved nothing itself.
			if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the damaged copy is still in place (%v)", err)
			}
			q := filepath.Join(r.root(ark.InternalID).Path, ".staging", "quarantine", tc.ref.ID)
			if _, err := os.Stat(q); err != nil {
				t.Errorf("no quarantine directory: %v", err)
			}
		})
	}
}

func TestHashBudgetRunsOutAndLeftoversAreSkipped(t *testing.T) {
	r := newContentRig(t)
	r.installNode()
	// Every file read costs ten minutes: the 90 minute budget is spent after the ninth item's file.
	rv := r.verifier(10 * time.Minute)
	c := r.hashCheck(HashDeps{Verifier: rv, Budget: 25 * time.Minute})
	out := run(t, c)
	if out.Result != drill.ResultWarn || out.Code != doctor.DRL012 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["budget_exhausted"] != true {
		t.Errorf("details = %v", out.Details)
	}
	skipped, _ := out.Details["skipped"].([]map[string]any)
	if len(skipped) == 0 || skipped[0]["reason"] != verify.ReasonTimeLimit {
		t.Errorf("skipped = %v", out.Details["skipped"])
	}
	if v, _ := out.Details["verified"].(int64); v < 2 || v > 4 {
		t.Errorf("verified = %v, want a few before the budget ended", out.Details["verified"])
	}
}

func TestHashDriveCopiesAreHashedWhenDueAndAnAbsentDriveIsNotAFailure(t *testing.T) {
	build := func(plugged bool) *contentRig {
		r := newContentRig(t)
		r.installNode()
		r.install(driveID, itemSpec{ref: refZimTier2, tier: 2, lastRe: epoch.Add(-90 * 24 * time.Hour)})
		r.install(driveID, itemSpec{ref: refKit, tier: 1, lastRe: epoch.Add(-90 * 24 * time.Hour)})
		if !plugged {
			r.unplug()
			r.rebuild()
		}
		return r
	}
	withDrive := run(t, build(true).hashCheck(HashDeps{}))
	if withDrive.Result != drill.ResultPass || withDrive.Details["verified"] != int64(11) {
		t.Fatalf("with the drive: %+v", withDrive)
	}
	without := run(t, build(false).hashCheck(HashDeps{}))
	if without.Result != drill.ResultPass || without.Details["verified"] != int64(9) {
		t.Fatalf("without the drive: %+v", without)
	}
}

func TestHashAnItemTheReportCallsMissingIsNotHashedOrFailed(t *testing.T) {
	r := newContentRig(t)
	r.installNode()
	r.declare(ark.InternalID, ready.Item{ID: "kit-missing", Kind: "kit", Tier: 1, Status: ready.StatusMissing})
	out := run(t, r.hashCheck(HashDeps{NodeKit: func(context.Context) []string { return []string{refKit.ID, "kit-missing"} }}))
	if out.Result != drill.ResultPass {
		t.Fatalf("outcome = %+v", out)
	}
}

type fakeVerifier struct {
	cands   []verify.Candidate
	runs    []verify.Report
	runErr  error
	candErr error
	calls   []verify.Options
}

func (f *fakeVerifier) Candidates(context.Context, verify.Options) ([]verify.Candidate, error) {
	return f.cands, f.candErr
}

func (f *fakeVerifier) Run(_ context.Context, o verify.Options) (verify.Report, error) {
	f.calls = append(f.calls, o)
	if f.runErr != nil {
		return verify.Report{}, f.runErr
	}
	if len(f.runs) == 0 {
		return verify.Report{}, nil
	}
	rep := f.runs[0]
	f.runs = f.runs[1:]
	return rep, nil
}

func verified(root, item string) verify.Outcome {
	return verify.Outcome{Root: root, Item: item, Result: verify.ResultVerified, Bytes: 10}
}

func TestHashWithAFakeVerifier(t *testing.T) {
	rep := ready.Report{Items: []ready.Item{{ID: "m1", Kind: "model", Tier: 1, Survival: true, Status: ready.StatusVerified}}}
	reports := &fakeReady{reports: map[string]ready.Report{ark.InternalID: rep}}
	roots := func() []ark.Root {
		return []ark.Root{{ID: ark.InternalID, Kind: ark.KindInternal, Path: "/x", State: ark.StateReady}}
	}
	mk := func(v *fakeVerifier) drill.Check {
		return NewHashCheck(HashDeps{Verifier: v, Roots: roots, Ready: reports, Now: func() time.Time { return epoch }})
	}
	boom := errors.New("disk exploded")
	cases := []struct {
		name   string
		v      *fakeVerifier
		result drill.Result
		code   string
		reason string
	}{
		{"all verified", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{verified("internal", "m1")}}}}, drill.ResultPass, "", ""},
		{"a quarantined copy", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{
			{Root: "internal", Item: "m1", Result: verify.ResultQuarantined, Reason: verify.ReasonBitRot, Code: verify.CodeBitRot}}}}}, drill.ResultFail, doctor.DRL012, ""},
		{"a copy restored from the trash is still damaged", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{
			{Root: "internal", Item: "m1", Result: verify.ResultQuarantined, Reason: verify.ReasonRestored, Restored: "v0"}}}}}, drill.ResultFail, doctor.DRL012, ""},
		{"a copy that could not be set aside", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{
			{Root: "internal", Item: "m1", Result: verify.ResultFailed, Reason: verify.ReasonQuarantineFailed}}}}}, drill.ResultFail, doctor.DRL012, ""},
		{"budget ended in the first pass", &fakeVerifier{runs: []verify.Report{{
			Outcomes: []verify.Outcome{verified("internal", "m1")}, Stopped: true,
			Left: []verify.Candidate{{Root: "internal", Item: "m2"}}}}}, drill.ResultWarn, doctor.DRL012, ""},
		{"nothing hashed", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{
			{Item: "m1", Result: verify.ResultSkipped, Reason: verify.ReasonNotPresent}}}}}, drill.ResultSkip, "", "nothing_to_hash"},
		{"a busy copy is skipped, not failed", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{
			verified("internal", "m1"), {Root: "internal", Item: "m9", Result: verify.ResultSkipped, Reason: verify.ReasonBusy}}}}}, drill.ResultPass, "", ""},
		{"the verifier fails", &fakeVerifier{runErr: boom}, drill.ResultFail, doctor.DRL012, "verifier_error"},
		{"the candidate listing fails", &fakeVerifier{runs: []verify.Report{{Outcomes: []verify.Outcome{verified("internal", "m1")}}}, candErr: boom}, drill.ResultFail, doctor.DRL012, "verifier_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := run(t, mk(tc.v))
			if out.Result != tc.result || out.Code != tc.code {
				t.Fatalf("outcome = %+v, want %s %q", out, tc.result, tc.code)
			}
			if tc.reason != "" && out.Details["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", out.Details["reason"], tc.reason)
			}
		})
	}

	t.Run("no verifier", func(t *testing.T) {
		if out := run(t, NewHashCheck(HashDeps{})); out.Result != drill.ResultSkip {
			t.Errorf("outcome = %+v", out)
		}
	})
	t.Run("pass 1 names the internal root and the items, pass 2 skips what pass 1 did", func(t *testing.T) {
		v := &fakeVerifier{
			runs: []verify.Report{
				{Outcomes: []verify.Outcome{verified("internal", "m1")}},
				{Outcomes: []verify.Outcome{verified(driveID, "d1")}},
			},
			cands: []verify.Candidate{{Root: "internal", Item: "m1"}, {Root: driveID, Item: "d1"}},
		}
		out := run(t, mk(v))
		if out.Result != drill.ResultPass || out.Details["verified"] != int64(2) {
			t.Fatalf("outcome = %+v", out)
		}
		if len(v.calls) != 2 {
			t.Fatalf("calls = %+v", v.calls)
		}
		if got := v.calls[0]; len(got.Roots) != 1 || got.Roots[0] != ark.InternalID || len(got.Items) != 1 || got.Items[0] != "m1" {
			t.Errorf("pass 1 = %+v", got)
		}
		if got := v.calls[1]; len(got.Roots) != 1 || got.Roots[0] != driveID || len(got.Items) != 1 || got.Items[0] != "d1" {
			t.Errorf("pass 2 = %+v (m1 was hashed in pass 1)", got)
		}
	})
	t.Run("budget ends in the second pass", func(t *testing.T) {
		v := &fakeVerifier{
			runs: []verify.Report{
				{Outcomes: []verify.Outcome{verified("internal", "m1")}},
				{Outcomes: []verify.Outcome{verified("internal", "d1")}, Stopped: true, Left: []verify.Candidate{{Root: "internal", Item: "d2"}}},
			},
			cands: []verify.Candidate{{Root: "internal", Item: "d1"}, {Root: "internal", Item: "d2"}, {Root: driveID, Item: "d3"}},
		}
		out := run(t, mk(v))
		if out.Result != drill.ResultWarn || out.Details["skipped_count"] != 2 {
			t.Fatalf("outcome = %+v", out)
		}
	})
}

// ---- helpers ---------------------------------------------------------------------------------------

func mustWrite(t testing.TB, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t testing.TB, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

func mustRemoveAll(t testing.TB, p string) {
	t.Helper()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t testing.TB, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
