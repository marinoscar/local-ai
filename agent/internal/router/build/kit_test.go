package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// ---- the source order: tarball, then mirror, then GitHub ---------------------------------

func TestFromKitOfflineBuildsFromTheTarball(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	res, err := k.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.Preflight == nil || !res.Preflight.OK() || res.Preflight.Source != "kit-tarball" {
		t.Fatalf("preflight = %+v", res.Preflight)
	}
	// The kit source is extracted only into src/<commit>/ (the top-level directory of the tarball
	// is dropped) and the build uses that tree, nothing under /var/lib/localai/build.
	if got := readFile(t, filepath.Join(k.srcTree(), "CMakeLists.txt")); got != "project(llama)\n" {
		t.Errorf("CMakeLists.txt = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(k.srcTree(), "scripts", "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("an executable file lost its mode: %v %v", fi, err)
	}
	for _, c := range k.exec.all() {
		if filepath.Base(c.Path) == "git" {
			t.Errorf("git ran for a tarball: %s", line(c))
		}
		if filepath.Base(c.Path) == "cmake" && len(c.Args) > 1 && c.Args[0] == "-S" && c.Args[1] != k.srcTree() {
			t.Errorf("cmake was told to build %s, want %s", c.Args[1], k.srcTree())
		}
	}
	entries, _ := os.ReadDir(k.scratch)
	if len(entries) != 0 {
		t.Errorf("nothing may be written to the scratch directory for a source: %v", entries)
	}
	if res.BuildID != testID || res.Switched {
		t.Errorf("result = %+v", res)
	}
}

func TestFromKitShortCommitFindsTheTarballByPrefix(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "mirror", "")
	k.req.Commit = testSHA[:8]
	if _, err := k.build(); err != nil {
		t.Fatal(err)
	}
	if !k.exists("src/" + testSHA + "/CMakeLists.txt") {
		t.Error("the tree of the full sha was not extracted")
	}
}

func TestFromKitOfflineSourceMatrix(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, k *kitEnv) *kitGit
		code  string // "" = the build succeeds
		via   string // the source that delivered (git calls) when it succeeds
	}{
		{
			name:  "no kit source at all",
			setup: func(*testing.T, *kitEnv) *kitGit { return nil },
			code:  doctor.KIT002,
		},
		{
			name: "the tarball is of another commit",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				other := testSHA[:9] + strings.Repeat("0", 31)
				k.kitTarball(other, kitTree(t, other), "src", "")
				return nil
			},
			code: doctor.KIT002,
		},
		{
			name: "the mirror does not have the commit",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				g := &kitGit{have: false}
				k.kitMirror(g)
				return g
			},
			code: doctor.KIT002,
		},
		{
			name: "the mirror has it",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
				k.kitMirror(g)
				return g
			},
			via: "mirror",
		},
		{
			name: "tarball hash mismatch falls back to the mirror",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				k.kitTarball(testSHA, kitTree(t, testSHA), "mirror", sum256([]byte("another file")))
				g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
				k.kitMirror(g)
				return g
			},
			via: "mirror",
		},
		{
			name: "tarball hash mismatch and the mirror lacks the commit is LAI-KIT-001",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				k.kitTarball(testSHA, kitTree(t, testSHA), "src", sum256([]byte("another file")))
				g := &kitGit{have: false}
				k.kitMirror(g)
				return g
			},
			code: doctor.KIT001,
		},
		{
			name: "a tarball nobody recorded is not trusted",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				k.kitTarball(testSHA, kitTree(t, testSHA), "", "")
				return nil
			},
			code: doctor.KIT001,
		},
		{
			name: "the tarball wins over the mirror",
			setup: func(t *testing.T, k *kitEnv) *kitGit {
				k.kitTarball(testSHA, kitTree(t, testSHA), "mirror", "")
				g := &kitGit{have: true, sha: testSHA, archive: treeTar(t)}
				k.kitMirror(g)
				return g
			},
			via: "tarball",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newKitEnv(t)
			g := tc.setup(t, k)
			// Without a mirror in the fixture there is no git to be asked: it must not be needed.
			_, err := k.build()
			if tc.code != "" {
				e := mustErr(t, err)
				if e.Code != tc.code {
					t.Fatalf("code = %s (%v), want %s", e.Code, e, tc.code)
				}
				if k.compiled() {
					t.Error("a build ran without a source")
				}
				if k.exists("src/" + testSHA + "/CMakeLists.txt") {
					t.Error("a failed source left a tree behind")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			archived := g != nil && strings.Contains(strings.Join(g.calls, "\n"), "archive")
			if (tc.via == "mirror") != archived {
				t.Errorf("via %s but git archive ran = %v", tc.via, archived)
			}
		})
	}
}

