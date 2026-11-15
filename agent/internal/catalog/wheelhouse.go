package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// The pip_wheelhouse source type (catalog.md section 3, rebuild-kit.md section 3.2.2): the offline install set of a
// Python engine (E11: the music runtime of ACE-Step). Ark sync fills kit/engines/<engine>/<runtime version>/ on an
// online node from what the item pins:
//
//   - files: the pinned standalone Python (one tarball, name python/<file>, sha256 pinned);
//   - lock + sha256: a hashed pip requirements file of this repository at the catalog tag
//     (engines/acestep/requirements-<backend>.lock); every wheel it names is downloaded from one of the indexes
//     and must have the sha256 the lock gives;
//   - builds: the packages that have no wheel on any index (a git repository at a pinned commit, optionally a
//     subdirectory of it) and the shim of this repository (repo_path), built into wheels by sync, offline, with
//     the pinned Python and the build backends of the lock.
//
// The item id is kit-engine-<engine>-<backend>.

// WheelhouseIDPrefix starts the id of every pip_wheelhouse item.
const WheelhouseIDPrefix = "kit-engine-"

// WheelhouseEngines are the engines a wheelhouse may be for.
var WheelhouseEngines = []string{EngineAcestep}

// WheelBuild is one wheel that ark sync builds for a pip_wheelhouse item: either a git repository at a pinned
// commit (URL, Commit, optional Subdirectory) or a directory of this repository at the catalog tag (RepoPath).
type WheelBuild struct {
	URL          string `yaml:"url"`
	Commit       string `yaml:"commit"`
	Subdirectory string `yaml:"subdirectory"`
	RepoPath     string `yaml:"repo_path"`
}

var (
	wheelhouseVersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+]{0,47}$`)
	commitRe            = regexp.MustCompile(`^[0-9a-f]{40}$`)
	relPathRe           = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*(/[A-Za-z0-9_][A-Za-z0-9._+-]*)*$`)
)

