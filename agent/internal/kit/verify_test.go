package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Tests of the kit verifier (KIT-03, rebuild-kit.md section 4.4). The kits are built on disk by
// package kittest with digests computed on the spot; the clock is a value in every test.

// day is one day after the fixtures were last verified: nothing is due for a re-hash yet.
var (
	soon = kittest.Epoch.Add(24 * time.Hour)
	late = kittest.Epoch.Add(60 * 24 * time.Hour)
)

// spyFS is the real disk that records what was read and whether the cache was dropped around it, and
// every write that reaches it (none must).
type spyFS struct {
	kit.OSFS
	mu     sync.Mutex
	events []string
	writes []string
}

func (s *spyFS) log(e string) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

// Open is the plain read (the signature check and VerifyDir); OpenSequential is the hashing read.
func (s *spyFS) Open(name string) (io.ReadCloser, error) {
	s.log("read " + name)
	return s.OSFS.Open(name)
}

func (s *spyFS) OpenSequential(name string) (io.ReadCloser, error) {
	s.log("open " + name)
	return s.OSFS.OpenSequential(name)
}

func (s *spyFS) DropCache(name string) error {
	s.log("drop " + name)
	return s.OSFS.DropCache(name)
}

func (s *spyFS) Rename(a, b string) error {
	s.writes = append(s.writes, "rename "+a)
	return s.OSFS.Rename(a, b)
}
func (s *spyFS) Remove(a string) error {
	s.writes = append(s.writes, "remove "+a)
	return s.OSFS.Remove(a)
}
func (s *spyFS) RemoveAll(a string) error {
	s.writes = append(s.writes, "removeall "+a)
	return s.OSFS.RemoveAll(a)
}
func (s *spyFS) MkdirAll(a string, m os.FileMode) error {
	s.writes = append(s.writes, "mkdir "+a)
	return s.OSFS.MkdirAll(a, m)
}
func (s *spyFS) Chmod(a string, m os.FileMode) error {
	s.writes = append(s.writes, "chmod "+a)
	return s.OSFS.Chmod(a, m)
}
func (s *spyFS) SyncDir(a string) error {
	s.writes = append(s.writes, "syncdir "+a)
	return s.OSFS.SyncDir(a)
}

// opened lists the files read, in order.
func (s *spyFS) opened() []string {
	var out []string
	for _, e := range s.events {
		if p, ok := strings.CutPrefix(e, "open "); ok {
			out = append(out, p)
		} else if p, ok := strings.CutPrefix(e, "read "); ok {
			out = append(out, p)
		}
	}
	return out
}

func newSpy() *spyFS { return &spyFS{} }

// run verifies the kit with the defaults of a test: the internal root, the clock one day after the
// fixtures, every file re-read (ModeFull), the kit's own key.
func run(t *testing.T, k *kittest.Kit, mutate func(*kit.Options)) kit.VerifyReport {
	t.Helper()
	rep, err := verifyWith(k, mutate)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return rep
}

func verifyWith(k *kittest.Kit, mutate func(*kit.Options)) (kit.VerifyReport, error) {
	return verifyOn(kit.OSFS{}, k, mutate)
}

func verifyOn(fsys kit.VerifyFS, k *kittest.Kit, mutate func(*kit.Options)) (kit.VerifyReport, error) {
	opts := kit.Options{Mode: kit.ModeFull, Now: soon, RootID: "internal", Internal: true, Keys: k.Keys}
	if mutate != nil {
		mutate(&opts)
	}
	return kit.Verifier{FS: fsys}.Verify(context.Background(), k.Root, opts)
}

func facts(n kittest.Node) func(*kit.Options) {
	f := n.Facts()
	return func(o *kit.Options) { o.Node = &f }
}

func partStatus(t *testing.T, rep kit.VerifyReport, part string) (status, code string) {
	t.Helper()
	p, ok := rep.PartOf(part)
	if !ok {
		t.Fatalf("part %s is not in the report: %+v", part, rep.Parts)
	}
	return p.Status, p.Code
}

func findCode(rep kit.VerifyReport, code string) []kit.Finding {
	var out []kit.Finding
	for _, f := range rep.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func codes(rep kit.VerifyReport) []string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, f.Code+"/"+f.Kind+"/"+f.Part)
	}
	return out
}

// ---- the three node types and a drive ---------------------------------------------------------------

func TestVerifyHealthyKitOfEachNodeType(t *testing.T) {
	tests := []struct {
		name  string
		node  kittest.Node
		parts []string
	}{
		{"ai-n1 vulkan with unified memory", kittest.AiN1(),
			[]string{"catalog", "installers", "bin", "src", "git", "toolchains", "tools", "debs", "prebuilt"}},
		{"ai-n2 cuda, toolkit packages in the debs closure (option A)", kittest.AiN2(),
			[]string{"catalog", "installers", "bin", "src", "git", "toolchains", "tools", "debs", "nvidia", "prebuilt"}},
		{"ai-n3 cuda, redistributable tarballs (option B)", kittest.AiN3(),
			[]string{"catalog", "installers", "bin", "src", "git", "toolchains", "tools", "debs", "nvidia", "cuda", "prebuilt"}},
	}
	for _, tt := range tests {
		for _, mode := range []kit.Mode{kit.ModeQuick, kit.ModeBudgeted, kit.ModeFull} {
			t.Run(tt.name+" "+string(mode), func(t *testing.T) {
				k := kittest.New(t)
				k.Provision(tt.node)
				rep := run(t, k, func(o *kit.Options) { facts(tt.node)(o); o.Mode = mode })
				if rep.Overall != kit.Green || len(rep.Findings) != 0 {
					t.Fatalf("overall %s, findings %v", rep.Overall, codes(rep))
				}
				var got []string
				for _, p := range rep.Parts {
					got = append(got, p.Part)
					if p.Status != kit.StatusOK || p.Code != "" {
						t.Errorf("part %s is %s %s", p.Part, p.Status, p.Code)
					}
				}
				if strings.Join(got, ",") != strings.Join(tt.parts, ",") {
					t.Errorf("parts %v, want %v", got, tt.parts)
				}
				if rep.Node == nil || rep.Node.Node != tt.node.ID || len(rep.Node.Needs) == 0 {
					t.Fatalf("node result %+v", rep.Node)
				}
				for _, n := range rep.Node.Needs {
					if n.Status != kit.StatusOK {
						t.Errorf("need %s %s is %s", n.Need, n.Arg, n.Status)
					}
				}
				if rep.Signature == nil || rep.Signature.Status != kit.SigOK {
					t.Errorf("signature %+v", rep.Signature)
				}
				if rep.Lab != nil {
					t.Errorf("the internal root has no lab check: %v", rep.Lab)
				}
				hashed := rep.Totals.FilesHashed > 0
				if hashed != (mode == kit.ModeFull) {
					t.Errorf("mode %s hashed %d files", mode, rep.Totals.FilesHashed)
				}
			})
		}
	}
}