func TestOfflineNeverTriesTheNetworkSource(t *testing.T) {
	k := newKitEnv(t)
	k.opts.Sources = ChainWithKit("llama.cpp", []string{k.root}, "https://example.test/llama.git")
	k.req.FromKit = false // the default chain, offline
	k.req.DryRun = false
	g := &fakeGit{}
	k.exec.on(g.handler)
	_, err := k.build()
	if e := mustErr(t, err); e.Code != doctor.KIT002 {
		t.Fatalf("got %v", e)
	}
	for _, l := range g.lines() {
		if strings.Contains(l, "clone") {
			t.Fatalf("offline cloned: %s", l)
		}
	}
}

func TestExtractionOfAKitTarballRejectsTraversal(t *testing.T) {
	k := newKitEnv(t)
	pre := "llama.cpp-" + testSHA + "/"
	evil := makeTar(t, []tarEntry{
		{name: pre + "CMakeLists.txt", body: "project(llama)\n"},
		{name: pre + "../../escaped.txt", body: "no"},
	}, true)
	k.kitTarball(testSHA, evil, "src", "")
	_, err := k.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["cause"] != "source_failed" {
		t.Fatalf("got %v", e)
	}
	if k.exists("src/" + testSHA + "/CMakeLists.txt") {
		t.Error("a tree from an unsafe archive was kept")
	}
	if _, err := os.Stat(filepath.Join(k.harness.root, "escaped.txt")); err == nil {
		t.Error("a file was written outside the tree")
	}
}

// ---- the preflight -----------------------------------------------------------------------

func TestPreflightAllPass(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	rep := k.manager().PreflightReport(context.Background(), k.req)
	if !rep.OK() || rep.Err() != nil {
		t.Fatalf("failed: %v", failedIDs(rep))
	}
	ids := []string{}
	for _, c := range rep.Checks {
		ids = append(ids, c.ID)
	}
	want := []string{CheckInputs, CheckDriver, CheckSource, CheckTools, CheckBackend, CheckDisk, CheckJobs}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("checks = %v, want %v", ids, want)
	}
	if rep.Commit != testSHA || rep.Backend != BackendCUDA || rep.CudaArch != "75" || rep.Jobs != 5 || rep.Source != "kit-tarball" {
		t.Errorf("report = %+v", rep)
	}
	if rep.Tools.CMake == "" || rep.Tools.CC == "" || rep.Tools.CXX == "" || rep.Tools.Ninja == "" || rep.Tools.NVCC == "" {
		t.Errorf("tools = %+v", rep.Tools)
	}
	if rep.InstallCommand != "" || len(rep.Packages) != 0 {
		t.Errorf("nothing is missing, yet %q %v", rep.InstallCommand, rep.Packages)
	}
	// It changes nothing.
	if k.exists(".build.lock") || k.exists("builds/"+testID+".tmp") || k.exists("src/"+testSHA) {
		t.Error("the preflight changed the node")
	}
}

