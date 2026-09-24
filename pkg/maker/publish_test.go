package maker_test

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

type quoteSigner struct {
	key    *ecdsa.PrivateKey
	err    error
	calls  int
	cancel context.CancelFunc
}

func (s *quoteSigner) Address() common.Address { return crypto.PubkeyToAddress(s.key.PublicKey) }

func (s *quoteSigner) SignDigest(ctx context.Context, digest common.Hash) ([]byte, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	if s.err != nil {
		return nil, s.err
	}
	return crypto.Sign(digest[:], s.key)
}

func quoteParams(t *testing.T) maker.PublishParams {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return maker.PublishParams{
		ChainID:  56,
		Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Maker:    common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Pair: maker.PairParams{
			PairID:       7,
			BaseToken:    common.HexToAddress("0x3333333333333333333333333333333333333333"),
			QuoteToken:   common.HexToAddress("0x4444444444444444444444444444444444444444"),
			BaseDecimals: 18, QuoteDecimals: 6,
			PriceTickSize: big.NewInt(10000), LotSize: big.NewInt(1e15),
		},
		Bids:      []maker.PriceLevel{{Price: "99.99", Amount: "0.002"}, {Price: "99.98", Amount: "0.003"}},
		Asks:      []maker.PriceLevel{{Price: "100.01", Amount: "0.004"}, {Price: "100.02", Amount: "0.005"}},
		UpdatedAt: 1700000000, MajorVersion: 12, MinorVersion: 34,
		Signer: &quoteSigner{key: key},
	}
}

