package kit_test

import (
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

// The drill's drl.kit.present check (drill.md row 17) reads the report through NodeProblems (fail:
// this computer's parts are missing or inconsistent) and LabProblems (warning: a gap for another
// computer of the lab on a drive).
func TestReportSplitsNodeAndLabProblemsForTheDrill(t *testing.T) {
	n1, n3 := kittest.AiN1(), kittest.AiN3()
	n1.LlamaSeed = "llama-old" // not in the kit: a lab-level gap
	k := kittest.New(t)
	k.Provision(n3)
	k.Pins(kittest.Epoch, n1, n3)
	k.Remove("prebuilt") // optional: neither list
	quick := func(o *kit.Options) { facts(n3)(o); o.Internal = false; o.Mode = kit.ModeQuick }
	rep := run(t, k, quick)
	if len(rep.NodeProblems()) != 0 || len(rep.LabProblems()) != 1 || rep.LabProblems()[0].Need != kit.NeedLlamaSource {
		t.Fatalf("node %v lab %v", rep.NodeProblems(), rep.LabProblems())
	}
	k.Remove("tools/piper")
	rep = run(t, k, quick)
	if len(rep.NodeProblems()) != 1 || rep.NodeProblems()[0].Need != kit.NeedPiper {
		t.Fatalf("node %v", rep.NodeProblems())
	}
}
