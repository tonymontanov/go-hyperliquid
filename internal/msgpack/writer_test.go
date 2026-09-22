/*
FILE: internal/msgpack/writer_test.go

DESCRIPTION:
Boundary tests of the MessagePack writer. Expected bytes follow the MessagePack
specification and were cross-checked with msgpack-python (msgpack.packb), the
encoder used by the official Hyperliquid SDK: shortest integer form, unsigned
family for non-negative integers, str family for text.
*/

package msgpack

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestAppendUintBoundaries(t *testing.T) {
	var cases = []struct {
		in   uint64
		want string
	}{
		{0, "00"},
		{127, "7f"},
		{128, "cc80"},
		{255, "ccff"},
		{256, "cd0100"},
		{65535, "cdffff"},
		{65536, "ce00010000"},
		{4294967295, "ceffffffff"},
		{4294967296, "cf0000000100000000"},
		{100000000000, "cf000000174876e800"},
		{18446744073709551615, "cfffffffffffffffff"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(AppendUint(nil, c.in)); got != c.want {
			t.Errorf("AppendUint(%d) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestAppendIntBoundaries(t *testing.T) {
	var cases = []struct {
		in   int64
		want string
	}{
		{5, "05"},
		{-1, "ff"},
		{-32, "e0"},
		{-33, "d0df"},
		{-128, "d080"},
		{-129, "d1ff7f"},
		{-32768, "d18000"},
		{-32769, "d2ffff7fff"},
		{-2500000, "d2ffd9da60"},
		{-2147483648, "d280000000"},
		{-2147483649, "d3ffffffff7fffffff"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(AppendInt(nil, c.in)); got != c.want {
			t.Errorf("AppendInt(%d) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestAppendStringBoundaries(t *testing.T) {
	var cases = []struct {
		length int
		prefix string
	}{
		{0, "a0"},
		{31, "bf"},
		{32, "d920"},
		{255, "d9ff"},
		{256, "da0100"},
		{65535, "daffff"},
		{65536, "db00010000"},
	}
	for _, c := range cases {
		var s = strings.Repeat("x", c.length)
		var got = AppendString(nil, s)
		if !strings.HasPrefix(hex.EncodeToString(got), c.prefix) || len(got) != len(c.prefix)/2+c.length {
			t.Errorf("AppendString(len=%d): bad header %s", c.length, hex.EncodeToString(got[:5]))
		}
		var fromBytes = AppendStringBytes(nil, []byte(s))
		if string(fromBytes) != string(got) {
			t.Errorf("AppendStringBytes(len=%d) differs from AppendString", c.length)
		}
	}
}

func TestContainersAndScalars(t *testing.T) {
	var cases = []struct {
		got  []byte
		want string
	}{
		{AppendMapHeader(nil, 3), "83"},
		{AppendMapHeader(nil, 15), "8f"},
		{AppendMapHeader(nil, 16), "de0010"},
		{AppendMapHeader(nil, 65536), "df00010000"},
		{AppendArrayHeader(nil, 1), "91"},
		{AppendArrayHeader(nil, 15), "9f"},
		{AppendArrayHeader(nil, 16), "dc0010"},
		{AppendArrayHeader(nil, 20), "dc0014"},
		{AppendArrayHeader(nil, 65536), "dd00010000"},
		{AppendBool(nil, true), "c3"},
		{AppendBool(nil, false), "c2"},
		{AppendNil(nil), "c0"},
	}
	for i, c := range cases {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("case %d: got %s, want %s", i, got, c.want)
		}
	}
}
