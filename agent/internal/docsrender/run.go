package docsrender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// Config is everything Run needs, so tests can run it hermetically.
type Config struct {
	// Src is the docs directory. Required.
	Src string
	// Outs are the output folders. Required (the CLI supplies the defaults).
	Outs []string
	// InventoryPath is the inventory.local.yaml found by FindInventory ("" when none).
	InventoryPath string
	// ExamplePath is inventory.example.yaml; "" looks in Src and next to it.
	ExamplePath string
	// NoPDF skips the PDFs (--no-pdf).
	NoPDF bool
	// NodeID is --node ("" lets the node state decide).
	NodeID string
	// State says where the node state is read from.
	State StateSources
	// Hostname is this machine's name, used to pick the node from the inventory as a last resort.
	Hostname string
	// Now is the clock (render date, rendered_at).
	Now time.Time
	// Runner and Lookup run and find the browser; nil Runner means execx.Exec, nil Lookup execx.Lookup.
	Runner execx.Runner
	Lookup func(string) (string, error)
	// TmpDir is the parent of the scratch folder the browser prints from ("" is os.TempDir()).
	TmpDir string
	// Owner owns the written files when set.
	Owner *Owner
	// GitHubBase overrides DefaultGitHubBase (tests).
	GitHubBase string
}

// Report is what Run did.
type Report struct {
	// Outputs are the folders that were written completely.
	Outputs []string
	// Pages is the number of documents rendered (the card is not counted).
	Pages int
	// Runbooks and Specs count the pages of those kinds.
	Runbooks, Specs int
	// PDFs is the number of PDF files made (the card's included).
	PDFs int
	// Inventory says real values were available.
	Inventory bool
	// Card is the card's file name without extension ("" when no card was made).
	Card string
	// NodeID is the node the card is for.
	NodeID   string
	Warnings []Warning
}

func (r *Report) warn(k WarningKind, path, msg string) {
	r.Warnings = append(r.Warnings, Warning{Kind: k, Path: path, Message: msg})
}

