package router

import (
	"errors"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

// BuildSwap builds protocol.swap parameters and ABI calldata from a validated
// market frame. It checks the token direction and swap arguments, normalizes the
// signature recovery ID, and includes the original signed words as a price update.
// It trusts the readable book already validated by Marketstream.
// Amounts use token native units. Compute minAmountOut from Quote and the
// caller's slippage tolerance before calling.
//
// The returned parameters can also be passed to protocol.Client.SimulateSwap.
// The caller selects the protocol proxy on value.Pair.ChainId as the destination.
// Signed updates are applied best-effort, so encoding does not guarantee this
// frame will be used. This function performs no RPC, freshness or signer-authority
// checks, and does not send a transaction. Returned data does not alias inputs;
// callers must not mutate inputs while the function is using them.
func BuildSwap(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn, minAmountOut *big.Int, recipient common.Address) (protocol.SwapParams, []byte, error) {
	return buildSwap(protocol.MethodSwap, value, tokenIn, tokenOut, amountIn, minAmountOut, recipient)
}

// BuildSwapExactIn builds protocol.swapExactIn parameters and ABI calldata from
// a validated market frame, with the same validation and ownership rules as
// BuildSwap. This entry point spends the full amountIn: any input left after
// matching and input fees goes to the protocol fee recipient.
// Use protocol.MethodSwapExactIn when simulating the returned parameters.
func BuildSwapExactIn(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn, minAmountOut *big.Int, recipient common.Address) (protocol.SwapParams, []byte, error) {
	return buildSwap(protocol.MethodSwapExactIn, value, tokenIn, tokenOut, amountIn, minAmountOut, recipient)
}

func buildSwap(method protocol.SwapMethod, value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn, minAmountOut *big.Int, recipient common.Address) (protocol.SwapParams, []byte, error) {
	if value == nil || value.Pair == nil || value.Frame == nil {
		return protocol.SwapParams{}, nil, errors.New("router: market frame, pair, and compact frame are required")
	}
	if len(value.Frame.Header) != 32 || len(value.Frame.BidLevels) != 32 || len(value.Frame.AskLevels) != 32 {
		return protocol.SwapParams{}, nil, errors.New("router: compact frame must contain three 32-byte words")
	}
	base, quote := common.HexToAddress(value.Pair.BaseToken), common.HexToAddress(value.Pair.QuoteToken)
	sell := tokenIn == base && tokenOut == quote
	buy := tokenIn == quote && tokenOut == base
	if tokenIn == (common.Address{}) || tokenOut == (common.Address{}) || tokenIn == tokenOut || (!sell && !buy) {
		return protocol.SwapParams{}, nil, errors.New("router: swap tokens must match the frame's base/quote pair in either direction")
	}
	// Normalize v on a copy; EncodeSwapCall validates the signed update.
	signature := append([]byte(nil), value.Signature...)
	if len(signature) == 65 && signature[64] < 2 {
		signature[64] += 27
	}
	signed := frame.SignedFrame{
		Frame: frame.CompactFrame{
			Header:    [32]byte(value.Frame.Header),
			BidLevels: [32]byte(value.Frame.BidLevels),
			AskLevels: [32]byte(value.Frame.AskLevels),
		},
		Maker:     common.HexToAddress(value.MakerAddr),
		Signature: signature,
	}
	params := protocol.SwapParams{
		PairID:       value.Pair.PairId,
		IsBuy:        buy,
		AmountIn:     amountIn,
		MinAmountOut: minAmountOut,
		Maker:        signed.Maker,
		Recipient:    recipient,
		PriceUpdates: []frame.SignedFrame{signed},
	}
	calldata, err := protocol.EncodeSwapCall(method, params)
	if err != nil {
		return protocol.SwapParams{}, nil, err
	}
	// Keep the returned request independent of the caller's cached inputs.
	params.AmountIn = new(big.Int).Set(amountIn)
	params.MinAmountOut = new(big.Int).Set(minAmountOut)
	return params, calldata, nil
}
