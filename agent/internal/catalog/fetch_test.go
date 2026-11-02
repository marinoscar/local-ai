package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bundleOf turns a catalog directory into a Bundle like the cloud serves it.
func bundleOf(t *testing.T, dir string) *Bundle {
	t.Helper()
	b := &Bundle{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		b.Files = append(b.Files, BundleFile{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(sum[:]), Content: string(data)})
		if rel == FileVersion {
			b.Version = strings.TrimSpace(string(data))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fakeCloud struct {
	b     *Bundle
	err   error
	calls int
}

func (f *fakeCloud) FetchCatalog(context.Context) (*Bundle, error) { f.calls++; return f.b, f.err }

type fakeGitHub struct {
	b       *Bundle
	err     error
	calls   int
	channel string
}

func (f *fakeGitHub) FetchCatalog(_ context.Context, channel string) (*Bundle, error) {
	f.calls++
	f.channel = channel
	return f.b, f.err
}

func newFetcher(t *testing.T) *Fetcher {
	t.Helper()
	live := liveDir(t)
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Fetcher{Dir: live, TempDir: t.TempDir()}
}

func TestStartupPriority(t *testing.T) {
	t.Run("the live copy is offline", func(t *testing.T) {
		f := newFetcher(t)
		mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
		l, err := f.Startup()
		if err != nil || l.Source != SourceOffline || l.Catalog.Version != v1 || l.Dir != f.Dir {
			t.Fatalf("%+v %v", l, err)
		}
	})
	t.Run("a TEST marker makes it a test catalog", func(t *testing.T) {
		f := newFetcher(t)
		mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{Test: true})
		l, err := f.Startup()
		if err != nil || l.Source != SourceTest {
			t.Fatalf("%+v %v", l, err)
		}
	})
	t.Run("a damaged live copy falls back to the kit and says so", func(t *testing.T) {
		f := newFetcher(t)
		mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
		applyEdits(t, f.Dir, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
		kit := versionedDir(t, "2026.10.01-1")
		f.KitDirs = []string{filepath.Join(t.TempDir(), "missing"), kit}
		l, err := f.Startup()
		if l.Source != SourceKit || l.Catalog.Version != "2026.10.01-1" || l.Dir != kit {
			t.Fatalf("%+v", l)
		}
		var le *LoadError
		if !errors.As(err, &le) || le.Code() != CodeUnparseable {
			t.Fatalf("the advisory error must be LAI-CAT-001, got %v", err)
		}
	})
	t.Run("a missing live copy falls back to the kit", func(t *testing.T) {
		f := newFetcher(t)
		f.KitDirs = []string{versionedDir(t, v2)}
		l, err := f.Startup()
		if l.Source != SourceKit || l.Catalog.Version != v2 || err == nil {
			t.Fatalf("%+v %v", l, err)
		}
	})
	t.Run("nothing loads: an empty catalog and LAI-CAT-001", func(t *testing.T) {
		f := newFetcher(t)
		f.KitDirs = []string{t.TempDir()}
		l, err := f.Startup()
		var le *LoadError
		if l.Catalog == nil || l.Source != "" || len(l.Catalog.Models) != 0 || !errors.As(err, &le) {
			t.Fatalf("%+v %v", l, err)
		}
	})
	t.Run("an install interrupted between renames is repaired at start", func(t *testing.T) {
		f := newFetcher(t)
		copyTree(t, versionedDir(t, v1), f.Dir+".prev") // live moved away, new not yet in place
		l, err := f.Startup()
		if err != nil || l.Source != SourceOffline || l.Catalog.Version != v1 {
			t.Fatalf("%+v %v", l, err)
		}
	})
}

func TestRefreshFromCloud(t *testing.T) {
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
	cloud := &fakeCloud{b: bundleOf(t, versionedDir(t, v2))}
	gh := &fakeGitHub{}
	f.Cloud, f.GitHub = cloud, gh
	res, err := f.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceCloud || res.Previous != v1 || !res.Changed || res.Catalog.Version != v2 || gh.calls != 0 {
		t.Fatalf("%+v github calls %d", res, gh.calls)
	}
	if versionOf(t, f.Dir) != v2 || versionOf(t, f.Dir+".prev") != v1 {
		t.Error("install did not swap")
	}
	if entries, _ := os.ReadDir(f.TempDir); len(entries) != 0 {
		t.Errorf("staging directory left behind: %v", entries)
	}
}

func TestRefreshFallsBackToGitHub(t *testing.T) {
	f := newFetcher(t)
	f.Cloud = &fakeCloud{err: errors.New("cloud unreachable")}
	gh := &fakeGitHub{b: bundleOf(t, versionedDir(t, v2))}
	f.GitHub = gh
	res, err := f.Refresh(context.Background())
	if err != nil || res.Source != SourceGitHub || res.Previous != "" || !res.Changed || gh.channel != "stable" {
		t.Fatalf("%+v %v channel %q", res, err, gh.channel)
	}
	f.Channel = "catalog-" + v2
	if _, err := f.Refresh(context.Background()); err != nil || gh.channel != "catalog-"+v2 {
		t.Fatalf("channel %q err %v", gh.channel, err)
	}
}

func TestRefreshSameVersionIsNotAChange(t *testing.T) {
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
	f.GitHub = &fakeGitHub{b: bundleOf(t, versionedDir(t, v1))}
	res, err := f.Refresh(context.Background())
	if err != nil || res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
}

// LAI-CAT-002: a bundle that fails lint is rejected, the previous catalog stays, and no lower
// priority source is asked.
func TestRefreshRejectedBundleKeepsThePreviousCatalog(t *testing.T) {
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
	before := snapshot(t, f.Dir)
	bad := versionedDir(t, v2)
	applyEdits(t, bad, []edit{{fM, "revision: fedcba9876543210fedcba9876543210fedcba98", "revision: main"}})
	gh := &fakeGitHub{b: bundleOf(t, versionedDir(t, "2026.11.05-1"))}
	f.Cloud, f.GitHub = &fakeCloud{b: bundleOf(t, bad)}, gh
	res, err := f.Refresh(context.Background())
	var re *RejectedError
	if res != nil || !errors.As(err, &re) || re.Code() != CodeRejected {
		t.Fatalf("%v %v", res, err)
	}
	if gh.calls != 0 {
		t.Error("GitHub was consulted after the cloud's catalog was rejected")
	}
	if !reflect.DeepEqual(snapshot(t, f.Dir), before) {
		t.Error("live catalog changed")
	}
}

func TestRefreshDamagedBundlesAreSkipped(t *testing.T) {
	good := bundleOf(t, versionedDir(t, v2))
	tampered := bundleOf(t, versionedDir(t, v2))
	tampered.Files[0].Content += "\n# changed after hashing\n"
	traversal := bundleOf(t, versionedDir(t, v2))
	traversal.Files = append(traversal.Files, BundleFile{Path: "../evil.yaml", Content: "x"})
	dup := bundleOf(t, versionedDir(t, v2))
	dup.Files = append(dup.Files, dup.Files[0])
	huge := bundleOf(t, versionedDir(t, v2))
	huge.Files[0].SHA256 = ""
	huge.Files[0].Content = strings.Repeat("x", MaxBundleBytes+1)

	for name, bad := range map[string]*Bundle{"sha mismatch": tampered, "path traversal": traversal, "duplicate": dup, "too large": huge, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			f := newFetcher(t)
			mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
			f.Cloud = &fakeCloud{b: bad}
			// alone: the refresh fails and the previous catalog stays
			if _, err := f.Refresh(context.Background()); err == nil || errors.Is(err, ErrRejected) {
				t.Fatalf("a damaged bundle must be a fetch failure, got %v", err)
			}
			if versionOf(t, f.Dir) != v1 {
				t.Error("live catalog changed")
			}
			// with GitHub behind it: skipped, the next source is used
			f.GitHub = &fakeGitHub{b: good}
			res, err := f.Refresh(context.Background())
			if err != nil || res.Source != SourceGitHub {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}

func TestRefreshNoSource(t *testing.T) {
	f := newFetcher(t)
	if _, err := f.Refresh(context.Background()); !errors.Is(err, ErrNoSource) {
		t.Fatalf("%v", err)
	}
}

func TestRefreshAllSourcesFail(t *testing.T) {
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{})
	f.Cloud = &fakeCloud{err: errors.New("cloud down")}
	f.GitHub = &fakeGitHub{err: errors.New("rate limited")}
	_, err := f.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cloud down") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("%v", err)
	}
	if versionOf(t, f.Dir) != v1 {
		t.Error("live catalog changed")
	}
}

func TestRefreshEndsATestCatalog(t *testing.T) {
	f := newFetcher(t)
	mustInstall(t, f.Dir, versionedDir(t, v1), InstallOptions{Test: true})
	f.GitHub = &fakeGitHub{b: bundleOf(t, versionedDir(t, v2))}
	if _, err := f.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if IsTestCatalog(f.Dir) {
		t.Error("TEST marker survived a successful refresh")
	}
}

func TestBundleJSONShapeMatchesTheCloud(t *testing.T) {
	// cloud-app.md 3.6: the agent decodes data.{version,bundleSha256,files[{path,sha256,content}]}.
	var b Bundle
	if err := json.Unmarshal([]byte(`{"version":"2026.11.02-1","bundleSha256":"x","approvedAt":"t","files":[{"path":"CATALOG_VERSION","sha256":"","content":"2026.11.02-1\n"}]}`), &b); err != nil {
		t.Fatal(err)
	}
	if b.Version != v1 || len(b.Files) != 1 || b.Files[0].Path != "CATALOG_VERSION" || b.BundleSHA256 != "x" {
		t.Errorf("%+v", b)
	}
}
