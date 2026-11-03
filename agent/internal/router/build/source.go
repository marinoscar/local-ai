package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/kit"
)

// DefaultCloneURL is the upstream repository of the online source.
const DefaultCloneURL = "https://github.com/ggml-org/llama.cpp.git"

// ErrNotAvailable is what a Source returns (wrapped is fine) when it does not have the commit: the
// next source of the chain is tried. Any other error means the source has the commit but could not
// deliver it, which stops the chain from ending in LAI-KIT-002.
var ErrNotAvailable = errors.New("build: the source does not have this commit")

// ErrSourceDamaged is what a Source returns (wrapped is fine) when it has the commit but the copy
// cannot be trusted: a kit tarball whose sha256 differs from the one the kit recorded. The chain goes
// on (rebuild-kit.md section 6: the mirror is used when it has the commit); when nothing else
// delivers the commit the build ends with LAI-KIT-001.
var ErrSourceDamaged = kit.ErrSourceDamaged

// MissingToolError is what a Source returns when a program it needs is not installed; the preflight
// turns it into the missing tool and its apt package.
type MissingToolError struct {
	Tool, Package string
	Err           error
}

// Error implements error.
func (e *MissingToolError) Error() string {
	return e.Tool + " is needed for this source: " + e.Err.Error()
}

// Unwrap lets errors.Is reach the cause.
func (e *MissingToolError) Unwrap() error { return e.Err }

// SourceEnv is what a Source may use: the same seams as the Manager.
type SourceEnv struct {
	Exec   execx.Runner
	FS     FS
	Lookup func(name string) (string, error)
	Log    *slog.Logger
}

// Source is one place a llama.cpp tree can come from (llama-router.md section 4.10.1). The Manager
// walks its chain in order; whatever a source delivers is extracted into src/<commit>/, the only
// source location, before the build starts. KIT-04 adds the kit tarball and the kit mirror
// (GitArchiveSource over <root>/kit/git/llama.cpp.git is already the mirror reader); ROUTER-09 adds
// the legacy checkout the same way.
type Source interface {
	// Name is for logs and errors ("dir", "git-archive", "github").
	Name() string
	// Network reports whether the source needs the network; Request.Offline skips those.
	Network() bool
	// Fetch writes the tree of ref (a full or short sha) into dest, an existing empty directory, as
	// plain files (no .git), and returns the full sha. It returns ErrNotAvailable when it does not
	// have the commit.
	Fetch(ctx context.Context, env SourceEnv, ref, dest string) (sha string, err error)
}

// SourceProbe is what a SourceChecker found.
type SourceProbe struct {
	// Detail says in one line what was found ("kit tarball <path>, sha256 verified").
	Detail string
	// NeedsGit is true when delivering the commit runs git.
	NeedsGit bool
}

// SourceChecker is the optional half of a Source that the preflight (`--dry-run`) uses: it says
// whether the source can deliver ref, without extracting anything and without the network. It
// returns ErrNotAvailable for a source that does not have the commit and an error wrapping
// ErrSourceDamaged for one whose copy fails its checksum. A source without it is assumed to work.
type SourceChecker interface {
	Check(ctx context.Context, env SourceEnv, ref string) (SourceProbe, error)
}

// DefaultSources is the chain a Manager uses when Options.Sources is nil: the online clone, which a
// Request.Offline skips. The already-extracted tree is always checked first, by the Manager.
func DefaultSources() []Source { return []Source{GitCloneSource{}} }

