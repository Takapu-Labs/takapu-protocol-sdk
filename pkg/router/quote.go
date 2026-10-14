package router

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

const quotePPM uint32 = 1_000_000

// Quote computes net amountOut from MarketFrame's decoded bids or asks.
// Frames past cfg.DecayStartOffsetSeconds and future frames are rejected.
// The current Unix time is sampled once per call.
// tokenIn/base sells into bids; tokenIn/quote buys from asks. The pair must match cfg.
//
// amountIn is a maximum spend, matching the protocol's native-unit matching: a
// fill can be a fractional lot, while insufficient depth and input fees can
// still leave input unspent. Each touched bid level's quote output is floored
// once and each touched ask level's cost is ceiled once. Empty books, or a budget
// whose match produces zero net output, return ErrZeroOutput. A buy that cannot
// afford one native base unit and a sell whose quote output floors to zero both
// fall under this; sub-lot budgets otherwise fill fractionally.
//
// Quote trusts the decoded fields validated by Marketstream and does not decode
// or validate the signed compact frame again. This is a local estimate; it does
// not check balances or subsequent on-chain fills. Use protocol simulation to
// check execution. All inputs remain caller-owned and unmodified.
func Quote(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn *big.Int, cfg QuoteConfig) (*big.Int, error) {
	if err := validateQuote(value, tokenIn, tokenOut, amountIn, cfg, uint64(time.Now().Unix())); err != nil {
		return nil, err
	}
	levels := value.Bids
	if tokenIn == cfg.Pair.QuoteToken {
		levels = value.Asks
	}
	return matchQuote(levels, tokenIn, tokenOut, amountIn, cfg)
}

func validateQuote(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn *big.Int, cfg QuoteConfig, now uint64) error {
	if value == nil || value.Pair == nil {
		return errors.New("router: market frame and pair are required")
	}
	updatedAt := uint64(value.UpdatedAt)
	if now < updatedAt {
		return ErrFutureFrame
	}
	if now-updatedAt > uint64(cfg.DecayStartOffsetSeconds) {
		return ErrExpiredFrame
	}
	if amountIn == nil || amountIn.Sign() <= 0 || amountIn.BitLen() > 256 {
		return errors.New("router: amountIn must be a positive uint256")
	}
	if err := frame.ValidatePair(cfg.Pair); err != nil {
		return fmt.Errorf("router: quote pair: %w", err)
	}
	sell := tokenIn == cfg.Pair.BaseToken && tokenOut == cfg.Pair.QuoteToken
	buy := tokenIn == cfg.Pair.QuoteToken && tokenOut == cfg.Pair.BaseToken
	if !sell && !buy {
		return errors.New("router: quote token direction does not match the pair")
	}
	if cfg.FeeRatePPM >= quotePPM || (cfg.FeeRatePPM == 0) != (cfg.FeeToken == (common.Address{})) {
		return errors.New("router: invalid quote fee configuration")
	}
	if cfg.FeeToken != (common.Address{}) && cfg.FeeToken != cfg.Pair.BaseToken && cfg.FeeToken != cfg.Pair.QuoteToken {
		return errors.New("router: fee token does not belong to the pair")
	}
	pair := value.Pair
	if pair.PairId != cfg.Pair.PairID || !common.IsHexAddress(pair.BaseToken) || !common.IsHexAddress(pair.QuoteToken) ||
		common.HexToAddress(pair.BaseToken) != cfg.Pair.BaseToken || common.HexToAddress(pair.QuoteToken) != cfg.Pair.QuoteToken {
		return errors.New("router: quote frame does not match the configured pair")
	}
	return nil
}

var (
	// priceScale18 is the protocol PRICE_SCALE; scaleMinusOne turns a division
	// into a ceiling via (x + 1e18 - 1) / 1e18.
	priceScale18  = big.NewInt(1_000_000_000_000_000_000)
	scaleMinusOne = new(big.Int).Sub(priceScale18, big.NewInt(1))
)

