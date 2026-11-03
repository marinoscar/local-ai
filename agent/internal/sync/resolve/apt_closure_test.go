package resolve

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
	"github.com/marinoscar/local-ai/agent/internal/sync/download"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/throttle"
)

// basicPkgs: pkg-a and pkg-b depend on each other (a dependency cycle), pkg-b carries an epoch,
// pkg-c stands alone.
func basicPkgs() []fxPkg {
	return []fxPkg{
		{Name: "pkg-a", Version: "1.0", Depends: "pkg-b"},
		{Name: "pkg-b", Version: "2:1.0-1", Depends: "pkg-a"},
		{Name: "pkg-c", Version: "1.0"},
	}
}

func produceApt(t *testing.T, a *AptClosure, f *aptFixture, root, presentDir, presentVersion string, pkgs ...string) (Product, plannedVersion) {
	t.Helper()
	item := aptItem("kit-debs-test-"+f.arch, "test", f.arch, pkgs...)
	r, err := a.Resolve(context.Background(), item)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	req := Request{Item: item, RootID: "internal", Root: root, Version: r.Version, PresentDir: presentDir, PresentVersion: presentVersion}
	prod, err := a.Produce(context.Background(), req)
	if err != nil {
		t.Fatalf("produce: %v", err)
	}
	return prod, plannedVersion{Version: r.Version, Size: r.SizeBytes, Req: req}
}

type plannedVersion struct {
	Version string
	Size    int64
	Req     Request
}

