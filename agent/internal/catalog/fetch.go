package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Source says where the active catalog came from (catalog.md section 1, agent.md 3.8.21).
type Source string

// The sources a Fetcher reports.
const (
	// SourceCloud: fetched from the cloud (priority 1, node enrolled and online).
	SourceCloud Source = "cloud"
	// SourceGitHub: fetched from GitHub at a catalog-<V> tag (priority 2, not enrolled, online).
	SourceGitHub Source = "github"
	// SourceOffline: the copy kept in /etc/localai/catalog/ from an earlier fetch or import.
	SourceOffline Source = "offline"
	// SourceKit: loaded at startup from <root>/kit/catalog/ because /etc/localai/catalog/ was missing or invalid.
	SourceKit Source = "kit"
	// SourceTest: a draft installed with `aictl catalog use --test`; /etc/localai/catalog/ carries a TEST marker.
	SourceTest Source = "test"
)

// MaxBundleBytes caps a fetched bundle (the cloud caps an uploaded catalog at 2 MB, cloud-app.md 4.6).
const MaxBundleBytes = 2 << 20

// Bundle is a catalog as the remote sources deliver it: the files of the catalog directory with
// their content. It matches the cloud's GET /api/nodes/{id}/localai/catalog body (cloud-app.md 3.6).
type Bundle struct {
	Version      string       `json:"version"`
	BundleSHA256 string       `json:"bundleSha256"`
	Files        []BundleFile `json:"files"`
}

// BundleFile is one file of a Bundle. SHA256, when present, is verified before anything is written.
type BundleFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// bundlePathRe is the only set of paths a bundle may carry (cloud-app.md 4.6).
var bundlePathRe = regexp.MustCompile(`^(CATALOG_VERSION|models\.yaml|ark\.yaml|profiles/[a-z0-9-]+\.yaml)$`)

