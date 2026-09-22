/*
FILE: internal/signing/signer.go

DESCRIPTION:
Signer holds the secp256k1 private key of the signing wallet (normally an API
wallet / agent, optionally the master wallet) and produces the {r, s, v}
signatures Hyperliquid expects.

SECURITY NOTES:
  - The key never leaves this package: there is no getter, String() is
    redacted, and no error produced here embeds key material.
  - The hex input buffer is wiped after parsing; Close() zeroes the key.
  - A Signer created from an empty key is DISABLED: public endpoints keep
    working and every signing call returns ErrSignerDisabled.

WHY decred/secp256k1 (pure Go):
The trading core is built with CGO_ENABLED=0, which rules out libsecp256k1
bindings (go-ethereum's crypto package, ~11.5 µs/sign on Apple M4 Pro, also
forces a newer Go toolchain). decred's implementation is pure Go, constant
time, uses RFC 6979 deterministic nonces — which makes signatures
byte-identical to eth_account, the library behind the official Python SDK.
The SDK drives decred's exported primitives through its own allocation-free
signing routine (rfc6979.go) instead of ecdsa.SignCompact: same bytes, 12
allocations instead of 29 (the rest are inside decred's math/big inverse).
See BenchmarkSignDigest / BenchmarkSignCompactDecred.

MAIN FUNCTIONS:
  - NewSigner(privateKeyHex)     : constructor; "" → disabled signer.
  - (Signer).SignDigest          : raw 32-byte digest → Signature.
  - (Signer).SignL1Action        : packed action → Signature (scheme 1).
  - (Signer).SignUserSigned      : typed fields → Signature (scheme 2).
  - (Signature).AppendJSON       : {"r":"0x..","s":"0x..","v":27} writer.

DEPENDENCIES:
- github.com/decred/dcrd/dcrec/secp256k1/v4 (+ /ecdsa): keys and signing.
*/

package signing

