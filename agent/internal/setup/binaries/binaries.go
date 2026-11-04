// Package binaries is `aiagent setup binaries` (installer.md sections 3.5 and 4.3 P6): it installs
// kiwix-tools, pmtiles and piper from verified tarballs, from the rebuild kit when there is no internet or
// from the URLs the catalog pins when there is. Both paths end in the same install functions as
// `aictl kit install <tool>` (internal/kit), so the checks, the atomic `current` link and the kept previous
// version are the same.
package binaries

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// DefaultTools are the three companion programs, in install order.
var DefaultTools = []string{kit.ToolKiwix, kit.ToolPmtiles, kit.ToolPiper}

// Installer is the part of kit.ToolInstaller this package uses; tests replace it.
type Installer interface {
	Locate(ctx context.Context, kitDir, tool string) (*kit.Located, error)
	Install(ctx context.Context, kitDir, tool string) (*kit.ToolResult, error)
	InstallFile(ctx context.Context, tool, tarball, sha256, version string) (*kit.ToolResult, error)
}

// Fetcher downloads url into dest (a new file). The real one is HTTPSFetcher.
type Fetcher interface {
	Fetch(ctx context.Context, url, dest string) error
}

// Pin is one tool's pinned online download.
type Pin struct {
	URL, SHA256, Version string
}

// Options configures Run.
type Options struct {
	// Tools to install; empty means DefaultTools.
	Tools []string
	// KitDir is <content root>/kit for an offline install; "" with Pins set means online.
	KitDir string
	// Pins are the online downloads by tool (PinsFromCatalog). Used only when KitDir is "".
	Pins map[string]Pin
	// Fetch downloads for the online path.
	Fetch Fetcher
	// TempDir holds downloads (removed afterwards); default os.TempDir().
	TempDir string
	DryRun  bool
}

// Outcome is what happened to one tool.
type Outcome struct {
	Tool string `json:"tool"`
	// Status is installed, unchanged, would-install or failed.
	Status   string `json:"status"`
	Version  string `json:"version,omitempty"`
	Previous string `json:"previous,omitempty"`
	Source   string `json:"source,omitempty"`
	// Code and Error are set for a failure.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
}

// Report is the result of Run.
type Report struct {
	DryRun bool      `json:"dry_run"`
	Online bool      `json:"online"`
	Tools  []Outcome `json:"tools"`
}

// Run installs the tools, stopping at the first failure; the failed tool is the last entry of the report
// and the error carries its doctor code (see CodeOf).
func Run(ctx context.Context, inst Installer, o Options) (*Report, error) {
	tools := o.Tools
	if len(tools) == 0 {
		tools = DefaultTools
	}
	rep := &Report{DryRun: o.DryRun, Online: o.KitDir == ""}
	for _, tool := range tools {
		if tool != kit.ToolKiwix && tool != kit.ToolPmtiles && tool != kit.ToolPiper {
			return rep, &Error{Tool: tool, Code: doctor.INS035, Err: fmt.Errorf("%q is not one of kiwix-tools, pmtiles, piper", tool)}
		}
		var out Outcome
		var err error
		if o.KitDir != "" {
			out, err = fromKit(ctx, inst, o, tool)
		} else {
			out, err = fromNet(ctx, inst, o, tool)
		}
		rep.Tools = append(rep.Tools, out)
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Error is a failed tool with its doctor code.
type Error struct {
	Tool string
	Code string
	Err  error
}

func (e *Error) Error() string { return e.Tool + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// CodeOf is the doctor code of an error returned by Run ("" for none).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func failed(tool string, err error) (Outcome, error) {
	code := doctor.INS035
	var te *kit.ToolError
	if errors.As(err, &te) {
		code = te.Code()
	}
	return Outcome{Tool: tool, Status: "failed", Code: code, Error: err.Error()}, &Error{Tool: tool, Code: code, Err: err}
}

func fromKit(ctx context.Context, inst Installer, o Options, tool string) (Outcome, error) {
	if o.DryRun {
		loc, err := inst.Locate(ctx, o.KitDir, tool)
		if err != nil {
			return failed(tool, err)
		}
		return Outcome{Tool: tool, Status: "would-install", Version: loc.Version, Source: loc.Path}, nil
	}
	res, err := inst.Install(ctx, o.KitDir, tool)
	if err != nil {
		return failed(tool, err)
	}
	return done(res), nil
}

func fromNet(ctx context.Context, inst Installer, o Options, tool string) (Outcome, error) {
	pin, ok := o.Pins[tool]
	if !ok {
		return failed(tool, fmt.Errorf("the catalog pins no download of %s for this computer's architecture", tool))
	}
	if o.DryRun {
		return Outcome{Tool: tool, Status: "would-install", Version: pin.Version, Source: pin.URL}, nil
	}
	if o.Fetch == nil {
		return failed(tool, errors.New("no downloader is configured"))
	}
	dir, err := os.MkdirTemp(o.TempDir, "lai-binaries-")
	if err != nil {
		return failed(tool, err)
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, path(pin.URL))
	if err := o.Fetch.Fetch(ctx, pin.URL, dest); err != nil {
		out, _ := failed(tool, fmt.Errorf("download of %s failed: %w", pin.URL, err))
		out.Code = doctor.INS015
		return out, &Error{Tool: tool, Code: doctor.INS015, Err: err}
	}
	res, err := inst.InstallFile(ctx, tool, dest, pin.SHA256, pin.Version)
	if err != nil {
		return failed(tool, err)
	}
	return done(res), nil
}

func done(res *kit.ToolResult) Outcome {
	st := "installed"
	if res.Unchanged {
		st = "unchanged"
	}
	return Outcome{Tool: res.Tool, Status: st, Version: res.Version, Previous: res.Previous, Source: res.Source}
}

func path(url string) string {
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PinsFromCatalog reads the pinned downloads of kit-kiwix-tools, kit-pmtiles and kit-piper for arch
// ("amd64" or "arm64"). A tool whose item is missing, or whose file for arch has no sha256, is left out of
// the map; Run then refuses it with a plain message instead of downloading something unchecked.
func PinsFromCatalog(c *catalog.Catalog, tools []string, arch string) map[string]Pin {
	if len(tools) == 0 {
		tools = DefaultTools
	}
	pins := map[string]Pin{}
	for _, tool := range tools {
		item, ok := c.ContentItem("kit-" + tool)
		if !ok {
			continue
		}
		for _, f := range item.Source.Files {
			v, ok := kit.ReleaseFromURL(tool, arch, f.URL)
			if !ok || !sha256Re.MatchString(f.SHA256) || !strings.HasPrefix(f.URL, "https://") {
				continue
			}
			pins[tool] = Pin{URL: f.URL, SHA256: f.SHA256, Version: v}
			break
		}
	}
	return pins
}

// maxDownload caps a download: the largest companion tarball is under 100 MB.
const maxDownload = 512 << 20

// HTTPSFetcher downloads over HTTPS with the standard library; plain http is refused.
type HTTPSFetcher struct {
	Client *http.Client
}

// Fetch implements Fetcher.
func (h HTTPSFetcher) Fetch(ctx context.Context, url, dest string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to download from a URL that is not https: %s", url)
	}
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxDownload {
		err = errors.New("the download is larger than expected")
	}
	if err != nil {
		_ = os.Remove(dest)
	}
	return err
}
