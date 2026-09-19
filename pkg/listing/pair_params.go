package listing

import (
	"math/big"
	"strings"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
)

// FrameParams parses raw Listing sizes directly into frame parameters, without
// rescaling. The returned integers are independent values on every call.
//
// This validates static frame metadata only. Callers must check Active, Gray,
// the chain and protocol deployment, and compare tokens and raw sizes with
// pairConfigs plus token decimals() before signing or using quotes. Listing
// results are not an on-chain snapshot.
func (p Pair) FrameParams() (frame.PairParams, error) {
	tick, err := parseRawSize(p.PriceTickSize, fieldPriceTickSize)
	if err != nil {
		return frame.PairParams{}, err
	}
	lot, err := parseRawSize(p.LotSize, fieldLotSize)
	if err != nil {
		return frame.PairParams{}, err
	}
	params := frame.PairParams{
		PairID: p.PairID, BaseToken: p.BaseToken, QuoteToken: p.QuoteToken,
		BaseDecimals: p.BaseDecimals, QuoteDecimals: p.QuoteDecimals,
		PriceTickSize: tick, LotSize: lot,
	}
	if err := frame.ValidatePair(params); err != nil {
		return frame.PairParams{}, configError("pair", err.Error())
	}
	return params, nil
}

func parseRawSize(value, name string) (*big.Int, error) {
	const message = "must be a raw positive uint128 base-10 integer string"
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return nil, configError(name, message)
		}
	}
	// Ignore leading zeroes for the range check without reformatting the
	// original API string. uint128 has at most 39 decimal digits.
	digits := strings.TrimLeft(value, "0")
	if len(digits) == 0 || len(digits) > 39 {
		return nil, configError(name, message)
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok || n.BitLen() > 128 {
		return nil, configError(name, message)
	}
	return n, nil
}