func TestAptClosureComputesVersionAndSize(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-a", "pkg-c")
	r, err := a.Resolve(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	want := closureVersion([]Pkg{{Name: "pkg-a", Version: "1.0"}, {Name: "pkg-b", Version: "2:1.0-1"}, {Name: "pkg-c", Version: "1.0"}})
	if r.Version != want {
		t.Errorf("version = %q, want %q (the cycle's other member is part of the closure)", r.Version, want)
	}
	var size int64
	for _, p := range basicPkgs() {
		st, err := os.Stat(filepath.Join(f.root, "pool", f.FileName(p)))
		if err != nil {
			t.Fatal(err)
		}
		size += st.Size()
	}
	if r.SizeBytes != size {
		t.Errorf("size = %d, want %d", r.SizeBytes, size)
	}
	// A package listed twice in the catalog and in another order is the same closure.
	again, err := a.Resolve(context.Background(), aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c", "pkg-a", "pkg-a"))
	if err != nil || again.Version != r.Version {
		t.Fatalf("duplicate/reordered packages: (%+v, %v)", again, err)
	}
}

func TestAptClosureProduceBuildsAUsableLocalRepository(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	root := t.TempDir()
	prod, pv := produceApt(t, a, f, root, "", "", "pkg-a", "pkg-c")

	if prod.Version != pv.Version || prod.Trust != manifest.TrustSignedIndex || prod.Keep {
		t.Fatalf("product = %+v", prod)
	}
	if len(prod.Parts) != 1 {
		t.Fatalf("parts = %+v", prod.Parts)
	}
	part := prod.Parts[0]
	if part.Kind != PartTree || part.From != "tree" || part.To != "kit/debs/test-amd64" {
		t.Errorf("part = %+v", part)
	}
	tree := filepath.Join(pv.Req.StagingDir(), "tree")
	for _, name := range []string{"Packages", "Packages.gz", "PACKAGES.lock", "README.txt", manifest.SumsName, "pool/pkg-a_1.0_amd64.deb", "pool/pkg-b_1.0-1_amd64.deb", "pool/pkg-c_1.0_amd64.deb"} {
		if _, err := os.Stat(filepath.Join(tree, filepath.FromSlash(name))); err != nil {
			t.Errorf("staged tree lacks %s: %v", name, err)
		}
	}
	// SHA256SUMS is written before the manifest and agrees with the tree hash and the real files.
	sums, err := manifest.ReadSums(osFS, tree)
	if err != nil {
		t.Fatal(err)
	}
	tsha, _ := manifest.TreeSHA256(sums)
	if tsha != part.TreeSHA256 || len(sums) != len(part.Sums) {
		t.Errorf("tree hash %s / %d sums, part says %s / %d", tsha, len(sums), part.TreeSHA256, len(part.Sums))
	}
	for _, s := range sums {
		got, _, err := hashFile(filepath.Join(tree, filepath.FromSlash(s.Path)))
		if err != nil || got != s.SHA256 {
			t.Errorf("%s: sha256 in SHA256SUMS does not match the file (%v)", s.Path, err)
		}
	}
	// Packages: Filename rewritten to pool/, no Description-md5, the epoch kept in Version.
	pk, _ := os.ReadFile(filepath.Join(tree, "Packages"))
	for _, want := range []string{"Package: pkg-b\nVersion: 2:1.0-1\n", "Filename: pool/pkg-b_1.0-1_amd64.deb\n", "Filename: pool/pkg-a_1.0_amd64.deb\n"} {
		if !strings.Contains(string(pk), want) {
			t.Errorf("Packages lacks %q:\n%s", want, pk)
		}
	}
	if strings.Contains(string(pk), "Description-md5") || strings.Contains(string(pk), "Filename: pool/main") {
		t.Errorf("Packages was not rewritten:\n%s", pk)
	}
	lock, _ := os.ReadFile(filepath.Join(tree, "PACKAGES.lock"))
	if !strings.HasPrefix(string(lock), "pkg-a=1.0 ") || !strings.Contains(string(lock), "\npkg-b=2:1.0-1 ") {
		t.Errorf("PACKAGES.lock:\n%s", lock)
	}

	// Offline use: apt, pointed at the staged folder as a flat [trusted=yes] repository, resolves the
	// closure from it with no network (rebuild-kit.md section 4.1.4).
	out := offlineInstallURIs(t, f, tree, "pkg-a")
	if !strings.Contains(out, "file:") || !strings.Contains(out, "pkg-a_1.0_amd64.deb") || !strings.Contains(out, "pkg-b_2%3a1.0-1_amd64.deb") {
		t.Errorf("offline apt did not resolve pkg-a and its cycle partner from the kit repository:\n%s", out)
	}
}

// offlineInstallURIs runs apt against the produced folder as a flat local repository, in a second
// private tree (its own state, no system apt) and returns the --print-uris output.
func offlineInstallURIs(t *testing.T, f *aptFixture, repo, pkg string) string {
	t.Helper()
	tree := t.TempDir()
	for _, d := range []string{"etc/apt/sources.list.d", "etc/apt/apt.conf.d", "etc/apt/trusted.gpg.d", "var/lib/apt/lists/partial", "var/cache/apt/archives/partial", "var/lib/dpkg"} {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(tree, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("var/lib/dpkg/status", "")
	must("etc/apt/apt.conf", "Dir \""+tree+"/\";\n")
	must("etc/apt/sources.list.d/kit.list", "deb [trusted=yes] file:"+repo+" ./\n")
	opts := []string{"-o", "Dir=" + tree, "-o", "Dir::State::status=" + tree + "/var/lib/dpkg/status", "-o", "Dir::Etc::sourcelist=/dev/null",
		"-o", "Dir::Etc::sourceparts=" + tree + "/etc/apt/sources.list.d", "-o", "APT::Architecture=" + f.arch, "-o", "APT::Architectures=" + f.arch,
		"-o", "Debug::NoLocking=1", "-o", "APT::Sandbox::User=", "-o", "Acquire::Languages=none", "-o", "APT::Install-Recommends=false",
		"-o", "Acquire::ForceHash=SHA256"}
	env := []string{"PATH=" + execx.FixedPath, "LC_ALL=C", "APT_CONFIG=" + tree + "/etc/apt/apt.conf", "HOME=" + tree}
	run := func(args ...string) execx.Result {
		res, err := execx.Exec{}.Run(context.Background(), execx.Cmd{Path: f.tools["apt-get"], Args: append(append([]string{}, opts...), args...), Env: env, Timeout: time.Minute, MaxOut: 1 << 20})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("apt-get %v: %v exit %d\n%s%s", args, err, res.ExitCode, res.Stdout, res.Stderr)
		}
		return res
	}
	run("update")
	return string(run("--print-uris", "--yes", "install", pkg).Stdout)
}

func TestAptClosureReusesUnchangedDebsAndKeepsAnUnchangedVersion(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	root := t.TempDir()
	prod1, pv1 := produceApt(t, a, f, root, "", "", "pkg-a", "pkg-c")
	// The engine swaps the staged tree into the root.
	present := filepath.Join(root, "kit", "debs", "test-amd64")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(pv1.Req.StagingDir(), "tree"), present); err != nil {
		t.Fatal(err)
	}
	firstGets := f.Hits("/pool/")
	if firstGets != 3 {
		t.Fatalf("first run fetched %d debs, want 3", firstGets)
	}

	// Same archive state: the version is unchanged, so the item is kept and nothing is staged.
	again, _ := produceApt(t, a, f, root, present, prod1.Version, "pkg-a", "pkg-c")
	if !again.Keep || len(again.Parts) != 0 {
		t.Fatalf("an unchanged closure must be Keep with no parts: %+v", again)
	}
	if f.Hits("/pool/") != firstGets {
		t.Error("a kept closure downloaded debs")
	}

	// A new pkg-c: pkg-a and pkg-b are hard-linked from the present tree, only pkg-c is downloaded.
	f.Pkgs = []fxPkg{basicPkgs()[0], basicPkgs()[1], {Name: "pkg-c", Version: "1.1"}}
	f.publish()
	prod2, pv2 := produceApt(t, a, f, root, present, prod1.Version, "pkg-a", "pkg-c")
	if prod2.Version == prod1.Version || prod2.Keep {
		t.Fatalf("the closure changed but the version did not: %+v", prod2)
	}
	if got := f.Hits("/pool/") - firstGets; got != 1 {
		t.Errorf("second run fetched %d debs, want only the new pkg-c", got)
	}
	for _, name := range []string{"pkg-a_1.0_amd64.deb", "pkg-b_1.0-1_amd64.deb"} {
		old, err1 := os.Stat(filepath.Join(present, "pool", name))
		cur, err2 := os.Stat(filepath.Join(pv2.Req.StagingDir(), "tree", "pool", name))
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: %v %v", name, err1, err2)
		}
		if !os.SameFile(old, cur) {
			t.Errorf("%s was copied or downloaded, not hard-linked", name)
		}
	}
	if _, err := os.Stat(filepath.Join(pv2.Req.StagingDir(), "tree", "pool", "pkg-c_1.1_amd64.deb")); err != nil {
		t.Errorf("the new deb is missing: %v", err)
	}
}

func TestAptClosureResumesAPartialStagingTree(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	root := t.TempDir()
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-a", "pkg-c")
	r, err := a.Resolve(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Item: item, RootID: "internal", Root: root, Version: r.Version}
	// An earlier attempt committed pkg-a's deb into staging before it was killed.
	pool := filepath.Join(req.StagingDir(), "tree", "pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.root, "pool", "pkg-a_1.0_amd64.deb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pool, "pkg-a_1.0_amd64.deb"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// And left a wrong file where pkg-c's deb belongs: it must be replaced, not trusted.
	if err := os.WriteFile(filepath.Join(pool, "pkg-c_1.0_amd64.deb"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Produce(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if f.Hits("pkg-a_1.0_amd64.deb") != 0 {
		t.Error("a verified file already in staging was downloaded again")
	}
	if f.Hits("pkg-c_1.0_amd64.deb") != 1 || f.Hits("pkg-b_1.0-1_amd64.deb") != 1 {
		t.Errorf("the missing and the wrong files must be fetched once each (pkg-c %d, pkg-b %d)", f.Hits("pkg-c_1.0_amd64.deb"), f.Hits("pkg-b_1.0-1_amd64.deb"))
	}
	got, _ := os.ReadFile(filepath.Join(pool, "pkg-c_1.0_amd64.deb"))
	if string(got) == "wrong" {
		t.Error("the wrong file was kept")
	}
}

func TestAptClosureFailuresAreLAISYN060(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *aptFixture)
		item  func(f *aptFixture) (codename, arch string, pkgs []string)
		msg   string
	}{
		{"wrong suite", nil, func(*aptFixture) (string, string, []string) { return "nosuchsuite", "amd64", []string{"pkg-c"} }, ""},
		{"wrong architecture", nil, func(*aptFixture) (string, string, []string) { return "test", "arm64", []string{"pkg-c"} }, ""},
		{"unknown package", nil, func(*aptFixture) (string, string, []string) { return "test", "amd64", []string{"no-such-package"} }, "no-such-package"},
		{"unsatisfiable dependency", func(f *aptFixture) {
			f.Pkgs = append(f.Pkgs, fxPkg{Name: "pkg-x", Version: "1.0", Depends: "pkg-does-not-exist"})
			f.publish()
		}, func(*aptFixture) (string, string, []string) { return "test", "amd64", []string{"pkg-x"} }, "pkg-does-not-exist"},
		{"md5 only index", func(f *aptFixture) { f.MD5Only = true; f.publish() },
			func(*aptFixture) (string, string, []string) { return "test", "amd64", []string{"pkg-c"} }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAptFixture(t, "test", "amd64", basicPkgs()...)
			if tc.setup != nil {
				tc.setup(f)
			}
			a := newAptClosure(t, f)
			cn, arch, pkgs := tc.item(f)
			_, err := a.Resolve(context.Background(), aptItem("kit-debs-"+cn+"-"+arch, cn, arch, pkgs...))
			if CodeOf(err) != doctor.SYN060 {
				t.Fatalf("code = %q, want LAI-SYN-060 (err %v)", CodeOf(err), err)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("the message should carry apt's own words (%q): %v", tc.msg, err)
			}
		})
	}
}

func TestAptClosureUnreachableArchive(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	f.srv.Close()
	_, err := a.Resolve(context.Background(), aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c"))
	if CodeOf(err) != doctor.SYN060 {
		t.Fatalf("code = %q, want LAI-SYN-060 (err %v)", CodeOf(err), err)
	}
}

func TestAptClosureDownloadFailuresKeepTheirOwnCodes(t *testing.T) {
	deb := "/pool/pkg-c_1.0_amd64.deb"
	tests := []struct {
		name     string
		override http.HandlerFunc
		wantCode string
		kind     download.Kind
	}{
		{"404 for one deb", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, doctor.SYN030, download.KindPermanent},
		{"truncated body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("short"))
			w.(http.Flusher).Flush() // chunked: the client cannot tell a short body from the end
		}, doctor.SYN027, download.KindMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAptFixture(t, "test", "amd64", basicPkgs()...)
			a := newAptClosure(t, f)
			item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")
			r, err := a.Resolve(context.Background(), item)
			if err != nil {
				t.Fatal(err)
			}
			f.Override(deb, tc.override)
			root := t.TempDir()
			_, err = a.Produce(context.Background(), Request{Item: item, RootID: "internal", Root: root, Version: r.Version})
			if err == nil {
				t.Fatal("a bad download was accepted")
			}
			var de *download.Error
			if !errors.As(err, &de) || de.Kind != tc.kind || de.Code != tc.wantCode {
				t.Fatalf("err = %v, want download %s / %s", err, tc.kind, tc.wantCode)
			}
		})
	}
}

func TestAptClosurePinMismatchIsQuarantinedNotInstalled(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")
	r, err := a.Resolve(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	// The archive serves other bytes of the right length than the signed index vouched for.
	real, _ := os.ReadFile(filepath.Join(f.root, "pool", "pkg-c_1.0_amd64.deb"))
	evil := append([]byte(nil), real...)
	evil[len(evil)-1] ^= 0xff
	f.Override("/pool/pkg-c_1.0_amd64.deb", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(evil) })
	root := t.TempDir()
	req := Request{Item: item, RootID: "internal", Root: root, Version: r.Version}
	_, err = a.Produce(context.Background(), req)
	var de *download.Error
	if !errors.As(err, &de) || de.Kind != download.KindMismatch || de.Code != doctor.SYN020 {
		t.Fatalf("err = %v, want a checksum mismatch (LAI-SYN-020)", err)
	}
	if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree", "pool", "pkg-c_1.0_amd64.deb")); err == nil {
		t.Error("the mismatching deb was committed to the staged tree")
	}
	if _, err := os.Stat(filepath.Join(req.StagingDir(), "tree", manifest.SumsName)); err == nil {
		t.Error("a SHA256SUMS was written for a failed item")
	}
}

