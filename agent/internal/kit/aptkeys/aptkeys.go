// Package aptkeys embeds the PUBLIC apt signing keyrings of the third-party repositories the rebuild kit
// uses (Tailscale and the NVIDIA CUDA repository), each with a pinned OpenPGP fingerprint
// (docs/specs/rebuild-kit.md section 3.4, issue #274).
//
// A keyring is never fetched at run time. The bytes are embedded at build time, and Get refuses any keyring
// whose primary-key fingerprints are not exactly the pinned ones (ErrMismatch), so a corrupted binary or a
// keyring swapped in a fork is caught before apt sees it. The Ubuntu archive keyring is not here: it is read
// from the node (/usr/share/keyrings).
//
// Rotating a key means a new pin and a new file in one reviewed change (docs/runbooks/06-rebuild-a-node-offline.md,
// "Rotate a vendor apt keyring"). The fingerprints below are also written in that runbook and in the spec;
// the three must agree.
package aptkeys

import (
	"crypto/sha1" //nolint:gosec // OpenPGP v4 fingerprints are SHA-1 by definition; this is identification, not trust.
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.gpg
var files embed.FS

// Pin is one vendor keyring and the primary-key fingerprints it must contain, and nothing else.
type Pin struct {
	Name         string   // file name, as catalog extra_repos[].key names it
	Fingerprints []string // upper-case hex, 40 digits
	Vendor       string   // where the key was published, for the docs and error messages
}

// Pins is the table of embedded vendor keyrings. Each fingerprint was checked against three independent
// sources: the vendor's binary keyring, the vendor's armored key, and keyserver.ubuntu.com.
var Pins = []Pin{
	{
		Name:         "tailscale-archive-keyring.gpg",
		Fingerprints: []string{"2596A99EAAB33821893C0A79458CA832957F5868"},
		Vendor:       "https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg",
	},
	{
		Name:         "nvidia-cuda-archive-keyring.gpg",
		Fingerprints: []string{"EB693B3035CD5710E231E123A4B469963BF863CC"},
		Vendor:       "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/3bf863cc.pub",
	},
}

// ErrMismatch marks a keyring that is refused: unreadable, or not the pinned fingerprints. Callers must
// treat it as fatal, never as "no keyring available" (that would silently downgrade to an unverified
// repository).
var ErrMismatch = errors.New("keyring does not match its pinned fingerprint")

// Get returns the embedded keyring name after checking it against its pin. A name that is not a vendor
// keyring returns an error that is not ErrMismatch (the caller may fall back to another source).
func Get(name string) ([]byte, error) {
	return get(files, Pins, name)
}

// readFS is the part of embed.FS that get needs (tests pass a fixture).
type readFS interface {
	ReadFile(name string) ([]byte, error)
}

func get(fsys readFS, pins []Pin, name string) ([]byte, error) {
	for _, p := range pins {
		if p.Name != name {
			continue
		}
		data, err := fsys.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is not embedded: %v", ErrMismatch, name, err)
		}
		if err := Check(p, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("no embedded vendor keyring named %q", name)
}

// Check verifies that data is a binary OpenPGP keyring whose primary keys are exactly p.Fingerprints.
func Check(p Pin, data []byte) error {
	got, err := Fingerprints(data)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMismatch, p.Name, err)
	}
	want := append([]string(nil), p.Fingerprints...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("%w: %s has %s, the pin is %s", ErrMismatch, p.Name, strings.Join(got, ","), strings.Join(want, ","))
	}
	return nil
}

// Fingerprints returns the sorted v4 fingerprints of the primary public keys in a binary (not armored)
// OpenPGP keyring. Subkeys are ignored: they are covered by the primary key's signatures.
func Fingerprints(data []byte) ([]string, error) {
	var out []string
	for len(data) > 0 {
		tag, body, rest, err := nextPacket(data)
		if err != nil {
			return nil, err
		}
		data = rest
		if tag != 6 { // public key (primary); 14 is a subkey
			continue
		}
		if len(body) == 0 || body[0] != 4 {
			return nil, errors.New("only version 4 keys are supported")
		}
		h := sha1.New() //nolint:gosec // see import
		h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
		h.Write(body)
		out = append(out, strings.ToUpper(hex.EncodeToString(h.Sum(nil))))
	}
	if len(out) == 0 {
		return nil, errors.New("no public key found (is the file armored text?)")
	}
	sort.Strings(out)
	return out, nil
}

// nextPacket reads one OpenPGP packet (RFC 4880 section 4.2): new and old formats, definite lengths only.
func nextPacket(b []byte) (tag int, body, rest []byte, err error) {
	bad := errors.New("damaged OpenPGP data")
	if len(b) < 2 || b[0]&0x80 == 0 {
		return 0, nil, nil, bad
	}
	hdr := b[0]
	var n, off int
	if hdr&0x40 != 0 { // new format
		tag = int(hdr & 0x3f)
		l := int(b[1])
		switch {
		case l < 192:
			n, off = l, 2
		case l < 224:
			if len(b) < 3 {
				return 0, nil, nil, bad
			}
			n, off = (l-192)<<8+int(b[2])+192, 3
		case l == 255:
			if len(b) < 6 {
				return 0, nil, nil, bad
			}
			n, off = int(b[2])<<24|int(b[3])<<16|int(b[4])<<8|int(b[5]), 6
		default:
			return 0, nil, nil, errors.New("partial-length packets are not supported")
		}
	} else { // old format
		tag = int(hdr >> 2 & 0x0f)
		switch hdr & 3 {
		case 0:
			n, off = int(b[1]), 2
		case 1:
			if len(b) < 3 {
				return 0, nil, nil, bad
			}
			n, off = int(b[1])<<8|int(b[2]), 3
		case 2:
			if len(b) < 5 {
				return 0, nil, nil, bad
			}
			n, off = int(b[1])<<24|int(b[2])<<16|int(b[3])<<8|int(b[4]), 5
		default:
			return 0, nil, nil, errors.New("indefinite-length packets are not supported")
		}
	}
	if n < 0 || off+n > len(b) {
		return 0, nil, nil, bad
	}
	return tag, b[off : off+n], b[off+n:], nil
}
