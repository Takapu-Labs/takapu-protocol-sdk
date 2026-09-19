package router_test

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

type swapBuilder func(*pb.MarketFrame, common.Address, common.Address, *big.Int, *big.Int, common.Address) (protocol.SwapParams, []byte, error)

func swapBuilders() []struct {
	name   string
	method protocol.SwapMethod
	build  swapBuilder
} {
	var nilRouter *router.Router
	var disconnected router.Router
	return []struct {
		name   string
		method protocol.SwapMethod
		build  swapBuilder
	}{
		{"swap/package", protocol.MethodSwap, router.BuildSwap},
		{"swap/nil receiver", protocol.MethodSwap, nilRouter.BuildSwap},
		{"swap/zero receiver", protocol.MethodSwap, disconnected.BuildSwap},
		{"swapExactIn/package", protocol.MethodSwapExactIn, router.BuildSwapExactIn},
		{"swapExactIn/nil receiver", protocol.MethodSwapExactIn, nilRouter.BuildSwapExactIn},
		{"swapExactIn/zero receiver", protocol.MethodSwapExactIn, disconnected.BuildSwapExactIn},
	}
}

func swapFrame(t *testing.T) (*pb.MarketFrame, frame.SigningDomain, common.Address) {
	t.Helper()
	prepared, err := frame.Prepare(frame.FrameSpec{
		ChainID:  1,
		Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Maker:    common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Pair: frame.PairParams{
			PairID:        7,
			BaseToken:     common.HexToAddress("0x0000000000000000000000000000000000000001"),
			QuoteToken:    common.HexToAddress("0x0000000000000000000000000000000000000002"),
			PriceTickSize: big.NewInt(1_000_000_000_000_000_000),
			LotSize:       big.NewInt(1),
		},
		BaseTick:     500,
		Bids:         []frame.Level{{Offset: 2, QtyLots: 100}},
		Asks:         []frame.Level{{Offset: 3, QtyLots: 50}},
		UpdatedAt:    1_700_000_000,
		MajorVersion: 1,
		CodecVersion: frame.CodecVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := prepared.SigningPayload()
	signature, err := crypto.Sign(payload.Digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)
	value, err := prepared.AttachSignature(signature, signer)
	if err != nil {
		t.Fatal(err)
	}
	return value, payload.Domain, signer
}

func TestBuildSwapPreservesSignedFrameAndEncodesBothDirections(t *testing.T) {
	for _, variant := range swapBuilders() {
		t.Run(variant.name, func(t *testing.T) {
			testBuildSwapPreservesSignedFrameAndEncodesBothDirections(t, variant.method, variant.build)
		})
	}
}

func testBuildSwapPreservesSignedFrameAndEncodesBothDirections(t *testing.T, methodName protocol.SwapMethod, build swapBuilder) {
	t.Helper()
	contractABI, err := protocol.ABI()
	if err != nil {
		t.Fatal(err)
	}
	method := contractABI.Methods[string(methodName)]
	for _, isBuy := range []bool{false, true} {
		for _, normalized := range []bool{false, true} {
			name := "sell"
			if isBuy {
				name = "buy"
			}
			if normalized {
				name += "/v27or28"
			} else {
				name += "/v0or1"
			}
			t.Run(name, func(t *testing.T) {
				value, domain, signer := swapFrame(t)
				if !normalized {
					value.Signature[64] -= 27
				}
				original := proto.Clone(value).(*pb.MarketFrame)
				tokenIn, tokenOut := common.HexToAddress(value.Pair.BaseToken), common.HexToAddress(value.Pair.QuoteToken)
				if isBuy {
					tokenIn, tokenOut = tokenOut, tokenIn
				}
				amountIn, minAmountOut := big.NewInt(1000), big.NewInt(10)
				recipient := common.HexToAddress("0x3333333333333333333333333333333333333333")
				params, data, err := build(value, tokenIn, tokenOut, amountIn, minAmountOut, recipient)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data[:4], method.ID) {
					t.Fatalf("unexpected selector: %x", data[:4])
				}
				decoded, err := method.Inputs.Unpack(data[4:])
				if err != nil {
					t.Fatal(err)
				}
				if decoded[0].(uint32) != value.Pair.PairId || decoded[1].(bool) != isBuy ||
					decoded[2].(*big.Int).Cmp(amountIn) != 0 || decoded[3].(*big.Int).Cmp(minAmountOut) != 0 ||
					decoded[4].(common.Address) != common.HexToAddress(value.MakerAddr) || decoded[5].(common.Address) != recipient {
					t.Fatalf("swap arguments changed: %v", decoded[:6])
				}
				updates := *abi.ConvertType(decoded[6], new([]frame.SignedFrame)).(*[]frame.SignedFrame)
				if len(updates) != 1 {
					t.Fatalf("got %d signed updates", len(updates))
				}
				if !bytes.Equal(updates[0].Frame.Header[:], value.Frame.Header) ||
					!bytes.Equal(updates[0].Frame.BidLevels[:], value.Frame.BidLevels) ||
					!bytes.Equal(updates[0].Frame.AskLevels[:], value.Frame.AskLevels) ||
					!bytes.Equal(updates[0].Signature[:64], value.Signature[:64]) {
					t.Fatal("signed bytes changed in calldata")
				}
				if err := frame.VerifySignature(domain, updates[0], signer); err != nil {
					t.Fatalf("encoded signature: %v", err)
				}
				if v := updates[0].Signature[64]; v != 27 && v != 28 {
					t.Fatalf("encoded signature recovery ID = %d, want 27 or 28", v)
				}
				// Returned params can be reused by the protocol simulation or
				// transaction APIs for the same swap entry point.
				expectedData, err := protocol.EncodeSwapCall(methodName, params)
				if err != nil || !bytes.Equal(data, expectedData) {
					t.Fatalf("returned params differ from returned calldata: %v", err)
				}
				if !proto.Equal(value, original) || amountIn.Int64() != 1000 || minAmountOut.Int64() != 10 {
					t.Fatal("building calldata mutated its inputs")
				}
				params.AmountIn.SetInt64(1)
				params.MinAmountOut.SetInt64(2)
				params.PriceUpdates[0].Signature[0] ^= 1
				params.PriceUpdates[0].Frame.Header[0] ^= 1
				params.PriceUpdates[0].Frame.BidLevels[0] ^= 1
				params.PriceUpdates[0].Frame.AskLevels[0] ^= 1
				if !proto.Equal(value, original) || amountIn.Int64() != 1000 || minAmountOut.Int64() != 10 {
					t.Fatal("returned swap aliases its inputs")
				}
				if !bytes.Equal(data, expectedData) {
					t.Fatal("returned calldata aliases returned params")
				}
				data[4] ^= 1
				if !proto.Equal(value, original) {
					t.Fatal("returned calldata aliases the input frame")
				}
			})
		}
	}
}

func TestBuildSwapRejectsInvalidInputs(t *testing.T) {
	for _, variant := range swapBuilders() {
		t.Run(variant.name, func(t *testing.T) {
			testBuildSwapRejectsInvalidInputs(t, variant.build)
		})
	}
}

func TestBuildSwapDoesNotRevalidateReadableBook(t *testing.T) {
	for _, variant := range swapBuilders() {
		t.Run(variant.name, func(t *testing.T) {
			value, _, _ := swapFrame(t)
			tokenIn, tokenOut := common.HexToAddress(value.Pair.BaseToken), common.HexToAddress(value.Pair.QuoteToken)
			recipient := common.HexToAddress("0x3333333333333333333333333333333333333333")
			_, want, err := variant.build(value, tokenIn, tokenOut, big.NewInt(1000), big.NewInt(0), recipient)
			if err != nil {
				t.Fatal(err)
			}
			// These fields are used by Quote, not by calldata encoding. The signed
			// words remain unchanged and do not need another wire-frame validation.
			value.Bids, value.Asks = nil, nil
			value.UpdatedAt++
			_, got, err := variant.build(value, tokenIn, tokenOut, big.NewInt(1000), big.NewInt(0), recipient)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("readable fields affected calldata: %v", err)
			}
		})
	}
}