// Capture real binary envelopes after the same handshake used by the backend.
func captureMaker(t *testing.T) (*maker.Maker, <-chan *pb.MarketFrame) {
	t.Helper()
	frames := make(chan *pb.MarketFrame, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		ack, err := proto.Marshal(&pb.MakerEnvelope{
			Type:    pb.MakerMessageType_MAKER_MESSAGE_TYPE_CONNECTION_ACK,
			Payload: &pb.MakerEnvelope_ConnectionAck{ConnectionAck: &pb.ConnectionAck{Success: true, Identity: "maker"}},
		})
		if err != nil || ws.WriteMessage(websocket.BinaryMessage, ack) != nil {
			return
		}
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var env pb.MakerEnvelope
			if err := proto.Unmarshal(data, &env); err != nil {
				t.Errorf("decode envelope: %v", err)
				return
			}
			if env.Type == pb.MakerMessageType_MAKER_MESSAGE_TYPE_FRAME_PUSH {
				frames <- env.GetFramePush()
			}
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	p, err := maker.Dial(ctx, maker.MakerConfig{Connection: maker.ConnectionConfig{
		URL: "ws" + strings.TrimPrefix(server.URL, "http"), APIKey: "test-key",
		Reconnect: maker.ReconnectConfig{Policy: maker.Disabled},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, frames
}

func TestPublishBuildsAndSignsDecimalQuotes(t *testing.T) {
	for _, name := range []string{
		"two sides", "bid only", "ask only", "withdraw", "negative price scale", "uint24 boundaries", "five levels",
		"rounded quotes", "rounded negative price scale", "nondecimal steps", "rounds into uint24 boundaries", "ask rounds to first tick",
	} {
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			var wantBids, wantAsks []maker.PriceLevel
			switch name {
			case "bid only":
				params.Asks = nil
			case "ask only":
				params.Bids = nil
			case "withdraw":
				params.Bids, params.Asks = nil, nil
			case "negative price scale":
				params.Pair.BaseDecimals, params.Pair.QuoteDecimals = 20, 0
				params.Pair.PriceTickSize = big.NewInt(1)
				params.Bids = []maker.PriceLevel{{Price: "100", Amount: "0.00001"}}
				params.Asks = []maker.PriceLevel{{Price: "200", Amount: "0.00002"}}
			case "uint24 boundaries":
				params.Bids = []maker.PriceLevel{{Price: "0.01", Amount: "16777.215"}}
				params.Asks = []maker.PriceLevel{{Price: "167772.15", Amount: "0.001"}}
			case "five levels":
				params.Bids = append(params.Bids, maker.PriceLevel{Price: "99.97", Amount: "0.001"}, maker.PriceLevel{Price: "99.96", Amount: "0.001"}, maker.PriceLevel{Price: "99.95", Amount: "0.001"})
				params.Asks = append(params.Asks, maker.PriceLevel{Price: "100.03", Amount: "0.001"}, maker.PriceLevel{Price: "100.04", Amount: "0.001"}, maker.PriceLevel{Price: "100.05", Amount: "0.001"})
			case "rounded quotes":
				params.Bids = []maker.PriceLevel{{Price: "99.999999999999999999999999999", Amount: "0.0029"}, {Price: "99.989", Amount: "0.003999"}}
				params.Asks = []maker.PriceLevel{{Price: "100.000000000000000000000000001", Amount: "0.0049"}, {Price: "100.010001", Amount: "0.005999"}}
				wantBids = []maker.PriceLevel{{Price: "99.99", Amount: "0.002"}, {Price: "99.98", Amount: "0.003"}}
				wantAsks = []maker.PriceLevel{{Price: "100.01", Amount: "0.004"}, {Price: "100.02", Amount: "0.005"}}
			case "rounded negative price scale":
				params.Pair.BaseDecimals, params.Pair.QuoteDecimals = 20, 0
				params.Pair.PriceTickSize = big.NewInt(1)
				params.Bids = []maker.PriceLevel{{Price: "199.999", Amount: "0.000019"}}
				params.Asks = []maker.PriceLevel{{Price: "200.001", Amount: "0.000029"}}
				wantBids = []maker.PriceLevel{{Price: "100", Amount: "0.00001"}}
				wantAsks = []maker.PriceLevel{{Price: "300", Amount: "0.00002"}}
			case "nondecimal steps":
				params.Pair.PriceTickSize, params.Pair.LotSize = big.NewInt(25000), big.NewInt(3e15)
				params.Bids = []maker.PriceLevel{{Price: "99.999", Amount: "0.0089"}}
				params.Asks = []maker.PriceLevel{{Price: "100.001", Amount: "0.0119"}}
				wantBids = []maker.PriceLevel{{Price: "99.975", Amount: "0.006"}}
				wantAsks = []maker.PriceLevel{{Price: "100.025", Amount: "0.009"}}
			case "rounds into uint24 boundaries":
				params.Bids = []maker.PriceLevel{{Price: "167772.159999", Amount: "16777.215999"}}
				params.Asks = nil
				wantBids = []maker.PriceLevel{{Price: "167772.15", Amount: "16777.215"}}
			case "ask rounds to first tick":
				params.Bids = nil
				params.Asks = []maker.PriceLevel{{Price: "0.000001", Amount: "0.001999"}}
				wantAsks = []maker.PriceLevel{{Price: "0.01", Amount: "0.001"}}
			}
			if wantBids == nil {
				wantBids = params.Bids
			}
			if wantAsks == nil {
				wantAsks = params.Asks
			}
			checkQuoteParamsUnchanged(t, &params)
			p, frames := captureMaker(t)
			if err := p.Publish(context.Background(), params); err != nil {
				t.Fatal(err)
			}
			var wire *pb.MarketFrame
			select {
			case wire = <-frames:
			case <-time.After(2 * time.Second):
				t.Fatal("no frame received")
			}
			signed := frame.SignedFrame{
				Frame: frame.CompactFrame{
					Header: [32]byte(wire.Frame.Header), BidLevels: [32]byte(wire.Frame.BidLevels), AskLevels: [32]byte(wire.Frame.AskLevels),
				},
				Maker: common.HexToAddress(wire.MakerAddr), Signature: wire.Signature,
			}
			if err := frame.VerifySignature(frame.SigningDomain{ChainID: params.ChainID, Protocol: params.Protocol}, signed, params.Signer.Address()); err != nil {
				t.Fatal(err)
			}
			h := frame.DecodeHeader(signed.Frame)
			if h.PairID != params.Pair.PairID || h.UpdatedAt != params.UpdatedAt || h.MajorVersion != params.MajorVersion || h.MinorVersion != params.MinorVersion || h.CodecVersion != frame.CodecVersion {
				t.Fatalf("unexpected header: %+v", h)
			}
			if wire.Pair.ChainId != params.ChainID || wire.Pair.BaseToken != params.Pair.BaseToken.Hex() || wire.Pair.QuoteToken != params.Pair.QuoteToken.Hex() || signed.Maker != params.Maker || wire.Signature[64] < 27 {
				t.Fatalf("unexpected metadata/signature: %v", wire)
			}
			for _, side := range []struct {
				input []maker.PriceLevel
				want  []maker.PriceLevel
				wire  []*pb.PriceLevel
				word  [32]byte
				bid   bool
			}{{params.Bids, wantBids, wire.Bids, signed.Frame.BidLevels, true}, {params.Asks, wantAsks, wire.Asks, signed.Frame.AskLevels, false}} {
				levels, err := frame.DecodeLevels(side.word)
				if err != nil || len(levels) != len(side.input) || len(side.wire) != len(side.input) {
					t.Fatalf("level count mismatch: %v", err)
				}
				for i, level := range levels {
					if side.wire[i].Price != side.want[i].Price || side.wire[i].Amount != side.want[i].Amount {
						t.Fatalf("wire quote = %v, want %v (input %v)", side.wire[i], side.want[i], side.input[i])
					}
					// Independently reconstruct human price and amount from signed
					// integers, including unequal decimals and negative scales.
					tick := h.BaseTick + level.Offset
					if side.bid {
						tick = h.BaseTick - level.Offset
					}
					price := new(big.Rat).SetInt(new(big.Int).Mul(new(big.Int).SetUint64(uint64(tick)), params.Pair.PriceTickSize))
					price.Mul(price, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(params.Pair.BaseDecimals)), nil)))
					price.Quo(price, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18+int64(params.Pair.QuoteDecimals)), nil)))
					amount := new(big.Rat).SetFrac(new(big.Int).Mul(new(big.Int).SetUint64(uint64(level.QtyLots)), params.Pair.LotSize), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(params.Pair.BaseDecimals)), nil))
					wantPrice, _ := new(big.Rat).SetString(side.want[i].Price)
					wantAmount, _ := new(big.Rat).SetString(side.want[i].Amount)
					if price.Cmp(wantPrice) != 0 || amount.Cmp(wantAmount) != 0 {
						t.Fatalf("signed price/amount differ: %s/%s", price, amount)
					}
				}
			}
		})
	}
}

