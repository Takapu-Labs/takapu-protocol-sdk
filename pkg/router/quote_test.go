package router_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

type quoteSigner struct{ key *ecdsa.PrivateKey }

func (s quoteSigner) Address() common.Address { return crypto.PubkeyToAddress(s.key.PublicKey) }
func (s quoteSigner) SignDigest(_ context.Context, digest common.Hash) ([]byte, error) {
	return crypto.Sign(digest[:], s.key)
}

func quoteSpec() frame.FrameSpec {
	return frame.FrameSpec{
		ChainID: 1, Protocol: common.HexToAddress("0x1000000000000000000000000000000000000000"),
		Maker: common.HexToAddress("0x2000000000000000000000000000000000000000"),
		Pair: frame.PairParams{
			PairID: 3, BaseToken: common.HexToAddress("0x3000000000000000000000000000000000000000"),
			QuoteToken:   common.HexToAddress("0x4000000000000000000000000000000000000000"),
			BaseDecimals: 18, QuoteDecimals: 18, PriceTickSize: big.NewInt(10_000_000_000_000_000), LotSize: big.NewInt(100_000_000_000_000),
		},
		BaseTick:  245445,
		Bids:      []frame.Level{{Offset: 1, QtyLots: 250480}, {Offset: 4, QtyLots: 8149}, {Offset: 13, QtyLots: 2000}, {Offset: 17, QtyLots: 6288}, {Offset: 18, QtyLots: 3118}},
		Asks:      []frame.Level{{Offset: 0, QtyLots: 19566}, {Offset: 15, QtyLots: 30}, {Offset: 17, QtyLots: 4382}, {Offset: 18, QtyLots: 5935}, {Offset: 21, QtyLots: 9892}},
		UpdatedAt: uint32(time.Now().Unix()), MajorVersion: 1, CodecVersion: frame.CodecVersion,
	}
}

func quoteFrame(t testing.TB, spec frame.FrameSpec) (*pb.MarketFrame, router.QuoteConfig) {
	t.Helper()
	key, err := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	value, err := frame.BuildAndSign(context.Background(), spec, quoteSigner{key})
	if err != nil {
		t.Fatal(err)
	}
	return value, router.QuoteConfig{Pair: spec.Pair, DecayStartOffsetSeconds: 10}
}

