package kit_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// putTool writes a tool directory <kitDir>/<dir> with its files and a MANIFEST.json that pins each
// file's real sha256.
func putTool(t testing.TB, kitDir, dir, version string, files map[string][]byte) string {
	t.Helper()
	d := filepath.Join(kitDir, filepath.FromSlash(dir))
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: "kit-" + filepath.Base(d), ItemKind: manifest.KindKit, CatalogVersion: "2026.11.02-1",
		Version: version, SortKey: version, State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
		DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		writeFile(t, filepath.Join(d, n), string(files[n]))
		m.TotalBytes += int64(len(files[n]))
		m.Files = append(m.Files, manifest.File{Kind: "archive", Path: n, SizeBytes: int64(len(files[n])), SHA256: hexSHA(files[n]), SHA256Source: "catalog"})
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(d), m); err != nil {
		t.Fatal(err)
	}
	return d
}

type tarItem struct {
	name string
	body string
	typ  byte
}

func makeTarGz(t testing.TB, items ...tarItem) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, it := range items {
		typ := it.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: it.name, Typeflag: typ, Mode: 0o644, Size: int64(len(it.body))}
		if typ == tar.TypeDir {
			h.Size, h.Mode = 0, 0o755
		}
		if typ == tar.TypeSymlink {
			h.Linkname, h.Size = it.body, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write([]byte(it.body))
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// recorder is a TarballInstaller that records what it was given.
type recorder struct {
	calls []string
	err   error
}

func (r *recorder) install(_ context.Context, tarball, sha, version string) error {
	r.calls = append(r.calls, strings.Join([]string{tarball, sha, version}, "|"))
	return r.err
}

func newTools(t testing.TB, r execx.Runner) (*kit.ToolInstaller, string) {
	t.Helper()
	opt := t.TempDir()
	return &kit.ToolInstaller{FS: kit.OSFS{}, Runner: r, LookPath: lookTo, Arch: "amd64", OptRoot: opt}, opt
}

func TestValidTool(t *testing.T) {
	for _, n := range []string{"kiwix-tools", "pmtiles", "piper", "agent"} {
		if !kit.ValidTool(n) {
			t.Errorf("%s is a tool", n)
		}
	}
	for _, n := range []string{"", "kiwix", "Piper", "llama", "../agent", "agent ", "kiwix-tools,piper"} {
		if kit.ValidTool(n) {
			t.Errorf("%q is not a tool", n)
		}
	}
	if !reflect.DeepEqual(kit.Tools, []string{"kiwix-tools", "pmtiles", "piper", "agent"}) {
		t.Errorf("tools %v", kit.Tools)
	}
}

func TestInstallKiwixFromTheKitManifest(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	amd := []byte("kiwix tarball for x86_64")
	arm := []byte("kiwix tarball for aarch64")
	dir := putTool(t, kitDir, "tools/kiwix-tools", "3.8.2", map[string][]byte{
		"kiwix-tools_linux-x86_64-3.8.2.tar.gz":  amd,
		"kiwix-tools_linux-aarch64-3.8.2.tar.gz": arm,
	})
	rec := &recorder{}
	ti, _ := newTools(t, nil)
	ti.Kiwix = rec.install
	var steps []string
	ti.Progress = func(s, tool string) { steps = append(steps, s+" "+tool) }
	res, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "kiwix-tools_linux-x86_64-3.8.2.tar.gz") + "|" + hexSHA(amd) + "|3.8.2"
	if !reflect.DeepEqual(rec.calls, []string{want}) {
		t.Fatalf("installer calls %q, want %q", rec.calls, want)
	}
	if res.Tool != "kiwix-tools" || res.Version != "3.8.2" || res.Unchanged || !strings.HasSuffix(res.Source, "x86_64-3.8.2.tar.gz") {
		t.Errorf("result %+v", res)
	}
	if !reflect.DeepEqual(steps, []string{"verify kiwix-tools", "install kiwix-tools"}) {
		t.Errorf("steps %v", steps)
	}
	// arm64 takes the other file.
	ti.Arch = "arm64"
	rec.calls = nil
	if _, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix); err != nil || !strings.Contains(rec.calls[0], "aarch64") || !strings.Contains(rec.calls[0], hexSHA(arm)) {
		t.Errorf("arm64: %v %v", rec.calls, err)
	}
}