func TestVerifyDriveChecksEveryPinnedNode(t *testing.T) {
	n1, n2, n3 := kittest.AiN1(), kittest.AiN2(), kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n1, n2, n3)
	rep := run(t, k, func(o *kit.Options) { facts(n3)(o); o.Internal = false; o.RootID = "ark-0a1b2c3d" })
	if rep.Overall != kit.Green || len(rep.Findings) != 0 {
		t.Fatalf("overall %s, findings %v", rep.Overall, codes(rep))
	}
	var lab []string
	for _, l := range rep.Lab {
		lab = append(lab, l.Node)
		for _, n := range l.Needs {
			if n.Status != kit.StatusOK {
				t.Errorf("%s: need %s %s is %s", l.Node, n.Need, n.Arg, n.Status)
			}
		}
	}
	if strings.Join(lab, ",") != "ai-n1,ai-n2" {
		t.Errorf("lab nodes %v (this computer is not repeated)", lab)
	}

	// Another computer of the lab lacks its llama.cpp source: yellow, LAI-KIT-041, naming the node.
	n4 := kittest.AiN1()
	n4.ID, n4.LlamaSeed = "ai-n4", "llama-old"
	k.Pins(kittest.Epoch, n1, n2, n3, n4)
	rep = run(t, k, func(o *kit.Options) { facts(n3)(o); o.Internal = false; o.RootID = "ark-0a1b2c3d" })
	got := findCode(rep, doctor.KIT041)
	if len(got) != 1 || rep.Overall != kit.Yellow || got[0].Need != kit.NeedLlamaSource || strings.Join(got[0].Nodes, ",") != "ai-n4" {
		t.Fatalf("overall %s, findings %v: %+v", rep.Overall, codes(rep), got)
	}
	if st, code := partStatus(t, rep, "src"); st != kit.StatusMissing || code != doctor.KIT041 {
		t.Errorf("src is %s %s", st, code)
	}
	if rep.Damaged() {
		t.Error("a missing part is not damage")
	}
}

func TestVerifyLabIgnoresStalePinsAndNeedsFreshOnes(t *testing.T) {
	n3 := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n3)
	// The file says ai-old has not been reported for 200 days (the merge would drop it) and ai-new
	// reported yesterday, both with commits the kit does not hold. Only the fresh one is required.
	pins := kit.Pins{
		UpdatedAt: late,
		LlamaCpp: []kit.CommitPin{
			{Commit: kittest.Hex40("old-pin"), Nodes: []string{"ai-old"}, ReportedAt: late.Add(-200 * 24 * time.Hour)},
			{Commit: kittest.Hex40("new-pin"), Nodes: []string{"ai-new"}, ReportedAt: late.Add(-24 * time.Hour)},
		},
	}
	k.WritePins(pins)
	rep := run(t, k, func(o *kit.Options) { facts(n3)(o); o.Internal = false; o.Now = late; o.Mode = kit.ModeQuick })
	got := findCode(rep, doctor.KIT041)
	if len(got) != 1 || strings.Join(got[0].Nodes, ",") != "ai-new" || got[0].Arg != kittest.Hex40("new-pin")[:9] {
		t.Fatalf("findings %v: %+v", codes(rep), got)
	}
	var stale bool
	for _, n := range rep.Notes {
		stale = stale || n.Kind == kit.NoteStalePins && len(n.Names) == 1 && strings.HasPrefix(n.Names[0], "llama.cpp ")
	}
	if !stale {
		t.Errorf("no note about the stale pin: %+v", rep.Notes)
	}
	for _, l := range rep.Lab {
		if l.Node == "ai-old" {
			t.Error("a node that only stale pins mention was judged")
		}
	}
}

func TestVerifyLabNeedsPinsAndSurvivesDamagedPins(t *testing.T) {
	n3 := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n3)
	k.Remove("catalog/pins.json")
	rep := run(t, k, func(o *kit.Options) { facts(n3)(o); o.Internal = false })
	if rep.Overall != kit.Green || len(rep.Lab) != 0 {
		t.Fatalf("no pins file: overall %s lab %v", rep.Overall, rep.Lab)
	}
	var noPins bool
	for _, n := range rep.Notes {
		noPins = noPins || n.Kind == kit.NoteNoPins
	}
	if !noPins {
		t.Errorf("notes %+v", rep.Notes)
	}
	k.Write("catalog/pins.json", []byte("{not json"))
	rep = run(t, k, func(o *kit.Options) { facts(n3)(o); o.Internal = false })
	got := findCode(rep, doctor.KIT045)
	if len(got) != 1 || rep.Overall != kit.Yellow || got[0].Part != "catalog" {
		t.Fatalf("damaged pins: overall %s %v", rep.Overall, codes(rep))
	}
	if data := k.Read("catalog/pins.json"); string(data) != "{not json" {
		t.Error("a damaged pins.json was rewritten")
	}
}

