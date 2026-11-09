package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/ark"
	"github.com/marinoscar/local-ai/agent/internal/ark/ready"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/drill"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/verify"
)

var (
	bg    = context.Background()
	epoch = time.Date(2026, 11, 2, 18, 4, 11, 0, time.UTC)
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

const driveID = "ark-0a0b0c0d"

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// env is the drill.Env a check runs in.
func testEnv(quick bool) *drill.Env {
	return &drill.Env{RunID: "run-test", Trigger: drill.TriggerManual, Quick: quick, Mode: "online", Now: func() time.Time { return epoch }, Log: quiet}
}

// run executes a check the way the engine does, without the timeout.
func run(t testing.TB, c drill.Check) drill.Outcome {
	t.Helper()
	out := c.Run(bg, testEnv(false))
	switch out.Result {
	case drill.ResultPass, drill.ResultWarn, drill.ResultFail, drill.ResultSkip:
	default:
		t.Fatalf("%s returned the invalid result %q", c.ID, out.Result)
	}
	return out
}

// clock is a manual clock the filesystem moves: reading a file costs readCost.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// slowFS is the real filesystem whose reads cost time on a manual clock.
type slowFS struct {
	verify.OSFS
	clk  *clock
	cost time.Duration
}

func (f slowFS) OpenRead(name string) (io.ReadCloser, error) {
	if f.clk != nil {
		f.clk.Advance(f.cost)
	}
	return f.OSFS.OpenRead(name)
}

// fakeReady is the ReadyReports of the tests: one report per root, built by want.
type fakeReady struct{ reports map[string]ready.Report }

func (f *fakeReady) Report(rootID string) (ready.Report, bool) {
	r, ok := f.reports[rootID]
	return r, ok
}

// contentRig is a node with an internal root and an ark drive on real directories, a real best-copy
// index, a real ark-sync re-verifier and the readiness reports the tests declare.
type contentRig struct {
	t     testing.TB
	roots []ark.Root
	idx   *ark.Index
	clk   *clock
	rv    *verify.Reverifier
	ready *fakeReady
}

func newContentRig(t testing.TB) *contentRig {
	t.Helper()
	base := t.TempDir()
	r := &contentRig{t: t, clk: &clock{t: epoch}, ready: &fakeReady{reports: map[string]ready.Report{}}}
	r.roots = []ark.Root{
		{ID: ark.InternalID, Kind: ark.KindInternal, Path: filepath.Join(base, "srv"), State: ark.StateReady},
		{ID: driveID, Kind: ark.KindArkDrive, Path: filepath.Join(base, "drive", "localai"), State: ark.StateReady, MountID: 3},
	}
	for _, ro := range r.roots {
		if err := os.MkdirAll(ro.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.idx = ark.NewIndex(config.OSFS{})
	r.rv = r.verifier(0)
	return r
}

func (r *contentRig) verifier(cost time.Duration) *verify.Reverifier {
	rv, err := verify.New(verify.Deps{
		FS: slowFS{clk: r.clk, cost: cost}, Roots: r.rootList, Index: r.idx, Now: r.clk.Now, Logger: quiet,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return rv
}

func (r *contentRig) rootList() []ark.Root { return r.roots }

func (r *contentRig) root(id string) ark.Root {
	for _, ro := range r.roots {
		if ro.ID == id {
			return ro
		}
	}
	r.t.Fatalf("no root %s", id)
	return ark.Root{}
}

// unplug removes the drive from the mounted roots, as an ejected or pulled drive is.
func (r *contentRig) unplug() {
	r.roots = r.roots[:1]
	delete(r.ready.reports, driveID)
}

func (r *contentRig) rebuild() {
	if _, err := r.idx.Rebuild(bg, r.roots); err != nil {
		r.t.Fatal(err)
	}
}

// itemSpec describes an item the tests install.
type itemSpec struct {
	ref      ark.ItemRef
	version  string
	files    map[string][]byte // name -> content; nil gets one small file
	lastRe   time.Time
	tier     int
	survival bool
	catalog  string // the catalog version recorded in the manifest
}

// install puts a verified copy of the item on a root, lists it in that root's report as verified, and
// returns the item directory.
func (r *contentRig) install(rootID string, s itemSpec) string {
	r.t.Helper()
	rel, err := ark.ItemDir(s.ref)
	if err != nil {
		r.t.Fatal(err)
	}
	dir := filepath.Join(r.root(rootID).Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	if s.version == "" {
		s.version = "v1"
	}
	if s.files == nil {
		s.files = map[string][]byte{"data.bin": []byte(strings.Repeat(s.ref.ID+" "+s.version+" ", 64))}
	}
	if s.lastRe.IsZero() {
		s.lastRe = epoch
	}
	m := manifest.Manifest{
		SchemaVersion: 1, ItemID: s.ref.ID, ItemKind: s.ref.Kind, Engine: s.ref.Engine, Version: s.version, SortKey: s.version,
		State: "verified", Trust: manifest.TrustPinned, CatalogVersion: s.catalog,
		Source:       manifest.Source{Type: "huggingface"},
		DownloadedAt: s.lastRe, VerifiedAt: s.lastRe, LastReverifiedAt: s.lastRe,
	}
	if s.ref.Kind == manifest.KindMaps {
		m.Source = manifest.Source{Type: "pmtiles_extract"}
	}
	var names []string
	for n := range s.files {
		names = append(names, n)
	}
	for _, n := range names {
		b := s.files[n]
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			r.t.Fatal(err)
		}
		m.Files = append(m.Files, manifest.File{Kind: "data", Path: n, SizeBytes: int64(len(b)), SHA256: sha(b)})
		m.TotalBytes += int64(len(b))
	}
	if err := manifest.Write(config.OSFS{}, manifest.Path(dir), m); err != nil {
		r.t.Fatal(err)
	}
	r.rebuild()
	r.declare(rootID, ready.Item{ID: s.ref.ID, Kind: s.ref.Kind, Tier: s.tier, Survival: s.survival, Status: ready.StatusVerified, Version: s.version})
	return dir
}

// declare adds an item to the readiness report of a root (replacing one with the same id).
func (r *contentRig) declare(rootID string, it ready.Item) {
	rep := r.ready.reports[rootID]
	rep.Root.ID = rootID
	var items []ready.Item
	for _, x := range rep.Items {
		if x.ID != it.ID {
			items = append(items, x)
		}
	}
	rep.Items = append(items, it)
	r.ready.reports[rootID] = rep
}

// refs of the items the tests install.
var (
	refSurvivalModel = ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EngineLlama, ID: "brain-main"}
	refOtherModel    = ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EngineLlama, ID: "brain-extra"}
	refWhisper       = ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EngineWhisper, ID: "whisper-main"}
	refPiper         = ark.ItemRef{Kind: manifest.KindModel, Engine: ark.EnginePiper, ID: "voice-en"}
	refMap           = ark.ItemRef{Kind: manifest.KindMaps, ID: "map-region"}
	refMapAssets     = ark.ItemRef{Kind: manifest.KindMaps, ID: ark.MapAssetsID}
	refZimTier1      = ark.ItemRef{Kind: manifest.KindKiwix, ID: "zim-water"}
	refZimTier2      = ark.ItemRef{Kind: manifest.KindKiwix, ID: "zim-extra"}
	refKit           = ark.ItemRef{Kind: manifest.KindKit, ID: "kit-tool", Subdir: "tools"}
	refDocs          = ark.ItemRef{Kind: manifest.KindDocs, ID: "docs-bundle"}
)

// installNode installs the usual content of a node on the internal root: a survival model, a spare
// model, whisper, a voice, a map with its assets, a tier-1 and a tier-2 ZIM and this node's kit part.
func (r *contentRig) installNode() {
	r.t.Helper()
	old := epoch.Add(-60 * 24 * time.Hour)
	r.install(ark.InternalID, itemSpec{ref: refSurvivalModel, tier: 1, survival: true, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refOtherModel, tier: 2, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refWhisper, tier: 1, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refPiper, tier: 1, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refMap, tier: 1, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refMapAssets, tier: 1, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refZimTier1, tier: 1, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refZimTier2, tier: 2, lastRe: old})
	r.install(ark.InternalID, itemSpec{ref: refKit, tier: 1, lastRe: old})
}

func engineOf(id string) string {
	switch id {
	case refWhisper.ID:
		return "whisper"
	case refPiper.ID:
		return "piper"
	}
	return "llama"
}
