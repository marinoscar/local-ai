package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
	"github.com/marinoscar/local-ai/agent/internal/router/build"
)

// kitFix is a KitSource over kits built on disk by kittest, verified by the real kit.Verifier in
// quick mode, exactly as the daemon's kitready.Source does.
type kitFix struct {
	t       *testing.T
	node    kittest.Node
	roots   []kit.StatusRoot
	kits    map[string]*kittest.Kit
	verify  func(id string) (kit.VerifyReport, error) // overrides the verifier for one test
	calls   []string
	noFacts bool
}

func newKitFix(t *testing.T, n kittest.Node) *kitFix {
	t.Helper()
	return &kitFix{t: t, node: n, kits: map[string]*kittest.Kit{}}
}

// internal adds the internal root with a kit provisioned for the node.
func (f *kitFix) internal(k *kittest.Kit) *kitFix {
	f.roots = append(f.roots, kit.StatusRoot{ID: "internal", Path: k.Root, Internal: true})
	f.kits["internal"] = k
	return f
}

// drive adds an ark drive root.
func (f *kitFix) drive(id string, k *kittest.Kit) *kitFix {
	f.roots = append(f.roots, kit.StatusRoot{ID: id, Path: k.Root})
	f.kits[id] = k
	return f
}

func (f *kitFix) Roots() []kit.StatusRoot { return f.roots }

func (f *kitFix) Verify(ctx context.Context, id string) (kit.VerifyReport, error) {
	f.calls = append(f.calls, id)
	if f.verify != nil {
		return f.verify(id)
	}
	var root kit.StatusRoot
	for _, r := range f.roots {
		if r.ID == id {
			root = r
		}
	}
	k := f.kits[id]
	if k == nil {
		k = f.kits["internal"] // a root with no kit of its own is verified with the lab's key
	}
	opts := kit.Options{
		Mode: kit.ModeQuick, Now: kittest.Epoch.Add(24 * time.Hour), RootID: root.ID, Internal: root.Internal, Keys: k.Keys,
	}
	if !f.noFacts {
		facts := f.node.Facts()
		opts.Node = &facts
	}
	return kit.Verifier{FS: kit.OSFS{}}.Verify(ctx, root.Path, opts)
}

func kitRun(f KitSource) drill.Outcome {
	return KitPresent(f).Run(context.Background(), &drill.Env{})
}

