package maker

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
)

var quoteDecimalPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// quoteUnits converts a decimal to ticks/lots, rounding only the final count.
// The exact count is value * 10^scale / step; roundUp chooses ceil instead of
// floor. Already aligned values are unchanged.
// Contract prices have 18 fixed-point decimals in native quote/base units.
// Positive values round down unless roundUp is set for an ask price.
func quoteUnits(value string, scale int, step *big.Int, roundUp bool) (uint32, error) {
	if len(value) == 0 || len(value) > 64 || !quoteDecimalPattern.MatchString(value) {
		return 0, errors.New("must be a positive plain decimal of at most 64 characters")
	}
	v, ok := new(big.Rat).SetString(value)
	if !ok || v.Sign() <= 0 {
		return 0, errors.New("must be positive")
	}
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(scale))), nil)
	if scale >= 0 {
		v.Mul(v, new(big.Rat).SetInt(power))
	} else {
		v.Quo(v, new(big.Rat).SetInt(power))
	}
	v.Quo(v, new(big.Rat).SetInt(step))
	count, remainder := new(big.Int).QuoRem(v.Num(), v.Denom(), new(big.Int))
	if roundUp && remainder.Sign() != 0 {
		count.Add(count, big.NewInt(1))
	}
	if count.Sign() <= 0 || count.Cmp(new(big.Int).SetUint64(uint64(frame.MaxTick))) > 0 {
		return 0, errors.New("rounded tick/lot count must be a positive uint24")
	}
	return uint32(count.Uint64()), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func quoteLevels(levels []PriceLevel, pair PairParams, bid bool) ([]frame.Level, error) {
	if len(levels) > frame.MaxLevels {
		return nil, errors.New("at most five levels are allowed")
	}
	out := make([]frame.Level, len(levels))
	for i, level := range levels {
		tick, err := quoteUnits(level.Price, 18+int(pair.QuoteDecimals)-int(pair.BaseDecimals), pair.PriceTickSize, !bid)
		if err != nil {
			return nil, fmt.Errorf("level %d price: %w", i, err)
		}
		lots, err := quoteUnits(level.Amount, int(pair.BaseDecimals), pair.LotSize, false)
		if err != nil {
			return nil, fmt.Errorf("level %d amount: %w", i, err)
		}
		// Offset temporarily holds the rounded absolute price tick. quoteSpec
		// chooses BaseTick and replaces this value with a relative offset.
		out[i] = frame.Level{Offset: tick, QtyLots: lots}
		if i > 0 && ((bid && tick >= out[i-1].Offset) || (!bid && tick <= out[i-1].Offset)) {
			return nil, errors.New("rounded prices must be strictly ordered, best first")
		}
	}
	return out, nil
}

func quoteSpec(params PublishParams) (frame.FrameSpec, error) {
	if err := frame.ValidatePair(params.Pair); err != nil {
		return frame.FrameSpec{}, err
	}
	bids, err := quoteLevels(params.Bids, params.Pair, true)
	if err != nil {
		return frame.FrameSpec{}, fmt.Errorf("maker: bids: %w", err)
	}
	asks, err := quoteLevels(params.Asks, params.Pair, false)
	if err != nil {
		return frame.FrameSpec{}, fmt.Errorf("maker: asks: %w", err)
	}
	if len(bids) > 0 && len(asks) > 0 {
		// quoteLevels already validated these decimals. Outward rounding must
		// not silently repair a locked or crossed input book.
		bestBid, _ := new(big.Rat).SetString(params.Bids[0].Price)
		bestAsk, _ := new(big.Rat).SetString(params.Asks[0].Price)
		if bestBid.Cmp(bestAsk) >= 0 || bids[0].Offset >= asks[0].Offset {
			return frame.FrameSpec{}, errors.New("maker: best bid must be below best ask")
		}
	}
	// base is the common price reference in whole ticks, stored as BaseTick.
	// Choose the best bid, or the best ask when there are no bids, so every
	// relative offset is nonnegative. An empty withdrawal frame still needs a
	// positive BaseTick, so use 1. Offset currently holds absolute price ticks.
	base := uint32(1)
	if len(bids) > 0 {
		base = bids[0].Offset
	} else if len(asks) > 0 {
		base = asks[0].Offset
	}
	// Convert absolute ticks to the codec's relative offsets:
	// bidTick = base - Offset; askTick = base + Offset. Prices do not change.
	for i := range bids {
		bids[i].Offset = base - bids[i].Offset
	}
	for i := range asks {
		asks[i].Offset -= base
	}
	return frame.FrameSpec{
		ChainID: params.ChainID, Protocol: params.Protocol, Maker: params.Maker,
		Pair: params.Pair, BaseTick: base, Bids: bids, Asks: asks,
		UpdatedAt: params.UpdatedAt, MajorVersion: params.MajorVersion,
		MinorVersion: params.MinorVersion, CodecVersion: frame.CodecVersion,
	}, nil
}