// WheelhouseItem splits a pip_wheelhouse id into its engine and backend ("kit-engine-acestep-cuda" is acestep,
// cuda). ok is false for an id that is not kit-engine-<engine>-<backend> with a known engine and backend.
func WheelhouseItem(id string) (engine, backend string, ok bool) {
	rest, found := strings.CutPrefix(id, WheelhouseIDPrefix)
	if !found {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 {
		return "", "", false
	}
	engine, backend = rest[:i], rest[i+1:]
	if !oneOf(engine, WheelhouseEngines) || !oneOf(backend, backends) {
		return "", "", false
	}
	return engine, backend, true
}

// ValidRepoPath reports whether p is a clean relative path inside the repository (no "..", no leading slash, plain
// elements): what a lock path, a repo_path and a git subdirectory must be.
func ValidRepoPath(p string) bool {
	return relPathRe.MatchString(p) && path.Clean(p) == p && !strings.Contains(p, "..")
}

// ValidCommit reports a full 40-hex lowercase git commit.
func ValidCommit(c string) bool { return commitRe.MatchString(c) }

// ValidWheelhouseVersion reports whether v is a usable runtime version for a pip_wheelhouse item ("1.5.0").
func ValidWheelhouseVersion(v string) bool { return wheelhouseVersionRe.MatchString(v) }

// WheelhouseVersion is the version of a pip_wheelhouse item: "<source.version>-<8 hex>", where the hex is the start
// of the sha256 of everything the item pins (the lock's sha256, the Python files, the indexes and the builds, in
// catalog order) and of the backend. The same string names the kit folder (kit/engines/<engine>/<version>/), the
// sync version and the runtime folder /opt/localai/<engine>/<version>/, so a change of any pin is a new runtime
// that ensure installs. It needs no network. "" when the source has no usable version.
func WheelhouseVersion(id string, src ContentSource) string {
	if !ValidWheelhouseVersion(src.Version) {
		return ""
	}
	_, backend, _ := WheelhouseItem(id)
	var b strings.Builder
	b.WriteString("pip_wheelhouse/1\n")
	b.WriteString("backend " + backend + "\n")
	b.WriteString("lock " + src.Lock + " " + strings.ToLower(src.SHA256) + "\n")
	for _, f := range src.Files {
		b.WriteString("file " + f.ItemPath() + " " + strings.ToLower(f.SHA256) + " " + f.URL + "\n")
	}
	for _, ix := range src.Indexes {
		b.WriteString("index " + ix + "\n")
	}
	for _, w := range src.Builds {
		b.WriteString("build " + w.URL + " " + w.Commit + " " + w.Subdirectory + " " + w.RepoPath + "\n")
	}
	h := sha256.Sum256([]byte(b.String()))
	return src.Version + "-" + hex.EncodeToString(h[:4])
}

// lintWheelhouse checks a pip_wheelhouse item (LAI-CAT-010, 011, 015, 016).
func (s *lintState) lintWheelhouse(p string, it *ContentItem) {
	const f = FileArk
	sp := joinPath(p, "source")
	src := &it.Source
	if _, _, ok := WheelhouseItem(it.ID); !ok {
		s.add(f, CodeArkItem, joinPath(p, "id"), "a pip_wheelhouse item's id must be kit-engine-<engine>-<backend> (engine %s, backend %s), got %q",
			quoted(WheelhouseEngines), quoted(backends), truncate(it.ID, 60))
	}
	switch {
	case src.Version == "":
		s.need(sp, "version", "")
	case !ValidWheelhouseVersion(src.Version):
		s.add(f, CodeSchema, joinPath(sp, "version"), "version must be a plain version such as 1.5.0, got %q", truncate(src.Version, 40))
	}
	switch {
	case src.Lock == "":
		s.need(sp, "lock", "")
	case !ValidRepoPath(src.Lock) || !strings.HasSuffix(src.Lock, ".lock"):
		s.add(f, CodeSchema, joinPath(sp, "lock"), "lock must be a path in this repository ending in .lock, got %q", truncate(src.Lock, 60))
	}
	s.pinnedSHA(f, joinPath(sp, "sha256"), src.SHA256)

	if len(src.Files) != 1 {
		s.add(f, CodeArkItem, joinPath(sp, "files"), "a pip_wheelhouse item pins exactly one Python tarball in files, got %d", len(src.Files))
	}
	for i, file := range src.Files {
		fp := indexPath(joinPath(sp, "files"), i)
		if u, err := url.Parse(file.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			s.add(f, CodeSchema, joinPath(fp, "url"), "url must be an https URL, got %q", truncate(file.URL, 60))
		}
		s.pinnedSHA(f, joinPath(fp, "sha256"), file.SHA256)
		if n := file.ItemPath(); !strings.HasPrefix(n, "python/") || !ValidRepoPath(n) || strings.Count(n, "/") != 1 || !strings.HasSuffix(n, ".tar.gz") {
			s.add(f, CodeSchema, joinPath(fp, "name"), "the Python tarball's name must be python/<file>.tar.gz, got %q", truncate(n, 60))
		}
	}

	if len(src.Indexes) == 0 {
		s.add(f, CodeSchema, joinPath(sp, "indexes"), `missing required field "indexes"`)
	}
	for i, ix := range src.Indexes {
		if u, err := url.Parse(ix); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			s.add(f, CodeSchema, indexPath(joinPath(sp, "indexes"), i), "an index must be a plain https URL (a PEP 503 simple index), got %q", truncate(ix, 60))
		}
	}

	for i, w := range src.Builds {
		bp := indexPath(joinPath(sp, "builds"), i)
		switch {
		case w.RepoPath != "" && (w.URL != "" || w.Commit != "" || w.Subdirectory != ""):
			s.add(f, CodeArkItem, bp, "a build is either url + commit (+ subdirectory) or repo_path, not both")
		case w.RepoPath != "":
			if !ValidRepoPath(w.RepoPath) {
				s.add(f, CodeSchema, joinPath(bp, "repo_path"), "repo_path must be a clean path in this repository, got %q", truncate(w.RepoPath, 60))
			}
		default:
			if u, err := url.Parse(w.URL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				s.add(f, CodeSchema, joinPath(bp, "url"), "url must be a plain https git URL, got %q", truncate(w.URL, 60))
			}
			if !ValidCommit(w.Commit) {
				s.add(f, CodePinning, joinPath(bp, "commit"), "commit must be 40 lowercase hex characters, got %q", truncate(w.Commit, 50))
			}
			if w.Subdirectory != "" && !ValidRepoPath(w.Subdirectory) {
				s.add(f, CodeSchema, joinPath(bp, "subdirectory"), "subdirectory must be a clean relative path, got %q", truncate(w.Subdirectory, 60))
			}
		}
	}
}
