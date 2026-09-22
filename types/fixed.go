/*
FILE: types/fixed.go

DESCRIPTION:
Fixed is the SDK's hot-path numeric type: a signed fixed-point decimal with
8 fractional digits stored in an int64. It is used for prices and sizes in
order requests and in market-data streams, where shopspring/decimal (big.Int
based, allocates on every operation) is too expensive.

WHY 8 DIGITS:
Hyperliquid prices carry at most MAX_DECIMALS - szDecimals decimal places with
MAX_DECIMALS = 6 for perpetuals and 8 for spot; sizes carry at most szDecimals
places. The official Python SDK serialises every price/size through "%.8f".
8 fractional digits therefore represent every valid wire value exactly.

RANGE:
|value| < 92_233_720_368.54775807 (int64 / 1e8). Constructors return an error
instead of silently overflowing.

MAIN FUNCTIONS:
  - FixedFromFloat64 / FixedFromDecimal / ParseFixed / ParseFixedBytes : constructors.
  - (Fixed).AppendWire : zero-allocation wire formatting (no trailing zeros,
                         no exponent) — byte-identical to the Python SDK's
                         float_to_wire for every representable value.
  - (Fixed).Float64 / Decimal / String : conversions for non-hot paths.
  - MarshalJSON / UnmarshalJSON        : JSON string form used by the exchange.

DEPENDENCIES:
- github.com/shopspring/decimal: conversion to the type used by non-hot-path
  responses (account state, fills, funding).
*/

package types

import (
	"errors"
	"math"
	"strconv"

	"github.com/shopspring/decimal"
)

// Fixed — signed fixed-point decimal, 8 fractional digits, int64 storage.
type Fixed int64

const (
	// FixedDecimals — number of fractional digits carried by Fixed.
	FixedDecimals int = 8
	// FixedScale — 10^FixedDecimals.
	FixedScale int64 = 100_000_000
	// fixedMaxFloat — largest float64 magnitude accepted by FixedFromFloat64.
	// Kept below int64/1e8 so the multiplication cannot overflow.
	fixedMaxFloat float64 = 9.2e10
	// fixedMaxIntPart — largest integer part representable (int64 max / 1e8).
	fixedMaxIntPart int64 = 92_233_720_368
)

// Sentinel errors of the Fixed constructors.
var (
	// ErrFixedSyntax — the input is not a plain decimal number.
	ErrFixedSyntax error = errors.New("types: invalid fixed-point syntax")
	// ErrFixedRange — the value does not fit into Fixed.
	ErrFixedRange error = errors.New("types: fixed-point value out of range")
	// ErrFixedPrecision — the input has more than 8 significant fractional digits.
	ErrFixedPrecision error = errors.New("types: more than 8 fractional digits")
)

// FixedFromInt converts an integer number of whole units. The caller must keep
// |i| <= 92_233_720_368; larger values overflow.
func FixedFromInt(i int64) Fixed {
	return Fixed(i * FixedScale)
}

// FixedFromFloat64 converts a float64, rounding half away from zero to the
// nearest 1e-8. NaN, ±Inf and out-of-range values return an error.
func FixedFromFloat64(f float64) (Fixed, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, ErrFixedSyntax
	}
	if f >= fixedMaxFloat || f <= -fixedMaxFloat {
		return 0, ErrFixedRange
	}
	return Fixed(math.Round(f * float64(FixedScale))), nil
}

// FixedFromDecimal converts a decimal.Decimal. Values with more than 8
// fractional digits return ErrFixedPrecision (no silent rounding).
func FixedFromDecimal(d decimal.Decimal) (Fixed, error) {
	return ParseFixed(d.String())
}

// ParseFixed parses a plain decimal string ("86759.0", "0.00012", "-3").
// Exponent notation is not accepted: the exchange never sends it. STRICT: more
// than 8 significant fractional digits is an error — caller-supplied prices and
// sizes must never be rounded silently.
func ParseFixed(s string) (Fixed, error) {
	return parseFixed(s, true)
}

// parseFixed implements ParseFixed. With strict == false fractional digits
// beyond the 8th are truncated instead of rejected (see UnmarshalJSON).
func parseFixed(s string, strict bool) (Fixed, error) {
	var i int
	var n int = len(s)
	if n == 0 {
		return 0, ErrFixedSyntax
	}
	var negative bool
	if s[0] == '-' || s[0] == '+' {
		negative = s[0] == '-'
		i = 1
	}
	if i == n {
		return 0, ErrFixedSyntax
	}

	var intPart int64
	var sawDigit bool
	for ; i < n && s[i] != '.'; i++ {
		var c byte = s[i]
		if c < '0' || c > '9' {
			return 0, ErrFixedSyntax
		}
		sawDigit = true
		intPart = intPart*10 + int64(c-'0')
		if intPart > fixedMaxIntPart {
			return 0, ErrFixedRange
		}
	}

	var fracPart int64
	var fracDigits int
	if i < n {
		// s[i] == '.'
		i++
		for ; i < n; i++ {
			var c byte = s[i]
			if c < '0' || c > '9' {
				return 0, ErrFixedSyntax
			}
			sawDigit = true
			if fracDigits < FixedDecimals {
				fracPart = fracPart*10 + int64(c-'0')
				fracDigits++
				continue
			}
			if c != '0' && strict {
				return 0, ErrFixedPrecision
			}
		}
	}
	if !sawDigit {
		return 0, ErrFixedSyntax
	}
	for ; fracDigits < FixedDecimals; fracDigits++ {
		fracPart *= 10
	}

	if intPart == fixedMaxIntPart && fracPart > 54_775_807 {
		return 0, ErrFixedRange
	}
	var v int64 = intPart*FixedScale + fracPart
	if negative {
		v = -v
	}
	return Fixed(v), nil
}

