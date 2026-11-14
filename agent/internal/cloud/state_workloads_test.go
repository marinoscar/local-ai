package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The workloads block (E11, cloud-app.md section 3.6 WorkloadsBlockV1): optional, strict, and only for a
// cloud whose handshake lists workloads.v1.

type fakeWorkloads struct{ r *WorkloadsReport }

func (f *fakeWorkloads) Workloads(context.Context) *WorkloadsReport { return f.r }

func workloadsReport() *WorkloadsReport {
	return &WorkloadsReport{Active: "coding", Items: []WorkloadReport{
		{Name: "coding", LabelEN: "Coding", LabelES: "Programación", Models: []string{"qwen3.5-9b-q4km"}, Fits: true},
		{Name: "music", LabelEN: "Music", LabelES: "Música", Models: []string{"acestep-v15-turbo"}, Missing: []string{"acestep-v15-turbo"}, Fits: true},
	}}
}

func workloadsBuilder(t *testing.T, r *rig, src WorkloadsSource) *SnapshotBuilder {
	t.Helper()
	sb, err := NewSnapshotBuilder(SnapshotOptions{
		Status: r.status, Models: r.models, Ark: r.ark, Drill: r.drill, Sync: &fakeSync{}, Config: cfgView{nodeConfig()}, Workloads: src,
		BootID: "5d2f6a54-0f6e-4c53-9d1e-4b6f3f2a7c10", Now: r.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func TestSnapshotWorkloadsBlockIsOptInAndConformsToTheSchema(t *testing.T) {
	r := newRig(t)
	sb := workloadsBuilder(t, r, &fakeWorkloads{r: workloadsReport()})
	without, err := sb.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if without.Snapshot.Workloads != nil || bytes.Contains(without.Body, []byte(`"workloads"`)) {
		t.Fatalf("a build without the option carries the block: %s", without.Body)
	}
	with, err := sb.BuildWith(context.Background(), BuildOpts{Workloads: true})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, with.Body)
	w := with.Snapshot.Workloads
	if w == nil || w.Active == nil || *w.Active != "coding" || len(w.Items) != 2 || w.Items[1].Missing[0] != "acestep-v15-turbo" || w.Items[0].Missing == nil {
		t.Fatalf("workloads = %+v", w)
	}
	if !bytes.Contains(with.Body, []byte(`"workloads":{"active":"coding","items":[{"name":"coding","label":{"en":"Coding","es":"Programación"},"models":["qwen3.5-9b-q4km"],"fits":true,"missing":[]}`)) {
		t.Errorf("shape: %s", with.Body)
	}
}

func TestSnapshotWorkloadsBlockKeepsToTheLimits(t *testing.T) {
	r := newRig(t)
	rep := &WorkloadsReport{Active: "gone"}
	for i := 0; i < 20; i++ {
		rep.Items = append(rep.Items, WorkloadReport{Name: "w" + string(rune('a'+i)),
			LabelEN: strings.Repeat("x", 100), LabelES: "y", Models: []string{"mod1", "mod2", "mod3", "mod4", "mod5", "mod6", "mod7", "mod8", "mod9"}})
	}
	rep.Items = append([]WorkloadReport{{Name: "Bad_Name", LabelEN: "x", LabelES: "y"}}, rep.Items...)
	b, err := workloadsBuilder(t, r, &fakeWorkloads{r: rep}).BuildWith(context.Background(), BuildOpts{Workloads: true})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, b.Body)
	w := b.Snapshot.Workloads
	if w.Active != nil || len(w.Items) != 16 || w.Items[0].Name == "Bad_Name" || len(w.Items[0].Models) != 8 || len(w.Items[0].Label.EN) != 64 {
		t.Fatalf("workloads = %+v", w)
	}
}

func TestWorkloadsBlockIsPushedOnlyToACloudThatAdvertisesIt(t *testing.T) {
	withSource := withDeps(func(d *Deps) { d.Snapshot.Workloads = &fakeWorkloads{r: workloadsReport()} })

	old := newRig(t, withSource)
	quickState(old)
	old.enroll()
	old.waitCount("state", 1)
	if body := old.srv.States()[0]; bytes.Contains(body, []byte(`"workloads"`)) {
		t.Fatalf("a cloud without workloads.v1 got the block: %s", body)
	}

	newer := newRig(t, withSource)
	quickState(newer)
	newer.srv.Handshake.Features = append(newer.srv.Handshake.Features, FeatureWorkloads)
	newer.enroll()
	newer.waitCount("state", 1)
	body := newer.srv.States()[0]
	validate(t, body)
	var snap StateSnapshot
	if err := json.Unmarshal(body, &snap); err != nil || snap.Workloads == nil || len(snap.Workloads.Items) != 2 {
		t.Fatalf("the block is missing from %s (%v)", body, err)
	}
}