// ---- what is wrong ------------------------------------------------------------------------------------

func TestVerifyFindsWhatIsWrong(t *testing.T) {
	n3 := kittest.AiN3()
	llamaTar := "src/llama.cpp-" + n3.Llama() + ".tar.gz"
	tests := []struct {
		name string
		do   func(t *testing.T, k *kittest.Kit)
		node func(kittest.Node) kittest.Node
		// the expected outcome
		overall    string
		part       string
		status     string
		code       string
		damaged    bool
		reason     string // a problem reason that must be listed
		wantAlsoIn []string
	}{
		{name: "a tampered file", do: func(t *testing.T, k *kittest.Kit) { k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz") },
			overall: kit.Red, part: "toolchains", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonChanged},
		{name: "a truncated file", do: func(t *testing.T, k *kittest.Kit) { k.Truncate("toolchains/go1.24.0.linux-amd64.tar.gz", 3) },
			overall: kit.Red, part: "toolchains", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonTruncated},
		{name: "a tampered source tarball", do: func(t *testing.T, k *kittest.Kit) { k.Corrupt(llamaTar) },
			overall: kit.Red, part: "src", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonChanged},
		{name: "a file the manifest lists is gone", do: func(t *testing.T, k *kittest.Kit) { k.Remove("tools/piper/piper_linux_x86_64.tar.gz") },
			overall: kit.Red, part: "tools", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonMissing},
		{name: "a manifest of a newer schema", do: func(t *testing.T, k *kittest.Kit) {
			p := "toolchains/MANIFEST.json"
			k.Write(p, []byte(strings.Replace(string(k.Read(p)), `"schema_version": 1`, `"schema_version": 2`, 1)))
		}, overall: kit.Red, part: "toolchains", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonBadManifest},
		{name: "a corrupt manifest", do: func(t *testing.T, k *kittest.Kit) { k.Write("tools/kiwix-tools/MANIFEST.json", []byte("{\"oops\"")) },
			overall: kit.Red, part: "tools", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonBadManifest},
		{name: "a manifest copied into the wrong folder", do: func(t *testing.T, k *kittest.Kit) {
			k.Write("toolchains/MANIFEST.json", k.Read("tools/piper/MANIFEST.json"))
		}, overall: kit.Red, part: "toolchains", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonMisplaced},
		{name: "a tampered package in an apt closure", do: func(t *testing.T, k *kittest.Kit) { k.Corrupt("debs/resolute-amd64/pool/cmake_3.28.3-1_amd64.deb") },
			overall: kit.Red, part: "debs", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonChanged},
		{name: "a tool missing for this architecture", do: func(t *testing.T, k *kittest.Kit) { k.Remove("tools/piper") },
			overall: kit.Red, part: "tools", status: kit.StatusMissing, code: doctor.KIT040},
		{name: "a partial kit: no packages at all", do: func(t *testing.T, k *kittest.Kit) {
			k.Remove("debs")
			k.Remove("nvidia")
			k.Remove("cuda")
		}, overall: kit.Red, part: "debs", status: kit.StatusMissing, code: doctor.KIT040, wantAlsoIn: []string{"nvidia", "cuda"}},
		{name: "no Go toolchain", do: func(t *testing.T, k *kittest.Kit) { k.Remove("toolchains") },
			overall: kit.Red, part: "toolchains", status: kit.StatusMissing, code: doctor.KIT040},
		{name: "the agent source of the running version is missing", do: func(t *testing.T, k *kittest.Kit) {
			k.Remove("src/local-ai-node-v0.4.0-src.tar.gz")
		}, overall: kit.Red, part: "src", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonMissing},
		{name: "a source tarball no manifest records", do: func(t *testing.T, k *kittest.Kit) {
			k.Write("src/llama.cpp-"+kittest.Hex40("a-newer-commit")+".tar.gz", []byte("stray"))
		}, node: func(n kittest.Node) kittest.Node { n.LlamaSeed = "a-newer-commit"; return n },
			overall: kit.Red, part: "src", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true},
		{name: "a tampered signature file", do: func(t *testing.T, k *kittest.Kit) { k.Corrupt("bin/release/SHA256SUMS.minisig") },
			overall: kit.Red, part: "bin", status: kit.StatusDamaged, code: doctor.KIT044, damaged: true},
		{name: "a signature made with another key", do: func(t *testing.T, k *kittest.Kit) {
			other := kittest.New(t)
			other.Provision(n3)
			k.Write("bin/release/SHA256SUMS.minisig", other.Read("bin/release/SHA256SUMS.minisig"))
		}, overall: kit.Red, part: "bin", status: kit.StatusDamaged, code: doctor.KIT044, damaged: true},
		{name: "the signature file is missing", do: func(t *testing.T, k *kittest.Kit) { k.Remove("bin/release/SHA256SUMS.minisig") },
			overall: kit.Red, part: "bin", status: kit.StatusDamaged, code: doctor.KIT044, damaged: true},
		{name: "a binary changed after it was signed", do: func(t *testing.T, k *kittest.Kit) { k.Corrupt("bin/aiagent-linux-amd64") },
			overall: kit.Red, part: "bin", status: kit.StatusDamaged, code: doctor.KIT044, damaged: true, reason: kit.ReasonChanged},
		{name: "no prebuilt for the current build is only a warning", do: func(t *testing.T, k *kittest.Kit) { k.Remove("prebuilt") },
			overall: kit.Yellow, part: "prebuilt", status: kit.StatusWarning, code: doctor.KIT040},
		{name: "a prebuilt for another commit is not the current build", do: func(t *testing.T, k *kittest.Kit) {
			k.Remove("prebuilt")
			k.Item("prebuilt", "kit-prebuilt", map[string][]byte{"llama.cpp-" + kittest.Hex40("other")[:9] + "-cuda-resolute-amd64-sm75.tar.gz": []byte("x")})
		}, overall: kit.Yellow, part: "prebuilt", status: kit.StatusWarning, code: doctor.KIT040},
		{name: "a prebuilt for another GPU generation is not the current build", do: func(t *testing.T, k *kittest.Kit) {
			k.Remove("prebuilt")
			k.Item("prebuilt", "kit-prebuilt", map[string][]byte{"llama.cpp-" + n3.Llama()[:9] + "-cuda-resolute-amd64-sm86.tar.gz": []byte("x")})
		}, overall: kit.Yellow, part: "prebuilt", status: kit.StatusWarning, code: doctor.KIT040},
		{name: "a damaged prebuilt is damage", do: func(t *testing.T, k *kittest.Kit) {
			k.Corrupt("prebuilt/llama.cpp-" + n3.Llama()[:9] + "-cuda-resolute-amd64-sm75.tar.gz")
		}, overall: kit.Red, part: "prebuilt", status: kit.StatusDamaged, code: doctor.KIT001, damaged: true, reason: kit.ReasonChanged},
		{name: "debs for another Ubuntu release", do: func(t *testing.T, k *kittest.Kit) {
			if err := os.Rename(k.Abs("debs/resolute-amd64"), k.Abs("debs/noble-amd64")); err != nil {
				t.Fatal(err)
			}
			k.Reseal("debs/noble-amd64", "kit-debs-noble-amd64")
		}, overall: kit.Red, part: "debs", status: kit.StatusInconsistent, code: doctor.KIT042},
		{name: "an NVIDIA closure for another release", do: func(t *testing.T, k *kittest.Kit) {
			if err := os.Rename(k.Abs("nvidia/resolute-amd64-595-open"), k.Abs("nvidia/noble-amd64-595-open")); err != nil {
				t.Fatal(err)
			}
			k.Reseal("nvidia/noble-amd64-595-open", "kit-nvidia-noble-amd64-595-open")
		}, overall: kit.Red, part: "nvidia", status: kit.StatusInconsistent, code: doctor.KIT042},
		{name: "an NVIDIA closure of another driver branch is missing, not another release", do: func(t *testing.T, k *kittest.Kit) {
			if err := os.Rename(k.Abs("nvidia/resolute-amd64-595-open"), k.Abs("nvidia/resolute-amd64-550-open")); err != nil {
				t.Fatal(err)
			}
			k.Reseal("nvidia/resolute-amd64-550-open", "kit-nvidia-resolute-amd64-550-open")
		}, overall: kit.Red, part: "nvidia", status: kit.StatusMissing, code: doctor.KIT040},
		{name: "a toolkit newer than the driver (option B)", do: func(t *testing.T, k *kittest.Kit) {},
			node:    func(n kittest.Node) kittest.Node { n.DriverCUDA = "12.2"; return n },
			overall: kit.Yellow, part: "cuda", status: kit.StatusInconsistent, code: doctor.KIT043},
		{name: "a toolkit as new as the driver is fine", do: func(t *testing.T, k *kittest.Kit) {},
			node:    func(n kittest.Node) kittest.Node { n.DriverCUDA = "12.4"; return n },
			overall: kit.Green, part: "cuda", status: kit.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kittest.New(t)
			k.Provision(n3)
			tt.do(t, k)
			n := n3
			if tt.node != nil {
				n = tt.node(n3)
			}
			rep := run(t, k, facts(n))
			if rep.Overall != tt.overall {
				t.Errorf("overall %s, want %s (findings %v)", rep.Overall, tt.overall, codes(rep))
			}
			if st, code := partStatus(t, rep, tt.part); st != tt.status || code != tt.code {
				t.Errorf("part %s is %s %s, want %s %s (findings %v)", tt.part, st, code, tt.status, tt.code, codes(rep))
			}
			for _, p := range tt.wantAlsoIn {
				if st, code := partStatus(t, rep, p); st != tt.status || code != tt.code {
					t.Errorf("part %s is %s %s, want %s %s", p, st, code, tt.status, tt.code)
				}
			}
			if rep.Damaged() != tt.damaged {
				t.Errorf("Damaged() = %v, want %v (findings %v)", rep.Damaged(), tt.damaged, codes(rep))
			}
			if tt.reason != "" {
				found := false
				for _, it := range rep.Items {
					for _, p := range it.Problems {
						found = found || p.Reason == tt.reason
					}
				}
				for _, f := range rep.Findings {
					for _, nm := range f.Names {
						found = found || strings.Contains(nm, "("+tt.reason+")")
					}
				}
				if !found {
					t.Errorf("no problem with the reason %q: %+v", tt.reason, rep.Items)
				}
			}
		})
	}
}

