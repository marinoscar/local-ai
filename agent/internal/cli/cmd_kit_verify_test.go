package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/local"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
)

// Tests of `aictl kit verify` and `aictl kit status` (KIT-03). The kit is built on disk by package
// kittest with digests computed on the spot; the machine (os-release, node.yaml, nvidia-smi, the clock)
// is the fixture's, and the dry run of the llama.cpp rebuild is a stub unless a test says otherwise.

// smiRunner answers nvidia-smi and hands every other program to the fake of the kit fixture.
type smiRunner struct {
	inner  execx.Runner
	cuda   string
	driver string
}

func (s *smiRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if filepath.Base(c.Path) != "nvidia-smi" {
		return s.inner.Run(ctx, c)
	}
	if s.cuda == "" {
		return execx.Result{ExitCode: 9}, nil
	}
	if len(c.Args) == 0 {
		return execx.Result{Stdout: []byte("| NVIDIA-SMI " + s.driver + "   Driver Version: " + s.driver + "   CUDA Version: " + s.cuda + " |\n")}, nil
	}
	return execx.Result{Stdout: []byte(s.driver + "\n")}, nil
}

// verifyFix is a computer whose internal root holds the kit of one node.
type verifyFix struct {
	*kitFix
	k    *kittest.Kit
	node kittest.Node
}

// newVerifyFix builds the kit of node on the internal root and makes the machine that node: node.yaml,
// os-release, nvidia-smi, the agent version and a clock one day after the kit was verified.
func newVerifyFix(t *testing.T, node kittest.Node) *verifyFix {
	t.Helper()
	f := newKitFix(t)
	k := kittest.NewAt(t, f.env.Paths.InternalRoot)
	k.Provision(node)
	yaml := fmt.Sprintf("schema_version: 1\nnode:\n  id: %s\nprofile: small-8gb\nllama:\n  commit: %q\n  backend: %s\n",
		node.ID, node.Llama(), node.Backend)
	if node.CudaArch != "" {
		yaml += fmt.Sprintf("  cuda_arch: %q\n", node.CudaArch)
	}
	if w := node.Whisper(); w != "" {
		yaml += fmt.Sprintf("whisper:\n  enabled: true\n  commit: %q\n", w)
	}
	kitWrite(t, f.env.Paths.NodeYAML(), yaml)
	osr := filepath.Join(t.TempDir(), "os-release")
	kitWrite(t, osr, "NAME=Ubuntu\nVERSION_CODENAME="+node.Codename+"\n")
	smi := &smiRunner{inner: f.runner, cuda: node.DriverCUDA, driver: "595.58.03"}
	oldHost, oldBuild := kitHostFn, kitBuildFn
	kitHostFn = func() kitHost {
		h := oldHost()
		h.OSRelease, h.Keys, h.Runner = osr, k.Keys, smi
		return h
	}
	kitBuildFn = func(local.Env, kitHost, kitFacts, []kitRoot) func(context.Context) kit.BuildResult {
		return func(context.Context) kit.BuildResult {
			return kit.BuildResult{OK: true, Checks: []kit.BuildCheck{{ID: "source", Title: "Source", OK: true}}}
		}
	}
	t.Cleanup(func() { kitHostFn, kitBuildFn = oldHost, oldBuild })
	f.env.Now = func() time.Time { return kittest.Epoch.Add(24 * time.Hour) }
	f.env.AgentVersion = node.AgentVersion
	return &verifyFix{kitFix: f, k: k, node: node}
}

// verifyDoc is the --json document of `kit verify` and `kit status`.
type verifyDoc struct {
	Overall string `json:"overall"`
	Roots   []struct {
		Root     string `json:"root"`
		Internal bool   `json:"internal"`
		HasKit   bool   `json:"has_kit"`
		Overall  string `json:"overall"`
		Part     string `json:"part"`
		Parts    []struct {
			Part   string `json:"part"`
			Status string `json:"status"`
			Code   string `json:"code"`
		} `json:"parts"`
		Findings []struct {
			Code string `json:"code"`
			Kind string `json:"kind"`
			Part string `json:"part"`
		} `json:"findings"`
		Lab    []struct{ Node string } `json:"lab"`
		Totals struct {
			FilesHashed int `json:"files_hashed"`
		} `json:"totals"`
		Build *struct {
			OK bool `json:"ok"`
		} `json:"build"`
	} `json:"roots"`
}

