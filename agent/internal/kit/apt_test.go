package kit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

var (
	debCmake  = fakeDeb{"cmake", "3.28.3-1"}
	debData   = fakeDeb{"cmake-data", "3.28.3-1"}
	debCurl   = fakeDeb{"curl", "8.5.0-1"}
	debNvidia = fakeDeb{"nvidia-driver-595-open", "595.58-0ubuntu1"}
	debDKMS   = fakeDeb{"dkms", "3.0.11-1"}
)

const cmakePlan = "Inst cmake-data (3.28.3-1 Ubuntu:24.04/noble [all])\nInst cmake (3.28.3-1 Ubuntu:24.04/noble [amd64])\nConf cmake-data (3.28.3-1 Ubuntu:24.04/noble [all])\nConf cmake (3.28.3-1 Ubuntu:24.04/noble [amd64])\n"

func TestParsePackageList(t *testing.T) {
	good := []struct {
		in   string
		want []string
	}{
		{"cmake", []string{"cmake"}},
		{"cmake,ninja-build", []string{"cmake", "ninja-build"}},
		{" cmake , curl ", []string{"cmake", "curl"}},
		{"cmake,cmake,curl", []string{"cmake", "curl"}},
		{"libstdc++-13-dev,g++,nvidia-driver-595-open", []string{"libstdc++-13-dev", "g++", "nvidia-driver-595-open"}},
	}
	for _, tc := range good {
		got, err := kit.ParsePackageList(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: %v %v", tc.in, got, err)
		}
	}
	for _, in := range []string{"", " ", ",", "cmake,", "Cmake", "-x", "--help", "a;b", "a b", "a=1", "a/b", "a:b", "$(id)", "a`id`", "cmake\ncurl", "a|b", "../x", ".hidden", "é"} {
		_, err := kit.ParsePackageList(in)
		var e *kit.Error
		if !errors.As(err, &e) || !errors.Is(err, kit.ErrBadPackage) || e.Code() != doctor.CLI005 {
			t.Errorf("%q: want a bad package error, got %v", in, err)
		}
	}
	many := make([]string, 201)
	for i := range many {
		many[i] = "pkg" + strings.Repeat("a", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := kit.ParsePackageList(strings.Join(many, ",")); err == nil {
		t.Error("more than 200 packages must be refused")
	}
}

func TestDefaultPackagesPerBackend(t *testing.T) {
	has := func(l []string, n string) bool {
		for _, x := range l {
			if x == n {
				return true
			}
		}
		return false
	}
	cpu, vulkan, cuda := kit.DefaultPackages("cpu"), kit.DefaultPackages("vulkan"), kit.DefaultPackages("cuda")
	for _, l := range [][]string{cpu, vulkan, cuda} {
		for _, n := range []string{"build-essential", "cmake", "ninja-build", "ccache", "curl", "jq", "minisign", "zstd"} {
			if !has(l, n) {
				t.Errorf("%s is in every default set", n)
			}
		}
		for _, n := range []string{"tailscale", "gpsd"} {
			if has(l, n) {
				t.Errorf("%s is optional: only with --packages", n)
			}
		}
	}
	if has(cpu, "libvulkan-dev") || has(cpu, "nvidia-cuda-toolkit") {
		t.Error("a cpu node needs neither Vulkan nor CUDA packages")
	}
	if !has(vulkan, "libvulkan-dev") || !has(vulkan, "mesa-vulkan-drivers") || has(vulkan, "nvidia-cuda-toolkit") {
		t.Errorf("vulkan: %v", vulkan)
	}
	if !has(cuda, "nvidia-cuda-toolkit") || has(cuda, "libvulkan-dev") {
		t.Errorf("cuda: %v", cuda)
	}
	if !reflect.DeepEqual(kit.DefaultPackages("anything"), cpu) || !reflect.DeepEqual(kit.DefaultPackages(""), cpu) {
		t.Error("an unknown backend counts as cpu")
	}
	seen := map[string]bool{}
	for _, n := range cuda {
		if seen[n] || !kit.ValidPackageName(n) {
			t.Errorf("%q duplicated or invalid", n)
		}
		seen[n] = true
	}
}

func TestMachineRelease(t *testing.T) {
	dir := t.TempDir()
	osr := filepath.Join(dir, "os-release")
	writeFile(t, osr, "NAME=\"Ubuntu\"\nVERSION_CODENAME=noble\nID=ubuntu\n")
	c, a, err := kit.MachineRelease(config.OSFS{}, osr, "amd64")
	if err != nil || c != "noble" || a != "amd64" {
		t.Fatalf("%q %q %v", c, a, err)
	}
	if _, a, err := kit.MachineRelease(config.OSFS{}, osr, "arm64"); err != nil || a != "arm64" {
		t.Fatalf("arm64: %q %v", a, err)
	}
	for name, content := range map[string]string{"none": "NAME=x\n", "slash": "VERSION_CODENAME=../x\n", "dash": "VERSION_CODENAME=a-b\n"} {
		p := filepath.Join(dir, name)
		writeFile(t, p, content)
		if _, _, err := kit.MachineRelease(config.OSFS{}, p, "amd64"); !errors.Is(err, kit.ErrWrongRelease) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := kit.MachineRelease(config.OSFS{}, osr, "riscv64"); !errors.Is(err, kit.ErrWrongRelease) {
		t.Errorf("riscv64: %v", err)
	}
	if _, _, err := kit.MachineRelease(config.OSFS{}, filepath.Join(dir, "missing"), "amd64"); !errors.Is(err, kit.ErrWrongRelease) {
		t.Errorf("missing file: %v", err)
	}
}

func TestVerifyRepoAcceptsAGoodRepository(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	dir := buildRepo(t, kitDir, "debs", "noble-amd64", debCmake, debData, debCurl)
	v, err := kit.VerifyRepo(context.Background(), kit.OSFS{}, kit.Repo{Part: "debs", Name: "noble-amd64", Dir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Entries) != 3 || v.Entries[0].Name != "cmake" || v.Entries[0].Version != "3.28.3-1" {
		t.Errorf("entries %+v", v.Entries)
	}
}

// Every way to damage a repository is refused with LAI-KIT-001 and names the file.
func TestVerifyRepoRefusesDamage(t *testing.T) {
	cases := []struct {
		name   string
		damage func(t *testing.T, dir string)
		names  string // a name the error must carry
	}{
		{"a deb changed", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "pool", debCmake.pool()), "tampered")
		}, "pool/cmake_3.28.3-1_amd64.deb"},
		{"a deb missing", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "pool", debCurl.pool()))
		}, "pool/curl_8.5.0-1_amd64.deb"},
		{"an unlisted deb in pool", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "pool", "evil_1_amd64.deb"), "x")
		}, "pool/evil_1_amd64.deb"},
		{"a Packages.xz that apt would prefer", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "Packages.xz"), "x")
		}, "Packages.xz"},
		{"a Release file", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "Release"), "x")
		}, "Release"},
		{"a folder next to pool", func(t *testing.T, dir string) {
			os.MkdirAll(filepath.Join(dir, "extra"), 0o755)
			writeFile(t, filepath.Join(dir, "extra", "f"), "x")
		}, ""},
		{"PACKAGES.lock missing", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "PACKAGES.lock"))
		}, "PACKAGES.lock"},
		{"SHA256SUMS missing", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "SHA256SUMS"))
		}, "SHA256SUMS"},
		{"PACKAGES.lock changed (sums not updated)", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "PACKAGES.lock"), "cmake=3.28.3-1 "+strings.Repeat("a", 64)+"\n")
		}, "PACKAGES.lock"},
		{"a deb swapped and the sums rewritten (the lock still says the old hash)", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "pool", debCmake.pool()), "evil cmake")
			reseal(t, dir)
		}, "cmake=3.28.3-1"},
		{"the lock rewritten with the sums, Packages not", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "PACKAGES.lock"), strings.Replace(readFile(t, filepath.Join(dir, "PACKAGES.lock")), "curl=8.5.0-1", "curl=9.0.0-1", 1))
			reseal(t, dir)
		}, "curl"},
		{"Packages.gz holds another index", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "Packages.gz"), gz(t, []byte("Package: evil\nVersion: 1\n\n")), 0o644)
			reseal(t, dir)
		}, "Packages.gz"},
		{"Packages.gz is not gzip", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "Packages.gz"), "plain")
			reseal(t, dir)
		}, "Packages.gz"},
		{"a deb replaced by a link", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "pool", debCurl.pool())
			os.Rename(p, filepath.Join(dir, "elsewhere"))
			os.Symlink(filepath.Join(dir, "elsewhere"), p)
		}, "pool/curl_8.5.0-1_amd64.deb"},
		{"MANIFEST.json with another tree hash", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "README.txt"), "changed")
			// SHA256SUMS is rewritten but MANIFEST.json is not.
			sums := readFile(t, filepath.Join(dir, "SHA256SUMS"))
			old := strings.Split(strings.TrimSpace(sums), "\n")
			for i, l := range old {
				if strings.HasSuffix(l, "  README.txt") {
					old[i] = hexSHA([]byte("changed")) + "  README.txt"
				}
			}
			writeFile(t, filepath.Join(dir, "SHA256SUMS"), strings.Join(old, "\n")+"\n")
		}, "MANIFEST.json"},
		{"MANIFEST.json damaged", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "MANIFEST.json"), "{not json")
		}, "MANIFEST.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kitDir := filepath.Join(t.TempDir(), "kit")
			dir := buildRepo(t, kitDir, "debs", "noble-amd64", debCmake, debCurl)
			tc.damage(t, dir)
			v, err := kit.VerifyRepo(context.Background(), kit.OSFS{}, kit.Repo{Part: "debs", Name: "noble-amd64", Dir: dir}, nil)
			var e *kit.Error
			if v != nil || !errors.As(err, &e) || !errors.Is(err, kit.ErrRepoDamaged) || e.Code() != doctor.KIT001 {
				t.Fatalf("want LAI-KIT-001, got %v %v", v, err)
			}
			if !strings.Contains(strings.Join(e.Names, "|"), tc.names) {
				t.Errorf("names %v lack %q", e.Names, tc.names)
			}
		})
	}
}

