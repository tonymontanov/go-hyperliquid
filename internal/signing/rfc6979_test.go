/*
FILE: internal/signing/rfc6979_test.go

DESCRIPTION:
Differential test of the allocation-free signer against decred's reference
ecdsa.SignCompact: random keys x random digests must give byte-identical
{v, r, s}. Edge digests (all zeros, all 0xff — larger than the group order)
exercise the "digest mod N" path. Reference vectors of the official Python SDK
(sign_test.go, internal/action) run through the same routine.
*/

package signing

import (
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func assertSameAsDecred(t *testing.T, priv *secp256k1.PrivateKey, digest [32]byte) {
	t.Helper()
	var want = ecdsa.SignCompact(priv, digest[:], false)
	var got Signature
	signDigest(priv, &digest, &got)
	if got.V != want[0] || string(got.R[:]) != string(want[1:33]) || string(got.S[:]) != string(want[33:65]) {
		t.Fatalf("signature differs from decred for digest %x\n got v=%d r=%x s=%x\nwant v=%d r=%x s=%x",
			digest, got.V, got.R, got.S, want[0], want[1:33], want[33:65])
	}
	// The signature must recover to the key's address.
	var pub, _, err = ecdsa.RecoverCompact(want, digest[:])
	if err != nil || !pub.IsEqual(priv.PubKey()) {
		t.Fatalf("recover failed: %v", err)
	}
}

func TestSignDigestMatchesDecred(t *testing.T) {
	for keyIndex := 0; keyIndex < 8; keyIndex++ {
		var priv, err = secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 250; i++ {
			var digest [32]byte
			if _, err = rand.Read(digest[:]); err != nil {
				t.Fatal(err)
			}
			assertSameAsDecred(t, priv, digest)
		}
		var zeros, ones [32]byte
		for i := range ones {
			ones[i] = 0xff
		}
		assertSameAsDecred(t, priv, zeros)
		assertSameAsDecred(t, priv, ones)
	}
}

// maxSignAllocs — allocation budget of one signature. Everything written in
// this package is allocation-free; the remaining allocations all come from
// decred's ModNScalar.InverseValNonConst, which computes k^-1 with math/big
// (verified with a memory profile). ecdsa.SignCompact needs 29.
const maxSignAllocs float64 = 12

func TestSignDigestAllocationBudget(t *testing.T) {
	var s = newTestSigner(t)
	var digest [32]byte
	digest[5] = 7
	var allocs = testing.AllocsPerRun(50, func() {
		var _, _ = s.SignDigest(&digest)
	})
	if allocs > maxSignAllocs {
		t.Fatalf("SignDigest allocates %.0f times per call, budget is %.0f", allocs, maxSignAllocs)
	}
}

func BenchmarkSignCompactDecred(b *testing.B) {
	var s = newTestSigner(b)
	var digest [32]byte
	digest[0] = 1
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ecdsa.SignCompact(s.privateKey, digest[:], false)
	}
}
