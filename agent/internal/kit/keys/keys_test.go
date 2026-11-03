package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// parseMinisignPub checks the minisign public key file format: an "untrusted comment:" line, then the base64 of
// 'E','d', an 8-byte key id and the 32-byte Ed25519 public key. It returns the key id and the public key.
func parseMinisignPub(data []byte) (id [8]byte, pub ed25519.PublicKey, err error) {
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "untrusted comment:") {
		return id, nil, errors.New("want an untrusted comment line and one key line")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil {
		return id, nil, err
	}
	if len(raw) != 2+8+ed25519.PublicKeySize || raw[0] != 'E' || raw[1] != 'd' {
		return id, nil, errors.New("not a minisign Ed25519 public key")
	}
	copy(id[:], raw[2:10])
	return id, ed25519.PublicKey(raw[10:]), nil
}

func encodePub(id [8]byte, pub ed25519.PublicKey) []byte {
	raw := append([]byte("Ed"), id[:]...)
	raw = append(raw, pub...)
	return []byte("untrusted comment: minisign public key test\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

func TestParseMinisignPub(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	good := encodePub(id, pub)

	gotID, gotPub, err := parseMinisignPub(good)
	if err != nil || gotID != id || !pub.Equal(gotPub) {
		t.Fatalf("round trip failed: id=%v err=%v", gotID, err)
	}
	for name, bad := range map[string][]byte{
		"empty":             nil,
		"no comment":        []byte(strings.SplitN(string(good), "\n", 2)[1]),
		"truncated key":     []byte("untrusted comment: x\nRWQAAAAA\n"),
		"not base64":        []byte("untrusted comment: x\n!!!!\n"),
		"wrong algorithm":   []byte("untrusted comment: x\n" + base64.StdEncoding.EncodeToString(append([]byte("XX"), make([]byte, 40)...)) + "\n"),
		"secret key header": []byte("untrusted comment: minisign encrypted secret key\n" + base64.StdEncoding.EncodeToString(make([]byte, 100)) + "\n"),
	} {
		if _, _, err := parseMinisignPub(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// TestCommittedKeyParses covers the committed minisign.pub. The file is the owner's to create
// (docs/architecture/release-signing.md section 3), so until it exists the test skips; the release workflow
// itself refuses to run without it.
func TestCommittedKeyParses(t *testing.T) {
	data, err := os.ReadFile("minisign.pub")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("minisign.pub is not committed yet (owner action: docs/architecture/release-signing.md section 3)")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseMinisignPub(data); err != nil {
		t.Fatalf("minisign.pub does not parse: %v", err)
	}
	if strings.Contains(string(data), "secret key") {
		t.Fatal("minisign.pub must hold the public key only")
	}
}
