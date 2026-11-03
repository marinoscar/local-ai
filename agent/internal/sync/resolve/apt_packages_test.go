package resolve

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// uriLine builds one line of `apt-get --print-uris` output. The hash is computed from seed, never
// written down.
func uriLine(uri, file string, size int, algo, seed string) string {
	sum := sha256Hex([]byte(seed))
	switch algo {
	case "MD5Sum":
		sum = sum[:32]
	case "SHA1":
		sum = sum[:40]
	}
	return fmt.Sprintf("'%s' %s %d %s:%s", uri, file, size, algo, sum)
}

const hostA = "mirror.example.test"

func TestParsePrintURIsTable(t *testing.T) {
	good1 := uriLine("http://"+hostA+"/ubuntu/pool/main/l/libx/libx_1.2-1_amd64.deb", "libx_1.2-1_amd64.deb", 1000, "SHA256", "libx")
	good2 := uriLine("http://"+hostA+"/ubuntu/pool/main/y/y/y_2%3a3.0-1_amd64.deb", "y_2%3a3.0-1_amd64.deb", 2000, "SHA256", "y")
	tests := []struct {
		name     string
		in       string
		wantCode string
		check    func(t *testing.T, p []Pkg)
	}{
		{"two packages and chatter", "Reading package lists...\nNeed to get 3000 B of archives.\n" + good1 + "\n" + good2 + "\n", "", func(t *testing.T, p []Pkg) {
			if len(p) != 2 || p[0].Name != "libx" || p[1].Name != "y" {
				t.Fatalf("pkgs = %+v", p)
			}
			if p[1].Version != "2:3.0-1" {
				t.Errorf("epoch not decoded: %q", p[1].Version)
			}
			if p[1].PoolName != "y_2_3.0-1_amd64.deb" {
				t.Errorf("pool name = %q", p[1].PoolName)
			}
			if p[0].Size != 1000 || p[0].SHA256 != sha256Hex([]byte("libx")) {
				t.Errorf("size/sha = %d %s", p[0].Size, p[0].SHA256)
			}
		}},
		{"md5 only is refused", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "a_1_amd64.deb", 5, "MD5Sum", "a"), doctor.SYN060, nil},
		{"sha1 is refused", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "a_1_amd64.deb", 5, "SHA1", "a"), doctor.SYN060, nil},
		{"sha512 is refused", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "a_1_amd64.deb", 5, "SHA512", "a"), doctor.SYN060, nil},
		{"no hash", "'http://" + hostA + "/p/a_1_amd64.deb' a_1_amd64.deb 5", doctor.SYN060, nil},
		{"short sha256", "'http://" + hostA + "/p/a_1_amd64.deb' a_1_amd64.deb 5 SHA256:abcd", doctor.SYN060, nil},
		{"upper-case sha256", strings.Replace(good1, "SHA256:"+sha256Hex([]byte("libx")), "SHA256:"+strings.ToUpper(sha256Hex([]byte("libx"))), 1), doctor.SYN060, nil},
		{"wrong architecture", uriLine("http://"+hostA+"/p/a_1_arm64.deb", "a_1_arm64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"arch all is fine", uriLine("http://"+hostA+"/p/a_1_all.deb", "a_1_all.deb", 5, "SHA256", "a"), "", nil},
		{"host the item does not name", uriLine("http://evil.example.test/p/a_1_amd64.deb", "a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"uri with credentials", uriLine("http://login:pw"+at+hostA+"/p/a_1_amd64.deb", "a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"file uri", uriLine("file:///p/a_1_amd64.deb", "a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"traversal in the file name", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "../a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"slash in the name", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "x/a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"upper-case package name", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "A_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"two underscores too many", uriLine("http://"+hostA+"/p/a_1_amd64.deb", "a_1_x_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"negative size", "'http://" + hostA + "/p/a_1_amd64.deb' a_1_amd64.deb -5 SHA256:" + sha256Hex([]byte("a")), doctor.SYN060, nil},
		{"no closing quote", "'http://" + hostA + "/p/a_1_amd64.deb a_1_amd64.deb 5 SHA256:" + sha256Hex([]byte("a")), doctor.SYN060, nil},
		{"unsafe pool name", uriLine("http://"+hostA+"/p/.hidden.deb", "a_1_amd64.deb", 5, "SHA256", "a"), doctor.SYN060, nil},
		{"uri path with an encoded slash keeps only the base", uriLine("http://"+hostA+"/p/..%2f..%2fetc%2fa_1_amd64.deb", "a_1_amd64.deb", 5, "SHA256", "a"), "", func(t *testing.T, p []Pkg) {
			if p[0].PoolName != "a_1_amd64.deb" {
				t.Errorf("pool name = %q", p[0].PoolName)
			}
		}},
		{"same package twice, same content", good1 + "\n" + good1, "", func(t *testing.T, p []Pkg) {
			if len(p) != 1 {
				t.Errorf("listed %d times", len(p))
			}
		}},
		{"same package twice, other content", good1 + "\n" + uriLine("http://"+hostA+"/ubuntu/pool/main/l/libx/libx_1.2-1_amd64.deb", "libx_1.2-1_amd64.deb", 1000, "SHA256", "other"), doctor.SYN060, nil},
		{"two names, one pool file, other content", good1 + "\n" + uriLine("http://"+hostA+"/ubuntu/pool/main/l/libx/libx_1.2-1_amd64.deb", "libz_1.2-1_amd64.deb", 1000, "SHA256", "other"), doctor.SYN060, nil},
		{"empty output", "", doctor.SYN060, nil},
		{"chatter only", "Reading package lists...\n", doctor.SYN060, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePrintURIs([]byte(tc.in), "amd64", []string{hostA})
			if tc.wantCode != "" {
				if CodeOf(err) != tc.wantCode {
					t.Fatalf("code = %q, want %q (err %v)", CodeOf(err), tc.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestParsePrintURIsBoundsTheClosure(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxClosurePkg+1; i++ {
		name := fmt.Sprintf("p%d", i)
		b.WriteString(uriLine("http://"+hostA+"/p/"+name+"_1_amd64.deb", name+"_1_amd64.deb", 1, "SHA256", name) + "\n")
	}
	if _, err := parsePrintURIs([]byte(b.String()), "amd64", []string{hostA}); CodeOf(err) != doctor.SYN060 {
		t.Fatalf("a closure over the bound was accepted: %v", err)
	}
}

func TestClosureVersionIsStableAndSensitive(t *testing.T) {
	a := Pkg{Name: "a", Version: "1"}
	b := Pkg{Name: "b", Version: "2"}
	v1 := closureVersion([]Pkg{a, b})
	if len(v1) != 12 {
		t.Fatalf("version %q is not 12 hex", v1)
	}
	if v2 := closureVersion([]Pkg{b, a}); v2 != v1 {
		t.Errorf("order changed the version: %s vs %s", v1, v2)
	}
	if v3 := closureVersion([]Pkg{a, {Name: "b", Version: "3"}}); v3 == v1 {
		t.Error("a new version of a package did not change the closure version")
	}
	if v4 := closureVersion([]Pkg{{Name: "a", Version: "1", SHA256: "x"}, b}); v4 != v1 {
		t.Error("the hash must not be part of the version, only name=version")
	}
}

func TestRenderSources(t *testing.T) {
	keyring := "/var/lib/localai/sync/apt/kit-debs-resolute-amd64/etc/apt/trusted.gpg.d/" + ubuntuKeyring
	amd := aptSource{Codename: "resolute", Arch: "amd64"}
	wantAmd := "Types: deb\nURIs: http://archive.ubuntu.com/ubuntu\nSuites: resolute resolute-updates resolute-security\n" +
		"Components: main restricted universe multiverse\nArchitectures: amd64\nSigned-By: " + keyring + "\n"
	if got := amd.renderUbuntuSources(keyring); got != wantAmd {
		t.Errorf("amd64 sources:\n%s\nwant:\n%s", got, wantAmd)
	}
	arm := aptSource{Codename: "resolute", Arch: "arm64"}
	if got := arm.renderUbuntuSources(keyring); !strings.Contains(got, "URIs: http://ports.ubuntu.com/ubuntu-ports\n") || !strings.Contains(got, "Architectures: arm64\n") {
		t.Errorf("arm64 sources:\n%s", got)
	}
	https := aptSource{Codename: "resolute", Arch: "amd64", HTTPS: true}
	if got := https.renderUbuntuSources(keyring); !strings.Contains(got, "URIs: https://archive.ubuntu.com/ubuntu\n") {
		t.Errorf("https sources:\n%s", got)
	}
	extra := aptSource{Codename: "resolute", Arch: "amd64", HTTPS: true, Extra: []catalog.ExtraRepo{
		{Name: "tailscale", URI: "http://pkgs.example.test/stable/ubuntu", Suite: "resolute", Components: []string{"main"}, Key: "tailscale-archive-keyring.gpg"},
		{Name: "cuda", URI: "https://dl.example.test/cuda", Suite: "stable", Components: []string{"main", "contrib"}, Key: "nvidia-cuda-archive-keyring.gpg"},
	}}
	wantExtra := "Types: deb\nURIs: https://pkgs.example.test/stable/ubuntu\nSuites: resolute\nComponents: main\nArchitectures: amd64\nSigned-By: /k/tailscale-archive-keyring.gpg\n\n" +
		"Types: deb\nURIs: https://dl.example.test/cuda\nSuites: stable\nComponents: main contrib\nArchitectures: amd64\nSigned-By: /k/nvidia-cuda-archive-keyring.gpg\n"
	if got := extra.renderExtraSources("/k"); got != wantExtra {
		t.Errorf("extra sources:\n%s\nwant:\n%s", got, wantExtra)
	}
	if got := amd.renderExtraSources("/k"); got != "" {
		t.Errorf("no extra repos must render nothing, got %q", got)
	}
	hosts := extra.hosts()
	if len(hosts) != 3 || hosts[0] != "archive.ubuntu.com" {
		t.Errorf("hosts = %v", hosts)
	}
	if ph := extra.proxyHosts(); len(ph) != 3 {
		t.Errorf("proxy hosts = %v", ph)
	}
}

func TestAptSourceValidateRejectsInjection(t *testing.T) {
	ok := catalog.ExtraRepo{Name: "tailscale", URI: "https://pkgs.example.test/ubuntu", Suite: "resolute", Components: []string{"main"}, Key: "tailscale-archive-keyring.gpg"}
	mod := func(f func(r *catalog.ExtraRepo)) aptSource {
		r := ok
		f(&r)
		return aptSource{Codename: "resolute", Arch: "amd64", Extra: []catalog.ExtraRepo{r}}
	}
	tests := []struct {
		name string
		src  aptSource
		ok   bool
	}{
		{"valid", aptSource{Codename: "resolute", Arch: "amd64", Extra: []catalog.ExtraRepo{ok}}, true},
		{"codename with newline", aptSource{Codename: "resolute\nTypes: deb-src", Arch: "amd64"}, false},
		{"codename uppercase", aptSource{Codename: "Resolute", Arch: "amd64"}, false},
		{"arch unknown", aptSource{Codename: "resolute", Arch: "riscv64"}, false},
		{"suite with newline", mod(func(r *catalog.ExtraRepo) { r.Suite = "x\nEvil: 1" }), false},
		{"suite with space", mod(func(r *catalog.ExtraRepo) { r.Suite = "a b" }), false},
		{"component with newline", mod(func(r *catalog.ExtraRepo) { r.Components = []string{"main\nX: y"} }), false},
		{"no components", mod(func(r *catalog.ExtraRepo) { r.Components = nil }), false},
		{"uri with space", mod(func(r *catalog.ExtraRepo) { r.URI = "https://a.example.test/x y" }), false},
		{"uri file scheme", mod(func(r *catalog.ExtraRepo) { r.URI = "file:///x" }), false},
		{"uri credentials", mod(func(r *catalog.ExtraRepo) { r.URI = "https://a:b" + at + "c.example.test/x" }), false},
		{"key not embedded", mod(func(r *catalog.ExtraRepo) { r.Key = "../../etc/shadow" }), false},
		{"name not a slug", mod(func(r *catalog.ExtraRepo) { r.Name = "../x" }), false},
		{"duplicate repo", aptSource{Codename: "resolute", Arch: "amd64", Extra: []catalog.ExtraRepo{ok, ok}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.src.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && CodeOf(err) != doctor.SYN060 {
				t.Errorf("code = %q", CodeOf(err))
			}
		})
	}
}

func TestAptOptionsGolden(t *testing.T) {
	got := strings.Join(aptOptions("/T", "amd64", "localai", ""), " ")
	want := "-o Dir=/T -o Dir::State::status=/T/var/lib/dpkg/status -o Dir::Etc::sourcelist=/dev/null " +
		"-o Dir::Etc::sourceparts=/T/etc/apt/sources.list.d -o Dir::Etc::trustedparts=/T/etc/apt/trusted.gpg.d " +
		"-o Dir::Etc::preferencesparts=/T/etc/apt/preferences.d -o APT::Architecture=amd64 -o APT::Architectures=amd64 " +
		"-o Debug::NoLocking=1 -o APT::Sandbox::User=localai -o Acquire::Languages=none -o APT::Install-Recommends=false " +
		"-o Acquire::ForceHash=SHA256 -o APT::Update::Error-Mode=any"
	if got != want {
		t.Errorf("options:\n%s\nwant:\n%s", got, want)
	}
	withProxy := strings.Join(aptOptions("/T", "arm64", "", "127.0.0.1:7"), " ")
	if !strings.HasSuffix(withProxy, "-o Acquire::https::Proxy=http://127.0.0.1:7") || strings.Contains(withProxy, "Sandbox") {
		t.Errorf("proxy options: %s", withProxy)
	}
}

const showGolden = `Package: libx
Version: 1.2-1
Architecture: amd64
Filename: pool/main/l/libx/libx_1.2-1_amd64.deb
Size: 1000
SHA256: %[1]s
Description: the x library
 With a long description,
 over three lines.
Description-md5:

Package: y
Version: 2:3.0-1
Architecture: amd64
Depends: libx (>= 1.0)
Filename: pool/main/y/y/y_3.0-1_amd64.deb
Size: 2000
MD5sum: 0123456789abcdef0123456789abcdef
Description-md5: abcdef

Package: y
Version: 2:3.0-1
Architecture: amd64
Filename: pool/universe/y/y/y_3.0-1_amd64.deb
Size: 2000
Description: a duplicate record of the same name and version
`

func TestBuildPackagesGolden(t *testing.T) {
	libxSum, ySum := sha256Hex([]byte("libx")), sha256Hex([]byte("y"))
	show := fmt.Sprintf(showGolden, libxSum)
	pkgs := []Pkg{
		{Name: "y", Version: "2:3.0-1", Arch: "amd64", Size: 2000, SHA256: ySum, PoolName: "y_3.0-1_amd64.deb"},
		{Name: "libx", Version: "1.2-1", Arch: "amd64", Size: 1000, SHA256: libxSum, PoolName: "libx_1.2-1_amd64.deb"},
	}
	got, err := buildPackages([]byte(show), pkgs)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`Package: libx
Version: 1.2-1
Architecture: amd64
Filename: pool/libx_1.2-1_amd64.deb
Size: 1000
SHA256: %[1]s
Description: the x library
 With a long description,
 over three lines.

Package: y
Version: 2:3.0-1
Architecture: amd64
Depends: libx (>= 1.0)
Filename: pool/y_3.0-1_amd64.deb
Size: 2000
MD5sum: 0123456789abcdef0123456789abcdef
SHA256: %[2]s

`, libxSum, ySum)
	if string(got) != want {
		t.Errorf("Packages:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(got), "Description-md5") {
		t.Error("Description-md5 was not dropped")
	}
}

func TestBuildPackagesErrors(t *testing.T) {
	sum := sha256Hex([]byte("a"))
	rec := "Package: a\nVersion: 1\nArchitecture: amd64\nFilename: pool/a.deb\nSize: 10\nSHA256: " + sum + "\n"
	pkg := Pkg{Name: "a", Version: "1", Size: 10, SHA256: sum, PoolName: "a_1_amd64.deb"}
	tests := []struct {
		name string
		show string
		pkg  Pkg
	}{
		{"no record", "Package: b\nVersion: 1\n", pkg},
		{"record has another sha256", rec, Pkg{Name: "a", Version: "1", Size: 10, SHA256: sha256Hex([]byte("other")), PoolName: "a_1_amd64.deb"}},
		{"record has another size", rec, Pkg{Name: "a", Version: "1", Size: 11, SHA256: sum, PoolName: "a_1_amd64.deb"}},
		{"continuation without a field", " orphan\n", pkg},
		{"line that is not a field", "this is not a field\n", pkg},
		{"nul byte", "Package: a\x00\n", pkg},
		{"field repeated", "Package: a\nPackage: a\nVersion: 1\n", pkg},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPackages([]byte(tc.show), []Pkg{tc.pkg})
			if CodeOf(err) != doctor.SYN060 {
				t.Fatalf("code = %q, want LAI-SYN-060 (err %v)", CodeOf(err), err)
			}
		})
	}
	// A record without Size or SHA256 gets them from the signed index.
	got, err := buildPackages([]byte("Package: a\nVersion: 1\nArchitecture: amd64\nFilename: x\n"), []Pkg{pkg})
	if err != nil || !strings.Contains(string(got), "Size: 10\n") || !strings.Contains(string(got), "SHA256: "+sum+"\n") {
		t.Fatalf("got (%q, %v)", got, err)
	}
}

func TestPackagesLockIsSortedAndParses(t *testing.T) {
	a, b := sha256Hex([]byte("a")), sha256Hex([]byte("b"))
	lock := packagesLock([]Pkg{{Name: "b", Version: "2", SHA256: b}, {Name: "a", Version: "1", SHA256: a}})
	if want := "a=1 " + a + "\nb=2 " + b + "\n"; string(lock) != want {
		t.Errorf("lock = %q, want %q", lock, want)
	}
	m := parsePackagesLock(lock)
	if m["a=1"] != a || m["b=2"] != b || len(m) != 2 {
		t.Errorf("parsed = %v", m)
	}
}

func TestGzipBytesHasNoNameOrTime(t *testing.T) {
	data := []byte("Package: a\n")
	gz, err := gzipBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	// RFC 1952: bytes 4-7 are the modification time, byte 3 the flags (no FNAME).
	if !bytes.Equal(gz[4:8], []byte{0, 0, 0, 0}) || gz[3]&0x08 != 0 {
		t.Errorf("header carries a time or a name: % x", gz[:10])
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	back, _ := io.ReadAll(zr)
	if !bytes.Equal(back, data) {
		t.Errorf("round trip = %q", back)
	}
	again, _ := gzipBytes(data)
	if !bytes.Equal(again, gz) {
		t.Error("two compressions of one input differ")
	}
}

func TestClipBoundsAndCleansMessages(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), 1<<20)
	if got := Clip(huge, 100); len([]rune(got)) > 103 {
		t.Errorf("clip left %d runes", len([]rune(got)))
	}
	if got := Clip([]byte("a\x1b[31mred\x00\n\n  b"), 50); strings.ContainsAny(got, "\x1b\x00\n") || got != "a [31mred b" {
		t.Errorf("clip = %q", got)
	}
	if got := Clip([]byte{0xff, 'a'}, 10); got != "?a" {
		t.Errorf("invalid utf-8: %q", got)
	}
}
