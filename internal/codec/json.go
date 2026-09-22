/*
FILE: internal/codec/json.go

DESCRIPTION:
codec/json.go is the single JSON parser entry point in the SDK. All calls
inside the SDK go through here so that switching from json-iterator to any
other parser (e.g. bytedance/sonic) requires only one change if benchmarks
show an advantage. encoding/json is not used directly anywhere in the SDK.

MAIN FUNCTIONS:
  - Marshal/Unmarshal — wrappers over the selected parser (json-iterator,
                        standard-library-compatible settings + CaseSensitive
                        field matching, see the `json` var below).
  - NewDecoder        — wrapper for streaming parsing (REST response body).
  - ParseDecimal      — string → decimal.Decimal. Empty string → decimal.Zero
                        (Hyperliquid sends numeric fields as strings and uses
                        null / omits optional ones).
  - ParseInt64        — string → int64. Empty string → 0.
  - ParseFloat64      — string → float64. Empty string → 0.

WHY CASE-SENSITIVE:
Hyperliquid wire structs use short keys that differ only by case in several
payloads: candles carry "t"/"T" (open / close time), "o"/"c"/"h"/"l", WsFill
and order wires mix "s"/"S". With case-insensitive matching (the jsoniter
default, inherited from encoding/json) both keys resolve to ONE struct field.

DEPENDENCIES:
- github.com/json-iterator/go: API identical to encoding/json.
- github.com/shopspring/decimal: target type for non-hot-path numerics.
*/

package codec

import (
	"io"
	"strconv"

	jsoniter "github.com/json-iterator/go"
	"github.com/shopspring/decimal"
)

// json — reusable parser instance. Same settings as
// ConfigCompatibleWithStandardLibrary (exact float64 handling, sorted map
// keys, validated RawMessage) PLUS CaseSensitive field matching.
var json = jsoniter.Config{
	EscapeHTML:             true,
	SortMapKeys:            true,
	ValidateJsonRawMessage: true,
	CaseSensitive:          true,
}.Froze()

// RawMessage — analogue of json.RawMessage that works correctly with jsoniter.
// Used as a field in envelope structs to avoid parsing data twice:
// first the envelope with RawMessage, then the payload into its typed destination.
type RawMessage []byte

// MarshalJSON implements json.Marshaler.
func (m RawMessage) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte("null"), nil
	}
	return []byte(m), nil
}

// UnmarshalJSON implements json.Unmarshaler. Copies the payload into the
// receiving slice to avoid dangling references to the gorilla/websocket
// read buffer.
func (m *RawMessage) UnmarshalJSON(data []byte) error {
	*m = append((*m)[:0], data...)
	return nil
}

// Marshal serializes a value to JSON.
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal parses JSON into a value.
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// NewDecoder creates a decoder for streaming parsing of a reader.
func NewDecoder(r io.Reader) *jsoniter.Decoder {
	return json.NewDecoder(r)
}

// GetString extracts a top-level (or nested, via path) string field without
// decoding the whole document. Returns "" when the path is absent. Used by
// the WS router to read the routing key ("coin", "user", ...) of a push.
func GetString(data []byte, path ...any) string {
	return json.Get(data, path...).ToString()
}

// ParseDecimal converts a string to decimal.Decimal. Empty string → Zero.
func ParseDecimal(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

// MustParseDecimal — like ParseDecimal, but on error returns Zero and sets
// the flag in ok (if not nil). Used in parsers where we do NOT want to abort
// the entire decode due to a single malformed field.
func MustParseDecimal(s string, ok *bool) decimal.Decimal {
	if s == "" {
		if ok != nil {
			*ok = true
		}
		return decimal.Zero
	}
	var d decimal.Decimal
	var err error
	d, err = decimal.NewFromString(s)
	if ok != nil {
		*ok = err == nil
	}
	if err != nil {
		return decimal.Zero
	}
	return d
}

// ParseInt64 converts a string to int64. Empty string → 0.
func ParseInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// ParseFloat64 converts a string to float64. Empty string → 0.
func ParseFloat64(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