func TestPreflightMatrix(t *testing.T) {
	type env func(k *kitEnv)
	withTarball := func(k *kitEnv) { k.kitTarball(testSHA, kitTree(t, testSHA), "src", "") }
	missing := func(names ...string) env {
		return func(k *kitEnv) {
			for _, n := range names {
				k.tools[n] = false
			}
		}
	}
	tests := []struct {
		name     string
		backend  string
		env      env
		want     []string // failed checks as id:code, in report order
		packages []string
		detail   string // a part of the first failure's detail
	}{
		{name: "cmake missing", backend: "cuda", env: missing("cmake"),
			want: []string{"tools:" + doctor.KIT010}, packages: []string{"cmake"}, detail: "cmake"},
		{name: "gcc and g++ share a package", backend: "cuda", env: missing("gcc", "g++", "ninja", "pkg-config"),
			want: []string{"tools:" + doctor.KIT010}, packages: []string{"build-essential", "ninja-build", "pkg-config"}},
		{name: "cmake too old", backend: "cuda", env: func(k *kitEnv) { k.cmakeOut = "cmake version 3.16.3\n" },
			want: []string{"tools:" + doctor.KIT010}, packages: []string{"cmake"}, detail: "older than 3.21"},
		{name: "cmake 3.21 is enough", backend: "cuda", env: func(k *kitEnv) { k.cmakeOut = "cmake version 3.21.0\n" }},
		{name: "cmake 4 is enough", backend: "cuda", env: func(k *kitEnv) { k.cmakeOut = "cmake version 4.0.1\n" }},
		{name: "nvcc missing and no redistributable", backend: "cuda", env: missing("nvcc"),
			want: []string{"backend:" + doctor.KIT011}, packages: []string{"nvidia-cuda-toolkit"}, detail: "no CUDA redistributable"},
		{name: "nvcc newer than the driver", backend: "cuda",
			env:  func(k *kitEnv) { k.driverOut = "CUDA Version: 11.8\n" },
			want: []string{"backend:" + doctor.KIT011}, detail: "newer than the CUDA 11.8"},
		{name: "nvcc newer by one minor", backend: "cuda",
			env:  func(k *kitEnv) { k.driverOut = "CUDA Version: 12.3\n" },
			want: []string{"backend:" + doctor.KIT011}},
		{name: "nvcc equal to the driver is fine", backend: "cuda",
			env: func(k *kitEnv) { k.driverOut = "CUDA Version: 12.4\n" }},
		{name: "driver not loaded", backend: "cuda",
			env:  func(k *kitEnv) { k.driverRC = 9 },
			want: []string{"driver:" + doctor.KIT013}, detail: "previous kernel"},
		{name: "disk low in /opt/localai", backend: "cuda", env: func(k *kitEnv) { k.freeOpt = 4 << 30 },
			want: []string{"disk:" + doctor.KIT014}, detail: "5 GB"},
		{name: "disk low in the scratch directory", backend: "cuda", env: func(k *kitEnv) { k.freeScr = 1 << 29 },
			want: []string{"disk:" + doctor.KIT014}, detail: k0Scratch},
		{name: "vulkan with a real device", backend: "vulkan"},
		{name: "vulkan with llvmpipe only", backend: "vulkan", env: func(k *kitEnv) { k.vulkanOut = vulkanSoft },
			want: []string{"backend:" + doctor.KIT012}, detail: "llvmpipe"},
		{name: "vulkan without glslc", backend: "vulkan", env: missing("glslc"),
			want: []string{"backend:" + doctor.KIT012}, packages: []string{"glslc"}},
		{name: "vulkan without vulkaninfo", backend: "vulkan", env: missing("vulkaninfo"),
			want: []string{"backend:" + doctor.KIT012}, packages: []string{"vulkan-tools"}},
		{name: "vulkan without the headers", backend: "vulkan", env: func(k *kitEnv) { k.pkgConfRC = 1 },
			want: []string{"backend:" + doctor.KIT012}, packages: []string{"libvulkan-dev"}},
		{name: "cpu needs no gpu", backend: "cpu", env: missing("nvcc", "glslc", "nvidia-smi", "vulkaninfo")},
		{name: "everything is wrong at once", backend: "cuda",
			env: func(k *kitEnv) {
				missing("cmake", "ninja", "nvcc")(k)
				k.driverRC = 9
				k.freeOpt = 1 << 30
				k.freeScr = 1 << 20
			},
			want:     []string{"driver:" + doctor.KIT013, "tools:" + doctor.KIT010, "backend:" + doctor.KIT011, "disk:" + doctor.KIT014},
			packages: []string{"cmake", "ninja-build", "nvidia-cuda-toolkit"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newKitEnv(t)
			withTarball(k)
			k.req.Backend = tc.backend
			if tc.env != nil {
				tc.env(k)
			}
			rep := k.manager().PreflightReport(context.Background(), k.req)
			got := failedIDs(rep)
			// The driver check runs first (the CUDA comparison needs its answer), then the order of
			// the issue's table.
			sortChecks := func(in []string) []string {
				order := map[string]int{"inputs": 0, "driver": 1, "source": 2, "tools": 3, "backend": 4, "disk": 5}
				out := append([]string(nil), in...)
				for i := range out {
					for j := i + 1; j < len(out); j++ {
						if order[strings.Split(out[j], ":")[0]] < order[strings.Split(out[i], ":")[0]] {
							out[i], out[j] = out[j], out[i]
						}
					}
				}
				return out
			}
			if !reflect.DeepEqual(got, sortChecks(tc.want)) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("failed checks = %v, want %v", got, sortChecks(tc.want))
			}
			if !reflect.DeepEqual(rep.Packages, tc.packages) && !(len(rep.Packages) == 0 && len(tc.packages) == 0) {
				t.Errorf("packages = %v, want %v", rep.Packages, tc.packages)
			}
			if len(tc.packages) > 0 {
				want := "sudo aictl kit install-debs --packages " + strings.Join(tc.packages, ",")
				if rep.InstallCommand != want {
					t.Errorf("install command = %q, want %q", rep.InstallCommand, want)
				}
			} else if rep.InstallCommand != "" {
				t.Errorf("install command = %q, want none", rep.InstallCommand)
			}
			if tc.detail != "" {
				found := false
				for _, c := range rep.Failed() {
					found = found || strings.Contains(c.Detail, tc.detail)
				}
				if !found {
					t.Errorf("no failed check mentions %q: %+v", tc.detail, rep.Failed())
				}
			}
			if len(tc.want) == 0 && !rep.OK() {
				t.Errorf("must pass: %+v", rep.Failed())
			}
			if (len(tc.want) == 0) != (rep.Err() == nil) {
				t.Errorf("Err() = %v for %v", rep.Err(), tc.want)
			}
		})
	}
}

