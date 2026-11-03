package kit

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/sync/manifest"
)

// Offline apt (rebuild-kit.md sections 4.1.4 and 4.1.5, aictl.md section 4.23c).
//
// `aictl kit install-debs` installs packages from the flat local apt repositories of the kit,
// kit/debs/<codename>-<arch>/ and kit/nvidia/<codename>-<arch>-<branch>/, with no network. Those
// repositories are used as `[trusted=yes]` sources, which makes apt check nothing, so every safety
// property comes from this file:
//
//  1. Apt.Run never writes a source and never starts apt or dpkg before VerifyRepo has accepted the
//     repository: every file named by SHA256SUMS is re-hashed, the .deb files are matched with
//     PACKAGES.lock, the Packages index agrees with the lock, and no file that apt could prefer
//     over the index (Packages.xz, Release) is present.
//  2. Only the repositories that hold a requested package are listed in the temporary source.
//  3. The plan apt makes (apt-get -s) is read before anything is installed: every package it would
//     install must be a package of the verified lock at the locked version, and it must remove
//     nothing. A plan outside that is refused.
//  4. The temporary source is removed on every path, success or failure.
//
// Everything outside the process (apt-get, dpkg-query) runs through execx with a fixed argv; a
// package name is validated against PackageNameRe and is never interpreted by a shell.

// Names of the files of a kit apt repository (apt_closure resolver, ark-sync.md section 4.9.7).
const (
	// LockName is PACKAGES.lock: sorted "name=version sha256" lines.
	LockName = "PACKAGES.lock"
	// PackagesName and PackagesGzName are the apt index.
	PackagesName   = "Packages"
	PackagesGzName = "Packages.gz"
	// PoolDir holds the .deb files.
	PoolDir = "pool"
)

// AptSourceList is the temporary apt source file (rebuild-kit.md section 4.1.4). It is written only
// after the repository verified and removed when the command ends.
const AptSourceList = "/etc/apt/sources.list.d/localai-ark.list"

// PackageNameRe is what a package name must match: a Debian package name as the kit lists them, and
// nothing a shell or apt's name syntax (name=version, name/release, name:arch) could interpret.
var PackageNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

// Limits of the verifier and of the apt calls.
const (
	maxPackageNameLen = 100
	maxPackages       = 200
	maxListBytes      = 64 << 20  // SHA256SUMS, PACKAGES.lock, Packages
	maxIndexBytes     = 512 << 20 // Packages after decompression
	maxDebBytes       = 8 << 30   // one file of the pool

	aptUpdateTimeout  = 10 * time.Minute
	aptPlanTimeout    = 5 * time.Minute
	aptInstallTimeout = 60 * time.Minute
	dpkgQueryTimeout  = 30 * time.Second
	aptMaxOut         = 4 << 20
	maxProblemsShown  = 20
)

// Package lists of rebuild-kit.md section 4.1.4. The default selection of `install-debs` is the build
// and tool lists plus the list of the node's llama backend. tailscale and gpsd are optional and are
// installed only when named with --packages.
var (
	// BuildPackages compile llama.cpp and whisper.cpp.
	BuildPackages = []string{"build-essential", "cmake", "ninja-build", "pkg-config", "ccache", "git"}
	// ToolPackages are the everyday tools of a node.
	ToolPackages = []string{"curl", "jq", "ca-certificates", "iw", "network-manager", "pciutils", "usbutils", "minisign", "zstd", "udisks2", "openssh-server", "python3"}
	// VulkanPackages are the Vulkan and Mesa packages of a vulkan node.
	VulkanPackages = []string{"libvulkan-dev", "glslc", "spirv-headers", "vulkan-tools", "mesa-vulkan-drivers"}
	// CudaPackages are the CUDA toolkit of a cuda node (rebuild-kit.md section 4.1.6, option A).
	CudaPackages = []string{"nvidia-cuda-toolkit"}
)

// DefaultPackages is the default package set for a node whose llama backend is backend ("cuda",
// "vulkan", "cpu"; anything else counts as cpu): the build list, the tool list and the backend's own
// list. The result has no duplicates and a stable order.
func DefaultPackages(backend string) []string {
	out := append([]string(nil), BuildPackages...)
	out = append(out, ToolPackages...)
	switch backend {
	case "vulkan":
		out = append(out, VulkanPackages...)
	case "cuda":
		out = append(out, CudaPackages...)
	}
	return out
}

