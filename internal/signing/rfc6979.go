/*
FILE: internal/signing/rfc6979.go

DESCRIPTION:
Low-allocation ECDSA signing over secp256k1 with RFC 6979 deterministic
nonces, built on the exported primitives of decred/secp256k1 (ModNScalar,
JacobianPoint, ScalarBaseMultNonConst).

WHY NOT ecdsa.SignCompact:
decred's high-level SignCompact is correct but allocates on every call (HMAC
hashers and their Sum() results, temporary scalars, the Signature object, the
65-byte result): 29 allocations / 1.6 KB per signature. Signing sits on the
order hot path, where allocations turn into GC jitter of tail latencies. This
file performs the SAME computation with every intermediate of OUR code on the
stack (Apple M4 Pro, median of 3 runs):

	ecdsa.SignCompact : 20.7 µs, 1592 B/op, 29 allocs/op
	signDigest        : 20.8 µs,  568 B/op, 12 allocs/op

The remaining 12 allocations all come from ModNScalar.InverseValNonConst — the
only modular inverse decred exports, implemented with math/big. Removing them
needs an own fixed-width inverse (safegcd / binary extended Euclid); the time
is dominated by the base-point multiplication either way. Tracked in
handoff.md; the budget is pinned by TestSignDigestAllocationBudget.

EQUIVALENCE:
The algorithm mirrors decred's signRFC6979 / sign / NonceRFC6979 step by step
(same HMAC key material: privateKey || hash, no extra data; same candidate
acceptance rule; same low-S normalisation and recovery code), therefore the
output is byte-identical. This is enforced by
  - the reference vectors of the official Python SDK (eth_account), and
  - a differential test against ecdsa.SignCompact over random keys / digests.

SECURITY NOTES:
  - ScalarBaseMultNonConst is not constant time — the very function decred's
    own signer uses; the nonce is secret-derived and never reused.
  - Every buffer that held key material or the nonce is wiped before return.

RFC 6979 (section 3.2) with HMAC-SHA256, qlen = hlen = 256:
	V = 0x01 * 32 ; K = 0x00 * 32
	K = HMAC_K(V || 0x00 || x || h) ; V = HMAC_K(V)
	K = HMAC_K(V || 0x01 || x || h) ; V = HMAC_K(V)
	loop: V = HMAC_K(V) ; k = int(V) ; accept if 0 < k < N
	      else K = HMAC_K(V || 0x00) ; V = HMAC_K(V)
*/

package signing