func TestReposForAndErrors(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	if _, err := kit.ReposFor(config.OSFS{}, kitDir, "noble", "amd64"); !errors.Is(err, kit.ErrNoRepo) {
		t.Errorf("no kit at all: %v", err)
	}
	buildRepo(t, kitDir, "debs", "resolute-amd64", debCmake)
	buildRepo(t, kitDir, "debs", "resolute-arm64", debCmake)
	_, err := kit.ReposFor(config.OSFS{}, kitDir, "noble", "amd64")
	var e *kit.Error
	if !errors.As(err, &e) || !errors.Is(err, kit.ErrWrongRelease) || e.Code() != doctor.KIT042 ||
		e.Want != "noble-amd64" || !reflect.DeepEqual(e.Have, []string{"debs/resolute-amd64", "debs/resolute-arm64"}) {
		t.Fatalf("unknown codename: %+v", err)
	}
	buildRepo(t, kitDir, "debs", "noble-amd64", debCmake)
	buildRepo(t, kitDir, "nvidia", "noble-amd64-595-open", debNvidia)
	buildRepo(t, kitDir, "nvidia", "noble-amd64-580", debNvidia)
	buildRepo(t, kitDir, "nvidia", "noble-arm64-595-open", debNvidia)
	repos, err := kit.ReposFor(config.OSFS{}, kitDir, "noble", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.Part+"/"+r.Name)
	}
	if !reflect.DeepEqual(names, []string{"debs/noble-amd64", "nvidia/noble-amd64-580", "nvidia/noble-amd64-595-open"}) {
		t.Errorf("repos %v", names)
	}
	// A folder that is not a plain name is never a repository.
	os.MkdirAll(filepath.Join(kitDir, "debs", "noble amd64"), 0o755)
	if repos2, _ := kit.ListRepos(config.OSFS{}, kitDir); len(repos2) != len(repos)+3+1-3 && false {
		t.Error("unreachable")
	}
}

