package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Minisign verification (agent.md section 4.10 step 3). The format is minisign's own:
//
//	untrusted comment: <free text>
//	base64( alg(2) || key id(8) || signature(64) )        alg "Ed": over the file; "ED": over BLAKE2b-512(file)
//	trusted comment: <free text>
//	base64( global signature(64) )                        over signature(64) || trusted comment
//
// The public key file is
//
//	untrusted comment: <free text>
//	base64( "Ed" || key id(8) || public key(32) )
//
// Only crypto/ed25519 and BLAKE2b-512 are used. Nothing here trusts a byte of the signature file before the
// signature over it has verified: the key id picks a key from the embedded list, and an unknown id is refused
// without any further work.

const (
	algLegacy    = "Ed"
	algPrehashed = "ED"

	untrustedPrefix = "untrusted comment:"
	trustedPrefix   = "trusted comment: "

	// maxSignatureFile bounds a signature file; a real one is about 300 bytes.
	maxSignatureFile = 4096
)

// Errors of the verifier. Every one of them is a failed verification: callers map any error of Verify to
// LAI-AGT-010, and use errors.Is to say why.
var (
	// ErrMalformed is a signature or key file that does not have the minisign format.
	ErrMalformed = errors.New("update: not a minisign file")
	// ErrNoKeys is a verification with no trusted key at all (no key is embedded yet).
	ErrNoKeys = errors.New("update: no trusted release key is embedded in this agent")
	// ErrUnknownKey is a signature made with a key whose id is not in the trusted list.
	ErrUnknownKey = errors.New("update: the signature was made with an unknown key")
	// ErrBadSignature is a signature (or global signature) that does not verify.
	ErrBadSignature = errors.New("update: the signature does not match")
)

// PublicKey is one trusted minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// KeyIDString is the id as minisign prints it: the eight bytes in reverse order, upper-case hex.
func KeyIDString(id [8]byte) string {
	var r [8]byte
	for i := range id {
		r[7-i] = id[i]
	}
	return strings.ToUpper(hex.EncodeToString(r[:]))
}

// String is the key id of k.
func (k PublicKey) String() string { return KeyIDString(k.ID) }

// ParsePublicKey parses a minisign public key file: the comment line and the key line, or the key line alone.
func ParsePublicKey(data []byte) (PublicKey, error) {
	lines := splitLines(data)
	var b64 string
	switch {
	case len(lines) == 2 && strings.HasPrefix(lines[0], untrustedPrefix):
		b64 = lines[1]
	case len(lines) == 1:
		b64 = lines[0]
	default:
		return PublicKey{}, fmt.Errorf("%w: want a comment line and one key line", ErrMalformed)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != algLegacy {
		return PublicKey{}, fmt.Errorf("%w: not an Ed25519 public key", ErrMalformed)
	}
	var k PublicKey
	copy(k.ID[:], raw[2:10])
	k.Key = ed25519.PublicKey(append([]byte(nil), raw[10:]...))
	return k, nil
}

// Signature is a parsed minisign signature file. Parsing proves nothing; Verify does.
type Signature struct {
	// Prehashed is true for algorithm "ED" (BLAKE2b-512 of the file), false for the legacy "Ed".
	Prehashed bool
	KeyID     [8]byte
	Sig       [ed25519.SignatureSize]byte
	// UntrustedComment is free text and is never used for a decision.
	UntrustedComment string
	// TrustedComment is covered by the global signature (the text after "trusted comment: ").
	TrustedComment string
	GlobalSig      [ed25519.SignatureSize]byte
}

// ParseSignature parses a minisign signature file (four lines).
func ParseSignature(data []byte) (Signature, error) {
	var s Signature
	if len(data) > maxSignatureFile {
		return s, fmt.Errorf("%w: too large", ErrMalformed)
	}
	lines := splitLines(data)
	if len(lines) != 4 || !strings.HasPrefix(lines[0], untrustedPrefix) || !strings.HasPrefix(lines[2], trustedPrefix) {
		return s, fmt.Errorf("%w: want four lines (comment, signature, trusted comment, global signature)", ErrMalformed)
	}
	s.UntrustedComment = strings.TrimSpace(strings.TrimPrefix(lines[0], untrustedPrefix))
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return s, fmt.Errorf("%w: bad signature line", ErrMalformed)
	}
	switch string(raw[:2]) {
	case algPrehashed:
		s.Prehashed = true
	case algLegacy:
	default:
		return s, fmt.Errorf("%w: unknown algorithm", ErrMalformed)
	}
	copy(s.KeyID[:], raw[2:10])
	copy(s.Sig[:], raw[10:])
	s.TrustedComment = strings.TrimPrefix(lines[2], trustedPrefix)
	g, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(g) != ed25519.SignatureSize {
		return s, fmt.Errorf("%w: bad global signature line", ErrMalformed)
	}
	copy(s.GlobalSig[:], g)
	return s, nil
}

// Verify checks that sigFile is a valid minisign signature of msg made by one of keys: the key id must be in
// the list, the signature must verify over msg (over its BLAKE2b-512 hash for "ED"), and the global
// signature must verify over signature || trusted comment, so the comment cannot be edited. It returns the
// parsed signature. It is the single verifier of the agent: self-update, `aictl verify-release` and the kit
// verification all call it.
func Verify(keys []PublicKey, sigFile, msg []byte) (Signature, error) {
	sig, err := ParseSignature(sigFile)
	if err != nil {
		return sig, err
	}
	if len(keys) == 0 {
		return sig, ErrNoKeys
	}
	signed := msg
	if sig.Prehashed {
		h := blake2b.Sum512(msg)
		signed = h[:]
	}
	global := make([]byte, 0, ed25519.SignatureSize+len(sig.TrustedComment))
	global = append(global, sig.Sig[:]...)
	global = append(global, sig.TrustedComment...)

	known := false
	for _, k := range keys {
		if len(k.Key) != ed25519.PublicKeySize || subtle.ConstantTimeCompare(k.ID[:], sig.KeyID[:]) != 1 {
			continue
		}
		known = true
		if ed25519.Verify(k.Key, signed, sig.Sig[:]) && ed25519.Verify(k.Key, global, sig.GlobalSig[:]) {
			return sig, nil
		}
	}
	if !known {
		return sig, fmt.Errorf("%w (key id %s)", ErrUnknownKey, KeyIDString(sig.KeyID))
	}
	return sig, ErrBadSignature
}

// splitLines splits on LF, tolerates CRLF and one trailing newline, and refuses a NUL.
func splitLines(data []byte) []string {
	if bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}
