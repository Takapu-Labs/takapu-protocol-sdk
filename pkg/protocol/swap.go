package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// SwapMethod identifies a protocol contract's swap entry point.
type SwapMethod string

// SwapParams describes a swap and optional signed price updates. Amounts are in
// token native units; AmountIn must be positive and MinAmountOut must be non-nil.
// Callers must not mutate pointer or slice fields while an operation uses them.
type SwapParams struct {
	PairID uint32
	// IsBuy selects quote-to-base (true) or base-to-quote (false).
	IsBuy    bool
	AmountIn *big.Int
	// MinAmountOut is the minimum receipt after output fees and decay.
	MinAmountOut *big.Int
	Maker        common.Address
	Recipient    common.Address
	// PriceUpdates are applied best-effort; including a frame does not guarantee
	// that the swap uses it. An empty slice uses the existing on-chain prices.
	PriceUpdates []frame.SignedFrame
}

// SwapResult reports a simulation's actual spend and net output in token native
// units. A successful simulation does not guarantee later execution.
type SwapResult struct {
	ActualAmountIn *big.Int
	AmountOut      *big.Int
}

// EncodeSwapCall encodes all three swap entry points without performing RPC or
// changing signed frame bytes. Signatures must use v=27/28. Callback calldata must
// be used by an authorized contract payer, whose actual entry must be simulated
// separately.
func EncodeSwapCall(method SwapMethod, params SwapParams) ([]byte, error) {
	if method != MethodSwap && method != MethodSwapExactIn && method != MethodSwapWithCallback {
		return nil, fmt.Errorf("protocol: unsupported swap method %q", method)
	}
	if params.PairID == 0 {
		return nil, fmt.Errorf("protocol: pair ID is zero")
	}
	if err := uint256("AmountIn", params.AmountIn, true); err != nil {
		return nil, err
	}
	if err := uint256("MinAmountOut", params.MinAmountOut, false); err != nil {
		return nil, err
	}
	if params.Maker == (common.Address{}) || params.Recipient == (common.Address{}) {
		return nil, fmt.Errorf("protocol: maker and recipient must be nonzero")
	}
	for i, value := range params.PriceUpdates {
		if err := validateSigned(value); err != nil {
			return nil, fmt.Errorf("protocol: PriceUpdates[%d]: %w", i, err)
		}
	}
	return contractABI.Pack(
		string(method),
		params.PairID,
		params.IsBuy,
		params.AmountIn,
		params.MinAmountOut,
		params.Maker,
		params.Recipient,
		params.PriceUpdates,
	)
}

// SimulateSwap calls the real swap using the payer's actual balance/allowance.
// Returned AmountOut is already net of fees and decay. A nil blockNumber selects
// the latest block. Simulation is not a fill.
func (c *Client) SimulateSwap(ctx context.Context, from common.Address, method SwapMethod, params SwapParams, blockNumber *big.Int) (SwapResult, error) {
	if err := validateBlock(ctx, blockNumber); err != nil {
		return SwapResult{}, err
	}
	if method != MethodSwap && method != MethodSwapExactIn {
		return SwapResult{}, errors.New("protocol: simulation supports only swap and swapExactIn; simulate the payer contract's actual entry for callbacks")
	}
	if from == (common.Address{}) {
		return SwapResult{}, errors.New("protocol: simulation payer is zero")
	}
	data, err := EncodeSwapCall(method, params)
	if err != nil {
		return SwapResult{}, err
	}
	raw, err := c.rpc.CallContract(ctx, ethereum.CallMsg{From: from, To: &c.cfg.Proxy, Data: data}, blockNumber)
	if err != nil {
		return SwapResult{}, ParseRevert(err)
	}
	values, err := contractABI.Unpack(string(method), raw)
	if err != nil {
		return SwapResult{}, fmt.Errorf("protocol: decode swap result: %w", err)
	}
	if len(values) != 2 {
		return SwapResult{}, errors.New("protocol: malformed swap result")
	}
	return SwapResult{
		ActualAmountIn: values[0].(*big.Int),
		AmountOut:      values[1].(*big.Int),
	}, nil
}

// Swap signs and broadcasts a swap, or only signs when opts.NoSend is true.
// It leaves opts unchanged. If broadcasting fails, the signed transaction is
// returned with the error so its hash can be used to resolve the outcome.
func (c *Client) Swap(opts *bind.TransactOpts, params SwapParams) (*types.Transaction, error) {
	return c.swap(opts, MethodSwap, params)
}

// SwapExactIn uses the full-input swap entry point. Any input left after matching
// and input fees goes to the protocol fee recipient. Transaction options and
// broadcast errors follow the same rules as Swap.
func (c *Client) SwapExactIn(opts *bind.TransactOpts, params SwapParams) (*types.Transaction, error) {
	return c.swap(opts, MethodSwapExactIn, params)
}

func (c *Client) swap(opts *bind.TransactOpts, method SwapMethod, params SwapParams) (*types.Transaction, error) {
	data, err := EncodeSwapCall(method, params)
	if err != nil {
		return nil, err
	}
	return c.transact(opts, c.cfg.Proxy, data)
}
