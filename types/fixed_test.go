/*
FILE: types/fixed_test.go

DESCRIPTION:
Unit tests and benchmarks for Fixed and Precision. The wire-format cases mirror
the behaviour of float_to_wire in the official Python SDK; the rounding cases
come from the official "Tick and lot size" page and from examples/rounding.py.
*/

package types

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestParseFixedAndWire(t *testing.T) {
	var cases = []struct {
		in   string
		raw  int64
		wire string
	}{
		{"0", 0, "0"},
		{"-0", 0, "0"},
		{"100", 100 * FixedScale, "100"},
		{"100.0", 100 * FixedScale, "100"},
		{"1670.1", 167_010_000_000, "1670.1"},
		{"0.0147", 1_470_000, "0.0147"},
		{"86759.0", 8_675_900_000_000, "86759"},
		{"0.00000001", 1, "0.00000001"},
		{"-3.5", -350_000_000, "-3.5"},
		{"+2", 2 * FixedScale, "2"},
		{".5", 50_000_000, "0.5"},
		{"5.", 5 * FixedScale, "5"},
		{"1.123456780000", 112_345_678, "1.12345678"},
		{"92233720368.54775807", 9_223_372_036_854_775_807, "92233720368.54775807"},
	}
	for _, c := range cases {
		var got, err = ParseFixed(c.in)
		if err != nil {
			t.Errorf("ParseFixed(%q): unexpected error %v", c.in, err)
			continue
		}
		if int64(got) != c.raw {
			t.Errorf("ParseFixed(%q) = %d, want %d", c.in, int64(got), c.raw)
		}
		if got.String() != c.wire {
			t.Errorf("ParseFixed(%q).String() = %q, want %q", c.in, got.String(), c.wire)
		}
	}
}

