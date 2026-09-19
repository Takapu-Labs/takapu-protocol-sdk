package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type chainState struct {
	Pair         frame.PairParams
	Cutoff       uint64
	Block        string
	FeeToken     common.Address
	FeeRatePPM   uint64
	BaseBalance  string
	QuoteBalance string
}

// loadState reads pair parameters, maker lifecycle and balances at one block.
func loadState(ctx context.Context, rpc *ethclient.Client, c config) (chainState, error) {
	var state chainState
	c.logf("state", "Loading Pair parameters, maker lifecycle and balances at the latest block...")
	head, err := rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return state, err
	}
	if head == nil || head.Number == nil || head.Number.Sign() < 0 {
		return state, errors.New("RPC returned a block without a valid number")
	}
	call := func(address common.Address, signature string, args []byte, size int) ([]byte, error) {
		data := append(crypto.Keccak256([]byte(signature))[:4], args...)
		raw, err := rpc.CallContract(ctx, ethereum.CallMsg{To: &address, Data: data}, head.Number)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", signature, err)
		}
		if len(raw) != size {
			return nil, fmt.Errorf("%s: unexpected ABI response length %d", signature, len(raw))
		}
		return raw, nil
	}
	maker, proxy := common.HexToAddress(c.Maker), common.HexToAddress(c.Protocol)
	raw, err := call(proxy, "makerConfigs(address)", common.LeftPadBytes(maker.Bytes(), 32), 64)
	if err != nil {
		return state, err
	}
	state.Cutoff = new(big.Int).SetBytes(raw[:32]).Uint64()
	c.logf("state", "Maker lifecycle cutoff=%s", time.Unix(int64(state.Cutoff), 0).Format(time.RFC3339))
	raw, err = call(proxy, "pairConfigs(uint32)", common.LeftPadBytes(new(big.Int).SetUint64(uint64(c.Pair.ID)).Bytes(), 32), 224)
	if err != nil {
		return state, err
	}
	word := func(i int) []byte { return raw[32*i : 32*(i+1)] }
	pair := frame.PairParams{
		PairID:        c.Pair.ID,
		BaseToken:     common.BytesToAddress(word(0)),
		QuoteToken:    common.BytesToAddress(word(1)),
		PriceTickSize: new(big.Int).SetBytes(word(2)),
		LotSize:       new(big.Int).SetBytes(word(3)),
	}
	state.FeeToken, state.FeeRatePPM = common.BytesToAddress(word(5)), new(big.Int).SetBytes(word(6)).Uint64()
	for _, token := range []struct {
		address  common.Address
		decimals *uint8
		balance  *string
	}{
		{address: pair.BaseToken, decimals: &pair.BaseDecimals, balance: &state.BaseBalance},
		{address: pair.QuoteToken, decimals: &pair.QuoteDecimals, balance: &state.QuoteBalance},
	} {
		raw, err := call(token.address, "decimals()", nil, 32)
		if err != nil {
			return state, err
		}
		*token.decimals = uint8(new(big.Int).SetBytes(raw).Uint64())
		raw, err = call(token.address, "balanceOf(address)", common.LeftPadBytes(maker.Bytes(), 32), 32)
		if err != nil {
			return state, err
		}
		*token.balance = new(big.Int).SetBytes(raw).String()
	}
	state.Pair, state.Block = pair, head.Number.String()
	c.logf("pair", "Pair %d loaded; base=%s (%d decimals), quote=%s (%d decimals)", pair.PairID, pair.BaseToken.Hex(), pair.BaseDecimals, pair.QuoteToken.Hex(), pair.QuoteDecimals)
	c.logf("pair", "1 lot=%s base; raw priceTickSize=%s; fee=%d ppm (%s%%), fee token=%s", tokenUnits(pair.LotSize.String(), pair.BaseDecimals), pair.PriceTickSize, state.FeeRatePPM, tokenUnits(fmt.Sprint(state.FeeRatePPM), 4), state.FeeToken.Hex())
	return state, nil
}
