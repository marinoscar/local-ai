package kit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// Tool install (rebuild-kit.md section 3.5, aictl.md section 4.23c): `aictl kit install <tool>` puts a
// tool of the kit into /opt/localai with no network. Every artifact is checked first:
//
//   - kiwix-tools, pmtiles, piper: the tarball in kit/tools/<tool>/ is hashed against the sha256 of
//     that directory's MANIFEST.json before a byte is extracted, then installed as
//     /opt/localai/<tool>/<version>/ with the `current` link repointed atomically and the previous
//     version kept. kiwix and piper are installed by the functions of their own packages
//     (svc/kiwix, svc/piper), handed in as TarballInstaller; pmtiles is installed here;
//   - agent: kit/bin/SHA256SUMS.minisig must verify against the embedded release key (update.VerifyDir)
//     and the binary for this architecture must match its line of SHA256SUMS. The binary is copied
//     next to the installed one, started once (`version --json`) and only then swapped in with
//     aiagent.prev kept; a bad signature changes nothing (LAI-KIT-044).
//
// The package imports neither svc/kiwix nor svc/piper (they pull in the HTTP layer): the caller hands
// their Install functions in, and `aiagent setup binaries` does the same.

// The tools `kit install` covers.
const (
	ToolKiwix   = "kiwix-tools"
	ToolPmtiles = "pmtiles"
	ToolPiper   = "piper"
	ToolAgent   = "agent"
)

// Tools lists them in the order help shows them.
var Tools = []string{ToolKiwix, ToolPmtiles, ToolPiper, ToolAgent}

// ValidTool reports whether name is one of Tools.
func ValidTool(name string) bool {
	for _, t := range Tools {
		if t == name {
			return true
		}
	}
	return false
}

// DefaultOptRoot is /opt/localai (overview.md section 3.2).
const DefaultOptRoot = "/opt/localai"

// CurrentName is the link a tool directory keeps to its active version.
const CurrentName = "current"

// FS is the disk the installers work on: the atomic-write FS of internal/config, what streaming a file
// needs and symbolic links. OSFS is the real one.
type FS interface {
	update.FS
	Symlink(oldname, newname string) error
	Readlink(name string) (string, error)
}

// OSFS is the real filesystem.
type OSFS struct{ update.OSFS }

// Symlink implements FS.
func (OSFS) Symlink(oldname, newname string) error { return os.Symlink(oldname, newname) }

// Readlink implements FS.
func (OSFS) Readlink(name string) (string, error) { return os.Readlink(name) }

var _ FS = OSFS{}

// TarballInstaller installs a release tarball whose sha256 is pinned: kiwix.Install and piper.Install.
type TarballInstaller func(ctx context.Context, tarball, expectSHA256, version string) error

// Tool failure kinds (errors.Is works on a *ToolError).
var (
	// ErrToolNotInKit: the kit has no copy of the tool for this machine (LAI-KIT-040).
	ErrToolNotInKit = errors.New("the kit has no copy of this tool for this computer")
	// ErrToolDamaged: a file of the kit does not match the checksum in its MANIFEST (LAI-KIT-001).
	ErrToolDamaged = errors.New("a file of the kit does not match its checksum")
	// ErrSignature: the agent files failed their signature or checksum check (LAI-KIT-044).
	ErrSignature = errors.New("the agent files failed their signature check")
	// ErrToolInstall: the install itself failed; the code is the installer's own (LAI-SVC-003,
	// LAI-SVC-020) or LAI-KIT-047.
	ErrToolInstall = errors.New("the tool could not be installed")
)

// ToolError is the error type of ToolInstaller.Install.
type ToolError struct {
	Tool   string
	Kind   error
	Detail string
	// Names are the files the message is about.
	Names []string
	Err   error
}

// Error implements error.
func (e *ToolError) Error() string {
	s := e.Tool + ": " + e.Kind.Error()
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if len(e.Names) > 0 {
		s += ": " + strings.Join(e.Names, ", ")
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap lets errors.Is match the kind and the cause.
func (e *ToolError) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Err}
}