func TestVerifyWrongReleaseIsYellowMissingOnADrive(t *testing.T) {
	n3 := kittest.AiN3()
	n3.Codename = "noble"
	k := kittest.New(t)
	k.Provision(n3)
	// This computer runs resolute now; the kit was synced for noble. On the internal root that is
	// LAI-KIT-042 (red); a drive holds closures for several releases, so there it is only "not for
	// this computer yet" (LAI-KIT-040, yellow).
	now := n3
	now.Codename = "resolute"
	internal := run(t, k, facts(now))
	wr := findCode(internal, doctor.KIT042)
	if len(wr) == 0 || internal.Overall != kit.Red || wr[0].Want != "resolute-amd64" || len(wr[0].Have) == 0 || wr[0].Have[0] != "debs/noble-amd64" {
		t.Fatalf("internal: overall %s findings %v %+v", internal.Overall, codes(internal), wr)
	}
	drive := run(t, k, func(o *kit.Options) { facts(now)(o); o.Internal = false })
	if len(findCode(drive, doctor.KIT042)) != 0 || len(findCode(drive, doctor.KIT040)) == 0 || drive.Overall != kit.Yellow {
		t.Fatalf("drive: overall %s findings %v", drive.Overall, codes(drive))
	}
}

func TestVerifyToolkitOptionAUsesTheLockOfTheDebsClosure(t *testing.T) {
	n2 := kittest.AiN2()
	n2.DriverCUDA = "11.8"
	k := kittest.New(t)
	k.Provision(n2)
	rep := run(t, k, facts(n2))
	got := findCode(rep, doctor.KIT043)
	if len(got) != 1 || got[0].Have[0] != "12.4" || got[0].Want != "11.8" || got[0].Part != "debs" || got[0].Item != "kit-debs-resolute-amd64" {
		t.Fatalf("findings %v: %+v", codes(rep), got)
	}
	if rep.Overall != kit.Yellow {
		t.Errorf("overall %s", rep.Overall)
	}
	// Without the answer of nvidia-smi the comparison is skipped, not failed.
	n2.DriverCUDA = ""
	rep = run(t, k, facts(n2))
	if len(findCode(rep, doctor.KIT043)) != 0 || rep.Overall != kit.Green {
		t.Fatalf("no driver answer: overall %s %v", rep.Overall, codes(rep))
	}
	var skipped bool
	for _, n := range rep.Notes {
		skipped = skipped || n.Kind == kit.NoteSkipped
	}
	if !skipped {
		t.Error("the skipped comparison is not mentioned")
	}
}

