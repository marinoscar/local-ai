package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/item"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
	"github.com/marinoscar/local-ai/agent/internal/sync/resolve"
)

// realTool runs the real version control tool for the fixtures and the producer of the test below.
type realTool struct {
	t    *testing.T
	bin  string
	env  []string
	gzip string
}

func newRealTool(t *testing.T) *realTool {
	t.Helper()
	g := &realTool{t: t}
	var err error
	if g.bin, err = execx.Lookup("git"); err != nil {
		t.Skip("git is not installed: this test needs the real tool")
	}
	if g.gzip, err = execx.Lookup("gzip"); err != nil {
		t.Skip("gzip is not installed: this test needs the real tool")
	}
	g.env = []string{
		"PATH=" + execx.FixedPath, "HOME=" + t.TempDir(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Person A", "GIT_AUTHOR_EMAIL=person.a@example.test", "GIT_COMMITTER_NAME=Person A", "GIT_COMMITTER_EMAIL=person.a@example.test",
		"GIT_AUTHOR_DATE=" + epoch.Format(time.RFC3339), "GIT_COMMITTER_DATE=" + epoch.Format(time.RFC3339),
	}
	return g
}

func (g *realTool) run(bin string, args ...string) (string, int) {
	g.t.Helper()
	res, err := execx.Exec{}.Run(bg, execx.Cmd{Path: bin, Args: args, Env: g.env, Timeout: time.Minute, MaxOut: 8 << 20})
	if err != nil {
		g.t.Fatalf("%s %v: %v", bin, args, err)
	}
	return strings.TrimSpace(string(res.Stdout)), res.ExitCode
}

func (g *realTool) must(args ...string) string {
	g.t.Helper()
	out, code := g.run(g.bin, args...)
	if code != 0 {
		g.t.Fatalf("%v exited %d", args, code)
	}
	return out
}

// realMirrorProducer is a resolve.Producer that makes its product with the real tool against a local
// origin: the first time a mirror clone into staging (a tree for the engine to swap in), afterwards a
// remote update in the present mirror (an in-place update), and a reproducible tarball of the pinned
// commit (archive, then gzip -n) staged for kit/src.
type realMirrorProducer struct {
	g      *realTool
	origin string
	pinned string
}

func (p *realMirrorProducer) Produce(_ context.Context, req resolve.Request) (resolve.Product, error) {
	g := p.g
	present := filepath.Join(req.Root, filepath.FromSlash(mirrorDir))
	staging := req.StagingDir()
	part := resolve.Part{Name: "mirror", To: mirrorDir, Record: []string{"HEAD"}}
	mirror := present
	if fi, err := os.Stat(present); err == nil && fi.IsDir() {
		part.Kind = resolve.PartInPlace
		g.must("-c", "protocol.file.allow=always", "-c", "safe.directory="+present, "-C", present, "remote", "update", "--prune")
	} else {
		mirror = filepath.Join(staging, "llama.cpp.git")
		part.Kind, part.From = resolve.PartTree, "llama.cpp.git"
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return resolve.Product{}, err
		}
		g.must("-c", "protocol.file.allow=always", "clone", "-q", "--mirror", "--no-tags", "--", p.origin, mirror)
	}
	head := g.must("-c", "safe.directory="+mirror, "-C", mirror, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(staging, "src"), 0o755); err != nil {
		return resolve.Product{}, err
	}
	base := "llama.cpp-" + p.pinned
	tarPath := filepath.Join(staging, "src", base+".tar")
	g.must("-c", "safe.directory="+mirror, "-C", mirror, "archive", "--format=tar", "--prefix="+base+"/", "-o", tarPath, p.pinned)
	if _, code := g.run(g.gzip, "-n", "-9", "-f", tarPath); code != 0 {
		g.t.Fatal("gzip failed")
	}
	b, err := os.ReadFile(tarPath + ".gz")
	if err != nil {
		return resolve.Product{}, err
	}
	files := []manifest.File{{Kind: "source_tarball", Path: base + ".tar.gz", SizeBytes: int64(len(b)), SHA256: sha(b), SHA256Source: "computed_after_verify"}}
	return resolve.Product{
		Version: head[:12], SortKey: head[:12], Trust: manifest.TrustComputed, Source: map[string]any{"url": req.Item.Content.Source.URL, "commit": head},
		Parts: []resolve.Part{part, {Name: "tarballs", Kind: resolve.PartFile, From: "src", To: "kit/src", Files: files}},
	}, nil
}

