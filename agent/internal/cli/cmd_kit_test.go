package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// Tests of the `aictl kit` group, `kit install-debs` and `kit install <tool>` (KIT-06). The kit is a
// directory built here with digests computed on the spot; apt, dpkg and systemctl are a fake runner.

var kitEpoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

func kitSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func kitWrite(t testing.TB, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type kitDeb struct{ name, version string }

func (d kitDeb) body() []byte { return []byte("fixture deb " + d.name + " " + d.version + "\n") }
func (d kitDeb) pool() string { return d.name + "_" + d.version + "_amd64.deb" }

func kitGzip(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// kitBuildRepo writes a complete apt repository of the kit the way the apt_closure resolver does.
func kitBuildRepo(t testing.TB, kitDir, part, name string, debs ...kitDeb) string {
	t.Helper()
	dir := filepath.Join(kitDir, part, name)
	var index, lock bytes.Buffer
	sort.Slice(debs, func(i, j int) bool { return debs[i].name < debs[j].name })
	for _, d := range debs {
		kitWrite(t, filepath.Join(dir, "pool", d.pool()), string(d.body()))
		sum := kitSHA(d.body())
		fmt.Fprintf(&index, "Package: %s\nVersion: %s\nArchitecture: amd64\nFilename: pool/%s\nSize: %d\nSHA256: %s\n\n", d.name, d.version, d.pool(), len(d.body()), sum)
		fmt.Fprintf(&lock, "%s=%s %s\n", d.name, d.version, sum)
	}
	kitWrite(t, filepath.Join(dir, "Packages"), index.String())
	if err := os.WriteFile(filepath.Join(dir, "Packages.gz"), kitGzip(t, index.Bytes()), 0o644); err != nil {
		t.Fatal(err)
	}
	kitWrite(t, filepath.Join(dir, "PACKAGES.lock"), lock.String())
	kitWrite(t, filepath.Join(dir, "README.txt"), "fixture\n")
	var sums []manifest.Sum
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		sums = append(sums, manifest.Sum{SHA256: kitSHA(b), Path: filepath.ToSlash(rel)})
		return nil
	})
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		t.Fatal(err)
	}
	return dir
}

type kitTarItem struct{ name, body string }

func kitTarGz(t testing.TB, items ...kitTarItem) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, it := range items {
		tw.WriteHeader(&tar.Header{Name: it.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(it.body))})
		tw.Write([]byte(it.body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// kitPutTool writes <kit>/<dir> with files and a MANIFEST.json that pins each file's sha256.
func kitPutTool(t testing.TB, kitDir, dir, version string, files map[string][]byte) string {
	t.Helper()
	d := filepath.Join(kitDir, filepath.FromSlash(dir))
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: "kit-" + filepath.Base(d), ItemKind: manifest.KindKit, CatalogVersion: "2026.11.02-1",
		Version: version, SortKey: version, State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
		DownloadedAt: kitEpoch, VerifiedAt: kitEpoch, LastReverifiedAt: kitEpoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		kitWrite(t, filepath.Join(d, n), string(files[n]))
		m.TotalBytes += int64(len(files[n]))
		m.Files = append(m.Files, manifest.File{Kind: "archive", Path: n, SizeBytes: int64(len(files[n])), SHA256: kitSHA(files[n]), SHA256Source: "catalog"})
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(d), m); err != nil {
		t.Fatal(err)
	}
	return d
}

// kitRunner is the fake of dpkg-query, apt-get, systemctl and the installed programs (a program
// "runs" by printing its own file).
type kitRunner struct {
	mu        sync.Mutex
	calls     []string
	installed map[string]string
	plan      string
	fail      map[string]int // "update", "plan", "install", "systemctl"
}

func (r *kitRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	switch filepath.Base(c.Path) {
	case "dpkg-query":
		var b strings.Builder
		for _, n := range c.Args[2:] {
			if v, ok := r.installed[n]; ok {
				fmt.Fprintf(&b, "%s\tii \t%s\n", n, v)
			}
		}
		return execx.Result{Stdout: []byte(b.String())}, nil
	case "apt-get":
		verb := c.Args[0]
		for _, a := range c.Args {
			if a == "-s" {
				verb = "plan"
			}
		}
		if code := r.fail[verb]; code != 0 {
			return execx.Result{ExitCode: code, Stderr: []byte("E: simulated failure of " + verb + "\n")}, nil
		}
		if verb == "plan" {
			return execx.Result{Stdout: []byte(r.plan)}, nil
		}
		if verb == "install" {
			for _, line := range strings.Split(r.plan, "\n") {
				f := strings.Fields(line)
				if i := strings.Index(line, "("); len(f) > 2 && f[0] == "Inst" && i >= 0 {
					r.installed[f[1]] = strings.Fields(line[i+1:])[0]
				}
			}
		}
		return execx.Result{}, nil
	case "systemctl":
		return execx.Result{ExitCode: r.fail["systemctl"]}, nil
	}
	b, err := os.ReadFile(c.Path)
	if err != nil {
		return execx.Result{}, err
	}
	return execx.Result{Stdout: b}, nil
}

func (r *kitRunner) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *kitRunner) aptCalls() int {
	n := 0
	for _, l := range r.lines() {
		if strings.HasPrefix(l, "apt-get ") {
			n++
		}
	}
	return n
}

