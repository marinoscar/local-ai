package docsrender

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"gopkg.in/yaml.v3"
)

// CardTemplateRel is the survival card template inside the docs tree (survival-portal.md 4.12).
const CardTemplateRel = "runbooks/00-survival-card.md"

// DefaultGitHubBase is where a link that leaves the docs tree points, written as plain text.
const DefaultGitHubBase = "https://github.com/marinoscar/local-ai/blob/main/"

// Kinds of document, from the top-level folder of docs/.
const (
	KindRunbook      = "runbook"
	KindSpec         = "spec"
	KindArchitecture = "architecture"
	KindDecision     = "decision"
	KindOther        = "other"
	KindCard         = "card"
)

// kindDirs maps a top-level folder to its kind, in the order the index lists them.
var kindDirs = []struct{ dir, kind string }{
	{"runbooks", KindRunbook},
	{"specs", KindSpec},
	{"architecture", KindArchitecture},
	{"decisions", KindDecision},
}

// skippedDirs are not rendered: source/ is the redacted archive "for history only" (docs/README.md).
var skippedDirs = map[string]bool{"source": true}

// ErrorKind classifies a fatal error so the CLI can map it to a doctor code.
type ErrorKind int

const (
	// KindIO: a file or directory could not be read or written.
	KindIO ErrorKind = iota + 1
	// KindUsage: the arguments are wrong (a refused output directory, no source).
	KindUsage
	// KindCardPages: the survival card does not fit on one page.
	KindCardPages
	// KindCardSecret: the survival card template holds a secret placeholder.
	KindCardSecret
	// KindInventory: inventory.local.yaml or inventory.example.yaml is not valid YAML.
	KindInventory
)

// Error is a fatal error of the renderer.
type Error struct {
	Kind ErrorKind
	// Path is the file or directory concerned.
	Path string
	// Placeholders are the secret placeholder names of KindCardSecret (names only, never values).
	Placeholders []string
	Msg          string
	Err          error
}

func (e *Error) Error() string {
	s := e.Msg
	if e.Path != "" {
		s += ": " + e.Path
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func ioError(msg, p string, err error) *Error {
	return &Error{Kind: KindIO, Path: p, Msg: msg, Err: err}
}

// WarningKind classifies a non-fatal finding.
type WarningKind int

const (
	// WarnBrokenLink: a link in a document points at a file or heading that does not exist.
	WarnBrokenLink WarningKind = iota + 1
	// WarnDocument: a document had a problem the renderer worked around (bad front matter, bad UTF-8).
	WarnDocument
	// WarnNoInventory: inventory.local.yaml was not found; placeholders stay.
	WarnNoInventory
	// WarnNoPDF: no Chromium is installed; only HTML was made.
	WarnNoPDF
	// WarnPDFFailed: Chromium failed on one page; that page has no PDF.
	WarnPDFFailed
	// WarnNoCard: the survival card template is missing, so no card was made.
	WarnNoCard
	// WarnNodeUnknown: the node id is unknown; the card is named for "unconfigured".
	WarnNodeUnknown
)

// Warning is a non-fatal finding.
type Warning struct {
	Kind    WarningKind
	Path    string
	Message string
}

// Doc is one rendered page.
type Doc struct {
	Rel     string // path inside the docs tree, slash separated: runbooks/01-survival-mode.md
	Slug    string // 01-survival-mode
	Kind    string
	Title   string // the first "# " heading
	TitleES string // from front matter or a title_es comment, "" when absent
	HTML    string // file name: 01-survival-mode.html

	ids map[string]bool // heading ids of the page
}

// BuildOptions drives Build.
type BuildOptions struct {
	// Src is the docs directory.
	Src string
	// Values fills placeholders in every page (inventory values; node-state placeholders are
	// card-only and not here).
	Values map[string]string
	// CardFile is the file name the card template's links resolve to ("" when no card is made).
	CardFile string
	// GitHubBase is the URL prefix for links that leave the tree (DefaultGitHubBase when empty).
	GitHubBase string
}

// Built is the result of Build.
type Built struct {
	Pages []*Doc
	// Files holds every output file by name: <slug>.html and the CSS.
	Files    map[string][]byte
	Warnings []Warning
	// CardTemplate is the card template source (nil when the tree has none).
	CardTemplate []byte
}

type tree struct {
	files map[string]bool // every file, slash relative path
	dirs  map[string]bool
	md    []string // markdown files to render, in render order
}

func scanTree(src string) (*tree, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return nil, ioError("cannot read the documents folder", src, unwrapPath(err))
	}
	if !fi.IsDir() {
		return nil, ioError("not a folder", src, nil)
	}
	t := &tree{files: map[string]bool{}, dirs: map[string]bool{".": true}}
	var all []string
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			t.dirs[rel] = true
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !d.Type().IsRegular() {
			return nil
		}
		t.files[rel] = true
		if strings.EqualFold(path.Ext(rel), ".md") && !skippedDirs[strings.SplitN(rel, "/", 2)[0]] {
			all = append(all, rel)
		}
		return nil
	})
	if err != nil {
		return nil, ioError("cannot read the documents folder", src, unwrapPath(err))
	}
	prio := func(rel string) int {
		top := strings.SplitN(rel, "/", 2)[0]
		for i, k := range kindDirs {
			if k.dir == top && strings.Contains(rel, "/") {
				return i
			}
		}
		return len(kindDirs)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if pi, pj := prio(all[i]), prio(all[j]); pi != pj {
			return pi < pj
		}
		return all[i] < all[j]
	})
	t.md = all
	return t, nil
}