func TestParseFixedErrors(t *testing.T) {
	var cases = []struct {
		in   string
		want error
	}{
		{"", ErrFixedSyntax},
		{"-", ErrFixedSyntax},
		{".", ErrFixedSyntax},
		{"1e5", ErrFixedSyntax},
		{"12a", ErrFixedSyntax},
		{"1.2.3", ErrFixedSyntax},
		{"0.000000001", ErrFixedPrecision},
		{"92233720369", ErrFixedRange},
		{"92233720368.54775808", ErrFixedRange},
	}
	for _, c := range cases {
		var _, err = ParseFixed(c.in)
		if !errors.Is(err, c.want) {
			t.Errorf("ParseFixed(%q) error = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestFixedFromFloat64(t *testing.T) {
	var cases = []struct {
		in   float64
		wire string
	}{
		{0.1 + 0.2, "0.3"},
		{1670.1, "1670.1"},
		{0.0147, "0.0147"},
		{123123123.123, "123123123.123"},
		{-1.5, "-1.5"},
		{1e-9, "0"},
	}
	for _, c := range cases {
		var got, err = FixedFromFloat64(c.in)
		if err != nil {
			t.Errorf("FixedFromFloat64(%v): %v", c.in, err)
			continue
		}
		if got.String() != c.wire {
			t.Errorf("FixedFromFloat64(%v) = %q, want %q", c.in, got.String(), c.wire)
		}
	}
	if _, err := FixedFromFloat64(1e11); !errors.Is(err, ErrFixedRange) {
		t.Errorf("FixedFromFloat64(1e11) error = %v, want ErrFixedRange", err)
	}
}

func TestFixedDecimalRoundTrip(t *testing.T) {
	var d = decimal.RequireFromString("1234.5678")
	var f, err = FixedFromDecimal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Decimal().Equal(d) {
		t.Fatalf("round trip: got %s want %s", f.Decimal(), d)
	}
	if f.Float64() != 1234.5678 {
		t.Fatalf("Float64 = %v", f.Float64())
	}
}

func TestFixedJSON(t *testing.T) {
	var f Fixed
	for _, in := range []string{`"86759.0"`, `86759`, `null`} {
		if err := f.UnmarshalJSON([]byte(in)); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", in, err)
		}
	}
	// Exchange statistics carry more than 8 decimals (observed live:
	// "premium":"0.0004493591"): JSON decoding truncates, ParseFixed rejects.
	if err := f.UnmarshalJSON([]byte(`"0.0004493591"`)); err != nil || f.String() != "0.00044935" {
		t.Fatalf("lenient JSON decode: %v %s", err, f)
	}
	if _, err := ParseFixed("0.0004493591"); !errors.Is(err, ErrFixedPrecision) {
		t.Fatalf("ParseFixed must stay strict: %v", err)
	}
	f = MustParseFixed("0.5")
	var out, _ = f.MarshalJSON()
	if string(out) != `"0.5"` {
		t.Fatalf("MarshalJSON = %s", out)
	}
}

func TestPricePrecision(t *testing.T) {
	// name, szDecimals, maxDecimals, input, valid, nearest, down, up
	var cases = []struct {
		name               string
		szDecimals, maxDec int
		in                 string
		valid              bool
		nearest, down, up  string
	}{
		// Official page: 1234.5 is valid, 1234.56 is not (perps).
		{"5 sig figs ok", 1, 6, "1234.5", true, "1234.5", "1234.5", "1234.5"},
		{"5 sig figs violated", 1, 6, "1234.56", false, "1234.6", "1234.5", "1234.6"},
		// Official page: 0.001234 valid, 0.0012345 not (more than 6 decimals), szDecimals 0.
		{"max decimals ok", 0, 6, "0.001234", true, "0.001234", "0.001234", "0.001234"},
		{"max decimals violated", 0, 6, "0.0012345", false, "0.001235", "0.001234", "0.001235"},
		// Integer prices are always allowed.
		{"integer beyond 5 sig figs", 5, 6, "123456", true, "123456", "123456", "123456"},
		{"non-integer beyond 5 sig figs", 5, 6, "12345.6", false, "12346", "12345", "12346"},
		// examples/rounding.py: px 1.2345678, OP-like szDecimals 1 → round(1.2346, 5) = 1.2346.
		{"rounding.py", 1, 6, "1.2345678", false, "1.2346", "1.2345", "1.2346"},
		// szDecimals cap dominates: BTC szDecimals 5 → 1 decimal.
		{"sz cap", 5, 6, "86759.12", false, "86759", "86759", "86760"},
		// Spot: 8 - szDecimals decimals. Formula wins over the ambiguous doc wording.
		{"spot ok", 2, 8, "0.0001234", false, "0.000123", "0.000123", "0.000124"},
		{"spot small sz", 0, 8, "0.0001234", true, "0.0001234", "0.0001234", "0.0001234"},
		// Carry into the next power of ten stays valid.
		{"carry", 0, 6, "9.99996", false, "10", "9.9999", "10"},
		{"carry big", 0, 6, "99999.6", false, "100000", "99999", "100000"},
	}
	for _, c := range cases {
		var p = Precision{SzDecimals: c.szDecimals, MaxDecimals: c.maxDec}
		var px = MustParseFixed(c.in)
		if p.ValidatePrice(px) != c.valid {
			t.Errorf("%s: ValidatePrice(%s) = %v, want %v", c.name, c.in, !c.valid, c.valid)
		}
		var modes = []struct {
			mode RoundingMode
			want string
		}{{RoundNearest, c.nearest}, {RoundDown, c.down}, {RoundUp, c.up}}
		for _, m := range modes {
			var got = p.NormalizePrice(px, m.mode)
			if got.String() != m.want {
				t.Errorf("%s: NormalizePrice(%s, %d) = %s, want %s", c.name, c.in, m.mode, got, m.want)
			}
			if !p.ValidatePrice(got) {
				t.Errorf("%s: normalized price %s is not valid", c.name, got)
			}
		}
	}
	var p = Precision{SzDecimals: 0, MaxDecimals: 6}
	if p.ValidatePrice(0) || p.ValidatePrice(-1) {
		t.Error("non-positive prices must be invalid")
	}
}

func TestSizePrecision(t *testing.T) {
	var p = Precision{SzDecimals: 1, MaxDecimals: 6}
	var sz = MustParseFixed("12.345678")
	if p.ValidateSize(sz) {
		t.Error("12.345678 must be invalid for szDecimals 1")
	}
	if got := p.NormalizeSize(sz, RoundDown).String(); got != "12.3" {
		t.Errorf("NormalizeSize down = %s", got)
	}
	if got := p.NormalizeSize(sz, RoundNearest).String(); got != "12.3" {
		t.Errorf("NormalizeSize nearest = %s", got)
	}
	if got := p.NormalizeSize(sz, RoundUp).String(); got != "12.4" {
		t.Errorf("NormalizeSize up = %s", got)
	}
	if !p.ValidateSize(MustParseFixed("12.3")) {
		t.Error("12.3 must be valid")
	}
	var whole = Precision{SzDecimals: 0, MaxDecimals: 6}
	if got := whole.NormalizeSize(MustParseFixed("7.9"), RoundDown).String(); got != "7" {
		t.Errorf("szDecimals 0: got %s", got)
	}
}

func TestMulFixed(t *testing.T) {
	var got, ok = MulFixed(MustParseFixed("86759"), MustParseFixed("0.00012"))
	if !ok || got.String() != "10.41108" {
		t.Fatalf("MulFixed = %s ok=%v", got, ok)
	}
	got, ok = MulFixed(MustParseFixed("-2"), MustParseFixed("3.5"))
	if !ok || got.String() != "-7" {
		t.Fatalf("MulFixed negative = %s ok=%v", got, ok)
	}
	_, ok = MulFixed(MustParseFixed("90000000000"), MustParseFixed("90000000000"))
	if ok {
		t.Fatal("MulFixed must report overflow")
	}
}

func BenchmarkFixedAppendWire(b *testing.B) {
	var px = MustParseFixed("86759.5")
	var buf = make([]byte, 0, 32)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = px.AppendWire(buf[:0])
	}
}

func BenchmarkParseFixedBytes(b *testing.B) {
	var in = []byte("86759.12345")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var _, _ = ParseFixedBytes(in)
	}
}

func BenchmarkFixedFromFloat64Normalize(b *testing.B) {
	var p = Precision{SzDecimals: 5, MaxDecimals: 6}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var px, _ = FixedFromFloat64(86759.123456)
		_ = p.NormalizePrice(px, RoundNearest)
	}
}