func checkQuoteParamsUnchanged(t *testing.T, params *maker.PublishParams) {
	t.Helper()
	before := *params
	before.Bids = append([]maker.PriceLevel(nil), params.Bids...)
	before.Asks = append([]maker.PriceLevel(nil), params.Asks...)
	before.Pair.PriceTickSize = new(big.Int).Set(params.Pair.PriceTickSize)
	before.Pair.LotSize = new(big.Int).Set(params.Pair.LotSize)
	before.Signer = nil
	t.Cleanup(func() {
		after := *params
		after.Signer = nil
		if !reflect.DeepEqual(before, after) {
			t.Error("quote conversion mutated caller-owned parameters")
		}
	})
}

func TestPublishRejectsInvalidQuotesBeforeSigning(t *testing.T) {
	for name, change := range map[string]func(*maker.PublishParams){
		"bid rounds to zero":     func(p *maker.PublishParams) { p.Bids[0].Price = "0.009" },
		"amount rounds to zero":  func(p *maker.PublishParams) { p.Bids[0].Amount = "0.000999999" },
		"zero price":             func(p *maker.PublishParams) { p.Bids[0].Price = "0" },
		"zero amount":            func(p *maker.PublishParams) { p.Bids[0].Amount = "0" },
		"negative":               func(p *maker.PublishParams) { p.Bids[0].Price = "-1" },
		"exponent":               func(p *maker.PublishParams) { p.Bids[0].Price = "1e2" },
		"fraction":               func(p *maker.PublishParams) { p.Bids[0].Amount = "1/2" },
		"whitespace":             func(p *maker.PublishParams) { p.Bids[0].Price = " 99" },
		"too long":               func(p *maker.PublishParams) { p.Bids[0].Price = strings.Repeat("1", 65) },
		"tick overflow":          func(p *maker.PublishParams) { p.Asks[0].Price = "167772.16" },
		"ask rounds past uint24": func(p *maker.PublishParams) { p.Asks[0].Price = "167772.150001" },
		"bid past uint24":        func(p *maker.PublishParams) { p.Bids[0].Price = "167772.16" },
		"lot overflow":           func(p *maker.PublishParams) { p.Bids[0].Amount = "16777.216" },
		"unordered bids":         func(p *maker.PublishParams) { p.Bids[1].Price = "100" },
		"unordered asks":         func(p *maker.PublishParams) { p.Asks[1].Price = "100" },
		"duplicate prices":       func(p *maker.PublishParams) { p.Bids[1].Price = p.Bids[0].Price },
		"rounded duplicate bids": func(p *maker.PublishParams) { p.Bids[0].Price, p.Bids[1].Price = "99.999", "99.991" },
		"rounded duplicate asks": func(p *maker.PublishParams) { p.Asks[0].Price, p.Asks[1].Price = "100.001", "100.009" },
		"locked book":            func(p *maker.PublishParams) { p.Bids[0].Price = p.Asks[0].Price },
		"crossed book":           func(p *maker.PublishParams) { p.Bids[0].Price = "101" },
		"unaligned locked book":  func(p *maker.PublishParams) { p.Bids[0].Price, p.Asks[0].Price = "100.005", "100.005" },
		"unaligned crossed book": func(p *maker.PublishParams) { p.Bids[0].Price, p.Asks[0].Price = "100.005", "100.004" },
		"six levels":             func(p *maker.PublishParams) { p.Bids = make([]maker.PriceLevel, 6) },
		"missing scale":          func(p *maker.PublishParams) { p.Pair.PriceTickSize = nil },
		"zero lot size":          func(p *maker.PublishParams) { p.Pair.LotSize = new(big.Int) },
		"missing pair":           func(p *maker.PublishParams) { p.Pair.PairID = 0 },
		"missing chain":          func(p *maker.PublishParams) { p.ChainID = 0 },
		"missing protocol":       func(p *maker.PublishParams) { p.Protocol = common.Address{} },
		"missing maker":          func(p *maker.PublishParams) { p.Maker = common.Address{} },
	} {
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			change(&params)
			// A disconnected zero value ensures validation never reaches transport.
			var p maker.Maker
			assertNotSent(t, p.Publish(context.Background(), params), nil)
			if params.Signer.(*quoteSigner).calls != 0 {
				t.Fatal("signed an invalid quote")
			}
		})
	}
}