func testBuildSwapRejectsInvalidInputs(t *testing.T, build swapBuilder) {
	t.Helper()
	type inputs struct {
		value                  *pb.MarketFrame
		tokenIn, tokenOut      common.Address
		amountIn, minAmountOut *big.Int
		recipient              common.Address
	}
	tests := map[string]func(*inputs){
		"nil frame":        func(v *inputs) { v.value = nil },
		"nil pair":         func(v *inputs) { v.value.Pair = nil },
		"nil compact":      func(v *inputs) { v.value.Frame = nil },
		"short header":     func(v *inputs) { v.value.Frame.Header = v.value.Frame.Header[:31] },
		"long header":      func(v *inputs) { v.value.Frame.Header = append(v.value.Frame.Header, 0) },
		"short bids":       func(v *inputs) { v.value.Frame.BidLevels = v.value.Frame.BidLevels[:31] },
		"long asks":        func(v *inputs) { v.value.Frame.AskLevels = append(v.value.Frame.AskLevels, 0) },
		"invalid token":    func(v *inputs) { v.value.Pair.BaseToken = "invalid" },
		"equal pair":       func(v *inputs) { v.value.Pair.BaseToken = v.value.Pair.QuoteToken },
		"zero token in":    func(v *inputs) { v.tokenIn = common.Address{} },
		"zero token out":   func(v *inputs) { v.tokenOut = common.Address{} },
		"same direction":   func(v *inputs) { v.tokenOut = v.tokenIn },
		"mismatched in":    func(v *inputs) { v.tokenIn = v.recipient },
		"mismatched out":   func(v *inputs) { v.tokenOut = v.recipient },
		"nil input":        func(v *inputs) { v.amountIn = nil },
		"zero input":       func(v *inputs) { v.amountIn = big.NewInt(0) },
		"negative input":   func(v *inputs) { v.amountIn = big.NewInt(-1) },
		"overflow input":   func(v *inputs) { v.amountIn = new(big.Int).Lsh(big.NewInt(1), 256) },
		"nil minimum":      func(v *inputs) { v.minAmountOut = nil },
		"negative minimum": func(v *inputs) { v.minAmountOut = big.NewInt(-1) },
		"overflow minimum": func(v *inputs) { v.minAmountOut = new(big.Int).Lsh(big.NewInt(1), 256) },
		"zero recipient":   func(v *inputs) { v.recipient = common.Address{} },
		"bad signature":    func(v *inputs) { v.value.Signature = nil },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			value, _, _ := swapFrame(t)
			v := inputs{
				value:        value,
				tokenIn:      common.HexToAddress(value.Pair.BaseToken),
				tokenOut:     common.HexToAddress(value.Pair.QuoteToken),
				amountIn:     big.NewInt(1000),
				minAmountOut: big.NewInt(0),
				recipient:    common.HexToAddress("0x3333333333333333333333333333333333333333"),
			}
			edit(&v)
			params, data, err := build(v.value, v.tokenIn, v.tokenOut, v.amountIn, v.minAmountOut, v.recipient)
			if err == nil || data != nil || params.AmountIn != nil {
				t.Fatalf("invalid inputs returned params=%+v calldata=%x error=%v", params, data, err)
			}
		})
	}
}
