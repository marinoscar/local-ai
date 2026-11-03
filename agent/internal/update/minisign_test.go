package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/update/updatetest"
)

func mustKey(t *testing.T, s *updatetest.Signer) PublicKey {
	t.Helper()
	k, err := ParsePublicKey(s.PublicKeyFile())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestVerifyBothAlgorithms(t *testing.T) {
	s := updatetest.NewSigner()
	key := mustKey(t, s)
	msg := []byte("a signed file\n")
	for _, legacy := range []bool{false, true} {
		name := map[bool]string{false: "ED prehashed", true: "Ed legacy"}[legacy]
		t.Run(name, func(t *testing.T) {
			sig := s.Sign(msg, updatetest.SignOptions{Legacy: legacy, TrustedComment: "timestamp:1 file:SHA256SUMS"})
			got, err := Verify([]PublicKey{key}, sig, msg)
			if err != nil {
				t.Fatalf("a valid signature was refused: %v", err)
			}
			if got.Prehashed == legacy || got.TrustedComment != "timestamp:1 file:SHA256SUMS" || got.KeyID != key.ID {
				t.Fatalf("parsed signature is wrong: %+v", got)
			}
		})
	}
}

func TestVerifyRefusals(t *testing.T) {
	s := updatetest.NewSigner()
	other := updatetest.NewSigner()
	key := mustKey(t, s)
	msg := []byte("SHA256SUMS content\n")
	good := s.Sign(msg, updatetest.SignOptions{})

	// A copy of the signature with one line replaced.
	withLine := func(sig []byte, n int, text string) []byte {
		lines := strings.Split(strings.TrimRight(string(sig), "\n"), "\n")
		lines[n] = text
		return []byte(strings.Join(lines, "\n") + "\n")
	}
	flip := func(b64 string) string {
		raw, _ := base64.StdEncoding.DecodeString(b64)
		raw[len(raw)-1] ^= 1
		return base64.StdEncoding.EncodeToString(raw)
	}
	lines := strings.Split(strings.TrimRight(string(good), "\n"), "\n")

	cases := []struct {
		name string
		keys []PublicKey
		sig  []byte
		msg  []byte
		want error
	}{
		{"wrong key id", []PublicKey{mustKey(t, other)}, good, msg, ErrUnknownKey},
		{"no keys at all", nil, good, msg, ErrNoKeys},
		{"tampered file", []PublicKey{key}, good, append([]byte("x"), msg...), ErrBadSignature},
		{"tampered signature", []PublicKey{key}, withLine(good, 1, flip(lines[1])), msg, ErrBadSignature},
		{"trusted comment changed", []PublicKey{key}, withLine(good, 2, "trusted comment: timestamp:9 file:other"), msg, ErrBadSignature},
		{"global signature tampered", []PublicKey{key}, withLine(good, 3, flip(lines[3])), msg, ErrBadSignature},
		{"not a signature", []PublicKey{key}, []byte("hello"), msg, ErrMalformed},
		{"missing trusted comment", []PublicKey{key}, []byte(strings.Join(lines[:2], "\n") + "\n"), msg, ErrMalformed},
		{"oversized", []PublicKey{key}, bytes.Repeat([]byte("a"), maxSignatureFile+1), msg, ErrMalformed},
		{"unknown algorithm", []PublicKey{key}, withLine(good, 1, base64.StdEncoding.EncodeToString(append([]byte("XX"), make([]byte, 72)...))), msg, ErrMalformed},
		{"short signature blob", []PublicKey{key}, withLine(good, 1, "AAAA"), msg, ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Verify(c.keys, c.sig, c.msg)
			if !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

// A prehashed signature must not verify as a legacy one over the same bytes, and the other way round: the
// algorithm tag is part of what is checked, because it picks what is signed.
func TestVerifyAlgorithmConfusion(t *testing.T) {
	s := updatetest.NewSigner()
	key := mustKey(t, s)
	msg := []byte("file")
	sig := s.Sign(msg, updatetest.SignOptions{})
	lines := strings.Split(strings.TrimRight(string(sig), "\n"), "\n")
	raw, _ := base64.StdEncoding.DecodeString(lines[1])
	raw[1] = 'd' // "ED" -> "Ed"
	lines[1] = base64.StdEncoding.EncodeToString(raw)
	if _, err := Verify([]PublicKey{key}, []byte(strings.Join(lines, "\n")+"\n"), msg); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a prehashed signature verified as legacy: %v", err)
	}
}

func TestVerifyKeyRotation(t *testing.T) {
	oldKey, newKey := updatetest.NewSigner(), updatetest.NewSigner()
	both := []PublicKey{mustKey(t, oldKey), mustKey(t, newKey)}
	msg := []byte("sums")
	for name, s := range map[string]*updatetest.Signer{"old": oldKey, "new": newKey} {
		if _, err := Verify(both, s.Sign(msg, updatetest.SignOptions{}), msg); err != nil {
			t.Errorf("%s key: %v", name, err)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	s := updatetest.NewSigner()
	file := s.PublicKeyFile()
	k, err := ParsePublicKey(file)
	if err != nil || k.ID != s.ID {
		t.Fatalf("parse: %v", err)
	}
	// The key line alone is accepted too.
	if _, err := ParsePublicKey([]byte(strings.SplitN(string(file), "\n", 2)[1])); err != nil {
		t.Fatalf("bare key line: %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for name, bad := range map[string][]byte{
		"empty":           nil,
		"three lines":     []byte("untrusted comment: x\nAAAA\nAAAA\n"),
		"not base64":      []byte("untrusted comment: x\n!!!!\n"),
		"wrong algorithm": []byte("untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append([]byte("XX"), make([]byte, 40)...)) + "\n"),
		"secret key":      []byte("untrusted comment: minisign encrypted secret key\n" + base64.StdEncoding.EncodeToString(make([]byte, 100)) + "\n"),
		"nul":             append([]byte("untrusted comment: x\n"), 0),
		"short":           []byte("untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append([]byte("Ed"), pub[:10]...)) + "\n"),
	} {
		if _, err := ParsePublicKey(bad); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: want ErrMalformed, got %v", name, err)
		}
	}
	if got := KeyIDString([8]byte{1, 2, 3, 4, 5, 6, 7, 8}); got != "0807060504030201" {
		t.Fatalf("KeyIDString = %s", got)
	}
}

// With no key committed the embedded list is empty and every verification fails closed.
func TestEmbeddedKeysFailClosed(t *testing.T) {
	keys := EmbeddedKeys()
	s := updatetest.NewSigner()
	sig := s.Sign([]byte("x"), updatetest.SignOptions{})
	if len(keys) == 0 {
		if _, err := Verify(keys, sig, []byte("x")); !errors.Is(err, ErrNoKeys) {
			t.Fatalf("no embedded key must fail closed with ErrNoKeys, got %v", err)
		}
		return
	}
	// A key is committed: a throwaway signer must not verify against it.
	if _, err := Verify(keys, sig, []byte("x")); err == nil {
		t.Fatal("a throwaway key verified against the embedded keys")
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	ok := h + "  aiagent-linux-amd64\n" + strings.ToUpper(h) + " *install-node.sh\n"
	sums, err := ParseSums([]byte(ok))
	if err != nil || len(sums) != 2 || sums[1].Hash != h || sums[1].Name != "install-node.sh" {
		t.Fatalf("parse: %v %+v", err, sums)
	}
	if e, found := sums.Lookup("aiagent-linux-amd64"); !found || e.Hash != h {
		t.Fatal("lookup failed")
	}
	bad := map[string]string{
		"empty":            "",
		"no newline":       h + "  a",
		"short hash":       "abcd  a\n",
		"not hex":          strings.Repeat("zz", 32) + "  a\n",
		"one space":        h + " a\n",
		"path":             h + "  ../etc/passwd\n",
		"subdirectory":     h + "  dir/file\n",
		"backslash":        h + "  dir\\file\n",
		"dotdot":           h + "  ..\n",
		"empty name":       h + "  \n",
		"duplicate":        h + "  a\n" + h + "  a\n",
		"blank line":       h + "  a\n\n",
		"control char":     h + "  a\x01b\n",
		"nul":              h + "  a\x00\n",
		"text after names": h + "  a\nnot a line\n",
	}
	for name, in := range bad {
		if _, err := ParseSums([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: want ErrMalformed, got %v", name, err)
		}
	}
}

func writeRelease(t *testing.T, dir string, rel *updatetest.Release) {
	t.Helper()
	for name, data := range rel.Assets {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifyDir(t *testing.T) {
	s := updatetest.NewSigner()
	key := mustKey(t, s)
	rel := updatetest.NewRelease(s, "0.2.0", map[string][]byte{
		"aiagent-linux-amd64": []byte("one"), "install-node.sh": []byte("two"),
	})

	t.Run("all good", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		rep, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{})
		if err != nil || rep.Checked() != 2 || rep.KeyID != KeyIDString(s.ID) {
			t.Fatalf("report %+v err %v", rep, err)
		}
	})
	t.Run("tampered file", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		if err := os.WriteFile(filepath.Join(dir, "install-node.sh"), []byte("evil"), 0o644); err != nil {
			t.Fatal(err)
		}
		rep, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{})
		var me *MismatchError
		if !errors.As(err, &me) || len(me.Files) != 1 || me.Files[0] != "install-node.sh" || rep.Checked() != 1 {
			t.Fatalf("want a mismatch of install-node.sh, got %v (%+v)", err, rep)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		_ = os.Remove(filepath.Join(dir, "install-node.sh"))
		var me *MismatchError
		if _, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{}); !errors.As(err, &me) {
			t.Fatalf("a missing file must fail, got %v", err)
		}
		if _, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{IgnoreMissing: true}); err != nil {
			t.Fatalf("IgnoreMissing: %v", err)
		}
	})
	t.Run("bad signature", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		if _, err := VerifyDir(OSFS{}, dir, []PublicKey{mustKey(t, updatetest.NewSigner())}, DirOptions{}); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("want ErrUnknownKey, got %v", err)
		}
	})
	t.Run("tampered SHA256SUMS", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		// A line swapped to the hash of the tampered file: the signature no longer covers the file.
		forged := updatetest.Sums(map[string][]byte{"aiagent-linux-amd64": []byte("one"), "install-node.sh": []byte("evil")})
		if err := os.WriteFile(filepath.Join(dir, SumsName), forged, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{}); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("want ErrBadSignature, got %v", err)
		}
	})
	t.Run("no signature files", func(t *testing.T) {
		if _, err := VerifyDir(OSFS{}, t.TempDir(), []PublicKey{key}, DirOptions{}); !errors.Is(err, ErrNoSums) {
			t.Fatalf("want ErrNoSums, got %v", err)
		}
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		_ = os.Remove(filepath.Join(dir, SigName))
		if _, err := VerifyDir(OSFS{}, dir, []PublicKey{key}, DirOptions{}); !errors.Is(err, ErrNoSums) {
			t.Fatalf("want ErrNoSums without the signature, got %v", err)
		}
	})
	t.Run("no embedded key", func(t *testing.T) {
		dir := t.TempDir()
		writeRelease(t, dir, rel)
		if _, err := VerifyDir(OSFS{}, dir, nil, DirOptions{}); !errors.Is(err, ErrNoKeys) {
			t.Fatalf("want ErrNoKeys, got %v", err)
		}
	})
}
