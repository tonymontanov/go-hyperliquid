/*
Package hyperliquid is the root of go-hyperliquid — a low-latency Go SDK for the
Hyperliquid exchange (HyperCore L1, fully on-chain order book).

# Architecture: two layers

COMMON LAYER (this package + internal/* + types):

  - signing: L1 actions (MessagePack → keccak → phantom agent → EIP-712 →
    secp256k1) and user-signed actions, byte-for-byte compatible with the
    official Python SDK and covered by its reference vectors;
  - lock-free nonce generator shared per signing wallet;
  - action builders with paired MessagePack / JSON writers;
  - REST (/info, /exchange) and WebSocket transports, including WS post
    requests for trading over the socket;
  - fixed-point prices / sizes and the exchange rounding rules without
    allocations (types.Fixed, types.Precision);
  - SDK-side rate-limit accounting (the exchange sends no headers);
  - one unified request engine used by every section.

SECTION LAYER — one package per exchange section, named after the exchange's
own vocabulary, each a thin specialisation of the common layer:

	perpetuals   Perpetuals (validator-operated perp dex)        v1.0
	spot         Spot                                             v2.0
	hip3         HIP-3: Builder-deployed perpetuals               v2.5
	outcomes     HIP-4: Outcome markets                           v2.5

Sections never import each other and never re-use each other's functions.

# Quick start

	var cfg hyperliquid.Config = hyperliquid.DefaultConfig()
	cfg.Testnet = true
	cfg.AccountAddress = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_API_KEY")
	cfg.PrivateKey = os.Getenv("HYPERLIQUID_PERPETUALS_TESTNET_SECRET_KEY")

	var client *hyperliquid.Client
	client, err = hyperliquid.NewClient(cfg)
	defer client.Close()

	var perps *perpetuals.Client = perpetuals.NewClient(client)

# Authentication

Hyperliquid has no API key / secret. Requests are signed with the private key
of a wallet: normally an API wallet (agent) approved by the master account.
See Config for the three credential fields and their exact semantics.

# Numbers

Order requests and market-data streams use types.Fixed (int64, 8 fractional
digits, zero allocations). Everything off the hot path (account state, fills,
funding) uses shopspring/decimal, like the sibling SDKs.
*/
package hyperliquid