// Failure kinds. Every error of this file is an *Error whose Kind is one of these (errors.Is works).
var (
	// ErrRepoDamaged: a repository of the kit failed its checks (LAI-KIT-001). Nothing was installed.
	ErrRepoDamaged = errors.New("the kit's package files do not match their checksums")
	// ErrNoRepo: the kit has no package repository at all, or none for this machine's part (LAI-KIT-040).
	ErrNoRepo = errors.New("the kit has no package repository")
	// ErrWrongRelease: the kit has repositories, but for other Ubuntu releases (LAI-KIT-042).
	ErrWrongRelease = errors.New("the kit's packages are for another Ubuntu release")
	// ErrBadPackage: a package name is not valid (usage error).
	ErrBadPackage = errors.New("invalid package name")
	// ErrNotInKit: a requested package is in no repository of the kit (LAI-KIT-040).
	ErrNotInKit = errors.New("package not in the kit")
	// ErrUnsafePlan: apt would install something that is not in the verified repository, or remove
	// something (LAI-KIT-047). Nothing was installed.
	ErrUnsafePlan = errors.New("apt's plan goes beyond the verified kit packages")
	// ErrApt: apt-get or dpkg-query failed (LAI-KIT-047).
	ErrApt = errors.New("apt could not do it")
	// ErrCleanup: the temporary apt source could not be removed (LAI-KIT-047).
	ErrCleanup = errors.New("the temporary apt source could not be removed")
	// ErrIO: the disk could not be read or written (LAI-KIT-047).
	ErrIO = errors.New("file system error")
)

// Error is the error type of Apt.Run, VerifyRepo and ChooseRepos.
type Error struct {
	// Kind is one of the Err* values above.
	Kind error
	// Detail is a one-line English description for logs and for -v.
	Detail string
	// Names carries the items the message is about: the files that did not verify, the packages that
	// are missing, the package name that is invalid, the packages apt would add.
	Names []string
	// Have is what the kit offers (the codename-arch names found), for ErrWrongRelease and ErrNoRepo.
	Have []string
	// Want is what this machine needs ("noble-amd64"), for ErrWrongRelease.
	Want string
	// Repo is the repository directory the error is about.
	Repo string
	// Err is the underlying cause, if any.
	Err error
}

// Error implements error.
func (e *Error) Error() string {
	s := e.Kind.Error()
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
func (e *Error) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Err}
}

// Code is the doctor code of the failure.
func (e *Error) Code() string {
	switch {
	case errors.Is(e.Kind, ErrRepoDamaged):
		return doctor.KIT001
	case errors.Is(e.Kind, ErrWrongRelease):
		return doctor.KIT042
	case errors.Is(e.Kind, ErrNoRepo), errors.Is(e.Kind, ErrNotInKit):
		return doctor.KIT040
	case errors.Is(e.Kind, ErrBadPackage):
		return doctor.CLI005
	}
	return doctor.KIT047
}

func aptErr(kind error, detail string, cause error) *Error {
	return &Error{Kind: kind, Detail: detail, Err: cause}
}

// ValidPackageName reports whether s is an acceptable package name.
func ValidPackageName(s string) bool {
	return len(s) <= maxPackageNameLen && PackageNameRe.MatchString(s)
}