func (f *verifyFix) doc(t *testing.T, args ...string) (verifyDoc, runResult) {
	t.Helper()
	r := f.run(t, append([]string{"--json"}, args...)...)
	var d verifyDoc
	if err := json.Unmarshal([]byte(r.out), &d); err != nil {
		t.Fatalf("exit %d, not a document: %v\n%q\n%s", r.code, err, r.out, r.errOut)
	}
	return d, r
}

func partOf(d verifyDoc, part string) (status, code string) {
	for _, p := range d.Roots[0].Parts {
		if p.Part == part {
			return p.Status, p.Code
		}
	}
	return "absent", ""
}

// ---- verify ----------------------------------------------------------------------------------------

func TestKitVerifyAllGoodEnAndEs(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	for _, c := range []struct {
		name string
		args []string
	}{
		{"kit.verify.green.en", []string{"kit", "verify"}},
		{"kit.verify.green.es", []string{"--lang", "es", "kit", "verify"}},
		{"kit.verify.full.en", []string{"kit", "verify", "--full"}},
		{"kit.verify.full.es", []string{"--lang", "es", "kit", "verify", "--full"}},
	} {
		r := f.run(t, c.args...)
		if r.code != ExitOK {
			t.Fatalf("%s: exit %d\n%s\n%s", c.name, r.code, r.out, r.errOut)
		}
		checkGolden(t, c.name, r)
	}
}

func TestKitVerifyDamagedExits1WithTheCode(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz")
	f.k.Truncate("tools/piper/piper_linux_x86_64.tar.gz", 2)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"kit.verify.damaged.en", []string{"kit", "verify", "--full"}},
		{"kit.verify.damaged.es", []string{"--lang", "es", "kit", "verify", "--full"}},
	} {
		r := f.run(t, c.args...)
		if r.code != ExitFailed || !strings.Contains(r.out, doctor.KIT001) {
			t.Fatalf("%s: exit %d\n%s\n%s", c.name, r.code, r.out, r.errOut)
		}
		checkGolden(t, c.name, r)
	}
	d, r := f.doc(t, "kit", "verify", "--full")
	if r.code != ExitFailed || d.Overall != "red" {
		t.Fatalf("exit %d overall %s", r.code, d.Overall)
	}
	if st, code := partOf(d, "toolchains"); st != "damaged" || code != doctor.KIT001 {
		t.Errorf("toolchains %s %s", st, code)
	}
}

func TestKitVerifyDefaultReadsOnlyWhatIsDue(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz") // same size: only a re-read can see it
	d, r := f.doc(t, "kit", "verify")
	if r.code != ExitOK || d.Roots[0].Totals.FilesHashed != 0 {
		t.Fatalf("not due: exit %d, %d files read", r.code, d.Roots[0].Totals.FilesHashed)
	}
	// Thirty days later the kit is due for a re-hash and the damage shows.
	f.env.Now = func() time.Time { return kittest.Epoch.Add(31 * 24 * time.Hour) }
	d, r = f.doc(t, "kit", "verify")
	if r.code != ExitFailed || d.Roots[0].Totals.FilesHashed == 0 {
		t.Fatalf("due: exit %d, %d files read", r.code, d.Roots[0].Totals.FilesHashed)
	}
	// sync.reverify_days of node.yaml moves the budget.
	f.env.Now = func() time.Time { return kittest.Epoch.Add(3 * 24 * time.Hour) }
	if d, r = f.doc(t, "kit", "verify"); r.code != ExitOK || d.Roots[0].Totals.FilesHashed != 0 {
		t.Fatalf("3 days: exit %d", r.code)
	}
	kitWrite(t, f.env.Paths.NodeYAML(), string(readFile(t, f.env.Paths.NodeYAML()))+"sync:\n  reverify_days: 2\n")
	if d, r = f.doc(t, "kit", "verify"); r.code != ExitFailed || d.Roots[0].Totals.FilesHashed == 0 {
		t.Fatalf("reverify_days 2: exit %d, %d files read", r.code, d.Roots[0].Totals.FilesHashed)
	}
}