func TestKitPresentCompleteKitPasses(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	f := newKitFix(t, n).internal(k)
	out := kitRun(f)
	if out.Result != drill.ResultPass || out.Code != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["kit_overall"] != "green" || out.Details["roots_checked"] != 1 {
		t.Fatalf("details = %v", out.Details)
	}
	if _, has := out.Details["missing"]; has {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestKitPresentMissingPrebuiltIsOnlyAWarningOfThePart(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Remove("prebuilt")
	out := kitRun(newKitFix(t, n).internal(k))
	if out.Result != drill.ResultPass {
		t.Fatalf("a missing prebuilt build is optional: %+v", out)
	}
	if out.Details["kit_overall"] != "yellow" || !strings.Contains(out.Details["missing"].(string), "prebuilt (") {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestKitPresentDriveMissingAnotherNodesPartsWarnsWith041(t *testing.T) {
	n1, n3 := kittest.AiN1(), kittest.AiN3()
	n1.LlamaSeed = "llama-old" // a commit the drive does not carry: a gap for another computer of the lab
	internal := kittest.New(t)
	internal.Provision(n3)
	drive := kittest.New(t)
	drive.Provision(n3)
	drive.Pins(kittest.Epoch, n1, n3)
	out := kitRun(newKitFix(t, n3).internal(internal).drive("ark-3f9c0a12", drive))
	if out.Result != drill.ResultWarn || out.Code != doctor.KIT041 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["roots_checked"] != 2 || !strings.Contains(out.Details["codes"].(string), doctor.KIT041) {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestKitPresentNoDriveSkipsTheLabLevelPart(t *testing.T) {
	n3 := kittest.AiN3()
	internal := kittest.New(t)
	internal.Provision(n3)
	out := kitRun(newKitFix(t, n3).internal(internal))
	if out.Result != drill.ResultPass || out.Details["roots_checked"] != 1 {
		t.Fatalf("outcome = %+v: an absent drive is skipped, not failed", out)
	}
}

func TestKitPresentDriveWithoutAKitFolderIsSkipped(t *testing.T) {
	n3 := kittest.AiN3()
	internal := kittest.New(t)
	internal.Provision(n3)
	f := newKitFix(t, n3).internal(internal)
	f.roots = append(f.roots, kit.StatusRoot{ID: "ark-aaaaaaaa", Path: t.TempDir()})
	out := kitRun(f)
	if out.Result != drill.ResultPass || !strings.Contains(out.Details["skipped"].(string), "ark-aaaaaaaa") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentOwnPartsMissingFailsWith070(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Remove("debs")
	out := kitRun(newKitFix(t, n).internal(k))
	if out.Result != drill.ResultFail || out.Code != doctor.DRL070 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["kit_overall"] != "red" || !strings.Contains(out.Details["missing"].(string), "debs ("+doctor.KIT040+")") ||
		!strings.Contains(out.Details["codes"].(string), doctor.KIT040) {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestKitPresentDebsOfAnotherUbuntuReleaseIsInconsistent(t *testing.T) {
	n := kittest.AiN3()
	other := n
	other.Codename = "noble"
	k := kittest.New(t)
	k.Provision(other) // the kit holds the closure of another release only
	out := kitRun(newKitFix(t, n).internal(k))
	if out.Result != drill.ResultFail || out.Code != doctor.DRL070 || !strings.Contains(out.Details["codes"].(string), doctor.KIT042) {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentDamagedFileFailsWith070(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Truncate("toolchains/go1.24.0.linux-amd64.tar.gz", 3)
	out := kitRun(newKitFix(t, n).internal(k))
	if out.Result != drill.ResultFail || out.Code != doctor.DRL070 || !strings.Contains(out.Details["codes"].(string), doctor.KIT001) {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentNoKitOnTheInternalRootFails(t *testing.T) {
	n := kittest.AiN3()
	f := newKitFix(t, n)
	f.roots = []kit.StatusRoot{{ID: "internal", Path: t.TempDir(), Internal: true}}
	f.kits["internal"] = kittest.New(t)
	out := kitRun(f)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL070 || out.Details["reason"] != "no_kit_on_internal_root" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentWithoutAnInternalRootFails(t *testing.T) {
	f := &kitFix{roots: []kit.StatusRoot{{ID: "ark-1", Path: "/x"}}}
	out := kitRun(f)
	if out.Result != drill.ResultFail || out.Code != doctor.DRL070 || out.Details["reason"] != "no_internal_root" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentUnknownNodeFactsAreNoted(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	f := newKitFix(t, n).internal(k)
	f.noFacts = true
	out := kitRun(f)
	if out.Details["node_facts"] != "unknown" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentVerifierFailures(t *testing.T) {
	boom := errors.New("disk read error")
	f := &kitFix{
		roots:  []kit.StatusRoot{{ID: "internal", Path: "/a", Internal: true}, {ID: "ark-1", Path: "/b"}},
		verify: func(id string) (kit.VerifyReport, error) { return kit.VerifyReport{Overall: kit.Green}, nil },
	}
	if out := kitRun(f); out.Result != drill.ResultPass {
		t.Fatalf("two clean reports: %+v", out)
	}
	f.verify = func(id string) (kit.VerifyReport, error) {
		if id == "internal" {
			return kit.VerifyReport{}, boom
		}
		return kit.VerifyReport{Overall: kit.Green}, nil
	}
	if out := kitRun(f); out.Result != drill.ResultFail || out.Code != doctor.DRL070 || out.Details["reason"] != "cannot_verify" {
		t.Fatalf("an internal root that cannot be verified: %+v", out)
	}
	f.verify = func(id string) (kit.VerifyReport, error) {
		if id == "ark-1" {
			return kit.VerifyReport{}, boom
		}
		return kit.VerifyReport{Overall: kit.Green}, nil
	}
	if out := kitRun(f); out.Result != drill.ResultPass || out.Details["skipped"] == nil {
		t.Fatalf("a drive that cannot be verified is skipped: %+v", out)
	}
}

func TestKitPresentDriveProblemsWarnWithTheirOwnCode(t *testing.T) {
	f := &kitFix{
		roots: []kit.StatusRoot{{ID: "internal", Path: "/a", Internal: true}, {ID: "ark-1", Path: "/b"}},
		verify: func(id string) (kit.VerifyReport, error) {
			if id == "internal" {
				return kit.VerifyReport{Overall: kit.Green}, nil
			}
			return kit.VerifyReport{Root: "ark-1", Overall: kit.Red, Findings: []kit.Finding{
				{Code: kit.CodeKitDamagedFile, Kind: kit.KindDamaged},
			}, Parts: []kit.PartResult{{Part: "tools", Status: kit.StatusDamaged, Code: kit.CodeKitDamagedFile}}}, nil
		},
	}
	out := kitRun(f)
	if out.Result != drill.ResultWarn || out.Code != doctor.KIT001 || out.Details["missing"] != "ark-1/tools ("+doctor.KIT001+")" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentCudaTooNewWarns(t *testing.T) {
	f := &kitFix{
		roots: []kit.StatusRoot{{ID: "internal", Path: "/a", Internal: true}},
		verify: func(string) (kit.VerifyReport, error) {
			return kit.VerifyReport{Overall: kit.Yellow, Findings: []kit.Finding{{Code: kit.CodeKitCudaTooNew, Kind: kit.KindCudaTooNew}}}, nil
		},
	}
	if out := kitRun(f); out.Result != drill.ResultWarn || out.Code != doctor.KIT043 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestKitPresentCheckIdentity(t *testing.T) {
	c := KitPresent(&kitFix{})
	if c.ID != "drl.kit.present" || c.Order != 17 || !c.Critical || c.FailCode != doctor.DRL070 || c.Area != "kit" {
		t.Fatalf("check = %+v", c)
	}
}

func TestKitPresentInterruptedIsAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &kitFix{roots: []kit.StatusRoot{{ID: "internal", Path: "/a", Internal: true}},
		verify: func(string) (kit.VerifyReport, error) { return kit.VerifyReport{}, ctx.Err() }}
	out := KitPresent(f).Run(ctx, &drill.Env{})
	if out.Result != drill.ResultFail || out.Details["reason"] != "interrupted" {
		t.Fatalf("outcome = %+v", out)
	}
}

// ---- check 18: the rebuild dry run -----------------------------------------------------------------

func report(checks ...build.Check) build.Report { return build.Report{Checks: checks} }

func okCheck(id string) build.Check { return build.Check{ID: id, Title: id, OK: true, Detail: "fine"} }

func TestRebuildDryRunPasses(t *testing.T) {
	var asked []string
	d := BuildDeps{
		Llama: func(context.Context, func([]string) []string) build.Report {
			asked = append(asked, "llama")
			return report(okCheck(build.CheckTools))
		},
		Whisper: func(context.Context, func([]string) []string) build.Report {
			asked = append(asked, "whisper")
			return report(okCheck(build.CheckTools))
		},
	}
	out := KitRebuildDryRun(d).Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultPass || out.Code != "" || out.Details["llama_ok"] != true || out.Details["whisper_ok"] != true {
		t.Fatalf("outcome = %+v", out)
	}
	if len(asked) != 2 {
		t.Fatalf("asked = %v", asked)
	}
}

func TestRebuildDryRunMissingCmakeFailsWith071ListingKIT010(t *testing.T) {
	d := BuildDeps{
		Llama: func(context.Context, func([]string) []string) build.Report {
			return report(okCheck(build.CheckInputs), build.Check{
				ID: build.CheckTools, Title: "Build tools", Code: doctor.KIT010, Detail: "cmake is not installed (needed to configure llama.cpp)",
			})
		},
		Whisper: func(context.Context, func([]string) []string) build.Report { return report(okCheck(build.CheckTools)) },
	}
	c := KitRebuildDryRun(d)
	out := c.Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultFail || out.Code != doctor.DRL071 || c.FailCode != doctor.DRL071 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Details["llama_ok"] != false || out.Details["whisper_ok"] != true {
		t.Fatalf("details = %v", out.Details)
	}
	failed, _ := out.Details["llama_failed"].(string)
	if !strings.HasPrefix(failed, doctor.KIT010+" cmake is not installed") {
		t.Fatalf("llama_failed = %q", failed)
	}
	if _, has := out.Details["whisper_failed"]; has {
		t.Fatalf("details = %v", out.Details)
	}
}

func TestRebuildDryRunWhisperFailureAloneFails(t *testing.T) {
	d := BuildDeps{
		Llama: func(context.Context, func([]string) []string) build.Report { return report(okCheck(build.CheckTools)) },
		Whisper: func(context.Context, func([]string) []string) build.Report {
			return report(build.Check{ID: build.CheckSource, Code: doctor.KIT002, Detail: "no source"})
		},
	}
	out := KitRebuildDryRun(d).Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultFail || out.Code != doctor.DRL071 || out.Details["whisper_ok"] != false || out.Details["llama_ok"] != true {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRebuildDryRunWithoutWhisperChecksOnlyLlama(t *testing.T) {
	d := BuildDeps{Llama: func(context.Context, func([]string) []string) build.Report { return report(okCheck(build.CheckTools)) }}
	out := KitRebuildDryRun(d).Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultPass || out.Details["whisper_skipped"] == nil || out.Details["whisper_ok"] != nil {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRebuildDryRunSkipsWhisperWhenSpeechIsSwitchedOff(t *testing.T) {
	asked := false
	d := BuildDeps{
		Llama: func(context.Context, func([]string) []string) build.Report { return report(okCheck(build.CheckTools)) },
		Whisper: func(context.Context, func([]string) []string) build.Report {
			asked = true
			return report(build.Check{ID: build.CheckTools, Code: doctor.KIT010})
		},
		WhisperEnabled: func() bool { return false },
	}
	out := KitRebuildDryRun(d).Run(context.Background(), &drill.Env{})
	if out.Result != drill.ResultPass || asked || out.Details["whisper_skipped"] == nil {
		t.Fatalf("outcome = %+v (whisper asked: %v)", out, asked)
	}
}

func TestRebuildDryRunDetailsStayShort(t *testing.T) {
	long := strings.Repeat("x", 2000)
	d := BuildDeps{Llama: func(context.Context, func([]string) []string) build.Report {
		var cs []build.Check
		for i := 0; i < 8; i++ {
			cs = append(cs, build.Check{ID: build.CheckTools, Code: doctor.KIT010, Detail: long})
		}
		return report(cs...)
	}}
	out := KitRebuildDryRun(d).Run(context.Background(), &drill.Env{})
	if s, _ := out.Details["llama_failed"].(string); len(s) > 500 {
		t.Fatalf("llama_failed is %d characters: the cloud keeps 500", len(s))
	}
}

func TestRegisterKitAddsTheFourChecks(t *testing.T) {
	reg := drill.NewRegistry()
	b := BuildDeps{Llama: func(context.Context, func([]string) []string) build.Report { return report() }}
	if err := RegisterKit(reg, &kitFix{}, b, PowerDeps{}, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := reg.Select(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cs {
		got = append(got, c.ID)
	}
	if want := "drl.kit.present drl.kit.rebuild_dry_run drl.power drl.clock"; strings.Join(got, " ") != want {
		t.Fatalf("checks = %v, want %s", got, want)
	}
	if err := RegisterKit(drill.NewRegistry(), nil, b, PowerDeps{}, nil); err == nil {
		t.Fatal("the kit verifier is required")
	}
	if err := RegisterKit(drill.NewRegistry(), &kitFix{}, BuildDeps{}, PowerDeps{}, nil); err == nil {
		t.Fatal("the llama.cpp preflight is required")
	}
}

func TestRebuildDryRunPassesTheSubprocessEnvironmentToThePreflights(t *testing.T) {
	var got []string
	pf := func(_ context.Context, subEnv func([]string) []string) build.Report {
		got = subEnv([]string{"LC_ALL=C", "HTTPS_PROXY=http://proxy.example.test:3128"})
		return report(okCheck(build.CheckTools))
	}
	KitRebuildDryRun(BuildDeps{Llama: pf}).Run(context.Background(), &drill.Env{})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "LC_ALL=C") || strings.Contains(joined, "proxy.example.test") || !strings.Contains(joined, "HTTPS_PROXY=http://127.0.0.1:9") {
		t.Fatalf("env = %v", got)
	}
}