// ParsePackageList splits a --packages value ("cmake,ninja-build") into validated names, in the order
// given and without duplicates. An empty element, a bad name and more than 200 names are errors.
func ParsePackageList(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, &Error{Kind: ErrBadPackage, Detail: "the list of packages is empty"}
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(list, ",") {
		name := strings.TrimSpace(raw)
		if !ValidPackageName(name) {
			return nil, &Error{Kind: ErrBadPackage, Detail: "a package name is empty or not valid", Names: []string{clip(name, 40)}}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) > maxPackages {
		return nil, &Error{Kind: ErrBadPackage, Detail: fmt.Sprintf("more than %d packages", maxPackages)}
	}
	return out, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Pkg is a package at a version.
type Pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// LockEntry is one line of PACKAGES.lock.
type LockEntry struct {
	Name, Version, SHA256 string
}

// ParseLock parses PACKAGES.lock: "name=version sha256" per line, no blank line, no duplicate
// name=version, every hash 64 lowercase hex digits, every name valid and every version a plain
// Debian version string.
func ParseLock(data []byte) ([]LockEntry, error) {
	if len(data) == 0 {
		return nil, errors.New("PACKAGES.lock is empty")
	}
	var out []LockEntry
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		nv, sum, ok := strings.Cut(line, " ")
		name, ver, ok2 := strings.Cut(nv, "=")
		switch {
		case !ok || !ok2 || !manifest.IsSHA256(sum):
			return nil, fmt.Errorf("PACKAGES.lock line %d is not \"name=version sha256\"", i+1)
		case !ValidPackageName(name) || !versionRe.MatchString(ver):
			return nil, fmt.Errorf("PACKAGES.lock line %d names an invalid package or version", i+1)
		case seen[nv]:
			return nil, fmt.Errorf("PACKAGES.lock lists %s twice", nv)
		}
		seen[nv] = true
		out = append(out, LockEntry{Name: name, Version: ver, SHA256: sum})
	}
	return out, nil
}

// versionRe is a Debian version as it appears in a lock line: epoch, upstream, revision.
var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+~:_-]{0,127}$`)

// Repo is one apt repository of the kit.
type Repo struct {
	// Part is PartDebs or PartNvidia.
	Part string
	// Name is the directory name: "<codename>-<arch>" or "<codename>-<arch>-<branch>".
	Name string
	// Dir is the absolute directory.
	Dir string
}

// VerifiedRepo is a repository that passed VerifyRepo, with the entries of the PACKAGES.lock that was
// hashed (not read again).
type VerifiedRepo struct {
	Repo
	Entries []LockEntry
}

// ---- verification -------------------------------------------------------------------------------

// VerifyRepo checks one kit apt repository before apt is let near it. It needs no network and no
// root. A repository is accepted only when all of this holds:
//
//   - SHA256SUMS and PACKAGES.lock exist and parse; SHA256SUMS lists Packages, Packages.gz and
//     PACKAGES.lock;
//   - every file SHA256SUMS lists is a regular file (no link) with the listed sha256;
//   - the pool/ folder holds nothing SHA256SUMS does not list, and the folder has no file or folder
//     outside SHA256SUMS, MANIFEST.json and pool/ (a stray Packages.xz or Release file would be
//     preferred by apt over the checked index);
//   - the sha256 of the pool .deb files and the sha256 column of PACKAGES.lock are the same set;
//   - the Packages index lists exactly the packages of PACKAGES.lock, each with the locked
//     version, the locked sha256 and a Filename that is a listed pool file with that hash;
//   - Packages.gz holds the same bytes as Packages;
//   - when MANIFEST.json exists, it is valid and its tree hash is the one of SHA256SUMS.
//
// Any failure is *Error with Kind ErrRepoDamaged naming (up to 20 of) the files; nothing is
// returned as verified. progress, when set, is called with the file being hashed.
func VerifyRepo(ctx context.Context, fsys FS, r Repo, progress func(file string)) (*VerifiedRepo, error) {
	bad := &problems{}
	fail := func() error {
		return &Error{Kind: ErrRepoDamaged, Detail: "repository " + r.Name + " failed its checks", Names: bad.list(), Repo: r.Dir}
	}

	sumsData, err := readCapped(fsys, filepath.Join(r.Dir, manifest.SumsName))
	if err != nil {
		bad.add(manifest.SumsName + " (" + reasonOf(err) + ")")
		return nil, fail()
	}
	sums, err := manifest.ParseSums(sumsData)
	if err != nil {
		bad.add(manifest.SumsName + " (unreadable)")
		return nil, fail()
	}
	listed := make(map[string]string, len(sums))
	for _, s := range sums {
		listed[s.Path] = s.SHA256
	}
	small := map[string][]byte{}
	for _, name := range []string{LockName, PackagesName, PackagesGzName} {
		want, ok := listed[name]
		if !ok {
			bad.add(name + " (not listed in " + manifest.SumsName + ")")
			continue
		}
		data, err := readCapped(fsys, filepath.Join(r.Dir, name))
		switch {
		case err != nil:
			bad.add(name + " (" + reasonOf(err) + ")")
		case sha256Hex(data) != want:
			bad.add(name + " (changed)")
		default:
			small[name] = data
		}
	}

	// The top folder: only listed files, MANIFEST.json, SHA256SUMS and pool/.
	if entries, err := fsys.ReadDir(r.Dir); err != nil {
		bad.add("the repository folder (" + reasonOf(err) + ")")
	} else {
		for _, e := range entries {
			n := e.Name()
			if n == manifest.SumsName || n == manifest.FileName || n == PoolDir && e.IsDir() {
				continue
			}
			if _, ok := listed[n]; !ok {
				bad.add(n + " (not listed in " + manifest.SumsName + ")")
			}
		}
	}
	// The pool: only listed regular files.
	if entries, err := fsys.ReadDir(filepath.Join(r.Dir, PoolDir)); err != nil {
		bad.add("pool/ (" + reasonOf(err) + ")")
	} else {
		for _, e := range entries {
			rel := PoolDir + "/" + e.Name()
			if _, ok := listed[rel]; !ok {
				bad.add(rel + " (not listed in " + manifest.SumsName + ")")
			}
		}
	}

	// Re-hash every listed file (the small ones were hashed above).
	for _, s := range sums {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, done := small[s.Path]; done {
			continue
		}
		if s.Path == LockName || s.Path == PackagesName || s.Path == PackagesGzName {
			continue // already reported
		}
		if progress != nil {
			progress(s.Path)
		}
		full := filepath.Join(r.Dir, filepath.FromSlash(s.Path))
		fi, err := fsys.Lstat(full)
		switch {
		case err != nil:
			bad.add(s.Path + " (" + reasonOf(err) + ")")
			continue
		case !fi.Mode().IsRegular():
			bad.add(s.Path + " (not a regular file)")
			continue
		}
		got, err := hashFile(ctx, fsys, full, maxDebBytes)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case err != nil:
			bad.add(s.Path + " (" + reasonOf(err) + ")")
		case got != s.SHA256:
			bad.add(s.Path + " (changed)")
		}
	}
	if bad.n > 0 {
		return nil, fail()
	}

	// The small files were hashed and match; now check how they fit together.
	entries, err := ParseLock(small[LockName])
	if err != nil {
		bad.add(LockName + " (" + err.Error() + ")")
		return nil, fail()
	}
	checkLockAgainstPool(bad, entries, sums)
	checkIndex(bad, small[PackagesName], small[PackagesGzName], entries, listed)
	checkManifest(bad, fsys, r.Dir, sums)
	if bad.n > 0 {
		return nil, fail()
	}
	return &VerifiedRepo{Repo: r, Entries: entries}, nil
}

// problems collects the findings of one verification, keeping the first few for the message.
type problems struct {
	items []string
	n     int
}

func (p *problems) add(s string) {
	p.n++
	if len(p.items) < maxProblemsShown {
		p.items = append(p.items, s)
	}
}

func (p *problems) list() []string {
	out := append([]string(nil), p.items...)
	if p.n > len(out) {
		out = append(out, fmt.Sprintf("and %d more", p.n-len(out)))
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "missing"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	return "unreadable"
}

// readCapped reads a small file, refusing one over maxListBytes.
func readCapped(fsys FS, name string) ([]byte, error) {
	fi, err := fsys.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxListBytes {
		return nil, errors.New("too large")
	}
	return fsys.ReadFile(name)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// hashFile streams a file through sha256, stopping at limit bytes and when ctx ends.
func hashFile(ctx context.Context, fsys FS, name string, limit int64) (string, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(ctxReader{ctx, f}, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", errors.New("too large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// checkLockAgainstPool compares the sha256 column of the lock with the pool .deb entries of
// SHA256SUMS as multisets.
func checkLockAgainstPool(bad *problems, entries []LockEntry, sums []manifest.Sum) {
	pool := map[string]int{}
	for _, s := range sums {
		if strings.HasPrefix(s.Path, PoolDir+"/") && strings.HasSuffix(s.Path, ".deb") {
			pool[s.SHA256]++
		} else if strings.HasPrefix(s.Path, PoolDir+"/") {
			bad.add(s.Path + " (a pool file that is not a .deb)")
		}
	}
	for _, e := range entries {
		if pool[e.SHA256] == 0 {
			bad.add(e.Name + "=" + e.Version + " (no matching .deb in pool/)")
			continue
		}
		pool[e.SHA256]--
	}
	var extra []string
	for sum, n := range pool {
		if n > 0 {
			extra = append(extra, sum)
		}
	}
	sort.Strings(extra)
	for _, sum := range extra {
		for _, s := range sums {
			if s.SHA256 == sum && strings.HasPrefix(s.Path, PoolDir+"/") && strings.HasSuffix(s.Path, ".deb") {
				bad.add(s.Path + " (a .deb that PACKAGES.lock does not list)")
				break
			}
		}
	}
}

// indexStanza is what the checker needs of one record of a Packages file.
type indexStanza struct{ pkg, version, filename, sha string }

// parseIndex reads the Package, Version, Filename and SHA256 fields of a Packages file.
func parseIndex(data []byte) ([]indexStanza, error) {
	var out []indexStanza
	var cur indexStanza
	have := false
	flush := func() {
		if have {
			out = append(out, cur)
		}
		cur, have = indexStanza{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("a line of Packages is not \"Key: value\"")
		}
		val = strings.TrimSpace(val)
		have = true
		switch key {
		case "Package":
			cur.pkg = val
		case "Version":
			cur.version = val
		case "Filename":
			cur.filename = val
		case "SHA256":
			cur.sha = val
		}
	}
	flush()
	return out, nil
}

// checkIndex checks that Packages lists exactly the lock, with the locked hashes and a file that
// SHA256SUMS holds with the same hash, and that Packages.gz is Packages.
func checkIndex(bad *problems, index, gz []byte, entries []LockEntry, listed map[string]string) {
	stanzas, err := parseIndex(index)
	if err != nil {
		bad.add(PackagesName + " (" + err.Error() + ")")
		return
	}
	lock := make(map[string]string, len(entries))
	for _, e := range entries {
		lock[e.Name+"="+e.Version] = e.SHA256
	}
	seen := map[string]bool{}
	for _, s := range stanzas {
		nv := s.pkg + "=" + s.version
		want, ok := lock[nv]
		switch {
		case !ok:
			bad.add(nv + " (in Packages but not in PACKAGES.lock)")
		case seen[nv]:
			bad.add(nv + " (twice in Packages)")
		case s.sha != want:
			bad.add(nv + " (Packages and PACKAGES.lock disagree on the sha256)")
		case !strings.HasPrefix(s.filename, PoolDir+"/") || path.Clean(s.filename) != s.filename || listed[s.filename] != want:
			bad.add(nv + " (its Filename is not a pool file with the locked sha256)")
		}
		seen[nv] = true
	}
	for nv := range lock {
		if !seen[nv] {
			bad.add(nv + " (in PACKAGES.lock but not in Packages)")
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		bad.add(PackagesGzName + " (not gzip)")
		return
	}
	unz, err := io.ReadAll(io.LimitReader(zr, maxIndexBytes+1))
	if err != nil || int64(len(unz)) > maxIndexBytes || !bytes.Equal(unz, index) {
		bad.add(PackagesGzName + " (does not hold the same index as " + PackagesName + ")")
	}
}

// checkManifest cross-checks MANIFEST.json, when the repository has one, with SHA256SUMS.
func checkManifest(bad *problems, fsys FS, dir string, sums []manifest.Sum) {
	m, err := manifest.Read(fsys, manifest.Path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		bad.add(manifest.FileName + " (invalid)")
		return
	}
	tree, err := manifest.TreeSHA256(sums)
	if err != nil || !m.IsDir() || m.TreeSHA256 != tree {
		bad.add(manifest.FileName + " (its tree hash is not the one of " + manifest.SumsName + ")")
	}
}

// ---- finding the repositories -------------------------------------------------------------------

// MachineRelease reads the Ubuntu codename of this machine from an os-release file (VERSION_CODENAME)
// and maps goarch ("amd64", "arm64") to the architecture word of the kit. Anything else is an *Error
// of Kind ErrWrongRelease.
func MachineRelease(fsys config.FS, osRelease, goarch string) (codename, arch string, err error) {
	switch goarch {
	case "amd64", "arm64":
		arch = goarch
	default:
		return "", "", &Error{Kind: ErrWrongRelease, Detail: fmt.Sprintf("the kit has packages for amd64 and arm64, not %s", goarch)}
	}
	data, err := fsys.ReadFile(osRelease)
	if err != nil {
		return "", "", &Error{Kind: ErrWrongRelease, Detail: "cannot read " + osRelease, Err: err}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION_CODENAME="); ok {
			v = strings.Trim(v, `"'`)
			if manifest.SafeName(v) && !strings.Contains(v, "-") {
				return v, arch, nil
			}
		}
	}
	return "", "", &Error{Kind: ErrWrongRelease, Detail: "the Ubuntu release name (VERSION_CODENAME) is not in " + osRelease}
}

