package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/cli/local"
)

// `aictl catalog restore` and `aictl catalog use <dir> --test` (aictl.md sections 4.11.3 and
// 4.11.4). Both run in this process as root and work with the agent stopped: they swap
// /etc/localai/catalog atomically (catalog.Install through package local) and, only when the agent
// answers, wait until GET /catalog reports the new version. `restore` is the offline fix for
// LAI-CAT-001; `use --test` installs a draft so `aictl bench` can measure a new entry.

// catalogWaitTimeout and catalogWaitEvery bound the wait for the agent to pick a swapped catalog up
// (it re-reads CATALOG_VERSION every 10 s); tests shorten them.
var (
	catalogWaitTimeout = 30 * time.Second
	catalogWaitEvery   = time.Second
)

func catalogRestoreCommand() *Command {
	return &Command{
		Name:    "restore",
		Summary: "Put a last good catalog back (from the ark kit, or the one before a test catalog).",
		Description: `Copies the last good catalog into /etc/localai/catalog: from the ark kit on the internal disk or
a mounted AI-ARK drive, or, after "catalog use --test", the catalog that was in use before it.
The copy is checked first; if it is not valid the catalog in use is left untouched. The replaced
catalog is kept as /etc/localai/catalog.prev. Works with no agent and no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.11.3`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: 30 * time.Second,
		Run:     catalogRestore,
	}
}

func catalogUseCommand() *Command {
	return &Command{
		Name:    "use",
		Summary: "Install a draft catalog directory on a test node (needs --test).",
		Usage:   "<dir> --test [--yes]",
		Description: `Checks <dir> with the catalog linter, asks for confirmation, keeps the current catalog as
/etc/localai/catalog.prev and installs <dir> as the catalog in use, marked as a test catalog.
"sudo aictl catalog restore" puts the previous catalog back. --test is required: there is no
other mode. Works with no agent and no network. Run it with sudo.
Specification: docs/specs/aictl.md section 4.11.4`,
		Marks:   MarkLocal | MarkRoot,
		Timeout: 30 * time.Second,
		Flags: func(fs *flag.FlagSet) {
			fs.Bool("test", false, "install the directory as a test catalog (required)")
			declareYes(fs)
		},
		Run: catalogUse,
	}
}

func catalogRestore(c *Call) error {
	if err := noExtraArgs(c); err != nil {
		return err
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	res, rerr := local.RestoreCatalog(c.Ctx, env)
	release()
	mapped := localErr(c, rerr)
	target := map[string]string{}
	if res != nil {
		target["version"], target["from"] = res.Version, res.From
		if res.Root != nil {
			target["root"] = res.Root.Path
		}
	}
	recordLocal(c, env, local.ActionCatalogRestore, mapped, target)
	if mapped != nil {
		return mapped
	}
	if err := waitAgentCatalog(c, res.Version, ""); err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"version": res.Version, "previous": res.Previous, "from": res.From})
	}
	c.Out.OK(restoredMessage(c, res))
	return nil
}

// restoredMessage is the success line of `catalog restore`.
func restoredMessage(c *Call, res *local.CatalogResult) string {
	switch {
	case res.From == local.FromPrevious:
		return fmt.Sprintf(pick(c.Lang,
			"Restored catalog %s, the one that was in use before the test catalog.",
			"Se restauró el catálogo %s, el que estaba en uso antes del catálogo de prueba."), res.Version)
	case res.Root != nil && res.Root.Kind == local.KindDrive:
		return fmt.Sprintf(pick(c.Lang,
			"Restored catalog %s from the ark kit on the drive at %s.",
			"Se restauró el catálogo %s desde el kit del arca en el disco de %s."), res.Version, res.Root.Mount)
	}
	return fmt.Sprintf(pick(c.Lang,
		"Restored catalog %s from the ark kit on the internal disk.",
		"Se restauró el catálogo %s desde el kit del arca en el disco interno."), res.Version)
}