import (
	"crypto/sha256"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const (
	// hmacBlockSize — SHA-256 block size.
	hmacBlockSize int = 64
	// rfc6979MaterialLen — V(32) || sep(1) || x(32) || h(32).
	rfc6979MaterialLen int = 97
	// compactSigMagicOffset — v of an uncompressed-key compact signature is
	// 27 + recovery code (Ethereum convention).
	compactSigMagicOffset byte = 27
)

// hmacSHA256 computes HMAC-SHA256(key, msg) into out without heap allocations.
// key is always 32 bytes here, i.e. shorter than the block size; msg is at
// most rfc6979MaterialLen bytes.
func hmacSHA256(key *[32]byte, msg []byte, out *[32]byte) {
	var inner [hmacBlockSize + rfc6979MaterialLen]byte
	var outer [hmacBlockSize + sha256.Size]byte
	var i int
	for i = 0; i < hmacBlockSize; i++ {
		var k byte
		if i < len(key) {
			k = key[i]
		}
		inner[i] = k ^ 0x36
		outer[i] = k ^ 0x5c
	}
	var n int = copy(inner[hmacBlockSize:], msg)
	var innerSum [32]byte = sha256.Sum256(inner[:hmacBlockSize+n])
	copy(outer[hmacBlockSize:], innerSum[:])
	*out = sha256.Sum256(outer[:])

	wipe(inner[:])
	wipe(outer[:])
	wipe(innerSum[:])
}

// nonceGenerator — RFC 6979 HMAC-DRBG state.
type nonceGenerator struct {
	k [32]byte
	v [32]byte
}

// init seeds the generator with the private key x and the message hash h.
func (g *nonceGenerator) init(x *[32]byte, h *[32]byte) {
	var material [rfc6979MaterialLen]byte
	var i int
	for i = 0; i < 32; i++ {
		g.v[i] = 0x01
		g.k[i] = 0x00
	}
	// K = HMAC_K(V || 0x00 || x || h)
	copy(material[0:32], g.v[:])
	material[32] = 0x00
	copy(material[33:65], x[:])
	copy(material[65:97], h[:])
	hmacSHA256(&g.k, material[:], &g.k)
	// V = HMAC_K(V)
	hmacSHA256(&g.k, g.v[:], &g.v)
	// K = HMAC_K(V || 0x01 || x || h)
	copy(material[0:32], g.v[:])
	material[32] = 0x01
	hmacSHA256(&g.k, material[:], &g.k)
	// V = HMAC_K(V)
	hmacSHA256(&g.k, g.v[:], &g.v)
	wipe(material[:])
}

// next writes the next valid nonce candidate (0 < k < N) into out.
func (g *nonceGenerator) next(out *secp256k1.ModNScalar) {
	var retry [33]byte
	for {
		// V = HMAC_K(V)
		hmacSHA256(&g.k, g.v[:], &g.v)
		var overflow uint32 = out.SetBytes(&g.v)
		if overflow == 0 && !out.IsZero() {
			return
		}
		g.reseed(&retry)
	}
}

// reseed advances the generator after a rejected candidate or a rejected
// signature: K = HMAC_K(V || 0x00) ; V = HMAC_K(V).
func (g *nonceGenerator) reseed(scratch *[33]byte) {
	copy(scratch[0:32], g.v[:])
	scratch[32] = 0x00
	hmacSHA256(&g.k, scratch[:], &g.k)
	hmacSHA256(&g.k, g.v[:], &g.v)
}

// wipeState clears the generator.
func (g *nonceGenerator) wipeState() {
	wipe(g.k[:])
	wipe(g.v[:])
}

/*
signDigest produces the low-S recoverable signature of digest with the
deterministic RFC 6979 nonce. Byte-identical to
ecdsa.SignCompact(priv, digest[:], false). Nothing in this function escapes to
the heap; see the file header for the allocations inside decred's inverse.
*/
func signDigest(priv *secp256k1.PrivateKey, digest *[32]byte, out *Signature) {
	var x [32]byte
	priv.Key.PutBytesUnchecked(x[:])

	var generator nonceGenerator
	generator.init(&x, digest)
	wipe(x[:])

	// e = digest mod N
	var e secp256k1.ModNScalar
	e.SetBytes(digest)

	var k, r, s, kinv secp256k1.ModNScalar
	var kG secp256k1.JacobianPoint
	var rx [32]byte
	var retry [33]byte
	for {
		generator.next(&k)

		// R = k*G ; r = R.x mod N
		secp256k1.ScalarBaseMultNonConst(&k, &kG)
		kG.ToAffine()
		kG.X.PutBytesUnchecked(rx[:])
		var overflow uint32 = r.SetBytes(&rx)
		if r.IsZero() {
			generator.reseed(&retry)
			continue
		}
		var recoveryCode byte = byte(overflow<<1) | byte(kG.Y.IsOddBit())

		// s = k^-1 * (e + d*r) mod N
		kinv.InverseValNonConst(&k)
		s.Mul2(&priv.Key, &r).Add(&e).Mul(&kinv)
		if s.IsZero() {
			generator.reseed(&retry)
			continue
		}
		if s.IsOverHalfOrder() {
			s.Negate()
			recoveryCode ^= 0x01
		}

		r.PutBytesUnchecked(out.R[:])
		s.PutBytesUnchecked(out.S[:])
		out.V = compactSigMagicOffset + recoveryCode
		break
	}

	k.Zero()
	kinv.Zero()
	generator.wipeState()
}