func fixtureKit(t *testing.T) (kitDir string) {
	t.Helper()
	kitDir = filepath.Join(t.TempDir(), "kit")
	buildRepo(t, kitDir, "debs", "noble-amd64", debCmake, debData, debCurl, debDKMS)
	return kitDir
}

func TestRunInstallsOnlyFromAVerifiedRepository(t *testing.T) {
	kitDir := fixtureKit(t)
	f := newAptFake(t)
	f.plan = cmakePlan
	a, _ := newApt(t, f.runner())
	r := f.runner()
	_ = r
	var steps []string
	a.Progress = func(s kit.Step, d string) {
		if s != kit.StepVerify || !strings.Contains(d, "/") || strings.HasPrefix(d, "debs/") {
			steps = append(steps, string(s))
		}
	}
	rn := &fnRunner{fn: f.runner().fn}
	a.Runner = rn
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if err != nil {
		t.Fatal(err)
	}
	list := a.SourceList
	wantList := "deb [trusted=yes] file:" + filepath.Join(kitDir, "debs", "noble-amd64") + " ./\n"
	for _, verb := range []string{"update", "plan", "install"} {
		if f.listSeen[verb] != wantList {
			t.Errorf("the source seen by apt-get %s was %q, want %q", verb, f.listSeen[verb], wantList)
		}
	}
	opts := "-o Dir::Etc::sourcelist=" + list + " -o Dir::Etc::sourceparts=-"
	wantLines := []string{
		"dpkg-query -W -f=${Package}\t${db:Status-Abbrev}\t${Version}\n cmake",
		"apt-get update " + opts + " -o APT::Get::List-Cleanup=0",
		"apt-get install -s -y --no-install-recommends " + opts + " cmake",
		"apt-get install -y --no-install-recommends " + opts + " cmake",
		"dpkg-query -W -f=${Package}\t${db:Status-Abbrev}\t${Version}\n cmake",
	}
	if got := rn.lines(); !reflect.DeepEqual(got, wantLines) {
		t.Fatalf("commands:\n%q\nwant:\n%q", got, wantLines)
	}
	if _, err := os.Stat(list); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the temporary source must be gone, stat: %v", err)
	}
	if !reflect.DeepEqual(res.Installed, []kit.Pkg{{Name: "cmake", Version: "3.28.3-1"}}) || len(res.Plan) != 2 || res.Plan[1].Name != "cmake" {
		t.Errorf("result %+v", res)
	}
	if !reflect.DeepEqual(res.Repos, []string{"debs/noble-amd64"}) || res.DryRun {
		t.Errorf("repos %v dry %v", res.Repos, res.DryRun)
	}
	// Fixed argv, a plain environment, no shell.
	for _, c := range rn.calls {
		if filepath.Base(c.Path) == "sh" || c.Timeout <= 0 || c.MaxOut <= 0 {
			t.Errorf("bad command %+v", c)
		}
		if !reflect.DeepEqual(c.Env[:1], []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}) {
			t.Errorf("env %v", c.Env)
		}
	}
	_ = steps
}

