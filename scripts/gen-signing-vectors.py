#!/usr/bin/env python3
"""
FILE: scripts/gen-signing-vectors.py

DESCRIPTION:
Generates reference signing vectors with the OFFICIAL Hyperliquid Python SDK and
prints them as a Go table (see internal/action/vectors_test.go). Runs fully
offline: only hyperliquid/utils/signing.py is used, no network calls are made.

The private key below is the public test key from the official SDK test-suite
(tests/signing_test.py). It is NOT a real wallet. Never put a real key here.

USAGE:
    git clone https://github.com/hyperliquid-dex/hyperliquid-python-sdk
    python3 -m venv venv && ./venv/bin/pip install eth-account msgpack eth-utils
    PYTHONPATH=hyperliquid-python-sdk ./venv/bin/python scripts/gen-signing-vectors.py

Vectors whose action shape exists only in the GitBook docs (single "modify",
the "f" / "a" flags) prove msgpack parity with msgpack-python for that shape;
acceptance by the exchange is verified separately on testnet.
"""

import json

import eth_account
import msgpack

from hyperliquid.utils.signing import action_hash, sign_l1_action

TEST_KEY = "0x0123456789012345678901234567890123456789012345678901234567890123"
VAULT = "0x1719884eb866cb12b2287399b15f7db5e7d775ea"
BUILDER = "0x5e9ee1089755c3435139848e47e6635505d5a13a"
CLOID_1 = "0x00000000000000000000000000000001"
CLOID_2 = "0x1234567890abcdef1234567890abcdef"


def limit(asset, is_buy, px, sz, reduce_only, tif, cloid=None):
    wire = {"a": asset, "b": is_buy, "p": px, "s": sz, "r": reduce_only, "t": {"limit": {"tif": tif}}}
    if cloid is not None:
        wire["c"] = cloid
    return wire


def trigger(asset, is_buy, px, sz, reduce_only, is_market, trigger_px, tpsl):
    return {
        "a": asset, "b": is_buy, "p": px, "s": sz, "r": reduce_only,
        "t": {"trigger": {"isMarket": is_market, "triggerPx": trigger_px, "tpsl": tpsl}},
    }


