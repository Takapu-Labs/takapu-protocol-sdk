package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/common"
)

func validateBlock(ctx context.Context, block *big.Int) error {
	if ctx == nil {
		return errors.New("protocol: context is nil")
	}
	if block != nil && block.Sign() < 0 {
		return errors.New("protocol: block number must be nonnegative")
	}
	return nil
}

func validateSigned(value frame.SignedFrame) error {
	if value.Maker == (common.Address{}) {
		return fmt.Errorf("protocol: price update maker is zero")
	}
	if err := frame.ValidateCompactFrame(value.Frame); err != nil {
		return fmt.Errorf("protocol: price update: %w", err)
	}
	if _, err := frame.NormalizeSignature(value.Signature); err != nil {
		return fmt.Errorf("protocol: price update signature: %w", err)
	}
	// The contract passes v directly to ecrecover; calldata must use 27/28.
	if value.Signature[64] != 27 && value.Signature[64] != 28 {
		return errors.New("protocol: price update signature recovery ID must be 27/28; use frame.NormalizeSignature before encoding")
	}
	return nil
}

func uint256(name string, v *big.Int, positive bool) error {
	if v == nil || v.Sign() < 0 || v.BitLen() > 256 || (positive && v.Sign() == 0) {
		requirement := "nonnegative"
		if positive {
			requirement = "positive"
		}
		return fmt.Errorf("protocol: %s must be a %s uint256", name, requirement)
	}
	return nil
}