// ParseFixedLenient parses a value RECEIVED from the exchange: like ParseFixed,
// but fractional digits beyond the 8th are truncated instead of rejected.
// Never use it for caller-supplied prices or sizes.
func ParseFixedLenient(s string) (Fixed, error) {
	return parseFixed(s, false)
}

// ParseFixedBytes — ParseFixed for a byte slice (WS payload fragments). Does
// not allocate: the conversion below is elided by the compiler because the
// string does not escape.
func ParseFixedBytes(b []byte) (Fixed, error) {
	return ParseFixed(string(b))
}

// MustParseFixed — ParseFixed that panics on error. For tests and constants only.
func MustParseFixed(s string) Fixed {
	var f Fixed
	var err error
	f, err = ParseFixed(s)
	if err != nil {
		panic("types.MustParseFixed(" + s + "): " + err.Error())
	}
	return f
}

// IsZero reports whether the value equals zero.
func (f Fixed) IsZero() bool { return f == 0 }

// Sign returns -1, 0 or +1.
func (f Fixed) Sign() int {
	switch {
	case f > 0:
		return 1
	case f < 0:
		return -1
	default:
		return 0
	}
}

// Abs returns the absolute value.
func (f Fixed) Abs() Fixed {
	if f < 0 {
		return -f
	}
	return f
}

// Neg returns the negated value.
func (f Fixed) Neg() Fixed { return -f }

// Float64 converts to float64 (lossy for magnitudes above 2^53 * 1e-8).
func (f Fixed) Float64() float64 {
	return float64(f) / float64(FixedScale)
}

// Decimal converts to decimal.Decimal exactly.
func (f Fixed) Decimal() decimal.Decimal {
	return decimal.New(int64(f), int32(-FixedDecimals))
}

/*
AppendWire appends the exchange wire form of the value to b and returns the
extended slice. The form has no trailing zeros, no trailing dot, no exponent
and renders zero as "0" — exactly what Hyperliquid requires for signing
("trailing zeroes" is the first item in the official list of signing mistakes).

Does not allocate when b has enough capacity (at most 21 bytes are appended).
*/
func (f Fixed) AppendWire(b []byte) []byte {
	var v int64 = int64(f)
	if v < 0 {
		b = append(b, '-')
		v = -v
	}
	var intPart int64 = v / FixedScale
	var fracPart int64 = v % FixedScale
	b = strconv.AppendInt(b, intPart, 10)
	if fracPart == 0 {
		return b
	}

	var digits [8]byte
	var i int
	for i = FixedDecimals - 1; i >= 0; i-- {
		digits[i] = byte('0' + fracPart%10)
		fracPart /= 10
	}
	var used int = FixedDecimals
	for used > 0 && digits[used-1] == '0' {
		used--
	}
	b = append(b, '.')
	return append(b, digits[:used]...)
}

// String returns the wire form as a string (allocates; not for hot paths).
func (f Fixed) String() string {
	var buf [24]byte
	return string(f.AppendWire(buf[:0]))
}

// MarshalJSON renders the value as a JSON string, the way the exchange does.
func (f Fixed) MarshalJSON() ([]byte, error) {
	var out []byte = make([]byte, 0, 24)
	out = append(out, '"')
	out = f.AppendWire(out)
	out = append(out, '"')
	return out, nil
}

/*
UnmarshalJSON accepts a JSON string ("123.45"), a bare JSON number or null
(null → 0: several exchange fields such as midPx are nullable).

LENIENT on precision: digits beyond the 8th decimal are TRUNCATED. Prices and
sizes never carry them, but derived statistics do (observed live:
"premium":"0.0004493591"), and one long statistic must not make the SDK drop a
whole market-data message. Values that can exceed the Fixed range (volumes,
open interest) are modelled as decimal.Decimal instead.
*/
func (f *Fixed) UnmarshalJSON(data []byte) error {
	var n int = len(data)
	if n == 0 {
		return ErrFixedSyntax
	}
	if n == 4 && data[0] == 'n' && data[1] == 'u' && data[2] == 'l' && data[3] == 'l' {
		*f = 0
		return nil
	}
	if data[0] == '"' {
		if n < 2 || data[n-1] != '"' {
			return ErrFixedSyntax
		}
		data = data[1 : n-1]
	}
	var parsed Fixed
	var err error
	parsed, err = parseFixed(string(data), false)
	if err != nil {
		return err
	}
	*f = parsed
	return nil
}
