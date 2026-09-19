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
// amountIn is a maximum spend, matching the protocol's partial-fill semantics:
// whole-lot truncation, insufficient depth and input fees can leave input
// unspent. Output is rounded down per bid level, ask cost up per ask level.
// Empty books or a budget too small to produce output return ErrZeroOutput.
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
	var price, available, lots, base, quote big.Int
	for i, level := range levels {
		if budget.Sign() == 0 {
			break
		}
		if !buy {
			lots.Quo(budget, cfg.Pair.LotSize)
			if lots.Sign() == 0 {
				break
			}
		}
		if err := quoteDecimal(&price, level.GetPrice(), priceScale); err != nil {
			return nil, fmt.Errorf("router: level %d price: %w", i, err)
		}
		if buy {
			// Preserve both the contract's nested floors and its uint256
			// bound on the first quotient, even when final output fits.
			lots.Mul(budget, scale)
			lots.Quo(&lots, cfg.Pair.LotSize)
			if lots.BitLen() > 256 {
				return nil, errors.New("router: ask affordability exceeds uint256")
			}
			lots.Quo(&lots, &price)
			if lots.Sign() == 0 {
				break
			}
		}
		if err := quoteDecimal(&available, level.GetAmount(), int(cfg.Pair.BaseDecimals)); err != nil {
			return nil, fmt.Errorf("router: level %d amount: %w", i, err)
		}
		available.Quo(&available, cfg.Pair.LotSize)
		if lots.Cmp(&available) > 0 {
			lots.Set(&available)
		}
		base.Mul(&lots, cfg.Pair.LotSize)
		quote.Mul(&base, &price)
		if buy {
			// Ceil the complete touched level once, not each individual lot.
			quote.Add(&quote, roundUp)
			quote.Quo(&quote, scale)
			budget.Sub(budget, &quote)
			out.Add(out, &base)
		} else {
			quote.Quo(&quote, scale)
			budget.Sub(budget, &base)
			out.Add(out, &quote)
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
