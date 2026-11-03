// Package kittest builds rebuild kits on disk for tests: a content root with a kit/ folder holding
// the items a lab needs (sources, signed agent binaries, toolchains, tools, apt closures, prebuilt
// builds, pins.json), with every digest computed on the spot. No digest or commit is ever written
// down as a literal (the repository's redaction check rejects 64-hex strings).
//
// It is used by the tests of internal/kit, internal/kit/kitready and internal/cli. It is never
// imported by the agent.
package kittest

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
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

// Epoch is the clock the fixtures share: every manifest was verified and re-verified then.
var Epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)

// SHA is the hex sha256 of data.
func SHA(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Hex40 is a stand-in commit: the first 40 hex digits of the sha256 of seed.
func Hex40(seed string) string { return SHA([]byte(seed))[:40] }

// Kit is a content root with a kit/ folder.
type Kit struct {
	T testing.TB
	// Root is the content root and Dir is Root/kit.
	Root, Dir string
	// Signer signs the agent checksums; Keys is its public key, what the verifier trusts.
	Signer *updatetest.Signer
	Keys   []update.PublicKey
}

// New creates an empty kit in a fresh temporary directory.
func New(t testing.TB) *Kit { return NewAt(t, filepath.Join(t.TempDir(), "srv")) }

// NewAt creates an empty kit under the content root root.
func NewAt(t testing.TB, root string) *Kit {
	t.Helper()
	signer := updatetest.NewSigner()
	pub, err := update.ParsePublicKey(signer.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	k := &Kit{T: t, Root: root, Dir: filepath.Join(root, "kit"), Signer: signer, Keys: []update.PublicKey{pub}}
	if err := os.MkdirAll(k.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return k
}

// Abs is the absolute path of a path below kit/.
func (k *Kit) Abs(rel string) string { return filepath.Join(k.Dir, filepath.FromSlash(rel)) }

// Write writes a file below kit/ (mode 0644, parents created).
func (k *Kit) Write(rel string, content []byte) {
	k.T.Helper()
	p := k.Abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		k.T.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		k.T.Fatal(err)
	}
}

// Read returns a file below kit/.
func (k *Kit) Read(rel string) []byte {
	k.T.Helper()
	b, err := os.ReadFile(k.Abs(rel))
	if err != nil {
		k.T.Fatal(err)
	}
	return b
}

// Corrupt flips one byte of a file below kit/ without changing its size.
func (k *Kit) Corrupt(rel string) {
	k.T.Helper()
	b := k.Read(rel)
	if len(b) == 0 {
		k.T.Fatalf("%s is empty", rel)
	}
	b[len(b)/2] ^= 0xff
	k.Write(rel, b)
}

// Truncate cuts a file below kit/ to n bytes.
func (k *Kit) Truncate(rel string, n int64) {
	k.T.Helper()
	if err := os.Truncate(k.Abs(rel), n); err != nil {
		k.T.Fatal(err)
	}
}

// Remove deletes a file or folder below kit/.
func (k *Kit) Remove(rel string) {
	k.T.Helper()
	if err := os.RemoveAll(k.Abs(rel)); err != nil {
		k.T.Fatal(err)
	}
}

// Opt changes a manifest before it is written.
type Opt func(*manifest.Manifest)

// Reverified sets last_reverified_at (and verified_at).
func Reverified(at time.Time) Opt {
	return func(m *manifest.Manifest) { m.LastReverifiedAt, m.VerifiedAt = at, at }
}

// WithSource sets a field of the manifest's source (the companion lists of a mirror).
func WithSource(key string, v any) Opt {
	return func(m *manifest.Manifest) {
		s, err := m.Source.With(key, v)
		if err != nil {
			panic(err)
		}
		m.Source = s
	}
}

func baseManifest(id string) manifest.Manifest {
	return manifest.Manifest{
		SchemaVersion: 1, ItemID: id, ItemKind: manifest.KindKit, CatalogVersion: "2026.11.02-1",
		Version: "1", SortKey: "1", State: manifest.StateVerified, Source: manifest.Source{Type: "url"},
		DownloadedAt: Epoch, VerifiedAt: Epoch, LastReverifiedAt: Epoch, Trust: manifest.TrustPinned,
		SyncedBy: manifest.SyncedBy{NodeID: "node-a", AgentVersion: "0.4.0", RunID: "run-20261102T180000Z-0001"},
	}
}

// Item writes a file item: the files under kit/<dir> and a MANIFEST.json that records each one's real
// size and sha256. It returns the item directory.
func (k *Kit) Item(dir, id string, files map[string][]byte, opts ...Opt) string {
	k.T.Helper()
	d := k.Abs(dir)
	m := baseManifest(id)
	names := sortedNames(files)
	for _, n := range names {
		k.Write(dir+"/"+n, files[n])
		m.TotalBytes += int64(len(files[n]))
		m.Files = append(m.Files, manifest.File{Kind: "archive", Path: n, SizeBytes: int64(len(files[n])), SHA256: SHA(files[n]), SHA256Source: "catalog"})
	}
	for _, o := range opts {
		o(&m)
	}
	k.writeManifest(d, m)
	return d
}

// Tree writes a directory item: the files, a SHA256SUMS, and a manifest with the tree hash.
func (k *Kit) Tree(dir, id string, files map[string][]byte, opts ...Opt) string {
	k.T.Helper()
	d := k.Abs(dir)
	var sums []manifest.Sum
	var total int64
	for _, n := range sortedNames(files) {
		k.Write(dir+"/"+n, files[n])
		sums = append(sums, manifest.Sum{SHA256: SHA(files[n]), Path: n})
		total += int64(len(files[n]))
	}
	if err := manifest.WriteSums(config.OSFS{}, d, sums); err != nil {
		k.T.Fatal(err)
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		k.T.Fatal(err)
	}
	m := baseManifest(id)
	m.TreeSHA256, m.FileCount, m.TotalBytes = tree, len(sums), total
	for _, o := range opts {
		o(&m)
	}
	k.writeManifest(d, m)
	return d
}

func (k *Kit) writeManifest(dir string, m manifest.Manifest) {
	k.T.Helper()
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		k.T.Fatal(err)
	}
}

// Deb is a package of a fixture apt repository.
type Deb struct{ Name, Version string }

func (d Deb) content() []byte { return []byte("fixture deb " + d.Name + " " + d.Version + "\n") }
func (d Deb) pool() string    { return d.Name + "_" + d.Version + "_amd64.deb" }

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

// Repo writes a complete apt repository kit/<part>/<name> the way the apt_closure resolver does (pool/,
// Packages, Packages.gz, PACKAGES.lock, README.txt, SHA256SUMS, MANIFEST.json with the tree hash).
func (k *Kit) Repo(part, name, id string, debs ...Deb) string {
	k.T.Helper()
	rel := part + "/" + name
	var index, lock bytes.Buffer
	sorted := append([]Deb(nil), debs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, d := range sorted {
		k.Write(rel+"/pool/"+d.pool(), d.content())
		sum := SHA(d.content())
		fmt.Fprintf(&index, "Package: %s\nVersion: %s\nArchitecture: amd64\nFilename: pool/%s\nSize: %d\nSHA256: %s\nDescription: fixture\n long line\n\n",
			d.Name, d.Version, d.pool(), len(d.content()), sum)
		fmt.Fprintf(&lock, "%s=%s %s\n", d.Name, d.Version, sum)
	}
	k.Write(rel+"/Packages", index.Bytes())
	k.Write(rel+"/Packages.gz", gz(k.T, index.Bytes()))
	k.Write(rel+"/PACKAGES.lock", lock.Bytes())
	k.Write(rel+"/README.txt", []byte("fixture repository\n"))
	return k.Reseal(rel, id)
}

// Reseal rewrites SHA256SUMS and MANIFEST.json of a repository or tree from what is on disk, so a test
// can change a file and still present consistent checksums (an attacker who also edits the sums).
func (k *Kit) Reseal(rel, id string) string {
	k.T.Helper()
	dir := k.Abs(rel)
	var sums []manifest.Sum
	var total int64
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		r, _ := filepath.Rel(dir, p)
		r = filepath.ToSlash(r)
		if r == "SHA256SUMS" || r == "MANIFEST.json" || !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sums = append(sums, manifest.Sum{SHA256: SHA(b), Path: r})
		total += int64(len(b))
		return nil
	})
	if err != nil {
		k.T.Fatal(err)
	}
	if err := manifest.WriteSums(config.OSFS{}, dir, sums); err != nil {
		k.T.Fatal(err)
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil {
		k.T.Fatal(err)
	}
	m := baseManifest(id)
	m.TreeSHA256, m.FileCount, m.TotalBytes = tree, len(sums), total
	k.writeManifest(dir, m)
	return dir
}

// Mirror writes a git mirror kit/git/<name>.git (a HEAD file and an objects folder, recorded as a
// directory item) whose manifest records the source tarballs of commits in kit/src/<stem>-<commit>.tar.gz
// (rebuild-kit.md section 3.2.1). It returns the commits' tarball file names.
func (k *Kit) Mirror(name, id string, commits ...string) []string {
	k.T.Helper()
	dir := "git/" + name + ".git"
	k.Write(dir+"/objects/pack/placeholder", []byte("objects of "+name+"\n"))
	var rec []map[string]any
	var names []string
	for _, c := range commits {
		fn := name + "-" + c + ".tar.gz"
		body := []byte("source tarball of " + name + " at " + c + "\n")
		k.Write("src/"+fn, body)
		rec = append(rec, map[string]any{"path": fn, "size_bytes": len(body), "sha256": SHA(body)})
		names = append(names, fn)
	}
	k.Write(dir+"/HEAD", []byte("ref: refs/heads/main\n"))
	// The mirror is a directory item whose SHA256SUMS lists its HEAD (the mirror is not hashed whole).
	d := k.Abs(dir)
	head := k.Read(dir + "/HEAD")
	sums := []manifest.Sum{{SHA256: SHA(head), Path: "HEAD"}}
	if err := manifest.WriteSums(config.OSFS{}, d, sums); err != nil {
		k.T.Fatal(err)
	}
	tree, _ := manifest.TreeSHA256(sums)
	m := baseManifest(id)
	m.TreeSHA256, m.FileCount, m.TotalBytes, m.Trust = tree, 1, int64(len(head)), manifest.TrustComputed
	m.Source = manifest.Source{Type: "git_mirror"}
	if len(rec) > 0 {
		WithSource("tarballs", rec)(&m)
	}
	k.writeManifest(d, m)
	return names
}

// SignBin writes kit/bin/SHA256SUMS and its minisign signature over the files of kit/bin that are
// listed, signed by the kit's throwaway key.
func (k *Kit) SignBin(files map[string][]byte) {
	k.T.Helper()
	sums := updatetest.Sums(files)
	k.Write("bin/SHA256SUMS", sums)
	k.Write("bin/SHA256SUMS.minisig", k.Signer.Sign(sums, updatetest.SignOptions{}))
}

// Index writes kit/MANIFEST.json and kit/README.txt the way sync does.
func (k *Kit) Index() {
	k.T.Helper()
	root := ark.NewInternalRoot(k.Root, "small-8gb")
	if _, err := kit.Regenerate(context.Background(), config.OSFS{}, root, kit.IndexOptions{Now: Epoch}); err != nil {
		k.T.Fatal(err)
	}
}

// Pins writes kit/catalog/pins.json from what the nodes report (kit.Report, merged), stamped at.
func (k *Kit) Pins(at time.Time, nodes ...Node) {
	k.T.Helper()
	var p kit.Pins
	for _, n := range nodes {
		rep, err := kit.Report(n.pins(), at)
		if err != nil {
			k.T.Fatal(err)
		}
		p = kit.Merge(p, rep, at)
	}
	if err := kit.WritePins(config.OSFS{}, k.Abs("catalog/pins.json"), p); err != nil {
		k.T.Fatal(err)
	}
}

// WritePins writes kit/catalog/pins.json from an explicit value (entries with their own report times).
func (k *Kit) WritePins(p kit.Pins) {
	k.T.Helper()
	if err := kit.WritePins(config.OSFS{}, k.Abs("catalog/pins.json"), p); err != nil {
		k.T.Fatal(err)
	}
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ---- a lab of nodes ---------------------------------------------------------------------------------

// Node describes a computer of the lab and, through Provision, what the kit holds for it.
type Node struct {
	ID       string
	Codename string
	Arch     string
	// Backend is cuda, vulkan or cpu.
	Backend string
	// CudaArch is the compute capability ("75"); Branch the NVIDIA driver branch ("595-open");
	// DriverCUDA the CUDA version the driver supports ("12.8").
	CudaArch, Branch, DriverCUDA string
	// AgentVersion is the running agent ("0.4.0").
	AgentVersion string
	// LlamaSeed and WhisperSeed make the pinned commits (Hex40 of the seed); the defaults are shared
	// by every node.
	LlamaSeed, WhisperSeed string
	// Toolkit is the Debian version of nvidia-cuda-toolkit in the debs closure ("" has none);
	// Redist is the version of a kit/cuda/<version>-<arch> redistributable set ("" has none).
	Toolkit, Redist string
	// Prebuilt exports a build of the node's current llama commit into kit/prebuilt.
	Prebuilt bool
	// NoWhisper leaves whisper out.
	NoWhisper bool
}

// Llama and Whisper are the node's pinned commits.
func (n Node) Llama() string {
	if n.LlamaSeed == "" {
		return Hex40("llama-pin")
	}
	return Hex40(n.LlamaSeed)
}

// Whisper is the node's pinned whisper.cpp commit ("" when it has none).
func (n Node) Whisper() string {
	switch {
	case n.NoWhisper:
		return ""
	case n.WhisperSeed == "":
		return Hex40("whisper-pin")
	}
	return Hex40(n.WhisperSeed)
}

// Facts are the node's facts for the verifier.
func (n Node) Facts() kit.NodeFacts {
	return kit.NodeFacts{
		NodeID: n.ID, Codename: n.Codename, Arch: n.Arch, Backend: n.Backend, CudaArch: n.CudaArch,
		NvidiaBranch: n.Branch, DriverCUDA: n.DriverCUDA, LlamaCommit: n.Llama(), WhisperCommit: n.Whisper(),
		AgentVersion: n.AgentVersion,
	}
}

func (n Node) pins() kit.NodePins {
	p := kit.NodePins{NodeID: n.ID, LlamaCommit: n.Llama(), WhisperCommit: n.Whisper(), AgentTag: "node-v" + n.AgentVersion,
		Codename: n.Codename, Arch: n.Arch}
	if n.Backend == "cuda" {
		p.NvidiaBranch = n.Branch
	}
	return p
}

// AiN1 is a Vulkan laptop with unified memory.
func AiN1() Node {
	return Node{ID: "ai-n1", Codename: "resolute", Arch: "amd64", Backend: "vulkan", AgentVersion: "0.4.0", Prebuilt: true}
}

// AiN2 is a CUDA node with the toolkit packages in its debs closure.
func AiN2() Node {
	return Node{ID: "ai-n2", Codename: "resolute", Arch: "amd64", Backend: "cuda", CudaArch: "86", Branch: "595-open",
		DriverCUDA: "13.0", AgentVersion: "0.4.0", Toolkit: "12.4.131~12.4.1-1", Prebuilt: true}
}

// AiN3 is a CUDA node whose toolkit is the redistributable set (option B).
func AiN3() Node {
	return Node{ID: "ai-n3", Codename: "resolute", Arch: "amd64", Backend: "cuda", CudaArch: "75", Branch: "595-open",
		DriverCUDA: "12.8", AgentVersion: "0.4.0", Redist: "12.4", Prebuilt: true}
}

// Provision writes everything the kit should hold for the nodes: the source tarballs and mirrors of
// their pinned commits, the agent binaries (signed) and source, the Go toolchain and the tools of each
// architecture, the apt closures of each release (the NVIDIA driver closure of each CUDA node), the
// CUDA toolkit, prebuilt builds, the catalog and pins.json (reported at Epoch).
func (k *Kit) Provision(nodes ...Node) {
	k.T.Helper()
	llama, whisper := map[string]bool{}, map[string]bool{}
	src := map[string][]byte{}
	archs := map[string]bool{}
	debs := map[string][]Deb{}
	nvidia := map[string]bool{}
	redist := map[string]bool{}
	for _, n := range nodes {
		llama[n.Llama()] = true
		if w := n.Whisper(); w != "" {
			whisper[w] = true
		}
		src["local-ai-node-v"+n.AgentVersion+"-src.tar.gz"] = []byte("agent source " + n.AgentVersion + "\n")
		archs[n.Arch] = true
		dir := n.Codename + "-" + n.Arch
		d := debs[dir]
		if d == nil {
			d = []Deb{{"cmake", "3.28.3-1"}, {"build-essential", "12.10"}}
		}
		if n.Toolkit != "" {
			d = append(d, Deb{"nvidia-cuda-toolkit", n.Toolkit})
		}
		debs[dir] = d
		if n.Backend == "cuda" && n.Branch != "" {
			nvidia[dir+"-"+n.Branch] = true
		}
		if n.Redist != "" {
			redist[n.Redist+"-"+n.Arch] = true
		}
	}
	k.Item("src", "kit-agent-source", src)
	if len(llama) > 0 {
		k.Mirror("llama.cpp", "kit-llama-cpp", sortedKeys(llama)...)
	}
	if len(whisper) > 0 {
		k.Mirror("whisper.cpp", "kit-whisper-cpp", sortedKeys(whisper)...)
	}
	bin := map[string][]byte{}
	for a := range archs {
		bin["aiagent-linux-"+a] = []byte("agent binary " + a + "\n")
	}
	installer := []byte("#!/bin/sh\n# installer fixture\n")
	k.Write("installers/install-node.sh", installer)
	k.Item("bin", "kit-agent-binaries", bin, WithSource("installers",
		[]map[string]any{{"path": "install-node.sh", "size_bytes": len(installer), "sha256": SHA(installer)}}))
	k.SignBin(bin)
	tc := map[string][]byte{}
	tools := map[string]map[string][]byte{"kiwix-tools": {}, "pmtiles": {}, "piper": {}}
	for a := range archs {
		tc["go1.24.0.linux-"+a+".tar.gz"] = []byte("go toolchain " + a + "\n")
		w1, w2, w3 := "x86_64", "Linux_x86_64", "x86_64"
		if a == "arm64" {
			w1, w2, w3 = "aarch64", "Linux_arm64", "aarch64"
		}
		tools["kiwix-tools"]["kiwix-tools_linux-"+w1+"-3.7.tar.gz"] = []byte("kiwix " + a + "\n")
		tools["pmtiles"]["go-pmtiles_1.0_"+w2+".tar.gz"] = []byte("pmtiles " + a + "\n")
		tools["piper"]["piper_linux_"+w3+".tar.gz"] = []byte("piper " + a + "\n")
	}
	k.Item("toolchains", "kit-go-toolchain", tc)
	for _, name := range []string{"kiwix-tools", "pmtiles", "piper"} {
		k.Item("tools/"+name, "kit-"+name, tools[name])
	}
	for dir, list := range debs {
		k.Repo("debs", dir, "kit-debs-"+dir, list...)
	}
	for dir := range nvidia {
		k.Repo("nvidia", dir, "kit-nvidia-"+dir, Deb{"nvidia-driver-595-open", "595.58.03-1"}, Deb{"dkms", "3.0.11-1"})
	}
	for dir := range redist {
		k.Item("cuda/"+dir, "kit-cuda-redist-"+dir, map[string][]byte{"cuda_nvcc-" + dir + ".tar.xz": []byte("nvcc " + dir + "\n")})
	}
	prebuilt := map[string][]byte{}
	for _, n := range nodes {
		if !n.Prebuilt {
			continue
		}
		name := "llama.cpp-" + n.Llama()[:9] + "-" + n.Backend + "-" + n.Codename + "-" + n.Arch
		if n.Backend == "cuda" && n.CudaArch != "" {
			name += "-sm" + strings.ReplaceAll(n.CudaArch, ";", "_")
		}
		prebuilt[name+".tar.gz"] = []byte("prebuilt " + name + "\n")
	}
	if len(prebuilt) > 0 {
		k.Item("prebuilt", "kit-prebuilt", prebuilt)
	}
	k.Item("catalog", "kit-catalog", map[string][]byte{"models.yaml": []byte("models: []\n"), "CATALOG_VERSION": []byte("2026.11.02-1\n")})
	k.Pins(Epoch, nodes...)
	k.Index()
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Paths lists every regular file below kit/ as slash paths, sorted: what a test compares before and
// after a verification to prove that nothing was written.
func (k *Kit) Paths() []string {
	k.T.Helper()
	var out []string
	err := filepath.Walk(k.Dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(k.Dir, p)
		line := filepath.ToSlash(r)
		if info.IsDir() {
			line += "/"
		}
		out = append(out, fmt.Sprintf("%s %d %v %d", line, info.Size(), info.Mode(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		k.T.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// Join is strings.Join, for tests that diff Paths.
func Join(s []string) string { return strings.Join(s, "\n") }