// tarballCall is what a fake kiwix or piper installer was given.
type tarballCall struct{ tarball, sha, version string }

type kitFix struct {
	*localFixture
	kitDir     string
	opt        string
	scratch    string
	sourceList string
	runner     *kitRunner
	kiwix      []tarballCall
	piper      []tarballCall
	kiwixErr   error
	piperErr   error
	signer     *updatetest.Signer
}

type kitCoded struct{ code string }

func (e *kitCoded) Error() string { return "the tool refused" }
func (e *kitCoded) Code() string  { return e.code }

func newKitFix(t *testing.T) *kitFix {
	t.Helper()
	applyLate(DefaultRegistry) // the kit subcommands join their group after every init
	f := &kitFix{localFixture: newLocalFixture(t), opt: t.TempDir(), scratch: t.TempDir(), signer: updatetest.NewSigner()}
	f.sourceList = filepath.Join(t.TempDir(), "localai-ark.list")
	f.kitDir = filepath.Join(f.env.Paths.InternalRoot, "kit")
	if err := os.MkdirAll(f.kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	osr := filepath.Join(t.TempDir(), "os-release")
	kitWrite(t, osr, "NAME=Ubuntu\nVERSION_CODENAME=noble\n")
	f.runner = &kitRunner{installed: map[string]string{}, fail: map[string]int{}}
	pub, err := update.ParsePublicKey(f.signer.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	old := kitHostFn
	kitHostFn = func() kitHost {
		return kitHost{
			FS: kit.OSFS{}, Runner: f.runner,
			LookPath:  func(n string) (string, error) { return "/usr/bin/" + n, nil },
			OSRelease: osr, GOARCH: "amd64", SourceList: f.sourceList, ScratchDir: f.scratch, OptRoot: f.opt,
			Kiwix: func(_ context.Context, tb, sha, v string) error {
				f.kiwix = append(f.kiwix, tarballCall{tb, sha, v})
				return f.kiwixErr
			},
			Piper: func(_ context.Context, tb, sha, v string) error {
				f.piper = append(f.piper, tarballCall{tb, sha, v})
				return f.piperErr
			},
			Keys: []update.PublicKey{pub},
		}
	}
	t.Cleanup(func() { kitHostFn = old })
	return f
}

func (f *kitFix) run(t *testing.T, args ...string) runResult {
	t.Helper()
	return runCLI(t, DefaultRegistry, localOpts(nil), args...)
}

func (f *kitFix) runAs(t *testing.T, euid int, args ...string) runResult {
	t.Helper()
	o := localOpts(nil)
	o.euid = euid
	return runCLI(t, DefaultRegistry, o, args...)
}

const kitCmakePlan = "Inst cmake-data (3.28.3-1 Ubuntu:24.04/noble [all])\nInst cmake (3.28.3-1 Ubuntu:24.04/noble [amd64])\n"

var (
	kitCmake = kitDeb{"cmake", "3.28.3-1"}
	kitData  = kitDeb{"cmake-data", "3.28.3-1"}
	kitCurl  = kitDeb{"curl", "8.5.0-1"}
)

// withDebs puts a debs repository for noble-amd64 into the internal kit.
func (f *kitFix) withDebs(t *testing.T) string {
	t.Helper()
	f.runner.plan = kitCmakePlan
	return kitBuildRepo(t, f.kitDir, "debs", "noble-amd64", kitCmake, kitData, kitCurl)
}

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(unwrap(s), p) {
			t.Errorf("%q does not contain %q", s, p)
		}
	}
}

