package frame

import (
	"errors"
	"math/big"
	"strings"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
)

// The backend accepts at most 64 ASCII characters per price or amount.
const maxDecimalLength = 64

func humanLevels(levels []Level, base uint32, bid bool, pair PairParams) ([]*pb.PriceLevel, error) {
	out := make([]*pb.PriceLevel, 0, len(levels))
	// Contract prices use 18 fixed-point decimals in quote/base native units.
	// Convert to human token units before rendering the exact decimal value.
	priceScale := 18 + int(pair.QuoteDecimals) - int(pair.BaseDecimals)
	for _, level := range levels {
		tick := uint64(base) + uint64(level.Offset)
		if bid {
			tick = uint64(base) - uint64(level.Offset)
		}
		priceUnits := new(big.Int).Mul(new(big.Int).SetUint64(tick), pair.PriceTickSize)
		amountUnits := new(big.Int).Mul(new(big.Int).SetUint64(uint64(level.QtyLots)), pair.LotSize)
		price := decimal(priceUnits, priceScale)
		amount := decimal(amountUnits, int(pair.BaseDecimals))
		// Never round a readable value to fit: it must match the signed integers.
		if len(price) > maxDecimalLength || len(amount) > maxDecimalLength {
			return nil, errors.New("frame: exact readable price or amount exceeds protocol's 64-character limit")
		}
		out = append(out, &pb.PriceLevel{Price: price, Amount: amount})
	}
	return out, nil
}

// decimal formats n/10^scale exactly, including a negative scale.
func decimal(n *big.Int, scale int) string {
	s := n.String()
	if scale <= 0 {
		return s + strings.Repeat("0", -scale)
	}
	if len(s) <= scale {
		s = strings.Repeat("0", scale+1-len(s)) + s
	}
	split := len(s) - scale
	s = s[:split] + "." + s[split:]
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
