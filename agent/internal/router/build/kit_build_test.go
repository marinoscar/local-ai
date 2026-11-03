package build

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/execx/fake"
)

// ---- --prebuilt --------------------------------------------------------------------------

// prebuiltParts is an exported build as `aictl kit export-build` writes it: BUILD.json and bin/.
type prebuiltParts struct {
	info  BuildInfo
	bins  map[string][]byte
	json  []byte          // raw BUILD.json instead of info
	omit  map[string]bool // entries left out: "BUILD.json" or a program name
	extra []tarEntry
}

func newPrebuilt() prebuiltParts {
	p := prebuiltParts{
		bins: map[string][]byte{}, omit: map[string]bool{},
		info: BuildInfo{
			BuildID: testID, Commit: testSHA, Backend: BackendCUDA, CudaArch: "75", CMakeFlags: []string{"-DGGML_CUDA=ON"},
			Compiler: "GNU 15.2.0", NVCC: "12.4.131", BuiltAt: testNow.Format("2006-01-02T15:04:05Z"), BuiltBy: "aiagent 0.1.0",
			SHA256: map[string]string{}, Smoke: Smoke{Version: "version: 6000 (f3586e6)", Devices: []string{"CUDA0: Test GPU"}},
		},
	}
	for _, n := range binaries {
		p.bins[n] = append(elfFixture(true, "", ""), []byte(n)...)
		p.info.SHA256[n] = sum256(p.bins[n])
	}
	return p
}

// write makes the archive and returns its path and digest.
func (p prebuiltParts) write(t *testing.T, dir string) (string, string) {
	t.Helper()
	raw := p.json
	if raw == nil {
		var err error
		if raw, err = json.MarshalIndent(p.info, "", "  "); err != nil {
			t.Fatal(err)
		}
	}
	var entries []tarEntry
	if !p.omit["BUILD.json"] {
		entries = append(entries, tarEntry{name: "BUILD.json", body: string(raw)})
	}
	entries = append(entries, tarEntry{name: "bin/", typ: tar.TypeDir})
	for _, n := range binaries {
		if !p.omit[n] {
			entries = append(entries, tarEntry{name: "bin/" + n, body: string(p.bins[n]), mode: 0o755})
		}
	}
	entries = append(entries, p.extra...)
	data := makeTar(t, entries, true)
	path := filepath.Join(dir, "prebuilt.tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, sum256(data)
}

func prebuiltHarness(t *testing.T, p prebuiltParts, opts ...hopt) (*harness, string, string) {
	t.Helper()
	h := newHarness(t, opts...)
	path, digest := p.write(t, t.TempDir())
	h.req = Request{Prebuilt: path}
	return h, path, digest
}

func TestPrebuiltIsVerifiedPlacedAndActivated(t *testing.T) {
	p := newPrebuilt()
	h, path, digest := prebuiltHarness(t, p)
	h.req.PrebuiltSHA256 = digest
	res, err := h.build()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restored || !res.Switched || res.BuildID != testID || res.PreviousBuild != oldID {
		t.Fatalf("result = %+v", res)
	}
	if h.link() != "builds/"+testID {
		t.Errorf("current = %q", h.link())
	}
	for _, c := range h.exec.all() {
		if filepath.Base(c.Path) == "cmake" || filepath.Base(c.Path) == "git" {
			t.Errorf("a restore compiles nothing and needs no source: %s", line(c))
		}
	}
	// The files are the exported ones, BUILD.json byte for byte, and nothing is left behind.
	for _, n := range binaries {
		if got := readFile(t, filepath.Join(h.root, "builds", testID, "bin", n)); got != string(p.bins[n]) {
			t.Errorf("%s differs from the exported file", n)
		}
	}
	if fi, err := os.Stat(filepath.Join(h.root, "builds", testID, "bin", "llama-server")); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("llama-server is not executable: %v %v", fi, err)
	}
	var onDisk BuildInfo
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(h.root, "builds", testID, "BUILD.json"))), &onDisk); err != nil || onDisk.BuildID != testID {
		t.Errorf("BUILD.json: %+v %v", onDisk, err)
	}
	if h.exists("builds/.prebuilt.tmp") || h.exists("builds/"+testID+".tmp") {
		t.Error("scratch directories were left behind")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the prebuilt file itself must stay")
	}
	if !strings.Contains(readFile(t, filepath.Join(h.root, "builds", testID, "build.log")), "restored from the prebuilt build") {
		t.Error("build.log says nothing about the restore")
	}
	// The smoke test ran on the restored files (--version and --list-devices).
	if !h.exec.ran("llama-server --version") || !h.exec.ran("llama-server --list-devices") {
		t.Errorf("no smoke test: %v", h.exec.lines())
	}
	if len(h.probe.completions) != 1 {
		t.Errorf("the post-switch check did not run: %v", h.probe.completions)
	}
}