func TestInstallTarballToolsRefuseWhatDoesNotMatchTheManifest(t *testing.T) {
	setup := func(t *testing.T) (kitDir, tarball string) {
		kitDir = filepath.Join(t.TempDir(), "kit")
		d := putTool(t, kitDir, "tools/kiwix-tools", "3.8.2", map[string][]byte{"kiwix-tools_linux-x86_64-3.8.2.tar.gz": []byte("good")})
		return kitDir, filepath.Join(d, "kiwix-tools_linux-x86_64-3.8.2.tar.gz")
	}
	check := func(t *testing.T, err error, kind error, code string) {
		t.Helper()
		var te *kit.ToolError
		if !errors.As(err, &te) || !errors.Is(err, kind) || te.Code() != code {
			t.Fatalf("want %v %s, got %v", kind, code, err)
		}
	}
	t.Run("a changed tarball is refused before the installer runs", func(t *testing.T) {
		kitDir, tarball := setup(t)
		os.WriteFile(tarball, []byte("evil"), 0o644)
		// Same size, different bytes: only the hash can tell.
		os.WriteFile(tarball, []byte("evil"), 0o644)
		rec := &recorder{}
		ti, _ := newTools(t, nil)
		ti.Kiwix = rec.install
		_, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
		check(t, err, kit.ErrToolDamaged, doctor.KIT001)
		if len(rec.calls) != 0 {
			t.Error("the installer must not see a tarball that fails its checksum")
		}
	})
	t.Run("no tool folder", func(t *testing.T) {
		ti, _ := newTools(t, nil)
		ti.Kiwix = (&recorder{}).install
		_, err := ti.Install(context.Background(), filepath.Join(t.TempDir(), "kit"), kit.ToolKiwix)
		check(t, err, kit.ErrToolNotInKit, doctor.KIT040)
	})
	t.Run("the manifest has no file for this architecture", func(t *testing.T) {
		kitDir, _ := setup(t)
		ti, _ := newTools(t, nil)
		ti.Arch = "arm64"
		ti.Kiwix = (&recorder{}).install
		_, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
		check(t, err, kit.ErrToolNotInKit, doctor.KIT040)
	})
	t.Run("a damaged manifest", func(t *testing.T) {
		kitDir, _ := setup(t)
		writeFile(t, filepath.Join(kitDir, "tools", "kiwix-tools", "MANIFEST.json"), "{broken")
		ti, _ := newTools(t, nil)
		ti.Kiwix = (&recorder{}).install
		_, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
		check(t, err, kit.ErrToolDamaged, doctor.KIT001)
	})
	t.Run("a missing tarball", func(t *testing.T) {
		kitDir, tarball := setup(t)
		os.Remove(tarball)
		ti, _ := newTools(t, nil)
		ti.Kiwix = (&recorder{}).install
		_, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
		check(t, err, kit.ErrToolDamaged, doctor.KIT001)
	})
	t.Run("no installer wired", func(t *testing.T) {
		kitDir, _ := setup(t)
		ti, _ := newTools(t, nil)
		_, err := ti.Install(context.Background(), kitDir, kit.ToolKiwix)
		check(t, err, kit.ErrToolInstall, doctor.KIT047)
	})
	t.Run("an unknown tool", func(t *testing.T) {
		ti, _ := newTools(t, nil)
		_, err := ti.Install(context.Background(), t.TempDir(), "llama")
		var te *kit.ToolError
		if !errors.As(err, &te) {
			t.Fatalf("%v", err)
		}
	})
}

// codedError is an installer failure that carries its own doctor code, like kiwix and piper do.
type codedError struct{ code string }

func (e *codedError) Error() string { return "boom" }
func (e *codedError) Code() string  { return e.code }

