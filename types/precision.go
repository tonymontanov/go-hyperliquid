/*
FILE: types/precision.go

DESCRIPTION:
Price and size rounding rules of Hyperliquid, implemented on top of Fixed in
pure int64 arithmetic (no allocations, no floating point). This is the common
layer: a section only supplies its two parameters (szDecimals of the asset and
the section's MAX_DECIMALS).

EXCHANGE RULES (official "Tick and lot size" page):
  - Prices can have up to 5 significant figures, but no more than
    MAX_DECIMALS - szDecimals decimal places, where MAX_DECIMALS is 6 for
    perpetuals and 8 for spot.
  - Integer prices are always allowed, regardless of significant figures
    (123456 is valid, 12345.6 is not).
  - Sizes are rounded to szDecimals decimal places.

ALGORITHM (price):
Let v be the raw Fixed value (price * 1e8) and nd its number of decimal digits.
The magnitude of the price is nd - 8, so 5 significant figures allow
13 - nd decimal places. The allowed number of decimals is
d = clamp(min(13 - nd, MAX_DECIMALS - szDecimals), 0, 8) and a price is valid
iff v is a multiple of 10^(8-d). Clamping at 0 encodes the integer-price rule.

MAIN FUNCTIONS:
  - (Precision).NormalizePrice / ValidatePrice / TickSize / PriceDecimals
  - (Precision).NormalizeSize / ValidateSize / LotSize
  - MulFixed : overflow-safe Fixed multiplication (notional = px * sz).
*/

package types

import "math/bits"

// RoundingMode — direction used when a value has to be snapped to a grid.
type RoundingMode uint8

const (
	// RoundNearest — to the nearest grid point, ties away from zero.
	RoundNearest RoundingMode = iota
	// RoundDown — toward zero (floor for positive values). Use for bid prices
	// and for sizes.
	RoundDown
	// RoundUp — away from zero (ceil for positive values). Use for ask prices.
	RoundUp
)

const (
	// MaxSignificantFigures — maximum significant figures of a non-integer price.
	MaxSignificantFigures int = 5
	// MinOrderValue — minimum order notional in quote units. Documented by the
	// exchange only through its error text ("Order must have minimum value of
	// $10." / "... of 10 {quote_token}."); the SDK exposes the constant but
	// does not enforce it, because the exchange exempts some order kinds.
	MinOrderValue Fixed = Fixed(10 * FixedScale)
)

// pow10 — powers of ten up to 10^8.
var pow10 [9]int64 = [9]int64{1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000, 100_000_000}

// Precision — rounding parameters of one asset inside one section.
type Precision struct {
	// SzDecimals — size decimals of the asset (meta.universe[i].szDecimals for
	// perpetuals, szDecimals of the BASE token for spot).
	SzDecimals int
	// MaxDecimals — MAX_DECIMALS of the section: 6 for perpetuals, 8 for spot.
	MaxDecimals int
}

// countDigits returns the number of decimal digits of v (v > 0).
func countDigits(v int64) int {
	var n int
	for v > 0 {
		n++
		v /= 10
	}
	return n
}

// clampDecimals clamps d into [0, FixedDecimals].
func clampDecimals(d int) int {
	if d < 0 {
		return 0
	}
	if d > FixedDecimals {
		return FixedDecimals
	}
	return d
}

// roundToStep snaps a non-negative raw value to a multiple of step.
func roundToStep(v int64, step int64, mode RoundingMode) int64 {
	var rem int64 = v % step
	if rem == 0 {
		return v
	}
	var down int64 = v - rem
	switch mode {
	case RoundDown:
		return down
	case RoundUp:
		return down + step
	default:
		if rem*2 >= step {
			return down + step
		}
		return down
	}
}

// PriceDecimals returns how many decimal places a price of this magnitude may
// carry. The result depends on the price itself (5 significant figures rule).
func (p Precision) PriceDecimals(px Fixed) int {
	var v int64 = int64(px.Abs())
	if v == 0 {
		return clampDecimals(p.MaxDecimals - p.SzDecimals)
	}
	var bySignificant int = MaxSignificantFigures + FixedDecimals - countDigits(v)
	var byDecimals int = p.MaxDecimals - p.SzDecimals
	if byDecimals < bySignificant {
		return clampDecimals(byDecimals)
	}
	return clampDecimals(bySignificant)
}

// TickSize returns the price grid step valid around px. Hyperliquid has no
// static tick size: the step changes when the price crosses a power of ten.
func (p Precision) TickSize(px Fixed) Fixed {
	return Fixed(pow10[FixedDecimals-p.PriceDecimals(px)])
}

// ValidatePrice reports whether px can be sent to the exchange as is.
// Zero and negative prices are invalid.
func (p Precision) ValidatePrice(px Fixed) bool {
	if px <= 0 {
		return false
	}
	return int64(px)%int64(p.TickSize(px)) == 0
}

/*
NormalizePrice snaps px to the nearest valid price in the given direction.

Rounding may carry into the next power of ten (9.99996 → 10); the result is
then a round number, which is valid on the coarser grid as well, so a single
pass is always sufficient. Non-positive input is returned unchanged.
*/
func (p Precision) NormalizePrice(px Fixed, mode RoundingMode) Fixed {
	if px <= 0 {
		return px
	}
	return Fixed(roundToStep(int64(px), int64(p.TickSize(px)), mode))
}

// LotSize returns the size grid step (10^-szDecimals).
func (p Precision) LotSize() Fixed {
	return Fixed(pow10[FixedDecimals-clampDecimals(p.SzDecimals)])
}

// ValidateSize reports whether sz can be sent to the exchange as is.
// Zero and negative sizes are invalid.
func (p Precision) ValidateSize(sz Fixed) bool {
	if sz <= 0 {
		return false
	}
	return int64(sz)%int64(p.LotSize()) == 0
}

// NormalizeSize snaps sz to the size grid. RoundDown is the safe default for
// sizes (never exceeds the requested quantity). Non-positive input is
// returned unchanged.
func (p Precision) NormalizeSize(sz Fixed, mode RoundingMode) Fixed {
	if sz <= 0 {
		return sz
	}
	return Fixed(roundToStep(int64(sz), int64(p.LotSize()), mode))
}

/*
MulFixed multiplies two Fixed values with a 128-bit intermediate, truncating
the result toward zero to 8 fractional digits. ok is false on overflow.

Typical use: notional = MulFixed(px, sz) compared against MinOrderValue.
*/
func MulFixed(a Fixed, b Fixed) (Fixed, bool) {
	var negative bool = (a < 0) != (b < 0)
	var hi uint64
	var lo uint64
	hi, lo = bits.Mul64(uint64(a.Abs()), uint64(b.Abs()))
	if hi >= uint64(FixedScale) {
		return 0, false
	}
	var quotient uint64
	quotient, _ = bits.Div64(hi, lo, uint64(FixedScale))
	if quotient > uint64(1<<63-1) {
		return 0, false
	}
	var result Fixed = Fixed(int64(quotient))
	if negative {
		result = -result
	}
	return result, true
}