CASES = [
    # name, action, nonce, vault, expires_after
    ("order gtc (official vector)", {"type": "order", "orders": [limit(1, True, "100", "100", False, "Gtc")], "grouping": "na"}, 0, None, None),
    ("order with cloid (official vector)", {"type": "order", "orders": [limit(1, True, "100", "100", False, "Gtc", CLOID_1)], "grouping": "na"}, 0, None, None),
    ("trigger sl (official vector)", {"type": "order", "orders": [trigger(1, True, "100", "100", False, True, "103", "sl")], "grouping": "na"}, 0, None, None),
    ("order ioc fractional", {"type": "order", "orders": [limit(4, True, "1670.1", "0.0147", False, "Ioc")], "grouping": "na"}, 1677777606040, None, None),
    ("order alo vault expires", {"type": "order", "orders": [limit(0, False, "86759", "0.00012", True, "Alo", CLOID_2)], "grouping": "na"}, 1700000000000, VAULT, 1700000015000),
    ("order batch of 20 (array16)", {"type": "order", "orders": [limit(i, i % 2 == 0, str(1000 + i), "0.5", False, "Alo") for i in range(20)], "grouping": "na"}, 1700000000001, None, None),
    ("order normalTpsl with builder", {"type": "order", "orders": [limit(3, True, "25.5", "10", False, "Gtc"), trigger(3, False, "30", "10", True, False, "29.5", "tp")], "grouping": "normalTpsl", "builder": {"b": BUILDER, "f": 10}}, 1700000000002, None, None),
    ("order hip3 asset id", {"type": "order", "orders": [limit(110000, True, "0.9", "100", False, "Ioc")], "grouping": "na"}, 1700000000003, None, None),
    ("cancel two", {"type": "cancel", "cancels": [{"a": 1, "o": 123}, {"a": 10000, "o": 91490942310}]}, 1700000000004, None, None),
    ("cancel fast", {"type": "cancel", "cancels": [{"a": 1, "o": 123}], "f": True}, 1700000000005, None, None),
    ("cancelByCloid", {"type": "cancelByCloid", "cancels": [{"asset": 1, "cloid": CLOID_2}]}, 1700000000006, VAULT, None),
    ("cancelByCloid fast", {"type": "cancelByCloid", "cancels": [{"asset": 1, "cloid": CLOID_2}], "f": True}, 1700000000007, None, None),
    ("modify by oid (docs shape)", {"type": "modify", "oid": 123, "order": limit(1, True, "101", "100", False, "Alo")}, 1700000000008, None, None),
    ("modify by cloid always place (docs shape)", {"type": "modify", "oid": CLOID_2, "order": limit(1, True, "101", "100", False, "Gtc", CLOID_1), "a": True}, 1700000000009, None, None),
    ("batchModify", {"type": "batchModify", "modifies": [{"oid": 123, "order": limit(1, True, "101", "100", False, "Alo")}, {"oid": CLOID_2, "order": limit(2, False, "0.5", "7", True, "Gtc", CLOID_1)}]}, 1700000000010, None, None),
    ("batchModify always place", {"type": "batchModify", "modifies": [{"oid": 123, "order": limit(1, True, "101", "100", False, "Alo")}], "a": True}, 1700000000011, None, None),
    ("scheduleCancel unset (official vector)", {"type": "scheduleCancel"}, 0, None, None),
    ("scheduleCancel time (official vector)", {"type": "scheduleCancel", "time": 123456789}, 0, None, None),
    ("noop", {"type": "noop"}, 1700000000012, None, None),
    ("updateLeverage", {"type": "updateLeverage", "asset": 1, "isCross": True, "leverage": 20}, 1700000000013, None, None),
    ("updateIsolatedMargin add", {"type": "updateIsolatedMargin", "asset": 1, "isBuy": True, "ntli": 1000000}, 1700000000014, None, None),
    ("updateIsolatedMargin remove", {"type": "updateIsolatedMargin", "asset": 1, "isBuy": True, "ntli": -2500000}, 1700000000015, VAULT, None),
]


def go_string(value):
    return json.dumps(value)


def main():
    wallet = eth_account.Account.from_key(TEST_KEY)
    print("// Code generated by scripts/gen-signing-vectors.py with the official Python SDK. DO NOT EDIT.")
    print("var generatedVectors = []signingVector{")
    for name, action, nonce, vault, expires_after in CASES:
        packed = msgpack.packb(action).hex()
        connection_id = action_hash(action, vault, nonce, expires_after).hex()
        mainnet = sign_l1_action(wallet, action, vault, nonce, expires_after, True)
        testnet = sign_l1_action(wallet, action, vault, nonce, expires_after, False)
        print("\t{")
        print(f"\t\tname:         {go_string(name)},")
        print(f"\t\tjson:         {go_string(json.dumps(action, separators=(',', ':')))},")
        print(f"\t\tmsgpackHex:   {go_string(packed)},")
        print(f"\t\tnonce:        {nonce},")
        print(f"\t\tvault:        {go_string(vault or '')},")
        print(f"\t\texpiresAfter: {expires_after if expires_after is not None else 0},")
        print(f"\t\thasExpires:   {'true' if expires_after is not None else 'false'},")
        print(f"\t\tconnectionID: {go_string(connection_id)},")
        print(f"\t\tmainnet:      wantSignature{{{go_string(mainnet['r'])}, {go_string(mainnet['s'])}, {mainnet['v']}}},")
        print(f"\t\ttestnet:      wantSignature{{{go_string(testnet['r'])}, {go_string(testnet['s'])}, {testnet['v']}}},")
        print("\t},")
    print("}")


if __name__ == "__main__":
    main()
