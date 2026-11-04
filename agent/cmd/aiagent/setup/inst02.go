package setup

// The INST-02 steps (installer.md section 3.5): preflight, profile-suggest and catalog. They register in the
// step table of setup.go. The read-only ones also run as a normal user (preflight reports LAI-INS-003 itself),
// so the dispatcher in cmd/aiagent lets them past its root gate; catalog checks root itself.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/setup/detect"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// ExitRefused is what preflight exits with when this machine is not supported (LAI-INS-001/002/003).
const (
	ExitRefused  = 3
	catalogDir   = "/etc/localai/catalog"
	internetURL  = "https://github.com"
	internetWait = 5 * time.Second
)

func init() {
	register("preflight", func(ctx context.Context, e *Env, args []string) int {
		return runInst02(ctx, inst02Deps(e), append([]string{"preflight"}, args...), e.Stdout, e.Stderr)
	})
	register("profile-suggest", func(ctx context.Context, e *Env, args []string) int {
		return runInst02(ctx, inst02Deps(e), append([]string{"profile-suggest"}, args...), e.Stdout, e.Stderr)
	})
	register("catalog", func(ctx context.Context, e *Env, args []string) int {
		return runInst02(ctx, inst02Deps(e), append([]string{"catalog"}, args...), e.Stdout, e.Stderr)
	})
}

// newInst02Deps builds the dependencies of the steps; tests replace it.
var newInst02Deps = RealDeps

func inst02Deps(e *Env) Deps {
	d := newInst02Deps()
	d.IsRoot = e.IsRoot
	if d.CatalogDir == catalogDir && e.Root != "" {
		d.CatalogDir = e.Root + catalogDir
	}
	return d
}

// Deps is everything the steps touch, so tests replace all of it.
type Deps struct {
	Env detect.Env
	// Internet returns nil when the internet answers (preflight, online installs).
	Internet func(ctx context.Context) error
	// KitCheck verifies the kit of the ark content root <root> for this computer and returns what is
	// missing or damaged, in plain words (an empty list means complete).
	KitCheck func(ctx context.Context, root string, facts kit.NodeFacts) ([]string, error)
	// CatalogDir is the live catalog directory ("/etc/localai/catalog").
	CatalogDir string
	// FetchRelease downloads the stable catalog from the release into dir (online install).
	FetchRelease func(ctx context.Context, dir string) error
	// IsRoot reports root (nil: the real euid).
	IsRoot func() bool
}

func (d Deps) rootOK() bool {
	if d.IsRoot != nil {
		return d.IsRoot()
	}
	return os.Geteuid() == 0
}

// RealDeps is the wiring for this machine.
func RealDeps() Deps {
	return Deps{
		Env:          detect.Real(),
		Internet:     checkInternet,
		KitCheck:     verifyKit,
		CatalogDir:   catalogDir,
		FetchRelease: fetchRelease,
	}
}

func checkInternet(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, internetWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, internetURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: internetWait}).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("answered %s", resp.Status)
	}
	return nil
}

// verifyKit runs the kit verifier in quick mode (no hashing of the big items: the installer verifies the
// agent binary itself in P2 and every later step verifies what it installs) and keeps the findings that
// are about this computer.
func verifyKit(ctx context.Context, root string, facts kit.NodeFacts) ([]string, error) {
	rep, err := kit.Verifier{FS: kit.OSFS{}}.Verify(ctx, root, kit.Options{
		Mode: kit.ModeQuick, Now: time.Now(), RootID: "ark", Node: &facts, Keys: update.EmbeddedKeys(),
	})
	if err != nil {
		if errors.Is(err, kit.ErrNoKit) {
			return []string{"the kit folder (kit/)"}, nil
		}
		return nil, err
	}
	var out []string
	if rep.Damaged() {
		out = append(out, "damaged kit files (run aictl kit verify on a working computer)")
	}
	for _, f := range rep.NodeProblems() {
		what := f.Item
		if what == "" && len(f.Names) > 0 {
			what = f.Names[0]
		}
		if what == "" {
			what = f.Need
		}
		if what == "" {
			what = f.Detail
		}
		out = append(out, what)
	}
	return out, nil
}

const inst02Usage = `usage: aiagent setup <step> [--dry-run] [--json] [step flags]
steps:
  preflight [--json] [--from-ark <mount>] [--desktop-user <name>] [--yes] [--min-free-bytes <n>]
  profile-suggest [--json] [--profile <id>] [--backend <b>] [--catalog <dir>]
  catalog --from <release|ark> [--ark <mount>] [--dir <catalog folder>] [--dry-run]
`

// runInst02 dispatches one of the three steps (args[0] is the step name).
func runInst02(ctx context.Context, d Deps, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, inst02Usage)
		return ExitUsage
	}
	switch args[0] {
	case "preflight":
		return runPreflight(ctx, d, args[1:], stdout, stderr)
	case "profile-suggest":
		return runProfileSuggest(ctx, d, args[1:], stdout, stderr)
	case "catalog":
		return runCatalog(ctx, d, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "aiagent setup: unknown step %q\n%s", args[0], inst02Usage)
		return ExitUsage
	}
}

// newFlags makes a quiet FlagSet whose errors go to stderr.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("setup "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

