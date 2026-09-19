package router

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

func TestQuoteAgeBoundaries(t *testing.T) {
	pair := frame.PairParams{
		PairID: 1, BaseToken: common.HexToAddress("0x1"), QuoteToken: common.HexToAddress("0x2"),
		PriceTickSize: big.NewInt(1), LotSize: big.NewInt(1),
	}
	value := &pb.MarketFrame{
		UpdatedAt: 1000,
		Pair: &pb.PairKey{
			PairId: pair.PairID, BaseToken: pair.BaseToken.Hex(), QuoteToken: pair.QuoteToken.Hex(),
		},
	}
	for _, start := range []uint32{0, 3, 10, math.MaxUint32} {
		cfg := QuoteConfig{Pair: pair, DecayStartOffsetSeconds: start}
		for _, age := range []uint64{0, uint64(start), uint64(start) + 1, math.MaxUint64 - uint64(value.UpdatedAt)} {
			now := uint64(value.UpdatedAt) + age
			err := validateQuote(value, pair.BaseToken, pair.QuoteToken, pair.LotSize, cfg, now)
			if age <= uint64(start) && err != nil || age > uint64(start) && !errors.Is(err, ErrExpiredFrame) {
				t.Fatalf("start %d, age %d: %v", start, age, err)
			}
		}
		if err := validateQuote(value, pair.BaseToken, pair.QuoteToken, pair.LotSize, cfg, uint64(value.UpdatedAt)-1); !errors.Is(err, ErrFutureFrame) {
			t.Fatalf("future frame: %v", err)
		}
	}
}
