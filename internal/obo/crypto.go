package obo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// Cipher seals/opens the two kinds of OBO secret: the self-contained PKCE state
// token (carried opaquely in the authorize URL) and the at-rest token /
// client-secret columns. Both use AES-256-GCM under subkeys derived from one
// master via HKDF-SHA256, so the state key and the column key are
// domain-separated — compromising one context doesn't yield the other.
type Cipher struct {
	state  cipher.AEAD
	column cipher.AEAD
}

// cipherVersion is a leading byte on every blob so a future key rotation can
// distinguish ciphertexts encrypted under different master keys.
const cipherVersion = 0x01

// NewCipher derives the state + column AEADs from a master key. The master is
// any high-entropy secret (≥16 bytes) — e.g. a dedicated ARTI_OBO_ENC_KEY, or
// arti's JWT signing key as a fallback.
func NewCipher(master []byte) (*Cipher, error) {
	if len(master) < 16 {
		return nil, fmt.Errorf("obo: master key too short (%d bytes; need ≥16)", len(master))
	}
	s, err := newGCM(deriveKey(master, "obo:state:v1"))
	if err != nil {
		return nil, err
	}
	c, err := newGCM(deriveKey(master, "obo:column:v1"))
	if err != nil {
		return nil, err
	}
	return &Cipher{state: s, column: c}, nil
}

func deriveKey(master []byte, label string) []byte {
	r := hkdf.New(sha256.New, master, nil, []byte(label))
	k := make([]byte, 32) // AES-256
	_, _ = io.ReadFull(r, k)
	return k
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// seal returns version || nonce || AES-GCM(plaintext). open is its inverse.
func seal(a cipher.AEAD, plaintext []byte) []byte {
	nonce := make([]byte, a.NonceSize())
	_, _ = rand.Read(nonce)
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+a.Overhead())
	out = append(out, cipherVersion)
	out = append(out, nonce...)
	return a.Seal(out, nonce, plaintext, nil)
}

func open(a cipher.AEAD, blob []byte) ([]byte, error) {
	ns := a.NonceSize()
	if len(blob) < 1+ns || blob[0] != cipherVersion {
		return nil, errors.New("obo: malformed ciphertext")
	}
	return a.Open(nil, blob[1:1+ns], blob[1+ns:], nil)
}

// SealState encodes + encrypts the pending-authorization payload into an opaque,
// URL-safe token. The authorization server treats it as opaque and echoes it
// back; OpenState recovers it on any pod. Encryption (not just a signature) is
// required because the PKCE verifier rides inside and must stay confidential.
func (c *Cipher) SealState(s authState) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(seal(c.state, b)), nil
}

// OpenState decrypts + decodes a state token. A tampered or wrong-key token
// fails the GCM auth check here.
func (c *Cipher) OpenState(tok string) (authState, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return authState{}, err
	}
	pt, err := open(c.state, raw)
	if err != nil {
		return authState{}, err
	}
	var s authState
	if err := json.Unmarshal(pt, &s); err != nil {
		return authState{}, err
	}
	return s, nil
}

// SealCol / OpenCol seal a secret column value (token, client secret) for at-rest
// storage. Returns bytea-shaped bytes; an empty plaintext seals to nil (NULL).
func (c *Cipher) SealCol(plaintext string) []byte {
	if plaintext == "" {
		return nil
	}
	return seal(c.column, []byte(plaintext))
}

func (c *Cipher) OpenCol(blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	pt, err := open(c.column, blob)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
