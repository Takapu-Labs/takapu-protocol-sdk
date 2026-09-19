package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// SwapEvent reports the payer's actual total spend and recipient's net receipt,
// not MakerFill's matched input and gross output.
type SwapEvent struct {
	Sender    common.Address
	TokenIn   common.Address
	TokenOut  common.Address
	AmountIn  *big.Int
	AmountOut *big.Int
	PairID    uint32
	Maker     common.Address
	Recipient common.Address
	Raw       types.Log
}

// WaitReceipt waits for inclusion only. A failed receipt is returned normally;
// inspect Status, and implement any additional confirmation policy separately.
func (c *Client) WaitReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	if ctx == nil {
		return nil, errors.New("protocol: context is nil")
	}
	if txHash == (common.Hash{}) {
		return nil, errors.New("protocol: transaction hash is zero")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receipt, err := c.rpc.TransactionReceipt(ctx, txHash)
		if err == nil {
			return receipt, nil
		}
		if !errors.Is(err, ethereum.NotFound) {
			return nil, err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// ParseSwapReceipt accepts only successful receipts and this client's proxy's
// Swap logs. Other emitters/events are ignored; malformed matching logs fail.
func (c *Client) ParseSwapReceipt(receipt *types.Receipt) ([]SwapEvent, error) {
	if receipt == nil {
		return nil, errors.New("protocol: receipt is nil")
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil, errors.New("protocol: cannot parse swaps from a failed receipt")
	}
	event := contractABI.Events["Swap"]
	indexed := make(abi.Arguments, 0, 3)
	for _, input := range event.Inputs {
		if input.Indexed {
			indexed = append(indexed, input)
		}
	}
	result := make([]SwapEvent, 0)
	for i, log := range receipt.Logs {
		if log == nil || log.Address != c.cfg.Proxy || len(log.Topics) == 0 || log.Topics[0] != event.ID {
			continue
		}
		if log.Removed {
			return nil, fmt.Errorf("protocol: receipt log %d is removed", i)
		}
		if len(log.Topics) != 4 || len(log.Data) != 5*32 {
			return nil, fmt.Errorf("protocol: malformed Swap log %d", i)
		}
		decoded := make(map[string]any)
		if err := event.Inputs.NonIndexed().UnpackIntoMap(decoded, log.Data); err != nil {
			return nil, fmt.Errorf("protocol: decode Swap log %d: %w", i, err)
		}
		if err := abi.ParseTopicsIntoMap(decoded, indexed, log.Topics[1:]); err != nil {
			return nil, fmt.Errorf("protocol: decode Swap topics %d: %w", i, err)
		}
		raw := *log
		raw.Data = append([]byte(nil), log.Data...)
		raw.Topics = append([]common.Hash(nil), log.Topics...)
		result = append(result, SwapEvent{
			Sender:    decoded["sender"].(common.Address),
			TokenIn:   decoded["tokenIn"].(common.Address),
			TokenOut:  decoded["tokenOut"].(common.Address),
			AmountIn:  decoded["amountIn"].(*big.Int),
			AmountOut: decoded["amountOut"].(*big.Int),
			PairID:    decoded["pairId"].(uint32),
			Maker:     decoded["maker"].(common.Address),
			Recipient: decoded["recipient"].(common.Address),
			Raw:       raw,
		})
	}
	return result, nil
}
