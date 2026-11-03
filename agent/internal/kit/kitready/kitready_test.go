package kitready_test

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kitready"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

var now = kittest.Epoch.Add(24 * time.Hour)

func source(k *kittest.Kit, id string, internal bool, node kittest.Node) *kitready.Source {
	f := node.Facts()
	return &kitready.Source{
		Verifier: kit.Verifier{FS: kit.OSFS{}},
		Roots:    func() []kit.StatusRoot { return []kit.StatusRoot{{ID: id, Path: k.Root, Internal: internal}} },
		Node:     func() *kit.NodeFacts { return &f },
		Keys:     k.Keys,
		Now:      func() time.Time { return now },
	}
}

// build runs the readiness builder (#58) over the kit report of one root. Nothing else is in the input,
// so every problem and colour of the result comes from the kit rows.
func build(src *kitready.Source, id string, kind ark.RootKind) ready.Report {
	return ready.Build(ready.Input{
		Now: now, NodeID: "ai-n3", AgentVersion: "0.4.0",
		Root: ready.RootFacts{ID: id, Kind: kind, Profile: "small-8gb", TotalBytes: 1 << 40, FreeBytes: 1 << 39},
		Kit:  src.KitReport(id),
	})
}

func problems(r ready.Report) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Code)
	}
	return out
}

// TestReadyKitRows is the acceptance test of the colour mapping: a kit with a given fault, read
// through the Source and judged by the real builder, gives this colour and these problems.
func TestReadyKitRows(t *testing.T) {
	n3 := kittest.AiN3()
	wrong := n3
	wrong.Codename = "noble" // the kit was synced for noble; this computer runs resolute
	tests := []struct {
		name     string
		provide  []kittest.Node
		mutate   func(k *kittest.Kit)
		drive    bool
		kitNode  kittest.Node // what the kit is built for
		runsAs   kittest.Node // what this computer is
		want     ready.Colour
		problems []string
	}{
		{name: "a complete kit is green", provide: []kittest.Node{n3}, kitNode: n3, runsAs: n3, want: ready.Green},
		{name: "040 on the internal root is red", provide: []kittest.Node{n3}, kitNode: n3, runsAs: n3, want: ready.Red, problems: []string{doctor.KIT040},
			mutate: func(k *kittest.Kit) { k.Remove("tools/piper") }},
		{name: "040 on a drive is yellow", provide: []kittest.Node{n3}, drive: true, kitNode: n3, runsAs: n3, want: ready.Yellow, problems: []string{doctor.KIT040},
			mutate: func(k *kittest.Kit) { k.Remove("tools/piper") }},
		{name: "042 on the internal root is red", provide: []kittest.Node{wrong}, kitNode: wrong, runsAs: n3, want: ready.Red, problems: []string{doctor.KIT042, doctor.KIT042}}, // the debs and the NVIDIA closure
		{name: "043 is yellow", provide: []kittest.Node{n3}, kitNode: n3,
			runsAs: func() kittest.Node { n := n3; n.DriverCUDA = "12.2"; return n }(), want: ready.Yellow, problems: []string{doctor.KIT043}},
		{name: "a missing prebuilt changes nothing", provide: []kittest.Node{n3}, kitNode: n3, runsAs: n3, want: ready.Green,
			mutate: func(k *kittest.Kit) { k.Remove("prebuilt") }},
		{name: "damage is not a READY finding (the age and status rules speak)", provide: []kittest.Node{n3}, kitNode: n3, runsAs: n3, want: ready.Green,
			mutate: func(k *kittest.Kit) { k.Truncate("toolchains/go1.24.0.linux-amd64.tar.gz", 3) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kittest.New(t)
			k.Provision(tt.provide...)
			if tt.mutate != nil {
				tt.mutate(k)
			}
			kind := ark.KindInternal
			if tt.drive {
				kind = ark.KindArkDrive
			}
			id := "internal"
			if tt.drive {
				id = "ark-0a1b2c3d"
			}
			r := build(source(k, id, !tt.drive, tt.runsAs), id, kind)
			if r.Overall != tt.want {
				t.Errorf("overall %s, want %s (problems %v)", r.Overall, tt.want, problems(r))
			}
			if got := problems(r); !reflect.DeepEqual(got, tt.problems) && !(len(got) == 0 && len(tt.problems) == 0) {
				t.Errorf("problems %v, want %v", got, tt.problems)
			}
		})
	}
}

