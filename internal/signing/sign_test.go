/*
FILE: internal/signing/sign_test.go

DESCRIPTION:
Tests of the signing primitives against the official Python SDK:
  - the phantom-agent connectionId and the "dummy action" signatures come
    verbatim from tests/signing_test.py;
  - the user-signed vectors (scheme 2) were produced by the SDK's own
    sign_*_action helpers for the same public test key (see
    scripts/gen-signing-vectors.py for the environment).

Action-level vectors (orders, cancels, modifies, ...) live in
internal/action/vectors_test.go.
*/

package signing

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/tonymontanov/go-hyperliquid/internal/msgpack"
)

// testPrivateKey — public test key from the official SDK test-suite. Not a real wallet.
const testPrivateKey string = "0x0123456789012345678901234567890123456789012345678901234567890123"

// testAddress — address of testPrivateKey as printed by eth_account (checksummed).
const testAddress string = "0x14791697260E4c9A71f18484C9f997B308e59325"

func newTestSigner(t testing.TB) *Signer {
	t.Helper()
	var s, err = NewSigner(testPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// packDummy packs {"type":"dummy","num":num} like msgpack.packb.
func packDummy(num uint64) []byte {
	var b []byte
	b = msgpack.AppendMapHeader(b, 2)
	b = msgpack.AppendString(b, "type")
	b = msgpack.AppendString(b, "dummy")
	b = msgpack.AppendString(b, "num")
	b = msgpack.AppendUint(b, num)
	return b
}

func TestSignerAddress(t *testing.T) {
	var s = newTestSigner(t)
	if got := s.Address().Hex(); got != strings.ToLower(testAddress) {
		t.Fatalf("address = %s, want %s", got, strings.ToLower(testAddress))
	}
	var parsed, err = ParseAddress(testAddress)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != s.Address() {
		t.Fatal("ParseAddress of the checksummed form must equal the derived address")
	}
}

func TestSignerNeverLeaksKey(t *testing.T) {
	var s = newTestSigner(t)
	var secret = strings.TrimPrefix(testPrivateKey, "0x")
	if strings.Contains(s.String(), secret) || strings.Contains(s.String(), secret[:16]) {
		t.Fatal("Signer.String() leaks key material")
	}
	var _, err = NewSigner("0x" + strings.Repeat("zz", 32))
	if !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("error = %v, want ErrInvalidPrivateKey", err)
	}
	if strings.Contains(err.Error(), "zz") {
		t.Fatal("error message echoes the key input")
	}
}

func TestSignerInvalidKeys(t *testing.T) {
	var bad = []string{
		"0x1234",
		strings.Repeat("0", 64), // zero scalar
		"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", // == curve order N
	}
	for _, key := range bad {
		if _, err := NewSigner(key); !errors.Is(err, ErrInvalidPrivateKey) {
			t.Errorf("NewSigner(len=%d) error = %v, want ErrInvalidPrivateKey", len(key), err)
		}
	}
}

func TestDisabledSigner(t *testing.T) {
	var s, err = NewSigner("")
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled() {
		t.Fatal("empty key must produce a disabled signer")
	}
	var digest [32]byte
	if _, err = s.SignDigest(&digest); !errors.Is(err, ErrSignerDisabled) {
		t.Fatalf("SignDigest error = %v, want ErrSignerDisabled", err)
	}
	var closed = newTestSigner(t)
	closed.Close()
	if _, err = closed.SignDigest(&digest); !errors.Is(err, ErrSignerDisabled) {
		t.Fatalf("SignDigest after Close error = %v, want ErrSignerDisabled", err)
	}
}

// Official vectors: test_l1_action_signing_matches / ..._with_vault.
func TestL1DummyActionOfficialVectors(t *testing.T) {
	var s = newTestSigner(t)
	var vault, err = ParseAddress("0x1719884eb866cb12b2287399b15f7db5e7d775ea")
	if err != nil {
		t.Fatal(err)
	}
	var cases = []struct {
		name      string
		vault     *Address
		isMainnet bool
		r, s      string
		v         byte
	}{
		{"mainnet", nil, true, "0x53749d5b30552aeb2fca34b530185976545bb22d0b3ce6f62e31be961a59298", "0x755c40ba9bf05223521753995abb2f73ab3229be8ec921f350cb447e384d8ed8", 27},
		{"testnet", nil, false, "0x542af61ef1f429707e3c76c5293c80d01f74ef853e34b76efffcb57e574f9510", "0x17b8b32f086e8cdede991f1e2c529f5dd5297cbe8128500e00cbaf766204a613", 28},
		{"vault mainnet", &vault, true, "0x3c548db75e479f8012acf3000ca3a6b05606bc2ec0c29c50c515066a326239", "0x4d402be7396ce74fbba3795769cda45aec00dc3125a984f2a9f23177b190da2c", 28},
		{"vault testnet", &vault, false, "0xe281d2fb5c6e25ca01601f878e4d69c965bb598b88fac58e475dd1f5e56c362b", "0x7ddad27e9a238d045c035bc606349d075d5c5cd00a6cd1da23ab5c39d4ef0f60", 27},
	}
	for _, c := range cases {
		// float_to_int_for_hashing(1000) == 100000000000
		var sig, _, signErr = s.SignL1Action(packDummy(100000000000), 0, c.vault, 0, false, c.isMainnet)
		if signErr != nil {
			t.Fatal(signErr)
		}
		if sig.RHex() != c.r || sig.SHex() != c.s || sig.V != c.v {
			t.Errorf("%s: got r=%s s=%s v=%d", c.name, sig.RHex(), sig.SHex(), sig.V)
		}
	}
}

func TestSignatureAppendJSON(t *testing.T) {
	var s = newTestSigner(t)
	// "vault mainnet" has r with a leading zero byte: eth_utils.to_hex drops it.
	var vault, _ = ParseAddress("0x1719884eb866cb12b2287399b15f7db5e7d775ea")
	var sig, _, err = s.SignL1Action(packDummy(100000000000), 0, &vault, 0, false, true)
	if err != nil {
		t.Fatal(err)
	}
	var want = `{"r":"0x3c548db75e479f8012acf3000ca3a6b05606bc2ec0c29c50c515066a326239","s":"0x4d402be7396ce74fbba3795769cda45aec00dc3125a984f2a9f23177b190da2c","v":28}`
	if got := string(sig.AppendJSON(nil)); got != want {
		t.Fatalf("AppendJSON\n got %s\nwant %s", got, want)
	}
	var zero [32]byte
	if got := string(appendMinimalHex(nil, &zero)); got != "0x0" {
		t.Fatalf("minimal hex of zero = %s", got)
	}
}

func TestActionHashExpiresAfter(t *testing.T) {
	var base, withExpiry [32]byte
	ActionHash(&base, packDummy(1), 5, nil, 0, false)
	ActionHash(&withExpiry, packDummy(1), 5, nil, 0, true)
	if base == withExpiry {
		t.Fatal("expiresAfter=0 (set) must hash differently from unset")
	}
	if hex.EncodeToString(base[:]) == "" {
		t.Fatal("unreachable")
	}
}

func mustAddress(t testing.TB, s string) Address {
	t.Helper()
	var a, err = ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestUserSignedMatchesPythonSDK(t *testing.T) {
	var s = newTestSigner(t)
	const dest string = "0x5e9ee1089755c3435139848e47e6635505d5a13a"
	var destAddr = mustAddress(t, dest)

	var cases = []struct {
		name        string
		primaryType string
		fields      []TypedField
		r, s        string
		v           byte
	}{
		{
			"usdSend testnet (official vector)", "HyperliquidTransaction:UsdSend",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainTestnet},
				{Name: "destination", Kind: FieldString, Str: dest},
				{Name: "amount", Kind: FieldString, Str: "1"},
				{Name: "time", Kind: FieldUint64, Uint: 1687816341423},
			},
			"0x637b37dd731507cdd24f46532ca8ba6eec616952c56218baeff04144e4a77073", "0x11a6a24900e6e314136d2592e2f8d502cd89b7c15b198e1bee043c9589f9fad7", 27,
		},
		{
			"withdraw testnet (official vector)", "HyperliquidTransaction:Withdraw",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainTestnet},
				{Name: "destination", Kind: FieldString, Str: dest},
				{Name: "amount", Kind: FieldString, Str: "1"},
				{Name: "time", Kind: FieldUint64, Uint: 1687816341423},
			},
			"0x8363524c799e90ce9bc41022f7c39b4e9bdba786e5f9c72b20e43e1462c37cf9", "0x58b1411a775938b83e29182e8ef74975f9054c8e97ebf5ec2dc8d51bfc893881", 28,
		},
		{
			"usdSend mainnet", "HyperliquidTransaction:UsdSend",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainMainnet},
				{Name: "destination", Kind: FieldString, Str: dest},
				{Name: "amount", Kind: FieldString, Str: "12.5"},
				{Name: "time", Kind: FieldUint64, Uint: 1700000000000},
			},
			"0x61beb61687673286028b8fa1096fbe488fb950a50c6377694c8bc44a04bd4923", "0x2e5352c790ecc8cdc6a114ec2fda2dc22657922afcb57b9dd770f32e002be40d", 27,
		},
		{
			"approveAgent mainnet", "HyperliquidTransaction:ApproveAgent",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainMainnet},
				{Name: "agentAddress", Kind: FieldAddress, Addr: destAddr},
				{Name: "agentName", Kind: FieldString, Str: "core-1"},
				{Name: "nonce", Kind: FieldUint64, Uint: 1700000000001},
			},
			"0xa8d917e0c002a002b6cc4a2d7f2063849c4edc8ef2e2caee30091c1ec45f9125", "0x7d3ca3682afcd68ca870e1864e637497188aac1754bcbd9c2ea9716d36aeb67d", 27,
		},
		{
			"usdClassTransfer testnet", "HyperliquidTransaction:UsdClassTransfer",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainTestnet},
				{Name: "amount", Kind: FieldString, Str: "100"},
				{Name: "toPerp", Kind: FieldBool, Bool: true},
				{Name: "nonce", Kind: FieldUint64, Uint: 1700000000002},
			},
			"0xac67a7acad07aeffddd44f38cbad5ed36a2e3c5df47fce1e4dd574f869d34dab", "0x5ac7ff1953610c9e7b7a8d003c2084fcbf1c42d0ddc5da6a15f6325a07c3ff98", 28,
		},
		{
			"spotSend mainnet", "HyperliquidTransaction:SpotSend",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainMainnet},
				{Name: "destination", Kind: FieldString, Str: dest},
				{Name: "token", Kind: FieldString, Str: "PURR:0xc1fb593aeffbeb02f85e0308e9956a90"},
				{Name: "amount", Kind: FieldString, Str: "0.5"},
				{Name: "time", Kind: FieldUint64, Uint: 1700000000003},
			},
			"0x580100a8548c7bb834f47fe6f04c070303f9a691b231141697239633ae3cbe4c", "0x19d2ff34fc0ab3cfb4a3b42a92380b50278add2b74e6bd249f1527aa001cae61", 27,
		},
		{
			"sendAsset testnet", "HyperliquidTransaction:SendAsset",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainTestnet},
				{Name: "destination", Kind: FieldString, Str: dest},
				{Name: "sourceDex", Kind: FieldString, Str: ""},
				{Name: "destinationDex", Kind: FieldString, Str: "spot"},
				{Name: "token", Kind: FieldString, Str: "USDC:0x6d1e7cde53ba9467b783cb7c530ce054"},
				{Name: "amount", Kind: FieldString, Str: "7"},
				{Name: "fromSubAccount", Kind: FieldString, Str: ""},
				{Name: "nonce", Kind: FieldUint64, Uint: 1700000000004},
			},
			"0x9abf4827982e6e1f7fc3ab01bd06c6d7678b042d8e4cc4aed2d8334a253b4846", "0x4e13f71f2907901d0774352efa8c3c503aabd91093e6452551aff3b1e5b5a025", 28,
		},
		{
			"approveBuilderFee mainnet", "HyperliquidTransaction:ApproveBuilderFee",
			[]TypedField{
				{Name: "hyperliquidChain", Kind: FieldString, Str: HyperliquidChainMainnet},
				{Name: "maxFeeRate", Kind: FieldString, Str: "0.001%"},
				{Name: "builder", Kind: FieldAddress, Addr: destAddr},
				{Name: "nonce", Kind: FieldUint64, Uint: 1700000000005},
			},
			"0x1dd2e1de7b1b125383952b4d38ed2a5fc5daf6793610badfdffff2706db1a143", "0x29cfb8221022371428245f308a01dd98306f712403cb74fc7917f5918d79e214", 28,
		},
	}
	for _, c := range cases {
		var sig, err = s.SignUserSigned(c.primaryType, c.fields, DefaultSignatureChainID)
		if err != nil {
			t.Fatal(err)
		}
		if sig.RHex() != c.r || sig.SHex() != c.s || sig.V != c.v {
			t.Errorf("%s: got r=%s s=%s v=%d", c.name, sig.RHex(), sig.SHex(), sig.V)
		}
	}
}

func TestParseAddressErrors(t *testing.T) {
	for _, in := range []string{"", "0x", "0x1234", "0x" + strings.Repeat("g", 40), strings.Repeat("a", 41)} {
		if _, err := ParseAddress(in); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("ParseAddress(%q) error = %v", in, err)
		}
	}
}

func BenchmarkAgentDigest(b *testing.B) {
	var packed = packDummy(100000000000)
	var buf = make([]byte, 0, 256)
	var connectionID, digest [32]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = append(buf[:0], packed...)
		buf = ActionHash(&connectionID, buf, uint64(i), nil, 0, false)
		AgentDigest(&digest, &connectionID, true)
	}
}

func BenchmarkSignDigest(b *testing.B) {
	var s = newTestSigner(b)
	var digest [32]byte
	digest[0] = 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var _, _ = s.SignDigest(&digest)
	}
}