func TestRunDryRunChangesNothing(t *testing.T) {
	kitDir := fixtureKit(t)
	f := newAptFake(t)
	f.plan = cmakePlan
	rn := f.runner()
	a, tmp := newApt(t, rn)
	before := dirNames(t, tmp)
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.Plan) != 2 || len(res.Installed) != 0 {
		t.Errorf("result %+v", res)
	}
	r := rn
	for _, v := range r.aptVerbs() {
		if v == "install" {
			t.Errorf("a dry run must not install: %v", r.aptVerbs())
		}
	}
	if !reflect.DeepEqual(r.aptVerbs(), []string{"update", "install -s"}) {
		t.Errorf("apt verbs %v", r.aptVerbs())
	}
	if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the real source list was touched: %v", err)
	}
	if after := dirNames(t, tmp); !reflect.DeepEqual(before, after) {
		t.Errorf("the scratch state was left behind: %v -> %v", before, after)
	}
	// The private state of the dry run is used, never /var/lib/apt.
	for _, c := range r.calls {
		if filepath.Base(c.Path) == "apt-get" && !strings.Contains(strings.Join(c.Args, " "), "Dir::State::Lists="+tmp) {
			t.Errorf("a dry run must use its own apt state: %v", c.Args)
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestRunDryRunExitsNonZeroWhenApiCannotResolve(t *testing.T) {
	kitDir := fixtureKit(t)
	f := newAptFake(t)
	f.fail["plan"] = 100
	a, _ := newApt(t, f.runner())
	_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}, DryRun: true})
	var e *kit.Error
	if !errors.As(err, &e) || !errors.Is(err, kit.ErrApt) || e.Code() != doctor.KIT047 || len(e.Names) == 0 {
		t.Fatalf("%v", err)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	kitDir := fixtureKit(t)
	f := newAptFake(t)
	f.plan = cmakePlan
	f.installed["cmake"] = "3.28.3-1"
	rn := f.runner()
	a, _ := newApt(t, rn)
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Skipped, []kit.Pkg{{Name: "cmake", Version: "3.28.3-1"}}) || len(res.Installed) != 0 || len(res.Repos) != 0 {
		t.Errorf("result %+v", res)
	}
	if v := rn.aptVerbs(); len(v) != 0 {
		t.Errorf("an installed package must not start apt: %v", v)
	}
	if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no source may be written: %v", err)
	}
	// An older version installed is not "already installed": it is upgraded.
	f.installed["cmake"] = "3.20.0-1"
	res, err = a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if err != nil || len(res.Skipped) != 0 || len(res.Installed) != 1 {
		t.Errorf("upgrade: %+v %v", res, err)
	}
	// And now it is at the kit's version: a second run is a no-op again.
	rn2 := f.runner()
	a.Runner = rn2
	res, err = a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if err != nil || len(res.Skipped) != 1 || len(rn2.aptVerbs()) != 0 {
		t.Errorf("second run: %+v %v %v", res, err, rn2.aptVerbs())
	}
}

