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
// too small to fill one native base unit or to produce nonzero output, return
// ErrZeroOutput.
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

func matchQuote(levels []*pb.PriceLevel, tokenIn, tokenOut common.Address, amountIn *big.Int, cfg QuoteConfig) (*big.Int, error) {
	budget := new(big.Int).Set(amountIn)
	feeActive := cfg.FeeRatePPM > 0
	if feeActive && cfg.FeeToken == tokenIn {
		// Largest x with x + floor(x*fee/PPM) <= amountIn:
		// floor(((amountIn + 1)*PPM - 1) / (PPM + fee)).
		budget.Add(budget, big.NewInt(1))
		budget.Mul(budget, new(big.Int).SetUint64(uint64(quotePPM)))
		budget.Sub(budget, big.NewInt(1))
		budget.Quo(budget, new(big.Int).SetUint64(uint64(quotePPM)+uint64(cfg.FeeRatePPM)))
	}

	buy := tokenIn == cfg.Pair.QuoteToken
	priceScale := 18 + int(cfg.Pair.QuoteDecimals) - int(cfg.Pair.BaseDecimals)
	scale := big.NewInt(1_000_000_000_000_000_000)
	roundUp := new(big.Int).Sub(scale, big.NewInt(1))
	out := new(big.Int)
	var price, available, take, cost, quote big.Int
	for i, level := range levels {
		if budget.Sign() == 0 {
			break
		}
		if err := quoteDecimal(&price, level.GetPrice(), priceScale); err != nil {
			return nil, fmt.Errorf("router: level %d price: %w", i, err)
		}
		// available is the level's raw depth in native base units (a whole number
		// of lots). A fill may take a fractional part of the final lot; the
		// protocol still occupies whole lots of depth, which this estimate ignores.
		if err := quoteDecimal(&available, level.GetAmount(), int(cfg.Pair.BaseDecimals)); err != nil {
			return nil, fmt.Errorf("router: level %d amount: %w", i, err)
		}
		if buy {
			// Ask: cost the full level once with ceil before inverting the price,
			// so an extreme budget cannot overflow the affordable base quotient.
			cost.Mul(&available, &price)
			cost.Add(&cost, roundUp)
			cost.Quo(&cost, scale)
			take.Set(&available)
			if cost.Cmp(budget) > 0 {
				take.Mul(budget, scale)
				take.Quo(&take, &price)
				if take.Sign() == 0 {
					break // not even one native base unit; later levels cost more
				}
				cost.Mul(&take, &price)
				cost.Add(&cost, roundUp)
				cost.Quo(&cost, scale)
			}
			out.Add(out, &take)
			budget.Sub(budget, &cost)
			if take.Cmp(&available) < 0 {
				break // residual budget cannot buy another native base unit
			}
		} else {
			// Bid: sell base for quote, one floor per touched level.
			take.Set(&available)
			if take.Cmp(budget) > 0 {
				take.Set(budget)
			}
			quote.Mul(&take, &price)
			quote.Quo(&quote, scale)
			out.Add(out, &quote)
			budget.Sub(budget, &take)
		}
		if out.BitLen() > 256 {
			return nil, errors.New("router: quote output exceeds uint256")
		}
	}
	if feeActive && cfg.FeeToken == tokenOut {
		out.Sub(out, quoteDeduction(out, cfg.FeeRatePPM))
	}
	if out.Sign() == 0 {
		return nil, ErrZeroOutput
	}
	return out, nil
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

func quoteDeduction(amount *big.Int, rate uint32) *big.Int {
	out := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(rate)))
	return out.Quo(out, new(big.Int).SetUint64(uint64(quotePPM)))
}