func TestPrebuiltNoActivateOnlyPlacesIt(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	h.req.NoActivate = true
	res, err := h.build()
	if err != nil || res.Switched || !res.Restored {
		t.Fatalf("%v %+v", err, res)
	}
	if h.link() != "builds/"+oldID || !h.exists("builds/"+testID+"/BUILD.json") || h.agent.restarts != 0 {
		t.Errorf("link %q, restarts %d", h.link(), h.agent.restarts)
	}
}

func TestPrebuiltTamperingIsRefusedBeforeAnythingIsPlaced(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(p *prebuiltParts)
		cause string
	}{
		{"a program was changed after the export", func(p *prebuiltParts) {
			p.bins["llama-cli"] = append(p.bins["llama-cli"], []byte("tampered")...)
		}, "prebuilt_tampered"},
		{"BUILD.json records another digest", func(p *prebuiltParts) { p.info.SHA256["llama-bench"] = sum256([]byte("other")) }, "prebuilt_tampered"},
		{"BUILD.json is missing", func(p *prebuiltParts) { p.omit["BUILD.json"] = true }, "prebuilt_no_build_json"},
		{"BUILD.json is not JSON", func(p *prebuiltParts) { p.json = []byte("{not json") }, "prebuilt_bad_build_json"},
		{"BUILD.json has no digest for a program", func(p *prebuiltParts) { delete(p.info.SHA256, "llama-server") }, "prebuilt_no_sha"},
		{"a digest that is not a sha256", func(p *prebuiltParts) { p.info.SHA256["llama-server"] = "abc" }, "prebuilt_no_sha"},
		{"a program is missing", func(p *prebuiltParts) { p.omit["llama-cli"] = true }, "prebuilt_missing_file"},
		{"a program is a link", func(p *prebuiltParts) {
			p.omit["llama-cli"] = true
			p.extra = append(p.extra, tarEntry{name: "bin/llama-cli", typ: tar.TypeSymlink, linkname: "llama-server"})
		}, "prebuilt_missing_file"},
		{"the backend is not one", func(p *prebuiltParts) { p.info.Backend = "metal" }, "prebuilt_bad_build_json"},
		{"the build id does not match the commit", func(p *prebuiltParts) { p.info.BuildID = "000000000-cuda" }, "prebuilt_bad_build_json"},
		{"the commit is short", func(p *prebuiltParts) { p.info.Commit = testSHA[:9] }, "prebuilt_bad_build_json"},
		{"cuda without an architecture", func(p *prebuiltParts) { p.info.CudaArch = "" }, "prebuilt_bad_build_json"},
		{"an entry climbs out of the directory", func(p *prebuiltParts) {
			p.extra = append(p.extra, tarEntry{name: "../escaped.txt", body: "no"})
		}, "prebuilt_damaged"},
		{"a link points outside", func(p *prebuiltParts) {
			p.extra = append(p.extra, tarEntry{name: "evil", typ: tar.TypeSymlink, linkname: "../../etc"})
		}, "prebuilt_damaged"},
		{"an entry is written through a link", func(p *prebuiltParts) {
			p.extra = append(p.extra,
				tarEntry{name: "lib", typ: tar.TypeSymlink, linkname: "bin"},
				tarEntry{name: "lib/llama-server", body: "overwritten"})
		}, "prebuilt_damaged"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPrebuilt()
			tc.edit(&p)
			h, _, _ := prebuiltHarness(t, p)
			_, err := h.build()
			e := mustErr(t, err)
			if e.Code != doctor.KIT001 || e.Details["cause"] != tc.cause {
				t.Fatalf("got %s cause %v (%v), want LAI-KIT-001 %s", e.Code, e.Details["cause"], e, tc.cause)
			}
			if h.exists("builds/"+testID) || h.exists("builds/"+testID+".tmp") || h.exists("builds/.prebuilt.tmp") {
				t.Error("a refused prebuilt left something in builds/")
			}
			if h.link() != "builds/"+oldID || h.agent.restarts != 0 || len(h.audit.events) != 0 {
				t.Error("a refused prebuilt touched the active build")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(h.root), "escaped.txt")); err == nil {
				t.Error("a file was written outside the build")
			}
		})
	}
}

