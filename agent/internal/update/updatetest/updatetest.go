// Package updatetest holds the test doubles of self-update: a minisign signer with a key generated at run time
// (no key material is ever committed), a signed release, and a fake GitHub server that serves it over TLS.
// `aictl update` (AICTL-09) and `aictl verify-release` (AICTL-10) tests use the same doubles.
//
// The package does not import internal/update, so the tests of that package can use it. It is for tests only:
// no production code imports it.
package updatetest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/blake2b"
)

// Signer is a throwaway minisign key pair.
type Signer struct {
	ID   [8]byte
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// NewSigner generates a key pair with a random key id.
func NewSigner() *Signer {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s := &Signer{pub: pub, priv: priv}
	if _, err := rand.Read(s.ID[:]); err != nil {
		panic(err)
	}
	return s
}

// PublicKeyFile is the signer's minisign.pub.
func (s *Signer) PublicKeyFile() []byte {
	raw := append([]byte("Ed"), s.ID[:]...)
	raw = append(raw, s.pub...)
	return []byte("untrusted comment: minisign public key (test)\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

// SignOptions tunes Sign.
type SignOptions struct {
	// Legacy signs the file itself (algorithm "Ed"); the default is the prehashed "ED".
	Legacy bool
	// TrustedComment defaults to "timestamp:0 file:SHA256SUMS".
	TrustedComment string
}

// Sign returns a minisign signature file for msg.
func (s *Signer) Sign(msg []byte, o SignOptions) []byte {
	alg, signed := "ED", msg
	if o.Legacy {
		alg = "Ed"
	} else {
		h := blake2b.Sum512(msg)
		signed = h[:]
	}
	trusted := o.TrustedComment
	if trusted == "" {
		trusted = "timestamp:0 file:SHA256SUMS"
	}
	sig := ed25519.Sign(s.priv, signed)
	global := ed25519.Sign(s.priv, append(append([]byte(nil), sig...), trusted...))
	blob := append([]byte(alg), s.ID[:]...)
	blob = append(blob, sig...)
	var b bytes.Buffer
	b.WriteString("untrusted comment: signature from test key\n")
	b.WriteString(base64.StdEncoding.EncodeToString(blob) + "\n")
	b.WriteString("trusted comment: " + trusted + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return b.Bytes()
}

// SHA256Hex is the hex sha256 of data.
func SHA256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Sums renders a SHA256SUMS for the files, sorted by name, "<sha256>  <name>\n".
func Sums(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", SHA256Hex(files[n]), n)
	}
	return b.Bytes()
}

// Release is one GitHub release of the fake server.
type Release struct {
	// Version is the semver without the tag prefix ("0.2.0").
	Version    string
	Prerelease bool
	Draft      bool
	// Assets are the files of the release by name (the binaries, SHA256SUMS, SHA256SUMS.minisig, ...).
	Assets map[string][]byte
}

// Tag is node-v<version>.
func (r *Release) Tag() string { return "node-v" + r.Version }

// NewRelease builds a release whose SHA256SUMS lists the given files and is signed by s. The files are
// copied; change r.Assets afterwards to tamper with one.
func NewRelease(s *Signer, version string, files map[string][]byte) *Release {
	r := &Release{Version: version, Assets: map[string][]byte{}}
	for n, d := range files {
		r.Assets[n] = append([]byte(nil), d...)
	}
	sums := Sums(files)
	r.Assets["SHA256SUMS"] = sums
	r.Assets["SHA256SUMS.minisig"] = s.Sign(sums, SignOptions{})
	return r
}

// FakeAgentScript is the content of a stand-in agent binary: a shell script that answers
// `version --json` with the given version and arch and nothing else. It runs only where /bin/sh exists.
func FakeAgentScript(version, arch string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ] && [ \"$2\" = --json ]; then\n"+
		"echo '{\"version\":\"%s\",\"commit\":\"test\",\"os\":\"linux\",\"arch\":\"%s\",\"built_at\":\"unknown\"}'\n"+
		"exit 0\nfi\nexit 3\n", version, arch))
}

// Request is one request the server received.
type Request struct {
	Method, Path, Range string
}

// Server is a fake GitHub (API and downloads) over TLS. Use Client() as the HTTP client and URL as both the
// API base and the download base.
type Server struct {
	*httptest.Server
	Repo string

	mu       sync.Mutex
	releases []*Release
	reqs     []Request
	// Hook, when set, sees every request first; returning true means it answered.
	Hook func(w http.ResponseWriter, r *http.Request) bool
}

// NewServer starts the server for repo "owner/name" with the releases given.
func NewServer(repo string, releases ...*Release) *Server {
	s := &Server{Repo: repo, releases: releases}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	return s
}

// Add adds a release.
func (s *Server) Add(r *Release) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, r)
}

// Requests returns the requests seen so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

type jsonAsset struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

type jsonRelease struct {
	TagName    string      `json:"tag_name"`
	Prerelease bool        `json:"prerelease"`
	Draft      bool        `json:"draft"`
	Assets     []jsonAsset `json:"assets"`
}

func (r *Release) json() jsonRelease {
	j := jsonRelease{TagName: r.Tag(), Prerelease: r.Prerelease, Draft: r.Draft}
	names := make([]string, 0, len(r.Assets))
	for n := range r.Assets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		j.Assets = append(j.Assets, jsonAsset{Name: n, Size: len(r.Assets[n])})
	}
	return j
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{r.Method, r.URL.Path, r.Header.Get("Range")})
	hook := s.Hook
	releases := append([]*Release(nil), s.releases...)
	s.mu.Unlock()
	if hook != nil && hook(w, r) {
		return
	}
	api := "/repos/" + s.Repo + "/releases"
	switch {
	case r.URL.Path == api:
		var out []jsonRelease
		for _, rel := range releases {
			out = append(out, rel.json())
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(r.URL.Path, api+"/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, api+"/tags/")
		for _, rel := range releases {
			if rel.Tag() == tag {
				_ = json.NewEncoder(w).Encode(rel.json())
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(r.URL.Path, "/"+s.Repo+"/releases/download/"):
		rest := strings.TrimPrefix(r.URL.Path, "/"+s.Repo+"/releases/download/")
		tag, name, ok := strings.Cut(rest, "/")
		if ok {
			for _, rel := range releases {
				if rel.Tag() == tag {
					if data, found := rel.Assets[name]; found {
						http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
						return
					}
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}
