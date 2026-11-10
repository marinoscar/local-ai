package host

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/kit/kittest"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// scriptRunner answers every program the host runs. calls is what it was asked, in order.
type scriptRunner struct {
	mu    sync.Mutex
	calls []execx.Cmd
	fn    func(c execx.Cmd) (execx.Result, error)
}

func (s *scriptRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	if s.fn == nil {
		return execx.Result{}, nil
	}
	return s.fn(c)
}
func (s *scriptRunner) Calls() []execx.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]execx.Cmd(nil), s.calls...)
}

// recordingInstaller stands in for kiwix.Install and piper.Install (the real ones unpack a program).
type recordingInstaller struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingInstaller) install(ctx context.Context, tarball, sha, version string) error {
	r.mu.Lock()
	r.calls = append(r.calls, filepath.Base(tarball)+" "+version)
	r.mu.Unlock()
	return nil
}

func tarGz(t *testing.T, name string, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type fixture struct {
	t       *testing.T
	kit     *kittest.Kit
	opt     string
	runner  *scriptRunner
	kiwix   *recordingInstaller
	piper   *recordingInstaller
	host    *Host
	node    kit.NodeFacts
	running map[string]bool
}

// fixPiper gives the piper item the release URL its version is read from.
func fixPiper(k *kittest.Kit) {
	k.Item("tools/piper", "kit-piper", map[string][]byte{"piper_linux_x86_64.tar.gz": []byte("piper amd64\n")}, func(m *manifest.Manifest) {
		m.Files[0].URL = "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz"
	})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k := kittest.New(t)
	n := kittest.AiN3()
	k.Provision(n)
	// pmtiles must be a real archive: the installer extracts it.
	k.Item("tools/pmtiles", "kit-pmtiles", map[string][]byte{"go-pmtiles_1.0_Linux_x86_64.tar.gz": tarGz(t, "pmtiles", "#!/bin/sh\necho pmtiles 1.0\n")})
	fixPiper(k)
	f := &fixture{t: t, kit: k, opt: filepath.Join(t.TempDir(), "opt"), kiwix: &recordingInstaller{}, piper: &recordingInstaller{}, node: n.Facts(), running: map[string]bool{}}
	f.runner = &scriptRunner{fn: func(c execx.Cmd) (execx.Result, error) {
		if filepath.Base(c.Path) == "pmtiles" && len(c.Args) == 1 && c.Args[0] == "version" {
			return execx.Result{Stdout: []byte("pmtiles 1.0\n")}, nil
		}
		return execx.Result{}, nil
	}}
	f.build(func() []kit.StatusRoot { return []kit.StatusRoot{{ID: "internal", Path: k.Root, Internal: true}} })
	return f
}

func (f *fixture) build(roots func() []kit.StatusRoot) {
	f.host = New(Config{
		FS: kit.OSFS{}, Runner: f.runner, OptRoot: f.opt, Roots: roots,
		Node:  func() *kit.NodeFacts { n := f.node; return &n },
		Kiwix: f.kiwix.install, Piper: f.piper.install, Keys: f.kit.Keys,
		Now:           func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) },
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
		AptSourceList: filepath.Join(f.t.TempDir(), "localai-ark.list"),
		ServiceActive: func(_ context.Context, s string) bool { return f.running[s] },
		Restart:       func(context.Context, string) error { return errors.New("restart is not part of this test") },
		Aiagent:       "/opt/localai/bin/aiagent",
	})
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestTheHostSeesTheKitAsTheVerifierDoes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	src, version, err := f.host.Tool(ctx, kit.ToolKiwix)
	if err != nil || version != "3.7" || src.Root != f.kit.Root || src.KitDir != f.kit.Dir {
		t.Fatalf("kiwix: %+v %q %v", src, version, err)
	}
	if _, v, err := f.host.Tool(ctx, kit.ToolPiper); err != nil || v == "" {
		t.Fatalf("piper: %q %v", v, err)
	}
	_, pkgs, err := f.host.Packages(ctx)
	if err != nil || !pkgs["cmake"] || !pkgs["build-essential"] || !pkgs["nvidia-cuda-toolkit"] && false {
		t.Fatalf("packages %v %v", pkgs, err)
	}
	t.Run("a file that is smaller than its manifest says is damaged", func(t *testing.T) {
		f := newFixture(t)
		f.kit.Truncate("tools/piper/piper_linux_x86_64.tar.gz", 3)
		if _, _, err := f.host.Tool(ctx, kit.ToolPiper); !errors.Is(err, ensure.ErrKitDamaged) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an item with no manifest is missing", func(t *testing.T) {
		f := newFixture(t)
		f.kit.Remove("tools/pmtiles/MANIFEST.json")
		if _, _, err := f.host.Tool(ctx, kit.ToolPmtiles); !errors.Is(err, ensure.ErrKitMissing) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a root without a kit is skipped and a drive that has it is used", func(t *testing.T) {
		f := newFixture(t)
		empty := t.TempDir()
		f.build(func() []kit.StatusRoot {
			return []kit.StatusRoot{{ID: "internal", Path: empty, Internal: true}, {ID: "ark-3f9c0a12", Path: f.kit.Root}}
		})
		src, _, err := f.host.Tool(ctx, kit.ToolKiwix)
		if err != nil || src.Root != f.kit.Root {
			t.Fatalf("%+v %v", src, err)
		}
	})
	t.Run("a damaged copy on the internal root and a good one on the drive: the good one", func(t *testing.T) {
		f := newFixture(t)
		drive := kittest.NewAt(t, filepath.Join(t.TempDir(), "drive"))
		drive.Provision(kittest.AiN3())
		fixPiper(drive)
		f.kit.Truncate("tools/piper/piper_linux_x86_64.tar.gz", 3)
		f.build(func() []kit.StatusRoot {
			return []kit.StatusRoot{{ID: "internal", Path: f.kit.Root, Internal: true}, {ID: "ark-3f9c0a12", Path: drive.Root}}
		})
		src, _, err := f.host.Tool(ctx, kit.ToolPiper)
		if err != nil || src.Root != drive.Root {
			t.Fatalf("%+v %v", src, err)
		}
	})
	t.Run("for a computer whose release is unknown the kit has no packages", func(t *testing.T) {
		f := newFixture(t)
		f.node.Codename = ""
		if _, _, err := f.host.Packages(ctx); !errors.Is(err, ensure.ErrKitMissing) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("packages for another release are missing, not damaged", func(t *testing.T) {
		f := newFixture(t)
		f.node.Codename = "noble"
		if _, _, err := f.host.Packages(ctx); !errors.Is(err, ensure.ErrKitMissing) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestMissingPackagesAsksDpkgOnly(t *testing.T) {
	f := newFixture(t)
	f.runner.fn = func(c execx.Cmd) (execx.Result, error) {
		return execx.Result{Stdout: []byte("cmake\tii \t3.28\ngit\tun \t\n")}, nil
	}
	missing, err := f.host.MissingPackages(context.Background(), []string{"cmake", "git", "curl"})
	if err != nil || strings.Join(missing, ",") != "git,curl" {
		t.Fatalf("%v %v", missing, err)
	}
	calls := f.runner.Calls()
	if len(calls) != 1 || filepath.Base(calls[0].Path) != "dpkg-query" || calls[0].Args[0] != "-W" {
		t.Errorf("calls %+v", calls)
	}
}

func TestInstallToolRunsTheKitsInstallersFromTheVerifiedCopy(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	out, err := f.host.InstallTool(ctx, f.kit.Dir, kit.ToolKiwix)
	if err != nil || out.Version != "3.7" || out.Unchanged {
		t.Fatalf("kiwix %+v %v", out, err)
	}
	if len(f.kiwix.calls) != 1 || f.kiwix.calls[0] != "kiwix-tools_linux-x86_64-3.7.tar.gz 3.7" {
		t.Errorf("kiwix calls %v", f.kiwix.calls)
	}
	if _, err := f.host.InstallTool(ctx, f.kit.Dir, kit.ToolPiper); err != nil || len(f.piper.calls) != 1 {
		t.Fatalf("piper %v %v", err, f.piper.calls)
	}
	// pmtiles is installed by the kit package itself: a real archive, a smoke test, the current link.
	pm, err := f.host.InstallTool(ctx, f.kit.Dir, kit.ToolPmtiles)
	if err != nil || pm.Version != "1.0" {
		t.Fatalf("pmtiles %+v %v", pm, err)
	}
	link, err := os.Readlink(filepath.Join(f.opt, "pmtiles", "current"))
	if err != nil || link != "1.0" {
		t.Fatalf("current -> %q %v", link, err)
	}
	if got := f.host.ToolVersion(kit.ToolPmtiles); got != "1.0" {
		t.Errorf("ToolVersion %q", got)
	}
	again, err := f.host.InstallTool(ctx, f.kit.Dir, kit.ToolPmtiles)
	if err != nil || !again.Unchanged {
		t.Errorf("a second install must change nothing: %+v %v", again, err)
	}
	smoke := 0
	for _, c := range f.runner.Calls() {
		if filepath.Base(c.Path) == "pmtiles" {
			smoke++
			if strings.Join(c.Args, " ") != "version" {
				t.Errorf("smoke argv %v", c.Args)
			}
		}
	}
	if smoke == 0 {
		t.Error("the new program was never smoke tested")
	}
}

func TestTheAgentIsNeverInstalledByTheEngine(t *testing.T) {
	f := newFixture(t)
	for _, tool := range []string{kit.ToolAgent, "sh", "../kiwix-tools", ""} {
		_, err := f.host.InstallTool(context.Background(), f.kit.Dir, tool)
		if !errors.Is(err, ensure.ErrNotAllowed) {
			t.Errorf("%q: %v", tool, err)
		}
	}
	if err := f.host.RestartService(context.Background(), "llama"); !errors.Is(err, ensure.ErrNotAllowed) {
		t.Errorf("restart llama: %v", err)
	}
	if len(f.runner.Calls()) != 0 || len(f.kiwix.calls)+len(f.piper.calls) != 0 {
		t.Error("something ran")
	}
}

func TestABadChecksumInstallsNothing(t *testing.T) {
	// The file has the size the manifest records (a quick verification cannot tell), but one byte is
	// flipped. The executor asks the kit, then the installer hashes the file against the manifest.
	f := newFixture(t)
	f.kit.Corrupt("tools/kiwix-tools/kiwix-tools_linux-x86_64-3.7.tar.gz")
	f.kit.Corrupt("tools/pmtiles/go-pmtiles_1.0_Linux_x86_64.tar.gz")
	aud := &memAudit{}
	ex := &ensure.Executor{Installer: f.host, Kit: f.host, Audit: aud, Clock: realClock{}}
	for _, st := range []ensure.Step{
		{ID: ensure.IDKiwix, Kind: ensure.KindTool, Target: kit.ToolKiwix},
		{ID: ensure.IDPmtiles, Kind: ensure.KindTool, Target: kit.ToolPmtiles},
	} {
		res := ex.Run(context.Background(), st, audit.System(), "run-1")
		if res.Step.Status != ensure.StatusFailed || res.Step.Code != doctor.KIT001 {
			t.Fatalf("%s: %+v", st.ID, res.Step)
		}
	}
	if len(f.kiwix.calls) != 0 {
		t.Errorf("kiwix was handed a damaged file: %v", f.kiwix.calls)
	}
	if _, err := os.Lstat(filepath.Join(f.opt, "pmtiles")); !os.IsNotExist(err) {
		t.Errorf("pmtiles was extracted: %v", err)
	}
	if len(aud.events) != 2 || aud.events[0].Code != doctor.KIT001 {
		t.Errorf("audit %+v", aud.events)
	}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type memAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (m *memAudit) Write(e audit.Event) error {
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
	return nil
}

func TestInstallPackagesGoesThroughTheKitsOfflineApt(t *testing.T) {
	f := newFixture(t)
	done := false
	f.runner.fn = func(c execx.Cmd) (execx.Result, error) {
		base := filepath.Base(c.Path)
		switch {
		case base == "apt-get" && len(c.Args) > 1 && c.Args[0] == "install" && c.Args[1] == "-s":
			return execx.Result{Stdout: []byte("Inst cmake (3.28.3-1 local [amd64])\n")}, nil
		case base == "apt-get" && len(c.Args) > 0 && c.Args[0] == "install":
			done = true
		case base == "dpkg-query" && done:
			return execx.Result{Stdout: []byte("cmake\tii \t3.28.3-1\n")}, nil
		}
		return execx.Result{}, nil
	}
	out, err := f.host.InstallPackages(context.Background(), f.kit.Dir, []string{"cmake"})
	if err != nil || len(out.Installed) != 1 || out.Installed[0] != "cmake" {
		t.Fatalf("%+v %v", out, err)
	}
	var install execx.Cmd
	for _, c := range f.runner.Calls() {
		if filepath.Base(c.Path) == "apt-get" && c.Args[0] == "install" && c.Args[1] != "-s" {
			install = c
		}
	}
	args := strings.Join(install.Args, " ")
	if !strings.HasSuffix(args, " cmake") || !strings.Contains(args, "Dir::Etc::sourceparts=-") || !strings.Contains(args, "--no-install-recommends") {
		t.Errorf("apt argv %q", args)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(f.host.apt().SourceList), "localai-ark.list")); !os.IsNotExist(err) {
		t.Errorf("the temporary apt source was left behind: %v", err)
	}
	// A package the kit does not carry is refused by the kit's own check, and nothing is installed.
	if _, err := f.host.InstallPackages(context.Background(), f.kit.Dir, []string{"not-in-the-kit"}); err == nil {
		t.Error("an unknown package was installed")
	}
	// A damaged package is refused.
	done = false // dpkg says cmake is not installed again
	f.kit.Corrupt("debs/resolute-amd64/pool/cmake_3.28.3-1_amd64.deb")
	var ke *kit.Error
	if _, err := f.host.InstallPackages(context.Background(), f.kit.Dir, []string{"cmake"}); !errors.As(err, &ke) || ke.Code() != doctor.KIT001 {
		t.Errorf("a damaged deb: %v", err)
	}
}

func TestWhisperRestoreAndBuildAreTheRebuildCommandWithTheExactArguments(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(f.kit.Dir, "prebuilt", "whisper.cpp-"+kittest.Hex40("whisper-pin")[:9]+"-cuda-resolute-amd64-sm75.tar.gz")
	if err := f.host.RestoreWhisper(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if err := f.host.BuildWhisper(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.runner.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	if calls[0].Path != "/opt/localai/bin/aiagent" || strings.Join(calls[0].Args, " ") != "ctl rebuild-whisper --prebuilt "+file+" --yes --json" {
		t.Errorf("restore: %s %v", calls[0].Path, calls[0].Args)
	}
	if strings.Join(calls[1].Args, " ") != "ctl rebuild-whisper --from-kit --yes --json" {
		t.Errorf("build: %v", calls[1].Args)
	}
	if calls[0].Timeout <= 0 || calls[1].Timeout <= calls[0].Timeout {
		t.Errorf("timeouts %v %v", calls[0].Timeout, calls[1].Timeout)
	}
	for _, bad := range []string{"relative.tar.gz", "/srv/../etc/x", "/srv//x"} {
		if err := f.host.RestoreWhisper(context.Background(), bad); !errors.Is(err, ensure.ErrNotAllowed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	t.Run("a failure keeps the doctor code the command printed", func(t *testing.T) {
		f.runner.fn = func(c execx.Cmd) (execx.Result, error) {
			return execx.Result{ExitCode: 1, Stderr: []byte("x [LAI-KIT-048] The ready-made build does not fit this computer.\n")}, nil
		}
		err := f.host.RestoreWhisper(context.Background(), file)
		var c interface{ Code() string }
		if !errors.As(err, &c) || c.Code() != doctor.KIT048 || !strings.Contains(err.Error(), "exit 1") {
			t.Errorf("%v", err)
		}
	})
}

func TestWhisperPrebuiltIsTheOneThatFitsThisComputer(t *testing.T) {
	f := newFixture(t)
	dir := kit.PrebuiltDir(f.kit.Root)
	name := kit.PrebuiltName(kit.ProgramWhisper, kittest.Hex40("whisper-pin"), "cuda", "resolute", "amd64", "75")
	body := []byte("a ready-made build")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := kit.PrebuiltRecord{File: name, Program: kit.ProgramWhisper, Commit: kittest.Hex40("whisper-pin"), Backend: "cuda", Codename: "resolute", Arch: "amd64",
		CudaArch: "75", SHA256: sha(body), Size: int64(len(body)), CMakeFlags: []string{}, BuiltAt: "2026-11-01T00:00:00Z"}
	m := kit.PrebuiltManifest{Schema: kit.PrebuiltSchema, SchemaVersion: kit.PrebuiltSchemaVersion, Files: []kit.PrebuiltRecord{rec}}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := f.host.WhisperPrebuilt(context.Background(), ensure.WhisperWant{Backend: "cuda"})
	if err != nil || got != filepath.Join(dir, name) {
		t.Fatalf("%q %v", got, err)
	}
	for _, w := range []ensure.WhisperWant{{Backend: "vulkan"}, {Backend: "cuda", Commit: kittest.Hex40("another")}} {
		if _, err := f.host.WhisperPrebuilt(context.Background(), w); !errors.Is(err, ensure.ErrKitMissing) {
			t.Errorf("%+v: %v", w, err)
		}
	}
	// And the kit's source is there for a build that needs a confirmation.
	ok, err := f.host.WhisperSource(context.Background(), ensure.WhisperWant{})
	if err != nil || !ok {
		t.Errorf("source %v %v", ok, err)
	}
	if ok, err := f.host.WhisperSource(context.Background(), ensure.WhisperWant{Commit: kittest.Hex40("elsewhere")}); ok || err == nil {
		t.Errorf("a commit the kit lacks: %v %v", ok, err)
	}
}

func TestWhisperInstalledMeansAProgramThatCanRun(t *testing.T) {
	f := newFixture(t)
	if f.host.WhisperInstalled() {
		t.Fatal("nothing is installed")
	}
	dir := filepath.Join(f.opt, "whisper.cpp", "current", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "whisper-server")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f.host.WhisperInstalled() {
		t.Error("a file that cannot run is not a build")
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if !f.host.WhisperInstalled() {
		t.Error("an executable file is")
	}
}

// ---- the whole engine over the kit fixture ----------------------------------------------------

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestTheEngineOverAKitFixtureInstallsWhatTheNodeIsMissingWithNoNetwork(t *testing.T) {
	f := newFixture(t)
	clock := fixedClock{time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	f.running["kiwix"] = true
	items := []string{"kit-kiwix-tools", "kit-pmtiles", "kit-piper", "kit-debs-resolute-amd64"}
	wants := ensure.WantsFor(items, ensure.Facts{Codename: "resolute", Arch: "amd64", Backend: "cuda"})
	// The fixture's closure holds two of the packages of the default set.
	wants.Packages, wants.Essential = []string{"cmake", "build-essential", "curl"}, []string{"cmake", "build-essential"}
	pkgs := wants.Packages
	f.runner.fn = func(c execx.Cmd) (execx.Result, error) {
		base := filepath.Base(c.Path)
		switch {
		case base == "pmtiles":
			return execx.Result{Stdout: []byte("pmtiles 1.0\n")}, nil
		case base == "dpkg-query":
			// Everything the default set names is missing, except what a pass installed already.
			var out strings.Builder
			for _, n := range c.Args[1:] {
				if installedPkgs[n] {
					out.WriteString(n + "\tii \t1\n")
				}
			}
			return execx.Result{Stdout: []byte(out.String())}, nil
		case base == "apt-get" && len(c.Args) > 1 && c.Args[0] == "install" && c.Args[1] == "-s":
			var out strings.Builder
			for _, n := range c.Args {
				if n == "cmake" {
					out.WriteString("Inst cmake (3.28.3-1 local [amd64])\n")
				}
				if n == "build-essential" {
					out.WriteString("Inst build-essential (12.10 local [amd64])\n")
				}
			}
			return execx.Result{Stdout: []byte(out.String())}, nil
		case base == "apt-get" && len(c.Args) > 0 && c.Args[0] == "install":
			for _, n := range c.Args {
				installedPkgs[n] = true
			}
		}
		return execx.Result{}, nil
	}
	installedPkgs = map[string]bool{}
	// The kiwix and piper installers of the fixture only record; the host's view of "installed" is the
	// current link, so they point it the way the real ones do.
	f.kiwix = &recordingInstaller{}
	link := func(tool, version string) kit.TarballInstaller {
		return func(ctx context.Context, tarball, sha, v string) error {
			dir := filepath.Join(f.opt, tool)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			_ = os.Remove(filepath.Join(dir, "current"))
			return os.Symlink(v, filepath.Join(dir, "current"))
		}
	}
	f.host.c.Kiwix, f.host.c.Piper = link("kiwix-tools", "3.7"), link("piper", "")
	f.host.tools.Kiwix, f.host.tools.Piper = f.host.c.Kiwix, f.host.c.Piper

	journal, _, err := ensure.OpenJournal(nil, filepath.Join(t.TempDir(), "ensure", ensure.JournalFile), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	aud := &memAudit{}
	svc := ensure.New(ensure.Deps{
		Settings: func() ensure.Settings { return ensure.DefaultSettings() },
		Wants:    func(context.Context) (ensure.Wants, error) { return wants, nil },
		Planner:  &ensure.Planner{Machine: f.host, Kit: f.host, Clock: clock},
		Executor: &ensure.Executor{Installer: f.host, Kit: f.host, Audit: aud, Clock: clock},
		Journal:  journal, Audit: aud, Clock: clock, PlanTTL: time.Nanosecond,
	})
	v, started, err := svc.Start(context.Background(), ensure.StartRequest{Lang: "en", Actor: audit.System()})
	if err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	if v.Progress.Total != 4 { // the packages and the three tools
		t.Errorf("plan: %+v", v.Progress)
	}
	svc.Wait()
	end := svc.View(context.Background(), "en")
	for _, s := range end.Steps {
		if s.Status != ensure.StatusOK && s.ID != ensure.IDRestartKiwix {
			t.Errorf("%s is %s (%s)", s.ID, s.Status, s.Error)
		}
	}
	if len(pkgs) == 0 || !installedPkgs["cmake"] {
		t.Errorf("packages: %v", installedPkgs)
	}
	for _, tool := range []string{kit.ToolPmtiles} {
		if f.host.ToolVersion(tool) != "1.0" {
			t.Errorf("%s is %q", tool, f.host.ToolVersion(tool))
		}
	}
	// A second pass changes nothing; the only thing left is the restart that needs the person's OK.
	before := len(f.runner.Calls())
	_, started, err = svc.Start(context.Background(), ensure.StartRequest{Lang: "en"})
	if err != nil || started {
		t.Fatalf("second pass: started=%v err=%v", started, err)
	}
	if end.State != ensure.StateNeedsYou || end.NextAction.Kind != ensure.NextConfirm || end.NextAction.Step != ensure.IDRestartKiwix {
		t.Errorf("end %s %+v", end.State, end.NextAction)
	}
	for _, c := range f.runner.Calls()[before:] {
		if filepath.Base(c.Path) == "apt-get" {
			t.Errorf("a second pass ran apt: %v", c.Args)
		}
	}
	// Nothing in this test could reach the network: the runner is the only way out and it ran no downloader.
	for _, c := range f.runner.Calls() {
		switch filepath.Base(c.Path) {
		case "curl", "wget", "git", "pip":
			t.Errorf("a program that fetches: %s", c.Path)
		}
	}
}

var installedPkgs map[string]bool