func TestAptClosureVersionMovedWhileSyncing(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")
	_, err := a.Resolve(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Produce(context.Background(), Request{Item: item, RootID: "internal", Root: t.TempDir(), Version: "0123456789ab"})
	if !errors.Is(err, ErrVersionMoved) || CodeOf(err) != doctor.SYN060 {
		t.Fatalf("err = %v, want ErrVersionMoved with LAI-SYN-060", err)
	}
}

// snapshot lists a directory tree with sizes and modification times, "" when it does not exist.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			b.WriteString(p + " " + info.Mode().String() + " " + info.ModTime().String() + "\n")
		}
		return nil
	})
	return b.String()
}

func TestAptClosureTouchesOnlyItsTreeAndStaging(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	system := []string{"/var/lib/apt", "/etc/apt", "/var/cache/apt", "/var/lib/dpkg"}
	before := make([]string, len(system))
	for i, d := range system {
		before[i] = snapshot(t, d)
	}
	root := t.TempDir()
	produceApt(t, a, f, root, "", "", "pkg-a")
	for i, d := range system {
		if after := snapshot(t, d); after != before[i] {
			t.Errorf("%s changed while the closure ran", d)
		}
	}
	// Everything the run created is under the private tree, the staging directory, or the test's own fixture.
	tree := filepath.Join(a.Env.SyncHome, "apt", "kit-debs-test-amd64")
	if _, err := os.Stat(filepath.Join(tree, "var", "lib", "apt", "lists")); err != nil {
		t.Errorf("apt's lists are not in the private tree: %v", err)
	}
	st, err := os.Stat(filepath.Join(tree, "var", "lib", "dpkg", "status"))
	if err != nil || st.Size() != 0 {
		t.Errorf("the dpkg status file must exist and be empty: %v", err)
	}
	if os.Geteuid() == 0 {
		// As root the agent drops to the apt user: the tree belongs to it, and so does everything apt wrote.
		owned := 0
		_ = filepath.WalkDir(filepath.Join(tree, "var", "lib", "apt", "lists"), func(p string, d fs.DirEntry, err error) error {
			if err == nil {
				if uid, ok := ownerUID(d); ok {
					owned++
					if uid != 65534 {
						t.Errorf("%s is owned by uid %d, not the apt user", p, uid)
					}
				}
			}
			return nil
		})
		if owned == 0 {
			t.Error("no file in the lists directory to check the owner of")
		}
	}
}

