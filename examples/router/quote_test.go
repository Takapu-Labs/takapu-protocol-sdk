package main

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestShowSwapUsesConfiguredDecayStart(t *testing.T) {
	cfg, update, pair := quoteFixture(t)
	logged, err := captureShowSwap(t, cfg, update, pair)
	if err != nil {
		t.Fatal(err)
	}
	amounts := "amount_in_cap=1000 amount_out=1000 min_amount_out=990"
	if !strings.Contains(logged, amounts) {
		t.Fatalf("missing expected amounts %q in log:\n%s", amounts, logged)
	}
	selector := crypto.Keccak256([]byte("swap(uint32,bool,uint256,uint256,address,address,((bytes32,bytes32,bytes32),address,bytes)[])"))[:4]
	calldata := fmt.Sprintf("[calldata] chain=1 to=%s data=0x%x", cfg.Swap.Protocol, selector)
	if !strings.Contains(logged, calldata) {
		t.Fatalf("missing swap calldata in log:\n%s", logged)
	}
}

func TestShowSwapUsesReadablePricesAndFetchedScalesAndFees(t *testing.T) {
	cfg, update, pair := quoteFixture(t)
	for _, test := range []struct {
		name                  string
		edit                  func(*router.QuoteConfig)
		amountOut, minimumOut int64
	}{
		{"tick scale is already decoded", func(p *router.QuoteConfig) { p.Pair.PriceTickSize = big.NewInt(2_000_000_000_000_000_000) }, 1000, 990},
		{"base decimals", func(p *router.QuoteConfig) { p.Pair.BaseDecimals = 2 }, 10, 9},
		{"quote decimals", func(p *router.QuoteConfig) { p.Pair.QuoteDecimals = 2 }, 100000, 99000},
		{"whole lot truncation", func(p *router.QuoteConfig) { p.Pair.LotSize = big.NewInt(3) }, 999, 989},
		{"input pair fee", func(p *router.QuoteConfig) { p.FeeToken, p.FeeRatePPM = p.Pair.BaseToken, 100_000 }, 909, 899},
		{"output pair fee", func(p *router.QuoteConfig) { p.FeeToken, p.FeeRatePPM = p.Pair.QuoteToken, 100_000 }, 900, 891},
		{"zero pair fee", func(p *router.QuoteConfig) { p.FeeToken, p.FeeRatePPM = common.Address{}, 0 }, 1000, 990},
	} {
		t.Run(test.name, func(t *testing.T) {
			fetched := pair
			test.edit(&fetched)
			logged, err := captureShowSwap(t, cfg, update, fetched)
			if err != nil {
				t.Fatal(err)
			}
			amounts := fmt.Sprintf("amount_in_cap=1000 amount_out=%d min_amount_out=%d", test.amountOut, test.minimumOut)
			if !strings.Contains(logged, amounts) {
				t.Fatalf("missing amounts from fetched pair %q in log:\n%s", amounts, logged)
			}
		})
	}
}

func TestShowSwapRejectsExpiredOrMismatchedPair(t *testing.T) {
	cfg, update, pair := quoteFixture(t)
	for name, edit := range map[string]func(*router.QuoteConfig){
		"expired":        func(p *router.QuoteConfig) { p.DecayStartOffsetSeconds = 5 },
		"token mismatch": func(p *router.QuoteConfig) { p.Pair.BaseToken = common.HexToAddress(cfg.Swap.Recipient) },
	} {
		t.Run(name, func(t *testing.T) {
			fetched := pair
			edit(&fetched)
			logged, err := captureShowSwap(t, cfg, update, fetched)
			if err == nil || strings.Contains(logged, "[calldata]") {
				t.Fatalf("invalid pair produced calldata: err=%v log=%s", err, logged)
			}
		})
	}
}

func captureShowSwap(t *testing.T, cfg config, update router.FrameUpdate, pair router.QuoteConfig) (string, error) {
	t.Helper()
	// showSwap logs to stderr; all callers intentionally run serially.
	output, err := os.CreateTemp(t.TempDir(), "quote-log")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = output
	defer func() {
		os.Stderr = original
		_ = output.Close()
	}()
	quoteErr := cfg.showSwap(update, pair)
	logged, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(logged), quoteErr
}

func quoteFixture(t *testing.T) (config, router.FrameUpdate, router.QuoteConfig) {
	t.Helper()
	spec := frame.FrameSpec{
		ChainID:  1,
		Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Maker:    common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Pair: frame.PairParams{
			PairID:        7,
			BaseToken:     common.HexToAddress("0x0000000000000000000000000000000000000001"),
			QuoteToken:    common.HexToAddress("0x0000000000000000000000000000000000000002"),
			PriceTickSize: big.NewInt(1_000_000_000_000_000_000), LotSize: big.NewInt(1),
		},
		BaseTick: 2,
		Bids:     []frame.Level{{Offset: 1, QtyLots: 1000}},
		Asks:     []frame.Level{{Offset: 1, QtyLots: 1000}},
		// Leave room on both sides of the five-second rejection and the
		// configured thirty-second acceptance limits when using wall time.
		UpdatedAt: uint32(time.Now().Unix() - 8), MajorVersion: 1, CodecVersion: frame.CodecVersion,
	}
	prepared, err := frame.Prepare(spec)
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := prepared.SigningPayload()
	signature, err := crypto.Sign(payload.Digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	value, err := prepared.AttachSignature(signature, crypto.PubkeyToAddress(key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{Swap: &swapConfig{
		ChainID: spec.ChainID, PairID: spec.Pair.PairID, Maker: spec.Maker.Hex(), Protocol: spec.Protocol.Hex(),
		Recipient: "0x3333333333333333333333333333333333333333",
		TokenIn:   spec.Pair.BaseToken.Hex(), TokenOut: spec.Pair.QuoteToken.Hex(),
		AmountIn: "1000", SlippageBPS: 100,
	}}
	return cfg, router.FrameUpdate{
		Key:   router.FrameKey{ChainID: spec.ChainID, PairID: spec.Pair.PairID, Maker: spec.Maker},
		Frame: value, Generation: 1, SubscriptionRevision: 1,
	}, router.QuoteConfig{Pair: spec.Pair, DecayStartOffsetSeconds: 30}
}
