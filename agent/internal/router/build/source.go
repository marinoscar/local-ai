package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
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
)

// DefaultCloneURL is the upstream repository of the online source.
const DefaultCloneURL = "https://github.com/ggml-org/llama.cpp.git"

// ErrNotAvailable is what a Source returns (wrapped is fine) when it does not have the commit: the
// next source of the chain is tried. Any other error means the source has the commit but could not
// deliver it, which stops the chain from ending in LAI-KIT-002.
var ErrNotAvailable = errors.New("build: the source does not have this commit")

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
		return execx.Result{}, fmt.Errorf("git is needed for this source: %w", err)
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

// treeComplete tells whether dir holds an extracted llama.cpp tree. Extraction is a rename of a
// finished directory, so a tree at its final path is whole; the marker file catches trees put there
// by hand and directories that were emptied.
func treeComplete(fsys FS, dir string) bool {
	fi, err := fsys.Stat(filepath.Join(dir, "CMakeLists.txt"))
	return err == nil && fi.Mode().IsRegular()
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

	sources := m.opt.Sources
	if req.Sources != nil {
		sources = req.Sources
	}
	if explicit {
		sources = []Source{DirSource{Dir: req.SourceDir}}
	}
	env := SourceEnv{Exec: m.opt.Exec, FS: fsys, Lookup: m.opt.Lookup, Log: m.log}
	var hard error
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

// extractTar unpacks a tar stream, gzip-compressed or not, into dest, dropping the first strip path
// components of every name. Names that are absolute or climb out with "..", links that point
// outside the tree and special files are refused. pax headers (git archive writes the commit id in
// one) are skipped.
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
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := fsys.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				perm = 0o755
			}
			w, err := fsys.Create(target, perm)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(w, tr) //nolint:gosec // the archive is a pinned commit from the kit or git
			if err := w.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return cerr
			}
		case tar.TypeSymlink:
			if path.IsAbs(hdr.Linkname) || !insideTree(path.Join(path.Dir(rel), hdr.Linkname)) {
				return fmt.Errorf("the link %q points outside the tree", hdr.Name)
			}
			if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := fsys.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported entry %q (type %c) in the archive", hdr.Name, hdr.Typeflag)
		}
	}
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
			if path.IsAbs(link) || !insideTree(path.Join(rel, link)) {
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
