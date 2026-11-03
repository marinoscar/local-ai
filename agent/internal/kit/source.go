package kit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// This file is the lookup half of the offline source of a rebuild (rebuild-kit.md sections 4.3 and
// 4.3.2, llama-router.md section 4.10.1): where on the content roots the source tarball and the git
// mirror of a commit live, which sha256 the kit recorded for a tarball, and where a CUDA
// redistributable set is. It only finds, reads and hashes: nothing is extracted here (the build
// manager extracts into /opt/localai/llama.cpp/src/<commit>/, the only source location).

// Doctor codes of the source lookup. They alias internal/doctor/codes.go.
const (
	// CodeSourceDamaged: a source tarball of the kit does not match its recorded sha256, or has none
	// recorded and so cannot be trusted (LAI-KIT-001).
	CodeSourceDamaged = doctor.KIT001
	// CodeSourceMissing: the kit does not have the commit (LAI-KIT-002).
	CodeSourceMissing = doctor.KIT002
)

// SourceStemLlama is the name stem of llama.cpp in the kit: the tarball is
// kit/src/llama.cpp-<commit>.tar.gz and the mirror kit/git/llama.cpp.git.
const SourceStemLlama = "llama.cpp"

// Errors of the lookup. Test with errors.Is.
var (
	// ErrSourceNotFound: no root has a tarball for the commit.
	ErrSourceNotFound = errors.New("kit: no source tarball for this commit")
	// ErrSourceDamaged: a tarball is there but cannot be trusted: its sha256 differs from the one the
	// kit recorded, no manifest records one, or it cannot be read (LAI-KIT-001).
	ErrSourceDamaged = errors.New("kit: a source tarball is damaged or cannot be verified")
)

var (
	hexCommitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// SourceFS is the part of the filesystem the lookup reads. The build manager passes an adapter over
// its own FS; OSSourceFS is the real one.
type SourceFS interface {
	Stat(name string) (os.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]os.DirEntry, error)
	// OpenRead opens a file for reading.
	OpenRead(name string) (io.ReadCloser, error)
}

// OSSourceFS is the real filesystem.
type OSSourceFS struct{}