// Code is the doctor code: the installer's own when it has one (kiwix: LAI-SVC-003, piper:
// LAI-SVC-020), else by kind.
func (e *ToolError) Code() string {
	switch {
	case errors.Is(e.Kind, ErrToolNotInKit):
		return doctor.KIT040
	case errors.Is(e.Kind, ErrToolDamaged):
		return doctor.KIT001
	case errors.Is(e.Kind, ErrSignature):
		return doctor.KIT044
	}
	var c interface{ Code() string }
	if e.Err != nil && errors.As(e.Err, &c) {
		return c.Code()
	}
	return doctor.KIT047
}

func toolErr(tool string, kind error, detail string, cause error, names ...string) *ToolError {
	return &ToolError{Tool: tool, Kind: kind, Detail: detail, Err: cause, Names: names}
}

// ToolResult is what Install did.
type ToolResult struct {
	Tool string `json:"tool"`
	// Version is the version now current.
	Version string `json:"version"`
	// Previous is the version that was current before ("" when none).
	Previous string `json:"previous"`
	// Source is the kit file the tool came from.
	Source string `json:"source"`
	// Unchanged is true when that version was already current (agent: the same binary was installed).
	Unchanged bool `json:"unchanged"`
	// RestartError is set when the agent was installed but systemctl could not restart it.
	RestartError string `json:"restart_error,omitempty"`
}

// ToolInstaller installs the tools of a kit. Fill FS, Runner, Arch and the tarball installers; NewToolInstaller
// gives the real defaults for everything but Kiwix and Piper.
type ToolInstaller struct {
	FS     FS
	Runner execx.Runner
	// LookPath finds systemctl (execx.Lookup).
	LookPath func(name string) (string, error)
	// Arch is runtime.GOARCH of the machine ("amd64" or "arm64").
	Arch string
	// Keys are the trusted minisign keys of the agent files (update.EmbeddedKeys()).
	Keys []update.PublicKey
	// OptRoot is /opt/localai.
	OptRoot string
	// Kiwix and Piper install their tarballs (kiwix.Install, piper.Install).
	Kiwix, Piper TarballInstaller
	// Progress, when set, is told each step ("verify", "install", "restart") and the tool.
	Progress func(step, tool string)
}

// NewToolInstaller returns a ToolInstaller for the real machine; the caller sets Kiwix and Piper.
func NewToolInstaller(arch string) *ToolInstaller {
	return &ToolInstaller{FS: OSFS{}, Runner: execx.Exec{}, LookPath: execx.Lookup, Arch: arch, Keys: update.EmbeddedKeys(), OptRoot: DefaultOptRoot}
}

func (t *ToolInstaller) step(step, tool string) {
	if t.Progress != nil {
		t.Progress(step, tool)
	}
}

func (t *ToolInstaller) optRoot() string {
	if t.OptRoot != "" {
		return t.OptRoot
	}
	return DefaultOptRoot
}

// Install installs one tool from the kit directory kitDir (<root>/kit). tool must be one of Tools.
func (t *ToolInstaller) Install(ctx context.Context, kitDir, tool string) (*ToolResult, error) {
	switch tool {
	case ToolKiwix:
		return t.installTarball(ctx, kitDir, tool, "kiwix-tools", t.Kiwix, kiwixFile)
	case ToolPmtiles:
		return t.installTarball(ctx, kitDir, tool, "pmtiles", t.installPmtiles, pmtilesFile)
	case ToolPiper:
		return t.installTarball(ctx, kitDir, tool, "piper", t.Piper, piperFile)
	case ToolAgent:
		return t.installAgent(ctx, kitDir)
	}
	return nil, toolErr(tool, ErrToolNotInKit, "not a tool of the kit", nil)
}

// releaseFile picks this machine's release file out of a manifest and says its version.
type releaseFile func(m manifest.Manifest, arch string) (file manifest.File, version string, ok bool)

var (
	kiwixFileRe   = regexp.MustCompile(`^kiwix-tools_linux-(x86_64|aarch64)-([0-9][0-9A-Za-z.+_-]{0,63})\.tar\.gz$`)
	pmtilesFileRe = regexp.MustCompile(`^go-pmtiles_([0-9][0-9A-Za-z.+_-]{0,63})_Linux_(x86_64|arm64)\.tar\.gz$`)
	piperFileRe   = regexp.MustCompile(`^piper_linux_(x86_64|aarch64)\.tar\.gz$`)
)