func TestInstallPiperUsesTheItemVersionAndKeepsTheInstallersCode(t *testing.T) {
	kitDir := filepath.Join(t.TempDir(), "kit")
	body := []byte("piper tarball")
	putTool(t, kitDir, "tools/piper", "2023.11.14-2", map[string][]byte{"piper_linux_x86_64.tar.gz": body, "piper_linux_aarch64.tar.gz": []byte("arm")})
	rec := &recorder{}
	ti, _ := newTools(t, nil)
	ti.Piper = rec.install
	res, err := ti.Install(context.Background(), kitDir, kit.ToolPiper)
	if err != nil || res.Version != "2023.11.14-2" || !strings.HasSuffix(rec.calls[0], hexSHA(body)+"|2023.11.14-2") {
		t.Fatalf("%+v %v %v", res, err, rec.calls)
	}
	// The installer's own failure and code come through (piper: LAI-SVC-020), and errors.Is sees it.
	cause := &codedError{doctor.SVC020}
	rec.err = cause
	_, err = ti.Install(context.Background(), kitDir, kit.ToolPiper)
	var te *kit.ToolError
	if !errors.As(err, &te) || te.Code() != doctor.SVC020 || !errors.Is(err, cause) || !errors.Is(err, kit.ErrToolInstall) {
		t.Fatalf("%v", err)
	}
	// An installer error without a code is LAI-KIT-047.
	rec.err = errors.New("plain")
	_, err = ti.Install(context.Background(), kitDir, kit.ToolPiper)
	if !errors.As(err, &te) || te.Code() != doctor.KIT047 {
		t.Fatalf("%v", err)
	}
	// A cancelled run is the context error, not an install failure.
	ctx, cancel := context.WithCancel(context.Background())
	rec.err = context.Canceled
	cancel()
	if _, err = ti.Install(ctx, kitDir, kit.ToolPiper); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// ---- pmtiles ------------------------------------------------------------------------------------

// echoRunner answers every program with the content of its own file: the fixture "binary" is the text
// the smoke test should read.
func echoRunner() *fnRunner {
	return &fnRunner{fn: func(c execx.Cmd) (execx.Result, error) {
		b, err := os.ReadFile(c.Path)
		if err != nil {
			return execx.Result{}, err
		}
		return execx.Result{Stdout: b}, nil
	}}
}

func pmtilesKit(t *testing.T, version, binary string) string {
	t.Helper()
	kitDir := filepath.Join(t.TempDir(), "kit")
	putPmtiles(t, kitDir, version, binary)
	return kitDir
}

func putPmtiles(t *testing.T, kitDir, version, binary string) {
	t.Helper()
	tgz := makeTarGz(t, tarItem{name: "LICENSE", body: "license"}, tarItem{name: "README.md", body: "readme"}, tarItem{name: "pmtiles", body: binary})
	putTool(t, kitDir, "tools/pmtiles", version, map[string][]byte{
		"go-pmtiles_" + version + "_Linux_x86_64.tar.gz": tgz,
		"go-pmtiles_" + version + "_Linux_arm64.tar.gz":  makeTarGz(t, tarItem{name: "pmtiles", body: "arm " + binary}),
	})
}

func link(t *testing.T, p string) string {
	t.Helper()
	s, err := os.Readlink(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInstallPmtilesSwitchesCurrentAndKeepsThePrevious(t *testing.T) {
	ti, opt := newTools(t, echoRunner())
	kit1 := pmtilesKit(t, "1.0.0", "pmtiles 1.0.0 build one")
	res, err := ti.Install(context.Background(), kit1, kit.ToolPmtiles)
	if err != nil || res.Version != "1.0.0" || res.Previous != "" || res.Unchanged {
		t.Fatalf("%+v %v", res, err)
	}
	root := filepath.Join(opt, "pmtiles")
	if link(t, filepath.Join(root, "current")) != "1.0.0" {
		t.Errorf("current -> %s", link(t, filepath.Join(root, "current")))
	}
	fi, err := os.Stat(filepath.Join(root, "1.0.0", "pmtiles"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("the program: %v %v", fi, err)
	}
	// Only the program is extracted: the license and the readme are never written.
	if es := dirNames(t, filepath.Join(root, "1.0.0")); !reflect.DeepEqual(es, []string{".tarball.sha256", "pmtiles"}) {
		t.Errorf("version dir holds %v", es)
	}

	// A new version: current moves, the old directory stays.
	kit2 := pmtilesKit(t, "1.1.0", "pmtiles 1.1.0 build two")
	res, err = ti.Install(context.Background(), kit2, kit.ToolPmtiles)
	if err != nil || res.Version != "1.1.0" || res.Previous != "1.0.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if link(t, filepath.Join(root, "current")) != "1.1.0" {
		t.Error("current did not move")
	}
	if _, err := os.Stat(filepath.Join(root, "1.0.0", "pmtiles")); err != nil {
		t.Error("the previous version must be kept")
	}
	if _, err := os.Stat(filepath.Join(root, "current.tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("current.tmp left behind")
	}

	// The same version again changes nothing and reports it.
	res, err = ti.Install(context.Background(), kit2, kit.ToolPmtiles)
	if err != nil || !res.Unchanged || res.Previous != "" {
		t.Fatalf("idempotent: %+v %v", res, err)
	}
	// Going back to the first one works, and the new one is kept.
	res, err = ti.Install(context.Background(), kit1, kit.ToolPmtiles)
	if err != nil || res.Previous != "1.1.0" || link(t, filepath.Join(root, "current")) != "1.0.0" {
		t.Fatalf("back: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, "1.1.0", "pmtiles")); err != nil {
		t.Error("the newer version must be kept")
	}
	// arm64 reads the other tarball: a different archive for the same version replaces the directory.
	ti.Arch = "arm64"
	if _, err := ti.Install(context.Background(), kit1, kit.ToolPmtiles); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "1.0.0", "pmtiles")); got != "arm pmtiles 1.0.0 build one" {
		t.Errorf("arm64 program: %q", got)
	}
}

func TestInstallPmtilesFailuresLeaveCurrentAlone(t *testing.T) {
	ti, opt := newTools(t, echoRunner())
	root := filepath.Join(opt, "pmtiles")
	if _, err := ti.Install(context.Background(), pmtilesKit(t, "1.0.0", "pmtiles 1.0.0"), kit.ToolPmtiles); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		items []tarItem
	}{
		{"the program does not report the version", []tarItem{{name: "pmtiles", body: "something else"}}},
		{"no program in the archive", []tarItem{{name: "LICENSE", body: "x"}}},
		{"two programs", []tarItem{{name: "pmtiles", body: "pmtiles 2.0.0"}, {name: "dir/pmtiles", body: "pmtiles 2.0.0"}}},
		{"an entry that climbs out", []tarItem{{name: "../evil", body: "x"}, {name: "pmtiles", body: "pmtiles 2.0.0"}}},
		{"an absolute entry", []tarItem{{name: "/etc/evil", body: "x"}, {name: "pmtiles", body: "pmtiles 2.0.0"}}},
		{"the program is a link", []tarItem{{name: "pmtiles", body: "/etc/passwd", typ: tar.TypeSymlink}}},
		{"an empty program", []tarItem{{name: "pmtiles", body: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kitDir := filepath.Join(t.TempDir(), "kit")
			putTool(t, kitDir, "tools/pmtiles", "2.0.0", map[string][]byte{"go-pmtiles_2.0.0_Linux_x86_64.tar.gz": makeTarGz(t, tc.items...)})
			_, err := ti.Install(context.Background(), kitDir, kit.ToolPmtiles)
			var te *kit.ToolError
			if !errors.As(err, &te) || !errors.Is(err, kit.ErrToolInstall) || te.Code() != doctor.KIT047 {
				t.Fatalf("%v", err)
			}
			if link(t, filepath.Join(root, "current")) != "1.0.0" {
				t.Error("current must not move")
			}
			for _, leftover := range []string{"2.0.0", "2.0.0.tmp", "current.tmp"} {
				if _, err := os.Stat(filepath.Join(root, leftover)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s left behind", leftover)
				}
			}
			if _, err := os.Stat(filepath.Join(opt, "evil")); err == nil {
				t.Error("an entry escaped the folder")
			}
			if _, err := os.Stat("/etc/evil"); err == nil {
				t.Error("an entry escaped the folder")
			}
		})
	}
	t.Run("a tampered tarball never reaches the extractor", func(t *testing.T) {
		kitDir := pmtilesKit(t, "3.0.0", "pmtiles 3.0.0")
		p := filepath.Join(kitDir, "tools", "pmtiles", "go-pmtiles_3.0.0_Linux_x86_64.tar.gz")
		b, _ := os.ReadFile(p)
		b[len(b)-3] ^= 0xff
		os.WriteFile(p, b, 0o644)
		_, err := ti.Install(context.Background(), kitDir, kit.ToolPmtiles)
		var te *kit.ToolError
		if !errors.As(err, &te) || te.Code() != doctor.KIT001 {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "3.0.0")); !errors.Is(err, fs.ErrNotExist) {
			t.Error("nothing may be extracted")
		}
	})
	t.Run("a version that is not a plain release number", func(t *testing.T) {
		kitDir := filepath.Join(t.TempDir(), "kit")
		putTool(t, kitDir, "tools/pmtiles", "x", map[string][]byte{"go-pmtiles_1.0.0.tmp_Linux_x86_64.tar.gz": makeTarGz(t, tarItem{name: "pmtiles", body: "pmtiles"})})
		if _, err := ti.Install(context.Background(), kitDir, kit.ToolPmtiles); err == nil {
			t.Error("a version ending in .tmp must be refused")
		}
	})
}

// ---- agent --------------------------------------------------------------------------------------

// agentBinary is the stand-in agent: the text `version --json` should print.
func agentBinary(version, arch string) []byte {
	return []byte(fmt.Sprintf(`{"version":"%s","os":"linux","arch":"%s"}`, version, arch))
}

type agentKit struct {
	kitDir string
	signer *updatetest.Signer
	keys   []update.PublicKey
}

// newAgentKit writes kit/bin with both binaries, SHA256SUMS signed by a throwaway key, and a manifest.
func newAgentKit(t *testing.T, version string) *agentKit {
	t.Helper()
	k := &agentKit{kitDir: filepath.Join(t.TempDir(), "kit"), signer: updatetest.NewSigner()}
	pub, err := update.ParsePublicKey(k.signer.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	k.keys = []update.PublicKey{pub}
	files := map[string][]byte{
		"aiagent-linux-amd64": agentBinary(version, "amd64"),
		"aiagent-linux-arm64": agentBinary(version, "arm64"),
	}
	dir := putTool(t, k.kitDir, "bin", "node-v"+version, files)
	sums := updatetest.Sums(files)
	writeFile(t, filepath.Join(dir, "SHA256SUMS"), string(sums))
	writeFile(t, filepath.Join(dir, "SHA256SUMS.minisig"), string(k.signer.Sign(sums, updatetest.SignOptions{})))
	return k
}

func (k *agentKit) bin(name string) string { return filepath.Join(k.kitDir, "bin", name) }

// installedAgent puts a running agent of the given version into <opt>/bin.
func installedAgent(t *testing.T, opt, version string) {
	t.Helper()
	writeFile(t, filepath.Join(opt, "bin", "aiagent"), string(agentBinary(version, "amd64")))
	os.Chmod(filepath.Join(opt, "bin", "aiagent"), 0o755)
}

func agentRunner(restartExit int) *fnRunner {
	return &fnRunner{fn: func(c execx.Cmd) (execx.Result, error) {
		if filepath.Base(c.Path) == "systemctl" {
			return execx.Result{ExitCode: restartExit}, nil
		}
		b, err := os.ReadFile(c.Path)
		if err != nil {
			return execx.Result{}, err
		}
		return execx.Result{Stdout: b}, nil
	}}
}

func TestInstallAgentVerifiesSwapsAndRestarts(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	rn := agentRunner(0)
	ti, opt := newTools(t, rn)
	ti.Keys = k.keys
	installedAgent(t, opt, "0.4.0")
	var steps []string
	ti.Progress = func(s, tool string) { steps = append(steps, s) }
	res, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "0.5.0" || res.Previous != "0.4.0" || res.Unchanged || res.RestartError != "" {
		t.Errorf("result %+v", res)
	}
	if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.5.0", "amd64")) {
		t.Errorf("installed agent: %s", got)
	}
	if got := readFile(t, filepath.Join(opt, "bin", "aiagent.prev")); got != string(agentBinary("0.4.0", "amd64")) {
		t.Errorf("aiagent.prev must keep the old agent: %s", got)
	}
	if fi, err := os.Stat(filepath.Join(opt, "bin", "aiagent")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode: %v %v", fi, err)
	}
	for _, leftover := range []string{"aiagent.new", "aiagent.prev.tmp"} {
		if _, err := os.Stat(filepath.Join(opt, "bin", leftover)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s left behind", leftover)
		}
	}
	lines := rn.lines()
	if last := lines[len(lines)-1]; last != "systemctl restart localai-agent.service" {
		t.Errorf("the agent must be restarted last: %v", lines)
	}
	if !reflect.DeepEqual(steps, []string{"verify", "install", "restart"}) {
		t.Errorf("steps %v", steps)
	}
	// The smoke test ran the copy next to the installed one, never the file on the kit drive.
	for _, l := range lines {
		if strings.Contains(l, k.kitDir) {
			t.Errorf("the kit binary itself was executed: %s", l)
		}
	}
}

func TestInstallAgentFirstInstallHasNoPrevious(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	ti, opt := newTools(t, agentRunner(0))
	ti.Keys = k.keys
	res, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if err != nil || res.Previous != "" || res.Version != "0.5.0" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(opt, "bin", "aiagent.prev")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("there is no previous agent to keep")
	}
}

func TestInstallAgentIsIdempotent(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	rn := agentRunner(0)
	ti, opt := newTools(t, rn)
	ti.Keys = k.keys
	if _, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent); err != nil {
		t.Fatal(err)
	}
	n := len(rn.lines())
	res, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if err != nil || !res.Unchanged {
		t.Fatalf("%+v %v", res, err)
	}
	if len(rn.lines()) != n {
		t.Errorf("an unchanged agent is not restarted: %v", rn.lines()[n:])
	}
	_ = opt
}

