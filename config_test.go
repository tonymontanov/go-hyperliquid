/*
FILE: config_test.go

DESCRIPTION:
Tests of Config defaults and of the credential handling of NewClient.
*/

package hyperliquid

import (
	"strings"
	"testing"
)

func TestTestnetSelectsTestnetEndpoints(t *testing.T) {
	// Regression: DefaultConfig() pre-fills mainnet URLs; flipping Testnet on
	// top of it used to keep them (found by a live smoke run on 2026-09-22).
	var fromDefault Config = DefaultConfig()
	fromDefault.Testnet = true
	var zero Config = Config{Testnet: true}
	for name, cfg := range map[string]Config{"DefaultConfig+Testnet": fromDefault, "zero+Testnet": zero} {
		var got Config = cfg.withDefaults()
		if got.REST.BaseURL != TestnetRestURL || got.WS.URL != TestnetWsURL {
			t.Errorf("%s: REST=%s WS=%s", name, got.REST.BaseURL, got.WS.URL)
		}
	}

	var mainnet Config = Config{}.withDefaults()
	if mainnet.REST.BaseURL != MainnetRestURL || mainnet.WS.URL != MainnetWsURL {
		t.Errorf("mainnet defaults: REST=%s WS=%s", mainnet.REST.BaseURL, mainnet.WS.URL)
	}

	var custom Config = Config{Testnet: true}
	custom.REST.BaseURL = "http://127.0.0.1:3001"
	custom.WS.URL = "ws://127.0.0.1:3001/ws"
	var kept Config = custom.withDefaults()
	if kept.REST.BaseURL != "http://127.0.0.1:3001" || kept.WS.URL != "ws://127.0.0.1:3001/ws" {
		t.Errorf("explicit URLs must never be overridden: %+v %+v", kept.REST.BaseURL, kept.WS.URL)
	}
}

func TestWithDefaultsDoesNotMutateCaller(t *testing.T) {
	var cfg Config
	_ = cfg.withDefaults()
	if cfg.REST.BaseURL != "" || cfg.Logger != nil {
		t.Fatal("withDefaults must work on a copy")
	}
	var filled Config = cfg.withDefaults()
	if filled.WS.ReadTimeout <= filled.WS.PingInterval || filled.AssetRefreshInterval <= 0 || filled.UserAgent == "" {
		t.Fatalf("defaults: %+v", filled.WS)
	}
	if err := filled.validate(); err != nil {
		t.Fatal(err)
	}
	filled.WS.ReadTimeout = filled.WS.PingInterval
	if err := filled.validate(); !IsInvalidRequest(err) {
		t.Fatalf("ReadTimeout <= PingInterval must be rejected: %v", err)
	}
}

func TestNewClientCredentials(t *testing.T) {
	const key string = "0x0123456789012345678901234567890123456789012345678901234567890123"
	const signer string = "0x14791697260e4c9a71f18484c9f997b308e59325"
	const account string = "0x5E9EE1089755C3435139848E47E6635505D5A13A"
	const vault string = "0x1719884eb866cb12b2287399b15f7db5e7d775ea"

	var client, err = NewClient(Config{PrivateKey: key, AccountAddress: account})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if !client.CanSign() || client.SignerAddress() != signer || client.UserAddress() != strings.ToLower(account) {
		t.Fatalf("signer=%s user=%s", client.SignerAddress(), client.UserAddress())
	}
	if client.Config().PrivateKey != "" {
		t.Fatal("Config() must not expose the private key")
	}

	var withVault *Client
	withVault, err = NewClient(Config{PrivateKey: key, AccountAddress: account, VaultAddress: vault})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = withVault.Close() }()
	if withVault.UserAddress() != vault || withVault.Engine().Vault() == nil {
		t.Fatalf("vault must become the info user: %s", withVault.UserAddress())
	}

	var masterOnly *Client
	masterOnly, err = NewClient(Config{PrivateKey: key})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = masterOnly.Close() }()
	if masterOnly.UserAddress() != signer {
		t.Fatalf("without AccountAddress the signer is the user: %s", masterOnly.UserAddress())
	}

	var public *Client
	public, err = NewClient(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = public.Close() }()
	if public.CanSign() || public.UserAddress() != "" || public.SignerAddress() != "" {
		t.Fatal("keyless client must be read-only and anonymous")
	}

	var bad = []Config{{PrivateKey: "0x1234"}, {AccountAddress: "0x12"}, {VaultAddress: "nope"}, {PrivateKey: key, AccountAddress: "zz", VaultAddress: vault}}
	for i, cfg := range bad {
		if _, err = NewClient(cfg); err == nil {
			t.Errorf("bad config %d must be rejected", i)
		} else if strings.Contains(err.Error(), "1234") && i == 0 {
			t.Errorf("error echoes key material: %v", err)
		}
	}
}