func BenchmarkQuote(b *testing.B) {
	for _, test := range []struct {
		name, amount string
		buy          bool
	}{
		{"sell one level", "100000000000000", false},
		{"sell full depth", "30123456789123456789", false},
		{"buy one level", "1000000000000000000", true},
		{"buy full depth", "10799526480848443322111", true},
	} {
		b.Run(test.name, func(b *testing.B) {
			value, cfg := quoteFrame(b, quoteSpec())
			cfg.DecayStartOffsetSeconds = math.MaxUint32 // Allow arbitrary benchmark durations.
			tokenIn, tokenOut := cfg.Pair.BaseToken, cfg.Pair.QuoteToken
			if test.buy {
				tokenIn, tokenOut = tokenOut, tokenIn
			}
			amount := quoteInt(test.amount)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := router.Quote(value, tokenIn, tokenOut, amount, cfg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func quoteInt(s string) *big.Int {
	x, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("invalid integer test fixture")
	}
	return x
}

func TestQuoteContractDepthVectors(t *testing.T) {
	// Independent expected values from the Solidity protocol's OrderbookDepth.t.sol
	// and docs/DEPTH_HANDCALC.md, including caps that exceed available book depth.
	value, cfg := quoteFrame(t, quoteSpec())
	cfg.FeeToken, cfg.FeeRatePPM = cfg.Pair.QuoteToken, 3000
	for _, test := range []struct {
		name, amountIn, want string
		buy                  bool
	}{
		{"buy multiple levels", "4819492361611371712678", "1957700000000000000", true},
		{"buy full depth", "10799526480848443322111", "3980500000000000000", true},
		{"sell multiple levels", "25171745678901234567", "61597076366089000000000", false},
		{"sell full depth", "30123456789123456789", "66079433673563000000000", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokenIn, tokenOut := cfg.Pair.BaseToken, cfg.Pair.QuoteToken
			if test.buy {
				tokenIn, tokenOut = tokenOut, tokenIn
			}
			got, err := router.Quote(value, tokenIn, tokenOut, quoteInt(test.amountIn), cfg)
			if err != nil || got.Cmp(quoteInt(test.want)) != 0 {
				t.Fatalf("output = %v, %v; want %s", got, err, test.want)
			}
		})
	}
}

func TestQuoteContractRoundingVectors(t *testing.T) {
	spec := quoteSpec()
	spec.Pair.PriceTickSize = big.NewInt(3333)
	spec.Pair.QuoteDecimals = 6
	spec.Bids, spec.Asks = []frame.Level{{Offset: 1, QtyLots: 10}}, []frame.Level{{Offset: 0, QtyLots: 10}}
	value, cfg := quoteFrame(t, spec)
	cfg.FeeToken, cfg.FeeRatePPM = cfg.Pair.QuoteToken, 3000
	got, err := router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(82060), cfg)
	if err != nil || got.Cmp(big.NewInt(100_000_000_000_000)) != 0 {
		t.Fatalf("buy = %v, %v", got, err)
	}
	// Input fee inversion's +1 correction makes exactly one lot affordable:
	// cost=81807, fee=floor(81807*.003)=245, total=82052.
	got, err = router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(82052), cfg)
	if err != nil || got.Cmp(spec.Pair.LotSize) != 0 {
		t.Fatalf("exact fee budget = %v, %v", got, err)
	}
	if _, err := router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(82051), cfg); !errors.Is(err, router.ErrZeroOutput) {
		t.Fatalf("below first lot = %v", err)
	}
	got, err = router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, spec.Pair.LotSize, cfg)
	// floor(bid)=81806, output fee=floor(81806*.003)=245.
	if err != nil || got.Cmp(big.NewInt(81561)) != 0 {
		t.Fatalf("bid output after fee = %v, %v", got, err)
	}
}

func TestQuoteRoundsEachTouchedLevelOnce(t *testing.T) {
	spec := quoteSpec()
	spec.Pair.PriceTickSize, spec.Pair.LotSize = big.NewInt(600_000_000_000_000_000), big.NewInt(1)
	spec.BaseTick = 4
	spec.Bids = []frame.Level{{Offset: 1, QtyLots: 1}, {Offset: 2, QtyLots: 1}}
	spec.Asks = []frame.Level{{Offset: 0, QtyLots: 2}, {Offset: 1, QtyLots: 1}}
	value, cfg := quoteFrame(t, spec)
	// Bid levels produce floor(1.8)+floor(1.2)=2, not floor(3)=3.
	got, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, big.NewInt(2), cfg)
	if err != nil || got.Int64() != 2 {
		t.Fatalf("bid rounding = %v, %v", got, err)
	}
	// First ask has cost ceil(2*2.4)=5, not 2*ceil(2.4)=6; second costs 3.
	got, err = router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(8), cfg)
	if err != nil || got.Int64() != 3 {
		t.Fatalf("ask rounding = %v, %v", got, err)
	}
	got, err = router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(7), cfg)
	if err != nil || got.Int64() != 2 {
		t.Fatalf("ask dust = %v, %v", got, err)
	}
}