func TestKitVerifyExitCodes(t *testing.T) {
	tests := []struct {
		name string
		do   func(f *verifyFix)
		code int
		sev  string
	}{
		{"all good", func(f *verifyFix) {}, ExitOK, "green"},
		{"a missing prebuilt is a warning", func(f *verifyFix) { f.k.Remove("prebuilt") }, ExitWarnings, "yellow"},
		{"a missing part is an error on the internal root", func(f *verifyFix) { f.k.Remove("tools/piper") }, ExitErrors, "red"},
		{"debs for another release", func(f *verifyFix) {
			if err := os.Rename(f.k.Abs("debs/resolute-amd64"), f.k.Abs("debs/noble-amd64")); err != nil {
				t.Fatal(err)
			}
			f.k.Reseal("debs/noble-amd64", "kit-debs-noble-amd64")
		}, ExitErrors, "red"},
		{"a tampered signature", func(f *verifyFix) { f.k.Corrupt("bin/SHA256SUMS.minisig") }, ExitFailed, "red"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newVerifyFix(t, kittest.AiN3())
			tt.do(f)
			d, r := f.doc(t, "kit", "verify", "--full")
			if r.code != tt.code || d.Overall != tt.sev {
				t.Fatalf("exit %d overall %s\n%s", r.code, d.Overall, r.out)
			}
		})
	}
}

func TestKitVerifyTamperedSignatureIsCritical(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Corrupt("bin/SHA256SUMS.minisig")
	r := f.run(t, "kit", "verify")
	if r.code != ExitFailed || !strings.Contains(unwrap(r.out), doctor.KIT044) || !strings.Contains(unwrap(r.out), "failed their signature check") &&
		!strings.Contains(unwrap(r.out), "did not verify") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	if !strings.Contains(unwrap(r.out), "Don't install them") {
		t.Errorf("the fix of LAI-KIT-044 is missing:\n%s", r.out)
	}
}

func TestKitVerifyPartFilterAndUnknownPart(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	d, r := f.doc(t, "kit", "verify", "--part", "debs", "--full")
	if r.code != ExitOK || len(d.Roots[0].Parts) != 1 || d.Roots[0].Parts[0].Part != "debs" || d.Roots[0].Part != "debs" {
		t.Fatalf("exit %d parts %+v", r.code, d.Roots[0].Parts)
	}
	if d.Roots[0].Build != nil {
		t.Error("a single part ran the dry run of the rebuild")
	}
	r = f.run(t, "kit", "verify", "--part", "nope")
	if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-005") || !strings.Contains(unwrap(r.errOut), "toolchains, tools") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	r = f.run(t, "--lang", "es", "kit", "verify", "--part", "nope")
	if r.code != ExitUsage || !strings.Contains(unwrap(r.errOut), "No hay una parte del kit llamada") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestKitVerifyNoKitIsExit5(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Remove("") // the whole kit folder
	r := f.run(t, "kit", "verify")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, doctor.KIT040) {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if r := f.run(t, "kit", "status"); r.code != ExitNotFound {
		t.Fatalf("status: exit %d", r.code)
	}
	if r := f.run(t, "kit", "verify", "--root", "ark-0a1b2c3d"); r.code != ExitNotFound {
		t.Fatalf("an id with no drive: exit %d", r.code)
	}
}

func TestKitVerifyNeedsNoRootAndNeverWritesToAReadOnlyRoot(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz")
	if err := filepath.Walk(f.k.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			err = os.Chmod(p, info.Mode()&^0o222)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.Walk(f.k.Root, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				os.Chmod(p, info.Mode()|0o700)
			}
			return nil
		})
	})
	before := kittest.Join(f.k.Paths())
	r := f.runAs(t, 1000, "kit", "verify", "--full")
	if r.code != ExitFailed || !strings.Contains(r.out, doctor.KIT001) {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if after := kittest.Join(f.k.Paths()); after != before {
		t.Errorf("the kit changed:\n%s\n---\n%s", before, after)
	}
	if r := f.runAs(t, 1000, "kit", "status"); r.code != ExitOK {
		t.Fatalf("status as an ordinary user: exit %d\n%s", r.code, r.errOut)
	}
}