func TestVerifyCudaNeedsAToolkitFromOptionAOrB(t *testing.T) {
	n := kittest.AiN3()
	n.Redist = ""
	k := kittest.New(t)
	k.Provision(n)
	rep := run(t, k, facts(n))
	got := findCode(rep, doctor.KIT040)
	if len(got) != 1 || got[0].Need != kit.NeedCuda || rep.Overall != kit.Red {
		t.Fatalf("overall %s findings %v", rep.Overall, codes(rep))
	}
	// A vulkan node needs no toolkit at all.
	v := kittest.AiN1()
	k2 := kittest.New(t)
	k2.Provision(v)
	if rep := run(t, k2, facts(v)); rep.Overall != kit.Green {
		t.Fatalf("vulkan: %v", codes(rep))
	}
}

// ---- --full, the budget and the quick mode ------------------------------------------------------------

func TestVerifyBudgetFullAndQuickReadDifferentAmounts(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	// The toolchain item is the only one whose last re-verification is old.
	k.Item("toolchains", "kit-go-toolchain", map[string][]byte{"go1.24.0.linux-amd64.tar.gz": []byte("go toolchain amd64\n")},
		kittest.Reverified(kittest.Epoch.Add(-90*24*time.Hour)))
	tests := []struct {
		name    string
		mode    kit.Mode
		now     time.Time
		hashed  func(rep kit.VerifyReport) bool
		onlyDir string
	}{
		{"quick reads nothing", kit.ModeQuick, soon, func(r kit.VerifyReport) bool { return r.Totals.FilesHashed == 0 }, ""},
		{"budgeted: only what is overdue", kit.ModeBudgeted, soon, func(r kit.VerifyReport) bool { return r.Totals.FilesHashed == 1 }, "toolchains"},
		{"budgeted later: everything is overdue", kit.ModeBudgeted, late, func(r kit.VerifyReport) bool { return r.Totals.FilesHashed > 10 }, ""},
		{"full: everything now", kit.ModeFull, soon, func(r kit.VerifyReport) bool { return r.Totals.FilesHashed > 10 }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newSpy()
			rep, err := verifyOn(spy, k, func(o *kit.Options) { facts(n)(o); o.Mode, o.Now = tt.mode, tt.now })
			if err != nil {
				t.Fatal(err)
			}
			if !tt.hashed(rep) {
				t.Errorf("%d files hashed (%d bytes)", rep.Totals.FilesHashed, rep.Totals.BytesHashed)
			}
			if tt.onlyDir != "" {
				for _, f := range spy.opened() {
					rel := strings.TrimPrefix(f, k.Dir+"/")
					// bin/ is read for the signature of the agent checksums, whatever is due
					if !strings.HasPrefix(rel, tt.onlyDir+"/") && !strings.HasPrefix(rel, "bin/") {
						t.Errorf("read %s, but only %s was due", rel, tt.onlyDir)
					}
				}
			}
		})
	}
}

func TestVerifyQuickCannotSeeAChangeThatKeepsTheSizeButFullCan(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz")
	if rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Mode = kit.ModeQuick }); rep.Overall != kit.Green {
		t.Fatalf("quick: %v", codes(rep))
	}
	if rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Mode = kit.ModeBudgeted }); rep.Overall != kit.Green {
		t.Fatalf("budgeted, nothing due: %v", codes(rep))
	}
	if rep := run(t, k, facts(n)); rep.Overall != kit.Red || !rep.Damaged() {
		t.Fatalf("full: %v", codes(rep))
	}
}

// ---- --part -----------------------------------------------------------------------------------------------

func TestVerifyPartReadsOnlyThatPart(t *testing.T) {
	n := kittest.AiN3()
	for _, part := range kit.Parts {
		t.Run(part, func(t *testing.T) {
			k := kittest.New(t)
			k.Provision(n)
			spy := newSpy()
			rep, err := verifyOn(spy, k, func(o *kit.Options) { facts(n)(o); o.Part = part })
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range rep.Parts {
				if p.Part != part {
					t.Errorf("part %s in a run limited to %s", p.Part, part)
				}
			}
			for _, it := range rep.Items {
				if it.Part != part && len(it.Problems) > 0 {
					t.Errorf("problems of %s in a run limited to %s", it.Dir, part)
				}
			}
			for _, f := range spy.opened() {
				rel := strings.TrimPrefix(f, k.Dir+"/")
				ok := strings.HasPrefix(rel, part+"/")
				// the tarballs of a mirror are recorded by the mirror but belong to src
				if part == "src" && strings.HasPrefix(rel, "git/") {
					ok = false
				}
				if part == "installers" && rel == "installers/install-node.sh" {
					ok = true
				}
				if !ok {
					t.Errorf("a run limited to %s read %s", part, rel)
				}
			}
			if rep.Overall != kit.Green {
				t.Errorf("overall %s %v", rep.Overall, codes(rep))
			}
		})
	}
}