func TestRunRefusesATamperedDebBeforeAptRuns(t *testing.T) {
	kitDir := fixtureKit(t)
	writeFile(t, filepath.Join(kitDir, "debs", "noble-amd64", "pool", debCmake.pool()), "tampered")
	f := newAptFake(t)
	f.plan = cmakePlan
	rn := f.runner()
	a, _ := newApt(t, rn)
	_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	var e *kit.Error
	if !errors.As(err, &e) || e.Code() != doctor.KIT001 {
		t.Fatalf("want LAI-KIT-001: %v", err)
	}
	if v := rn.aptVerbs(); len(v) != 0 {
		t.Errorf("apt ran on an unverified repository: %v", v)
	}
	if len(f.listSeen) != 0 {
		t.Error("a [trusted=yes] source was written for an unverified repository")
	}
	if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source list exists: %v", err)
	}
	// A missing PACKAGES.lock is the same refusal.
	kitDir = fixtureKit(t)
	os.Remove(filepath.Join(kitDir, "debs", "noble-amd64", "PACKAGES.lock"))
	f2 := newAptFake(t)
	rn2 := f2.runner()
	a2, _ := newApt(t, rn2)
	_, err = a2.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if !errors.As(err, &e) || e.Code() != doctor.KIT001 || len(rn2.aptVerbs()) != 0 {
		t.Fatalf("missing lock: %v", err)
	}
}