// k0Scratch is a part of the failure text of the scratch directory check.
const k0Scratch = "1 GB"

func TestPreflightSourceChecks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, k *kitEnv)
		code  string
		src   string
	}{
		{"tree already extracted", func(t *testing.T, k *kitEnv) {
			k.write(filepath.Join(k.srcTree(), "CMakeLists.txt"), "x")
		}, "", "tree"},
		{"no source", func(*testing.T, *kitEnv) {}, doctor.KIT002, ""},
		{"damaged tarball", func(t *testing.T, k *kitEnv) {
			k.kitTarball(testSHA, kitTree(t, testSHA), "src", sum256([]byte("no")))
		}, doctor.KIT001, ""},
		{"mirror has the commit", func(t *testing.T, k *kitEnv) { k.kitMirror(&kitGit{have: true, sha: testSHA}) }, "", "kit-mirror"},
		{"mirror lacks the commit", func(t *testing.T, k *kitEnv) { k.kitMirror(&kitGit{}) }, doctor.KIT002, ""},
		{"mirror but no git", func(t *testing.T, k *kitEnv) {
			k.kitMirror(&kitGit{have: true, sha: testSHA})
			k.tools["git"] = false
		}, doctor.KIT010, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newKitEnv(t)
			tc.setup(t, k)
			rep := k.manager().PreflightReport(context.Background(), k.req)
			var src Check
			for _, c := range rep.Checks {
				if c.ID == CheckSource {
					src = c
				}
			}
			if tc.code == "" && (!src.OK || rep.Source != tc.src) {
				t.Fatalf("source check = %+v, source %q, want ok from %q", src, rep.Source, tc.src)
			}
			if tc.code != "" && (src.OK || src.Code != tc.code) {
				t.Fatalf("source check = %+v, want %s", src, tc.code)
			}
			if tc.name == "mirror but no git" && !reflect.DeepEqual(rep.Packages, []string{"git"}) {
				t.Errorf("packages = %v, want [git]", rep.Packages)
			}
			// cat-file, never archive: the preflight extracts nothing.
			for _, c := range k.exec.all() {
				if filepath.Base(c.Path) == "git" && strings.Contains(strings.Join(c.Args, " "), "archive") {
					t.Errorf("the preflight extracted: %s", line(c))
				}
			}
		})
	}
}

func TestPreflightChecksTheMirrorWithCatFile(t *testing.T) {
	k := newKitEnv(t)
	g := &kitGit{have: true, sha: testSHA}
	k.kitMirror(g)
	k.manager().PreflightReport(context.Background(), k.req)
	found := false
	for _, l := range g.calls {
		found = found || strings.HasSuffix(l, "cat-file -e "+testSHA+"^{commit}")
		if strings.Contains(l, "-c safe.directory=") == false {
			t.Errorf("a git call without safe.directory: %s", l)
		}
	}
	if !found {
		t.Errorf("git calls: %v; want `git cat-file -e <commit>^{commit}`", g.calls)
	}
}

type fakeApt struct {
	err   error
	asked [][]string
}