func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func kindOf(rel string) string {
	if !strings.Contains(rel, "/") {
		return KindOther
	}
	top := strings.SplitN(rel, "/", 2)[0]
	for _, k := range kindDirs {
		if k.dir == top {
			return k.kind
		}
	}
	return KindOther
}

// assignSlugs gives each page its slug: the file name without .md. When two files share a name
// (every folder has a README.md) all of them get the folder name as a prefix, so the result does not
// depend on the order of the files.
func assignSlugs(rels []string) map[string]string {
	base := func(rel string) string { return strings.TrimSuffix(path.Base(rel), path.Ext(rel)) }
	count := map[string]int{}
	for _, r := range rels {
		count[base(r)]++
	}
	out := map[string]string{}
	used := map[string]bool{}
	for _, r := range rels {
		slug := base(r)
		if count[slug] > 1 {
			dir := path.Base(path.Dir(r))
			if dir == "." {
				dir = "docs"
			}
			slug = dir + "-" + slug
		}
		for n := 2; used[slug]; n++ {
			slug = fmt.Sprintf("%s-%d", base(r), n)
		}
		used[slug] = true
		out[r] = slug
	}
	return out
}

var (
	titleESCommentRE = regexp.MustCompile(`<!--\s*title_es:\s*(.*?)\s*-->`)
	pageBreakRE      = regexp.MustCompile(`(?m)^[ \t]*<div style="page-break-after:\s*always">\s*</div>[ \t]*$`)
	schemeRE         = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)
)

const pageBreakToken = "PAGEBREAK"

// source is one document read and cleaned.
type source struct {
	body    string
	titleES string
	notes   []string // problems worked around
}

// prepare cleans a document: invalid UTF-8 and NUL bytes, front matter, the title_es comment.
func prepare(raw []byte) source {
	var s source
	text := string(raw)
	if !utf8Valid(text) {
		text = strings.ToValidUTF8(text, "�")
		s.notes = append(s.notes, "the file is not valid UTF-8; bad bytes were replaced")
	}
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		rest := text[4:]
		end := strings.Index(rest, "\n---\n")
		tail := 5
		if end < 0 && strings.HasSuffix(rest, "\n---") {
			end, tail = len(rest)-4, 4
		}
		if end < 0 {
			s.notes = append(s.notes, "front matter starts with --- but is never closed; it was left as text")
		} else {
			var fm map[string]any
			if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
				s.notes = append(s.notes, "front matter is not valid YAML; it was ignored")
			} else if v, ok := fm["title_es"].(string); ok {
				s.titleES = strings.TrimSpace(v)
			}
			text = rest[min(end+tail, len(rest)):]
		}
	}
	if m := titleESCommentRE.FindStringSubmatch(text); m != nil && s.titleES == "" {
		s.titleES = m[1]
	}
	s.body = pageBreakRE.ReplaceAllString(text, pageBreakToken)
	return s
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "") == s }