func TestKitVerifyOnADriveChecksTheLab(t *testing.T) {
	n3 := kittest.AiN3()
	f := newVerifyFix(t, n3)
	mount := f.addDrive("a", false)
	drive := kittest.NewAt(t, filepath.Join(mount, "localai"))
	n1 := kittest.AiN1()
	drive.Provision(n1, n3)
	// Another computer of the lab: its llama.cpp commit is not on the drive.
	n4 := kittest.AiN1()
	n4.ID, n4.LlamaSeed = "ai-n4", "llama-old"
	drive.Pins(kittest.Epoch, n1, n3, n4)
	k := drive.Keys
	oldHost := kitHostFn
	kitHostFn = func() kitHost { h := oldHost(); h.Keys = append(append(h.Keys[:0:0], f.k.Keys...), k...); return h }
	t.Cleanup(func() { kitHostFn = oldHost })

	d, r := f.doc(t, "kit", "verify")
	if len(d.Roots) != 2 || d.Roots[0].Root != "internal" || d.Roots[1].Internal {
		t.Fatalf("roots %+v", d.Roots)
	}
	// The internal root is whole; the drive lacks something for ai-n4: yellow, exit 8, LAI-KIT-041.
	if d.Roots[0].Overall != "green" || d.Roots[1].Overall != "yellow" || r.code != ExitWarnings {
		t.Fatalf("exit %d, overall %s / %s", r.code, d.Roots[0].Overall, d.Roots[1].Overall)
	}
	var lab []string
	for _, l := range d.Roots[1].Lab {
		lab = append(lab, l.Node)
	}
	if strings.Join(lab, ",") != "ai-n1,ai-n4" {
		t.Errorf("lab %v", lab)
	}
	if len(d.Roots[1].Findings) != 1 || d.Roots[1].Findings[0].Code != doctor.KIT041 {
		t.Errorf("findings %+v", d.Roots[1].Findings)
	}
	human := f.run(t, "kit", "verify", "--root", "internal")
	if human.code != ExitOK {
		t.Fatalf("--root internal: exit %d", human.code)
	}
}

