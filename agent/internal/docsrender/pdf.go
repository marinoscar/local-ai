package docsrender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
)

// PDFs are optional (aictl.md 4.22): a locally installed Chromium prints each HTML page. No PDF
// library is vendored; the one-page check of the card counts the page objects of the file.

// printers are the browsers tried, in order. They are looked up in execx.FixedPath only.
var printers = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"}

// PDFTimeout is the limit for one page.
const PDFTimeout = 60 * time.Second

// FindPrinter returns the absolute path of the first installed browser, or "".
func FindPrinter(lookup func(string) (string, error)) string {
	for _, name := range printers {
		if p, err := lookup(name); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// PDFMaker prints HTML files to PDF with a fixed argv and a profile folder of its own.
type PDFMaker struct {
	Runner  execx.Runner
	Bin     string
	Timeout time.Duration
}

// Make prints htmlPath to pdfPath. profileDir is a scratch folder for the browser's profile.
func (m PDFMaker) Make(ctx context.Context, htmlPath, pdfPath, profileDir string) error {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = PDFTimeout
	}
	res, err := m.Runner.Run(ctx, execx.Cmd{
		Path: m.Bin,
		Args: []string{
			"--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
			"--no-pdf-header-footer",
			"--user-data-dir=" + profileDir,
			"--print-to-pdf=" + pdfPath,
			"file://" + htmlPath,
		},
		Timeout: timeout,
		MaxOut:  64 << 10,
		Env:     []string{"PATH=" + execx.FixedPath, "HOME=" + profileDir},
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("the browser exited with status %d: %s", res.ExitCode, firstLine(res.Stderr))
	}
	fi, err := os.Stat(pdfPath)
	if err != nil || fi.Size() == 0 {
		return fmt.Errorf("the browser made no PDF: %s", firstLine(res.Stderr))
	}
	return nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// pageObjectRE matches a page object ("/Type /Page") and not the page tree ("/Type /Pages").
var pageObjectRE = regexp.MustCompile(`/Type\s*/Page([^s]|$)`)

// CountPages counts the pages of a PDF by its page objects. It is what the card's one-page check uses.
func CountPages(pdf []byte) int { return len(pageObjectRE.FindAll(pdf, -1)) }

// PrintAll prints the given pages (file name -> HTML bytes, with the CSS files) to PDFs and returns the
// PDFs by file name. A page the browser fails on is reported in failed and has no PDF; the HTML stays
// valid. The scratch folder is removed before return.
func PrintAll(ctx context.Context, m PDFMaker, tmpRoot string, pages []string, files map[string][]byte) (pdfs map[string][]byte, failed map[string]error, err error) {
	scratch, err := os.MkdirTemp(tmpRoot, "aictl-docs-")
	if err != nil {
		return nil, nil, ioError("cannot make a scratch folder", tmpRoot, unwrapPath(err))
	}
	defer os.RemoveAll(scratch)
	profile := filepath.Join(scratch, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		return nil, nil, ioError("cannot make a scratch folder", profile, unwrapPath(err))
	}
	for _, css := range []string{"print.css", "card.css"} {
		if err := os.WriteFile(filepath.Join(scratch, css), files[css], 0o600); err != nil {
			return nil, nil, ioError("cannot write", filepath.Join(scratch, css), unwrapPath(err))
		}
	}
	pdfs, failed = map[string][]byte{}, map[string]error{}
	for _, name := range pages {
		if ctx.Err() != nil {
			return pdfs, failed, ctx.Err()
		}
		htmlPath := filepath.Join(scratch, name)
		if err := os.WriteFile(htmlPath, files[name], 0o600); err != nil {
			return nil, nil, ioError("cannot write", htmlPath, unwrapPath(err))
		}
		pdfName := strings.TrimSuffix(name, ".html") + ".pdf"
		pdfPath := filepath.Join(scratch, pdfName)
		if err := m.Make(ctx, htmlPath, pdfPath, profile); err != nil {
			failed[name] = err
			continue
		}
		b, err := os.ReadFile(pdfPath)
		if err != nil {
			failed[name] = err
			continue
		}
		pdfs[pdfName] = b
	}
	return pdfs, failed, nil
}