// matchQuote estimates the net output of matching amountIn against one side of
// the book (bids when selling base, asks when buying), mirroring the protocol's
// _matchBid/_matchAsk for a fresh frame: no decay and no on-chain consumed
// depth, and fills may be fractional lots.
//
// Per touched level, in native units, with price = tick price scaled by 1e18 and
// depth = the level's raw base depth:
//
//	sell base: take = min(depth, budget); out += floor(take * price / 1e18)
//	buy base : cost = ceil(depth * price / 1e18); if cost > budget then
//	           take = floor(budget * 1e18 / price), cost = ceil(take * price / 1e18)
//	           out += take
//
// Fees: an input-side fee first reduces the budget to the largest leg B' with
// B' + floor(B'*fee/1e6) <= amountIn; an output-side fee is deducted at the end
// as floor(gross*fee/1e6). An empty book or a zero net output returns
// ErrZeroOutput.
func matchQuote(levels []*pb.PriceLevel, tokenIn, tokenOut common.Address, amountIn *big.Int, cfg QuoteConfig) (*big.Int, error) {
	budget := legBudgetWithInputFee(amountIn, cfg, tokenIn) // B'
	out := new(big.Int)
	buy := tokenIn == cfg.Pair.QuoteToken
	var price, depth, take, cost, quote big.Int // scratch reused across levels
	for i, level := range levels {
		if budget.Sign() == 0 {
			break // budget exhausted
		}
		// price = readable * 10^(18+quoteDec-baseDec) = tick * priceTickSize.
		if err := quoteDecimal(&price, level.GetPrice(), 18+int(cfg.Pair.QuoteDecimals)-int(cfg.Pair.BaseDecimals)); err != nil {
			return nil, fmt.Errorf("router: level %d price: %w", i, err)
		}
		// depth = readable * 10^baseDec = qtyLots * lotSize, in native base units.
		if err := quoteDecimal(&depth, level.GetAmount(), int(cfg.Pair.BaseDecimals)); err != nil {
			return nil, fmt.Errorf("router: level %d amount: %w", i, err)
		}
		if buy {
			quoteCost(&cost, &depth, &price) // cost = ceil(depth*price/1e18)
			take.Set(&depth)                 // provisionally take the whole level
			if cost.Cmp(budget) > 0 {
				baseAffordable(&take, budget, &price) // take = floor(budget*1e18/price)
				if take.Sign() == 0 {
					break // cannot afford one native base unit
				}
				quoteCost(&cost, &take, &price) // cost = ceil(take*price/1e18)
			}
			out.Add(out, &take)       // out += take (base)
			budget.Sub(budget, &cost) // budget -= cost (quote)
			if take.Cmp(&depth) < 0 {
				break // partial level: residual budget buys nothing more
			}
		} else {
			take.Set(&depth)
			if take.Cmp(budget) > 0 {
				take.Set(budget) // take = min(depth, budget)
			}
			quoteValue(&quote, &take, &price) // quote = floor(take*price/1e18)
			out.Add(out, &quote)              // out += quote
			budget.Sub(budget, &take)         // budget -= take (base)
		}
		if out.BitLen() > 256 {
			return nil, errors.New("router: quote output exceeds uint256")
		}
	}
	return applyOutputFee(out, cfg, tokenOut)
}

// quoteCost sets z = ceil(base * price / 1e18): the quote spent to buy base
// units. This is the protocol's ask-side ceiling (TakapuProtocol._matchAsk).
func quoteCost(z, base, price *big.Int) *big.Int {
	z.Mul(base, price)            // base * price
	z.Add(z, scaleMinusOne)       // + (1e18 - 1)
	return z.Quo(z, priceScale18) // / 1e18, i.e. ceil
}

// quoteValue sets z = floor(base * price / 1e18): the quote received for base
// units. This is the protocol's bid-side floor (TakapuProtocol._matchBid).
func quoteValue(z, base, price *big.Int) *big.Int {
	return z.Quo(z.Mul(base, price), priceScale18)
}

// baseAffordable sets z = floor(budget * 1e18 / price): the base units a quote
// budget buys at price. This is the protocol's inverse pricing (_matchAsk).
func baseAffordable(z, budget, price *big.Int) *big.Int {
	return z.Quo(z.Mul(budget, priceScale18), price)
}

// legBudgetWithInputFee returns amountIn, or with an input-side fee the largest leg B' whose
// fee-inclusive outlay still fits: B' + floor(B'*fee/1e6) <= amountIn, in closed
// form B' = floor(((amountIn+1)*1e6 - 1)/(1e6 + fee)). Matches _legBudget.
func legBudgetWithInputFee(amountIn *big.Int, cfg QuoteConfig, tokenIn common.Address) *big.Int {
	if cfg.FeeRatePPM == 0 || cfg.FeeToken != tokenIn {
		return new(big.Int).Set(amountIn)
	}
	b := new(big.Int).Add(amountIn, big.NewInt(1))
	b.Mul(b, big.NewInt(int64(quotePPM)))
	b.Sub(b, big.NewInt(1))
	return b.Quo(b, big.NewInt(int64(quotePPM)+int64(cfg.FeeRatePPM)))
}

// applyOutputFee deducts an output-side fee from the gross, net = gross -
// floor(gross*fee/1e6), and rejects a zero net output. Decay is zero for the
// fresh frames Quote accepts, so gross is already undecayed.
func applyOutputFee(gross *big.Int, cfg QuoteConfig, tokenOut common.Address) (*big.Int, error) {
	if cfg.FeeRatePPM > 0 && cfg.FeeToken == tokenOut {
		gross.Sub(gross, quoteDeduction(gross, cfg.FeeRatePPM))
	}
	if gross.Sign() == 0 {
		return nil, ErrZeroOutput
	}
	return gross, nil
}

// quoteDecimal converts a readable price/amount to an exact positive uint256.
// Shifting decimal digits avoids floating point and rational-number arithmetic.
func quoteDecimal(out *big.Int, value string, scale int) error {
	if len(value) == 0 || len(value) > 64 {
		return errors.New("must be a positive plain decimal of at most 64 characters")
	}
	whole, fraction, dot := strings.Cut(value, ".")
	if whole == "" || (dot && fraction == "") {
		return errors.New("must be a plain decimal")
	}
	digits := whole + fraction
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return errors.New("must be a plain decimal")
		}
	}
	shift := scale - len(fraction)
	if shift < 0 {
		end := len(digits) + shift
		if end <= 0 || strings.Trim(digits[end:], "0") != "" {
			return errors.New("does not represent exact native units")
		}
		digits = digits[:end]
	} else if shift > 0 {
		digits += strings.Repeat("0", shift)
	}
	if _, ok := out.SetString(digits, 10); !ok || out.Sign() <= 0 || out.BitLen() > 256 {
		return errors.New("must represent a positive uint256")
	}
	return nil
}

// quoteDeduction returns the output-side fee floor(amount * rate / quotePPM).
func quoteDeduction(amount *big.Int, rate uint32) *big.Int {
	out := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(rate)))
	return out.Quo(out, new(big.Int).SetUint64(uint64(quotePPM)))
}
