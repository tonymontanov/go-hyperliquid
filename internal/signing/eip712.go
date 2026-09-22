/*
FILE: internal/signing/eip712.go

DESCRIPTION:
Keccak-256 and the two EIP-712 schemes used by Hyperliquid. The GitBook docs
only name the schemes; the algorithms below follow the official Python SDK
(hyperliquid/utils/signing.py), which the docs designate as the reference, and
are verified byte-for-byte against its test vectors (see sign_test.go).

SCHEME 1 — L1 ACTIONS (orders, cancels, modifies, leverage, ...):
  connectionId = keccak256( msgpack(action)
                            ++ nonce            (8 bytes, big-endian)
                            ++ 0x00 | 0x01 ++ vaultAddress (20 bytes)
                            ++ [0x00 ++ expiresAfter (8 bytes BE)]  only if set )
  The signed struct is the "phantom agent":
      Agent(string source, bytes32 connectionId)
      source = "a" on mainnet, "b" on testnet
  in the domain {name:"Exchange", version:"1", chainId:1337,
  verifyingContract:0x0}. Everything except connectionId is constant, so the
  domain separator, the type hash and both source hashes are precomputed at
  package init; signing an action costs 3 keccak calls + 1 ECDSA signature.

SCHEME 2 — USER-SIGNED ACTIONS (transfers, withdrawals, approveAgent, ...):
  The action fields themselves are the typed message, primary type
  "HyperliquidTransaction:<Name>", domain {name:"HyperliquidSignTransaction",
  version:"1", chainId:<signatureChainId>, verifyingContract:0x0}.
  These actions are rare (not a hot path), so the encoder is generic over a
  list of typed fields instead of being hand-unrolled per action.

MAIN FUNCTIONS:
  - Keccak256(out, parts...)          : legacy Keccak-256 (NOT NIST SHA3-256).
  - ActionHash(out, packed, ...)      : connectionId of an L1 action.
  - AgentDigest(out, connID, mainnet) : EIP-712 digest of the phantom agent.
  - UserSignedDigest(out, ...)        : EIP-712 digest of a user-signed action.

DEPENDENCIES:
- golang.org/x/crypto/sha3: NewLegacyKeccak256.
*/

package signing

import (
	"encoding/binary"
	"hash"
	"strings"

	"golang.org/x/crypto/sha3"
)

const (
	// l1ChainID — chainId of the L1 "Exchange" signing domain (constant).
	l1ChainID uint64 = 1337
	// DefaultSignatureChainID — chainId of the user-signed domain used by the
	// official Python SDK ("0x66eee", Arbitrum Sepolia). The exchange accepts
	// any chain here ("signatureChainId is the chain used by the wallet to
	// sign and can be any chain"); the value must match the signatureChainId
	// field sent in the action.
	DefaultSignatureChainID uint64 = 0x66eee
	// DefaultSignatureChainIDHex — wire form of DefaultSignatureChainID.
	DefaultSignatureChainIDHex string = "0x66eee"
	// HyperliquidChainMainnet / HyperliquidChainTestnet — values of the
	// hyperliquidChain field of user-signed actions.
	HyperliquidChainMainnet string = "Mainnet"
	HyperliquidChainTestnet string = "Testnet"

	eip712DomainType string = "EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"
	agentType        string = "Agent(string source,bytes32 connectionId)"
)

// Precomputed constants of the L1 scheme.
var (
	l1DomainSeparator [32]byte
	agentTypeHash     [32]byte
	sourceHashMainnet [32]byte
	sourceHashTestnet [32]byte
	// eip712Prefix — "\x19\x01".
	eip712Prefix []byte = []byte{0x19, 0x01}
)

func init() {
	domainSeparator(&l1DomainSeparator, "Exchange", "1", l1ChainID)
	Keccak256(&agentTypeHash, []byte(agentType))
	Keccak256(&sourceHashMainnet, []byte("a"))
	Keccak256(&sourceHashTestnet, []byte("b"))
}

// Keccak256 hashes the concatenation of parts into out.
func Keccak256(out *[32]byte, parts ...[]byte) {
	var h hash.Hash = sha3.NewLegacyKeccak256()
	var i int
	for i = 0; i < len(parts); i++ {
		_, _ = h.Write(parts[i])
	}
	h.Sum(out[:0])
}

// domainSeparator computes an EIP-712 domain separator with a zero
// verifyingContract.
func domainSeparator(out *[32]byte, name string, version string, chainID uint64) {
	var typeHash, nameHash, versionHash, chainWord, contractWord [32]byte
	Keccak256(&typeHash, []byte(eip712DomainType))
	Keccak256(&nameHash, []byte(name))
	Keccak256(&versionHash, []byte(version))
	binary.BigEndian.PutUint64(chainWord[24:], chainID)
	Keccak256(out, typeHash[:], nameHash[:], versionHash[:], chainWord[:], contractWord[:])
}