// ListRepos lists the apt repositories of a kit directory (<root>/kit): the directories of debs/ and
// nvidia/ with a plain name, debs first, each part sorted by name. A missing debs/ or nvidia/ is an
// empty list.
func ListRepos(fsys config.FS, kitDir string) ([]Repo, error) {
	var out []Repo
	for _, part := range []string{PartDebs, PartNvidia} {
		entries, err := fsys.ReadDir(filepath.Join(kitDir, part))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if err != nil {
			return nil, aptErr(ErrIO, "cannot list "+filepath.Join(kitDir, part), err)
		}
		for _, e := range entries {
			if !e.IsDir() || !manifest.SafeName(e.Name()) {
				continue
			}
			out = append(out, Repo{Part: part, Name: e.Name(), Dir: filepath.Join(kitDir, part, e.Name())})
		}
	}
	return out, nil
}

// ReposFor picks the repositories of a kit directory that belong to a machine: debs/<codename>-<arch>
// and every nvidia/<codename>-<arch>-<branch>. When the machine has no repository there, the error
// names what the kit does have: ErrWrongRelease when it has packages for other releases, ErrNoRepo
// when it has none at all.
func ReposFor(fsys config.FS, kitDir, codename, arch string) ([]Repo, error) {
	all, err := ListRepos(fsys, kitDir)
	if err != nil {
		return nil, err
	}
	want := codename + "-" + arch
	var mine []Repo
	var have []string
	for _, r := range all {
		have = append(have, r.Part+"/"+r.Name)
		if r.Part == PartDebs && r.Name == want || r.Part == PartNvidia && strings.HasPrefix(r.Name, want+"-") {
			mine = append(mine, r)
		}
	}
	if len(mine) > 0 {
		return mine, nil
	}
	if len(all) == 0 {
		return nil, &Error{Kind: ErrNoRepo, Detail: "kit/debs and kit/nvidia have no repository", Want: want}
	}
	return nil, &Error{Kind: ErrWrongRelease, Detail: "the kit has no repository for " + want, Have: have, Want: want}
}

