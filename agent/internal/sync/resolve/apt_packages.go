package resolve

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
)

// This file is the pure half of apt_closure (ark-sync.md section 4.9.7): rendering the private apt
// tree's source files, parsing `apt-get --print-uris`, building the local repository index and the
// lock file. Nothing here touches the disk or runs a process, so each rule is tested on its own.

var (
	pkgNameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9+.\-]*$`)
	debVersionRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+~:\-]*$`)
	debArchRe     = regexp.MustCompile(`^[a-z0-9]+$`)
	codenameRe    = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	repoNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._\-]*$`)
	suiteTokenRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]*(/[A-Za-z0-9._+\-]+)*$`)
	flatSuiteRe   = regexp.MustCompile(`^([A-Za-z0-9._+\-]+(/[A-Za-z0-9._+\-]+)*)?/$`)
	poolNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~\-]*\.deb$`)
	sha256HexRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	maxClosurePkg = 20000
)

// Default archive URIs (ark-sync.md section 4.9.7): amd64 comes from archive.ubuntu.com, every other
// architecture from ports.ubuntu.com.
const (
	ubuntuArchiveURI = "http://archive.ubuntu.com/ubuntu"
	ubuntuPortsURI   = "http://ports.ubuntu.com/ubuntu-ports"
	// ubuntuKeyring is the keyring of the Ubuntu archive; it is also the first name of
	// catalog.EmbeddedKeyrings.
	ubuntuKeyring = "ubuntu-archive-keyring.gpg"
)

// Pkg is one deb of the closure, as apt printed it and the signed index vouched for it.
type Pkg struct {
	Name, Version, Arch string
	// URI is where apt would download it; Size and SHA256 come from the signed index.
	URI    string
	Size   int64
	SHA256 string
	// PoolName is the file name inside pool/ (the URI's last element, made safe).
	PoolName string
}

// NameVersion is "name=version".
func (p Pkg) NameVersion() string { return p.Name + "=" + p.Version }

// aptSource is what a private tree's sources are generated from.
type aptSource struct {
	Codename string
	Arch     string
	// Mirror is the Ubuntu archive URI ("" selects the default of the architecture).
	Mirror string
	Extra  []catalog.ExtraRepo
	// HTTPS rewrites every http URI to https (set when the throttling proxy is in use: it tunnels
	// CONNECT only, which carries https and not plain http).
	HTTPS bool
	// Unverified names the extra repositories that have no keyring and are taken without checking the
	// vendor signature (sync.apt_unverified_repos: allow). They are written with `Trusted: yes` and no
	// Signed-By; every other repository, and always the Ubuntu archive, is verified with its keyring.
	Unverified map[string]bool
}

// isFlatSuite reports whether a suite names a flat repository (it ends with a slash).
func isFlatSuite(suite string) bool { return strings.HasSuffix(suite, "/") }

// validate checks every value that is about to be written into a deb822 file or an argv.
func (s aptSource) validate() error {
	if !codenameRe.MatchString(s.Codename) {
		return fail(CodeApt, nil, "%q is not an Ubuntu codename", s.Codename)
	}
	if s.Arch != "amd64" && s.Arch != "arm64" {
		return fail(CodeApt, nil, "architecture %q is not amd64 or arm64", s.Arch)
	}
	seen := map[string]bool{}
	for _, r := range s.Extra {
		switch {
		case !repoNameRe.MatchString(r.Name):
			return fail(CodeApt, nil, "extra repository name %q is not a slug", r.Name)
		case seen[r.Name]:
			return fail(CodeApt, nil, "extra repository %q is listed twice", r.Name)
		case !contains(catalog.EmbeddedKeyrings, r.Key):
			return fail(CodeApt, nil, "extra repository %q uses a key that is not embedded in the agent", r.Name)
		case isFlatSuite(r.Suite):
			// A flat repository (the NVIDIA CUDA one: suite "/") has no components; deb822 requires
			// the Components field to be left out for it.
			if !flatSuiteRe.MatchString(r.Suite) || strings.Contains(r.Suite, "..") {
				return fail(CodeApt, nil, "extra repository %q has a bad suite", r.Name)
			}
			if len(r.Components) != 0 {
				return fail(CodeApt, nil, "extra repository %q is a flat repository (its suite ends with /) and must have no components", r.Name)
			}
		case !suiteTokenRe.MatchString(r.Suite):
			return fail(CodeApt, nil, "extra repository %q has a bad suite", r.Name)
		case len(r.Components) == 0:
			return fail(CodeApt, nil, "extra repository %q has no components", r.Name)
		}
		seen[r.Name] = true
		for _, c := range r.Components {
			if !suiteTokenRe.MatchString(c) {
				return fail(CodeApt, nil, "extra repository %q has a bad component", r.Name)
			}
		}
		if _, err := cleanURI(r.URI); err != nil {
			return fail(CodeApt, err, "extra repository %q has a bad URI", r.Name)
		}
	}
	if s.Mirror != "" {
		if _, err := cleanURI(s.Mirror); err != nil {
			return fail(CodeApt, err, "the Ubuntu mirror URI is not usable")
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// cleanURI returns the URI trimmed of a trailing slash if it is http(s) with a host, no credentials,
// no query and no whitespace.
func cleanURI(raw string) (*url.URL, error) {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\"'") {
		return nil, fmt.Errorf("not a plain URI")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("not an http(s) URI with a host")
	}
	return u, nil
}

func (s aptSource) ubuntuURI() string {
	switch {
	case s.Mirror != "":
		return s.fix(s.Mirror)
	case s.Arch == "amd64":
		return s.fix(ubuntuArchiveURI)
	}
	return s.fix(ubuntuPortsURI)
}

func (s aptSource) fix(uri string) string {
	if s.HTTPS && strings.HasPrefix(uri, "http://") {
		return "https://" + strings.TrimPrefix(uri, "http://")
	}
	return uri
}

// hosts are the "host[:port]" values a deb URI may have: the Ubuntu archive and the extra repos.
func (s aptSource) hosts() []string {
	set := map[string]bool{}
	add := func(raw string) {
		if u, err := cleanURI(raw); err == nil {
			set[strings.ToLower(u.Host)] = true
		}
	}
	add(s.ubuntuURI())
	for _, r := range s.Extra {
		add(r.URI)
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// proxyHosts are the hostnames (no port) the throttling proxy may tunnel to.
func (s aptSource) proxyHosts() []string {
	set := map[string]bool{}
	for _, h := range s.hosts() {
		if u, err := url.Parse("https://" + h); err == nil {
			set[strings.ToLower(u.Hostname())] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// renderUbuntuSources is A/etc/apt/sources.list.d/ubuntu.sources. keyring is the absolute path of
// the copied archive keyring.
func (s aptSource) renderUbuntuSources(keyring string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Types: deb\nURIs: %s\n", s.ubuntuURI())
	fmt.Fprintf(&b, "Suites: %s %s-updates %s-security\n", s.Codename, s.Codename, s.Codename)
	b.WriteString("Components: main restricted universe multiverse\n")
	fmt.Fprintf(&b, "Architectures: %s\nSigned-By: %s\n", s.Arch, keyring)
	return b.String()
}

// renderExtraSources is A/etc/apt/sources.list.d/extra.sources: one stanza per extra repository, in
// the catalog's order, "" when there are none. keyringDir is A/etc/apt/trusted.gpg.d.
func (s aptSource) renderExtraSources(keyringDir string) string {
	var b strings.Builder
	for i, r := range s.Extra {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s\n", s.fix(r.URI), r.Suite)
		if len(r.Components) > 0 { // a flat repository has none
			fmt.Fprintf(&b, "Components: %s\n", strings.Join(r.Components, " "))
		}
		fmt.Fprintf(&b, "Architectures: %s\n", s.Arch)
		if s.Unverified[r.Name] {
			// The narrowest trust apt has: this one source, no key. Never written for the Ubuntu archive.
			b.WriteString("Trusted: yes\n")
		} else {
			fmt.Fprintf(&b, "Signed-By: %s\n", path.Join(keyringDir, r.Key))
		}
	}
	return b.String()
}

// aptOptions is the common option list O of ark-sync.md section 4.9.7, with two additions the spec
// did not name: Acquire::ForceHash=SHA256 (apt otherwise prints MD5Sum for a record that has both)
// and APT::Update::Error-Mode=any (a source that cannot be fetched fails the update instead of
// being a warning). proxy is the throttling proxy address ("" for none).
func aptOptions(tree, arch, user, proxy string) []string {
	o := []string{
		"-o", "Dir=" + tree,
		"-o", "Dir::State::status=" + tree + "/var/lib/dpkg/status",
		"-o", "Dir::Etc::sourcelist=/dev/null",
		"-o", "Dir::Etc::sourceparts=" + tree + "/etc/apt/sources.list.d",
		"-o", "Dir::Etc::trustedparts=" + tree + "/etc/apt/trusted.gpg.d",
		"-o", "Dir::Etc::preferencesparts=" + tree + "/etc/apt/preferences.d",
		"-o", "APT::Architecture=" + arch,
		"-o", "APT::Architectures=" + arch,
		"-o", "Debug::NoLocking=1",
	}
	if user != "" {
		o = append(o, "-o", "APT::Sandbox::User="+user)
	}
	o = append(o,
		"-o", "Acquire::Languages=none",
		"-o", "APT::Install-Recommends=false",
		"-o", "Acquire::ForceHash=SHA256",
		"-o", "APT::Update::Error-Mode=any",
	)
	if proxy != "" {
		o = append(o, "-o", "Acquire::https::Proxy=http://"+proxy)
	}
	return o
}

// parsePrintURIs parses `apt-get --print-uris` lines: '<uri>' <filename> <size> SHA256:<hex>. Lines
// that do not start with a quote are apt's chatter and are skipped; a quoted line that is not exactly
// that shape is an error, and so is any hash other than SHA256 (MD5 and SHA1 are not trusted), a deb
// of another architecture, a URI outside allowedHosts, an unsafe name, or the same package twice with
// different content. The same package twice with the same content is listed once.
func parsePrintURIs(out []byte, arch string, allowedHosts []string) ([]Pkg, error) {
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	byName := map[string]Pkg{}
	byPool := map[string]Pkg{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "'") {
			continue
		}
		p, err := parseURILine(line, arch)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(p.URI)
		if !allowed[strings.ToLower(u.Host)] {
			return nil, fail(CodeApt, nil, "apt wants to download %s from %s, which the catalog item does not name", p.Name, u.Host)
		}
		if prev, dup := byName[p.Name]; dup {
			if prev.Version != p.Version || prev.SHA256 != p.SHA256 {
				return nil, fail(CodeApt, nil, "apt lists %s twice with different content", p.Name)
			}
			continue
		}
		if prev, dup := byPool[p.PoolName]; dup && prev.SHA256 != p.SHA256 {
			return nil, fail(CodeApt, nil, "two different packages would be stored as %s", p.PoolName)
		}
		byName[p.Name] = p
		byPool[p.PoolName] = p
		if len(byName) > maxClosurePkg {
			return nil, fail(CodeApt, nil, "the closure has more than %d packages", maxClosurePkg)
		}
	}
	if len(byName) == 0 {
		return nil, fail(CodeApt, nil, "apt did not list any package to download")
	}
	pkgs := make([]Pkg, 0, len(byName))
	for _, p := range byName {
		pkgs = append(pkgs, p)
	}
	sortPkgs(pkgs)
	return pkgs, nil
}

func sortPkgs(pkgs []Pkg) {
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Name != pkgs[j].Name {
			return pkgs[i].Name < pkgs[j].Name
		}
		return pkgs[i].Version < pkgs[j].Version
	})
}

// parseURILine parses one quoted line.
func parseURILine(line, arch string) (Pkg, error) {
	bad := func(why string) (Pkg, error) {
		return Pkg{}, fail(CodeApt, nil, "apt printed a line that is not '<uri>' <file> <size> SHA256:<hex> (%s): %s", why, Clip([]byte(line), 120))
	}
	rest := line[1:]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return bad("no closing quote")
	}
	uri := rest[:end]
	fields := strings.Fields(rest[end+1:])
	if len(fields) != 3 {
		return bad("wrong number of fields")
	}
	file, sizeS, hash := fields[0], fields[1], fields[2]
	algo, sum, ok := strings.Cut(hash, ":")
	switch {
	case !ok:
		return bad("no hash")
	case algo != "SHA256":
		return Pkg{}, fail(CodeApt, nil, "apt printed a %s hash for %s; only SHA256 from the signed index is accepted", Clip([]byte(algo), 12), Clip([]byte(file), 60))
	case !sha256HexRe.MatchString(sum):
		return bad("the SHA256 is not 64 lowercase hex digits")
	}
	size, err := strconv.ParseInt(sizeS, 10, 64)
	if err != nil || size < 0 {
		return bad("bad size")
	}
	u, err := cleanURI(uri)
	if err != nil {
		return bad("bad URI")
	}
	name, version, debArch, err := splitDebName(file)
	if err != nil {
		return bad(err.Error())
	}
	if debArch != arch && debArch != "all" {
		return Pkg{}, fail(CodeApt, nil, "%s is built for %s, not %s", name, debArch, arch)
	}
	pool, err := poolName(u)
	if err != nil {
		return bad(err.Error())
	}
	return Pkg{Name: name, Version: version, Arch: debArch, URI: uri, Size: size, SHA256: sum, PoolName: pool}, nil
}

// splitDebName splits apt's destination name <name>_<version>_<arch>.deb; the epoch colon is written
// %3a there.
func splitDebName(file string) (name, version, arch string, err error) {
	base, ok := strings.CutSuffix(file, ".deb")
	if !ok {
		return "", "", "", fmt.Errorf("not a .deb")
	}
	parts := strings.Split(base, "_")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("not name_version_arch")
	}
	name, version, arch = parts[0], strings.NewReplacer("%3a", ":", "%3A", ":").Replace(parts[1]), parts[2]
	if !pkgNameRe.MatchString(name) || !debVersionRe.MatchString(version) || !debArchRe.MatchString(arch) {
		return "", "", "", fmt.Errorf("unsafe package name, version or architecture")
	}
	return name, version, arch, nil
}

// poolName is the last element of the URI's path, decoded, with ':' replaced by '_' (a Debian pool
// name never has an epoch), and it must be a plain deb file name: no separators, no leading dot, no
// control characters.
func poolName(u *url.URL) (string, error) {
	base := path.Base(u.Path)
	if base == "." || base == "/" || strings.ContainsAny(base, `/\`) {
		return "", fmt.Errorf("the URI has no file name")
	}
	base = strings.ReplaceAll(base, ":", "_")
	if !poolNameRe.MatchString(base) {
		return "", fmt.Errorf("unsafe file name")
	}
	return base, nil
}

// closureVersion is the first 12 hex of the sha256 of the sorted name=version list, one per line.
func closureVersion(pkgs []Pkg) string {
	list := make([]string, len(pkgs))
	for i, p := range pkgs {
		list[i] = p.NameVersion()
	}
	sort.Strings(list)
	sum := sha256.Sum256([]byte(strings.Join(list, "\n") + "\n"))
	return hex.EncodeToString(sum[:])[:12]
}

// totalSize is the sum of the sizes of the debs.
func totalSize(pkgs []Pkg) int64 {
	var n int64
	for _, p := range pkgs {
		n += p.Size
	}
	return n
}

// packagesLock is PACKAGES.lock: "name=version sha256" per package, sorted.
func packagesLock(pkgs []Pkg) []byte {
	sorted := append([]Pkg(nil), pkgs...)
	sortPkgs(sorted)
	var b bytes.Buffer
	for _, p := range sorted {
		fmt.Fprintf(&b, "%s %s\n", p.NameVersion(), p.SHA256)
	}
	return b.Bytes()
}

// parsePackagesLock reads a PACKAGES.lock into poolless Pkgs (name, version, sha256).
func parsePackagesLock(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		nv, sum, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && sha256HexRe.MatchString(sum) {
			out[nv] = sum
		}
	}
	return out
}

// gzipBytes compresses like `gzip -n -9`: no file name, no modification time.
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	zw.Header = gzip.Header{}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stanza is one control paragraph: fields in order, a field's value including its continuation lines.
type stanza struct {
	names  []string
	values map[string]string
}

func (s *stanza) get(name string) string { return s.values[strings.ToLower(name)] }

func (s *stanza) set(name, value string) {
	k := strings.ToLower(name)
	if _, ok := s.values[k]; !ok {
		s.names = append(s.names, name)
	}
	s.values[k] = value
}

func (s *stanza) drop(name string) {
	k := strings.ToLower(name)
	if _, ok := s.values[k]; !ok {
		return
	}
	delete(s.values, k)
	for i, n := range s.names {
		if strings.ToLower(n) == k {
			s.names = append(s.names[:i], s.names[i+1:]...)
			break
		}
	}
}

func (s *stanza) String() string {
	var b strings.Builder
	for _, n := range s.names {
		v := s.values[strings.ToLower(n)]
		b.WriteString(n)
		b.WriteString(":")
		if v != "" && !strings.HasPrefix(v, "\n") {
			b.WriteString(" ")
		}
		b.WriteString(v)
		b.WriteString("\n")
	}
	return b.String()
}

// parseStanzas reads the paragraphs of `apt-cache show` output.
func parseStanzas(data []byte) ([]*stanza, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("the output has a NUL byte")
	}
	var out []*stanza
	var cur *stanza
	var last string
	flush := func() {
		if cur != nil && len(cur.names) > 0 {
			out = append(out, cur)
		}
		cur, last = nil, ""
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case line[0] == ' ' || line[0] == '\t':
			if cur == nil || last == "" {
				return nil, fmt.Errorf("line %d continues no field", i+1)
			}
			cur.values[last] += "\n" + line
		default:
			name, val, ok := strings.Cut(line, ":")
			if !ok || name == "" || strings.ContainsAny(name, " \t") {
				return nil, fmt.Errorf("line %d is not a field", i+1)
			}
			if cur == nil {
				cur = &stanza{values: map[string]string{}}
			}
			k := strings.ToLower(name)
			if _, dup := cur.values[k]; dup {
				return nil, fmt.Errorf("line %d repeats the field %s", i+1, name)
			}
			cur.names = append(cur.names, name)
			cur.values[k] = strings.TrimPrefix(val, " ")
			last = k
		}
	}
	flush()
	return out, nil
}

// buildPackages makes the Packages file of the local repository from `apt-cache show name=version`
// output: for each package of the closure the stanza that matches it, with Filename rewritten to
// pool/<file>, Description-md5 dropped, and Size and SHA256 set to the values the signed index gave
// (a stanza whose own SHA256 or Size disagrees is an error). The stanzas are written in name order.
func buildPackages(show []byte, pkgs []Pkg) ([]byte, error) {
	stanzas, err := parseStanzas(show)
	if err != nil {
		return nil, fail(CodeApt, err, "apt-cache show printed something unexpected")
	}
	type key struct{ name, version string }
	index := map[key]*stanza{}
	for _, s := range stanzas {
		k := key{s.get("Package"), s.get("Version")}
		if _, ok := index[k]; !ok { // the first record of a name=version wins
			index[k] = s
		}
	}
	sorted := append([]Pkg(nil), pkgs...)
	sortPkgs(sorted)
	var out bytes.Buffer
	for _, p := range sorted {
		s, ok := index[key{p.Name, p.Version}]
		if !ok {
			return nil, fail(CodeApt, nil, "apt-cache has no record of %s", p.NameVersion())
		}
		if v := s.get("SHA256"); v != "" && v != p.SHA256 {
			return nil, fail(CodeApt, nil, "the index record of %s has another SHA256 than apt downloads", p.NameVersion())
		}
		if v := s.get("Size"); v != "" && v != strconv.FormatInt(p.Size, 10) {
			return nil, fail(CodeApt, nil, "the index record of %s has another size than apt downloads", p.NameVersion())
		}
		s.drop("Description-md5")
		s.set("Filename", "pool/"+p.PoolName)
		s.set("Size", strconv.FormatInt(p.Size, 10))
		s.set("SHA256", p.SHA256)
		out.WriteString(s.String())
		out.WriteString("\n")
	}
	return out.Bytes(), nil
}

// readme is README.txt of a closure: what it is, how to use it offline (rebuild-kit.md section 4.1.4).
func readme(codename, arch, version string, at time.Time, pkgs []Pkg, wanted []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Local AI offline Ubuntu packages\n================================\n\n")
	fmt.Fprintf(&b, "Ubuntu %s, %s. Closure %s, made %s UTC.\n", codename, arch, version, at.UTC().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "%d packages, %d bytes. Wanted: %s.\n\n", len(pkgs), totalSize(pkgs), strings.Join(wanted, " "))
	b.WriteString("This folder is a flat apt repository: pool/ holds the .deb files, Packages and\n")
	b.WriteString("Packages.gz describe them, PACKAGES.lock lists name=version and sha256 of each.\n")
	b.WriteString("The list was taken from the signed Ubuntu indexes; apt checks nothing here, so\n")
	b.WriteString("check the files first:  aictl kit verify --part debs\n\n")
	b.WriteString("Install with no internet (run as root, REPO is this folder):\n\n")
	b.WriteString("  REPO=<this folder>\n")
	b.WriteString("  echo \"deb [trusted=yes] file:$REPO ./\" > /etc/apt/sources.list.d/localai-ark.list\n")
	b.WriteString("  apt-get update -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/localai-ark.list -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0\n")
	b.WriteString("  apt-get install -y --no-install-recommends -o Dir::Etc::sourcelist=/etc/apt/sources.list.d/localai-ark.list -o Dir::Etc::sourceparts=- <packages>\n")
	b.WriteString("  rm /etc/apt/sources.list.d/localai-ark.list\n\n")
	b.WriteString("Or simply:  aictl kit install-debs\n\n")
	b.WriteString("More: docs/runbooks/06-rebuild-a-node-offline.md\n")
	return []byte(b.String())
}
