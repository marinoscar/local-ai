package cli

import (
	"net/http"
	"time"
)

// `aictl catalog refresh` (aictl.md section 4.11.2): ask the agent to fetch the newest catalog. It
// needs the network, so the agent refuses it in offline and survival mode (409 LAI-AGT-023, exit 6);
// `aictl catalog restore` is the offline fix. It replaces the "not implemented yet" stub of the
// group through RegisterLate.

// catalogRefreshed is POST /catalog/refresh (agent.md section 3.8.21).
type catalogRefreshed struct {
	Version  string  `json:"version"`
	Previous *string `json:"previous"`
	Changed  bool    `json:"changed"`
	Source   string  `json:"source"`
}

func init() {
	attachAll(at(&Command{
		Name: "refresh", Summary: "Fetch the newest catalog of models and content.", Timeout: 120 * time.Second,
		Description: "Needs the internet: it is refused in offline and survival mode. A new catalog that fails validation is\n" +
			"rejected and the previous one stays.",
		Run: catalogRefresh,
	}, "catalog"))
}

func catalogSourceText(l Lang, source string) string {
	switch source {
	case "cloud":
		return pick(l, "from the cloud", "desde la nube")
	case "github":
		return pick(l, "from GitHub", "desde GitHub")
	case "kit":
		return pick(l, "from the rebuild kit", "desde el kit de reconstrucción")
	case "test":
		return pick(l, "a test catalog", "un catálogo de prueba")
	case "offline":
		return pick(l, "the saved copy", "la copia guardada")
	}
	return source
}

func catalogRefresh(c *Call) error {
	if err := c.noArgs(); err != nil {
		return err
	}
	var r catalogRefreshed
	raw, _, err := c.call(http.MethodPost, "/catalog/refresh", map[string]any{}, &r)
	if err != nil {
		return err
	}
	if c.Global.JSON {
		return c.emitRaw(raw)
	}
	from := catalogSourceText(c.Lang, r.Source)
	switch {
	case !r.Changed:
		c.Out.Info(pick(c.Lang,
			"The catalog is already up to date ("+r.Version+").",
			"El catálogo ya está al día ("+r.Version+")."))
	case r.Previous != nil && *r.Previous != "":
		c.Out.OK(pick(c.Lang,
			"Catalog updated: "+*r.Previous+" "+c.Out.Symbols().Arrow+" "+r.Version+" ("+from+").",
			"Catálogo actualizado: "+*r.Previous+" "+c.Out.Symbols().Arrow+" "+r.Version+" ("+from+")."))
	default:
		c.Out.OK(pick(c.Lang, "Catalog updated to "+r.Version+" ("+from+").", "Catálogo actualizado a "+r.Version+" ("+from+")."))
	}
	return nil
}