// Run renders the documents and the survival card and writes them to every output folder. Every
// check that can refuse the run (a folder inside a git checkout, a secret placeholder or an
// overflowing page on the card) happens before the first byte is written. On a write failure the
// folders that did succeed are in Report.Outputs and the error is returned.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	rep := &Report{}
	if len(cfg.Outs) == 0 {
		return rep, &Error{Kind: KindUsage, Msg: "no output folder"}
	}
	seen := map[string]bool{}
	var outs []string
	for _, o := range cfg.Outs {
		abs, err := filepath.Abs(o)
		if err != nil {
			return rep, &Error{Kind: KindUsage, Path: o, Msg: "not a usable path", Err: err}
		}
		if err := CheckOut(abs); err != nil {
			return rep, err
		}
		if !seen[abs] {
			seen[abs] = true
			outs = append(outs, abs)
		}
	}
	if cfg.Now.IsZero() {
		cfg.Now = time.Now()
	}
	if cfg.Runner == nil {
		cfg.Runner = execx.Exec{}
	}
	if cfg.Lookup == nil {
		cfg.Lookup = execx.Lookup
	}

	pairs, local, err := loadInventory(cfg, rep)
	if err != nil {
		return rep, err
	}

	st := LoadNodeState(cfg.State)
	switch {
	case cfg.NodeID != "":
		st.NodeID = cfg.NodeID
	case st.NodeID != "" && st.NodeID != UnconfiguredNode:
	default:
		st.NodeID = ""
		if id := NodeIDByMachine(local, cfg.Hostname); id != "" {
			st.NodeID = id
		}
	}
	if st.NodeID == "" {
		st.NodeID = UnconfiguredNode
		rep.warn(WarnNodeUnknown, "", "this node's id is unknown; the card is named for \""+UnconfiguredNode+"\" (pass --node)")
	}
	rep.NodeID = st.NodeID

	values := pairs.Values(st.NodeID)
	if values["DESKTOP_USER"] == "" && st.DesktopUser != "" {
		values["DESKTOP_USER"] = st.DesktopUser
	}
	cardBase := CardFileName(st.NodeID)
	built, err := Build(BuildOptions{Src: cfg.Src, Values: values, CardFile: cardBase + ".html", GitHubBase: cfg.GitHubBase})
	if err != nil {
		return rep, err
	}
	rep.Warnings = append(rep.Warnings, built.Warnings...)
	files := built.Files

	var entries []IndexEntry
	if built.CardTemplate != nil {
		card, err := BuildCard(built.CardTemplate, pairs, st, cfg.Now)
		if err != nil {
			return rep, err
		}
		files[cardBase+".html"] = card
		rep.Card = cardBase
		entries = append(entries, IndexEntry{Slug: cardBase, Title: cardTitleEN + " " + st.NodeID, TitleES: cardTitleES, Kind: KindCard, HTMLFile: cardBase + ".html"})
	} else {
		rep.warn(WarnNoCard, CardTemplateRel, "the survival card template is missing, so no card was made")
	}
	for _, d := range built.Pages {
		entries = append(entries, IndexEntry{Slug: d.Slug, Title: d.Title, TitleES: d.TitleES, Kind: d.Kind, HTMLFile: d.HTML})
		rep.Pages++
		switch d.Kind {
		case KindRunbook:
			rep.Runbooks++
		case KindSpec:
			rep.Specs++
		}
	}

	if !cfg.NoPDF {
		bin := FindPrinter(cfg.Lookup)
		if bin == "" {
			rep.warn(WarnNoPDF, "", "no Chromium is installed; only HTML was made")
		} else {
			var pages []string
			for _, e := range entries {
				pages = append(pages, e.HTMLFile)
			}
			pdfs, failed, err := PrintAll(ctx, PDFMaker{Runner: cfg.Runner, Bin: bin}, cfg.TmpDir, pages, files)
			if err != nil {
				return rep, err
			}
			failedNames := make([]string, 0, len(failed))
			for n := range failed {
				failedNames = append(failedNames, n)
			}
			sort.Strings(failedNames)
			for _, n := range failedNames {
				rep.warn(WarnPDFFailed, n, "no PDF for this page: "+failed[n].Error())
			}
			if rep.Card != "" {
				if pdf, ok := pdfs[cardBase+".pdf"]; ok {
					if n := CountPages(pdf); n != 1 {
						return rep, &Error{Kind: KindCardPages, Path: CardTemplateRel,
							Msg: fmt.Sprintf("the survival card needs %d pages, not one", n)}
					}
				}
			}
			for name, b := range pdfs {
				files[name] = b
				rep.PDFs++
			}
			for i := range entries {
				if _, ok := pdfs[strings.TrimSuffix(entries[i].HTMLFile, ".html")+".pdf"]; ok {
					entries[i].PDFFile = strings.TrimSuffix(entries[i].HTMLFile, ".html") + ".pdf"
				}
			}
		}
	}

	files["index.html"] = IndexHTML(entries)
	var firstErr error
	for _, out := range outs {
		idx, err := IndexJSON(out, cfg.Now, entries)
		if err != nil {
			return rep, &Error{Kind: KindIO, Path: out, Msg: "cannot make index.json", Err: err}
		}
		set := make(map[string][]byte, len(files)+1)
		for k, v := range files {
			set[k] = v
		}
		set["index.json"] = idx
		if err := WriteTree(out, set, cfg.Owner); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rep.Outputs = append(rep.Outputs, out)
	}
	return rep, firstErr
}

// loadInventory reads and pairs the inventory. A missing local file is a warning, not an error.
func loadInventory(cfg Config, rep *Report) (*Pairs, []byte, error) {
	example := cfg.ExamplePath
	if !isFile(example) {
		example = ""
		for _, c := range []string{filepath.Join(cfg.Src, ExampleFile), filepath.Join(filepath.Dir(filepath.Clean(cfg.Src)), ExampleFile)} {
			if isFile(c) {
				example = c
				break
			}
		}
	}
	var local []byte
	if cfg.InventoryPath != "" {
		b, err := os.ReadFile(cfg.InventoryPath)
		if err != nil {
			return nil, nil, ioError("cannot read", cfg.InventoryPath, unwrapPath(err))
		}
		local = b
	} else {
		rep.warn(WarnNoInventory, "", InventoryFile+" was not found; real addresses are shown as placeholders")
	}
	if example == "" {
		if local != nil {
			rep.warn(WarnNoInventory, ExampleFile, ExampleFile+" was not found next to the documents, so the values could not be matched to placeholders")
		}
		return nil, local, nil
	}
	exb, err := os.ReadFile(example)
	if err != nil {
		return nil, nil, ioError("cannot read", example, unwrapPath(err))
	}
	pairs, err := PairInventory(exb, local)
	if err != nil {
		path := example
		if strings.HasPrefix(err.Error(), InventoryFile) {
			path = cfg.InventoryPath
		}
		return nil, nil, &Error{Kind: KindInventory, Path: path, Msg: "not valid YAML", Err: err}
	}
	rep.Inventory = local != nil
	return pairs, local, nil
}
