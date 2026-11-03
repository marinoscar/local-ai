package kit

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
	"github.com/marinoscar/local-ai/agent/internal/update"
)

// The lookup half of `aictl rebuild-agent` (rebuild-kit.md section 4.3.4): the agent source tarball
// kit/src/local-ai-<tag>-src.tar.gz and the Go toolchain kit/toolchains/go<ver>.linux-<arch>.tar.gz,
// each found on the content roots and checked against the sha256 the kit recorded for it. Nothing is
// extracted here; the build manager does that (internal/router/build, AgentBuilder).

const (
	// AgentSourceStem is the name stem of the agent source in the kit.
	AgentSourceStem = "local-ai"
	// ToolchainsDir is kit/toolchains.
	ToolchainsDir = PartToolchains
)

var agentSrcRe = regexp.MustCompile(`^local-ai-(node-v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)-src\.tar\.gz$`)

// AgentSource is an agent source tarball that matched its recorded sha256.
type AgentSource struct {
	// Root is the content root it was found on and Path its absolute path.
	Root, Path, Name string
	// Tag is the full release tag ("node-v0.4.0") and Version the semver in it ("0.4.0"): the string
	// `aiagent version` reports.
	Tag, Version string
	SHA256       string
	Size         int64
}

// GoToolchain is a Go toolchain tarball that matched its recorded sha256.
type GoToolchain struct {
	Root, Path, Name string
	// Version is the Go version in the file name ("1.24.7").
	Version string
	SHA256  string
	Size    int64
}

// RecordedIn returns the sha256 the MANIFEST.json of dir records for the file (its `files` list,
// `path` or `name`). It reports false when there is no manifest, it does not name the file, the digest
// is malformed, or two entries name the file with different digests.
func (f SourceFinder) RecordedIn(dir, file string) (string, bool) {
	data, err := f.fs().ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		return "", false
	}
	var v manifestView
	if json.Unmarshal(data, &v) != nil {
		return "", false
	}
	var found string
	for _, r := range v.Files {
		if r.file() != file {
			continue
		}
		sum := strings.ToLower(r.SHA256)
		if !manifest.IsSHA256(sum) || (found != "" && found != sum) {
			return "", false
		}
		found = sum
	}
	return found, found != ""
}

// FindAgentSource returns the agent source tarball of tag ("node-v0.4.0"), or, when tag is empty, the
// newest one in the kit by semver. A tarball with no recorded sha256, or one that does not match it,
// is skipped and the next candidate tried; when none is good the error is ErrSourceDamaged (wrapping
// every problem) if at least one was present, else ErrSourceNotFound.
func (f SourceFinder) FindAgentSource(tag string) (AgentSource, error) {
	type cand struct {
		root, name, tag string
		v               update.Version
	}
	var cands []cand
	for _, root := range f.Roots {
		entries, err := f.fs().ReadDir(SourceTarballDir(root))
		if err != nil {
			continue
		}
		for _, e := range entries {
			m := agentSrcRe.FindStringSubmatch(e.Name())
			if m == nil || !e.Type().IsRegular() || (tag != "" && m[1] != tag) {
				continue
			}
			v, err := update.ParseTag(m[1])
			if err != nil {
				continue
			}
			cands = append(cands, cand{root: root, name: e.Name(), tag: m[1], v: v})
		}
	}
	// Newest first; the same tag keeps the order of the roots.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].v.Compare(cands[j].v) > 0 })
	var problems []error
	for _, c := range cands {
		p := filepath.Join(SourceTarballDir(c.root), c.name)
		want, ok := f.RecordedSHA256(c.root, AgentSourceStem, c.name)
		if !ok {
			problems = append(problems, fmt.Errorf("%w: %s has no sha256 recorded in the kit manifests", ErrSourceDamaged, p))
			continue
		}
		got, size, err := f.hash(p)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("%w: %s: %v", ErrSourceDamaged, p, err))
		case got != want:
			problems = append(problems, fmt.Errorf("%w: %s does not match the sha256 the kit recorded", ErrSourceDamaged, p))
		default:
			return AgentSource{Root: c.root, Path: p, Name: c.name, Tag: c.tag, Version: c.v.String(), SHA256: got, Size: size}, nil
		}
	}
	if len(problems) > 0 {
		return AgentSource{}, errors.Join(problems...)
	}
	return AgentSource{}, ErrSourceNotFound
}

// FindGoToolchain returns the newest Go toolchain of the kit for the CPU architecture (amd64 or
// arm64) whose sha256 matches the one kit/toolchains/MANIFEST.json records, with the same skipping
// and errors as FindAgentSource.
func (f SourceFinder) FindGoToolchain(arch string) (GoToolchain, error) {
	re := regexp.MustCompile(`^go([0-9]+\.[0-9]+(?:\.[0-9]+)?)\.linux-` + regexp.QuoteMeta(arch) + `\.tar\.gz$`)
	type cand struct{ root, name, version string }
	var cands []cand
	for _, root := range f.Roots {
		dir := filepath.Join(root, DirName, ToolchainsDir)
		entries, err := f.fs().ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if m := re.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
				cands = append(cands, cand{root: root, name: e.Name(), version: m[1]})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return versionLess(cands[j].version, cands[i].version) })
	var problems []error
	for _, c := range cands {
		dir := filepath.Join(c.root, DirName, ToolchainsDir)
		p := filepath.Join(dir, c.name)
		want, ok := f.RecordedIn(dir, c.name)
		if !ok {
			problems = append(problems, fmt.Errorf("%w: %s has no sha256 recorded in %s", ErrSourceDamaged, p, filepath.Join(dir, manifest.FileName)))
			continue
		}
		got, size, err := f.hash(p)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("%w: %s: %v", ErrSourceDamaged, p, err))
		case got != want:
			problems = append(problems, fmt.Errorf("%w: %s does not match the sha256 the kit recorded", ErrSourceDamaged, p))
		default:
			return GoToolchain{Root: c.root, Path: p, Name: c.name, Version: c.version, SHA256: got, Size: size}, nil
		}
	}
	if len(problems) > 0 {
		return GoToolchain{}, errors.Join(problems...)
	}
	return GoToolchain{}, ErrSourceNotFound
}

// GoVersionAtLeast reports whether the dotted Go version have ("1.24.7") is at least need ("1.24",
// "1.23.1"); a missing part counts as 0.
func GoVersionAtLeast(have, need string) bool { return !versionLess(have, need) }
