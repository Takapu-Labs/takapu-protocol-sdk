package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// swapConfig selects a stream and trade. Pair, fee and decay parameters come
// from the protocol proxy; token decimals come from the ERC-20 contracts.
// AmountIn uses the input token's native units.
type swapConfig struct {
	ChainID     uint64 `json:"chain_id"`
	RPCURL      string `json:"rpc_url"`
	PairID      uint32 `json:"pair_id"`
	Maker       string `json:"maker"`
	Protocol    string `json:"protocol"`
	Recipient   string `json:"recipient"`
	TokenIn     string `json:"token_in"`
	TokenOut    string `json:"token_out"`
	AmountIn    string `json:"amount_in"`
	SlippageBPS uint32 `json:"slippage_bps"`
}

func positiveInteger(name, value string, bits int) (*big.Int, error) {
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return nil, fmt.Errorf("swap.%s must be a positive uint%d decimal string", name, bits)
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > bits {
		return nil, fmt.Errorf("swap.%s must be a positive uint%d decimal string", name, bits)
	}
	return n, nil
}

func (s swapConfig) validate() error {
	endpoint, err := url.Parse(s.RPCURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.Fragment != "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https" && endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
		return errors.New("swap.rpc_url must be an HTTP(S) or WS(S) URL without a fragment")
	}
	if s.ChainID == 0 || s.PairID == 0 || s.SlippageBPS >= 10000 {
		return errors.New("swap requires positive chain_id/pair_id and slippage_bps below 10000")
	}
	for name, value := range map[string]string{
		"maker": s.Maker, "protocol": s.Protocol, "recipient": s.Recipient,
		"token_in": s.TokenIn, "token_out": s.TokenOut,
	} {
		if !common.IsHexAddress(value) || common.HexToAddress(value) == (common.Address{}) {
			return fmt.Errorf("swap.%s must be a nonzero EVM address", name)
		}
	}
	if common.HexToAddress(s.TokenIn) == common.HexToAddress(s.TokenOut) {
		return errors.New("swap token_in and token_out must differ")
	}
	if _, err := positiveInteger("amount_in", s.AmountIn, 256); err != nil {
		return err
	}
	return nil
}

// loadQuoteConfig reads all settings at one block so the quote uses consistent
// fees, decay timing and token scales.
func loadQuoteConfig(ctx context.Context, contract *protocol.Client, rpcClient *ethclient.Client, pairID uint32) (router.QuoteConfig, error) {
	var params router.QuoteConfig
	number, err := rpcClient.BlockNumber(ctx)
	if err != nil {
		return params, err
	}
	block := new(big.Int).SetUint64(number)
	pair, err := contract.EffectivePairConfig(ctx, pairID, block)
	if err != nil {
		return params, err
	}
	if !pair.Active {
		return params, fmt.Errorf("pair %d is inactive", pairID)
	}
	decay, err := contract.GlobalDecayConfig(ctx, block)
	if err != nil {
		return params, err
	}
	// EffectivePairConfig already applies the protocol's global fee switch.
	params = router.QuoteConfig{
		Pair: frame.PairParams{
			PairID: pairID, BaseToken: pair.BaseToken, QuoteToken: pair.QuoteToken,
			PriceTickSize: pair.PriceTickSize, LotSize: pair.LotSize,
		},
		FeeToken: pair.FeeToken, FeeRatePPM: pair.FeeRate,
		DecayStartOffsetSeconds: decay.DecayStartOffsetSeconds,
	}
	for _, token := range []struct {
		address  common.Address
		decimals *uint8
	}{
		{pair.BaseToken, &params.Pair.BaseDecimals},
		{pair.QuoteToken, &params.Pair.QuoteDecimals},
	} {
		raw, err := rpcClient.CallContract(ctx, ethereum.CallMsg{
			To: &token.address, Data: crypto.Keccak256([]byte("decimals()"))[:4],
		}, block)
		if err != nil {
			return router.QuoteConfig{}, fmt.Errorf("token %s decimals: %w", token.address, err)
		}
		decimals := new(big.Int).SetBytes(raw)
		if len(raw) != 32 || decimals.BitLen() > 8 {
			return router.QuoteConfig{}, fmt.Errorf("token %s decimals: invalid uint8 ABI response", token.address)
		}
		*token.decimals = uint8(decimals.Uint64())
	}
	return params, nil
}

func (c config) showSwap(update router.FrameUpdate, params router.QuoteConfig) error {
	s := c.Swap
	amountIn, err := positiveInteger("amount_in", s.AmountIn, 256)
	if err != nil {
		return err
	}
	value := update.Frame
	tokenIn, tokenOut := common.HexToAddress(s.TokenIn), common.HexToAddress(s.TokenOut)
	// Quote rejects frames older than the protocol's decay start. This is a
	// local estimate; simulate the swap to check on-chain execution conditions.
	amountOut, err := router.Quote(value, tokenIn, tokenOut, amountIn, params)
	if err != nil {
		return err
	}
	minOut := new(big.Int).Mul(amountOut, new(big.Int).SetUint64(uint64(10000-s.SlippageBPS)))
	minOut.Quo(minOut, big.NewInt(10000))
	_, calldata, err := router.BuildSwap(value, tokenIn, tokenOut, amountIn, minOut, common.HexToAddress(s.Recipient))
	if err != nil {
		return err
	}
	c.logf("amount-out", "amount_in_cap=%s amount_out=%s min_amount_out=%s (native token units; partial fills possible)", amountIn, amountOut, minOut)
	c.logf("calldata", "chain=%d to=%s data=0x%x", s.ChainID, s.Protocol, calldata)
	return nil
}