func TestVerifyPartDebsRehashesOnlyDebs(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz") // damage outside the part is not looked at
	spy := newSpy()
	rep, err := verifyOn(spy, k, func(o *kit.Options) { facts(n)(o); o.Part = "debs" })
	if err != nil {
		t.Fatal(err)
	}
	if rep.Overall != kit.Green || rep.Totals.FilesHashed == 0 {
		t.Fatalf("overall %s hashed %d", rep.Overall, rep.Totals.FilesHashed)
	}
	for _, f := range spy.opened() {
		if !strings.HasPrefix(f, k.Dir+"/debs/") {
			t.Errorf("read %s", f)
		}
	}
	// And a damaged deb is found through VerifyRepo.
	k.Corrupt("debs/resolute-amd64/pool/cmake_3.28.3-1_amd64.deb")
	rep = run(t, k, func(o *kit.Options) { facts(n)(o); o.Part = "debs" })
	if st, code := partStatus(t, rep, "debs"); st != kit.StatusDamaged || code != doctor.KIT001 {
		t.Fatalf("debs %s %s", st, code)
	}
}

func TestVerifyPartSrcChecksTheTarballsTheMirrorRecords(t *testing.T) {
	n := kittest.AiN3()
	for part, wantDamage := range map[string]bool{"src": true, "git": false} {
		k := kittest.New(t)
		k.Provision(n)
		k.Corrupt("src/llama.cpp-" + n.Llama() + ".tar.gz")
		rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Part = part })
		if rep.Damaged() != wantDamage {
			t.Errorf("--part %s: damaged=%v, want %v (%v)", part, rep.Damaged(), wantDamage, codes(rep))
		}
	}
	k := kittest.New(t)
	k.Provision(n)
	k.Corrupt("installers/install-node.sh")
	if rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Part = "installers" }); !rep.Damaged() {
		t.Errorf("a damaged installer was not found: %v", codes(rep))
	}
}

func TestVerifyUnknownPart(t *testing.T) {
	k := kittest.New(t)
	k.Provision(kittest.AiN1())
	_, err := verifyWith(k, func(o *kit.Options) { o.Part = "nope" })
	if !errors.Is(err, kit.ErrUnknownPart) {
		t.Fatalf("err = %v", err)
	}
}

// ---- reading only -------------------------------------------------------------------------------------------

func TestVerifyNeverWritesAndDropsTheCacheAroundEveryRead(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Corrupt("toolchains/go1.24.0.linux-amd64.tar.gz") // a damaged kit is reported, never repaired or moved
	if err := filepath.Walk(k.Root, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			err = os.Chmod(p, info.Mode()&^0o222) // a read-only root
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.Walk(k.Root, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				os.Chmod(p, info.Mode()|0o700)
			}
			return nil
		})
	})
	before := kittest.Join(k.Paths())
	spy := newSpy()
	rep, err := verifyOn(spy, k, func(o *kit.Options) {
		facts(n)(o)
		o.Runner = &fnRunner{fn: func(c execx.Cmd) (execx.Result, error) { return execx.Result{}, nil }}
		o.LookPath = lookTo
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Damaged() {
		t.Fatalf("the damage was not found: %v", codes(rep))
	}
	if after := kittest.Join(k.Paths()); after != before {
		t.Errorf("the kit changed:\n%s\n---\n%s", before, after)
	}
	if len(spy.writes) != 0 {
		t.Errorf("writes reached the disk: %v", spy.writes)
	}
	if len(spy.opened()) == 0 {
		t.Fatal("nothing was read")
	}
	// Every file that was hashed is preceded and followed by a drop of its cached pages: what is hashed
	// is the disk, and checking gigabytes does not evict a loaded model.
	ev := spy.events
	for i, e := range ev {
		p, ok := strings.CutPrefix(e, "open ")
		if !ok || strings.HasSuffix(p, "SHA256SUMS.minisig") || strings.HasSuffix(p, "/SHA256SUMS") {
			continue
		}
		if i == 0 || ev[i-1] != "drop "+p {
			t.Errorf("%s was read without its cache dropped first (before: %q)", p, ev[max(0, i-1)])
		}
		after := false
		for _, later := range ev[i+1:] {
			if later == "drop "+p {
				after = true
				break
			}
			if strings.HasPrefix(later, "open ") {
				break
			}
		}
		if !after {
			t.Errorf("%s was read without its cache dropped afterwards", p)
		}
	}
}

func TestReadOnlyViewRefusesEveryWrite(t *testing.T) {
	k := kittest.New(t)
	k.Write("x", []byte("x"))
	ro := kit.ReadOnly(kit.OSFS{})
	p := k.Abs("x")
	if _, err := ro.OpenFile(p, os.O_WRONLY, 0o644); !errors.Is(err, kit.ErrReadOnly) {
		t.Errorf("OpenFile for writing: %v", err)
	}
	if f, err := ro.OpenFile(p, os.O_RDONLY, 0); err != nil {
		t.Errorf("OpenFile for reading: %v", err)
	} else {
		f.Close()
	}
	for name, err := range map[string]error{
		"rename": ro.Rename(p, p+"2"), "remove": ro.Remove(p), "chmod": ro.Chmod(p, 0o600), "chown": ro.Chown(p, 0, 0),
		"syncdir": ro.SyncDir(k.Dir), "mkdir": ro.MkdirAll(k.Abs("a/b"), 0o755), "removeall": ro.RemoveAll(p),
		"symlink": ro.Symlink(p, p+"3"),
	} {
		if !errors.Is(err, kit.ErrReadOnly) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if string(k.Read("x")) != "x" {
		t.Error("the file changed")
	}
	if _, err := ro.ReadFile(p); err != nil {
		t.Error(err)
	}
}

func TestVerifyNeedsAKitAndStopsOnCancel(t *testing.T) {
	root := t.TempDir()
	_, err := kit.Verifier{FS: kit.OSFS{}}.Verify(context.Background(), root, kit.Options{Now: soon})
	if !errors.Is(err, kit.ErrNoKit) {
		t.Fatalf("err = %v", err)
	}
	k := kittest.New(t)
	k.Provision(kittest.AiN3())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (kit.Verifier{FS: kit.OSFS{}}).Verify(ctx, k.Root, kit.Options{Now: soon, Mode: kit.ModeFull}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// ---- the signature, the mirrors, the build, the index, the node ------------------------------------------

func TestVerifyWithoutAKeyCannotCheckTheSignature(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Keys = nil })
	if rep.Signature == nil || rep.Signature.Status != kit.SigUnchecked || rep.Damaged() {
		t.Fatalf("signature %+v findings %v", rep.Signature, codes(rep))
	}
	if st, code := partStatus(t, rep, "bin"); st != kit.StatusWarning || code != "" {
		t.Errorf("bin is %s %s", st, code)
	}
	if rep.Overall != kit.Yellow {
		t.Errorf("a signature nobody could check is not green: %s", rep.Overall)
	}
	// With the key missing from the program a tampered SHA256SUMS.minisig cannot be told from a good one,
	// but a missing one still can.
	k.Remove("bin/release/SHA256SUMS.minisig")
	if rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Keys = nil }); !rep.HasCode(doctor.KIT044) {
		t.Errorf("a missing signature file: %v", codes(rep))
	}
}

