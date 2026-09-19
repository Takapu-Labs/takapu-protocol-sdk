// Package frame prepares, signs and validates Takapu codec-v1 quotes without
// network access, clocks, private-key storage or version management.
package frame

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// PairParams holds on-chain pair parameters. PriceTickSize and LotSize must be
// positive uint128 integers. Token decimals describe human-readable wire values.
type PairParams struct {
	PairID                      uint32
	BaseToken, QuoteToken       common.Address
	BaseDecimals, QuoteDecimals uint8
	PriceTickSize, LotSize      *big.Int
}

// Pair is an alias for PairParams.
type Pair = PairParams

// FrameSpec describes an unsigned quote, including its signing domain and
// readable pair metadata. Set its CodecVersion field to [CodecVersion]. The
// caller owns timestamp and version progression; empty books withdraw quotes.
type FrameSpec struct {
	ChainID                 uint64
	Protocol, Maker         common.Address
	Pair                    PairParams
	BaseTick                uint32
	Bids, Asks              []Level
	UpdatedAt, MajorVersion uint32
	MinorVersion            uint8
	CodecVersion            uint16
}

// ValidatePair checks static metadata and uint128 scales, without chain access.
func ValidatePair(pair PairParams) error {
	if pair.PairID == 0 {
		return errors.New("frame: pair ID must be positive")
	}
	if pair.BaseToken == (common.Address{}) || pair.QuoteToken == (common.Address{}) || pair.BaseToken == pair.QuoteToken {
		return errors.New("frame: pair token addresses must be distinct and nonzero")
	}
	scales := []struct {
		name  string
		value *big.Int
	}{
		{name: "price tick size", value: pair.PriceTickSize},
		{name: "lot size", value: pair.LotSize},
	}
	for _, scale := range scales {
		if scale.value == nil || scale.value.Sign() <= 0 || scale.value.BitLen() > 128 {
			return fmt.Errorf("frame: %s must be a positive uint128", scale.name)
		}
	}
	return nil
}
