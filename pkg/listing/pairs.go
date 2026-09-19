package listing

import (
	"context"
	"math"
	"net/url"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
)

// Pair is the public configuration of a pair on one chain. PairID is the
// on-chain identifier; zero means the server has not assigned it yet.
type Pair struct {
	ChainID                     uint64
	PairID                      uint32
	BaseToken, QuoteToken       common.Address
	BaseSymbol, QuoteSymbol     string
	BaseDecimals, QuoteDecimals uint8
	// PriceTickSize and LotSize preserve the API's raw positive uint128
	// base-10 integer strings. They use the same units as pairConfigs:
	// priceTickSize is 18-decimal fixed point in native quote/base units;
	// lotSize is in native base-token units. See FrameParams for exact parsing.
	PriceTickSize, LotSize string
	Active, Gray           bool
	Tax                    *Tax // Nil means the server has no active tax configuration.
}

// Tax is the active listing tax configuration, which may differ from on-chain
// state until its transaction is confirmed. A zero RatePPM disables the fee.
type Tax struct {
	Token   common.Address // The pair's base or quote token.
	Symbol  string         // Display only; identify the token by its address.
	RatePPM uint32         // Parts per million; 1,000 means 0.1%.
}

// ListPairs fetches /pairs for one chain. The server returns approved,
// active records with an active tax configuration, without pagination. Callers
// still need to check Gray and PairID before using a record. Ordering is
// preserved and the SDK performs no retries.
func (c *Client) ListPairs(ctx context.Context, chainID uint64) ([]Pair, error) {
	if err := validateChainID(chainID); err != nil {
		return nil, err
	}
	query := url.Values{fieldChainID: {strconv.FormatUint(chainID, decimalBase)}}
	env, status, err := c.get(ctx, pairsPath, query)
	if err != nil {
		return nil, err
	}
	return decodePairs(env.Data, status, chainID)
}

// ListPair queries /pair with chain_id and the on-chain pair_id. The server
// returns APIError 404 for records that are absent, unapproved, or inactive.
func (c *Client) ListPair(ctx context.Context, chainID uint64, pairID uint32) (Pair, error) {
	if err := validateChainID(chainID); err != nil {
		return Pair{}, err
	}
	if pairID == 0 || pairID > math.MaxInt32 {
		return Pair{}, &InputError{Field: "PairID", Message: "must be positive and fit int32"}
	}
	query := url.Values{
		fieldChainID: {strconv.FormatUint(chainID, decimalBase)},
		fieldPairID:  {strconv.FormatUint(uint64(pairID), decimalBase)},
	}
	env, status, err := c.get(ctx, pairPath, query)
	if err != nil {
		return Pair{}, err
	}
	pair, err := decodePair(env.Data)
	if err != nil {
		return Pair{}, pairResponseError(status, fieldData, err)
	}
	if pair.ChainID != chainID || pair.PairID != pairID {
		return Pair{}, invalidResponse(status, "detail chain or pair ID does not match request")
	}
	return pair, nil
}

func validateChainID(chainID uint64) error {
	if chainID == 0 || chainID > math.MaxInt64 {
		return &InputError{Field: "ChainID", Message: "must be positive and fit int64"}
	}
	return nil
}
