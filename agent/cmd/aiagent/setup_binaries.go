package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
	"github.com/marinoscar/local-ai/agent/internal/setup/binaries"
	"github.com/marinoscar/local-ai/agent/internal/svc/kiwix"
	"github.com/marinoscar/local-ai/agent/internal/svc/piper"
)

func init() { setupSteps["binaries"] = runSetupBinaries }

// newBinariesInstaller is the kit.ToolInstaller of the real machine; tests replace it.
var newBinariesInstaller = func() binaries.Installer {
	ti := kit.NewToolInstaller(runtime.GOARCH)
	ti.Kiwix, ti.Piper = kiwix.Install, piper.Install
	return ti
}

// defaultCatalogDir is where the installer put the catalog (overview.md section 3.2).
const defaultCatalogDir = "/etc/localai/catalog"

// binariesCatalog loads the catalog for the online pins; tests replace it.
var binariesCatalog = func(dir string) (*catalog.Catalog, error) { return catalog.Load(dir) }

const setupBinariesUsage = `usage: aiagent setup binaries (--kit <dir> | --online [--catalog <dir>]) [--tools kiwix-tools,pmtiles,piper] [--dry-run] [--json]

Installs kiwix-tools, pmtiles and piper into /opt/localai/<tool>/<version>/ and makes each the current version
(the previous version is kept). Every tarball is checked against its pinned sha256 before a byte is extracted.
  --kit <dir>      offline: the kit folder of the ark drive (<mount>/localai/kit)
  --online         download the tarballs pinned in the catalog (default catalog: /etc/localai/catalog)
  --dry-run        check the files and print what would be installed; install nothing
`

func runSetupBinaries(args []string, stdout, stderr io.Writer) int {
	ctx := context.Background()
	fs := flag.NewFlagSet("setup binaries", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		dry, asJSON, online bool
		kitDir, cat, tools  string
	)
	fs.BoolVar(&dry, "dry-run", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	fs.BoolVar(&online, "online", false, "")
	fs.StringVar(&kitDir, "kit", "", "")
	fs.StringVar(&cat, "catalog", defaultCatalogDir, "")
	fs.StringVar(&tools, "tools", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || (kitDir == "") == !online {
		fmt.Fprintf(stderr, "aiagent setup binaries: give exactly one of --kit <dir> or --online (%s)\n\n%s", doctor.CLI005, setupBinariesUsage)
		return 2
	}
	var list []string
	if tools != "" {
		list = strings.Split(tools, ",")
	}
	opts := binaries.Options{Tools: list, DryRun: dry, Fetch: binaries.HTTPSFetcher{}}
	if kitDir != "" {
		opts.KitDir = filepath.Clean(kitDir)
	} else {
		c, err := binariesCatalog(cat)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR %s: cannot read the catalog in %s to find the pinned downloads: %v\n", doctor.INS035, cat, err)
			return 1
		}
		opts.Pins = binaries.PinsFromCatalog(c, list, runtime.GOARCH)
	}
	rep, err := binaries.Run(ctx, newBinariesInstaller(), opts)
	if asJSON {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, o := range rep.Tools {
			switch o.Status {
			case "would-install":
				fmt.Fprintf(stdout, "[dry-run] would install %s %s from %s\n", o.Tool, o.Version, o.Source)
			case "unchanged":
				fmt.Fprintf(stdout, "%s %s is already the current version\n", o.Tool, o.Version)
			case "installed":
				fmt.Fprintf(stdout, "installed %s %s (previous: %s)\n", o.Tool, o.Version, orNone(o.Previous))
			}
		}
	}
	if err != nil {
		code := binaries.CodeOf(err)
		fmt.Fprintf(stderr, "ERROR %s: %v\n", code, err)
		return 1
	}
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