// gitEnv is the environment of every git run: the fixed PATH, no prompts, no locale surprises.
func gitEnv() []string {
	return []string{"PATH=" + execx.FixedPath, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"}
}

func (e SourceEnv) git(ctx context.Context, timeout time.Duration, args ...string) (execx.Result, error) {
	gitPath, err := e.Lookup("git")
	if err != nil {
		return execx.Result{}, &MissingToolError{Tool: "git", Package: "git", Err: err}
	}
	return e.Exec.Run(ctx, execx.Cmd{Path: gitPath, Args: args, Timeout: timeout, MaxOut: 1 << 20, Env: gitEnv()})
}

func gitFailure(what string, res execx.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if len(msg) > 300 {
		msg = msg[len(msg)-300:]
	}
	return fmt.Errorf("%s: git exited with status %d: %s", what, res.ExitCode, msg)
}

// GitCloneSource is the online source: `git clone --filter=blob:none <url>`, `git checkout <ref>`,
// then the .git directory is removed so the tree is the same plain tree every other source gives.
type GitCloneSource struct {
	// URL is the repository; default DefaultCloneURL.
	URL string
}

// Name implements Source.
func (GitCloneSource) Name() string { return "github" }

// Network implements Source.
func (GitCloneSource) Network() bool { return true }

// Fetch implements Source.
func (s GitCloneSource) Fetch(ctx context.Context, env SourceEnv, ref, dest string) (string, error) {
	url := s.URL
	if url == "" {
		url = DefaultCloneURL
	}
	res, err := env.git(ctx, 30*time.Minute, "clone", "--filter=blob:none", url, dest)
	if err != nil || res.ExitCode != 0 {
		return "", gitFailure("cloning "+url, res, err)
	}
	if res, err = env.git(ctx, 10*time.Minute, "-C", dest, "checkout", ref); err != nil || res.ExitCode != 0 {
		return "", gitFailure("checking out "+ref, res, err)
	}
	res, err = env.git(ctx, time.Minute, "-C", dest, "rev-parse", "HEAD")
	if err != nil || res.ExitCode != 0 {
		return "", gitFailure("reading the checked-out commit", res, err)
	}
	if err := env.FS.RemoveAll(filepath.Join(dest, ".git")); err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// GitArchiveSource reads a commit out of a local git repository, a working checkout or a bare
// mirror, with `git archive`: the legacy checkout of the migration and the kit mirror
// (<root>/kit/git/llama.cpp.git). A repository that is missing, or that does not have the commit,
// is ErrNotAvailable.
type GitArchiveSource struct {
	// Repo is the repository directory.
	Repo string
	// Label names the source in logs ("legacy", "kit-mirror"); default "git-archive".
	Label string
}

// Name implements Source.
func (s GitArchiveSource) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return "git-archive"
}

// Network implements Source.
func (GitArchiveSource) Network() bool { return false }

// Fetch implements Source. It runs as root over a repository that another user may own, so every
// git call carries -c safe.directory=<repo> (honoured on the command line, not in the repository).
func (s GitArchiveSource) Fetch(ctx context.Context, env SourceEnv, ref, dest string) (string, error) {
	if fi, err := env.FS.Stat(s.Repo); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrNotAvailable, s.Repo)
	}
	base := []string{"-c", "safe.directory=" + s.Repo, "-C", s.Repo}
	res, err := env.git(ctx, time.Minute, append(base, "rev-parse", "--verify", "--quiet", ref+"^{commit}")...)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%w: %s", ErrNotAvailable, s.Repo)
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if !fullSHARe.MatchString(sha) {
		return "", fmt.Errorf("git rev-parse in %s answered %q", s.Repo, sha)
	}
	tarPath := dest + ".tar"
	defer func() { _ = env.FS.RemoveAll(tarPath) }()
	if res, err = env.git(ctx, 10*time.Minute, append(base, "archive", "--format=tar", "-o", tarPath, sha)...); err != nil || res.ExitCode != 0 {
		return "", gitFailure("archiving "+sha+" from "+s.Repo, res, err)
	}
	f, err := env.FS.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := extractTar(env.FS, f, dest, 0); err != nil {
		return "", fmt.Errorf("extracting %s: %w", sha, err)
	}
	return sha, nil
}