func TestQuoteUsesCurrentTime(t *testing.T) {
	for _, test := range []struct {
		name   string
		offset int64
		want   error
	}{
		{"fresh", 0, nil},
		{"expired", -60, router.ErrExpiredFrame},
		{"future", 60, router.ErrFutureFrame},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := quoteSpec()
			spec.UpdatedAt = uint32(time.Now().Unix() + test.offset)
			value, cfg := quoteFrame(t, spec)
			_, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg)
			if !errors.Is(err, test.want) {
				t.Fatalf("quote error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestQuoteFeeSwitchesAndBaseFeeDirection(t *testing.T) {
	spec := quoteSpec()
	spec.Pair.PriceTickSize, spec.Pair.LotSize = big.NewInt(1_000_000_000_000_000_000), big.NewInt(10)
	spec.BaseTick = 3
	spec.Bids, spec.Asks = []frame.Level{{Offset: 1, QtyLots: 10}}, []frame.Level{{Offset: 0, QtyLots: 10}}
	value, cfg := quoteFrame(t, spec)
	// Zero token and zero rate disable fees in the local estimate.
	got, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, big.NewInt(20), cfg)
	if err != nil || got.Int64() != 40 {
		t.Fatalf("disabled fee = %v, %v", got, err)
	}
	cfg.FeeToken, cfg.FeeRatePPM = cfg.Pair.BaseToken, 100000
	// Selling base: 20 cannot afford two ten-unit lots plus their input fee.
	got, err = router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, big.NewInt(20), cfg)
	if err != nil || got.Int64() != 20 {
		t.Fatalf("base input fee = %v, %v", got, err)
	}
	got, err = router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, big.NewInt(22), cfg)
	if err != nil || got.Int64() != 40 {
		t.Fatalf("base input fee exact budget = %v, %v", got, err)
	}
	// Buying base: 60 buys 20 gross, less the base output fee of two.
	got, err = router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, big.NewInt(60), cfg)
	if err != nil || got.Int64() != 18 {
		t.Fatalf("base output fee = %v, %v", got, err)
	}
}

func TestQuoteRejectsInvalidInputs(t *testing.T) {
	value, cfg := quoteFrame(t, quoteSpec())
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 256)} {
		if _, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, amount, cfg); err == nil {
			t.Fatalf("accepted amount %v", amount)
		}
	}
	for _, change := range []func(*router.QuoteConfig){
		func(c *router.QuoteConfig) { c.Pair.PairID++ },
		func(c *router.QuoteConfig) { c.Pair.PriceTickSize = nil },
		func(c *router.QuoteConfig) { c.Pair.LotSize = big.NewInt(0) },
		func(c *router.QuoteConfig) { c.Pair.PriceTickSize = new(big.Int).Lsh(big.NewInt(1), 128) },
		func(c *router.QuoteConfig) { c.Pair.BaseToken, c.Pair.QuoteToken = c.Pair.QuoteToken, c.Pair.BaseToken },
		func(c *router.QuoteConfig) { c.FeeToken = c.Pair.BaseToken },
		func(c *router.QuoteConfig) { c.FeeRatePPM = 1 },
		func(c *router.QuoteConfig) { c.FeeToken, c.FeeRatePPM = c.Pair.BaseToken, 1000000 },
		func(c *router.QuoteConfig) { c.FeeToken, c.FeeRatePPM = common.HexToAddress(value.MakerAddr), 1 },
	} {
		bad := cfg
		change(&bad)
		if _, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, bad); err == nil {
			t.Fatalf("accepted config %+v", bad)
		}
	}
	for _, tokens := range [][2]common.Address{{{}, cfg.Pair.QuoteToken}, {cfg.Pair.BaseToken, cfg.Pair.BaseToken}, {cfg.Pair.BaseToken, common.HexToAddress(value.MakerAddr)}} {
		if _, err := router.Quote(value, tokens[0], tokens[1], cfg.Pair.LotSize, cfg); err == nil {
			t.Fatalf("accepted direction %v", tokens)
		}
	}
	for _, change := range []func(*pb.MarketFrame){
		func(f *pb.MarketFrame) { f.Pair = nil },
		func(f *pb.MarketFrame) { f.UpdatedAt = math.MaxUint32 },
		func(f *pb.MarketFrame) { f.Pair.PairId++ },
		func(f *pb.MarketFrame) { f.Pair.BaseToken = "invalid" },
		func(f *pb.MarketFrame) { f.Pair.QuoteToken = "invalid" },
		func(f *pb.MarketFrame) { f.Bids[0] = nil },
	} {
		bad := proto.Clone(value).(*pb.MarketFrame)
		change(bad)
		if _, err := router.Quote(bad, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg); err == nil {
			t.Fatal("accepted invalid market frame")
		}
	}
	if _, err := router.Quote(nil, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg); err == nil {
		t.Fatal("accepted nil market frame")
	}
}