// ---- the group ----------------------------------------------------------------------------------

func TestKitGroupListsItsSubcommandsAndExits2(t *testing.T) {
	f := newKitFix(t)
	r := f.run(t, "kit")
	if r.code != ExitUsage || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "kit.en", r)
	es := f.run(t, "--lang", "es", "kit")
	if es.code != ExitUsage {
		t.Fatalf("exit %d", es.code)
	}
	checkGolden(t, "kit.es", es)
}

func TestKitGroupUnknownSubcommandAndJSON(t *testing.T) {
	f := newKitFix(t)
	r := f.run(t, "kit", "bogus")
	if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-005") || !strings.Contains(r.errOut, "install, install-debs") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	r = f.run(t, "--json", "kit")
	var doc struct{ Error struct{ Code string } }
	if r.code != ExitUsage || json.Unmarshal([]byte(r.out), &doc) != nil || doc.Error.Code != CodeUsage {
		t.Fatalf("exit %d out %q err %q", r.code, r.out, r.errOut)
	}
}

// A subcommand registered by another file joins the group and the group answer lists it, without the
// group file being edited (the convention of #59, #62 and #70).
func TestRegisterKitSubJoinsTheGroup(t *testing.T) {
	reg := NewRegistry()
	reg.MustAdd(kitCommand())
	attach(reg, []string{"kit"}, &Command{Name: "status", Summary: "Show the kit.", Run: func(*Call) error { return nil }})
	attach(reg, []string{"kit"}, &Command{Name: "verify", Summary: "Verify the kit.", Run: func(*Call) error { return nil }})
	kitCmd, _ := reg.Lookup("kit")
	if subNames(kitCmd) != "status,verify" {
		t.Fatalf("subcommands %s", subNames(kitCmd))
	}
	// The real tree has both of this issue's subcommands.
	applyLate(DefaultRegistry)
	real, ok := DefaultRegistry.Lookup("kit")
	if !ok || !strings.Contains(subNames(real), "install") || !strings.Contains(subNames(real), "install-debs") {
		t.Fatalf("real kit group: %v", subNames(real))
	}
	for _, s := range real.Sub {
		if s.Marks&MarkRoot == 0 || s.Marks&MarkLocal == 0 {
			t.Errorf("%s must be marked [local] [root]", s.Name)
		}
	}
}

// ---- install-debs -------------------------------------------------------------------------------

func TestInstallDebsInstallsFromAVerifiedKit(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "kit.install-debs.en", r)
	if _, err := os.Stat(f.sourceList); !os.IsNotExist(err) {
		t.Errorf("the temporary source must be removed: %v", err)
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["action"] != "kit.install_debs" || lines[0]["result"] != "ok" ||
		lines[0]["target"].(map[string]any)["installed"] != "cmake" || lines[0]["target"].(map[string]any)["root"] != "internal" {
		t.Errorf("audit %v", lines)
	}
}

func TestInstallDebsSpanish(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	f.runner.plan = kitCmakePlan + "Inst curl (8.5.0-1 Ubuntu:24.04/noble [amd64])\n"
	r := f.run(t, "--lang", "es", "kit", "install-debs", "--packages", "cmake,curl")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "kit.install-debs.es", r)
}