// Materialize verifies the bundle and writes its files under dir (created). It refuses a path
// outside the catalog layout, a duplicate, a sha256 that does not match the content and a bundle
// over MaxBundleBytes. It does not lint: Install does.
func (b *Bundle) Materialize(dir string) error {
	var total int
	seen := map[string]bool{}
	for _, f := range b.Files {
		if !bundlePathRe.MatchString(f.Path) {
			return fmt.Errorf("bundle file path %q is not part of the catalog layout", truncate(f.Path, 60))
		}
		if seen[f.Path] {
			return fmt.Errorf("bundle lists %s twice", f.Path)
		}
		seen[f.Path] = true
		if f.SHA256 != "" {
			sum := sha256.Sum256([]byte(f.Content))
			if hex.EncodeToString(sum[:]) != strings.ToLower(f.SHA256) {
				return fmt.Errorf("bundle file %s does not match its sha256", f.Path)
			}
		}
		if total += len(f.Content); total > MaxBundleBytes {
			return fmt.Errorf("bundle is larger than %d bytes", MaxBundleBytes)
		}
	}
	for _, f := range b.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// CloudSource is priority 1: the cloud's approved catalog. The transport (GET
// /api/nodes/{id}/localai/catalog with the node credential) is supplied by the cloud link.
type CloudSource interface {
	FetchCatalog(ctx context.Context) (*Bundle, error)
}

// GitHubSource is priority 2: the catalog at a GitHub tag. channel is node.yaml catalog.channel
// ("stable" or "catalog-<CATALOG_VERSION>"); resolving it to a tag is CAT-04's job.
type GitHubSource interface {
	FetchCatalog(ctx context.Context, channel string) (*Bundle, error)
}

// Fetcher applies the source priority of catalog.md section 1. Any of the fields may be zero.
type Fetcher struct {
	// Dir is /etc/localai/catalog.
	Dir string
	// KitDirs are the <root>/kit/catalog directories of the mounted content roots, in order.
	KitDirs []string
	// Cloud is nil when the node is not enrolled.
	Cloud CloudSource
	// GitHub is nil when GitHub must not be used.
	GitHub GitHubSource
	// Channel is node.yaml catalog.channel (default "stable").
	Channel string
	// FS is used by Install and Recover (default OSFS).
	FS FS
	// TempDir is where bundles are staged (default os.TempDir()).
	TempDir string
}

// Loaded is a catalog together with where it came from.
type Loaded struct {
	Catalog *Catalog
	Source  Source
	// Dir is the directory the catalog was read from.
	Dir string
}

// Startup is agent.md 4.1 step 8: load /etc/localai/catalog/, else <root>/kit/catalog/ of a mounted
// root, else an empty catalog. Catalog is never nil. The error is advisory: it is the reason the live
// directory was not used (a *LoadError, doctor code LAI-CAT-001) and is set even when the kit copy
// saved the day, so the agent can raise the code while it keeps running.
//
// An install interrupted between its renames is repaired first (Recover).
func (f *Fetcher) Startup() (Loaded, error) {
	_, _ = Recover(f.FS, f.Dir) // a failure here shows up as a failed load below
	cat, err := Load(f.Dir)
	if err == nil {
		src := SourceOffline
		if IsTestCatalog(f.Dir) {
			src = SourceTest
		}
		return Loaded{Catalog: cat, Source: src, Dir: f.Dir}, nil
	}
	for _, kit := range f.KitDirs {
		if kc, kerr := Load(kit); kerr == nil {
			return Loaded{Catalog: kc, Source: SourceKit, Dir: kit}, err
		}
	}
	return Loaded{Catalog: Empty()}, err
}

// RefreshResult is the outcome of a successful Refresh.
type RefreshResult struct {
	Loaded
	// Previous is the version that was active before ("" when none loaded).
	Previous string
	// Changed is false when the new version equals the previous one.
	Changed bool
}

// ErrNoSource is returned by Refresh when neither the cloud nor GitHub is configured.
var ErrNoSource = errors.New("no catalog source is configured")

// Refresh fetches a catalog and installs it. Sources are tried in priority order, cloud then GitHub;
// a source that fails (transport error, damaged bundle) is skipped. The first bundle that arrives
// intact is linted and installed: if it fails lint the refresh stops with a *RejectedError
// (LAI-CAT-002) and the previous catalog stays, without falling back to a lower-priority source.
// A test catalog (TEST marker) is replaced like any other: a successful refresh ends the test.
func (f *Fetcher) Refresh(ctx context.Context) (*RefreshResult, error) {
	type attempt struct {
		src   Source
		fetch func() (*Bundle, error)
	}
	var attempts []attempt
	if f.Cloud != nil {
		attempts = append(attempts, attempt{SourceCloud, func() (*Bundle, error) { return f.Cloud.FetchCatalog(ctx) }})
	}
	if f.GitHub != nil {
		channel := f.Channel
		if channel == "" {
			channel = "stable"
		}
		attempts = append(attempts, attempt{SourceGitHub, func() (*Bundle, error) { return f.GitHub.FetchCatalog(ctx, channel) }})
	}
	if len(attempts) == 0 {
		return nil, ErrNoSource
	}

	previous := readVersionFile(f.Dir)
	var errs []error
	for _, a := range attempts {
		b, err := a.fetch()
		if err == nil && b == nil {
			err = errors.New("empty answer")
		}
		var stage string
		if err == nil {
			stage, err = f.stage(b)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.src, err))
			continue
		}
		res, ierr := Install(f.Dir, stage, InstallOptions{FS: f.FS})
		_ = os.RemoveAll(stage)
		if ierr != nil {
			return nil, ierr
		}
		cat, lerr := Load(f.Dir)
		if lerr != nil {
			return nil, lerr
		}
		return &RefreshResult{
			Loaded:   Loaded{Catalog: cat, Source: a.src, Dir: f.Dir},
			Previous: previous,
			Changed:  res.Version != previous,
		}, nil
	}
	return nil, fmt.Errorf("catalog refresh: %w", errors.Join(errs...))
}

// stage writes a bundle into a fresh temporary directory and returns it.
func (f *Fetcher) stage(b *Bundle) (string, error) {
	dir, err := os.MkdirTemp(f.TempDir, "catalog-fetch-")
	if err != nil {
		return "", err
	}
	if err := b.Materialize(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