// ---- installing ---------------------------------------------------------------------------------

// Step names what Apt.Run is doing, for the progress line of the command.
type Step string

// The steps of a run, in order.
const (
	StepVerify  Step = "verify"
	StepUpdate  Step = "update"
	StepPlan    Step = "plan"
	StepInstall Step = "install"
)

// Apt installs packages from the kit's local repositories. The zero value is not usable: set FS,
// Runner, LookPath, Codename and Arch (NewApt fills the real ones).
type Apt struct {
	// FS is the disk; Runner runs apt-get and dpkg-query; LookPath finds them (execx.Lookup).
	FS       FS
	Runner   execx.Runner
	LookPath func(name string) (string, error)
	// Codename and Arch are this machine's (MachineRelease).
	Codename, Arch string
	// SourceList is the temporary apt source of a real run; default AptSourceList.
	SourceList string
	// ScratchDir is the parent of the private apt state of a dry run; default os.TempDir().
	ScratchDir string
	// Progress, when set, is told each step (detail: the repository name, or the file being hashed
	// for StepVerify).
	Progress func(step Step, detail string)
}

// NewApt returns an Apt for the real machine.
func NewApt(codename, arch string) *Apt {
	return &Apt{FS: OSFS{}, Runner: execx.Exec{}, LookPath: execx.Lookup, Codename: codename, Arch: arch}
}

// Request is one install-debs run.
type Request struct {
	// Packages is the explicit allowlist (ParsePackageList); empty means the default set of Backend.
	Packages []string
	// Backend selects the default set when Packages is empty ("cuda", "vulkan", "cpu").
	Backend string
	// DryRun resolves everything and installs nothing; the system is not changed at all.
	DryRun bool
}

