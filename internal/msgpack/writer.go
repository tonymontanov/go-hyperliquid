/*
FILE: internal/msgpack/writer.go

DESCRIPTION:
Minimal append-style MessagePack writer. Hyperliquid L1 actions are hashed over
their MessagePack form, so the encoding is part of the signing hot path.

WHY NOT A GENERAL-PURPOSE LIBRARY:
 1. Field ORDER is part of the signature: the exchange re-serialises the action
    it received and recovers the signer from the hash. A reflection-based
    encoder ties the order to struct layout / map iteration; explicit Append*
    calls make the order visible and reviewable next to the JSON writer.
 2. Reflection allocates. These functions only append to a caller-owned buffer.
 3. Byte-for-byte parity with the official Python SDK (msgpack.packb) is
    required: every integer must use the SHORTEST encoding and positive
    integers must use the unsigned family, exactly like msgpack-python.

SUPPORTED SUBSET:
nil, bool, integers, strings, array and map headers. Floats and binary are
intentionally absent — no Hyperliquid action uses them (all decimals travel as
strings).

MAIN FUNCTIONS:
  - AppendMapHeader / AppendArrayHeader : container headers (fix / 16 / 32).
  - AppendString                        : fixstr / str8 / str16 / str32.
  - AppendUint / AppendInt              : shortest-form integers.
  - AppendBool / AppendNil.
*/

package msgpack

// AppendNil appends nil (Python None).
func AppendNil(b []byte) []byte {
	return append(b, 0xc0)
}

// AppendBool appends a boolean.
func AppendBool(b []byte, v bool) []byte {
	if v {
		return append(b, 0xc3)
	}
	return append(b, 0xc2)
}

// AppendMapHeader appends a map header for n key-value pairs.
func AppendMapHeader(b []byte, n int) []byte {
	switch {
	case n <= 15:
		return append(b, 0x80|byte(n))
	case n <= 0xffff:
		return append(b, 0xde, byte(n>>8), byte(n))
	default:
		return append(b, 0xdf, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

// AppendArrayHeader appends an array header for n elements.
func AppendArrayHeader(b []byte, n int) []byte {
	switch {
	case n <= 15:
		return append(b, 0x90|byte(n))
	case n <= 0xffff:
		return append(b, 0xdc, byte(n>>8), byte(n))
	default:
		return append(b, 0xdd, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

// AppendString appends a UTF-8 string (str family, as msgpack-python does with
// use_bin_type=True).
func AppendString(b []byte, s string) []byte {
	var n int = len(s)
	switch {
	case n <= 31:
		b = append(b, 0xa0|byte(n))
	case n <= 0xff:
		b = append(b, 0xd9, byte(n))
	case n <= 0xffff:
		b = append(b, 0xda, byte(n>>8), byte(n))
	default:
		b = append(b, 0xdb, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(b, s...)
}

// AppendStringBytes — AppendString for a byte slice holding UTF-8 text.
func AppendStringBytes(b []byte, s []byte) []byte {
	var n int = len(s)
	switch {
	case n <= 31:
		b = append(b, 0xa0|byte(n))
	case n <= 0xff:
		b = append(b, 0xd9, byte(n))
	case n <= 0xffff:
		b = append(b, 0xda, byte(n>>8), byte(n))
	default:
		b = append(b, 0xdb, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(b, s...)
}

// AppendUint appends a non-negative integer in its shortest unsigned form.
func AppendUint(b []byte, v uint64) []byte {
	switch {
	case v <= 0x7f:
		return append(b, byte(v))
	case v <= 0xff:
		return append(b, 0xcc, byte(v))
	case v <= 0xffff:
		return append(b, 0xcd, byte(v>>8), byte(v))
	case v <= 0xffffffff:
		return append(b, 0xce, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(b, 0xcf,
			byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}

// AppendInt appends a signed integer in its shortest form. Non-negative
// values use the unsigned family (msgpack-python behaviour).
func AppendInt(b []byte, v int64) []byte {
	if v >= 0 {
		return AppendUint(b, uint64(v))
	}
	switch {
	case v >= -32:
		return append(b, byte(v))
	case v >= -128:
		return append(b, 0xd0, byte(v))
	case v >= -32768:
		return append(b, 0xd1, byte(v>>8), byte(v))
	case v >= -2147483648:
		return append(b, 0xd2, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	default:
		return append(b, 0xd3,
			byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
			byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
}