import (
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Sentinel errors of the signer.
var (
	// ErrSignerDisabled — a signing call was made on a Signer without a key.
	ErrSignerDisabled error = errors.New("signing: signer is disabled (no private key configured)")
	// ErrInvalidPrivateKey — the key is not 32 bytes of hex or is outside the
	// valid scalar range. The message intentionally carries no key material.
	ErrInvalidPrivateKey error = errors.New("signing: invalid private key")
)

// privateKeyLength — secp256k1 private key length in bytes.
const privateKeyLength int = 32

// Signature — ECDSA signature in the form the exchange expects.
type Signature struct {
	R [32]byte
	S [32]byte
	// V — recovery id + 27 (27 or 28).
	V byte
}

// Signer — holder of the signing key. Safe for concurrent use: signing does
// not mutate the key.
type Signer struct {
	privateKey *secp256k1.PrivateKey
	address    Address
	enabled    bool
}

// NewSigner parses a hex private key (optional "0x" prefix). An empty string
// yields a disabled signer and a nil error.
func NewSigner(privateKeyHex string) (*Signer, error) {
	if privateKeyHex == "" {
		return &Signer{}, nil
	}
	var s string = privateKeyHex
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	if len(s) != privateKeyLength*2 {
		return nil, ErrInvalidPrivateKey
	}

	var raw [privateKeyLength]byte
	var i int
	for i = 0; i < privateKeyLength; i++ {
		var hi int = fromHexChar(s[2*i])
		var lo int = fromHexChar(s[2*i+1])
		if hi < 0 || lo < 0 {
			wipe(raw[:])
			return nil, ErrInvalidPrivateKey
		}
		raw[i] = byte(hi<<4 | lo)
	}

	var scalar secp256k1.ModNScalar
	var overflow bool = scalar.SetByteSlice(raw[:])
	if overflow || scalar.IsZero() {
		scalar.Zero()
		wipe(raw[:])
		return nil, ErrInvalidPrivateKey
	}
	scalar.Zero()

	var privateKey *secp256k1.PrivateKey = secp256k1.PrivKeyFromBytes(raw[:])
	wipe(raw[:])

	return &Signer{
		privateKey: privateKey,
		address:    addressFromPublicKey(privateKey.PubKey()),
		enabled:    true,
	}, nil
}

// wipe overwrites b with zeros.
func wipe(b []byte) {
	var i int
	for i = 0; i < len(b); i++ {
		b[i] = 0
	}
}

// Enabled reports whether the signer holds a key.
func (s *Signer) Enabled() bool {
	return s != nil && s.enabled
}

// Address returns the address derived from the key (zero when disabled).
func (s *Signer) Address() Address {
	if s == nil {
		return Address{}
	}
	return s.address
}

// String returns a redacted description. Never prints key material.
func (s *Signer) String() string {
	if !s.Enabled() {
		return "Signer{disabled}"
	}
	return "Signer{address=" + s.address.Hex() + ", key=<redacted>}"
}

// Close zeroes the private key. The signer becomes disabled.
func (s *Signer) Close() {
	if s == nil || s.privateKey == nil {
		return
	}
	s.privateKey.Zero()
	s.enabled = false
}

// SignDigest signs a 32-byte digest. Deterministic (RFC 6979), low-S.
func (s *Signer) SignDigest(digest *[32]byte) (Signature, error) {
	var out Signature
	if !s.Enabled() {
		return out, ErrSignerDisabled
	}
	// Byte-identical, low-allocation equivalent of ecdsa.SignCompact — see rfc6979.go.
	signDigest(s.privateKey, digest, &out)
	return out, nil
}

/*
SignL1Action signs an L1 action given its MessagePack form.

packed is extended in place with the hashing trailer; the extended slice is
returned so pooled buffers keep their grown capacity. See ActionHash for the
meaning of vault / expiresAfter.
*/
func (s *Signer) SignL1Action(packed []byte, nonce uint64, vault *Address, expiresAfter uint64, hasExpiresAfter bool, isMainnet bool) (Signature, []byte, error) {
	if !s.Enabled() {
		return Signature{}, packed, ErrSignerDisabled
	}
	var connectionID [32]byte
	var digest [32]byte
	packed = ActionHash(&connectionID, packed, nonce, vault, expiresAfter, hasExpiresAfter)
	AgentDigest(&digest, &connectionID, isMainnet)
	var sig Signature
	var err error
	sig, err = s.SignDigest(&digest)
	return sig, packed, err
}

// SignUserSigned signs a user-signed action (see UserSignedDigest).
func (s *Signer) SignUserSigned(primaryType string, fields []TypedField, signatureChainID uint64) (Signature, error) {
	if !s.Enabled() {
		return Signature{}, ErrSignerDisabled
	}
	var digest [32]byte
	UserSignedDigest(&digest, primaryType, fields, signatureChainID)
	return s.SignDigest(&digest)
}

// appendMinimalHex appends "0x" + hex of word without leading zero nibbles —
// the form produced by eth_utils.to_hex(int) in the official Python SDK.
func appendMinimalHex(b []byte, word *[32]byte) []byte {
	b = append(b, '0', 'x')
	var started bool
	var i int
	for i = 0; i < len(word); i++ {
		var hi byte = word[i] >> 4
		var lo byte = word[i] & 0x0f
		if started || hi != 0 {
			b = append(b, hexDigits[hi])
			started = true
		}
		if started || lo != 0 {
			b = append(b, hexDigits[lo])
			started = true
		}
	}
	if !started {
		b = append(b, '0')
	}
	return b
}

// AppendJSON appends {"r":"0x..","s":"0x..","v":27} to b. Zero allocations
// when b has capacity (at most 150 bytes are appended).
func (sig *Signature) AppendJSON(b []byte) []byte {
	b = append(b, `{"r":"`...)
	b = appendMinimalHex(b, &sig.R)
	b = append(b, `","s":"`...)
	b = appendMinimalHex(b, &sig.S)
	b = append(b, `","v":`...)
	b = append(b, '0'+sig.V/10, '0'+sig.V%10)
	b = append(b, '}')
	return b
}

// RHex returns r in the minimal-hex wire form (tests, diagnostics).
func (sig *Signature) RHex() string {
	var buf [66]byte
	return string(appendMinimalHex(buf[:0], &sig.R))
}

// SHex returns s in the minimal-hex wire form (tests, diagnostics).
func (sig *Signature) SHex() string {
	var buf [66]byte
	return string(appendMinimalHex(buf[:0], &sig.S))
}