func TestVerifyMirrorStructureAndFsck(t *testing.T) {
	n := kittest.AiN3()
	var runs [][]string
	runner := func(exit int) *fnRunner {
		return &fnRunner{fn: func(c execx.Cmd) (execx.Result, error) {
			runs = append(runs, c.Args)
			return execx.Result{ExitCode: exit, Stderr: []byte("error in a pack")}, nil
		}}
	}
	t.Run("a mirror without HEAD", func(t *testing.T) {
		k := kittest.New(t)
		k.Provision(n)
		k.Remove("git/llama.cpp.git/HEAD")
		rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Part = "git" })
		if st, code := partStatus(t, rep, "git"); st != kit.StatusDamaged || code != doctor.KIT001 {
			t.Fatalf("git %s %s", st, code)
		}
	})
	t.Run("git fsck passes, in full mode only", func(t *testing.T) {
		k := kittest.New(t)
		k.Provision(n)
		runs = nil
		rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Runner, o.LookPath = runner(0), lookTo })
		if rep.Overall != kit.Green || len(runs) != 2 {
			t.Fatalf("overall %s, fsck runs %d", rep.Overall, len(runs))
		}
		joined := strings.Join(runs[0], " ")
		if !strings.Contains(joined, "fsck --connectivity-only --no-dangling") || !strings.Contains(joined, "safe.directory=") {
			t.Errorf("args %v", runs[0])
		}
		runs = nil
		run(t, k, func(o *kit.Options) { facts(n)(o); o.Mode = kit.ModeBudgeted; o.Runner, o.LookPath = runner(0), lookTo })
		if len(runs) != 0 {
			t.Errorf("fsck ran in budgeted mode: %v", runs)
		}
	})
	t.Run("git fsck fails", func(t *testing.T) {
		k := kittest.New(t)
		k.Provision(n)
		rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Runner, o.LookPath = runner(1), lookTo })
		if st, code := partStatus(t, rep, "git"); st != kit.StatusDamaged || code != doctor.KIT001 {
			t.Fatalf("git %s %s (%v)", st, code, codes(rep))
		}
	})
	t.Run("no git is a note, not a failure", func(t *testing.T) {
		k := kittest.New(t)
		k.Provision(n)
		rep := run(t, k, func(o *kit.Options) {
			facts(n)(o)
			o.Runner = runner(0)
			o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
		})
		var noted bool
		for _, nt := range rep.Notes {
			noted = noted || nt.Kind == kit.NoteNoGit
		}
		if !noted || rep.Overall != kit.Green {
			t.Fatalf("overall %s notes %+v", rep.Overall, rep.Notes)
		}
	})
}

func TestVerifyBuildabilityIsStepSix(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	calls := 0
	pass := func(context.Context) kit.BuildResult {
		calls++
		return kit.BuildResult{OK: true, Checks: []kit.BuildCheck{{ID: "source", Title: "Source", OK: true}}}
	}
	rep := run(t, k, func(o *kit.Options) { facts(n)(o); o.Build = pass })
	if rep.Build == nil || !rep.Build.OK || rep.Overall != kit.Green || calls != 1 {
		t.Fatalf("build %+v overall %s calls %d", rep.Build, rep.Overall, calls)
	}
	fail := func(context.Context) kit.BuildResult {
		return kit.BuildResult{Checks: []kit.BuildCheck{
			{ID: "source", Title: "Source", OK: true},
			{ID: "tools", Title: "Build tools", OK: false, Code: doctor.KIT010, Detail: "cmake is missing"},
		}}
	}
	rep = run(t, k, func(o *kit.Options) { facts(n)(o); o.Build = fail })
	got := findCode(rep, doctor.KIT010)
	if len(got) != 1 || rep.Overall != kit.Red || got[0].Kind != kit.KindBuildCheck || !strings.Contains(got[0].Detail, "cmake") {
		t.Fatalf("findings %v overall %s", codes(rep), rep.Overall)
	}
	// A run limited to one part, and the drill's steps 1 to 5 (no Build), do not make the dry run.
	calls = 0
	run(t, k, func(o *kit.Options) { facts(n)(o); o.Build = pass; o.Part = "debs" })
	if calls != 0 {
		t.Error("the dry run ran for a single part")
	}
}