// Nothing is replaced when the signature or a checksum does not verify (LAI-KIT-044).
func TestInstallAgentRefusesBadSignaturesAndChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, k *agentKit)
		keys   func(k *agentKit) []update.PublicKey
	}{
		{"a signature from another key", func(t *testing.T, k *agentKit) {
			sums := readFile(t, k.bin("SHA256SUMS"))
			writeFile(t, k.bin("SHA256SUMS.minisig"), string(updatetest.NewSigner().Sign([]byte(sums), updatetest.SignOptions{})))
		}, nil},
		{"the checksum file changed after signing", func(t *testing.T, k *agentKit) {
			writeFile(t, k.bin("SHA256SUMS"), strings.Replace(readFile(t, k.bin("SHA256SUMS")), "  aiagent-linux-amd64", "  aiagent-linux-amd64 ", 1))
		}, nil},
		{"the binary changed", func(t *testing.T, k *agentKit) {
			writeFile(t, k.bin("aiagent-linux-amd64"), `{"version":"9.9.9","os":"linux","arch":"amd64"}`)
		}, nil},
		{"the signature file is missing", func(t *testing.T, k *agentKit) { os.Remove(k.bin("SHA256SUMS.minisig")) }, nil},
		{"the checksum file is missing", func(t *testing.T, k *agentKit) { os.Remove(k.bin("SHA256SUMS")) }, nil},
		{"the signature file is garbage", func(t *testing.T, k *agentKit) { writeFile(t, k.bin("SHA256SUMS.minisig"), "garbage") }, nil},
		{"no trusted key is embedded", func(t *testing.T, k *agentKit) {}, func(k *agentKit) []update.PublicKey { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := newAgentKit(t, "0.5.0")
			tc.mutate(t, k)
			rn := agentRunner(0)
			ti, opt := newTools(t, rn)
			ti.Keys = k.keys
			if tc.keys != nil {
				ti.Keys = tc.keys(k)
			}
			installedAgent(t, opt, "0.4.0")
			_, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
			var te *kit.ToolError
			if !errors.As(err, &te) || !errors.Is(err, kit.ErrSignature) || te.Code() != doctor.KIT044 {
				t.Fatalf("want LAI-KIT-044, got %v", err)
			}
			if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.4.0", "amd64")) {
				t.Errorf("the installed agent was replaced: %s", got)
			}
			es := dirNames(t, filepath.Join(opt, "bin"))
			if !reflect.DeepEqual(es, []string{"aiagent"}) {
				t.Errorf("files left in bin: %v", es)
			}
			if len(rn.lines()) != 0 {
				t.Errorf("nothing may run: %v", rn.lines())
			}
		})
	}
}