// Check implements SourceChecker with `git cat-file -e <ref>^{commit}` (rebuild-kit.md section
// 4.3.1): the mirror is there and has the commit.
func (s GitArchiveSource) Check(ctx context.Context, env SourceEnv, ref string) (SourceProbe, error) {
	if fi, err := env.FS.Stat(s.Repo); err != nil || !fi.IsDir() {
		return SourceProbe{}, fmt.Errorf("%w: %s is not a directory", ErrNotAvailable, s.Repo)
	}
	res, err := env.git(ctx, time.Minute, "-c", "safe.directory="+s.Repo, "-C", s.Repo, "cat-file", "-e", ref+"^{commit}")
	if err != nil {
		return SourceProbe{}, err
	}
	if res.ExitCode != 0 {
		return SourceProbe{}, fmt.Errorf("%w: %s", ErrNotAvailable, s.Repo)
	}
	return SourceProbe{Detail: "commit " + ref + " is in " + s.Repo, NeedsGit: true}, nil
}

// DirSource builds from a directory the operator names (--source). A git checkout is read with
// `git archive <commit>`, so the tree is exactly the commit whatever is checked out; a plain tree is
// copied as it is (without .git), and then the commit must be a full sha, because nothing can
// verify it.
type DirSource struct {
	Dir string
}

// Name implements Source.
func (DirSource) Name() string { return "dir" }

// Network implements Source.
func (DirSource) Network() bool { return false }

// Fetch implements Source. A directory that is not a llama.cpp tree, or a repository without the
// commit, is an error, not ErrNotAvailable: the operator asked for this directory.
func (s DirSource) Fetch(ctx context.Context, env SourceEnv, ref, dest string) (string, error) {
	fi, err := env.FS.Stat(s.Dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", s.Dir)
	}
	if _, err := env.FS.Lstat(filepath.Join(s.Dir, ".git")); err == nil {
		sha, err := GitArchiveSource{Repo: s.Dir, Label: "dir"}.Fetch(ctx, env, ref, dest)
		if errors.Is(err, ErrNotAvailable) {
			return "", fmt.Errorf("commit %s is not in %s", ref, s.Dir)
		}
		return sha, err
	}
	if !fullSHARe.MatchString(ref) {
		return "", fmt.Errorf("%s has no git history, so --commit must be a full 40-character sha", s.Dir)
	}
	if !treeComplete(env.FS, s.Dir) {
		return "", fmt.Errorf("%s is not a llama.cpp source tree (no CMakeLists.txt)", s.Dir)
	}
	if err := copyTree(env.FS, s.Dir, dest, "", env.Log); err != nil {
		return "", err
	}
	return ref, nil
}

// Check implements SourceChecker.
func (s DirSource) Check(ctx context.Context, env SourceEnv, ref string) (SourceProbe, error) {
	fi, err := env.FS.Stat(s.Dir)
	if err != nil || !fi.IsDir() {
		return SourceProbe{}, fmt.Errorf("%s is not a directory", s.Dir)
	}
	if _, err := env.FS.Lstat(filepath.Join(s.Dir, ".git")); err == nil {
		p, err := GitArchiveSource{Repo: s.Dir, Label: "dir"}.Check(ctx, env, ref)
		if errors.Is(err, ErrNotAvailable) {
			return SourceProbe{}, fmt.Errorf("commit %s is not in %s", ref, s.Dir)
		}
		return p, err
	}
	if !fullSHARe.MatchString(ref) {
		return SourceProbe{}, fmt.Errorf("%s has no git history, so --commit must be a full 40-character sha", s.Dir)
	}
	if !treeComplete(env.FS, s.Dir) {
		return SourceProbe{}, fmt.Errorf("%s is not a llama.cpp source tree (no CMakeLists.txt)", s.Dir)
	}
	return SourceProbe{Detail: s.Dir + " is a source tree"}, nil
}

// treeComplete tells whether dir holds an extracted llama.cpp tree. Extraction is a rename of a
// finished directory, so a tree at its final path is whole; the marker file catches trees put there
// by hand and directories that were emptied.
func treeComplete(fsys FS, dir string) bool {
	fi, err := fsys.Stat(filepath.Join(dir, "CMakeLists.txt"))
	return err == nil && fi.Mode().IsRegular()
}

// ---- the kit sources (KIT-04) ----------------------------------------------------------