func (a *fakeApt) Simulate(_ context.Context, pkgs []string) (AptPlan, error) {
	a.asked = append(a.asked, pkgs)
	if a.err != nil {
		return AptPlan{}, a.err
	}
	return AptPlan{Install: append(append([]string{}, pkgs...), "libarchive13")}, nil
}

func TestPreflightSimulatesTheMissingPackagesOffline(t *testing.T) {
	tests := []struct {
		name   string
		apt    *fakeApt
		okPkgs bool
		detail string
	}{
		{"no simulator configured", nil, true, "not simulated"},
		{"apt can install them", &fakeApt{}, true, "would install 2 package(s)"},
		{"apt cannot", &fakeApt{err: errors.New("E: Unable to locate package cmake")}, false, "Unable to locate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newKitEnv(t)
			k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
			k.tools["cmake"] = false
			if tc.apt != nil {
				k.opts.Apt = tc.apt
			}
			rep := k.manager().PreflightReport(context.Background(), k.req)
			var c Check
			for _, x := range rep.Checks {
				if x.ID == CheckPackages {
					c = x
				}
			}
			if c.ID == "" || c.OK != tc.okPkgs || !strings.Contains(c.Detail, tc.detail) {
				t.Fatalf("packages check = %+v", c)
			}
			if !tc.okPkgs && c.Code != doctor.KIT010 {
				t.Errorf("code = %s", c.Code)
			}
			if tc.apt != nil && (len(tc.apt.asked) != 1 || !reflect.DeepEqual(tc.apt.asked[0], []string{"cmake"})) {
				t.Errorf("asked apt about %v", tc.apt.asked)
			}
			// The missing tool fails the preflight whatever the simulation says.
			if rep.OK() {
				t.Error("a missing tool must fail the preflight")
			}
			if !strings.Contains(c.Detail, "sudo aictl kit install-debs --packages cmake") && tc.okPkgs {
				t.Errorf("the exact command is not printed: %s", c.Detail)
			}
		})
	}
}

func TestPreflightWhenTheInputsCannotBeResolved(t *testing.T) {
	t.Run("no nvidia-smi for the architecture is also a driver that is not loaded", func(t *testing.T) {
		k := newKitEnv(t)
		k.tools["nvidia-smi"] = false
		k.opts.Node = nil
		k.req.Commit = testSHA
		rep := k.manager().PreflightReport(context.Background(), k.req)
		got := failedIDs(rep)
		want := []string{"inputs:" + doctor.HW019, "driver:" + doctor.KIT013}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("failed = %v, want %v", got, want)
		}
	})
	t.Run("bad commit", func(t *testing.T) {
		k := newKitEnv(t)
		k.req.Commit = "zz"
		rep := k.manager().PreflightReport(context.Background(), k.req)
		if got := failedIDs(rep); len(got) != 1 || got[0] != "inputs:"+doctor.LLM018 {
			t.Fatalf("failed = %v", got)
		}
	})
}

func TestReportErrCarriesEveryFailureAndTheFix(t *testing.T) {
	k := newKitEnv(t)
	k.tools["cmake"] = false
	k.freeOpt = 1 << 30
	rep := k.manager().PreflightReport(context.Background(), k.req)
	e := rep.Err()
	if e == nil || e.Code != doctor.KIT002 && e.Code != doctor.KIT010 {
		t.Fatalf("err = %v", e)
	}
	ids, _ := e.Details["failed_checks"].([]string)
	if len(ids) < 3 {
		t.Errorf("failed_checks = %v", ids)
	}
	if e.Details["install_command"] != "sudo aictl kit install-debs --packages cmake" || !strings.Contains(e.Message, "sudo aictl kit install-debs --packages cmake") {
		t.Errorf("the fix is missing: %v / %v", e.Details, e.Message)
	}
	if e.Stage != StagePreflight {
		t.Errorf("stage = %s", e.Stage)
	}
}