func TestPrebuiltDigestOfTheArchive(t *testing.T) {
	h, _, digest := prebuiltHarness(t, newPrebuilt())
	h.req.PrebuiltSHA256 = strings.ToUpper(digest)
	if _, err := h.build(); err != nil {
		t.Fatalf("an upper-case digest is the same digest: %v", err)
	}
	h2, _, _ := prebuiltHarness(t, newPrebuilt())
	h2.req.PrebuiltSHA256 = sum256([]byte("not this archive"))
	_, err := h2.build()
	if e := mustErr(t, err); e.Code != doctor.KIT001 || e.Details["cause"] != "prebuilt_digest" {
		t.Fatalf("got %v", e)
	}
	if h2.exists("builds/" + testID) {
		t.Error("a digest mismatch placed the build")
	}
}

func TestPrebuiltMustMatchTheRequest(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		ok   bool
	}{
		{"same backend and short commit", Request{Backend: "cuda", Commit: testSHA[:7]}, true},
		{"another backend", Request{Backend: "vulkan"}, false},
		{"another commit", Request{Commit: "1234567"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, path, _ := prebuiltHarness(t, newPrebuilt())
			h.req = tc.req
			h.req.Prebuilt = path
			_, err := h.build()
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if e := mustErr(t, err); e.Code != doctor.LLM018 || e.Details["cause"] != "prebuilt_mismatch" || h.exists("builds/"+testID) {
				t.Fatalf("got %v", e)
			}
		})
	}
}

func TestPrebuiltFileThatCannotBeOpened(t *testing.T) {
	h := newHarness(t)
	h.req = Request{Prebuilt: filepath.Join(t.TempDir(), "missing.tar.gz")}
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 || e.Details["cause"] != "prebuilt_unreadable" {
		t.Fatalf("got %v", e)
	}
}

func TestPrebuiltThatFailsTheSmokeTestIsNotActivated(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	h.version = "version: 6000 (0000000)\n"
	_, err := h.build()
	if e := mustErr(t, err); e.Code != doctor.LLM019 || e.Stage != StageSmoke {
		t.Fatalf("got %v", e)
	}
	if h.link() != "builds/"+oldID || h.exists("builds/"+testID) || !h.exists("builds/"+testID+".tmp.failed/build.log") {
		t.Errorf("link %q; the failed build must be kept as .tmp.failed", h.link())
	}
}

func TestPrebuiltThatFailsAfterTheSwitchIsRolledBack(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	h.probe.completeErr = errors.New("500")
	_, err := h.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM019 || e.Details["rolled_back"] != true || h.link() != "builds/"+oldID {
		t.Fatalf("got %v, link %q", e, h.link())
	}
	if h.agent.restarts != 2 {
		t.Errorf("restarts = %d, want the switch and the rollback", h.agent.restarts)
	}
}

func TestPrebuiltDryRunOnlyVerifies(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	h.req.DryRun = true
	res, err := h.build()
	if err != nil || !res.DryRun || !res.Restored || res.BuildID != testID {
		t.Fatalf("%v %+v", err, res)
	}
	if h.exists("builds/"+testID) || h.exists("builds/.prebuilt.tmp") || h.exec.ran("llama-server") {
		t.Error("a dry run placed or ran something")
	}
	p := newPrebuilt()
	p.bins["llama-server"] = []byte("tampered")
	h2, _, _ := prebuiltHarness(t, p)
	h2.req.DryRun = true
	if _, err := h2.build(); mustErr(t, err).Code != doctor.KIT001 {
		t.Fatal("a dry run of a tampered prebuilt must fail")
	}
}

func TestPrebuiltCannotReplaceTheBuildInUseWithoutActivation(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt(), func(h *harness) { h.prevID = testID })
	h.req.NoActivate = true
	_, err := h.build()
	if e := mustErr(t, err); e.Details["cause"] != "active_build" {
		t.Fatalf("got %v", e)
	}
}

func TestPrebuiltIsRecordedAndExported(t *testing.T) {
	h, _, _ := prebuiltHarness(t, newPrebuilt())
	nc, ex := &fakeNodeConfig{}, &fakeExporter{}
	h.opts.NodeConfig, h.opts.Exporter = nc, ex
	h.opts.Node = nil
	if _, err := h.build(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nc.commits, []string{testSHA}) || !reflect.DeepEqual(ex.built, []string{testID + "@" + testID}) {
		t.Errorf("recorded %v, exported %v", nc.commits, ex.built)
	}
}

// ---- the compile runs as an unprivileged user --------------------------------------------

// ownerFS is the real filesystem that records Chown and Rename in a shared log instead of changing
// owners (the tests run as any user).
type ownerFS struct {
	OSFS
	log     *eventLog
	root    string
	chownFn func(path string, uid, gid int) error
}

func (f *ownerFS) rel(p string) string {
	if r, err := filepath.Rel(f.root, p); err == nil {
		return r
	}
	return p
}