// slugify is GitHub's heading anchor rule: lowercase, keep letters, digits, marks, '_' and '-', and
// turn each space into '-' (no collapsing).
func slugify(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// plainText is the text a reader sees for a node (used for titles, ids and image alternatives).
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// assignIDs gives every heading its GitHub-style id (duplicates get -1, -2, ...), and returns the ids
// and the text of the first level-1 heading.
func assignIDs(doc ast.Node, src []byte, subs *substitutions) (map[string]bool, string) {
	ids := map[string]bool{}
	title := ""
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		txt := strings.TrimSpace(subs.plain(plainText(h, src)))
		if h.Level == 1 && title == "" {
			title = txt
		}
		id := slugify(txt)
		if id == "" {
			return ast.WalkSkipChildren, nil
		}
		unique := id
		for i := 1; ids[unique]; i++ {
			unique = fmt.Sprintf("%s-%d", id, i)
		}
		ids[unique] = true
		h.SetAttributeString("id", []byte(unique))
		return ast.WalkSkipChildren, nil
	})
	return ids, title
}

// linkRenderer writes links and images itself: a link that must not be clickable becomes text, and an
// image never fetches anything (offline rule, aictl.md 4.22).
type linkRenderer struct {
	subs *substitutions
}

func (r *linkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindImage, r.renderImage)
}

const plainAttr = "aictl-plain"

func (r *linkRenderer) renderLink(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	l := n.(*ast.Link)
	if v, ok := l.AttributeString(plainAttr); ok {
		// Not a link: the text stays and the address follows in parentheses.
		if !entering {
			if u, _ := v.([]byte); len(u) > 0 {
				_, _ = w.WriteString(" (" + html.EscapeString(string(u)) + ")")
			}
		}
		return ast.WalkContinue, nil
	}
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	dest := r.subs.plain(string(l.Destination))
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML(util.URLEscape([]byte(dest), true)))
	_ = w.WriteByte('"')
	if len(l.Title) > 0 {
		_, _ = w.WriteString(` title="`)
		_, _ = w.Write(util.EscapeHTML(l.Title))
		_ = w.WriteByte('"')
	}
	_ = w.WriteByte('>')
	return ast.WalkContinue, nil
}

func (r *linkRenderer) renderImage(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	img := n.(*ast.Image)
	alt := strings.TrimSpace(r.subs.plain(plainText(img, src)))
	dest := string(img.Destination)
	if strings.HasPrefix(strings.ToLower(dest), "data:image/") {
		_, _ = w.WriteString(`<img src="` + html.EscapeString(dest) + `" alt="` + html.EscapeString(alt) + `">`)
		return ast.WalkSkipChildren, nil
	}
	if alt == "" {
		alt = "image"
	}
	_, _ = w.WriteString(`<span class="noimage">[` + html.EscapeString(alt) + `]</span>`)
	return ast.WalkSkipChildren, nil
}

func newMarkdown(subs *substitutions) goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
			extension.Strikethrough,
			extension.TaskList,
		),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(&linkRenderer{subs: subs}, 100)),
		),
	)
}

// linkContext resolves links for Build.
type linkContext struct {
	docs     map[string]*Doc // by Rel
	readme   map[string]*Doc // folder -> its README page
	t        *tree
	cardFile string
	ghBase   string
	broken   []Warning
}

// resolved is the decision for one link.
type resolved struct {
	href  string // set: a real link
	plain bool   // set: not a link; plainURL (maybe "") follows the text
	url   string
}