// kitFS lets the kit lookup read through the build manager's FS.
type kitFS struct{ FS }

func (k kitFS) OpenRead(name string) (io.ReadCloser, error) { return k.FS.Open(name) }

// KitTarballSource is the first offline source: the source tarball of the commit on any content root,
// kit/src/<stem>-<commit>.tar.gz, accepted only when its sha256 is the one the kit recorded
// (rebuild-kit.md sections 3.2.1 and 4.3.1). A tarball that fails the check is skipped with
// ErrSourceDamaged so that the next source (the kit mirror) is tried; a commit with no tarball is
// ErrNotAvailable. The digest is checked twice: before extraction, to pick a good root, and again
// over the bytes that are extracted, so a file replaced in between is refused.
type KitTarballSource struct {
	// Roots are the content roots to look on, the internal root first.
	Roots []string
	// Stem is the kit name of the source ("llama.cpp"); default kit.SourceStemLlama.
	Stem string
}

func (s KitTarballSource) stem() string {
	if s.Stem != "" {
		return s.Stem
	}
	return kit.SourceStemLlama
}

// Name implements Source.
func (KitTarballSource) Name() string { return "kit-tarball" }

// Network implements Source.
func (KitTarballSource) Network() bool { return false }

func (s KitTarballSource) find(env SourceEnv, ref string) (kit.Tarball, error) {
	tb, err := kit.SourceFinder{FS: kitFS{env.FS}, Roots: s.Roots}.FindTarball(s.stem(), ref)
	if errors.Is(err, kit.ErrSourceNotFound) {
		return kit.Tarball{}, fmt.Errorf("%w: no tarball for %s in the kit", ErrNotAvailable, ref)
	}
	return tb, err
}

// Check implements SourceChecker.
func (s KitTarballSource) Check(_ context.Context, env SourceEnv, ref string) (SourceProbe, error) {
	tb, err := s.find(env, ref)
	if err != nil {
		return SourceProbe{}, err
	}
	return SourceProbe{Detail: "kit tarball " + tb.Path + " (sha256 matches the kit manifest)"}, nil
}

// Fetch implements Source: the tarball's top-level directory (git archive --prefix) is dropped.
func (s KitTarballSource) Fetch(_ context.Context, env SourceEnv, ref, dest string) (string, error) {
	tb, err := s.find(env, ref)
	if err != nil {
		return "", err
	}
	f, err := env.FS.Open(tb.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if err := extractTar(env.FS, io.TeeReader(f, h), dest, 1); err != nil {
		return "", fmt.Errorf("extracting %s: %w", tb.Path, err)
	}
	// extractTar stops at the end of the tar stream; read what is left so the digest covers the file.
	if _, err := io.Copy(io.Discard, io.TeeReader(f, h)); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != tb.SHA256 {
		return "", fmt.Errorf("%w: %s changed while it was read", ErrSourceDamaged, tb.Path)
	}
	return tb.Commit, nil
}

// KitSources is the offline chain of --from-kit: the verified source tarball of the commit on any
// root, then the git mirror kit/git/<stem>.git of every root (`git archive <commit>`), in root order.
// stem is the kit name of the source ("llama.cpp", see Recipe.Name).
func KitSources(stem string, roots []string) []Source {
	out := []Source{KitTarballSource{Roots: roots, Stem: stem}}
	for _, root := range roots {
		out = append(out, GitArchiveSource{Repo: kit.MirrorDir(root, stem), Label: "kit-mirror"})
	}
	return out
}

// ChainWithKit is the default chain of a node that has a kit: the kit sources, then (online only) the
// GitHub clone. Options.Sources takes it; Request.Offline drops the clone.
func ChainWithKit(stem string, roots []string, cloneURL string) []Source {
	return append(KitSources(stem, roots), GitCloneSource{URL: cloneURL})
}

// sourceChain is the chain a build walks: Request.Sources and --from-kit replace Options.Sources.
// (--source replaces everything with the one directory; ensureSource and the preflight apply that.)
func (m *Manager) sourceChain(req Request) []Source {
	switch {
	case req.Sources != nil:
		return req.Sources
	case req.FromKit:
		return KitSources(m.opt.Recipe.Name, m.kitRoots())
	}
	return m.opt.Sources
}

func (m *Manager) kitRoots() []string {
	if m.opt.KitRoots == nil {
		return nil
	}
	return m.opt.KitRoots()
}

// existingTree returns the full sha of an already-extracted tree for ref: src/<ref> for a full sha,
// the one tree whose name starts with ref for a short one. "" when there is none (or the short sha
// is ambiguous).
func (m *Manager) existingTree(ref string) string {
	srcRoot := filepath.Join(m.root, srcDir)
	if fullSHARe.MatchString(ref) {
		if treeComplete(m.opt.FS, filepath.Join(srcRoot, ref)) {
			return ref
		}
		return ""
	}
	entries, err := m.opt.FS.ReadDir(srcRoot)
	if err != nil {
		return ""
	}
	var found string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || !fullSHARe.MatchString(n) || !strings.HasPrefix(n, ref) || !treeComplete(m.opt.FS, filepath.Join(srcRoot, n)) {
			continue
		}
		if found != "" {
			return ""
		}
		found = n
	}
	return found
}