func TestQuoteEmptyBookAndDust(t *testing.T) {
	spec := quoteSpec()
	for _, buy := range []bool{false, true} {
		value, cfg := quoteFrame(t, spec)
		tokenIn, tokenOut := cfg.Pair.BaseToken, cfg.Pair.QuoteToken
		if buy {
			tokenIn, tokenOut = tokenOut, tokenIn
		}
		if _, err := router.Quote(value, tokenIn, tokenOut, big.NewInt(1), cfg); !errors.Is(err, router.ErrZeroOutput) {
			t.Fatalf("dust buy=%v: %v", buy, err)
		}
	}
	spec.Bids, spec.Asks = nil, nil
	value, cfg := quoteFrame(t, spec)
	if _, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg); !errors.Is(err, router.ErrZeroOutput) {
		t.Fatalf("empty book: %v", err)
	}
	// A whole lot with sub-unit bid output also fails, matching ZeroOutput.
	spec.Pair.LotSize, spec.Pair.PriceTickSize = big.NewInt(1), big.NewInt(1)
	spec.Bids = []frame.Level{{Offset: 1, QtyLots: 1}}
	value, cfg = quoteFrame(t, spec)
	if _, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, big.NewInt(1), cfg); !errors.Is(err, router.ErrZeroOutput) {
		t.Fatalf("rounded-zero output: %v", err)
	}
}

func TestQuoteAskAffordabilityUint256Overflow(t *testing.T) {
	spec := quoteSpec()
	spec.Pair.LotSize, spec.Pair.PriceTickSize = big.NewInt(1), big.NewInt(1)
	spec.Bids, spec.Asks = nil, []frame.Level{{Offset: 0, QtyLots: 1}}
	value, cfg := quoteFrame(t, spec)
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if _, err := router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, max, cfg); err == nil {
		t.Fatal("accepted a budget whose first Solidity mulDiv quotient overflows uint256")
	}
	// The precise representable intermediate boundary still returns a capped fill.
	boundary := new(big.Int).Quo(max, big.NewInt(1_000_000_000_000_000_000))
	got, err := router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, boundary, cfg)
	if err != nil || got.Int64() != 1 {
		t.Fatalf("representable affordability boundary = %v, %v", got, err)
	}
}

func TestQuoteDoesNotMutateInputs(t *testing.T) {
	value, cfg := quoteFrame(t, quoteSpec())
	before := proto.Clone(value)
	amount := new(big.Int).Set(cfg.Pair.LotSize)
	tick, lot := new(big.Int).Set(cfg.Pair.PriceTickSize), new(big.Int).Set(cfg.Pair.LotSize)
	cfg.FeeToken, cfg.FeeRatePPM = cfg.Pair.BaseToken, 3000
	amount.Mul(amount, big.NewInt(2))
	inputBefore := new(big.Int).Set(amount)
	var client *router.Router // Local methods do not access connection state.
	out, err := client.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, amount, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out.SetInt64(0)
	if !proto.Equal(value, before) || amount.Cmp(inputBefore) != 0 || cfg.Pair.LotSize.Cmp(lot) != 0 || cfg.Pair.PriceTickSize.Cmp(tick) != 0 {
		t.Fatal("quote mutated or aliased caller input")
	}
}

func TestQuoteUsesDecodedFields(t *testing.T) {
	spec := quoteSpec()
	spec.UpdatedAt -= 100
	value, cfg := quoteFrame(t, spec)
	// Only the readable fields determine the quote, including its age and depth.
	// The signed frame still contains the original timestamp, prices and amounts.
	value.UpdatedAt = uint32(time.Now().Unix())
	value.Bids = []*pb.PriceLevel{{Price: "2000", Amount: "0.0002"}}
	value.Asks = []*pb.PriceLevel{{Price: "2500", Amount: "0.0003"}}
	for _, signed := range []bool{true, false} {
		if !signed {
			value.Frame, value.Signature, value.MakerAddr = nil, nil, ""
		}
		got, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, quoteInt("1000000000000000000"), cfg)
		if err != nil || got.String() != "400000000000000000" {
			t.Fatalf("decoded bid, signed=%v: %v, %v", signed, got, err)
		}
		got, err = router.Quote(value, cfg.Pair.QuoteToken, cfg.Pair.BaseToken, quoteInt("1000000000000000000"), cfg)
		if err != nil || got.String() != "300000000000000" {
			t.Fatalf("decoded ask, signed=%v: %v, %v", signed, got, err)
		}
	}
	// Expiration is checked before parsing any book levels.
	value.UpdatedAt = uint32(time.Now().Unix()) - cfg.DecayStartOffsetSeconds - 1
	value.Bids[0] = nil
	if _, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg); !errors.Is(err, router.ErrExpiredFrame) {
		t.Fatalf("expired decoded frame: %v", err)
	}
}