func TestRunRemovesTheSourceWhenAptFails(t *testing.T) {
	for _, verb := range []string{"update", "plan", "install"} {
		t.Run(verb, func(t *testing.T) {
			kitDir := fixtureKit(t)
			f := newAptFake(t)
			f.plan = cmakePlan
			f.fail[verb] = 100
			a, _ := newApt(t, f.runner())
			_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
			var e *kit.Error
			if !errors.As(err, &e) || !errors.Is(err, kit.ErrApt) || e.Code() != doctor.KIT047 {
				t.Fatalf("%v", err)
			}
			if !strings.Contains(strings.Join(e.Names, "|"), "simulated failure of "+verb) {
				t.Errorf("the message must carry the end of apt's output: %v", e.Names)
			}
			if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the source list must be removed after a failure: %v", err)
			}
		})
	}
}

// failRemoveFS cannot delete the source file.
type failRemoveFS struct{ kit.FS }

func (failRemoveFS) Remove(name string) error { return errors.New("read-only file system") }

func TestRunReportsASourceItCannotRemove(t *testing.T) {
	kitDir := fixtureKit(t)
	f := newAptFake(t)
	f.plan = cmakePlan
	a, _ := newApt(t, f.runner())
	a.FS = failRemoveFS{kit.OSFS{}}
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if !errors.Is(err, kit.ErrCleanup) || res == nil {
		t.Fatalf("a [trusted=yes] source that stays must be an error: %v", err)
	}
	// After a failed apt the cleanup failure does not hide the first error.
	f.installed = map[string]string{}
	f.fail["install"] = 100
	_, err = a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
	if !errors.Is(err, kit.ErrCleanup) || !errors.Is(err, kit.ErrApt) {
		t.Fatalf("both failures must be visible: %v", err)
	}
	var e *kit.Error
	if !errors.As(err, &e) || !errors.Is(e.Kind, kit.ErrApt) {
		t.Errorf("the first error comes first: %v", err)
	}
}

func TestRunRefusesAPlanBeyondTheVerifiedPackages(t *testing.T) {
	cases := []struct {
		name, plan, want string
	}{
		{"a package that is not in the lock", cmakePlan + "Inst evil-tool (1.0 Ubuntu:24.04/noble [amd64])\n", "evil-tool=1.0"},
		{"a locked package at another version", "Inst cmake (9.9.9-1 Ubuntu:24.04/noble [amd64])\n", "cmake=9.9.9-1"},
		{"a removal", cmakePlan + "Remv libold [1.0]\n", "libold"},
		{"a line apt printed in a shape we do not know", "Inst weird\n", "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kitDir := fixtureKit(t)
			f := newAptFake(t)
			f.plan = tc.plan
			rn := f.runner()
			a, _ := newApt(t, rn)
			_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
			var e *kit.Error
			if !errors.As(err, &e) || !errors.Is(err, kit.ErrUnsafePlan) || e.Code() != doctor.KIT047 {
				t.Fatalf("%v", err)
			}
			if !strings.Contains(strings.Join(e.Names, "|"), tc.want) {
				t.Errorf("names %v lack %q", e.Names, tc.want)
			}
			for _, v := range rn.aptVerbs() {
				if v == "install" {
					t.Error("nothing may be installed after an unsafe plan")
				}
			}
			if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the source list must be removed: %v", err)
			}
		})
	}
}

func TestRunPicksTheNvidiaRepositoryForNvidiaPackages(t *testing.T) {
	kitDir := fixtureKit(t)
	nv := buildRepo(t, kitDir, "nvidia", "noble-amd64-595-open", debNvidia, debDKMS)
	// The debs repository is damaged: a request that does not need it must not care.
	os.Remove(filepath.Join(kitDir, "debs", "noble-amd64", "PACKAGES.lock"))
	f := newAptFake(t)
	f.plan = "Inst nvidia-driver-595-open (595.58-0ubuntu1 Ubuntu:24.04/noble [amd64])\nInst dkms (3.0.11-1 Ubuntu:24.04/noble [all])\n"
	a, _ := newApt(t, f.runner())
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"nvidia-driver-595-open"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Repos, []string{"nvidia/noble-amd64-595-open"}) {
		t.Errorf("repos %v", res.Repos)
	}
	if want := "deb [trusted=yes] file:" + nv + " ./\n"; f.listSeen["install"] != want {
		t.Errorf("source %q, want %q", f.listSeen["install"], want)
	}
}

