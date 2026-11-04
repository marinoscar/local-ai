package aptkeys

import (
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // fixture fingerprint, same as production
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// fixtureKey builds a minimal binary keyring at test time: one v4 primary public-key packet with random
// key material (no key is committed), a user id and a subkey (which must be ignored). It returns the
// keyring and the fingerprint of the primary key.
func fixtureKey(t *testing.T, oldFormat bool) ([]byte, string) {
	t.Helper()
	body := make([]byte, 40)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	body[0], body[1], body[2], body[3], body[4], body[5] = 4, 0, 0, 0, 1, 22 // v4, time, algorithm
	h := sha1.Sum(append([]byte{0x99, 0, byte(len(body))}, body...))
	var out []byte
	if oldFormat {
		out = append(out, 0x80|6<<2, byte(len(body)))
	} else {
		out = append(out, 0xc0|6, byte(len(body)))
	}
	out = append(out, body...)
	out = append(out, 0xc0|13, 4, 'T', 'e', 's', 't') // user id
	out = append(out, 0xc0|14, byte(len(body)))       // subkey: ignored
	out = append(out, body...)
	return out, strings.ToUpper(hex.EncodeToString(h[:]))
}

type mapFS map[string][]byte

func (m mapFS) ReadFile(n string) ([]byte, error) {
	if b, ok := m[n]; ok {
		return b, nil
	}
	return nil, errors.New("not found")
}

func TestFingerprintsAndGet(t *testing.T) {
	good, fp := fixtureKey(t, false)
	goodOld, fpOld := fixtureKey(t, true)
	other, _ := fixtureKey(t, false)
	two := append(append([]byte(nil), good...), goodOld...)
	cases := []struct {
		name     string
		data     []byte
		pin      []string
		mismatch bool
	}{
		{"match", good, []string{fp}, false},
		{"match old packet format", goodOld, []string{fpOld}, false},
		{"different key", other, []string{fp}, true},
		{"extra primary key", two, []string{fp}, true},
		{"both pinned", two, []string{fp, fpOld}, false},
		{"armored text", []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n"), []string{fp}, true},
		{"truncated", good[:10], []string{fp}, true},
		{"empty", nil, []string{fp}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pins := []Pin{{Name: "v.gpg", Fingerprints: c.pin}}
			b, err := get(mapFS{"v.gpg": c.data}, pins, "v.gpg")
			if c.mismatch {
				if !errors.Is(err, ErrMismatch) || b != nil {
					t.Fatalf("want ErrMismatch, got %v", err)
				}
				return
			}
			if err != nil || len(b) != len(c.data) {
				t.Fatalf("want ok, got %v", err)
			}
		})
	}
}

func TestGetUnknownAndMissing(t *testing.T) {
	if _, err := get(mapFS{}, nil, "ubuntu-archive-keyring.gpg"); err == nil || errors.Is(err, ErrMismatch) {
		t.Fatalf("unknown name must be a plain error, got %v", err)
	}
	if _, err := get(mapFS{}, []Pin{{Name: "v.gpg", Fingerprints: []string{"AA"}}}, "v.gpg"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("missing embedded file must be refused, got %v", err)
	}
}

// The real embedded files must match their pins.
func TestEmbeddedKeyringsMatchPins(t *testing.T) {
	if len(Pins) != 2 {
		t.Fatalf("pins: %d", len(Pins))
	}
	for _, p := range Pins {
		b, err := Get(p.Name)
		if err != nil || len(b) == 0 {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
}
