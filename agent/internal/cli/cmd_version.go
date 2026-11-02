package cli

import (
	"runtime"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/app"
	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// `aictl version` (aictl.md section 4.26): the first line is aictl's own build and needs nothing;
// the second asks the agent (GET /health, then GET /status for the llama.cpp build and the catalog).
// An agent that is not running is not an error here: the line says so and the exit code is 0, so the
// command works as a quick "what is installed" check on a broken node. A version mismatch shows the
// framework's skew warning on stderr after the lines.

func init() {
	Register(&Command{
		Name:    "version",
		Summary: "Show the aictl and agent versions.",
		Description: `Prints this program's version and, if the agent answers, the agent's version, the llama.cpp build and the catalog version.
Exit 0 even when the agent is not reachable.
--json prints {"cli": {...}, "agent": {"version": ...} or null, "llama_build": ..., "catalog_version": ...}.`,
		Timeout: 5 * time.Second,
		Run:     runVersion,
	})
}

// versionInfo is what the version command knows, for the renderer and the JSON document.
type versionInfo struct {
	CLI struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		BuiltAt string `json:"built_at"`
	} `json:"cli"`
	Agent          *versionAgent `json:"agent"`
	LlamaBuild     *string       `json:"llama_build"`
	CatalogVersion *string       `json:"catalog_version"`
}

type versionAgent struct {
	Version string `json:"version"`
}

func runVersion(c *Call) error {
	if err := roNoArgs(c); err != nil {
		return err
	}
	var v versionInfo
	v.CLI.Version = c.env.Version
	v.CLI.Commit, v.CLI.BuiltAt = app.Commit, app.BuiltAt
	v.CLI.OS, v.CLI.Arch = runtime.GOOS, runtime.GOARCH

	cl, err := c.Client()
	if err != nil {
		return err // a bad --url is a usage error, not "agent down"
	}
	h, err := cl.Health(c.Ctx)
	switch {
	case err == nil:
		v.Agent = &versionAgent{Version: h.Version}
		var st wireStatus
		if _, serr := roGetCtx(c, c.Ctx, "/status", &st); serr == nil {
			if st.Node.LlamaBuild != "" {
				v.LlamaBuild = &st.Node.LlamaBuild
			}
			if st.Node.CatalogVer != "" {
				v.CatalogVersion = &st.Node.CatalogVer
			}
		} else if c.Ctx.Err() != nil {
			return serr // the deadline or Ctrl-C, not a missing detail
		}
	case ExitFor(err) == ExitUnreachable:
		// Not reachable: say so and succeed.
	default:
		return err // not allowed, timed out, undecodable: the framework explains it
	}
	if c.Global.JSON {
		return c.Out.JSON().Document(v)
	}
	renderVersion(c.Out, &v)
	return nil
}

func versionLine(v *versionInfo) string {
	var parts []string
	if v.CLI.Commit != "" {
		parts = append(parts, v.CLI.Commit)
	}
	parts = append(parts, v.CLI.OS+"/"+v.CLI.Arch)
	if v.CLI.BuiltAt != "" {
		parts = append(parts, "built "+v.CLI.BuiltAt)
	}
	return "aictl " + v.CLI.Version + " (" + strings.Join(parts, ", ") + ")"
}

func renderVersion(p *out.Printer, v *versionInfo) {
	tr := func(en, es string) string { return pick(p.Lang(), en, es) }
	p.Result(versionLine(v))
	if v.Agent == nil {
		p.Result("agent  " + tr("not reachable", "no responde"))
		return
	}
	parts := []string{"agent " + v.Agent.Version}
	if v.LlamaBuild != nil {
		parts = append(parts, "llama.cpp "+*v.LlamaBuild)
	}
	if v.CatalogVersion != nil {
		parts = append(parts, tr("catalog ", "catálogo ")+*v.CatalogVersion)
	}
	p.Result(strings.Join(parts, roDot(p)))
}