func TestAptClosureRefusesToRunAptAsRoot(t *testing.T) {
	f := fake.New()
	a := &AptClosure{Env: &Env{Runner: f, Lookup: func(string) (string, error) { return "/usr/bin/apt-get", nil }, Online: func() bool { return true }, SyncHome: t.TempDir()},
		Euid: func() int { return 0 }}
	_, err := a.Resolve(context.Background(), aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c"))
	if CodeOf(err) != doctor.SYN060 || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want LAI-SYN-060 about root", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("%d commands ran", len(f.Calls()))
	}
}

func TestAptClosureOfflineAndMissingTools(t *testing.T) {
	f := fake.New()
	a := &AptClosure{Env: &Env{Runner: f, Lookup: func(string) (string, error) { return "/usr/bin/apt-get", nil }, Online: func() bool { return false }, SyncHome: t.TempDir()},
		Euid: func() int { return 1000 }}
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")
	if _, err := a.Resolve(context.Background(), item); !errors.Is(err, ErrOffline) {
		t.Fatalf("Resolve offline: %v", err)
	}
	if _, err := a.Produce(context.Background(), Request{Item: item, Root: t.TempDir(), Version: "v"}); !errors.Is(err, ErrOffline) {
		t.Fatalf("Produce offline: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Error("commands ran while offline")
	}
	a.Env.Online = func() bool { return true }
	a.Env.Lookup = func(string) (string, error) { return "", execx.ErrNotFound }
	if _, err := a.Resolve(context.Background(), item); CodeOf(err) != doctor.SYN061 {
		t.Fatalf("missing apt-get: code %q (err %v)", CodeOf(err), err)
	}
}

func TestAptClosureArgvEnvAndOutputBounds(t *testing.T) {
	const bin = "/usr/bin/apt-get"
	sync := t.TempDir()
	tree := filepath.Join(sync, "apt", "kit-debs-test-amd64")
	uri := "https://archive.ubuntu.com/ubuntu/pool/main/p/pkg-c/pkg-c_1.0_amd64.deb"
	line := uriLine(uri, "pkg-c_1.0_amd64.deb", 10, "SHA256", "pkg-c")
	opts := aptOptions(tree, "amd64", "localai", "127.0.0.1:7")
	run := fake.New()
	run.Expect(fake.Key(bin, append(append([]string{}, opts...), "update")...), execx.Result{}, nil)
	run.Expect(fake.Key(bin, append(append([]string{}, opts...), "--print-uris", "--yes", "install", "pkg-a", "pkg-c")...), execx.Result{Stdout: []byte(line + "\n")}, nil)

	var hosts []string
	a := &AptClosure{
		Env: &Env{Runner: run, Lookup: func(string) (string, error) { return bin, nil }, Online: func() bool { return true }, SyncHome: sync,
			StartProxy: func(_ context.Context, _ throttle.Bucket, h []string) (string, func(), error) {
				hosts = h
				return "127.0.0.1:7", func() {}, nil
			}},
		Keyrings:   func(string) ([]byte, error) { return []byte("keyring"), nil },
		Credential: &execx.Credential{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())},
		User:       "localai", Euid: func() int { return 1000 },
	}
	if _, err := a.Resolve(context.Background(), aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c", "pkg-a", "pkg-a")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(hosts, ","); got != "archive.ubuntu.com" {
		t.Errorf("proxy hosts = %q, want only the archive", got)
	}
	for i, c := range run.Calls() {
		env := strings.Join(c.Env, "\n")
		for _, want := range []string{"APT_CONFIG=" + tree + "/etc/apt/apt.conf", "PATH=" + execx.FixedPath, "LC_ALL=C"} {
			if !strings.Contains(env, want) {
				t.Errorf("call %d env lacks %s: %v", i, want, c.Env)
			}
		}
		if c.Credential == nil || c.Timeout <= 0 || c.MaxOut <= 0 {
			t.Errorf("call %d is not bounded and dropped: %+v", i, c)
		}
	}
	// Behind the proxy the tree's sources are https (the proxy tunnels CONNECT only).
	src, _ := os.ReadFile(filepath.Join(tree, "etc", "apt", "sources.list.d", "ubuntu.sources"))
	if !strings.Contains(string(src), "URIs: https://archive.ubuntu.com/ubuntu\n") {
		t.Errorf("sources behind the proxy:\n%s", src)
	}
	conf, _ := os.ReadFile(filepath.Join(tree, "etc", "apt", "apt.conf"))
	if !strings.Contains(string(conf), `Dir "`+tree+`/";`) {
		t.Errorf("apt.conf = %q", conf)
	}

	// Output that hit the cap is refused, never parsed.
	run2 := fake.New()
	run2.Expect(fake.Key(bin, append(append([]string{}, aptOptions(tree, "amd64", "localai", "")...), "update")...), execx.Result{}, nil)
	run2.Expect(fake.Key(bin, append(append([]string{}, aptOptions(tree, "amd64", "localai", "")...), "--print-uris", "--yes", "install", "pkg-c")...),
		execx.Result{Stdout: []byte(line + "\n"), Truncated: true}, nil)
	a.Env.Runner = run2
	a.Env.StartProxy = nil
	a.Env.Bucket = nil
	if _, err := a.Resolve(context.Background(), aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")); CodeOf(err) != doctor.SYN060 {
		t.Fatalf("truncated output: code %q (err %v)", CodeOf(err), err)
	}
}