func (f *ownerFS) Chown(path string, uid, gid int) error {
	f.log.add("chown %s %d:%d", f.rel(path), uid, gid)
	if f.chownFn != nil {
		return f.chownFn(path, uid, gid)
	}
	return nil
}

func (f *ownerFS) Rename(oldpath, newpath string) error {
	f.log.add("rename %s %s", f.rel(oldpath), f.rel(newpath))
	return f.OSFS.Rename(oldpath, newpath)
}

func unprivilegedKit(t *testing.T) (*kitEnv, *ownerFS) {
	t.Helper()
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	fsys := &ownerFS{OSFS: OSFS{}, log: k.events, root: k.harness.root}
	k.opts.FS = fsys
	k.opts.CompileUser = "" // the default: localai
	k.opts.LookupUser = func(name string) (int, int, error) {
		if name != "localai" {
			return 0, 0, fmt.Errorf("unknown user %s", name)
		}
		return 1234, 2345, nil
	}
	return k, fsys
}

func TestCompileRunsAsLocalaiAndTheTreeGoesBackToRoot(t *testing.T) {
	k, _ := unprivilegedKit(t)
	res, err := k.build()
	if err != nil {
		t.Fatal(err)
	}
	// Both compile steps run through setpriv with the user and group, no supplementary groups, and
	// the real program after them.
	if len(k.setprivCmd) != 2 {
		t.Fatalf("setpriv ran %d times, want 2 (configure and build): %v", len(k.setprivCmd), k.events.all())
	}
	want := []string{"--reuid", "localai", "--regid", "localai", "--clear-groups", "/usr/bin/cmake"}
	for i, c := range k.setprivCmd {
		if !reflect.DeepEqual(c.Args[:6], want) {
			t.Errorf("setpriv argv %d = %v, want it to start with %v", i, c.Args[:6], want)
		}
	}
	if k.setprivCmd[0].Args[6] != "-S" || k.setprivCmd[1].Args[6] != "--build" {
		t.Errorf("the programs are %v and %v", k.setprivCmd[0].Args[6:8], k.setprivCmd[1].Args[6:8])
	}
	for _, e := range k.events.all() {
		if strings.HasPrefix(e, "exec cmake -S") || strings.HasPrefix(e, "exec cmake --build") {
			t.Errorf("cmake ran directly: %s", e)
		}
	}

	id := testID
	tmp := "builds/" + id + ".tmp"
	ev := k.events
	handOver := ev.index("chown " + tmp + " 1234:2345")
	handOverBin := ev.index("chown " + tmp + "/bin 1234:2345")
	configure := ev.index("exec setpriv --reuid localai --regid localai --clear-groups /usr/bin/cmake -S")
	build := ev.index("exec setpriv --reuid localai --regid localai --clear-groups /usr/bin/cmake --build")
	back := ev.index("chown " + tmp + " 0:0")
	backBin := ev.index("chown " + tmp + "/build/bin/llama-server 0:0")
	rename := ev.index("rename " + tmp + " builds/" + id)
	for name, i := range map[string]int{"hand over": handOver, "hand over bin": handOverBin, "configure": configure, "build": build, "back": back, "back build tree": backBin, "rename": rename} {
		if i < 0 {
			t.Fatalf("no %q event in %v", name, ev.all())
		}
	}
	if !(handOver < configure && handOverBin < configure && configure < build && build < back && back < backBin && backBin < rename) {
		t.Errorf("wrong order: hand over %d/%d, configure %d, build %d, back %d/%d, rename %d\n%v",
			handOver, handOverBin, configure, build, back, backBin, rename, ev.all())
	}
	for _, e := range ev.all() {
		if strings.HasPrefix(e, "chown builds/"+id+" ") {
			t.Errorf("the placed build was chowned after the rename: %s", e)
		}
	}
	// Nothing owned by localai is ever run: smoke and activation come after the tree is root's.
	smoke := ev.index("exec llama-server --version")
	if smoke < back {
		t.Errorf("llama-server ran (%d) before the tree went back to root (%d)", smoke, back)
	}
	if res.BuildID != id {
		t.Errorf("result = %+v", res)
	}
}

func TestCompileUserProblemsAreBuildFailuresThatChangeNothing(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(k *kitEnv, fsys *ownerFS)
		cause string
	}{
		{"the user does not exist", func(k *kitEnv, _ *ownerFS) {
			k.opts.LookupUser = func(string) (int, int, error) { return 0, 0, errors.New("user: unknown user localai") }
		}, "no_compile_user"},
		{"setpriv is not installed", func(k *kitEnv, _ *ownerFS) { k.tools["setpriv"] = false }, "no_setpriv"},
		{"the tree cannot be handed over", func(_ *kitEnv, fsys *ownerFS) {
			fsys.chownFn = func(string, int, int) error { return errors.New("operation not permitted") }
		}, "chown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, fsys := unprivilegedKit(t)
			tc.edit(k, fsys)
			_, err := k.build()
			e := mustErr(t, err)
			if e.Code != doctor.LLM018 || e.Details["cause"] != tc.cause {
				t.Fatalf("got %v", e)
			}
			if k.compiled() || len(k.setprivCmd) != 0 {
				t.Error("a compile ran without the unprivileged user")
			}
			if k.link() != "builds/"+oldID {
				t.Error("the active build changed")
			}
		})
	}
}