func TestInstallAgentRefusesAFailingNewBinary(t *testing.T) {
	cases := map[string]string{
		"it reports another version than the kit": `{"version":"0.6.0","os":"linux","arch":"amd64"}`,
		"it reports another architecture":         `{"version":"0.5.0","os":"linux","arch":"arm64"}`,
		"it reports another system":               `{"version":"0.5.0","os":"darwin","arch":"amd64"}`,
		"it prints something that is not JSON":    `hello`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			k := newAgentKit(t, "0.5.0")
			// A genuinely signed kit whose binary lies about itself.
			files := map[string][]byte{"aiagent-linux-amd64": []byte(out), "aiagent-linux-arm64": agentBinary("0.5.0", "arm64")}
			sums := updatetest.Sums(files)
			for n, b := range files {
				writeFile(t, k.bin(n), string(b))
			}
			writeFile(t, k.bin("SHA256SUMS"), string(sums))
			writeFile(t, k.bin("SHA256SUMS.minisig"), string(k.signer.Sign(sums, updatetest.SignOptions{})))
			rn := agentRunner(0)
			ti, opt := newTools(t, rn)
			ti.Keys = k.keys
			installedAgent(t, opt, "0.4.0")
			_, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
			var te *kit.ToolError
			if !errors.As(err, &te) || !errors.Is(err, kit.ErrToolInstall) || te.Code() != doctor.KIT047 {
				t.Fatalf("%v", err)
			}
			if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.4.0", "amd64")) {
				t.Errorf("the installed agent was replaced: %s", got)
			}
			if es := dirNames(t, filepath.Join(opt, "bin")); !reflect.DeepEqual(es, []string{"aiagent"}) {
				t.Errorf("files left in bin: %v", es)
			}
			for _, l := range rn.lines() {
				if strings.HasPrefix(l, "systemctl") {
					t.Error("the agent must not be restarted")
				}
			}
		})
	}
}

