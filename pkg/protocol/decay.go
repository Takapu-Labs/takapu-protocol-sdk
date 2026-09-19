package protocol

import (
	"errors"
	"fmt"
	"math/big"
)

var (
	// ErrInvalidDecayConfig indicates that on-chain decay constraints are violated.
	ErrInvalidDecayConfig = errors.New("protocol: invalid global decay configuration")
	// ErrFutureFrame indicates a frame timestamp later than the evaluation time.
	ErrFutureFrame = errors.New("protocol: frame timestamp is in the future")
	// ErrExpiredFrame indicates a frame whose age has reached the configured limit.
	ErrExpiredFrame = errors.New("protocol: frame has expired")
)

// GlobalDecayConfig is the protocol's global frame-age and decay configuration.
// Offsets and MaxAge are measured in seconds since the frame timestamp;
// MaxDecayPpm is measured in parts per million.
type GlobalDecayConfig struct {
	DecayStartOffsetSeconds uint32
	DecayEndOffsetSeconds   uint32
	MaxAge                  uint32
	MaxDecayPpm             uint32
}

// Validate checks the exact on-chain configuration constraints.
func (c GlobalDecayConfig) Validate() error {
	if c.MaxAge == 0 || c.DecayStartOffsetSeconds >= c.DecayEndOffsetSeconds || c.DecayEndOffsetSeconds >= c.MaxAge || c.MaxDecayPpm >= PPMDenominator {
		return ErrInvalidDecayConfig
	}
	return nil
}

// DecayPPM computes floor-linear decay at an explicit Unix timestamp. Even when
// decay is disabled, future or expired frames are rejected. It reads no clock.
func DecayPPM(updatedAt uint32, at uint64, cfg GlobalDecayConfig) (uint32, error) {
	if err := cfg.Validate(); err != nil {
		return 0, err
	}
	if at < uint64(updatedAt) {
		return 0, ErrFutureFrame
	}
	age := at - uint64(updatedAt)
	if age >= uint64(cfg.MaxAge) {
		return 0, ErrExpiredFrame
	}
	start, end := uint64(cfg.DecayStartOffsetSeconds), uint64(cfg.DecayEndOffsetSeconds)
	if age <= start {
		return 0, nil
	}
	if age >= end {
		return cfg.MaxDecayPpm, nil
	}
	return uint32(uint64(cfg.MaxDecayPpm) * (age - start) / (end - start)), nil
}

// ApplyDecay returns floor(grossAmountOut*decayPpm/1e6) and the amount remaining
// after that deduction. Both results are fresh objects. Output fees have not yet
// been deducted from decayedGross.
func ApplyDecay(grossAmountOut *big.Int, decayPpm uint32) (decayAmount, decayedGross *big.Int, err error) {
	if err = uint256("grossAmountOut", grossAmountOut, false); err != nil {
		return nil, nil, err
	}
	if decayPpm >= PPMDenominator {
		return nil, nil, fmt.Errorf("protocol: decay PPM must be below %d", PPMDenominator)
	}
	decayAmount = new(big.Int).Mul(grossAmountOut, new(big.Int).SetUint64(uint64(decayPpm)))
	decayAmount.Quo(decayAmount, new(big.Int).SetUint64(uint64(PPMDenominator)))
	return decayAmount, new(big.Int).Sub(grossAmountOut, decayAmount), nil
}