func TestParsersOfThePreflight(t *testing.T) {
	if got := ParseDriverCUDA(driverHeader); got != "12.8" {
		t.Errorf("ParseDriverCUDA = %q", got)
	}
	if ParseDriverCUDA("nothing") != "" {
		t.Error("no header, no version")
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"12.4.131", "12.8", true}, {"12.8", "12.4", false}, {"12.4", "12.4", true}, {"11.8", "12.0", true},
		{"13.0", "12.9", false}, {"x", "12.4", false}, {"12.4", "", false},
	} {
		if got := cudaVersionLE(tc.a, tc.b); got != tc.want {
			t.Errorf("cudaVersionLE(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
	if got := ParseVulkanDevices(vulkanReal + vulkanSoft); !reflect.DeepEqual(got, []string{"Test Graphics 9000", "llvmpipe (LLVM 17.0.6, 256 bits)"}) {
		t.Errorf("ParseVulkanDevices = %v", got)
	}
	if got := InstallDebsCommand([]string{"a", "b"}); got != "sudo aictl kit install-debs --packages a,b" {
		t.Error(got)
	}
}

// ---- --dry-run ---------------------------------------------------------------------------

func TestDryRunRunsOnlyThePreflight(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	k.req.DryRun = true
	res, err := k.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Preflight == nil || !res.Preflight.OK() || res.BuildID != "" {
		t.Fatalf("result = %+v", res)
	}
	for _, c := range k.exec.all() {
		if filepath.Base(c.Path) == "cmake" && (len(c.Args) == 0 || c.Args[0] != "--version") {
			t.Errorf("a dry run built: %s", line(c))
		}
	}
	if k.exists(".build.lock") || k.exists("builds/"+testID+".tmp") || k.exists("src/"+testSHA) {
		t.Error("a dry run changed the node")
	}
	if len(k.audit.events) != 0 || k.agent.restarts != 0 {
		t.Error("a dry run switched")
	}
}

func TestDryRunFailsOnAnyFailedCheck(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	k.tools["ninja"] = false
	k.req.DryRun = true
	res, err := k.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT010 || res.Preflight == nil || res.Preflight.OK() || !res.DryRun {
		t.Fatalf("err %v, result %+v", e, res)
	}
	if e.Details["install_command"] != "sudo aictl kit install-debs --packages ninja-build" {
		t.Errorf("details = %v", e.Details)
	}
	if k.exists(".build.lock") || k.exists("builds/"+testID+".tmp") {
		t.Error("a dry run changed the node")
	}
}

func TestDryRunWorksOnlineToo(t *testing.T) {
	k := newKitEnv(t)
	k.req.FromKit, k.req.Offline, k.req.DryRun = false, false, true
	k.opts.Sources = ChainWithKit("llama.cpp", []string{k.root}, "https://example.test/llama.git")
	res, err := k.build()
	if err != nil || res.Preflight == nil || res.Preflight.Source != "github" {
		t.Fatalf("%v %+v", err, res.Preflight)
	}
}

func TestFromKitMissingToolIsKit010WithTheKitCommand(t *testing.T) {
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	k.tools["cmake"] = false
	_, err := k.build()
	e := mustErr(t, err)
	if e.Code != doctor.KIT010 || e.Stage != StagePreflight || !strings.Contains(e.Message, "sudo aictl kit install-debs --packages cmake") {
		t.Fatalf("got %v", e)
	}
	if strings.Contains(e.Message, "apt install") {
		t.Errorf("offline, the fix is the kit, not apt: %s", e.Message)
	}
	if k.exists("src/"+testSHA) || k.exists("builds/"+testID+".tmp") || k.exists(".build.lock") {
		t.Error("nothing may be extracted or created when the preflight fails")
	}
}

func TestWithoutFromKitAMissingToolStaysLAILLM018(t *testing.T) {
	h := newHarness(t)
	h.tools["cmake"] = false
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 {
		t.Fatalf("got %v", e)
	}
}

// ---- jobs --------------------------------------------------------------------------------

func TestJobsAreCappedByMemoryAndProcessors(t *testing.T) {
	tests := []struct {
		name    string
		jobs    int
		mem     int64
		memErr  error
		cpus    int
		want    int
		comment string
	}{
		{"asked for fewer than allowed", 2, 10240, nil, 8, 2, ""},
		{"asked for more than the memory feeds", 16, 10240, nil, 8, 5, "floor(10 GiB / 2)"},
		{"asked for more than the processors", 16, 65536, nil, 4, 4, ""},
		{"no request", 0, 10240, nil, 8, 5, ""},
		{"low memory still gets one", 8, 1000, nil, 8, 1, ""},
		{"memory unknown, a request is capped by the processors", 16, 0, errors.New("x"), 4, 4, ""},
		{"memory unknown, no request", 0, 0, errors.New("x"), 4, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.opts.MemAvailableMiB = func() (int64, error) { return tc.mem, tc.memErr }
			h.opts.NumCPU = func() int { return tc.cpus }
			in, err := h.manager().Resolve(context.Background(), Request{Backend: BackendCPU, Jobs: tc.jobs})
			if err != nil || in.Jobs != tc.want {
				t.Fatalf("jobs = %d, %v; want %d", in.Jobs, err, tc.want)
			}
		})
	}
}

// ---- the retired code --------------------------------------------------------------------

func TestNothingEmitsTheRetiredKit022(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if strings.Contains(readFile(t, f), "KIT022") {
			t.Errorf("%s uses LAI-KIT-022, which is retired (LAI-LLM-019 replaced it)", f)
		}
	}
}