func TestInstallAgentRestartFailureIsAWarning(t *testing.T) {
	k := newAgentKit(t, "0.5.0")
	ti, opt := newTools(t, agentRunner(5))
	ti.Keys = k.keys
	res, err := ti.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if err != nil || res.RestartError == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := readFile(t, filepath.Join(opt, "bin", "aiagent")); got != string(agentBinary("0.5.0", "amd64")) {
		t.Error("the verified agent is installed even when systemctl fails")
	}
}

func TestInstallAgentNotInTheKit(t *testing.T) {
	ti, _ := newTools(t, agentRunner(0))
	ti.Keys = nil
	_, err := ti.Install(context.Background(), filepath.Join(t.TempDir(), "kit"), kit.ToolAgent)
	var te *kit.ToolError
	if !errors.As(err, &te) || !errors.Is(err, kit.ErrToolNotInKit) || te.Code() != doctor.KIT040 {
		t.Fatalf("%v", err)
	}
	// An architecture the kit does not build for.
	ti.Arch = "riscv64"
	if _, err := ti.Install(context.Background(), t.TempDir(), kit.ToolAgent); !errors.Is(err, kit.ErrToolNotInKit) {
		t.Fatalf("%v", err)
	}
	// A kit that only has the other architecture.
	k := newAgentKit(t, "0.5.0")
	os.Remove(k.bin("aiagent-linux-amd64"))
	files := map[string][]byte{"aiagent-linux-arm64": agentBinary("0.5.0", "arm64")}
	sums := updatetest.Sums(files)
	writeFile(t, k.bin("SHA256SUMS"), string(sums))
	writeFile(t, k.bin("SHA256SUMS.minisig"), string(k.signer.Sign(sums, updatetest.SignOptions{})))
	ti2, _ := newTools(t, agentRunner(0))
	ti2.Keys = k.keys
	_, err = ti2.Install(context.Background(), k.kitDir, kit.ToolAgent)
	if !errors.Is(err, kit.ErrToolNotInKit) {
		t.Fatalf("%v", err)
	}
}