// Stat implements SourceFS.
func (OSSourceFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// ReadFile implements SourceFS.
func (OSSourceFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// ReadDir implements SourceFS.
func (OSSourceFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

// OpenRead implements SourceFS.
func (OSSourceFS) OpenRead(name string) (io.ReadCloser, error) { return os.Open(name) }

// SourceTarballName is the file name of the source tarball of a commit: "llama.cpp-<commit>.tar.gz".
func SourceTarballName(stem, commit string) string { return stem + "-" + commit + ".tar.gz" }

// SourceTarballDir is <root>/kit/src, where the tarballs of the pinned commits are written.
func SourceTarballDir(root string) string { return filepath.Join(root, DirName, PartSrc) }

// MirrorDir is the git mirror of a source on a root: <root>/kit/git/<stem>.git.
func MirrorDir(root, stem string) string {
	return filepath.Join(root, DirName, PartGit, stem+".git")
}

// Tarball is a source tarball that matched its recorded sha256.
type Tarball struct {
	// Root is the content root it was found on; Path its absolute path.
	Root, Path string
	// Name is the file name and Commit the full 40-hex commit in it.
	Name, Commit string
	// SHA256 is the verified digest and Size the size in bytes.
	SHA256 string
	Size   int64
}

// SourceFinder looks for kit sources on a list of content roots, in order (the internal root first,
// then the mounted ark drives).
type SourceFinder struct {
	FS    SourceFS
	Roots []string
}

func (f SourceFinder) fs() SourceFS {
	if f.FS == nil {
		return OSSourceFS{}
	}
	return f.FS
}

// Mirrors returns the git mirrors of stem that exist as directories, in root order.
func (f SourceFinder) Mirrors(stem string) []string {
	var out []string
	for _, root := range f.Roots {
		dir := MirrorDir(root, stem)
		if fi, err := f.fs().Stat(dir); err == nil && fi.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// FindTarball returns the first tarball of ref (a full commit, or a prefix of 7 to 40 hex characters
// that names exactly one tarball on the root) whose sha256 matches the one the kit recorded. A
// tarball that does not match, has no recorded digest or cannot be read is skipped and the next root
// is tried; when no root has a good one the error is ErrSourceDamaged (wrapping every problem found)
// if at least one tarball was present, else ErrSourceNotFound.
func (f SourceFinder) FindTarball(stem, ref string) (Tarball, error) {
	var problems []error
	for _, root := range f.Roots {
		for _, name := range f.candidates(root, stem, ref) {
			p := filepath.Join(SourceTarballDir(root), name)
			want, ok := f.RecordedSHA256(root, stem, name)
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
				commit := strings.TrimSuffix(strings.TrimPrefix(name, stem+"-"), ".tar.gz")
				return Tarball{Root: root, Path: p, Name: name, Commit: commit, SHA256: got, Size: size}, nil
			}
		}
	}
	if len(problems) > 0 {
		return Tarball{}, errors.Join(problems...)
	}
	return Tarball{}, ErrSourceNotFound
}

// candidates lists the tarball file names of ref on one root: the one name for a full commit, the
// names with that prefix for a short one when they are one commit.
func (f SourceFinder) candidates(root, stem, ref string) []string {
	dir := SourceTarballDir(root)
	if hexCommitRe.MatchString(ref) {
		name := SourceTarballName(stem, ref)
		if fi, err := f.fs().Stat(filepath.Join(dir, name)); err == nil && fi.Mode().IsRegular() {
			return []string{name}
		}
		return nil
	}
	if !hexPrefixRe.MatchString(ref) {
		return nil
	}
	entries, err := f.fs().ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, stem+"-") || !strings.HasSuffix(n, ".tar.gz") || !e.Type().IsRegular() {
			continue
		}
		commit := strings.TrimSuffix(strings.TrimPrefix(n, stem+"-"), ".tar.gz")
		if hexCommitRe.MatchString(commit) && strings.HasPrefix(commit, ref) {
			names = append(names, n)
		}
	}
	if len(names) != 1 {
		return nil // none, or a short commit that names several tarballs
	}
	return names
}

func (f SourceFinder) hash(p string) (string, int64, error) {
	r, err := f.fs().OpenRead(p)
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// manifestView is the slice of an item MANIFEST.json the lookup needs, read leniently: the strict
// parser is the sync engine's, and a manifest that fails it is exactly the one this must not trust.
type manifestView struct {
	Files  []recordedFile `json:"files"`
	Source struct {
		Tarballs []recordedFile `json:"tarballs"`
	} `json:"source"`
}

type recordedFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func (r recordedFile) file() string {
	if r.Path != "" {
		return r.Path
	}
	return r.Name
}

// RecordedSHA256 returns the sha256 the kit recorded for the tarball file on a root. It reads the
// mirror item's manifest (kit/git/<stem>.git/MANIFEST.json, source.tarballs: where the git_mirror
// resolver records it, rebuild-kit.md section 3.2.1) and kit/src/MANIFEST.json (files). It reports
// false when no manifest names the file, and when two manifests name it with different digests.
func (f SourceFinder) RecordedSHA256(root, stem, file string) (string, bool) {
	var found string
	read := func(manifestPath string, pick func(manifestView) []recordedFile) bool {
		data, err := f.fs().ReadFile(manifestPath)
		if err != nil {
			return true
		}
		var v manifestView
		if json.Unmarshal(data, &v) != nil {
			return true
		}
		for _, r := range pick(v) {
			if r.file() != file {
				continue
			}
			sum := strings.ToLower(r.SHA256)
			if !manifest.IsSHA256(sum) {
				return false
			}
			if found != "" && found != sum {
				return false
			}
			found = sum
		}
		return true
	}
	ok := read(filepath.Join(SourceTarballDir(root), manifest.FileName), func(v manifestView) []recordedFile { return v.Files }) &&
		read(filepath.Join(MirrorDir(root, stem), manifest.FileName), func(v manifestView) []recordedFile { return v.Source.Tarballs })
	return found, ok && found != ""
}

// CudaRedist is one CUDA redistributable set of the kit (rebuild-kit.md section 4.1.6 option B):
// kit/cuda/<ver>-<arch>/ with the tarballs its MANIFEST lists.
type CudaRedist struct {
	// Root is the content root, Dir the set's directory.
	Root, Dir string
	// Version is the CUDA version in the directory name ("12.4") and Arch the CPU architecture.
	Version, Arch string
	// Files are the archives the manifest lists, sorted by name, with their recorded sha256.
	Files []RedistFile
}

// RedistFile is one archive of a CudaRedist.
type RedistFile struct {
	// Name is the file name inside Dir; Path its absolute path.
	Name, Path string
	SHA256     string
}

var redistDirRe = regexp.MustCompile(`^([0-9]+\.[0-9]+(?:\.[0-9]+)?)-(amd64|arm64)$`)

// CudaRedists lists the redistributable sets of arch on the roots, newest version first within a
// root, roots in order. A directory whose manifest cannot be read, or lists no archive, is left out:
// nothing is extracted from a set that cannot be verified.
func (f SourceFinder) CudaRedists(arch string) []CudaRedist {
	var out []CudaRedist
	for _, root := range f.Roots {
		base := filepath.Join(root, DirName, PartCuda)
		entries, err := f.fs().ReadDir(base)
		if err != nil {
			continue
		}
		var here []CudaRedist
		for _, e := range entries {
			m := redistDirRe.FindStringSubmatch(e.Name())
			if m == nil || m[2] != arch || !e.IsDir() {
				continue
			}
			dir := filepath.Join(base, e.Name())
			data, err := f.fs().ReadFile(filepath.Join(dir, manifest.FileName))
			if err != nil {
				continue
			}
			var v manifestView
			if json.Unmarshal(data, &v) != nil {
				continue
			}
			set := CudaRedist{Root: root, Dir: dir, Version: m[1], Arch: m[2]}
			for _, r := range v.Files {
				name := r.file()
				sum := strings.ToLower(r.SHA256)
				if name == "" || path.Base(name) != name || !manifest.IsSHA256(sum) || !isArchive(name) {
					continue
				}
				set.Files = append(set.Files, RedistFile{Name: name, Path: filepath.Join(dir, name), SHA256: sum})
			}
			if len(set.Files) == 0 {
				continue
			}
			sort.Slice(set.Files, func(i, j int) bool { return set.Files[i].Name < set.Files[j].Name })
			here = append(here, set)
		}
		sort.SliceStable(here, func(i, j int) bool { return versionLess(here[j].Version, here[i].Version) })
		out = append(out, here...)
	}
	return out
}

// HashFile returns the sha256 of a file through the finder's filesystem.
func (f SourceFinder) HashFile(p string) (string, error) {
	sum, _, err := f.hash(p)
	return sum, err
}

func isArchive(name string) bool {
	for _, s := range []string{".tar.gz", ".tgz", ".tar.xz", ".tar"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// versionLess compares dotted numeric versions.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscanf(pa[i], "%d", &x)
		}
		if i < len(pb) {
			fmt.Sscanf(pb[i], "%d", &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}
