package docsrender

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark/text"
)

// NodeState is what the renderer knows about this node that is not in the inventory: the
// placeholders <NODE_ID>, <EMERGENCY_NUMBER>, <BRAINS>, <CATALOG_VERSION>, <RENDER_DATE> and the
// fallback for <DESKTOP_USER> (aictl.md 4.22 note 2). They are filled on the survival card only:
// the other documents use the same names as prose (for example catalog-<CATALOG_VERSION>).
type NodeState struct {
	NodeID          string
	EmergencyNumber string
	CatalogVersion  string
	DesktopUser     string
	// Brains are the display names of the installed models.
	Brains []string
}

// CardTitle is the title of the card page and its index entry.
const (
	cardTitleEN = "Survival card"
	cardTitleES = "Tarjeta de supervivencia"
)

// CardFileName is the card's file name for a node, without extension: survival-card-<node-id>.
func CardFileName(nodeID string) string { return "survival-card-" + nodeID }

// SecretPlaceholders returns the placeholders of template that are secret (built-in list, or a field
// in secret_fields of the example inventory), sorted. The survival card must have none.
func SecretPlaceholders(template []byte, pairs *Pairs) []string {
	var out []string
	for _, name := range FindPlaceholders(string(template)) {
		if pairs.IsSecret(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// cardValues is the placeholder map of the card: the inventory values for this node, then the node state.
func cardValues(pairs *Pairs, st NodeState, now time.Time) map[string]string {
	v := pairs.Values(st.NodeID)
	if v["DESKTOP_USER"] == "" && st.DesktopUser != "" {
		v["DESKTOP_USER"] = st.DesktopUser
	}
	set := func(k, val string) {
		if val != "" {
			v[k] = val
		}
	}
	brains := strings.Join(st.Brains, ", ")
	if brains == "" {
		brains = "none yet / ninguno todavía"
	}
	set("NODE_ID", st.NodeID)
	set("EMERGENCY_NUMBER", st.EmergencyNumber)
	set("BRAINS", brains)
	set("BRAINS_INSTALLED", brains) // the name the committed template uses
	set("CATALOG_VERSION", st.CatalogVersion)
	set("RENDER_DATE", now.Format("2006-01-02"))
	return v
}

// BuildCard renders the survival card template for a node to a self-contained HTML page. A secret
// placeholder in the template is refused before anything is rendered (an *Error of KindCardSecret
// that names the placeholders and never a value).
func BuildCard(template []byte, pairs *Pairs, st NodeState, now time.Time) ([]byte, error) {
	if secret := SecretPlaceholders(template, pairs); len(secret) > 0 {
		return nil, &Error{
			Kind: KindCardSecret, Path: CardTemplateRel, Placeholders: secret,
			Msg: "the survival card would show a secret value (" + strings.Join(secret, ", ") + ")",
		}
	}
	s := prepare(template)
	body, subs := protect(s.body, cardValues(pairs, st, now))
	md := newMarkdown(subs)
	src := []byte(body)
	node := md.Parser().Parse(text.NewReader(src))
	html, err := renderBody(md, node, src, subs)
	if err != nil {
		return nil, &Error{Kind: KindIO, Path: CardTemplateRel, Msg: "cannot render", Err: err}
	}
	title := fmt.Sprintf("%s %s / %s", cardTitleEN, st.NodeID, cardTitleES)
	return pageHTML(title, "en", CardTemplateRel, html, pageOpts{Class: "card"}, "print.css", "card.css"), nil
}