func TestVerifyIndexOutOfDateIsOnlyANote(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	budgeted := func(o *kit.Options) { facts(n)(o); o.Mode = kit.ModeBudgeted }
	if rep := run(t, k, budgeted); len(rep.Notes) != 0 {
		t.Fatalf("a fresh index has notes: %+v", rep.Notes)
	}
	k.Item("desktop", "kit-desktop", map[string][]byte{"ai-desktop_0.1.0_amd64.deb": []byte("desktop\n")})
	rep := run(t, k, budgeted)
	var stale bool
	for _, nt := range rep.Notes {
		stale = stale || nt.Kind == kit.NoteIndexStale && nt.Code == doctor.KIT046
	}
	if !stale || rep.Overall != kit.Green {
		t.Fatalf("overall %s notes %+v", rep.Overall, rep.Notes)
	}
	k.Remove("MANIFEST.json")
	if rep := run(t, k, budgeted); len(rep.Notes) == 0 {
		t.Error("a missing index is not mentioned")
	}
}

func TestVerifyWithoutFactsSaysSo(t *testing.T) {
	k := kittest.New(t)
	k.Provision(kittest.AiN3())
	rep := run(t, k, nil)
	if rep.Node != nil || rep.Overall != kit.Green {
		t.Fatalf("node %+v overall %s", rep.Node, rep.Overall)
	}
	var skipped bool
	for _, nt := range rep.Notes {
		skipped = skipped || nt.Kind == kit.NoteSkipped
	}
	if !skipped {
		t.Error("nothing says that this computer's needs were not checked")
	}
	// Facts with holes skip only the rules that need them.
	rep = run(t, k, func(o *kit.Options) { o.Node = &kit.NodeFacts{NodeID: "ai-n3", Arch: "amd64"} })
	if rep.Node == nil || rep.Overall != kit.Green {
		t.Fatalf("partial facts: %+v %v", rep.Node, codes(rep))
	}
	for _, nd := range rep.Node.Needs {
		if nd.Need == kit.NeedLlamaSource || nd.Need == kit.NeedDebs {
			t.Errorf("judged %s without the facts it needs", nd.Need)
		}
	}
}

func TestVerifyShortCommitNamesOneTarball(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	f := n.Facts()
	f.LlamaCommit = f.LlamaCommit[:12]
	rep := run(t, k, func(o *kit.Options) { o.Node = &f })
	if rep.Overall != kit.Green {
		t.Fatalf("a short commit: %v", codes(rep))
	}
	f.LlamaCommit = "0000000"
	rep = run(t, k, func(o *kit.Options) { o.Node = &f })
	if !rep.HasCode(doctor.KIT040) {
		t.Fatalf("an absent commit: %v", codes(rep))
	}
}

// ---- the report ------------------------------------------------------------------------------------------------

func TestReportJSONShapeAndGolden(t *testing.T) {
	n := kittest.AiN3()
	k := kittest.New(t)
	k.Provision(n)
	k.Corrupt("tools/piper/piper_linux_x86_64.tar.gz")
	k.Remove("prebuilt")
	rep := run(t, k, facts(n))
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), k.Root, "<root>")
	text = regexp.MustCompile(`"key_id": "[^"]*"`).ReplaceAllString(text, `"key_id": "<key>"`)
	golden(t, "verify-damaged.json", text+"\n")

	// The compact contract of rebuild-kit.md section 4.4: {"overall", "parts":[{"part","status","code"}]}.
	var generic struct {
		Overall string `json:"overall"`
		Parts   []struct {
			Part   string `json:"part"`
			Status string `json:"status"`
			Code   string `json:"code"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	if generic.Overall != "red" || len(generic.Parts) == 0 {
		t.Fatalf("%+v", generic)
	}
	byPart := map[string][2]string{}
	for _, p := range generic.Parts {
		byPart[p.Part] = [2]string{p.Status, p.Code}
	}
	if byPart["tools"] != [2]string{"damaged", "LAI-KIT-001"} || byPart["prebuilt"] != [2]string{"warning", "LAI-KIT-040"} {
		t.Errorf("parts %v", byPart)
	}
	// The same kit always gives the same bytes.
	again, _ := json.MarshalIndent(run(t, k, facts(n)), "", "  ")
	if string(again) != string(data) {
		t.Error("the report is not deterministic")
	}
}

func TestStatusIsTheQuickReportOfEveryRoot(t *testing.T) {
	n1, n3 := kittest.AiN1(), kittest.AiN3()
	internal := kittest.New(t)
	internal.Provision(n3)
	drive := kittest.New(t)
	drive.Provision(n1, n3)
	empty := t.TempDir()
	spy := newSpy()
	f := n3.Facts()
	st, err := kit.Verifier{FS: spy}.Status(context.Background(), []kit.StatusRoot{
		{ID: "internal", Path: internal.Root, Internal: true},
		{ID: "ark-0a1b2c3d", Path: drive.Root},
		{ID: "bare", Path: empty},
	}, kit.StatusOptions{Now: soon, Node: &f, Keys: append(append([]update.PublicKey(nil), internal.Keys...), drive.Keys...)})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Roots) != 3 || !st.Roots[0].HasKit || !st.Roots[1].HasKit || st.Roots[2].HasKit {
		t.Fatalf("%+v", st.Roots)
	}
	if st.Overall != kit.Green {
		t.Errorf("overall %s: %+v / %+v", st.Overall, st.Roots[0].Findings, st.Roots[1].Findings)
	}
	for _, f := range spy.opened() {
		if !strings.HasSuffix(f, "SHA256SUMS.minisig") && !strings.HasSuffix(f, "/SHA256SUMS") && !strings.Contains(f, "/bin/") {
			t.Errorf("status read %s: it must not re-hash", f)
		}
	}
	if len(st.Roots[1].Lab) != 1 || st.Roots[1].Lab[0].Node != "ai-n1" || st.Roots[0].Lab != nil {
		t.Errorf("lab: drive %+v internal %+v", st.Roots[1].Lab, st.Roots[0].Lab)
	}
	if st.Roots[0].Node == nil || len(st.Roots[0].Parts) == 0 {
		t.Errorf("internal root %+v", st.Roots[0])
	}
}