func TestAptClosureExtraReposAndKeysAreWrittenToTheTree(t *testing.T) {
	f := newAptFixture(t, "test", "amd64", basicPkgs()...)
	a := newAptClosure(t, f)
	item := aptItem("kit-debs-test-amd64", "test", "amd64", "pkg-c")
	item.Content.Source.ExtraRepos = nil
	src, _, err := a.item(item)
	if err != nil {
		t.Fatal(err)
	}
	src.Extra = nil
	tree := filepath.Join(t.TempDir(), "tree")
	if err := a.prepareTree(tree, src); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"etc/apt/trusted.gpg.d/" + ubuntuKeyring, "etc/apt/sources.list.d/ubuntu.sources", "etc/apt/sources.list.d/extra.sources", "etc/apt/apt.conf", "var/lib/dpkg/status", "etc/apt/preferences.d", "var/lib/apt/lists"} {
		if _, err := os.Stat(filepath.Join(tree, rel)); err != nil {
			t.Errorf("tree lacks %s: %v", rel, err)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(tree, "etc", "apt", "preferences.d")); len(ents) != 0 {
		t.Error("preferences.d must be empty")
	}
	// A keyring that is not available fails with the tree incomplete rather than trusting nothing.
	a.Keyrings = func(string) ([]byte, error) { return nil, errors.New("not embedded") }
	a.SystemKeyringDir = t.TempDir()
	if err := a.prepareTree(filepath.Join(t.TempDir(), "t2"), src); CodeOf(err) != doctor.SYN060 {
		t.Errorf("missing keyring: code %q (err %v)", CodeOf(err), err)
	}
}

func TestItemDirDerivation(t *testing.T) {
	tests := []struct {
		req  Request
		want string
		ok   bool
	}{
		{Request{Item: aptItem("kit-debs-resolute-amd64", "resolute", "amd64", "x")}, "kit/debs/resolute-amd64", true},
		{Request{Item: aptItem("kit-nvidia-resolute-amd64-595-open", "resolute", "amd64", "x")}, "kit/nvidia/resolute-amd64-595-open", true},
		{Request{Item: aptItem("kit-other", "resolute", "amd64", "x")}, "", false},
		{Request{Item: aptItem("kit-other", "resolute", "amd64", "x"), ItemDir: "kit/other/x"}, "kit/other/x", true},
		{Request{Item: aptItem("kit-other", "resolute", "amd64", "x"), ItemDir: "../escape"}, "", false},
		{Request{Item: aptItem("kit-other", "resolute", "amd64", "x"), ItemDir: "/abs"}, "", false},
	}
	for _, tc := range tests {
		got, err := itemDir(tc.req)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s / %q: (%q, %v), want (%q, ok=%v)", tc.req.Item.ID, tc.req.ItemDir, got, err, tc.want, tc.ok)
		}
	}
}