// Result is what a run did (or, for a dry run, would do).
type Result struct {
	// KitDir is the kit directory used.
	KitDir string `json:"kit_dir"`
	// Repos are the repositories verified and used (as "debs/<name>" or "nvidia/<name>").
	Repos []string `json:"repos"`
	// Requested is the package set, in order.
	Requested []string `json:"requested"`
	// NotInKit lists default packages the kit does not carry; they are skipped. An explicit package
	// that is not in the kit is an error instead.
	NotInKit []string `json:"not_in_kit"`
	// Skipped lists the requested packages that were already installed at the kit's version.
	Skipped []Pkg `json:"skipped"`
	// Plan is what apt would install, dependencies included (also set after a real run).
	Plan []Pkg `json:"plan"`
	// Installed lists, after a real run, the requested packages now installed.
	Installed []Pkg `json:"installed"`
	// DryRun repeats the request.
	DryRun bool `json:"dry_run"`
}

func (a *Apt) step(s Step, detail string) {
	if a.Progress != nil {
		a.Progress(s, detail)
	}
}

func (a *Apt) sourceList() string {
	if a.SourceList != "" {
		return a.SourceList
	}
	return AptSourceList
}

// peekLock reads a repository's PACKAGES.lock before verification, only to decide which repositories
// the request needs. Nothing it returns is trusted: the choice is verified afterwards, and the plan
// is checked against the verified copy.
func peekLock(fsys FS, r Repo) ([]LockEntry, error) {
	data, err := readCapped(fsys, filepath.Join(r.Dir, LockName))
	if err != nil {
		return nil, &Error{Kind: ErrRepoDamaged, Detail: "repository " + r.Name + " failed its checks", Names: []string{LockName + " (" + reasonOf(err) + ")"}, Repo: r.Dir}
	}
	entries, err := ParseLock(data)
	if err != nil {
		return nil, &Error{Kind: ErrRepoDamaged, Detail: "repository " + r.Name + " failed its checks", Names: []string{LockName + " (" + err.Error() + ")"}, Repo: r.Dir}
	}
	return entries, nil
}

// Run installs (or, for a dry run, plans) the request from the repositories of kitDir. See the
// package comment for the safety properties. The error is an *Error (or a context error).
func (a *Apt) Run(ctx context.Context, kitDir string, req Request) (*Result, error) {
	for _, p := range req.Packages {
		if !ValidPackageName(p) {
			return nil, &Error{Kind: ErrBadPackage, Detail: "a package name is empty or not valid", Names: []string{clip(p, 40)}}
		}
	}
	repos, err := ReposFor(a.FS, kitDir, a.Codename, a.Arch)
	if err != nil {
		return nil, err
	}
	res := &Result{KitDir: kitDir, DryRun: req.DryRun, Repos: []string{}, Requested: []string{}, NotInKit: []string{}, Skipped: []Pkg{}, Plan: []Pkg{}, Installed: []Pkg{}}

	// Which repository holds which package (an unverified peek; verified below).
	explicit := len(req.Packages) > 0
	wanted := req.Packages
	var searched []Repo
	if explicit {
		searched = repos
	} else {
		wanted = DefaultPackages(req.Backend)
		for _, r := range repos {
			if r.Part == PartDebs {
				searched = append(searched, r)
			}
		}
		if len(searched) == 0 {
			all, _ := ListRepos(a.FS, kitDir)
			var have []string
			for _, r := range all {
				have = append(have, r.Part+"/"+r.Name)
			}
			return nil, &Error{Kind: ErrNoRepo, Detail: "the kit has no " + PartDebs + "/" + a.Codename + "-" + a.Arch + " repository", Have: have, Want: a.Codename + "-" + a.Arch}
		}
	}
	home := map[string]int{} // package -> index in searched
	locked := make([]map[string]string, len(searched))
	var peekErr error // a repository whose lock cannot be read is skipped, and reported if a package is then missing
	for i, r := range searched {
		locked[i] = map[string]string{}
		entries, err := peekLock(a.FS, r)
		if err != nil {
			if peekErr == nil {
				peekErr = err
			}
			continue
		}
		for _, e := range entries {
			locked[i][e.Name] = e.Version
		}
		for name := range locked[i] {
			if _, ok := home[name]; !ok {
				home[name] = i
			}
		}
	}
	var missing []string
	for _, name := range wanted {
		if _, ok := home[name]; ok {
			res.Requested = append(res.Requested, name)
		} else if explicit {
			missing = append(missing, name)
		} else {
			res.NotInKit = append(res.NotInKit, name)
		}
	}
	if len(missing) > 0 {
		if peekErr != nil {
			return nil, peekErr
		}
		return nil, &Error{Kind: ErrNotInKit, Detail: "not in the repositories of the kit for this computer", Names: missing}
	}
	if len(res.Requested) == 0 {
		if peekErr != nil {
			return nil, peekErr
		}
		return nil, &Error{Kind: ErrNotInKit, Detail: "none of the default packages is in the kit", Names: res.NotInKit}
	}

	// Already installed at the kit's version: skipped (idempotent). dpkg-query only reads.
	installed, err := a.installedVersions(ctx, res.Requested)
	if err != nil {
		return nil, err
	}
	var todo []string
	for _, name := range res.Requested {
		if v, ok := installed[name]; ok && v == locked[home[name]][name] {
			res.Skipped = append(res.Skipped, Pkg{Name: name, Version: v})
			continue
		}
		todo = append(todo, name)
	}
	if len(todo) == 0 {
		return res, nil
	}

	// Verify only the repositories the remaining packages live in, before anything else happens.
	used := map[int]bool{}
	for _, name := range todo {
		used[home[name]] = true
	}
	var order []int
	for i := range searched {
		if used[i] {
			order = append(order, i)
		}
	}
	var verified []*VerifiedRepo
	for _, i := range order {
		a.step(StepVerify, searched[i].Part+"/"+searched[i].Name)
		v, err := VerifyRepo(ctx, a.FS, searched[i], func(f string) { a.step(StepVerify, f) })
		if err != nil {
			return nil, err
		}
		verified = append(verified, v)
		res.Repos = append(res.Repos, searched[i].Part+"/"+searched[i].Name)
	}
	trusted := map[string]map[string]bool{} // name -> versions of the verified lock
	for _, v := range verified {
		for _, e := range v.Entries {
			if trusted[e.Name] == nil {
				trusted[e.Name] = map[string]bool{}
			}
			trusted[e.Name][e.Version] = true
		}
	}

	return a.install(ctx, res, verified, trusted, todo, req.DryRun)
}