func assertNotSent(t *testing.T, err, cause error) {
	t.Helper()
	var publishErr *maker.PublishError
	if !errors.As(err, &publishErr) || publishErr.Outcome != maker.NotSent || (cause != nil && !errors.Is(err, cause)) {
		t.Fatalf("expected NotSent wrapping %v, got %v", cause, err)
	}
}

func TestPublishSigningAndCancellationFailures(t *testing.T) {
	var p maker.Maker
	params := quoteParams(t)
	signer := params.Signer.(*quoteSigner)
	signer.err = errors.New("remote signer unavailable")
	assertNotSent(t, p.Publish(context.Background(), params), signer.err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertNotSent(t, p.Publish(ctx, params), context.Canceled)
	if signer.calls != 1 {
		t.Fatal("canceled publish invoked signer")
	}
	signer.err = nil
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	signer.cancel = cancel
	assertNotSent(t, p.Publish(ctx, params), context.Canceled)
	params.Signer = nil
	assertNotSent(t, p.Publish(context.Background(), params), nil)
	var nilSigner *quoteSigner
	params.Signer = nilSigner
	assertNotSent(t, p.Publish(context.Background(), params), nil)
}

func TestPublishPreservesTransportNotSent(t *testing.T) {
	p, _ := captureMaker(t)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertNotSent(t, p.Publish(context.Background(), quoteParams(t)), maker.ErrClosed)
}