func TestInstallDebsJSON(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	r := f.run(t, "--json", "kit", "install-debs", "--packages", "cmake")
	if r.code != 0 || r.errOut != "" {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	var doc struct {
		Repos     []string `json:"repos"`
		Requested []string `json:"requested"`
		Installed []struct{ Name, Version string }
		Plan      []struct{ Name, Version string }
		DryRun    bool `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(r.out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%q", err, r.out)
	}
	if len(doc.Repos) != 1 || doc.Repos[0] != "debs/noble-amd64" || len(doc.Installed) != 1 || doc.Installed[0].Name != "cmake" || len(doc.Plan) != 2 || doc.DryRun {
		t.Errorf("%+v", doc)
	}
	if strings.Count(r.out, "\n") != 1 {
		t.Errorf("exactly one document: %q", r.out)
	}
}

func TestInstallDebsDryRunChangesNothing(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "kit.install-debs.dry-run.en", r)
	for _, l := range f.runner.lines() {
		if strings.HasPrefix(l, "apt-get install ") && !strings.Contains(l, " -s ") {
			t.Errorf("a dry run installed: %s", l)
		}
	}
	if len(f.auditLines()) != 0 {
		t.Errorf("a dry run changes nothing, so nothing is audited: %v", f.auditLines())
	}
	if _, err := os.Stat(f.sourceList); !os.IsNotExist(err) {
		t.Error("the source list was written")
	}
	// If apt cannot resolve it, a dry run exits non-zero.
	f.runner.fail["plan"] = 100
	r = f.run(t, "kit", "install-debs", "--packages", "cmake", "--dry-run")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "LAI-KIT-047") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestInstallDebsIsIdempotent(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	f.runner.installed["cmake"] = "3.28.3-1"
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "Everything asked for is already installed (1 package).") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	if f.runner.aptCalls() != 0 {
		t.Errorf("apt must not run: %v", f.runner.lines())
	}
}

func TestInstallDebsNeedsRoot(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	r := f.runAs(t, 1000, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if len(f.runner.lines()) != 0 || len(f.auditLines()) != 0 {
		t.Errorf("nothing may run or be audited: %v %v", f.runner.lines(), f.auditLines())
	}
	for _, args := range [][]string{{"kit", "install-debs", "--dry-run"}, {"kit", "install", "pmtiles"}} {
		if r := f.runAs(t, 1000, args...); r.code != ExitDenied || !strings.Contains(r.errOut, "LAI-CLI-006") {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.errOut)
		}
	}
}

func TestInstallDebsBadPackageNames(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	for _, in := range []string{"cmake;id", "Cmake", "-y", "a b", "$(id)", "cmake,,curl", "", "a=1"} {
		r := f.run(t, "kit", "install-debs", "--packages", in)
		if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-005") {
			t.Errorf("%q: exit %d\n%s", in, r.code, r.errOut)
		}
	}
	if len(f.runner.lines()) != 0 {
		t.Errorf("nothing may run: %v", f.runner.lines())
	}
	if r := f.run(t, "kit", "install-debs", "extra"); r.code != ExitUsage {
		t.Errorf("a stray argument: exit %d", r.code)
	}
}

func TestInstallDebsRefusesATamperedDeb(t *testing.T) {
	f := newKitFix(t)
	repo := f.withDebs(t)
	kitWrite(t, filepath.Join(repo, "pool", kitCmake.pool()), "tampered")
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-001]") || !strings.Contains(unwrap(r.errOut), "pool/cmake_3.28.3-1_amd64.deb") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if f.runner.aptCalls() != 0 {
		t.Errorf("apt ran on a damaged kit: %v", f.runner.lines())
	}
	if _, err := os.Stat(f.sourceList); !os.IsNotExist(err) {
		t.Error("a source was written")
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["result"] != "fail" || lines[0]["code"] != "LAI-KIT-001" {
		t.Errorf("audit %v", lines)
	}
	checkGolden(t, "kit.install-debs.damaged.en", r)
}

func TestInstallDebsDamagedInSpanish(t *testing.T) {
	f := newKitFix(t)
	repo := f.withDebs(t)
	kitWrite(t, filepath.Join(repo, "pool", kitCmake.pool()), "tampered")
	r := f.run(t, "--lang", "es", "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitFailed {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "kit.install-debs.damaged.es", r)
}

func TestInstallDebsMissingPackagesLock(t *testing.T) {
	f := newKitFix(t)
	repo := f.withDebs(t)
	os.Remove(filepath.Join(repo, "PACKAGES.lock"))
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-001]") || f.runner.aptCalls() != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
}

func TestInstallDebsUnknownCodenameNamesWhatTheKitHas(t *testing.T) {
	f := newKitFix(t)
	kitBuildRepo(t, f.kitDir, "debs", "resolute-amd64", kitCmake)
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-042]") ||
		!strings.Contains(unwrap(r.errOut), "needs packages for noble-amd64, but the kit only has: debs/resolute-amd64.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "kit.install-debs.codename.en", r)
	es := f.run(t, "--lang", "es", "kit", "install-debs", "--packages", "cmake")
	checkGolden(t, "kit.install-debs.codename.es", es)
}

func TestInstallDebsNoKitAndNoPackage(t *testing.T) {
	f := newKitFix(t)
	if err := os.RemoveAll(f.kitDir); err != nil {
		t.Fatal(err)
	}
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") {
		t.Fatalf("no kit: exit %d\n%s", r.code, r.errOut)
	}
	f = newKitFix(t)
	f.withDebs(t)
	r = f.run(t, "kit", "install-debs", "--packages", "cmake,gpsd")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") || !strings.Contains(r.errOut, "gpsd") {
		t.Fatalf("no package: exit %d\n%s", r.code, r.errOut)
	}
	// A kit folder with no debs at all.
	f = newKitFix(t)
	r = f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") {
		t.Fatalf("empty kit: exit %d\n%s", r.code, r.errOut)
	}
}

func TestInstallDebsAptFailureRemovesTheSource(t *testing.T) {
	f := newKitFix(t)
	f.withDebs(t)
	f.runner.fail["install"] = 100
	r := f.run(t, "kit", "install-debs", "--packages", "cmake")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-047]") || !strings.Contains(unwrap(r.errOut), "simulated failure of install") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if _, err := os.Stat(f.sourceList); !os.IsNotExist(err) {
		t.Error("the source list must be removed after a failure")
	}
	if lines := f.auditLines(); len(lines) != 1 || lines[0]["code"] != "LAI-KIT-047" {
		t.Errorf("audit %v", lines)
	}
}

func TestInstallDebsRootOption(t *testing.T) {
	f := newKitFix(t)
	// The internal root has no packages; a drive-like root given by path has them.
	drive := filepath.Join(t.TempDir(), "drive", "localai")
	f.runner.plan = kitCmakePlan
	kitBuildRepo(t, filepath.Join(drive, "kit"), "debs", "noble-amd64", kitCmake, kitData)
	r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--root", drive)
	if r.code != 0 || !strings.Contains(r.out, "Installed 1 package from the kit: cmake.") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	// The kit folder itself is accepted too.
	f.runner.installed = map[string]string{}
	if r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--root", filepath.Join(drive, "kit")); r.code != 0 {
		t.Fatalf("kit path: exit %d\n%s", r.code, r.errOut)
	}
	for _, bad := range []string{"nonsense", "ark-1234", "../x"} {
		if r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--root", bad); r.code != ExitUsage {
			t.Errorf("--root %q: exit %d\n%s", bad, r.code, r.errOut)
		}
	}
	if r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--root", "ark-3f9c0a12"); r.code != ExitNotFound {
		t.Errorf("an id with no drive: exit %d\n%s", r.code, r.errOut)
	}
	if r := f.run(t, "kit", "install-debs", "--packages", "cmake", "--root", filepath.Join(t.TempDir(), "nothing")); r.code != ExitNotFound {
		t.Errorf("a path with no kit: exit %d\n%s", r.code, r.errOut)
	}
}

func TestInstallDebsDefaultSetFollowsTheNodeBackend(t *testing.T) {
	f := newKitFix(t)
	kitBuildRepo(t, f.kitDir, "debs", "noble-amd64", kitCmake, kitCurl, kitDeb{"libvulkan-dev", "1.3.275-1"}, kitDeb{"nvidia-cuda-toolkit", "12.4.131-1"})
	kitWrite(t, f.env.Paths.NodeYAML(), "llama:\n  backend: vulkan\n")
	r := f.run(t, "--json", "kit", "install-debs", "--dry-run")
	var doc struct{ Requested []string }
	if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	got := strings.Join(doc.Requested, ",")
	if !strings.Contains(got, "libvulkan-dev") || strings.Contains(got, "nvidia-cuda-toolkit") || !strings.Contains(got, "cmake") {
		t.Errorf("vulkan node: %s", got)
	}
	kitWrite(t, f.env.Paths.NodeYAML(), "llama:\n  backend: cuda\n")
	r = f.run(t, "--json", "kit", "install-debs", "--dry-run")
	doc.Requested = nil
	if r.code != 0 || json.Unmarshal([]byte(r.out), &doc) != nil {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	got = strings.Join(doc.Requested, ",")
	if strings.Contains(got, "libvulkan-dev") || !strings.Contains(got, "nvidia-cuda-toolkit") {
		t.Errorf("cuda node: %s", got)
	}
}

// ---- install <tool> -----------------------------------------------------------------------------

func TestInstallToolArguments(t *testing.T) {
	f := newKitFix(t)
	for _, args := range [][]string{{"kit", "install"}, {"kit", "install", "llama"}, {"kit", "install", "kiwix"}, {"kit", "install", "agent", "piper"}, {"kit", "install", "../agent"}} {
		r := f.run(t, args...)
		if r.code != ExitUsage || !strings.Contains(r.errOut, "LAI-CLI-005") {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.errOut)
		}
	}
	r := f.run(t, "kit", "install", "llama")
	mustContain(t, r.errOut, `Unknown tool "llama".`, "Use one of: kiwix-tools, pmtiles, piper, agent.")
	// An unknown tool is refused even for a non-root caller, before anything is looked at.
	if r := f.runAs(t, 1000, "kit", "install", "llama"); r.code != ExitUsage {
		t.Errorf("exit %d", r.code)
	}
}

func TestInstallKiwix(t *testing.T) {
	f := newKitFix(t)
	body := []byte("kiwix tarball")
	dir := kitPutTool(t, f.kitDir, "tools/kiwix-tools", "3.8.2", map[string][]byte{
		"kiwix-tools_linux-x86_64-3.8.2.tar.gz": body, "kiwix-tools_linux-aarch64-3.8.2.tar.gz": []byte("arm"),
	})
	r := f.run(t, "kit", "install", "kiwix-tools")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	want := tarballCall{filepath.Join(dir, "kiwix-tools_linux-x86_64-3.8.2.tar.gz"), kitSHA(body), "3.8.2"}
	if len(f.kiwix) != 1 || f.kiwix[0] != want {
		t.Fatalf("kiwix installer got %+v, want %+v", f.kiwix, want)
	}
	checkGolden(t, "kit.install.kiwix.en", r)
	es := f.run(t, "--lang", "es", "kit", "install", "kiwix-tools")
	checkGolden(t, "kit.install.kiwix.es", es)
	lines := f.auditLines()
	if len(lines) != 2 || lines[0]["action"] != "kit.install" || lines[0]["target"].(map[string]any)["tool"] != "kiwix-tools" {
		t.Errorf("audit %v", lines)
	}
}

func TestInstallPiperPassesTheInstallersOwnCode(t *testing.T) {
	f := newKitFix(t)
	body := []byte("piper tarball")
	kitPutTool(t, f.kitDir, "tools/piper", "2023.11.14-2", map[string][]byte{"piper_linux_x86_64.tar.gz": body, "piper_linux_aarch64.tar.gz": []byte("a")})
	r := f.run(t, "--json", "kit", "install", "piper")
	if r.code != 0 || len(f.piper) != 1 || f.piper[0].sha != kitSHA(body) || f.piper[0].version != "2023.11.14-2" {
		t.Fatalf("exit %d\n%s\n%s\n%+v", r.code, r.out, r.errOut, f.piper)
	}
	f.piperErr = &kitCoded{"LAI-SVC-020"}
	r = f.run(t, "kit", "install", "piper")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-SVC-020]") || !strings.Contains(unwrap(r.errOut), "version in use was not changed") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if lines := f.auditLines(); lines[len(lines)-1]["code"] != "LAI-SVC-020" || lines[len(lines)-1]["result"] != "fail" {
		t.Errorf("audit %v", lines)
	}
}

func TestInstallToolRefusesATamperedTarball(t *testing.T) {
	f := newKitFix(t)
	dir := kitPutTool(t, f.kitDir, "tools/kiwix-tools", "3.8.2", map[string][]byte{"kiwix-tools_linux-x86_64-3.8.2.tar.gz": []byte("good")})
	kitWrite(t, filepath.Join(dir, "kiwix-tools_linux-x86_64-3.8.2.tar.gz"), "evil")
	r := f.run(t, "kit", "install", "kiwix-tools")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-001]") || !strings.Contains(unwrap(r.errOut), "kiwix-tools_linux-x86_64-3.8.2.tar.gz does not match its checksum") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if len(f.kiwix) != 0 {
		t.Error("the installer must not see a tarball that fails its checksum")
	}
	checkGolden(t, "kit.install.damaged.en", r)
}

func TestInstallToolNotInKitIsExit5(t *testing.T) {
	f := newKitFix(t)
	r := f.run(t, "kit", "install", "pmtiles")
	if r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") || !strings.Contains(unwrap(r.errOut), "The kit has no pmtiles for this computer.") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	if err := os.RemoveAll(f.kitDir); err != nil {
		t.Fatal(err)
	}
	if r := f.run(t, "kit", "install", "pmtiles"); r.code != ExitNotFound || !strings.Contains(r.errOut, "[LAI-KIT-040]") {
		t.Fatalf("no kit: exit %d\n%s", r.code, r.errOut)
	}
}

func TestInstallPmtilesAndSwitchBack(t *testing.T) {
	f := newKitFix(t)
	put := func(version string) {
		kitPutTool(t, f.kitDir, "tools/pmtiles", version, map[string][]byte{
			"go-pmtiles_" + version + "_Linux_x86_64.tar.gz": kitTarGz(t, kitTarItem{"LICENSE", "x"}, kitTarItem{"pmtiles", "pmtiles " + version}),
			"go-pmtiles_" + version + "_Linux_arm64.tar.gz":  kitTarGz(t, kitTarItem{"pmtiles", "pmtiles " + version}),
		})
	}
	put("1.0.0")
	if r := f.run(t, "kit", "install", "pmtiles"); r.code != 0 || !strings.Contains(r.out, "Installed pmtiles 1.0.0 from the kit and made it the current version.") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	put("1.1.0")
	r := f.run(t, "kit", "install", "pmtiles")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "The previous version 1.0.0 is kept (/opt/localai/pmtiles/1.0.0).") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	cur, err := os.Readlink(filepath.Join(f.opt, "pmtiles", "current"))
	if err != nil || cur != "1.1.0" {
		t.Errorf("current -> %q %v", cur, err)
	}
	if _, err := os.Stat(filepath.Join(f.opt, "pmtiles", "1.0.0", "pmtiles")); err != nil {
		t.Error("the previous version must be kept")
	}
	r = f.run(t, "kit", "install", "pmtiles")
	if r.code != 0 || !strings.Contains(unwrap(r.out), "pmtiles 1.1.0 is already the current version; nothing was changed.") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
}

func agentJSON(version string) string {
	return fmt.Sprintf(`{"version":"%s","os":"linux","arch":"amd64"}`, version)
}

// kitAgent writes a signed kit/bin with the stand-in agent of version.
func (f *kitFix) kitAgent(t *testing.T, version string, signer *updatetest.Signer) {
	t.Helper()
	files := map[string][]byte{"aiagent-linux-amd64": []byte(agentJSON(version)), "aiagent-linux-arm64": []byte(strings.Replace(agentJSON(version), "amd64", "arm64", 1))}
	d := kitPutTool(t, f.kitDir, "bin", "node-v"+version, files)
	sums := updatetest.Sums(files)
	kitWrite(t, filepath.Join(d, "SHA256SUMS"), string(sums))
	kitWrite(t, filepath.Join(d, "SHA256SUMS.minisig"), string(signer.Sign(sums, updatetest.SignOptions{})))
}

func (f *kitFix) installedAgent(t *testing.T, version string) {
	t.Helper()
	p := filepath.Join(f.opt, "bin", "aiagent")
	kitWrite(t, p, agentJSON(version))
	os.Chmod(p, 0o755)
}

func TestInstallAgentFromTheKit(t *testing.T) {
	f := newKitFix(t)
	f.kitAgent(t, "0.5.0", f.signer)
	f.installedAgent(t, "0.4.0")
	r := f.run(t, "kit", "install", "agent")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
	checkGolden(t, "kit.install.agent.en", r)
	if got, _ := os.ReadFile(filepath.Join(f.opt, "bin", "aiagent")); string(got) != agentJSON("0.5.0") {
		t.Errorf("installed: %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(f.opt, "bin", "aiagent.prev")); string(got) != agentJSON("0.4.0") {
		t.Errorf("aiagent.prev: %s", got)
	}
	lines := f.runner.lines()
	if lines[len(lines)-1] != "systemctl restart localai-agent.service" {
		t.Errorf("the agent must be restarted: %v", lines)
	}
}

func TestInstallAgentBadSignatureReplacesNothing(t *testing.T) {
	f := newKitFix(t)
	f.kitAgent(t, "0.5.0", updatetest.NewSigner()) // signed by a key this program does not trust
	f.installedAgent(t, "0.4.0")
	r := f.run(t, "kit", "install", "agent")
	if r.code != ExitFailed || !strings.Contains(r.errOut, "[LAI-KIT-044]") {
		t.Fatalf("exit %d\n%s", r.code, r.errOut)
	}
	checkGolden(t, "kit.install.agent.signature.en", r)
	if got, _ := os.ReadFile(filepath.Join(f.opt, "bin", "aiagent")); string(got) != agentJSON("0.4.0") {
		t.Errorf("the agent was replaced: %s", got)
	}
	if es := fileNames(t, filepath.Join(f.opt, "bin")); strings.Join(es, ",") != "aiagent" {
		t.Errorf("bin holds %v", es)
	}
	for _, l := range f.runner.lines() {
		if strings.HasPrefix(l, "systemctl") {
			t.Error("the agent must not be restarted")
		}
	}
	lines := f.auditLines()
	if len(lines) != 1 || lines[0]["code"] != "LAI-KIT-044" || lines[0]["result"] != "fail" {
		t.Errorf("audit %v", lines)
	}
}

func TestInstallAgentRestartFailureIsAWarning(t *testing.T) {
	f := newKitFix(t)
	f.kitAgent(t, "0.5.0", f.signer)
	f.runner.fail["systemctl"] = 5
	r := f.run(t, "kit", "install", "agent")
	if r.code != 0 || !strings.Contains(unwrap(r.errOut), "could not be restarted: run `sudo systemctl restart localai-agent`") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errOut)
	}
}

func fileNames(t *testing.T, dir string) []string {
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