// install writes the temporary source (only now that the repositories verified), updates the index,
// reads apt's plan, checks it and, unless dry, installs. The source is removed on every path.
func (a *Apt) install(ctx context.Context, res *Result, repos []*VerifiedRepo, trusted map[string]map[string]bool, todo []string, dry bool) (out *Result, err error) {
	aptGet, err := a.LookPath("apt-get")
	if err != nil {
		return nil, aptErr(ErrApt, "apt-get was not found on this computer", err)
	}
	var lines strings.Builder
	for _, r := range repos {
		if !safeRepoPath(r.Dir) {
			return nil, aptErr(ErrIO, "the folder "+r.Dir+" has characters apt cannot take in a source line (use letters, digits and . _ - / +)", nil)
		}
		fmt.Fprintf(&lines, "deb [trusted=yes] file:%s ./\n", r.Dir)
	}

	list := a.sourceList()
	var opts []string
	if dry {
		scratch, err := os.MkdirTemp(a.ScratchDir, "localai-kit-apt-")
		if err != nil {
			return nil, aptErr(ErrIO, "cannot create a scratch folder for the dry run", err)
		}
		defer os.RemoveAll(scratch)
		for _, d := range []string{"lists/partial", "cache/archives/partial"} {
			if err := os.MkdirAll(filepath.Join(scratch, d), 0o755); err != nil {
				return nil, aptErr(ErrIO, "cannot create a scratch folder for the dry run", err)
			}
		}
		list = filepath.Join(scratch, "ark.list")
		opts = append(opts,
			"-o", "Dir::State::Lists="+filepath.Join(scratch, "lists"),
			"-o", "Dir::Cache="+filepath.Join(scratch, "cache"))
		if err := config.WriteFileAtomicFS(a.FS, list, []byte(lines.String()), 0o644, -1, -1); err != nil {
			return nil, aptErr(ErrIO, "cannot write the scratch source", err)
		}
	} else {
		if err := config.WriteFileAtomicFS(a.FS, list, []byte(lines.String()), 0o644, -1, -1); err != nil {
			return nil, aptErr(ErrIO, "cannot write "+list, err)
		}
		defer func() {
			if rerr := a.FS.Remove(list); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				cerr := &Error{Kind: ErrCleanup, Detail: "remove " + list + " by hand: apt would trust it", Err: rerr}
				if err == nil {
					out, err = res, cerr
				} else {
					err = errors.Join(err, cerr)
				}
			}
		}()
	}
	srcOpts := append([]string{"-o", "Dir::Etc::sourcelist=" + list, "-o", "Dir::Etc::sourceparts=-"}, opts...)

	a.step(StepUpdate, "")
	updateArgs := append([]string{"update"}, srcOpts...)
	updateArgs = append(updateArgs, "-o", "APT::Get::List-Cleanup=0")
	if err := a.apt(ctx, aptGet, updateArgs, aptUpdateTimeout, "apt-get update"); err != nil {
		return nil, err
	}

	a.step(StepPlan, "")
	planArgs := append([]string{"install", "-s", "-y", "--no-install-recommends"}, srcOpts...)
	planArgs = append(planArgs, todo...)
	planOut, err := a.aptOut(ctx, aptGet, planArgs, aptPlanTimeout, "apt-get install -s")
	if err != nil {
		return nil, err
	}
	plan, removed := parsePlan(planOut)
	if len(removed) > 0 {
		return nil, &Error{Kind: ErrUnsafePlan, Detail: "apt would remove packages", Names: removed}
	}
	var stray []string
	for _, p := range plan {
		if !trusted[p.Name][p.Version] {
			stray = append(stray, p.Name+"="+p.Version)
		}
	}
	if len(stray) > 0 {
		return nil, &Error{Kind: ErrUnsafePlan, Detail: "apt would install packages that are not in the verified kit repository", Names: stray}
	}
	res.Plan = plan
	if res.Plan == nil {
		res.Plan = []Pkg{}
	}
	if dry {
		return res, nil
	}

	a.step(StepInstall, "")
	instArgs := append([]string{"install", "-y", "--no-install-recommends"}, srcOpts...)
	instArgs = append(instArgs, todo...)
	if err := a.apt(ctx, aptGet, instArgs, aptInstallTimeout, "apt-get install"); err != nil {
		return nil, err
	}
	now, err := a.installedVersions(ctx, todo)
	if err != nil {
		return nil, err
	}
	for _, name := range todo {
		v, ok := now[name]
		if !ok {
			return nil, aptErr(ErrApt, "apt finished but "+name+" is not installed", nil)
		}
		res.Installed = append(res.Installed, Pkg{Name: name, Version: v})
	}
	return res, nil
}

