/*
FILE: internal/signing/address.go

DESCRIPTION:
20-byte account address helpers. Hyperliquid requires every address to be
LOWER-CASE both in the signed payload and on the wire ("uppercase addresses"
is one of the documented signing mistakes), so the SDK parses addresses into a
binary Address once and always renders the lower-case hex form.

MAIN FUNCTIONS:
  - ParseAddress(s)         : "0x" + 40 hex chars (any case) → Address.
  - (Address).Hex()         : lower-case "0x..." string.
  - (Address).AppendHex(b)  : zero-allocation variant for JSON/msgpack writers.
  - addressFromPublicKey    : keccak256(uncompressed pubkey[1:])[12:].
*/

package signing

import (
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// AddressLength — length of an account address in bytes.
const AddressLength int = 20

// Address — 20-byte account / wallet / vault address.
type Address [AddressLength]byte

// ErrInvalidAddress — the string is not "0x" followed by 40 hex characters.
var ErrInvalidAddress error = errors.New("signing: invalid address")

// hexDigits — lower-case hex alphabet.
const hexDigits string = "0123456789abcdef"

// ParseAddress parses "0x"-prefixed (prefix optional) 40-char hex of any case.
func ParseAddress(s string) (Address, error) {
	var out Address
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	if len(s) != AddressLength*2 {
		return out, ErrInvalidAddress
	}
	var i int
	for i = 0; i < AddressLength; i++ {
		var hi int = fromHexChar(s[2*i])
		var lo int = fromHexChar(s[2*i+1])
		if hi < 0 || lo < 0 {
			return Address{}, ErrInvalidAddress
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, nil
}

// fromHexChar returns the value of a hex digit or -1.
func fromHexChar(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

// IsZero reports whether the address is all zeros (unset).
func (a Address) IsZero() bool {
	return a == Address{}
}

// AppendHex appends the lower-case "0x..." form to b.
func (a Address) AppendHex(b []byte) []byte {
	b = append(b, '0', 'x')
	var i int
	for i = 0; i < AddressLength; i++ {
		b = append(b, hexDigits[a[i]>>4], hexDigits[a[i]&0x0f])
	}
	return b
}

// Hex returns the lower-case "0x..." form.
func (a Address) Hex() string {
	var buf [2 + AddressLength*2]byte
	return string(a.AppendHex(buf[:0]))
}

// addressFromPublicKey derives the Ethereum-style address of a public key:
// the last 20 bytes of keccak256 over the 64-byte uncompressed point.
func addressFromPublicKey(pub *secp256k1.PublicKey) Address {
	var uncompressed []byte = pub.SerializeUncompressed()
	var digest [32]byte
	Keccak256(&digest, uncompressed[1:])
	var out Address
	copy(out[:], digest[12:])
	return out
}
