package kit_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Fixtures for the offline apt tests: kit directories built in a temp dir at runtime, with digests
// computed on the spot (no digest literal is ever written down).

// fakeDeb is a package of a fixture repository. Its "file" is a short text, which is all the
// verifier needs: it hashes, it never unpacks.
type fakeDeb struct{ name, version string }

func (d fakeDeb) content() []byte { return []byte("fixture deb " + d.name + " " + d.version + "\n") }
func (d fakeDeb) pool() string    { return d.name + "_" + d.version + "_amd64.deb" }

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func gz(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildRepo writes a complete repository <kitDir>/<part>/<name> the way the apt_closure resolver does
// (pool/, Packages, Packages.gz, PACKAGES.lock, README.txt, SHA256SUMS, MANIFEST.json) and returns
// its directory.
func buildRepo(t testing.TB, kitDir, part, name string, debs ...fakeDeb) string {
	t.Helper()
	dir := filepath.Join(kitDir, part, name)
	if err := os.MkdirAll(filepath.Join(dir, "pool"), 0o755); err != nil {
		t.Fatal(err)
	}
	var index, lock bytes.Buffer
	sorted := append([]fakeDeb(nil), debs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, d := range sorted {
		writeFile(t, filepath.Join(dir, "pool", d.pool()), string(d.content()))
		sum := hexSHA(d.content())
		fmt.Fprintf(&index, "Package: %s\nVersion: %s\nArchitecture: amd64\nFilename: pool/%s\nSize: %d\nSHA256: %s\nDescription: fixture\n long line\n\n",
			d.name, d.version, d.pool(), len(d.content()), sum)
		fmt.Fprintf(&lock, "%s=%s %s\n", d.name, d.version, sum)
	}
	writeFile(t, filepath.Join(dir, "Packages"), index.String())
	if err := os.WriteFile(filepath.Join(dir, "Packages.gz"), gz(t, index.Bytes()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "PACKAGES.lock"), lock.String())
	writeFile(t, filepath.Join(dir, "README.txt"), "fixture repository\n")
	reseal(t, dir)
	return dir
}

// reseal rewrites SHA256SUMS and MANIFEST.json of a repository from what is on disk, so a test can
// change a file and still present consistent checksums (an attacker who also edits the sums).
func reseal(t testing.TB, dir string) {
	t.Helper()
	var sums []manifest.Sum
	var total int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "SHA256SUMS" || rel == "MANIFEST.json" || !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sums = append(sums, manifest.Sum{SHA256: hexSHA(b), Path: rel})
		total += int64(len(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		t.Fatal(err)
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: "kit-debs-" + filepath.Base(dir), ItemKind: manifest.KindKit, CatalogVersion: "2026.11.02-1",
		Version: "1", SortKey: "1", State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
		TreeSHA256: tree, FileCount: len(sums), TotalBytes: total,
		DownloadedAt: epoch, VerifiedAt: epoch, LastReverifiedAt: epoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		t.Fatal(err)
	}
}

// fnRunner is an execx.Runner that answers with a function and records every call.
type fnRunner struct {
	mu    sync.Mutex
	calls []execx.Cmd
	fn    func(c execx.Cmd) (execx.Result, error)
}

func (r *fnRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	return r.fn(c)
}

func (r *fnRunner) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
	}
	return out
}

// verbs lists the first argument of every apt-get call (update, install), "-s" marked.
func (r *fnRunner) aptVerbs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if filepath.Base(c.Path) != "apt-get" {
			continue
		}
		v := c.Args[0]
		for _, a := range c.Args {
			if a == "-s" {
				v += " -s"
			}
		}
		out = append(out, v)
	}
	return out
}

func lookTo(name string) (string, error) { return "/usr/bin/" + name, nil }

// aptFake is a scripted dpkg and apt: installed is what dpkg-query reports (name -> version); plan
// is the "Inst"/"Remv" text of apt-get install -s; fail maps "update"/"plan"/"install" to an exit code.
type aptFake struct {
	installed map[string]string
	plan      string
	fail      map[string]int
	// listSeen records the content of the apt source at each apt-get call (by verb).
	listSeen map[string]string
	t        testing.TB
}

func (a *aptFake) runner() *fnRunner {
	return &fnRunner{fn: func(c execx.Cmd) (execx.Result, error) {
		switch filepath.Base(c.Path) {
		case "dpkg-query":
			var b strings.Builder
			names := c.Args[2:]
			for _, n := range names {
				if v, ok := a.installed[n]; ok {
					fmt.Fprintf(&b, "%s\tii \t%s\n", n, v)
				}
			}
			code := 0
			if b.Len() < len(names)*3 { // some name unknown to dpkg
				code = 1
			}
			return execx.Result{Stdout: []byte(b.String()), ExitCode: code}, nil
		case "apt-get":
			verb := c.Args[0]
			if verb == "install" {
				for _, x := range c.Args {
					if x == "-s" {
						verb = "plan"
					}
				}
			}
			for _, x := range c.Args {
				if p, ok := strings.CutPrefix(x, "Dir::Etc::sourcelist="); ok {
					if data, err := os.ReadFile(p); err == nil {
						a.listSeen[verb] = string(data)
					} else {
						a.t.Errorf("apt-get %s ran without a readable source list: %v", verb, err)
					}
				}
			}
			if code := a.fail[verb]; code != 0 {
				return execx.Result{ExitCode: code, Stderr: []byte("E: simulated failure of " + verb + "\n")}, nil
			}
			if verb == "install" {
				for n, v := range a.installedAfter(c.Args) {
					a.installed[n] = v
				}
			}
			if verb == "plan" {
				return execx.Result{Stdout: []byte(a.plan)}, nil
			}
			return execx.Result{}, nil
		}
		return execx.Result{}, fmt.Errorf("unexpected program %s", c.Path)
	}}
}

// installedAfter: a real install makes the requested names installed at the version of the plan.
func (a *aptFake) installedAfter(args []string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(a.plan, "\n") {
		f := strings.Fields(line)
		if i := strings.Index(line, "("); len(f) > 2 && f[0] == "Inst" && i >= 0 {
			out[f[1]] = strings.Fields(line[i+1:])[0]
		}
	}
	return out
}

func newAptFake(t testing.TB) *aptFake {
	return &aptFake{installed: map[string]string{}, fail: map[string]int{}, listSeen: map[string]string{}, t: t}
}

// newApt is the Apt under test with temp paths; the source list lives in the temp dir.
func newApt(t testing.TB, r execx.Runner) (*kit.Apt, string) {
	t.Helper()
	tmp := t.TempDir()
	return &kit.Apt{
		FS: kit.OSFS{}, Runner: r, LookPath: lookTo,
		Codename: "noble", Arch: "amd64",
		SourceList: filepath.Join(tmp, "localai-ark.list"),
		ScratchDir: tmp,
	}, tmp
}