// safeRepoPath reports whether a folder can be written into an apt source line as it is.
var safePathRe = regexp.MustCompile(`^/[A-Za-z0-9_./+-]+$`)

func safeRepoPath(p string) bool { return safePathRe.MatchString(p) && !strings.Contains(p, "..") }

// aptEnv is the environment of apt-get and dpkg-query: the fixed PATH, no questions, English output.
func aptEnv() []string {
	return []string{"PATH=" + execx.FixedPath, "DEBIAN_FRONTEND=noninteractive", "APT_LISTCHANGES_FRONTEND=none", "LC_ALL=C"}
}

// apt runs apt-get and fails on a non-zero exit with the tail of its output.
func (a *Apt) apt(ctx context.Context, bin string, args []string, timeout time.Duration, what string) error {
	_, err := a.aptOut(ctx, bin, args, timeout, what)
	return err
}

func (a *Apt) aptOut(ctx context.Context, bin string, args []string, timeout time.Duration, what string) ([]byte, error) {
	res, err := a.Runner.Run(ctx, execx.Cmd{Path: bin, Args: args, Timeout: timeout, MaxOut: aptMaxOut, Env: aptEnv()})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, aptErr(ErrApt, what+" could not run", err)
	}
	if res.ExitCode != 0 {
		return nil, &Error{Kind: ErrApt, Detail: fmt.Sprintf("%s failed (exit %d)", what, res.ExitCode), Names: tailLines(res.Stderr, res.Stdout)}
	}
	return res.Stdout, nil
}

// tailLines returns up to the last five non-empty lines of the first non-empty output.
func tailLines(outs ...[]byte) []string {
	for _, o := range outs {
		var lines []string
		for _, l := range strings.Split(string(o), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, clip(l, 200))
			}
		}
		if len(lines) > 0 {
			if len(lines) > 5 {
				lines = lines[len(lines)-5:]
			}
			return lines
		}
	}
	return nil
}

var instRe = regexp.MustCompile(`^Inst (\S+) (?:\[\S+\] )?\((\S+) `)

// parsePlan reads the "Inst" and "Remv" lines of `apt-get install -s`.
func parsePlan(out []byte) (install []Pkg, remove []string) {
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "Remv "):
			if f := strings.Fields(line); len(f) > 1 {
				remove = append(remove, f[1])
			}
		case strings.HasPrefix(line, "Inst "):
			if m := instRe.FindStringSubmatch(line); m != nil {
				install = append(install, Pkg{Name: m[1], Version: m[2]})
			} else {
				// A line apt printed in a shape this parser does not know is a package it could not
				// check: it is reported as unverified rather than skipped.
				f := strings.Fields(line)
				if len(f) > 1 {
					install = append(install, Pkg{Name: f[1], Version: "?"})
				}
			}
		}
	}
	return install, remove
}

// installedVersions asks dpkg which of names are installed (status "ii") and at which version. It
// only reads. A name dpkg does not know is simply absent from the answer.
func (a *Apt) installedVersions(ctx context.Context, names []string) (map[string]string, error) {
	bin, err := a.LookPath("dpkg-query")
	if err != nil {
		return nil, aptErr(ErrApt, "dpkg-query was not found on this computer", err)
	}
	args := append([]string{"-W", "-f=${Package}\t${db:Status-Abbrev}\t${Version}\n"}, names...)
	res, err := a.Runner.Run(ctx, execx.Cmd{Path: bin, Args: args, Timeout: dpkgQueryTimeout, MaxOut: aptMaxOut, Env: aptEnv()})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, aptErr(ErrApt, "dpkg-query could not run", err)
	}
	if res.ExitCode != 0 && res.ExitCode != 1 { // 1: some name is unknown to dpkg
		return nil, &Error{Kind: ErrApt, Detail: fmt.Sprintf("dpkg-query failed (exit %d)", res.ExitCode), Names: tailLines(res.Stderr)}
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && strings.HasPrefix(strings.TrimSpace(f[1]), "ii") {
			out[f[0]] = f[2]
		}
	}
	return out, nil
}