/*
ActionHash appends the nonce / vault / expiresAfter trailer to packed (the
MessagePack form of the action) and writes the keccak256 of the result to out.

packed is extended in place (at most 38 bytes); the extended slice is returned
so the caller can keep reusing the same backing array. vault == nil means "no
vault / sub-account". hasExpiresAfter distinguishes "unset" from 0.
*/
func ActionHash(out *[32]byte, packed []byte, nonce uint64, vault *Address, expiresAfter uint64, hasExpiresAfter bool) []byte {
	packed = binary.BigEndian.AppendUint64(packed, nonce)
	if vault == nil {
		packed = append(packed, 0x00)
	} else {
		packed = append(packed, 0x01)
		packed = append(packed, vault[:]...)
	}
	if hasExpiresAfter {
		packed = append(packed, 0x00)
		packed = binary.BigEndian.AppendUint64(packed, expiresAfter)
	}
	Keccak256(out, packed)
	return packed
}

// AgentDigest computes the EIP-712 digest of the phantom agent for an L1 action.
func AgentDigest(out *[32]byte, connectionID *[32]byte, isMainnet bool) {
	var source *[32]byte = &sourceHashTestnet
	if isMainnet {
		source = &sourceHashMainnet
	}
	var structHash [32]byte
	Keccak256(&structHash, agentTypeHash[:], source[:], connectionID[:])
	Keccak256(out, eip712Prefix, l1DomainSeparator[:], structHash[:])
}

// FieldKind — Solidity type of a user-signed action field.
type FieldKind uint8

const (
	// FieldString — "string": hashed with keccak256.
	FieldString FieldKind = iota
	// FieldAddress — "address": 20 bytes left-padded to a 32-byte word.
	FieldAddress
	// FieldUint64 — "uint64": big-endian 32-byte word.
	FieldUint64
	// FieldBool — "bool": 0 / 1 as a 32-byte word.
	FieldBool
	// FieldBytes32 — "bytes32": used as is.
	FieldBytes32
)

// solidityName returns the Solidity type name used in the EIP-712 type string.
func (k FieldKind) solidityName() string {
	switch k {
	case FieldAddress:
		return "address"
	case FieldUint64:
		return "uint64"
	case FieldBool:
		return "bool"
	case FieldBytes32:
		return "bytes32"
	default:
		return "string"
	}
}

// TypedField — one field of a user-signed action, in EIP-712 declaration order.
type TypedField struct {
	Name    string
	Kind    FieldKind
	Str     string
	Addr    Address
	Uint    uint64
	Bool    bool
	Bytes32 [32]byte
}

/*
UserSignedDigest computes the EIP-712 digest of a user-signed action.

primaryType is the full EIP-712 type name, e.g. "HyperliquidTransaction:UsdSend".
fields must be listed in the exact order of the official type definition and
must start with hyperliquidChain (the SDK callers add it). signatureChainID is
the numeric value of the action's signatureChainId field.
*/
func UserSignedDigest(out *[32]byte, primaryType string, fields []TypedField, signatureChainID uint64) {
	var typeString strings.Builder
	typeString.Grow(len(primaryType) + 24*len(fields))
	typeString.WriteString(primaryType)
	typeString.WriteByte('(')
	var i int
	for i = 0; i < len(fields); i++ {
		if i > 0 {
			typeString.WriteByte(',')
		}
		typeString.WriteString(fields[i].Kind.solidityName())
		typeString.WriteByte(' ')
		typeString.WriteString(fields[i].Name)
	}
	typeString.WriteByte(')')

	var typeHash [32]byte
	Keccak256(&typeHash, []byte(typeString.String()))

	var encoded []byte = make([]byte, 0, 32*(len(fields)+1))
	encoded = append(encoded, typeHash[:]...)
	for i = 0; i < len(fields); i++ {
		var word [32]byte
		switch fields[i].Kind {
		case FieldAddress:
			copy(word[12:], fields[i].Addr[:])
		case FieldUint64:
			binary.BigEndian.PutUint64(word[24:], fields[i].Uint)
		case FieldBool:
			if fields[i].Bool {
				word[31] = 1
			}
		case FieldBytes32:
			word = fields[i].Bytes32
		default:
			Keccak256(&word, []byte(fields[i].Str))
		}
		encoded = append(encoded, word[:]...)
	}

	var structHash, separator [32]byte
	Keccak256(&structHash, encoded)
	domainSeparator(&separator, "HyperliquidSignTransaction", "1", signatureChainID)
	Keccak256(out, eip712Prefix, separator[:], structHash[:])
}