func TestKitVerifyDryRunOfTheRebuildIsStepSix(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	failing := func(local.Env, kitHost, kitFacts, []kitRoot) func(context.Context) kit.BuildResult {
		return func(context.Context) kit.BuildResult {
			return kit.BuildResult{Checks: []kit.BuildCheck{{ID: "tools", Title: "Build tools", Code: doctor.KIT010, Detail: "cmake is missing"}}}
		}
	}
	old := kitBuildFn
	kitBuildFn = failing
	t.Cleanup(func() { kitBuildFn = old })
	r := f.run(t, "kit", "verify")
	if r.code != ExitErrors || !strings.Contains(unwrap(r.out), "cmake is missing") || !strings.Contains(r.out, doctor.KIT010) {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	d, _ := f.doc(t, "kit", "verify")
	if d.Roots[0].Build == nil || d.Roots[0].Build.OK {
		t.Errorf("build %+v", d.Roots[0].Build)
	}
}

// The real wiring of step 6 builds the engine's manager from the machine and runs its preflight; with a
// machine on which no program answers, the preflight fails and says why, and nothing panics.
func TestKitBuildDryRunWiring(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	kitBuildFn = func(env local.Env, host kitHost, facts kitFacts, roots []kitRoot) func(context.Context) kit.BuildResult {
		host.Runner = &fnKitRunner{}
		return realKitBuild(env, host, facts, roots)
	}
	d, r := f.doc(t, "kit", "verify")
	if d.Roots[0].Build == nil || d.Roots[0].Build.OK || r.code != ExitErrors {
		t.Fatalf("exit %d build %+v", r.code, d.Roots[0].Build)
	}
	var coded bool
	for _, fd := range d.Roots[0].Findings {
		coded = coded || strings.HasPrefix(fd.Code, "LAI-KIT-01") && fd.Kind == kit.KindBuildCheck
	}
	if !coded {
		t.Errorf("no failed preflight check in the findings: %+v", d.Roots[0].Findings)
	}
}

// realKitBuild is the production kitBuildFn captured before the fixture replaced it.
var realKitBuild = kitBuildFn

// fnKitRunner is a machine on which every program exits 127.
type fnKitRunner struct{}

func (*fnKitRunner) Run(context.Context, execx.Cmd) (execx.Result, error) {
	return execx.Result{ExitCode: 127}, nil
}

func TestKitVerifyFactsFromTheMachine(t *testing.T) {
	// A CUDA node whose driver supports an older CUDA than the kit's toolkit: LAI-KIT-043, yellow, exit 8.
	n := kittest.AiN3()
	n.DriverCUDA = "12.2"
	f := newVerifyFix(t, n)
	d, r := f.doc(t, "kit", "verify")
	if r.code != ExitWarnings || d.Overall != "yellow" {
		t.Fatalf("exit %d overall %s", r.code, d.Overall)
	}
	if len(d.Roots[0].Findings) != 1 || d.Roots[0].Findings[0].Code != doctor.KIT043 {
		t.Fatalf("findings %+v", d.Roots[0].Findings)
	}
	// No node.yaml: the facts of os-release and the architecture still check what they can.
	g := newVerifyFix(t, kittest.AiN3())
	if err := os.Remove(g.env.Paths.NodeYAML()); err != nil {
		t.Fatal(err)
	}
	r = g.run(t, "kit", "verify")
	if r.code != ExitOK || !strings.Contains(unwrap(r.out), "Not checked, because it is not known for this computer") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
}

// ---- status ------------------------------------------------------------------------------------------

func TestKitStatusEnAndEs(t *testing.T) {
	f := newVerifyFix(t, kittest.AiN3())
	f.k.Remove("prebuilt")
	for _, c := range []struct {
		name string
		args []string
	}{
		{"kit.status.en", []string{"kit", "status"}},
		{"kit.status.es", []string{"--lang", "es", "kit", "status"}},
	} {
		r := f.run(t, c.args...)
		if r.code != ExitOK {
			t.Fatalf("%s: exit %d\n%s\n%s", c.name, r.code, r.out, r.errOut)
		}
		checkGolden(t, c.name, r)
	}
	f.k.Remove("tools/piper")
	r := f.run(t, "kit", "status")
	if r.code != ExitOK || !strings.Contains(unwrap(r.out), "The kit is missing parts for this computer: piper for amd64.") ||
		!strings.Contains(r.out, doctor.KIT040) {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	checkGolden(t, "kit.status.missing.en", r)
}

func TestKitStatusJSONAndRoots(t *testing.T) {
	n3 := kittest.AiN3()
	f := newVerifyFix(t, n3)
	mount := f.addDrive("a", false)
	drive := kittest.NewAt(t, filepath.Join(mount, "localai"))
	drive.Provision(kittest.AiN1(), n3)
	oldHost := kitHostFn
	kitHostFn = func() kitHost {
		h := oldHost()
		h.Keys = append(append(h.Keys[:0:0], f.k.Keys...), drive.Keys...)
		return h
	}
	t.Cleanup(func() { kitHostFn = oldHost })

	d, r := f.doc(t, "kit", "status")
	if r.code != ExitOK || len(d.Roots) != 2 || !d.Roots[0].HasKit || !d.Roots[1].HasKit || d.Overall != "green" {
		t.Fatalf("exit %d doc %+v", r.code, d)
	}
	if len(d.Roots[1].Lab) != 1 || d.Roots[1].Lab[0].Node != "ai-n1" || len(d.Roots[0].Lab) != 0 {
		t.Errorf("lab: drive %+v internal %+v", d.Roots[1].Lab, d.Roots[0].Lab)
	}
	one, _ := f.doc(t, "kit", "status", "--root", "internal")
	if len(one.Roots) != 1 || one.Roots[0].Root != "internal" {
		t.Errorf("--root internal: %+v", one.Roots)
	}
	human := f.run(t, "kit", "status")
	if human.code != ExitOK || !strings.Contains(human.out, "ai-n1") || !strings.Contains(unwrap(human.out), "The drive has everything") &&
		!strings.Contains(human.out, "complete") {
		t.Fatalf("exit %d\n%s", human.code, human.out)
	}
	// status re-reads nothing: a same-size change of a file is invisible to it and visible to verify.
	f.k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz")
	if r := f.run(t, "kit", "status"); r.code != ExitOK || strings.Contains(r.out, "damaged") {
		t.Fatalf("status saw a change it cannot see:\n%s", r.out)
	}
	if r := f.run(t, "kit", "verify", "--full", "--root", "internal"); r.code != ExitFailed {
		t.Fatalf("verify --full: exit %d", r.code)
	}
}