func kiwixFile(m manifest.Manifest, arch string) (manifest.File, string, bool) {
	want := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
	for _, f := range m.Files {
		if g := kiwixFileRe.FindStringSubmatch(f.Path); g != nil && g[1] == want {
			return f, g[2], true
		}
	}
	return manifest.File{}, "", false
}

func pmtilesFile(m manifest.Manifest, arch string) (manifest.File, string, bool) {
	want := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[arch]
	for _, f := range m.Files {
		if g := pmtilesFileRe.FindStringSubmatch(f.Path); g != nil && g[2] == want {
			return f, g[1], true
		}
	}
	return manifest.File{}, "", false
}

// piperReleaseRe takes the release number out of a GitHub release URL:
// https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz.
var piperReleaseRe = regexp.MustCompile(`/releases/download/([0-9][0-9A-Za-z.+_-]{0,63})/[^/]+$`)

// piperFile: the release number is not in the file name, and the manifest's version of a url item is the
// content hash of the sync, not the release (found on a real node). It is read from the file's source
// URL; the item's version is used only when it is itself a release number.
func piperFile(m manifest.Manifest, arch string) (manifest.File, string, bool) {
	want := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
	for _, f := range m.Files {
		g := piperFileRe.FindStringSubmatch(f.Path)
		if g == nil || g[1] != want {
			continue
		}
		if r := piperReleaseRe.FindStringSubmatch(f.URL); r != nil {
			return f, r[1], true
		}
		if v := m.Version; v != "" && regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}(-[0-9]+)?$`).MatchString(v) {
			return f, v, true
		}
	}
	return manifest.File{}, "", false
}

// Located is a tarball of the kit that passed its checksum: the file, its pinned sha256 and its version.
type Located struct {
	Path, SHA256, Version string
}

// Locate finds the tarball of a tarball tool (kiwix-tools, pmtiles, piper) in kitDir and checks it against
// the sha256 of the tool's MANIFEST.json, without installing anything. `aiagent setup binaries --dry-run`
// uses it; Install runs the same code first.
func (t *ToolInstaller) Locate(ctx context.Context, kitDir, tool string) (*Located, error) {
	var dirName string
	var pick releaseFile
	switch tool {
	case ToolKiwix:
		dirName, pick = "kiwix-tools", kiwixFile
	case ToolPmtiles:
		dirName, pick = "pmtiles", pmtilesFile
	case ToolPiper:
		dirName, pick = "piper", piperFile
	default:
		return nil, toolErr(tool, ErrToolNotInKit, "not a tarball tool of the kit", nil)
	}
	return t.locate(ctx, kitDir, tool, dirName, pick)
}

func (t *ToolInstaller) locate(ctx context.Context, kitDir, tool, dirName string, pick releaseFile) (*Located, error) {
	t.step("verify", tool)
	dir := filepath.Join(kitDir, PartTools, dirName)
	m, err := manifest.Load(t.FS, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, toolErr(tool, ErrToolNotInKit, "the kit has no "+filepath.Join(PartTools, dirName)+" folder with a MANIFEST.json", nil)
	case err != nil:
		return nil, toolErr(tool, ErrToolDamaged, "the MANIFEST.json of "+filepath.Join(PartTools, dirName)+" is damaged or incomplete", err)
	}
	file, version, ok := pick(m, t.Arch)
	if !ok {
		return nil, toolErr(tool, ErrToolNotInKit, "its MANIFEST.json lists no file for "+t.Arch, nil)
	}
	path := filepath.Join(dir, filepath.FromSlash(file.Path))
	got, err := hashFile(ctx, t.FS, path, maxDebBytes)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	case err != nil:
		return nil, toolErr(tool, ErrToolDamaged, "cannot read the file", err, file.Path)
	case got != file.SHA256:
		return nil, toolErr(tool, ErrToolDamaged, "the file does not match the checksum in its MANIFEST.json", nil, file.Path)
	}
	return &Located{Path: path, SHA256: file.SHA256, Version: version}, nil
}

// installTarball is the flow of the three tarball tools: find the file in the tool's MANIFEST, hash it
// against the pinned sha256, hand it to the installer.
func (t *ToolInstaller) installTarball(ctx context.Context, kitDir, tool, dirName string, install TarballInstaller, pick releaseFile) (*ToolResult, error) {
	if install == nil {
		return nil, toolErr(tool, ErrToolInstall, "no installer is configured for this tool", nil)
	}
	loc, err := t.locate(ctx, kitDir, tool, dirName, pick)
	if err != nil {
		return nil, err
	}
	return t.installLocated(ctx, tool, dirName, install, loc.Path, loc.SHA256, loc.Version)
}

func (t *ToolInstaller) installLocated(ctx context.Context, tool, dirName string, install TarballInstaller, path, sha, version string) (*ToolResult, error) {
	prev := t.current(dirName)
	t.step("install", tool)
	if err := install(ctx, path, sha, version); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, toolErr(tool, ErrToolInstall, "", err)
	}
	cur := t.current(dirName)
	return &ToolResult{Tool: tool, Version: version, Previous: prevOf(prev, cur), Source: path, Unchanged: prev == version}, nil
}

// InstallFile installs a tarball tool from a file that is not in a kit (the online path of the installer:
// a download whose sha256 the catalog pins). The file is hashed against sha256 first; a mismatch is
// ErrToolDamaged and nothing is extracted.
func (t *ToolInstaller) InstallFile(ctx context.Context, tool, tarball, sha256, version string) (*ToolResult, error) {
	var dirName string
	var install TarballInstaller
	switch tool {
	case ToolKiwix:
		dirName, install = "kiwix-tools", t.Kiwix
	case ToolPmtiles:
		dirName, install = "pmtiles", t.installPmtiles
	case ToolPiper:
		dirName, install = "piper", t.Piper
	default:
		return nil, toolErr(tool, ErrToolNotInKit, "not a tarball tool of the kit", nil)
	}
	if install == nil {
		return nil, toolErr(tool, ErrToolInstall, "no installer is configured for this tool", nil)
	}
	t.step("verify", tool)
	got, err := hashFile(ctx, t.FS, tarball, maxDebBytes)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, err
	case err != nil:
		return nil, toolErr(tool, ErrToolDamaged, "cannot read the file", err, filepath.Base(tarball))
	case got != sha256:
		return nil, toolErr(tool, ErrToolDamaged, "the download does not match the checksum pinned in the catalog", nil, filepath.Base(tarball))
	}
	return t.installLocated(ctx, tool, dirName, install, tarball, sha256, version)
}

// ReleaseFromURL says the version of a tool's release tarball from its download URL, or false when the URL is
// not this tool's tarball for arch ("amd64" or "arm64"). It uses the same file-name rules as the kit.
func ReleaseFromURL(tool, arch, url string) (string, bool) {
	base := url
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		base = url[i+1:]
	}
	switch tool {
	case ToolKiwix:
		if g := kiwixFileRe.FindStringSubmatch(base); g != nil && g[1] == map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch] {
			return g[2], true
		}
	case ToolPmtiles:
		if g := pmtilesFileRe.FindStringSubmatch(base); g != nil && g[2] == map[string]string{"amd64": "x86_64", "arm64": "arm64"}[arch] {
			return g[1], true
		}
	case ToolPiper:
		if g := piperFileRe.FindStringSubmatch(base); g != nil && g[1] == map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch] {
			if r := piperReleaseRe.FindStringSubmatch(url); r != nil {
				return r[1], true
			}
		}
	}
	return "", false
}

// prevOf is the version that was current before: the one seen before the install, unless the
// install changed nothing.
func prevOf(before, after string) string {
	if before == after {
		return ""
	}
	return before
}

// current reads the version <OptRoot>/<dirName>/current points to ("" when there is none).
func (t *ToolInstaller) current(dirName string) string {
	target, err := t.FS.Readlink(filepath.Join(t.optRoot(), dirName, CurrentName))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// ---- agent --------------------------------------------------------------------------------------

// agentSmoke is `aiagent version --json`.
type agentSmoke struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

const (
	agentSmokeTimeout = 10 * time.Second
	restartTimeout    = 2 * time.Minute
	agentUnit         = "localai-agent.service"
)

// installAgent verifies kit/bin, copies the binary for this machine next to the installed agent,
// starts it once, swaps it in (aiagent.prev kept) and restarts the agent.
func (t *ToolInstaller) installAgent(ctx context.Context, kitDir string) (*ToolResult, error) {
	const tool = ToolAgent
	t.step("verify", tool)
	if !update.SupportedArch(t.Arch) {
		return nil, toolErr(tool, ErrToolNotInKit, "the kit has agent files for amd64 and arm64, not "+t.Arch, nil)
	}
	binDir := filepath.Join(kitDir, PartBin)
	if fi, err := t.FS.Stat(binDir); err != nil || !fi.IsDir() {
		return nil, toolErr(tool, ErrToolNotInKit, "the kit has no bin/ folder", err)
	}
	rep, err := update.VerifyDir(t.FS, binDir, t.Keys, update.DirOptions{IgnoreMissing: true})
	if err != nil {
		var mm *update.MismatchError
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case errors.As(err, &mm):
			return nil, toolErr(tool, ErrSignature, "these files do not match the signed checksums", nil, mm.Files...)
		case errors.Is(err, update.ErrNoSums):
			return nil, toolErr(tool, ErrSignature, "SHA256SUMS or SHA256SUMS.minisig is missing", err)
		}
		return nil, toolErr(tool, ErrSignature, "the signature of SHA256SUMS did not verify", err)
	}
	name := update.BinaryName(t.Arch)
	listed := false
	for _, f := range rep.Files {
		if f.Name == name {
			listed = f.Status == "ok"
		}
	}
	if !listed {
		return nil, toolErr(tool, ErrToolNotInKit, "the signed checksums have no verified "+name, nil)
	}
	src := filepath.Join(binDir, name)
	sum, err := hashFile(ctx, t.FS, src, update.MaxBinaryBytes)
	if err != nil {
		return nil, toolErr(tool, ErrToolDamaged, "cannot read the file", err, name)
	}
	want := t.expectedAgentVersion(binDir)

	binDirOpt := filepath.Join(t.optRoot(), "bin")
	paths := update.Paths{BinDir: binDirOpt}
	// Already the installed one: nothing to do.
	if cur, err := hashFile(ctx, t.FS, paths.Agent(), update.MaxBinaryBytes); err == nil && cur == sum {
		return &ToolResult{Tool: tool, Version: want, Source: src, Unchanged: true}, nil
	}

	t.step("install", tool)
	if err := t.FS.MkdirAll(binDirOpt, 0o755); err != nil {
		return nil, toolErr(tool, ErrToolInstall, "cannot create "+binDirOpt, err)
	}
	// Copy, hashing while copying: what is smoke tested and installed is the verified content.
	if err := t.copyFile(ctx, src, paths.New(), sum); err != nil {
		_ = t.FS.Remove(paths.New())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, toolErr(tool, ErrToolInstall, "cannot copy the new agent next to the installed one", err)
	}
	version, err := t.smokeAgent(ctx, paths.New(), want)
	if err != nil {
		_ = t.FS.Remove(paths.New())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, toolErr(tool, ErrToolInstall, "the new agent did not pass its start-up test, so nothing was replaced", err)
	}
	prev := ""
	if fi, err := t.FS.Stat(paths.Agent()); err == nil && fi.Mode().IsRegular() {
		if v := t.installedAgentVersion(ctx, paths.Agent()); v != "" {
			prev = v
		}
		if err := t.copyFile(ctx, paths.Agent(), paths.Prev()+".tmp", ""); err != nil {
			_ = t.FS.Remove(paths.New())
			_ = t.FS.Remove(paths.Prev() + ".tmp")
			return nil, toolErr(tool, ErrToolInstall, "cannot keep the installed agent as aiagent.prev", err)
		}
		if err := t.FS.Rename(paths.Prev()+".tmp", paths.Prev()); err != nil {
			_ = t.FS.Remove(paths.New())
			_ = t.FS.Remove(paths.Prev() + ".tmp")
			return nil, toolErr(tool, ErrToolInstall, "cannot keep the installed agent as aiagent.prev", err)
		}
	}
	if err := t.FS.Rename(paths.New(), paths.Agent()); err != nil {
		_ = t.FS.Remove(paths.New())
		return nil, toolErr(tool, ErrToolInstall, "cannot put the new agent in place", err)
	}
	_ = t.FS.SyncDir(binDirOpt)

	res := &ToolResult{Tool: tool, Version: version, Previous: prev, Source: src}
	t.step("restart", tool)
	if err := t.restartAgent(ctx); err != nil {
		res.RestartError = err.Error()
	}
	return res, nil
}

// expectedAgentVersion is the version recorded by the manifest of kit/bin ("" when there is none or
// it does not look like a version): the smoke test then only requires a sane answer.
func (t *ToolInstaller) expectedAgentVersion(binDir string) string {
	m, err := manifest.Read(t.FS, manifest.Path(binDir))
	if err != nil {
		return ""
	}
	if v, err := update.ParseTag(m.Version); err == nil {
		return v.String()
	}
	if v, err := update.ParseVersion(strings.TrimPrefix(m.Version, "v")); err == nil {
		return v.String()
	}
	return ""
}

// copyFile copies src to dst (0755, fsynced). With wantSHA256 set, the bytes copied must hash to it.
func (t *ToolInstaller) copyFile(ctx context.Context, src, dst, wantSHA256 string) (err error) {
	in, err := t.FS.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := t.FS.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if err != nil {
			if !closed {
				_ = out.Close()
			}
			_ = t.FS.Remove(dst)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(ctxReader{ctx, in}, update.MaxBinaryBytes+1))
	if err != nil {
		return err
	}
	if n > update.MaxBinaryBytes {
		return fmt.Errorf("%s is larger than %d bytes", filepath.Base(src), update.MaxBinaryBytes)
	}
	if wantSHA256 != "" && hex.EncodeToString(h.Sum(nil)) != wantSHA256 {
		return errors.New("the copy does not match the verified file")
	}
	if err = out.Chmod(0o755); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	closed = true
	return out.Close()
}

// smokeAgent runs `<binary> version --json` and requires linux, this architecture and, when known,
// the version of the kit. It returns the version the binary reports.
func (t *ToolInstaller) smokeAgent(ctx context.Context, binary, want string) (string, error) {
	res, err := t.Runner.Run(ctx, execx.Cmd{Path: binary, Args: []string{"version", "--json"}, Timeout: agentSmokeTimeout, MaxOut: 64 << 10})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("exit status %d", res.ExitCode)
	}
	var info agentSmoke
	if err := json.Unmarshal(res.Stdout, &info); err != nil {
		return "", fmt.Errorf("the output is not JSON: %w", err)
	}
	v, err := update.ParseVersion(info.Version)
	switch {
	case err != nil:
		return "", fmt.Errorf("it reports version %q", clip(info.Version, 32))
	case want != "" && v.String() != want:
		return "", fmt.Errorf("it reports version %q, the kit says %q", clip(info.Version, 32), want)
	case info.Arch != t.Arch:
		return "", fmt.Errorf("it reports architecture %q, not %q", clip(info.Arch, 16), t.Arch)
	case info.OS != "linux":
		return "", fmt.Errorf("it reports os %q", clip(info.OS, 16))
	}
	return v.String(), nil
}

// installedAgentVersion asks the installed agent for its version ("" when it cannot say).
func (t *ToolInstaller) installedAgentVersion(ctx context.Context, binary string) string {
	res, err := t.Runner.Run(ctx, execx.Cmd{Path: binary, Args: []string{"version", "--json"}, Timeout: agentSmokeTimeout, MaxOut: 64 << 10})
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	var info agentSmoke
	if json.Unmarshal(res.Stdout, &info) != nil {
		return ""
	}
	if v, err := update.ParseVersion(info.Version); err == nil {
		return v.String()
	}
	return ""
}

// restartAgent restarts the agent unit with systemctl (a rebuild must work when the agent does not
// run, so this is best effort: the caller shows the failure as a warning).
func (t *ToolInstaller) restartAgent(ctx context.Context) error {
	bin, err := t.LookPath("systemctl")
	if err != nil {
		return err
	}
	res, err := t.Runner.Run(ctx, execx.Cmd{Path: bin, Args: []string{"restart", agentUnit}, Timeout: restartTimeout, MaxOut: 64 << 10})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("systemctl restart %s exited %d", agentUnit, res.ExitCode)
	}
	return nil
}