func TestQuoteConvertsTokenDecimals(t *testing.T) {
	for _, decimals := range [][2]uint8{{18, 6}, {6, 18}, {30, 0}, {0, 0}} {
		spec := quoteSpec()
		spec.Pair.BaseDecimals, spec.Pair.QuoteDecimals = decimals[0], decimals[1]
		value, cfg := quoteFrame(t, spec)
		for _, test := range []struct {
			buy          bool
			amount, want string
		}{
			{false, "100000000000000", "245444000000000000"},
			{true, "245445000000000000", "100000000000000"},
		} {
			tokenIn, tokenOut := cfg.Pair.BaseToken, cfg.Pair.QuoteToken
			if test.buy {
				tokenIn, tokenOut = tokenOut, tokenIn
			}
			got, err := router.Quote(value, tokenIn, tokenOut, quoteInt(test.amount), cfg)
			if err != nil || got.String() != test.want {
				t.Fatalf("decimals %v, buy=%v: %v, %v", decimals, test.buy, got, err)
			}
		}
	}
}

func TestQuoteRejectsInvalidDecodedDecimals(t *testing.T) {
	value, cfg := quoteFrame(t, quoteSpec())
	for _, invalid := range []string{"", "0", "-1", "+1", "1e3", "1/2", ".1", "1.", "1.2.3", " 1", "1_000", strings.Repeat("1", 65), "0.0000000000000000001"} {
		for _, price := range []bool{false, true} {
			bad := proto.Clone(value).(*pb.MarketFrame)
			if price {
				bad.Bids[0].Price = invalid
			} else {
				bad.Bids[0].Amount = invalid
			}
			if _, err := router.Quote(bad, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg); err == nil {
				t.Fatalf("accepted %q, price=%v", invalid, price)
			}
		}
	}
	// Extra trailing decimal zeroes are exact and do not require rounding.
	value.Bids[0].Price = "2454.4400000000000000000000"
	value.Bids[0].Amount = "0.0001000000000000000000"
	got, err := router.Quote(value, cfg.Pair.BaseToken, cfg.Pair.QuoteToken, cfg.Pair.LotSize, cfg)
	if err != nil || got.String() != "245444000000000000" {
		t.Fatalf("trailing zeroes: %v, %v", got, err)
	}
}

func TestQuoteOnlyReadsNeededLevels(t *testing.T) {
	for _, buy := range []bool{false, true} {
		value, cfg := quoteFrame(t, quoteSpec())
		tokenIn, tokenOut := cfg.Pair.BaseToken, cfg.Pair.QuoteToken
		amount, want := "100000000000001", "245444000000000000"
		value.Bids[0].Amount, value.Asks[0].Amount = "0.0001", "0.0001"
		if buy {
			tokenIn, tokenOut = tokenOut, tokenIn
			amount, want = "245445000000000001", "100000000000000"
			value.Bids = []*pb.PriceLevel{nil}
			value.Asks[1].Amount = "invalid"
		} else {
			value.Asks = []*pb.PriceLevel{nil}
			value.Bids[1] = nil
		}
		got, err := router.Quote(value, tokenIn, tokenOut, quoteInt(amount), cfg)
		if err != nil || got.String() != want {
			t.Fatalf("unused levels, buy=%v: %v, %v", buy, got, err)
		}
	}
}
