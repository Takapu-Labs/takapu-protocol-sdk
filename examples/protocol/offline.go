package main

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func runOffline() error {
	// 1. The caller supplies the pair, deployment, integer book, time and version.
	// These addresses and this historical timestamp are offline fixture values.
	spec := frame.FrameSpec{
		ChainID:  1,
		Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Maker:    common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Pair: frame.PairParams{
			PairID:        7,
			BaseToken:     common.HexToAddress("0x0000000000000000000000000000000000000001"),
			QuoteToken:    common.HexToAddress("0x0000000000000000000000000000000000000002"),
			BaseDecimals:  18,
			QuoteDecimals: 6,
			PriceTickSize: big.NewInt(10_000),
			LotSize:       big.NewInt(10_000_000_000_000_000),
		},
		BaseTick:     500,
		Bids:         []frame.Level{{Offset: 2, QtyLots: 100}},
		Asks:         []frame.Level{{Offset: 3, QtyLots: 50}},
		UpdatedAt:    1_700_000_000,
		MajorVersion: 1,
		MinorVersion: 0,
		CodecVersion: frame.CodecVersion,
	}
	prepared, err := frame.Prepare(spec)
	if err != nil {
		return err
	}
	payload := prepared.SigningPayload()
	fmt.Printf("1. Prepared pair=%d version=(%d,%d) digest=%s\n", spec.Pair.PairID, spec.MajorVersion, spec.MinorVersion, payload.Digest)

	// 2. Stand in for an external signer with a fresh, disposable in-memory key.
	// Sign the digest directly; do not add personal_sign or hash it again.
	key, err := crypto.GenerateKey()
	if err != nil {
		return err
	}
	expectedSigner := crypto.PubkeyToAddress(key.PublicKey)
	signature, err := crypto.Sign(payload.Digest[:], key)
	if err != nil {
		return err
	}
	marketFrame, err := prepared.AttachSignature(signature, expectedSigner)
	if err != nil {
		return err
	}
	fmt.Printf("2. Attached signature from %s\n", expectedSigner)

	// 3. Keep the original compact words and signature when building calldata.
	// Use the prepared payload and attached signature directly.
	signed := frame.SignedFrame{
		Frame: payload.Frame, Maker: payload.Maker, Signature: marketFrame.Signature,
	}
	if err := frame.VerifySignature(payload.Domain, signed, expectedSigner); err != nil {
		return fmt.Errorf("verify original quote: %w", err)
	}
	fmt.Println("3. Original quote verified")

	// 4. An existing signature cannot authorize a different chain, proxy,
	// maker or book. Each change is independently checked by the SDK.
	otherChain := payload.Domain
	otherChain.ChainID++
	otherProxy := payload.Domain
	otherProxy.Protocol = common.HexToAddress("0x3333333333333333333333333333333333333333")
	otherMaker := signed
	otherMaker.Maker = common.HexToAddress("0x4444444444444444444444444444444444444444")
	changedSpec := spec
	changedSpec.BaseTick++
	changed, err := frame.Prepare(changedSpec)
	if err != nil {
		return err
	}
	otherBook := signed
	otherBook.Frame = changed.SigningPayload().Frame
	for _, attempt := range []struct {
		name   string
		domain frame.SigningDomain
		quote  frame.SignedFrame
	}{
		{name: "chain", domain: otherChain, quote: signed},
		{name: "proxy", domain: otherProxy, quote: signed},
		{name: "maker", domain: payload.Domain, quote: otherMaker},
		{name: "book", domain: payload.Domain, quote: otherBook},
	} {
		if err := frame.VerifySignature(attempt.domain, attempt.quote, expectedSigner); err == nil {
			return fmt.Errorf("changed %s unexpectedly passed signature verification", attempt.name)
		}
		fmt.Printf("4. Changed %s rejected\n", attempt.name)
	}

	// 5. Decay uses explicit seconds and integer token amounts. This fixture
	// evaluates the quote 11 seconds after its timestamp, without reading a clock.
	decayConfig := protocol.GlobalDecayConfig{
		DecayStartOffsetSeconds: 10,
		DecayEndOffsetSeconds:   13,
		MaxAge:                  20,
		MaxDecayPpm:             100_000,
	}
	decay, err := protocol.DecayPPM(spec.UpdatedAt, uint64(spec.UpdatedAt)+11, decayConfig)
	if err != nil {
		return err
	}
	deduction, remaining, err := protocol.ApplyDecay(big.NewInt(1_000_000), decay)
	if err != nil {
		return err
	}
	fmt.Printf("5. Decay=%d ppm: gross=1000000 deduction=%s remaining=%s (before output fees)\n", decay, deduction, remaining)
	if _, err := protocol.DecayPPM(spec.UpdatedAt, uint64(spec.UpdatedAt)+uint64(decayConfig.MaxAge), decayConfig); !errors.Is(err, protocol.ErrExpiredFrame) {
		return fmt.Errorf("expected an expired frame at MaxAge, got %v", err)
	}
	fmt.Println("   Quote rejected at MaxAge")

	// 6. Sell base for quote with the original signed price update. AmountIn is
	// in base-token units; MinAmountOut is net quote-token units after fees and
	// decay. These illustrative amounts do not establish an executable trade.
	params := protocol.SwapParams{
		PairID:       spec.Pair.PairID,
		IsBuy:        false,
		AmountIn:     big.NewInt(1_000_000_000_000_000_000),
		MinAmountOut: big.NewInt(4_000_000),
		Maker:        spec.Maker,
		Recipient:    common.HexToAddress("0x5555555555555555555555555555555555555555"),
		PriceUpdates: []frame.SignedFrame{signed},
	}
	for _, method := range []protocol.SwapMethod{
		protocol.MethodSwap,
		protocol.MethodSwapExactIn,
		protocol.MethodSwapWithCallback,
	} {
		calldata, err := protocol.EncodeSwapCall(method, params)
		if err != nil {
			return fmt.Errorf("encode %s: %w", method, err)
		}
		fmt.Printf("6. %s to=%s selector=0x%x bytes=%d\n", method, spec.Protocol, calldata[:4], len(calldata))
	}
	fmt.Println("   swap may partially fill; swapExactIn spends the full input, including any remainder sent to the fee recipient.")
	fmt.Println("   Callback calldata requires an authorized contract payer; simulate that contract's actual entry separately.")

	return nil
}
