package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// fetchRelease downloads the stable catalog of the GitHub release into dir (installer.md P2).
func fetchRelease(ctx context.Context, dir string) error {
	b, err := catalog.NewGitHub("").FetchCatalog(ctx, catalog.ChannelStable)
	if err != nil {
		return err
	}
	return b.Materialize(dir)
}

// InstallCatalog installs the catalog from "release" or "ark" into d.CatalogDir. It is idempotent: the
// same version already installed is left alone. With dryRun it fetches and lints but changes nothing
// under the live catalog folder. It returns a plain-language line.
func InstallCatalog(ctx context.Context, d Deps, from, arkMount string, dryRun bool) (string, error) {
	var src string
	switch from {
	case "ark":
		if arkMount == "" {
			return "", errors.New("--from ark needs --ark <mount>")
		}
		src = filepath.Join(arkMount, "localai", "kit", "catalog")
		if _, err := os.Stat(filepath.Join(src, catalog.FileVersion)); err != nil {
			return "", fmt.Errorf("the ark drive has no catalog at %s: sync the drive on a computer with internet first", src)
		}
	case "release":
		if d.FetchRelease == nil {
			return "", errors.New("no release source is configured")
		}
		tmp, err := os.MkdirTemp("", "lai-catalog-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := d.FetchRelease(ctx, tmp); err != nil {
			return "", fmt.Errorf("could not download the catalog: %w", err)
		}
		src = tmp
	default:
		return "", fmt.Errorf("--from must be release or ark, not %q", from)
	}
	rep := catalog.Lint(src, catalog.LintOptions{})
	if !rep.Valid() {
		return "", &catalog.RejectedError{Report: rep}
	}
	newV := readVersion(src)
	if cur := readVersion(d.CatalogDir); cur != "" && cur == newV {
		return fmt.Sprintf("catalog %s is already installed in %s", newV, d.CatalogDir), nil
	}
	if dryRun {
		return fmt.Sprintf("[dry-run] would install catalog %s from %s into %s", newV, from, d.CatalogDir), nil
	}
	if err := os.MkdirAll(filepath.Dir(d.CatalogDir), 0o755); err != nil {
		return "", err
	}
	res, err := catalog.Install(d.CatalogDir, src, catalog.InstallOptions{})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("installed catalog %s from %s into %s", res.Version, from, d.CatalogDir), nil
}

func readVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, catalog.FileVersion))
	if err != nil {
		return ""
	}
	for i, c := range b {
		if c == '\n' || c == '\r' {
			return string(b[:i])
		}
	}
	return string(b)
}

func runCatalog(ctx context.Context, d Deps, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("catalog", stderr)
	var from, ark, dir string
	var dry bool
	fs.StringVar(&from, "from", "", "release or ark")
	fs.StringVar(&ark, "ark", "", "ark mount (with --from ark)")
	fs.StringVar(&dir, "dir", "", "live catalog folder (default /etc/localai/catalog; for tests)")
	fs.BoolVar(&dry, "dry-run", false, "fetch and lint, change nothing")
	fs.BoolVar(new(bool), "json", false, "accepted; the output is one line of text")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || from == "" {
		fmt.Fprint(stderr, inst02Usage)
		return ExitUsage
	}
	if dir != "" {
		d.CatalogDir = dir
	}
	if !dry && !d.rootOK() {
		fmt.Fprintln(stderr, "aiagent setup catalog: must run as root (use sudo)")
		return ExitNotRoot
	}
	msg, err := InstallCatalog(ctx, d, from, ark, dry)
	if err != nil {
		fmt.Fprintf(stderr, "aiagent setup catalog: %v\n", err)
		return ExitFailed
	}
	fmt.Fprintln(stdout, msg)
	return ExitOK
}
