package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marinoscar/local-ai/agent/internal/fsperm"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/sync/plan"
)

// Codes of the git producer besides the shared ones.
const (
	// CodeFetch: a network transfer of a producer failed (LAI-SYN-031).
	CodeFetch = doctor.SYN031
	// CodeNoVersion: the newest version could not be read from the source (LAI-SYN-033).
	CodeNoVersion = doctor.SYN033
)

// Run-time bounds of the git subprocesses.
const (
	gitResolveTimeout = 30 * time.Second
	gitCloneTimeout   = 2 * time.Hour
	gitUpdateTimeout  = time.Hour
	gitFsckTimeout    = 30 * time.Minute
	gitArchiveTimeout = 30 * time.Minute
	gitQuickTimeout   = time.Minute
	gzipTimeout       = 30 * time.Minute
)

var (
	fullSHA      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	mirrorNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// pinKeysDefault maps a mirror name to the key of kit/catalog/pins.json that lists its pinned commits
// (rebuild-kit.md section 3.3).
var pinKeysDefault = map[string]string{
	"llama.cpp":   "llama_cpp",
	"whisper.cpp": "whisper_cpp",
}

// GitMirror is the git_mirror resolver (ark-sync.md section 4.9.6): a bare mirror in
// kit/git/<name>.git, fetched in place, plus a reproducible tarball of every pinned commit in
// kit/src/ when the item says also_tarball_of: pinned.
type GitMirror struct {
	Env *Env
	// NodeCommits returns the commits this node itself pins for a mirror name (node.yaml
	// llama.commit for "llama.cpp"). nil means none. An internal root verifies and archives only
	// these; an ark drive adds every commit of kit/catalog/pins.json.
	NodeCommits func(mirror string) []string
	// PinKeys overrides the mirror-name to pins.json-key map (default llama.cpp and whisper.cpp).
	PinKeys map[string]string

	// allowLocal lets a test clone from a local path: it drops protocol.file.allow=never and
	// GIT_ALLOW_PROTOCOL=https, the two guards that forbid it. Production never sets it.
	allowLocal bool
}

// SourceType implements NamedResolver.
func (*GitMirror) SourceType() string { return "git_mirror" }

// mirrorSource validates the catalog fields of a git_mirror item and returns the mirror name and the
// URL's host. The URL must be https with a host and no credentials, and the name is the URL's last
// path element without ".git": nothing else ever becomes part of an argv or a path.
func (g *GitMirror) mirrorSource(it plan.CatalogItem) (name, host, rawURL string, err error) {
	if it.Content == nil || it.Content.Source.Type != "git_mirror" {
		return "", "", "", fail(CodeRefused, nil, "item %s is not a git_mirror item", it.ID)
	}
	raw := it.Content.Source.URL
	if g.allowLocal && !strings.Contains(raw, "://") && filepath.IsAbs(raw) {
		return strings.TrimSuffix(filepath.Base(raw), ".git"), "localhost", raw, nil
	}
	u, perr := url.Parse(raw)
	switch {
	case perr != nil || u.Host == "" || u.Hostname() == "":
		return "", "", "", fail(CodeRefused, nil, "item %s has no usable git URL", it.ID)
	case u.Scheme != "https":
		return "", "", "", fail(CodeRefused, nil, "item %s: the git URL must be https", it.ID)
	case u.User != nil, u.RawQuery != "", u.Fragment != "":
		return "", "", "", fail(CodeRefused, nil, "item %s: the git URL carries credentials, a query or a fragment", it.ID)
	}
	base := path.Base(strings.TrimSuffix(u.Path, "/"))
	name = strings.TrimSuffix(base, ".git")
	if !mirrorNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", "", "", fail(CodeRefused, nil, "item %s: %q is not a usable mirror name", it.ID, name)
	}
	return name, strings.ToLower(u.Hostname()), raw, nil
}