func TestReadyLabRowOnADriveIsYellowAndNamesTheItem(t *testing.T) {
	n3 := kittest.AiN3()
	n1 := kittest.AiN1()
	n1.ID, n1.Codename = "ai-n1", "plucky" // a lab node on another release the drive was not synced for
	k := kittest.New(t)
	k.Provision(n3)
	k.Pins(kittest.Epoch, n3, n1)
	src := source(k, "ark-0a1b2c3d", false, n3)
	r := build(src, "ark-0a1b2c3d", ark.KindArkDrive)
	if r.Overall != ready.Yellow || !reflect.DeepEqual(problems(r), []string{doctor.KIT041}) {
		t.Fatalf("overall %s problems %v", r.Overall, problems(r))
	}
	if r.Problems[0].Item != "kit-debs-plucky-amd64" {
		t.Errorf("the problem names %q", r.Problems[0].Item)
	}
}

// TestFindingColoursAgreeWithTheBuilder: the verifier decides a colour of its own report, the builder
// decides the colour of READY; they must be one table (the verifier cannot import the builder).
func TestFindingColoursAgreeWithTheBuilder(t *testing.T) {
	n3 := kittest.AiN3()
	wrong := n3
	wrong.Codename = "noble"
	for _, internal := range []bool{true, false} {
		k := kittest.New(t)
		k.Provision(wrong)
		k.Remove("tools/piper")
		k.Pins(kittest.Epoch, wrong, func() kittest.Node { n := kittest.AiN1(); n.Codename = "plucky"; return n }())
		f := n3.Facts()
		f.DriverCUDA = "12.2"
		rep, err := kit.Verifier{FS: kit.OSFS{}}.Verify(context.Background(), k.Root, kit.Options{
			Mode: kit.ModeQuick, Now: now, RootID: "r", Internal: internal, Node: &f, Keys: k.Keys})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, fd := range rep.Findings {
			want, ok := ready.KitColour(fd.Code, internal)
			if !ok || fd.Optional {
				continue
			}
			seen[fd.Code] = true
			if string(want) != fd.Colour {
				t.Errorf("internal=%v %s: the verifier says %s, the builder %s", internal, fd.Code, fd.Colour, want)
			}
		}
		if !seen[doctor.KIT040] || (internal && !seen[doctor.KIT042]) || (!internal && !seen[doctor.KIT041]) {
			t.Errorf("internal=%v: codes seen %v", internal, seen)
		}
	}
}

func TestSourceKeepsTheLastReportAndAnswersNothingForUnknownRoots(t *testing.T) {
	n3 := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n3)
	src := source(k, "internal", true, n3)
	if got := src.KitReport("elsewhere"); len(got.Findings) != 0 {
		t.Errorf("an unknown root has findings: %+v", got)
	}
	if _, ok := src.Last("internal"); ok {
		t.Error("a report before any refresh")
	}
	src.KitReport("internal")
	rep, ok := src.Last("internal")
	if !ok || rep.Overall != kit.Green || rep.Mode != kit.ModeQuick {
		t.Fatalf("last %+v ok=%v", rep, ok)
	}
	// The drill asks for its own options (steps 1 to 5, no build) through Verify.
	calls := 0
	rep, err := src.Verify(context.Background(), "internal", func(o *kit.Options) {
		o.Build = func(context.Context) kit.BuildResult { calls++; return kit.BuildResult{} }
		o.Part = "debs"
	})
	if err != nil || rep.Part != "debs" || calls != 0 {
		t.Fatalf("err %v part %q calls %d", err, rep.Part, calls)
	}
	// A root whose kit/ folder is gone has no report any more.
	if err := os.RemoveAll(k.Dir); err != nil {
		t.Fatal(err)
	}
	if got := src.KitReport("internal"); len(got.Findings) != 0 {
		t.Errorf("a root with no kit has findings: %+v", got)
	}
	if _, ok := src.Last("internal"); ok {
		t.Error("the report of a vanished kit was kept")
	}
}