// ensureSource makes src/<sha>/ exist and returns the full sha: the tree already there, else the
// first source of the chain that has the commit (each extracted into src/<ref>.tmp first and renamed
// into place, so a tree at its final path is always whole). With --source the named directory
// replaces the tree that is there.
func (m *Manager) ensureSource(ctx context.Context, req Request, in Inputs) (string, *Error) {
	fsys := m.opt.FS
	ref := in.Commit
	srcRoot := filepath.Join(m.root, srcDir)
	fail := func(code, msg, cause string, err error) (string, *Error) {
		return "", newErr(code, StageSource, msg, err, "cause", cause, "commit", ref)
	}
	if err := fsys.MkdirAll(srcRoot, 0o755); err != nil {
		return fail(CodeBuildFailed, "The source folder could not be created.", "source_dir", err)
	}

	explicit := req.SourceDir != ""
	if explicit && filepath.Clean(req.SourceDir) == filepath.Join(srcRoot, ref) && treeComplete(fsys, req.SourceDir) {
		explicit = false // the operator points at the canonical tree: use it as it is
	}
	if !explicit {
		if sha := m.existingTree(ref); sha != "" {
			m.touch(filepath.Join(srcRoot, sha))
			m.log.Info("using the extracted llama.cpp source", "commit", sha)
			return sha, nil
		}
	}

	sources := m.sourceChain(req)
	if explicit {
		sources = []Source{DirSource{Dir: req.SourceDir}}
	}
	env := SourceEnv{Exec: m.opt.Exec, FS: fsys, Lookup: m.opt.Lookup, Log: m.log}
	var hard, damaged error
	for _, s := range sources {
		if req.Offline && s.Network() {
			continue
		}
		tmp := filepath.Join(srcRoot, ref+suffixTmp)
		cleanup := func() { _ = fsys.RemoveAll(tmp); _ = fsys.RemoveAll(tmp + ".tar") }
		cleanup()
		if err := fsys.MkdirAll(tmp, 0o755); err != nil {
			return fail(CodeBuildFailed, "The source folder could not be created.", "source_dir", err)
		}
		m.log.Info("fetching the llama.cpp source", "source", s.Name(), "commit", ref)
		sha, err := s.Fetch(ctx, env, ref, tmp)
		if err == nil {
			switch {
			case !fullSHARe.MatchString(sha) || !strings.HasPrefix(sha, ref):
				err = fmt.Errorf("source %s answered the commit %q for %s", s.Name(), sha, ref)
			case !treeComplete(fsys, tmp):
				err = fmt.Errorf("source %s delivered a tree without CMakeLists.txt", s.Name())
			}
		}
		if err != nil {
			cleanup()
			if ctx.Err() != nil {
				return "", &Error{Code: CodeBuildFailed, Stage: StageSource, Message: "cancelled", Err: ctx.Err(), Details: map[string]any{}}
			}
			if errors.Is(err, ErrNotAvailable) {
				m.log.Info("the source does not have the commit", "source", s.Name(), "commit", ref)
				continue
			}
			m.log.Warn("the source failed", "source", s.Name(), "commit", ref, "error", err)
			hard = fmt.Errorf("%s: %w", s.Name(), err)
			if errors.Is(err, ErrSourceDamaged) {
				damaged = hard
			}
			continue
		}
		final := filepath.Join(srcRoot, sha)
		if err := fsys.RemoveAll(final); err != nil {
			cleanup()
			return fail(CodeBuildFailed, "The old source tree could not be replaced.", "source_dir", err)
		}
		if err := fsys.Rename(tmp, final); err != nil {
			cleanup()
			return fail(CodeBuildFailed, "The source tree could not be put in place.", "source_dir", err)
		}
		m.touch(final)
		return sha, nil
	}

	switch {
	case damaged != nil:
		return fail(CodeSourceDamaged, "A llama.cpp source package of the kit is damaged and no other source has the commit "+ref+": "+damaged.Error(), "source_damaged", damaged)
	case hard != nil:
		return fail(CodeBuildFailed, "The llama.cpp source could not be fetched: "+hard.Error(), "source_failed", hard)
	case req.Offline:
		return fail(CodeNoSource, "The kit and this computer do not have the llama.cpp version "+ref+". Sync the kit with internet, or choose a commit that is in the kit.", "no_source", nil)
	default:
		return fail(CodeBuildFailed, "No source for llama.cpp "+ref+" is configured.", "no_source", nil)
	}
}