// gitEnv is the minimal environment of every git call (ark-sync.md section 4.9.6): no prompts, no
// system config, HOME the sync directory, and only the https transport allowed.
func (g *GitMirror) gitEnv() []string {
	env := []string{
		"PATH=" + execx.FixedPath,
		"HOME=" + g.Env.SyncHome,
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	if !g.allowLocal {
		env = append(env, "GIT_ALLOW_PROTOCOL=https")
	}
	return env
}

// netArgs are the -c options in front of every network git call.
func (g *GitMirror) netArgs(proxy string) []string {
	var a []string
	if !g.allowLocal {
		a = append(a, "-c", "protocol.file.allow=never")
	}
	a = append(a, "-c", "core.hooksPath=/dev/null")
	if proxy != "" {
		a = append(a, "-c", "http.proxy=http://"+proxy)
	}
	return a
}

// repoArgs are the options in front of a git call on a repository we may not own: safe.directory is
// honoured on the command line (not in the repository), the same as the build manager does.
func (g *GitMirror) repoArgs(repo string) []string {
	return []string{"-c", "safe.directory=" + repo, "-c", "core.hooksPath=/dev/null", "-C", repo}
}

func (g *GitMirror) git(ctx context.Context, bin string, timeout time.Duration, args ...string) (execx.Result, error) {
	return g.Env.run(ctx, cmd{bin: bin, args: args, env: g.gitEnv(), timeout: timeout, maxOut: smallOut})
}

// Resolve implements plan.Resolver: the version is the first 12 hex of the remote HEAD, read with
// `git ls-remote` (metadata only, no objects).
func (g *GitMirror) Resolve(ctx context.Context, it plan.CatalogItem) (plan.Resolved, error) {
	_, _, rawURL, err := g.mirrorSource(it)
	if err != nil {
		return plan.Resolved{}, err
	}
	if err := g.Env.requireOnline(); err != nil {
		return plan.Resolved{}, err
	}
	bin, err := g.Env.tool("git")
	if err != nil {
		return plan.Resolved{}, err
	}
	args := append(g.netArgs(""), "ls-remote", "--", rawURL, "HEAD")
	res, err := g.git(ctx, bin, gitResolveTimeout, args...)
	if err != nil {
		return plan.Resolved{}, fail(CodeNoVersion, err, "could not ask %s for its newest commit", it.ID)
	}
	if res.ExitCode != 0 || res.Truncated {
		return plan.Resolved{}, fail(CodeNoVersion, nil, "git ls-remote for %s failed: %s", it.ID, Clip(res.Stderr, 200))
	}
	sha, ok := parseLsRemoteHead(res.Stdout)
	if !ok {
		return plan.Resolved{}, fail(CodeNoVersion, nil, "the source of %s did not report a HEAD commit", it.ID)
	}
	return plan.Resolved{Version: sha[:12]}, nil
}

// parseLsRemoteHead finds the "<40 hex>\tHEAD" line of a ls-remote answer.
func parseLsRemoteHead(out []byte) (string, bool) {
	for _, line := range strings.Split(string(out), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok && ref == "HEAD" && fullSHA.MatchString(sha) {
			return sha, true
		}
	}
	return "", false
}

// pinnedCommits is the set of commits the mirror must contain and archive: the node's own, and on a
// drive every commit of kit/catalog/pins.json for this mirror. Every value is a 40-hex commit or it
// is refused, because it ends up in an argv.
func (g *GitMirror) pinnedCommits(req Request, name string) ([]string, error) {
	set := map[string]bool{}
	add := func(c string) error {
		c = strings.ToLower(strings.TrimSpace(c))
		if !fullSHA.MatchString(c) {
			return fail(CodeMirror, nil, "a pinned commit for %s is not 40 hex digits", name)
		}
		set[c] = true
		return nil
	}
	if g.NodeCommits != nil {
		for _, c := range g.NodeCommits(name) {
			if c == "" {
				continue
			}
			if err := add(c); err != nil {
				return nil, err
			}
		}
	}
	if req.Drive {
		keys := g.PinKeys
		if keys == nil {
			keys = pinKeysDefault
		}
		if key, ok := keys[name]; ok {
			cs, err := readPins(filepath.Join(req.Root, "kit", "catalog", "pins.json"), key)
			if err != nil {
				return nil, err
			}
			for _, c := range cs {
				if err := add(c); err != nil {
					return nil, err
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}

// readPins reads the commits under key ("llama_cpp") of a pins.json (rebuild-kit.md section 3.3). A
// missing file means no pins; a damaged one is an error (a drive must not silently stop keeping the
// sources it promised).
func readPins(file, key string) ([]string, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fail(CodeMirror, err, "cannot read pins.json")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fail(CodeMirror, err, "pins.json is not valid JSON")
	}
	raw, ok := doc[key]
	if !ok {
		return nil, nil
	}
	var entries []struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fail(CodeMirror, err, "pins.json: %s is not a list of commits", key)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Commit)
	}
	return out, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Produce implements Producer: clone or update the mirror, verify it, and archive the pinned commits.
//
// The first time the mirror is cloned into <staging>/<name>.git (part "mirror", kind tree, to
// kit/git/<name>.git) and the engine swaps it in; afterwards the present mirror is updated in place
// (part "mirror", kind in_place) because it is large and append-mostly. The tarballs are staged under
// <staging>/src/ (part "tarballs", to kit/src). Product.Version is the mirror's HEAD after the fetch,
// which may be newer than req.Version if the source moved in between.
func (g *GitMirror) Produce(ctx context.Context, req Request) (Product, error) {
	name, host, rawURL, err := g.mirrorSource(req.Item)
	if err != nil {
		return Product{}, err
	}
	if g.Env.SyncHome == "" {
		return Product{}, fail(CodeRefused, nil, "the sync directory is not configured")
	}
	if err := g.Env.requireOnline(); err != nil {
		return Product{}, err
	}
	bin, err := g.Env.tool("git")
	if err != nil {
		return Product{}, err
	}
	proxy, stop, perr := g.Env.startProxy(ctx, []string{host})
	if perr != nil {
		return Product{}, fail(CodeFetch, perr, "cannot start the bandwidth limiter")
	}
	defer stop()

	staging := req.StagingDir()
	mirrorRel := "kit/git/" + name + ".git"
	present := filepath.Join(req.Root, filepath.FromSlash(mirrorRel))
	var mirror string
	part := Part{Name: "mirror", Record: []string{"HEAD"}}
	if isDir(present) {
		mirror = present
		part.Kind, part.To = PartInPlace, mirrorRel
		if err := g.update(ctx, bin, mirror, proxy); err != nil {
			// A fetch that fails over a damaged repository is a damaged mirror, not a flaky network:
			// tell them apart with fsck, so the remedy (prune the mirror) is the right one.
			if CodeOf(err) == CodeFetch {
				if ferr := g.fsck(ctx, bin, mirror, name); ferr != nil {
					return Product{}, ferr
				}
			}
			return Product{}, err
		}
	} else {
		mirror = filepath.Join(staging, name+".git")
		part.Kind, part.From, part.To = PartTree, name+".git", mirrorRel
		if err := g.cloneOrResume(ctx, bin, rawURL, mirror, proxy); err != nil {
			return Product{}, err
		}
	}

	if err := g.fsck(ctx, bin, mirror, name); err != nil {
		return Product{}, err
	}
	head, err := g.revParse(ctx, bin, mirror, "HEAD")
	if err != nil {
		return Product{}, fail(CodeMirror, err, "the mirror of %s has no HEAD commit", name)
	}
	prod := Product{Version: head[:12], SortKey: head[:12], Trust: manifest.TrustComputed,
		Source: map[string]any{"url": rawURL, "commit": head}}

	if req.Item.Content.Source.AlsoTarballOf == "pinned" {
		commits, err := g.pinnedCommits(req, name)
		if err != nil {
			return Product{}, err
		}
		for _, c := range commits {
			ok, err := g.hasCommit(ctx, bin, mirror, c)
			if err != nil {
				return Product{}, fail(CodeMirror, err, "cannot check the mirror of %s", name)
			}
			if !ok {
				return Product{}, fail(CodeMirror, nil, "the mirror of %s lacks the pinned commit %s", name, c[:12])
			}
		}
		files, err := g.tarballs(ctx, bin, mirror, staging, name, commits)
		if err != nil {
			return Product{}, err
		}
		if len(files) > 0 {
			var total int64
			for _, f := range files {
				total += f.SizeBytes
			}
			prod.Parts = append(prod.Parts, Part{Name: "tarballs", Kind: PartFile, From: "src", To: "kit/src", Files: files, Bytes: total})
		}
	}
	prod.Parts = append([]Part{part}, prod.Parts...)
	return prod, nil
}

// update fetches into the present mirror. Git writes objects before refs and updates refs one by one,
// so a crash leaves a valid repository.
func (g *GitMirror) update(ctx context.Context, bin, mirror, proxy string) error {
	args := append(append(g.netArgs(proxy), "-c", "safe.directory="+mirror, "-C", mirror), "remote", "update", "--prune")
	res, err := g.git(ctx, bin, gitUpdateTimeout, args...)
	return g.fetchResult("updating the mirror", res, err)
}

// cloneOrResume makes the first clone into dest. A leftover of an earlier attempt that is a valid
// repository is finished with a fetch (a fetch resumes naturally); anything else is moved aside, never
// deleted (deleting is retention's job).
func (g *GitMirror) cloneOrResume(ctx context.Context, bin, rawURL, dest, proxy string) error {
	if _, err := os.Lstat(dest); err == nil {
		if g.isRepo(ctx, bin, dest) {
			if err := g.update(ctx, bin, dest, proxy); err != nil {
				return err
			}
			return g.fetchTags(ctx, bin, dest, proxy)
		}
		aside := fmt.Sprintf("%s.unusable-%d", dest, g.Env.now().Unix())
		if err := os.Rename(dest, aside); err != nil {
			return fail(CodeFetch, err, "cannot move an unusable earlier clone aside")
		}
		g.Env.log().Warn("an unusable earlier clone was moved aside", "component", "sync", "path", filepath.Base(aside))
	}
	if err := fsperm.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fail(CodeFetch, err, "cannot create the staging directory")
	}
	args := append(g.netArgs(proxy), "clone", "--mirror", "--no-tags", "--", rawURL, dest)
	res, err := g.git(ctx, bin, gitCloneTimeout, args...)
	if err := g.fetchResult("cloning the mirror", res, err); err != nil {
		return err
	}
	return g.fetchTags(ctx, bin, dest, proxy)
}

func (g *GitMirror) fetchTags(ctx context.Context, bin, dest, proxy string) error {
	args := append(append(g.netArgs(proxy), "-C", dest), "fetch", "--tags")
	res, err := g.git(ctx, bin, gitUpdateTimeout, args...)
	return g.fetchResult("fetching the tags", res, err)
}

func (g *GitMirror) isRepo(ctx context.Context, bin, dir string) bool {
	res, err := g.git(ctx, bin, gitQuickTimeout, append(g.repoArgs(dir), "rev-parse", "--is-bare-repository")...)
	return err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) == "true"
}

// fetchResult turns the outcome of a network git call into an error: cancellation passes through, a
// start failure or a timeout is LAI-SYN-031, a non-zero exit carries git's (clipped) message.
func (g *GitMirror) fetchResult(what string, res execx.Result, err error) error {
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return err
	case err != nil:
		return fail(CodeFetch, err, "%s did not finish", what)
	case res.ExitCode != 0:
		return fail(CodeFetch, nil, "%s failed: %s", what, Clip(res.Stderr, 300))
	}
	return nil
}