// TestRealRepositoryThroughTheEngine runs the machine over a real bare mirror: read-only object files,
// a repository that the tool itself changes, a real archive tarball. The first run installs the clone
// and the tarball; the second updates the repository in place. After each, the repository passes its own
// consistency check and holds the pinned commit; the manifest records HEAD only plus the tarball; the
// unchanged tarball is not moved or trashed again; the old manifest and SHA256SUMS are in the trash.
func TestRealRepositoryThroughTheEngine(t *testing.T) {
	g := newRealTool(t)
	dir := t.TempDir()
	work, origin := filepath.Join(dir, "work"), filepath.Join(dir, "origin.git")
	g.must("init", "-q", "-b", "main", work)
	commitFile := func(name, content string) string {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		g.must("-C", work, "add", "-A")
		g.must("-C", work, "commit", "-q", "-m", "change "+name)
		if _, err := os.Stat(origin); err == nil {
			g.must("-C", work, "push", "-q", origin, "HEAD:refs/heads/main")
		}
		return g.must("-C", work, "rev-parse", "HEAD")
	}
	pinned := commitFile("README", "first\n")
	g.must("clone", "-q", "--bare", work, origin)

	prod := &realMirrorProducer{g: g, origin: origin, pinned: pinned}
	s := newPStack(t, false, map[string]resolve.Producer{"git_mirror": prod}, llamaMirror())
	mirror := filepath.Join(s.internal.Path, filepath.FromSlash(mirrorDir))
	tarball := filepath.Join(s.internal.Path, "kit", "src", "llama.cpp-"+pinned+".tar.gz")

	check := func(version, wantHead string) manifest.Manifest {
		t.Helper()
		m, err := s.load(s.internal, mirrorDir)
		if err != nil || m.Version != version || m.Source.String("commit") != wantHead || m.FileCount != 1 {
			t.Fatalf("manifest = %+v, %v", m, err)
		}
		g.must("-c", "safe.directory="+mirror, "-C", mirror, "fsck", "--connectivity-only", "--no-dangling")
		g.must("-c", "safe.directory="+mirror, "-C", mirror, "cat-file", "-e", pinned+"^{commit}")
		sums, err := manifest.ReadSums(item.OSFS{}, mirror)
		if err != nil || len(sums) != 1 || sums[0].Path != "HEAD" {
			t.Fatalf("SHA256SUMS = %v, %v", sums, err)
		}
		b, err := os.ReadFile(tarball)
		if err != nil {
			t.Fatal(err)
		}
		if sum, ok := (kit.SourceFinder{}).RecordedSHA256(s.internal.Path, "llama.cpp", filepath.Base(tarball)); !ok || sum != sha(b) {
			t.Fatalf("RecordedSHA256 = %q, %v", sum, ok)
		}
		if m.TotalBytes < int64(len(b)) {
			t.Fatalf("total_bytes = %d", m.TotalBytes)
		}
		s.assertStagingClean(s.internal)
		return m
	}

	head1 := g.must("-C", work, "rev-parse", "HEAD")
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {dlRow(mirrorName, "kit", head1[:12])}})
	if st := s.sync(); st.Result != ResultOK || st.Totals.Done != 1 {
		t.Fatalf("first run = %+v (%+v)", st, st.Items)
	}
	check(head1[:12], head1)

	head2 := commitFile("NEWS", "second\n")
	s.setRows(map[string][]plan.ItemPlan{ark.InternalID: {{ID: mirrorName, Kind: "kit", Action: plan.ActionUpdate, ToVersion: head2[:12], Source: plan.SourceNetwork, NeedBytes: 100, FinalBytes: 100}}})
	if st := s.sync(); st.Result != ResultOK || st.Totals.Done != 1 {
		t.Fatalf("second run = %+v (%+v)", st, st.Items)
	}
	m := check(head2[:12], head2)
	g.must("-c", "safe.directory="+mirror, "-C", mirror, "cat-file", "-e", head2+"^{commit}")
	if len(m.History) != 1 || m.History[0].Version != head1[:12] {
		t.Fatalf("history = %+v", m.History)
	}
	trash := filepath.Join(s.internal.Path, ".trash", mirrorName, head1[:12])
	if tm, err := manifest.Read(item.OSFS{}, filepath.Join(trash, manifest.FileName)); err != nil || tm.Version != head1[:12] {
		t.Fatalf("the old manifest is not in the trash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, manifest.SumsName)); err != nil {
		t.Fatalf("the old SHA256SUMS is not in the trash: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, "companions")); err == nil {
		t.Fatal("the reproducible tarball was trashed although its bytes did not change")
	}
}