func TestCompileAsCallerSkipsTheDrop(t *testing.T) {
	k, _ := unprivilegedKit(t)
	k.opts.CompileUser = CompileAsCaller
	if _, err := k.build(); err != nil {
		t.Fatal(err)
	}
	if len(k.setprivCmd) != 0 || k.events.index("chown ") >= 0 {
		t.Errorf("CompileAsCaller must not drop or chown: %v", k.events.all())
	}
}

func TestAFailedUnprivilegedCompileKeepsARootOwnedFailedDirectory(t *testing.T) {
	k, _ := unprivilegedKit(t)
	k.buildRes = exitWith(1, "make: *** error")
	_, err := k.build()
	e := mustErr(t, err)
	if e.Code != doctor.LLM018 || e.Details["command"] != "cmake" {
		t.Fatalf("got %v (the error names cmake, not setpriv)", e)
	}
	if !k.exists("builds/" + testID + ".tmp.failed/build.log") {
		t.Error("the failed build was not kept")
	}
	failed := k.events.index("exec setpriv --reuid localai --regid localai --clear-groups /usr/bin/cmake --build")
	back := k.events.index("chown builds/" + testID + ".tmp 0:0")
	if failed < 0 || back < failed {
		t.Errorf("the failed tree must go back to root after the compile: %v", k.events.all())
	}
}