func catalogUse(c *Call) error {
	applyYes(c)
	switch {
	case len(c.Args) == 0:
		return c.usageErr(pick(c.Lang, "A catalog directory is required.", "Se necesita un directorio de catálogo."),
			pick(c.Lang, "Run `sudo aictl catalog use <dir> --test`.", "Ejecute `sudo aictl catalog use <dir> --test`."))
	case len(c.Args) > 1:
		return c.usageErr(fmt.Sprintf(pick(c.Lang, "Unexpected argument %q (one directory only).", "Argumento inesperado %q (solo un directorio)."), c.Args[1]),
			pick(c.Lang, "Run `sudo aictl catalog use <dir> --test`.", "Ejecute `sudo aictl catalog use <dir> --test`."))
	case !c.Bool("test"):
		return c.usageErr(pick(c.Lang, "`catalog use` needs --test: there is no other mode.", "`catalog use` necesita --test: no hay otro modo."),
			pick(c.Lang, "Run `sudo aictl catalog use <dir> --test`.", "Ejecute `sudo aictl catalog use <dir> --test`."))
	}
	if err := RequireRoot(c); err != nil {
		return err
	}
	dir := c.Args[0]

	// Lint first, exactly as `catalog lint` prints it; a failing catalog never reaches the node.
	rep := lintCatalog(dir, catalog.LintOptions{Context: c.Ctx})
	if !rep.Valid() {
		printLintReport(c, dir, rep, c.Global.JSON)
		return ExitWith(ExitUsage)
	}
	ok, err := c.Confirm(fmt.Sprintf(pick(c.Lang,
		"Use the catalog in %s as a test catalog on this node? The current catalog is kept and `sudo aictl catalog restore` puts it back.",
		"¿Usar el catálogo de %s como catálogo de prueba en este nodo? El catálogo actual se conserva y `sudo aictl catalog restore` lo devuelve."), dir))
	if err != nil {
		return err
	}
	if !ok {
		if c.Global.JSON {
			return c.Out.JSON().Document(map[string]any{"cancelled": true})
		}
		c.Out.Info(pick(c.Lang, "Cancelled. Nothing was changed.", "Cancelado. No se cambió nada."))
		return nil
	}

	env := localEnv()
	release, err := local.Lock(env)
	if err != nil {
		return localErr(c, err)
	}
	res, uerr := local.UseTestCatalog(env, dir)
	release()
	mapped := localErr(c, uerr)
	target := map[string]string{"dir": dir}
	if res != nil {
		target["version"], target["previous"] = res.Version, res.Previous
	}
	recordLocal(c, env, local.ActionCatalogUseTest, mapped, target)
	if mapped != nil {
		return mapped
	}
	if err := waitAgentCatalog(c, res.Version, string(catalog.SourceTest)); err != nil {
		return err
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(map[string]any{"version": res.Version, "source": string(catalog.SourceTest), "previous": res.Previous})
	}
	c.Out.OK(fmt.Sprintf(pick(c.Lang,
		"Test catalog %s is in use (from %s). Undo with `sudo aictl catalog restore`.",
		"El catálogo de prueba %s está en uso (desde %s). Para deshacerlo: `sudo aictl catalog restore`."), res.Version, dir))
	return nil
}

// waitAgentCatalog waits until GET /catalog reports version (and source, when given). It is the
// last step of a swap and only matters when the agent runs: if the agent does not answer at all it
// returns at once and quietly, because the agent will read the catalog when it starts; if it
// answers but does not report the new catalog within catalogWaitTimeout, that is a warning, not a
// failure (the swap is done and the agent re-reads every 10 s). A client that cannot even be built
// (a bad --url) is skipped: local commands work with any transport setting. Ctrl-C stops the wait.
func waitAgentCatalog(c *Call, version, source string) error {
	cl, err := c.Client()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.Ctx, catalogWaitTimeout)
	defer cancel()
	for first := true; ; first = false {
		var got struct {
			Version string `json:"version"`
			Source  string `json:"source"`
		}
		_, err := cl.Do(ctx, http.MethodGet, "/catalog", nil, &got)
		switch {
		case err == nil && got.Version == version && (source == "" || got.Source == source):
			return nil
		case err != nil && first && agentNotThere(err):
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(catalogWaitEvery):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if errors.Is(c.Ctx.Err(), context.Canceled) {
		c.Out.Info(pick(c.Lang, "The catalog is installed; the agent will pick it up within 10 seconds.",
			"El catálogo está instalado; el agente lo tomará en unos 10 segundos."))
		return ExitWith(ExitInterrupted)
	}
	c.Out.Warn(pick(c.Lang,
		"The catalog is installed, but the agent has not reported it yet. It re-reads the catalog every 10 seconds: check `aictl status` in a moment.",
		"El catálogo está instalado, pero el agente todavía no lo informa. Lo vuelve a leer cada 10 segundos: revise `aictl status` en un momento."))
	return nil
}

// agentNotThere reports whether err means the agent is not running (or not reachable at all).
func agentNotThere(err error) bool {
	var ce *CLIError
	return errors.As(err, &ce) && (ce.Code == CodeUnreachable || ce.Code == CodePermission)
}