// touch marks a source tree as used now, so that retention keeps the trees of the latest commits.
func (m *Manager) touch(dir string) {
	now := m.opt.Clock()
	if err := m.opt.FS.Chtimes(dir, now, now); err != nil {
		m.log.Warn("could not mark the source tree as used", "dir", dir, "error", err)
	}
}

// ExtractArchive unpacks a tar stream, gzip-compressed or not, into dest (an existing directory),
// dropping the first strip path components of every name. It is the one extraction every kit archive
// goes through (source tarballs, prebuilt builds, the CUDA redistributables; the whisper and agent
// builds reuse it). It refuses, without writing past the offending entry: names that are absolute or
// climb out with "..", special files and hard links, a symlink whose target is absolute, climbs out
// of the tree, or is not in its cleaned form (a/../b is how a chain of links reaches outside), an
// entry whose parent directory is a symlink (nothing is ever written through a link), and a file or
// directory that would replace a symlink. pax headers (git archive writes the commit id in one) are
// skipped.
func ExtractArchive(fsys FS, r io.Reader, dest string, strip int) error {
	return extractTar(fsys, r, dest, strip)
}

func extractTar(fsys FS, r io.Reader, dest string, strip int) error {
	br := bufio.NewReader(r)
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	} else {
		r = br
	}
	tr := tar.NewReader(r)
	real := map[string]bool{"": true} // directories (slash paths below dest) known to be real directories
	// prepare checks that every parent of rel is a real directory, creating the missing ones.
	prepare := func(rel string) error {
		dir := path.Dir(rel)
		if dir == "." {
			return nil
		}
		var cur string
		for _, part := range strings.Split(dir, "/") {
			cur = path.Join(cur, part)
			if real[cur] {
				continue
			}
			fi, err := fsys.Lstat(filepath.Join(dest, filepath.FromSlash(cur)))
			switch {
			case errors.Is(err, os.ErrNotExist):
				if err := fsys.MkdirAll(filepath.Join(dest, filepath.FromSlash(cur)), 0o755); err != nil {
					return err
				}
			case err != nil:
				return err
			case !fi.IsDir():
				return fmt.Errorf("%q is a link or a file where a directory is needed", cur)
			}
			real[cur] = true
		}
		return nil
	}
	// replaceable refuses to write over a symlink (and over a directory with a file).
	replaceable := func(target string, wantDir bool) error {
		fi, err := fsys.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("it would replace a link")
		}
		if fi.IsDir() != wantDir {
			return errors.New("it would replace a directory with a file or the reverse")
		}
		return nil
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}
		rel, ok := stripName(hdr.Name, strip)
		if !ok {
			return fmt.Errorf("unsafe path %q in the archive", hdr.Name)
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := prepare(rel); err != nil {
			return fmt.Errorf("the entry %q: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := replaceable(target, true); err != nil {
				return fmt.Errorf("the entry %q: %w", hdr.Name, err)
			}
			if err := fsys.MkdirAll(target, 0o755); err != nil {
				return err
			}
			real[rel] = true
		case tar.TypeReg:
			if err := replaceable(target, false); err != nil {
				return fmt.Errorf("the entry %q: %w", hdr.Name, err)
			}
			perm := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				perm = 0o755
			}
			w, err := fsys.Create(target, perm)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(w, tr) //nolint:gosec // the archive is a pinned commit or a build from the kit, verified by its digest
			if err := w.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return cerr
			}
		case tar.TypeSymlink:
			if !safeLinkTarget(rel, hdr.Linkname) {
				return fmt.Errorf("the link %q points outside the tree or is not a plain relative path", hdr.Name)
			}
			if err := replaceable(target, false); err != nil {
				return fmt.Errorf("the entry %q: %w", hdr.Name, err)
			}
			_ = fsys.RemoveAll(target)
			if err := fsys.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported entry %q (type %c) in the archive", hdr.Name, hdr.Typeflag)
		}
	}
}