// decode makes a URL fragment or path comparable.
func decode(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func hasID(ids map[string]bool, frag string) bool {
	f := decode(frag)
	return ids[f] || ids[strings.ToLower(f)]
}

// resolve decides what a link in page cur becomes.
func (c *linkContext) resolve(cur *Doc, dest string) resolved {
	if dest == "" {
		return resolved{href: ""}
	}
	if schemeRE.MatchString(dest) {
		l := strings.ToLower(dest)
		if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:") {
			return resolved{href: dest}
		}
		return resolved{plain: true}
	}
	if strings.HasPrefix(dest, "//") || strings.HasPrefix(dest, "/") {
		return resolved{plain: true}
	}
	p, frag, hasFrag := strings.Cut(dest, "#")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	suffix := ""
	if hasFrag {
		suffix = "#" + frag
	}
	if p == "" {
		if hasFrag && frag != "" && !hasID(cur.ids, frag) {
			c.fail(cur, dest, "no heading with that anchor")
		}
		return resolved{href: suffix}
	}
	isDir := strings.HasSuffix(p, "/")
	target := path.Join(path.Dir(cur.Rel), decode(p))
	if target == ".." || strings.HasPrefix(target, "../") {
		rest := strings.TrimPrefix(target, "../")
		if rest == ".." || strings.HasPrefix(rest, "../") || rest == "" {
			return resolved{plain: true}
		}
		return resolved{plain: true, url: c.ghBase + rest + suffix}
	}
	if target == CardTemplateRel {
		if c.cardFile != "" {
			return resolved{href: c.cardFile}
		}
		return resolved{plain: true, url: c.ghBase + "docs/" + target}
	}
	if d, ok := c.docs[target]; ok {
		if hasFrag && frag != "" && !hasID(d.ids, frag) {
			c.fail(cur, dest, "no heading with that anchor in "+d.Rel)
		}
		return resolved{href: d.HTML + suffix}
	}
	if isDir || c.t.dirs[target] {
		if !c.t.dirs[target] {
			c.fail(cur, dest, "folder not found")
			return resolved{plain: true}
		}
		if d, ok := c.readme[target]; ok {
			return resolved{href: d.HTML}
		}
		return resolved{href: "index.html"}
	}
	if c.t.files[target] {
		return resolved{plain: true, url: c.ghBase + "docs/" + target + suffix}
	}
	c.fail(cur, dest, "file not found")
	return resolved{plain: true}
}

func (c *linkContext) fail(cur *Doc, dest, why string) {
	c.broken = append(c.broken, Warning{Kind: WarnBrokenLink, Path: cur.Rel, Message: fmt.Sprintf("link %q: %s", dest, why)})
}

func (c *linkContext) rewrite(doc ast.Node, cur *Doc, subs *substitutions) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		l, ok := n.(*ast.Link)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		r := c.resolve(cur, subs.plain(string(l.Destination)))
		if r.plain {
			l.SetAttributeString(plainAttr, []byte(r.url))
		} else {
			l.Destination = []byte(r.href)
		}
		return ast.WalkContinue, nil
	})
}

// pageHTML wraps a body in the self-contained page: the print CSS by relative <link>, no script, no
// font, no remote asset (the CSP of /docs/ forbids all three, agent.md 3.11.3).
type pageOpts struct {
	Nav   bool   // the "All manuals" bar (hidden when printing)
	Main  bool   // wrap the body in <main>
	Class string // class of <body>
}

func pageHTML(title, lang, srcRel, body string, o pageOpts, css ...string) []byte {
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html lang=\"" + lang + "\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<meta name=\"generator\" content=\"aictl docs render\">\n")
	if srcRel != "" {
		b.WriteString("<meta name=\"source\" content=\"" + html.EscapeString(srcRel) + "\">\n")
	}
	b.WriteString("<title>" + html.EscapeString(title) + "</title>\n")
	for _, c := range css {
		b.WriteString("<link rel=\"stylesheet\" href=\"" + c + "\">\n")
	}
	b.WriteString("</head>\n<body")
	if o.Class != "" {
		b.WriteString(` class="` + o.Class + `"`)
	}
	b.WriteString(">\n")
	if o.Nav {
		b.WriteString("<nav class=\"noprint\"><a href=\"index.html\">All manuals / Todos los manuales</a></nav>\n")
	}
	if o.Main {
		b.WriteString("<main>\n")
	}
	b.WriteString(body)
	if o.Main {
		b.WriteString("</main>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return b.Bytes()
}

// renderBody renders cleaned Markdown (with sentinels) to the HTML of the page body.
func renderBody(md goldmark.Markdown, doc ast.Node, src []byte, subs *substitutions) (string, error) {
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", err
	}
	out := strings.ReplaceAll(buf.String(), "<p>"+pageBreakToken+"</p>", `<div class="pagebreak"></div>`)
	out = strings.ReplaceAll(out, pageBreakToken, "")
	return subs.html(out), nil
}