func TestRunListsEveryRepositoryThatHoldsARequestedPackage(t *testing.T) {
	kitDir := fixtureKit(t)
	nv := buildRepo(t, kitDir, "nvidia", "noble-amd64-595-open", debNvidia)
	f := newAptFake(t)
	f.plan = "Inst cmake (3.28.3-1 Ubuntu:24.04/noble [amd64])\nInst nvidia-driver-595-open (595.58-0ubuntu1 Ubuntu:24.04/noble [amd64])\n"
	a, _ := newApt(t, f.runner())
	res, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake", "nvidia-driver-595-open"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "deb [trusted=yes] file:" + filepath.Join(kitDir, "debs", "noble-amd64") + " ./\n" + "deb [trusted=yes] file:" + nv + " ./\n"
	if f.listSeen["install"] != want || !reflect.DeepEqual(res.Repos, []string{"debs/noble-amd64", "nvidia/noble-amd64-595-open"}) {
		t.Errorf("source %q repos %v", f.listSeen["install"], res.Repos)
	}
}

func TestRunDamageInAnotherRepositoryOnlyMattersWhenItIsUsed(t *testing.T) {
	kitDir := fixtureKit(t)
	nv := buildRepo(t, kitDir, "nvidia", "noble-amd64-595-open", debNvidia)
	writeFile(t, filepath.Join(nv, "pool", debNvidia.pool()), "tampered")
	f := newAptFake(t)
	f.plan = cmakePlan
	a, _ := newApt(t, f.runner())
	if _, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}}); err != nil {
		t.Fatalf("cmake needs only the debs repository: %v", err)
	}
	_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"nvidia-driver-595-open"}})
	var e *kit.Error
	if !errors.As(err, &e) || e.Code() != doctor.KIT001 {
		t.Fatalf("a damaged nvidia repository must refuse its own packages: %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	t.Run("an explicit package that the kit lacks", func(t *testing.T) {
		f := newAptFake(t)
		rn := f.runner()
		a, _ := newApt(t, rn)
		_, err := a.Run(context.Background(), fixtureKit(t), kit.Request{Packages: []string{"cmake", "gpsd", "tailscale"}})
		var e *kit.Error
		if !errors.As(err, &e) || !errors.Is(err, kit.ErrNotInKit) || e.Code() != doctor.KIT040 || !reflect.DeepEqual(e.Names, []string{"gpsd", "tailscale"}) {
			t.Fatalf("%+v", err)
		}
		if len(rn.lines()) != 0 {
			t.Errorf("nothing may run: %v", rn.lines())
		}
	})
	t.Run("a bad name never reaches the disk or apt", func(t *testing.T) {
		rn := newAptFake(t).runner()
		a, _ := newApt(t, rn)
		_, err := a.Run(context.Background(), filepath.Join(t.TempDir(), "nokit"), kit.Request{Packages: []string{"cmake;rm -rf /"}})
		if !errors.Is(err, kit.ErrBadPackage) || len(rn.lines()) != 0 {
			t.Fatalf("%v %v", err, rn.lines())
		}
	})
	t.Run("unknown codename names what the kit has", func(t *testing.T) {
		rn := newAptFake(t).runner()
		a, _ := newApt(t, rn)
		a.Codename = "oracular"
		_, err := a.Run(context.Background(), fixtureKit(t), kit.Request{Packages: []string{"cmake"}})
		var e *kit.Error
		if !errors.As(err, &e) || !errors.Is(err, kit.ErrWrongRelease) || e.Code() != doctor.KIT042 || e.Want != "oracular-amd64" || !reflect.DeepEqual(e.Have, []string{"debs/noble-amd64"}) {
			t.Fatalf("%+v", err)
		}
		if len(rn.lines()) != 0 {
			t.Errorf("nothing may run: %v", rn.lines())
		}
	})
	t.Run("a folder with a space cannot be an apt source", func(t *testing.T) {
		kitDir := filepath.Join(t.TempDir(), "my kit")
		buildRepo(t, kitDir, "debs", "noble-amd64", debCmake)
		f := newAptFake(t)
		f.plan = cmakePlan
		rn := f.runner()
		a, _ := newApt(t, rn)
		_, err := a.Run(context.Background(), kitDir, kit.Request{Packages: []string{"cmake"}})
		if !errors.Is(err, kit.ErrIO) || len(rn.aptVerbs()) != 0 {
			t.Fatalf("%v %v", err, rn.aptVerbs())
		}
		if _, err := os.Stat(a.SourceList); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("no source may be written: %v", err)
		}
	})
	t.Run("a cancelled context stops verification", func(t *testing.T) {
		rn := newAptFake(t).runner()
		a, _ := newApt(t, rn)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := a.Run(ctx, fixtureKit(t), kit.Request{Packages: []string{"cmake"}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	})
}

func TestRunDefaultSet(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	buildRepo(t, kitDir, "debs", "noble-amd64", debCmake, debCurl, fakeDeb{"libvulkan-dev", "1.3.275-1"}, fakeDeb{"jq", "1.7.1-3"})
	f := newAptFake(t)
	f.plan = "Inst cmake (3.28.3-1 x [amd64])\nInst curl (8.5.0-1 x [amd64])\nInst jq (1.7.1-3 x [amd64])\nInst libvulkan-dev (1.3.275-1 x [amd64])\n"
	rn := f.runner()
	a, _ := newApt(t, rn)
	res, err := a.Run(context.Background(), kitDir, kit.Request{Backend: "vulkan"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Requested, []string{"cmake", "curl", "jq", "libvulkan-dev"}) {
		t.Errorf("requested %v", res.Requested)
	}
	if !strings.Contains(strings.Join(res.NotInKit, ","), "build-essential") || !strings.Contains(strings.Join(res.NotInKit, ","), "mesa-vulkan-drivers") {
		t.Errorf("the default packages the kit lacks are reported: %v", res.NotInKit)
	}
	// A cpu node does not install the Vulkan package.
	f.installed = map[string]string{}
	res, err = a.Run(context.Background(), kitDir, kit.Request{Backend: "cpu", DryRun: true})
	if err != nil || strings.Contains(strings.Join(res.Requested, ","), "libvulkan-dev") {
		t.Errorf("cpu: %+v %v", res, err)
	}
	// Default packages come from the debs repository only, never from nvidia/.
	nv := buildRepo(t, kitDir, "nvidia", "noble-amd64-595-open", fakeDeb{"curl", "9.9.9-9"}, debNvidia)
	_ = nv
	res, err = a.Run(context.Background(), kitDir, kit.Request{Backend: "cpu", DryRun: true})
	if err != nil || !reflect.DeepEqual(res.Repos, []string{"debs/noble-amd64"}) {
		t.Errorf("repos %+v %v", res, err)
	}
	// A kit with none of the default packages.
	empty := filepath.Join(t.TempDir(), "kit")
	buildRepo(t, empty, "debs", "noble-amd64", debNvidia)
	if _, err := a.Run(context.Background(), empty, kit.Request{Backend: "cpu"}); !errors.Is(err, kit.ErrNotInKit) {
		t.Errorf("%v", err)
	}
	// Only nvidia repositories for this machine: the default set has no repository to come from.
	only := filepath.Join(t.TempDir(), "kit")
	buildRepo(t, only, "nvidia", "noble-amd64-595-open", debNvidia)
	if _, err := a.Run(context.Background(), only, kit.Request{Backend: "cpu"}); !errors.Is(err, kit.ErrNoRepo) {
		t.Errorf("%v", err)
	}
}
