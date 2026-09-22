/*
FILE: types/cloid.go

DESCRIPTION:
Cloid — Hyperliquid client order id: an optional 128-bit value written as
"0x" + 32 hex characters (e.g. 0x1234567890abcdef1234567890abcdef).

A UUID is exactly 128 bits, so desks that identify orders by UUID v4 map them
to cloids losslessly in both directions (CloidFromUUID / (Cloid).UUID).

The type is a small value (no pointers): "absent" is represented by the zero
Cloid, which keeps order requests allocation-free.

MAIN FUNCTIONS:
  - ParseCloid(s)      : "0x" + 32 hex → Cloid.
  - CloidFromUUID(s)   : canonical 36-char UUID → Cloid.
  - CloidFromBytes(b)  : raw 16 bytes → Cloid.
  - (Cloid).AppendHex  : zero-allocation wire form.
*/

package types

import "errors"

// CloidLength — cloid length in bytes.
const CloidLength int = 16

// ErrInvalidCloid — the input is not a valid cloid / UUID.
var ErrInvalidCloid error = errors.New("types: invalid cloid")

// cloidHexDigits — lower-case hex alphabet.
const cloidHexDigits string = "0123456789abcdef"

// Cloid — optional 128-bit client order id. The zero value means "not set".
type Cloid struct {
	raw [CloidLength]byte
	set bool
}

// CloidFromBytes wraps 16 raw bytes.
func CloidFromBytes(b [CloidLength]byte) Cloid {
	return Cloid{raw: b, set: true}
}

// cloidHexValue returns the value of a hex digit or -1.
func cloidHexValue(c byte) int {
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

// ParseCloid parses "0x" + 32 hex characters (any case).
func ParseCloid(s string) (Cloid, error) {
	if len(s) != 2+CloidLength*2 || s[0] != '0' || (s[1] != 'x' && s[1] != 'X') {
		return Cloid{}, ErrInvalidCloid
	}
	var out Cloid
	var i int
	for i = 0; i < CloidLength; i++ {
		var hi int = cloidHexValue(s[2+2*i])
		var lo int = cloidHexValue(s[3+2*i])
		if hi < 0 || lo < 0 {
			return Cloid{}, ErrInvalidCloid
		}
		out.raw[i] = byte(hi<<4 | lo)
	}
	out.set = true
	return out, nil
}

// CloidFromUUID converts a canonical UUID string (8-4-4-4-12, any case).
func CloidFromUUID(s string) (Cloid, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return Cloid{}, ErrInvalidCloid
	}
	var out Cloid
	var i int
	var pos int
	for i = 0; i < CloidLength; i++ {
		if pos == 8 || pos == 13 || pos == 18 || pos == 23 {
			pos++
		}
		var hi int = cloidHexValue(s[pos])
		var lo int = cloidHexValue(s[pos+1])
		if hi < 0 || lo < 0 {
			return Cloid{}, ErrInvalidCloid
		}
		out.raw[i] = byte(hi<<4 | lo)
		pos += 2
	}
	out.set = true
	return out, nil
}

// IsSet reports whether the cloid is present.
func (c Cloid) IsSet() bool { return c.set }

// Bytes returns the raw 16 bytes.
func (c Cloid) Bytes() [CloidLength]byte { return c.raw }

// AppendHex appends the wire form ("0x" + 32 lower-case hex) to b.
func (c Cloid) AppendHex(b []byte) []byte {
	b = append(b, '0', 'x')
	var i int
	for i = 0; i < CloidLength; i++ {
		b = append(b, cloidHexDigits[c.raw[i]>>4], cloidHexDigits[c.raw[i]&0x0f])
	}
	return b
}

// String returns the wire form, or "" when the cloid is not set.
func (c Cloid) String() string {
	if !c.set {
		return ""
	}
	var buf [2 + CloidLength*2]byte
	return string(c.AppendHex(buf[:0]))
}

// UUID returns the canonical UUID form of the same 128 bits, or "" when not set.
func (c Cloid) UUID() string {
	if !c.set {
		return ""
	}
	var buf [36]byte
	var pos int
	var i int
	for i = 0; i < CloidLength; i++ {
		if pos == 8 || pos == 13 || pos == 18 || pos == 23 {
			buf[pos] = '-'
			pos++
		}
		buf[pos] = cloidHexDigits[c.raw[i]>>4]
		buf[pos+1] = cloidHexDigits[c.raw[i]&0x0f]
		pos += 2
	}
	return string(buf[:])
}

// MarshalJSON renders the wire form as a JSON string, or null when not set.
func (c Cloid) MarshalJSON() ([]byte, error) {
	if !c.set {
		return []byte("null"), nil
	}
	var out []byte = make([]byte, 0, 4+CloidLength*2)
	out = append(out, '"')
	out = c.AppendHex(out)
	out = append(out, '"')
	return out, nil
}

// UnmarshalJSON accepts a JSON string or null.
func (c *Cloid) UnmarshalJSON(data []byte) error {
	if len(data) == 4 && string(data) == "null" {
		*c = Cloid{}
		return nil
	}
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return ErrInvalidCloid
	}
	var parsed Cloid
	var err error
	parsed, err = ParseCloid(string(data[1 : len(data)-1]))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