func (g *GitMirror) fsck(ctx context.Context, bin, mirror, name string) error {
	res, err := g.git(ctx, bin, gitFsckTimeout, append(g.repoArgs(mirror), "fsck", "--connectivity-only", "--no-dangling")...)
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		return err
	case err != nil:
		return fail(CodeMirror, err, "could not check the mirror of %s", name)
	case res.ExitCode != 0:
		return fail(CodeMirror, nil, "the mirror of %s failed git fsck: %s", name, Clip(res.Stderr, 300))
	}
	return nil
}

func (g *GitMirror) revParse(ctx context.Context, bin, repo, rev string) (string, error) {
	res, err := g.git(ctx, bin, gitQuickTimeout, append(g.repoArgs(repo), "rev-parse", "--verify", "--quiet", rev+"^{commit}")...)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(res.Stdout))
	if res.ExitCode != 0 || !fullSHA.MatchString(sha) {
		return "", errors.New("not a commit")
	}
	return sha, nil
}

// hasCommit is `git cat-file -e <commit>^{commit}`: exit 0 present, anything else absent.
func (g *GitMirror) hasCommit(ctx context.Context, bin, repo, commit string) (bool, error) {
	res, err := g.git(ctx, bin, gitQuickTimeout, append(g.repoArgs(repo), "cat-file", "-e", commit+"^{commit}")...)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// tarballs writes <staging>/src/<name>-<commit>.tar.gz for every commit: `git archive` writes the tar
// straight to a file (no pipe through memory, the runner buffers stdout) and `gzip -n -9` compresses
// it in place, so the bytes depend only on the commit: two runs give one sha256.
func (g *GitMirror) tarballs(ctx context.Context, bin, mirror, staging, name string, commits []string) ([]manifest.File, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	gzipBin, err := g.Env.tool("gzip")
	if err != nil {
		return nil, err
	}
	srcDir := filepath.Join(staging, "src")
	if err := fsperm.MkdirAll(srcDir, 0o755); err != nil {
		return nil, fail(CodeMirror, err, "cannot create the staging directory")
	}
	var files []manifest.File
	for _, c := range commits {
		base := name + "-" + c
		tar := filepath.Join(srcDir, base+".tar")
		args := append(g.repoArgs(mirror), "archive", "--format=tar", "--prefix="+base+"/", "-o", tar, c)
		res, err := g.git(ctx, bin, gitArchiveTimeout, args...)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fail(CodeMirror, err, "git archive of %s did not finish", c[:12])
		}
		if res.ExitCode != 0 {
			return nil, fail(CodeMirror, nil, "git archive of %s failed: %s", c[:12], Clip(res.Stderr, 300))
		}
		res, err = g.Env.run(ctx, cmd{bin: gzipBin, args: []string{"-n", "-9", "-f", tar}, env: []string{"PATH=" + execx.FixedPath}, timeout: gzipTimeout, maxOut: smallOut})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fail(CodeMirror, err, "gzip of %s did not finish", c[:12])
		}
		if res.ExitCode != 0 {
			return nil, fail(CodeMirror, nil, "gzip of %s failed: %s", c[:12], Clip(res.Stderr, 200))
		}
		sum, size, err := hashFile(tar + ".gz")
		if err != nil {
			return nil, fail(CodeMirror, err, "cannot read the tarball of %s", c[:12])
		}
		files = append(files, manifest.File{
			Kind: "source_tarball", Path: base + ".tar.gz", SizeBytes: size, SHA256: sum, SHA256Source: "computed_after_verify",
		})
	}
	return files, nil
}
