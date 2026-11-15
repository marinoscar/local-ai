package resolve

import (
	"fmt"
	"regexp"
	"strings"
)

// PipLock is a parsed hashed pip requirements file (engines/acestep/requirements-<backend>.lock, written by
// scripts/release/lock-acestep.sh). Only the strict subset that tool writes is accepted:
//
//	# any comment
//	# repo-tree-sha256: <repo path> <sha256>    the tree hash of a directory of this repository that sync builds
//	# build-requires: <name> <name> ...         the build backends sync installs to build the wheels it makes
//	# unresolved: <name>==<version> (<why>)     a package the generator could not pin: the lock is unusable
//	<name>==<version> --hash=sha256:<sha256>    one wheel
//
// Anything else (an option line, a range, a URL, two hashes for one package, a name twice) is refused, so what pip
// installs offline with --require-hashes is exactly what sync downloaded and verified.
type PipLock struct {
	Reqs []PipReq
	// RepoTrees maps a repository path to the tree hash the lock records for it.
	RepoTrees map[string]string
	// BuildRequires are the normalized names of the build backends.
	BuildRequires []string
	// Unresolved are the packages the generator could not pin.
	Unresolved []string
}

// PipReq is one pinned wheel.
type PipReq struct {
	Name, Version, SHA256 string
	// Line is the line as written, used verbatim in the lock sync writes.
	Line string
}

var (
	pipNameRe    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	pipVersionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.!+_-]{0,63}$`)
	pipNormRe    = regexp.MustCompile(`[-_.]+`)
)

// NormalizeDist is the PEP 503 normalized form of a distribution name ("PyYAML" is "pyyaml", "typing_extensions"
// is "typing-extensions").
func NormalizeDist(name string) string { return strings.ToLower(pipNormRe.ReplaceAllString(name, "-")) }

// ParsePipLock parses a lock. The error names the line.
func ParsePipLock(data []byte) (PipLock, error) {
	l := PipLock{RepoTrees: map[string]string{}}
	seen := map[string]bool{}
	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			switch {
			case strings.HasPrefix(body, "repo-tree-sha256:"):
				f := strings.Fields(strings.TrimPrefix(body, "repo-tree-sha256:"))
				if len(f) != 2 || !sha256HexRe.MatchString(f[1]) || f[0] == "" {
					return PipLock{}, fmt.Errorf("line %d: a repo-tree-sha256 line is <path> <sha256>", n)
				}
				l.RepoTrees[f[0]] = f[1]
			case strings.HasPrefix(body, "build-requires:"):
				for _, name := range strings.Fields(strings.TrimPrefix(body, "build-requires:")) {
					if !pipNameRe.MatchString(name) {
						return PipLock{}, fmt.Errorf("line %d: %q is not a package name", n, Clip([]byte(name), 40))
					}
					l.BuildRequires = append(l.BuildRequires, NormalizeDist(name))
				}
			case strings.HasPrefix(body, "unresolved:"):
				l.Unresolved = append(l.Unresolved, Clip([]byte(strings.TrimSpace(strings.TrimPrefix(body, "unresolved:"))), 80))
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return PipLock{}, fmt.Errorf("line %d: expected name==version --hash=sha256:<sha256>", n)
		}
		name, version, ok := strings.Cut(f[0], "==")
		if !ok || !pipNameRe.MatchString(name) || !pipVersionRe.MatchString(version) {
			return PipLock{}, fmt.Errorf("line %d: %q is not name==version", n, Clip([]byte(f[0]), 60))
		}
		sum, ok := strings.CutPrefix(f[1], "--hash=sha256:")
		if !ok || !sha256HexRe.MatchString(sum) {
			return PipLock{}, fmt.Errorf("line %d: the hash must be --hash=sha256:<64 lowercase hex>", n)
		}
		key := NormalizeDist(name)
		if seen[key] {
			return PipLock{}, fmt.Errorf("line %d: %s is pinned twice", n, name)
		}
		seen[key] = true
		l.Reqs = append(l.Reqs, PipReq{Name: name, Version: version, SHA256: sum, Line: f[0] + " " + f[1]})
	}
	for _, b := range l.BuildRequires {
		if !seen[b] {
			return PipLock{}, fmt.Errorf("the build backend %s is not pinned in the lock", b)
		}
	}
	if len(l.Reqs) == 0 && len(l.Unresolved) == 0 {
		return PipLock{}, fmt.Errorf("the lock pins nothing")
	}
	return l, nil
}

// WheelName splits a wheel file name into its normalized distribution name and its version
// ("torch-2.10.0+cu128-cp312-cp312-manylinux_2_28_x86_64.whl" is torch, 2.10.0+cu128). ok is false for a name
// that is not <dist>-<version>(-<build>)?-<py>-<abi>-<plat>.whl.
func WheelName(file string) (dist, version string, ok bool) {
	base, found := strings.CutSuffix(file, ".whl")
	if !found {
		return "", "", false
	}
	p := strings.Split(base, "-")
	if len(p) != 5 && len(p) != 6 {
		return "", "", false
	}
	if !pipNameRe.MatchString(p[0]) || !pipVersionRe.MatchString(p[1]) {
		return "", "", false
	}
	return NormalizeDist(p[0]), p[1], true
}