// safeLinkTarget reports whether a symlink named rel (slash path inside the tree) may point at link:
// relative, not empty, in cleaned form (so ".." only leads) and inside the tree. With every parent
// directory a real one, the cleaned form resolves on disk exactly as it reads, so a link can never
// reach outside however the links are chained.
func safeLinkTarget(rel, link string) bool {
	if link == "" || path.IsAbs(link) || path.Clean(link) != link {
		return false
	}
	return insideTree(path.Join(path.Dir(rel), link))
}

// stripName drops strip leading components of a tar name and reports whether the rest is a safe
// relative path. "" with ok means the entry is the stripped directory itself.
func stripName(name string, strip int) (string, bool) {
	if strings.HasPrefix(name, "/") {
		return "", false
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		switch p {
		case "", ".":
		case "..":
			return "", false
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) <= strip {
		return "", true
	}
	return strings.Join(parts[strip:], "/"), true
}

// insideTree reports whether a slash path, relative to the tree root, stays inside it.
func insideTree(p string) bool {
	p = path.Clean(p)
	return p != ".." && !strings.HasPrefix(p, "../") && !path.IsAbs(p)
}

// copyTree copies the directory src into dst (which exists), leaving out .git; rel is the slash path
// of src inside the tree, which a link is resolved against. Regular files keep
// their executable bit; symlinks that stay inside the tree are recreated and any other symlink is
// skipped with a warning.
func copyTree(fsys FS, src, dst, rel string, log *slog.Logger) error {
	entries, err := fsys.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}
		from, to := filepath.Join(src, name), filepath.Join(dst, name)
		fi, err := fsys.Lstat(from)
		if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			if err := fsys.MkdirAll(to, 0o755); err != nil {
				return err
			}
			if err := copyTree(fsys, from, to, path.Join(rel, name), log); err != nil {
				return err
			}
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := fsys.Readlink(from)
			if err != nil {
				return err
			}
			if !safeLinkTarget(path.Join(rel, name), link) {
				if log != nil {
					log.Warn("skipping a symlink that leaves the source tree", "path", from)
				}
				continue
			}
			if err := fsys.Symlink(link, to); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			perm := os.FileMode(0o644)
			if fi.Mode().Perm()&0o111 != 0 {
				perm = 0o755
			}
			if _, err := copyFile(fsys, from, to, perm, false); err != nil {
				return err
			}
		}
	}
	return nil
}