// Build renders every document of the tree. It writes nothing to disk.
func Build(o BuildOptions) (*Built, error) {
	t, err := scanTree(o.Src)
	if err != nil {
		return nil, err
	}
	gh := o.GitHubBase
	if gh == "" {
		gh = DefaultGitHubBase
	}
	res := &Built{Files: map[string][]byte{}}

	var rels []string
	for _, rel := range t.md {
		if rel == CardTemplateRel {
			b, err := os.ReadFile(filepath.Join(o.Src, filepath.FromSlash(rel)))
			if err != nil {
				return nil, ioError("cannot read", filepath.Join(o.Src, filepath.FromSlash(rel)), unwrapPath(err))
			}
			res.CardTemplate = b
			continue
		}
		rels = append(rels, rel)
	}
	slugs := assignSlugs(rels)

	type parsed struct {
		doc  *Doc
		ast  ast.Node
		src  []byte
		subs *substitutions
		md   goldmark.Markdown
	}
	cardFile := o.CardFile
	if res.CardTemplate == nil {
		cardFile = "" // no template, no card: a link to it becomes text
	}
	ctx := &linkContext{docs: map[string]*Doc{}, readme: map[string]*Doc{}, t: t, cardFile: cardFile, ghBase: gh}
	var all []*parsed
	for _, rel := range rels {
		full := filepath.Join(o.Src, filepath.FromSlash(rel))
		raw, err := os.ReadFile(full)
		if err != nil {
			return nil, ioError("cannot read", full, unwrapPath(err))
		}
		s := prepare(raw)
		for _, n := range s.notes {
			res.Warnings = append(res.Warnings, Warning{Kind: WarnDocument, Path: rel, Message: n})
		}
		body, subs := protect(s.body, o.Values)
		md := newMarkdown(subs)
		srcBytes := []byte(body)
		node := md.Parser().Parse(text.NewReader(srcBytes))
		ids, title := assignIDs(node, srcBytes, subs)
		d := &Doc{Rel: rel, Slug: slugs[rel], Kind: kindOf(rel), Title: title, TitleES: subs.plain(s.titleES), ids: ids}
		if d.Title == "" {
			d.Title = d.Slug
		}
		d.HTML = d.Slug + ".html"
		ctx.docs[rel] = d
		if strings.EqualFold(path.Base(rel), "README.md") {
			dir := path.Dir(rel)
			if _, taken := ctx.readme[dir]; !taken {
				ctx.readme[dir] = d
			}
		}
		res.Pages = append(res.Pages, d)
		all = append(all, &parsed{doc: d, ast: node, src: srcBytes, subs: subs, md: md})
	}
	for _, p := range all {
		ctx.rewrite(p.ast, p.doc, p.subs)
		body, err := renderBody(p.md, p.ast, p.src, p.subs)
		if err != nil {
			return nil, &Error{Kind: KindIO, Path: p.doc.Rel, Msg: "cannot render", Err: err}
		}
		res.Files[p.doc.HTML] = pageHTML(p.doc.Title, "en", p.doc.Rel, body, pageOpts{Nav: true, Main: true}, "print.css")
	}
	res.Warnings = append(res.Warnings, ctx.broken...)
	res.Files["print.css"] = []byte(printCSS)
	res.Files["card.css"] = []byte(cardCSS)
	return res, nil
}