func TestACompileUserCannotPlantALinkThatRootFollows(t *testing.T) {
	k, _ := unprivilegedKit(t)
	victim := t.TempDir()
	out := filepath.Join(k.harness.root, "builds", testID+".tmp")
	// While "compiling", the build replaces the output's bin directory with a link into a directory
	// root must not write to.
	k.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "setpriv" && len(c.Args) > 6 && c.Args[6] == "--build" {
			_ = os.RemoveAll(filepath.Join(out, "bin"))
			if err := os.Symlink(victim, filepath.Join(out, "bin")); err != nil {
				t.Error(err)
			}
		}
		return execx.Result{}, nil, false
	})
	if _, err := k.build(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 0 {
		t.Fatalf("root wrote through the planted link: %v", entries)
	}
	fi, err := os.Lstat(filepath.Join(k.harness.root, "builds", testID, "bin"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("bin must be a real directory, got %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(k.harness.root, "builds", testID, "bin", "llama-server")); err != nil {
		t.Error("the programs are not in the build")
	}
}

func TestInstallRefusesAProgramThatIsALink(t *testing.T) {
	k, _ := unprivilegedKit(t)
	k.opts.CompileUser = CompileAsCaller
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	k.exec.on(func(c execx.Cmd) (execx.Result, error, bool) {
		if filepath.Base(c.Path) == "cmake" && len(c.Args) > 0 && c.Args[0] == "--build" {
			res, err, _ := k.harness.world(c)
			dir := filepath.Join(c.Args[1], "bin", "llama-cli")
			_ = os.Remove(dir)
			_ = os.Symlink(secret, dir)
			return res, err, true
		}
		return execx.Result{}, nil, false
	})
	_, err := k.build()
	if e := mustErr(t, err); e.Code != doctor.LLM018 || e.Details["cause"] != "not_regular" {
		t.Fatalf("got %v", e)
	}
}

// ---- archive extraction ------------------------------------------------------------------

func TestExtractArchiveRefusesLinkChains(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
	}{
		{"a link that climbs out", []tarEntry{{name: "a", typ: tar.TypeSymlink, linkname: "../x"}}},
		{"an absolute link", []tarEntry{{name: "a", typ: tar.TypeSymlink, linkname: "/etc"}}},
		{"an empty link", []tarEntry{{name: "a", typ: tar.TypeSymlink, linkname: ""}}},
		{"a link that is not in cleaned form", []tarEntry{{name: "a", typ: tar.TypeSymlink, linkname: "b/../c"}}},
		{"a chain: a link to . then a link up from inside it", []tarEntry{
			{name: "a", typ: tar.TypeSymlink, linkname: "."},
			{name: "a/b", typ: tar.TypeSymlink, linkname: ".."},
		}},
		{"the chain that reaches outside through a middle link", []tarEntry{
			{name: "d/", typ: tar.TypeDir},
			{name: "d/up", typ: tar.TypeSymlink, linkname: ".."},
			{name: "x", typ: tar.TypeSymlink, linkname: "d/up/.."},
		}},
		{"a file written through a link to a directory", []tarEntry{
			{name: "d/", typ: tar.TypeDir},
			{name: "l", typ: tar.TypeSymlink, linkname: "d"},
			{name: "l/f", body: "x"},
		}},
		{"a directory entry through a link", []tarEntry{
			{name: "d/", typ: tar.TypeDir},
			{name: "l", typ: tar.TypeSymlink, linkname: "d"},
			{name: "l/sub/", typ: tar.TypeDir},
		}},
		{"a file that replaces a link", []tarEntry{
			{name: "l", typ: tar.TypeSymlink, linkname: "t"},
			{name: "l", body: "x"},
		}},
		{"a directory that replaces a link", []tarEntry{
			{name: "l", typ: tar.TypeSymlink, linkname: "t"},
			{name: "l/", typ: tar.TypeDir},
		}},
		{"a hard link", []tarEntry{{name: "h", typ: tar.TypeLink, linkname: "x"}}},
		{"a device", []tarEntry{{name: "dev", typ: tar.TypeChar}}},
		{"an absolute name", []tarEntry{{name: "/etc/passwd", body: "x"}}},
		{"a name with ..", []tarEntry{{name: "a/../../b", body: "x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "tree")
			if err := os.MkdirAll(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			err := ExtractArchive(OSFS{}, strings.NewReader(string(makeTar(t, tc.entries, false))), dest, 0)
			if err == nil {
				t.Fatal("the archive must be refused")
			}
			// Nothing appeared next to the tree.
			if entries, _ := os.ReadDir(parent); len(entries) != 1 {
				t.Errorf("something was written outside the tree: %v", entries)
			}
		})
	}
}

func TestExtractArchiveKeepsGoodLinks(t *testing.T) {
	dest := t.TempDir()
	entries := []tarEntry{
		{name: "lib/", typ: tar.TypeDir},
		{name: "lib/libx.so.1", body: "elf"},
		{name: "lib/libx.so", typ: tar.TypeSymlink, linkname: "libx.so.1"},
		{name: "bin/", typ: tar.TypeDir},
		{name: "bin/tool", typ: tar.TypeSymlink, linkname: "../lib/libx.so.1"},
	}
	if err := ExtractArchive(OSFS{}, strings.NewReader(string(makeTar(t, entries, true))), dest, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(dest, "bin", "tool")); err != nil || got != "../lib/libx.so.1" {
		t.Errorf("link = %q %v", got, err)
	}
	if got := readFile(t, filepath.Join(dest, "bin", "tool")); got != "elf" {
		t.Errorf("through the link: %q", got)
	}
}

func TestCopyTreeSkipsLinksThatAreNotPlain(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"good": "a", "unclean": "a/../a", "up": "../x", "abs": "/etc"} {
		if err := os.Symlink(target, filepath.Join(src, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyTree(OSFS{}, src, dst, "", nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"good": true, "unclean": false, "up": false, "abs": false} {
		_, err := os.Lstat(filepath.Join(dst, name))
		if (err == nil) != want {
			t.Errorf("%s copied = %v, want %v", name, err == nil, want)
		}
	}
}

// ---- CUDA option B -----------------------------------------------------------------------

// redistKit puts a redistributable set on the kit root: cuda_nvcc as .tar.gz and libcublas as
// .tar.xz (served by the fake streaming xz). It returns the streamer and the archive paths.
func redistKit(t *testing.T, k *kitEnv, version string) (*fake.Streamer, map[string]string) {
	t.Helper()
	nvccTop := "cuda_nvcc-linux-x86_64-" + version + ".131-archive/"
	nvcc := makeTar(t, []tarEntry{
		{name: nvccTop, typ: tar.TypeDir},
		{name: nvccTop + "bin/", typ: tar.TypeDir},
		{name: nvccTop + "bin/nvcc", body: "#!nvcc\n", mode: 0o755},
		{name: nvccTop + "include/crt/host_defines.h", body: "//"},
	}, true)
	blasTop := "libcublas-linux-x86_64-" + version + ".5.1-archive/"
	blas := makeTar(t, []tarEntry{
		{name: blasTop, typ: tar.TypeDir},
		{name: blasTop + "lib/", typ: tar.TypeDir},
		{name: blasTop + "lib/libcublas.so.12", body: "blas"},
		{name: blasTop + "lib/libcublasLt.so.12", body: "blaslt"},
		{name: blasTop + "lib/libcublas.so", typ: tar.TypeSymlink, linkname: "libcublas.so.12"},
		{name: blasTop + "lib/libother.so", body: "not copied"},
		{name: blasTop + "lib/libcublas_far.so", typ: tar.TypeSymlink, linkname: "../elsewhere/libx.so"},
	}, false)
	cudart := makeTar(t, []tarEntry{
		{name: "cudart/lib/libcudart.so.12", body: "rt"},
	}, true)
	dir := filepath.Join(k.root, "kit", "cuda", version+"-amd64")
	paths := map[string]string{
		"cuda_nvcc.tar.gz":      string(nvcc),
		"libcublas.tar.xz":      string(blas), // the fake streamer plays the decompressed tar
		"cuda_cudart.tar.gz":    string(cudart),
		"redistrib_manifest.md": "ignored",
	}
	var files []string
	out := map[string]string{}
	for name, body := range paths {
		k.write(filepath.Join(dir, name), body)
		out[name] = filepath.Join(dir, name)
		if strings.HasSuffix(name, ".md") {
			continue
		}
		files = append(files, fmt.Sprintf(`{"path":%q,"sha256":%q}`, name, sum256([]byte(body))))
	}
	k.write(filepath.Join(dir, "MANIFEST.json"), `{"schema_version":1,"files":[`+strings.Join(files, ",")+`]}`)
	return fake.NewStreamer(fake.Script{Chunks: [][]byte{blas}}), out
}

func redistEnv(t *testing.T) (*kitEnv, *fake.Streamer) {
	t.Helper()
	k := newKitEnv(t)
	k.kitTarball(testSHA, kitTree(t, testSHA), "src", "")
	k.tools["nvcc"] = false
	st, _ := redistKit(t, k, "12.4")
	k.opts.Streamer = st
	return k, st
}

func TestCudaRedistributableIsWiredIntoTheBuild(t *testing.T) {
	k, st := redistEnv(t)
	res, err := k.build()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(k.scratch, "cuda-12.4")
	if res.Preflight == nil || res.Preflight.Redist == nil || res.Preflight.Redist.Version != "12.4" {
		t.Fatalf("preflight = %+v", res.Preflight)
	}
	// The three archives of the set are merged into one tree under the scratch directory, with the
	// top-level directories dropped.
	for _, f := range []string{"bin/nvcc", "include/crt/host_defines.h", "lib/libcublas.so.12", "lib/libcudart.so.12"} {
		if !fileExists(filepath.Join(root, f)) {
			t.Errorf("%s is not in the redistributable tree", f)
		}
	}
	// cmake is pointed at it.
	var configure []string
	for _, c := range k.exec.all() {
		if filepath.Base(c.Path) == "cmake" && len(c.Args) > 0 && c.Args[0] == "-S" {
			configure = c.Args
		}
	}
	for _, want := range []string{"-DCUDAToolkit_ROOT=" + root, "-DCMAKE_CUDA_COMPILER=" + filepath.Join(root, "bin", "nvcc"), "-DCMAKE_BUILD_RPATH=$ORIGIN/../lib", "-DGGML_CUDA=ON"} {
		if !contains(configure, want) {
			t.Errorf("configure lacks %s: %v", want, configure)
		}
	}
	if !contains(res.BuildJSON.CMakeFlags, "-DCUDAToolkit_ROOT="+root) {
		t.Errorf("BUILD.json does not record the toolkit root: %v", res.BuildJSON.CMakeFlags)
	}
	if res.BuildJSON.NVCC != "12.4.131" {
		t.Errorf("BUILD.json nvcc = %q", res.BuildJSON.NVCC)
	}
	// libcudart and libcublas go into the build's lib/, next to bin/, the run path of the programs.
	lib := filepath.Join(k.harness.root, "builds", testID, "lib")
	for _, f := range []string{"libcublas.so.12", "libcublasLt.so.12", "libcudart.so.12"} {
		if got := readFile(t, filepath.Join(lib, f)); got == "" {
			t.Errorf("%s not copied", f)
		}
	}
	if got, err := os.Readlink(filepath.Join(lib, "libcublas.so")); err != nil || got != "libcublas.so.12" {
		t.Errorf("libcublas.so link = %q %v", got, err)
	}
	for _, f := range []string{"libother.so", "libcublas_far.so"} {
		if fileExists(filepath.Join(lib, f)) {
			t.Errorf("%s must not be copied", f)
		}
	}
	// The xz archive was read through the streaming runner, never through a shell or a file.
	calls := st.Calls()
	if len(calls) != 1 || filepath.Base(calls[0].Path) != "xz" ||
		!reflect.DeepEqual(calls[0].Args[:3], []string{"--decompress", "--stdout", "--"}) {
		t.Errorf("xz calls = %+v", calls)
	}
}

func TestACompleteRedistributableTreeIsReused(t *testing.T) {
	k, st := redistEnv(t)
	if _, err := k.build(); err != nil {
		t.Fatal(err)
	}
	// Build again (another backend id is not needed: remove the placed build and the source).
	_ = os.RemoveAll(filepath.Join(k.harness.root, "builds", testID))
	m := New(k.opts)
	if _, err := m.Build(context.Background(), k.req); err != nil {
		t.Fatal(err)
	}
	if n := len(st.Calls()); n != 1 {
		t.Errorf("the redistributable was unpacked %d times, want once", n)
	}
}

func TestCudaRedistributableProblems(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(t *testing.T, k *kitEnv)
		code  string
		cause string
	}{
		{"an archive does not match the recorded digest", func(t *testing.T, k *kitEnv) {
			k.write(filepath.Join(k.root, "kit", "cuda", "12.4-amd64", "cuda_nvcc.tar.gz"), "damaged")
		}, doctor.KIT001, "redist_damaged"},
		{"xz is not installed", func(t *testing.T, k *kitEnv) { k.tools["xz"] = false }, doctor.LLM018, "redist_unpack"},
		{"xz fails", func(t *testing.T, k *kitEnv) {
			k.opts.Streamer = fake.NewStreamer(fake.Script{ExitCode: 2, Stderr: "xz: truncated"})
		}, doctor.LLM018, "redist_unpack"},
		{"the set holds no nvcc", func(t *testing.T, k *kitEnv) {
			dir := filepath.Join(k.root, "kit", "cuda", "12.4-amd64")
			body := string(makeTar(t, []tarEntry{{name: "top/readme", body: "x"}}, true))
			k.write(filepath.Join(dir, "cuda_nvcc.tar.gz"), body)
			k.write(filepath.Join(dir, "MANIFEST.json"), fmt.Sprintf(`{"files":[{"path":"cuda_nvcc.tar.gz","sha256":%q}]}`, sum256([]byte(body))))
		}, doctor.LLM018, "redist_no_nvcc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, _ := redistEnv(t)
			tc.edit(t, k)
			_, err := k.build()
			e := mustErr(t, err)
			if e.Code != tc.code || e.Details["cause"] != tc.cause {
				t.Fatalf("got %s %v, want %s %s", e.Code, e, tc.code, tc.cause)
			}
			if k.compiled() {
				t.Error("a build ran with a bad redistributable")
			}
			entries, _ := os.ReadDir(k.scratch)
			for _, en := range entries {
				t.Errorf("a half-unpacked tree was left in the scratch directory: %s", en.Name())
			}
		})
	}
}

func TestCudaRedistributableMustFitTheDriver(t *testing.T) {
	k, _ := redistEnv(t)
	k.driverOut = "CUDA Version: 11.8\n"
	rep := k.manager().PreflightReport(context.Background(), k.req)
	got := failedIDs(rep)
	if len(got) != 1 || got[0] != "backend:"+doctor.KIT011 || rep.Redist != nil {
		t.Fatalf("failed = %v, redist %+v", got, rep.Redist)
	}
	// Another architecture's set is not offered.
	k2 := newKitEnv(t)
	k2.tools["nvcc"] = false
	redistKit(t, k2, "12.4")
	k2.opts.Arch = "arm64"
	rep = k2.manager().PreflightReport(context.Background(), k2.req)
	if rep.Redist != nil {
		t.Errorf("an amd64 set was offered to arm64: %+v", rep.Redist)
	}
}

func TestNvccOnTheSystemWinsOverTheRedistributable(t *testing.T) {
	k := newKitEnv(t)
	redistKit(t, k, "12.4")
	rep := k.manager().PreflightReport(context.Background(), k.req)
	if rep.Redist != nil {
		t.Error("the system's nvcc is installed, the redistributable must not be used")
	}
}

func TestWithCudaRootDoesNotChangeTheOriginalPlan(t *testing.T) {
	in := Inputs{Commit: testSHA, ShortCommit: testShort9, Backend: BackendCUDA, CudaArch: "75", Jobs: 4, Activate: true}
	p := NewPlan("/opt/localai/llama.cpp", in, testSHA)
	n := len(p.CMakeFlags)
	q := p.WithCudaRoot("/var/lib/localai/build/cuda-12.4")
	if len(p.CMakeFlags) != n || len(q.CMakeFlags) != n+3 || len(q.ConfigureArgs) != len(p.ConfigureArgs)+3 {
		t.Errorf("flags %d -> %d, args %d -> %d", n, len(q.CMakeFlags), len(p.ConfigureArgs), len(q.ConfigureArgs))
	}
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
