package docsrender

import _ "embed"

// The CSS is embedded, so the pages need nothing from the network: no web font, no CDN, no script
// (offline rule, AGENTS.md rule 3; the CSP of /docs/ is default-src 'self', agent.md 3.11.3).

//go:embed assets/print.css
var printCSS string

//go:embed assets/card.css
var cardCSS string