// ---- recording what changed --------------------------------------------------------------

type fakeNodeConfig struct {
	commits []string
	err     error
}

func (n *fakeNodeConfig) RecordLlamaCommit(c string) error {
	n.commits = append(n.commits, c)
	return n.err
}

type fakeExporter struct {
	built []string
	err   error
}

func (e *fakeExporter) ExportBuild(_ context.Context, id, dir string) error {
	e.built = append(e.built, id+"@"+filepath.Base(dir))
	return e.err
}

func TestActivatedBuildIsRecordedAndExported(t *testing.T) {
	tests := []struct {
		name        string
		nodeCommit  string
		noActivate  bool
		recordErr   error
		exportErr   error
		wantCommits []string
		wantExport  bool
		wantWarn    string
	}{
		{"a new commit is recorded and the build exported", "0a1b2c3d4" + strings.Repeat("0", 31), false, nil, nil, []string{testSHA}, true, ""},
		{"the same commit is not rewritten", testSHA, false, nil, nil, nil, true, ""},
		{"a short node commit that names it is not rewritten", testSHA[:9], false, nil, nil, nil, true, ""},
		{"nothing is recorded without a switch", "0a1b2c3d4" + strings.Repeat("0", 31), true, nil, nil, nil, false, ""},
		{"a failed record is a warning", "0a1b2c3d4" + strings.Repeat("0", 31), false, errors.New("disk full"), nil, []string{testSHA}, true, "node.yaml could not be updated"},
		{"a failed export is a warning", testSHA, false, nil, errors.New("read-only drive"), nil, true, "could not be exported"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			nc := &fakeNodeConfig{err: tc.recordErr}
			ex := &fakeExporter{err: tc.exportErr}
			h.opts.Node = func() (b config.LlamaBlock) { b.Commit, b.Backend = tc.nodeCommit, BackendCUDA; return }
			h.opts.NodeConfig, h.opts.Exporter = nc, ex
			h.req.NoActivate = tc.noActivate
			h.req.Commit = testSHA
			if tc.noActivate {
				h.prevID = ""
			}
			res, err := h.build()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(nc.commits, tc.wantCommits) {
				t.Errorf("recorded commits = %v, want %v", nc.commits, tc.wantCommits)
			}
			if got := len(ex.built) == 1 && ex.built[0] == testID+"@"+testID; got != tc.wantExport {
				t.Errorf("exports = %v, want export %v", ex.built, tc.wantExport)
			}
			warned := strings.Contains(strings.Join(res.Warnings, "\n"), tc.wantWarn)
			if tc.wantWarn != "" && !warned {
				t.Errorf("warnings = %v, want one with %q", res.Warnings, tc.wantWarn)
			}
		})
	}
}

func TestARolledBackBuildIsNotRecordedOrExported(t *testing.T) {
	h := newHarness(t)
	nc, ex := &fakeNodeConfig{}, &fakeExporter{}
	h.opts.Node = func() (b config.LlamaBlock) {
		b.Commit, b.Backend = "0a1b2c3d4"+strings.Repeat("0", 31), BackendCUDA
		return
	}
	h.opts.NodeConfig, h.opts.Exporter = nc, ex
	h.probe.completeErr = errors.New("500")
	h.req.Commit = testSHA
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM019 {
		t.Fatalf("got %v", e)
	}
	if len(nc.commits) != 0 || len(ex.built) != 0 {
		t.Errorf("recorded %v, exported %v after a rollback", nc.commits, ex.built)
	}
	if h.link() != "builds/"+oldID {
		t.Errorf("current = %q", h.link())
	}
}

// keep execx referenced for the shared helpers of this file.
var _ = execx.ErrNotFound
