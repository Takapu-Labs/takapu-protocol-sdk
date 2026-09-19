package frame_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

// These fixed vectors were independently generated with Foundry cast against
// UpdateFrame(bytes32 header,bytes32 bidLevels,bytes32 askLevels,address maker).
// The digest uses cast abi-encode/keccak; signatures use cast wallet sign --data.
// Tests require no sibling source tree, cast, RPC or wallet.
const (
	headerHex      = "00000000000000000000000000000001000000070001f40000000001000003e8"
	bidsHex        = "0000000000000000000000000000000000000000000000000000000002000064"
	asksHex        = "0000000000000000000000000000000000000000000000000000000003000032"
	digestHex      = "da81cb33874b3c2a3f4e3aa1e243644cab62bd8bc8464554b76819abbc0b2a96"
	signatureHex   = "7aa782866e3f4c04115a141a3590dcb725a110565cd78bddc8a56ab2704a688201be590112187b7edf620a0796864aed1f126f242af5d9b1bf69b5adc5852b821c"
	highSHex       = "7aa782866e3f4c04115a141a3590dcb725a110565cd78bddc8a56ab2704a6882fe41a6feede78481209df5f86979b5119b9c6dc28452c68a0068a8df0ab115bf1b"
	wrongSignerHex = "ba3a1cb4c4761656babbbe23fc2cd347bb3a3d8b412a38cb71c7e5e8c68605a303757138bc82c1d4b002419378b15f814e2a8fd6a5d559cd143227c34a373b521b"
	testPrivateKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

var expectedSigner = common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")

func spec() frame.FrameSpec {
	return frame.FrameSpec{ChainID: 1, Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"), Maker: common.HexToAddress("0x2222222222222222222222222222222222222222"), Pair: frame.PairParams{PairID: 7, BaseToken: common.HexToAddress("0x0000000000000000000000000000000000000001"), QuoteToken: common.HexToAddress("0x0000000000000000000000000000000000000002"), PriceTickSize: big.NewInt(1e18), LotSize: big.NewInt(1)}, BaseTick: 500, Bids: []frame.Level{{Offset: 2, QtyLots: 100}}, Asks: []frame.Level{{Offset: 3, QtyLots: 50}}, UpdatedAt: 1000, MajorVersion: 1, CodecVersion: 1}
}

func mustPrepare(t *testing.T, s frame.FrameSpec) *frame.PreparedFrame {
	t.Helper()
	p, err := frame.Prepare(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func array(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatal("bad fixture")
	}
	return [32]byte(b)
}

type keySigner struct{ key *ecdsa.PrivateKey }

func (s keySigner) Address() common.Address {
	return crypto.PubkeyToAddress(s.key.PublicKey)
}

func (s keySigner) SignDigest(ctx context.Context, d common.Hash) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crypto.Sign(d[:], s.key)
}

func signer(t *testing.T) keySigner {
	t.Helper()
	k, err := crypto.HexToECDSA(testPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return keySigner{k}
}

func TestIndependentSolidityEncodingAndSigningVector(t *testing.T) {
	p := mustPrepare(t, spec())
	payload := p.SigningPayload()
	want := frame.CompactFrame{Header: array(t, headerHex), BidLevels: array(t, bidsHex), AskLevels: array(t, asksHex)}
	if payload.Frame != want {
		t.Fatalf("encoded frame differs from Solidity vector: %+v", payload.Frame)
	}
	if payload.Digest != common.HexToHash(digestHex) {
		t.Fatalf("digest %s", payload.Digest)
	}
	f, err := frame.BuildAndSign(context.Background(), spec(), signer(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Signature, common.FromHex(signatureHex)) {
		t.Fatalf("signature %x", f.Signature)
	}
	if f.MakerAddr != spec().Maker.Hex() {
		t.Fatal("market frame MakerAddr must match the maker address")
	}
	if f.Bids[0].Price != "498" || f.Bids[0].Amount != "100" || f.Asks[0].Price != "503" || f.Asks[0].Amount != "50" {
		t.Fatalf("unexpected presentation: %v", f)
	}
	signed := frame.SignedFrame{
		Frame: frame.CompactFrame{
			Header: [32]byte(f.Frame.Header), BidLevels: [32]byte(f.Frame.BidLevels), AskLevels: [32]byte(f.Frame.AskLevels),
		},
		Maker: common.HexToAddress(f.MakerAddr), Signature: f.Signature,
	}
	if signed.Frame != want || signed.Maker != spec().Maker {
		t.Fatal("signed wire payload differs from Solidity vector")
	}
	if err := frame.VerifySignature(payload.Domain, signed, expectedSigner); err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip := new(pb.MarketFrame)
	if err := proto.Unmarshal(data, roundtrip); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(roundtrip, f) {
		t.Fatal("protobuf roundtrip changed frame")
	}
}

func TestPrepareSigningPayloadMatchesPreparedFrame(t *testing.T) {
	for _, name := range []string{"two-sided", "bid-only", "ask-only", "empty"} {
		t.Run(name, func(t *testing.T) {
			s := spec()
			if name == "ask-only" || name == "empty" {
				s.Bids = nil
			}
			if name == "bid-only" || name == "empty" {
				s.Asks = nil
			}
			payload, err := frame.PrepareSigningPayload(s)
			if err != nil {
				t.Fatal(err)
			}
			if want := mustPrepare(t, s).SigningPayload(); payload != want {
				t.Fatalf("signing payload differs from prepared frame: got %+v, want %+v", payload, want)
			}
			signed, err := frame.SignFrame(context.Background(), payload.Domain, payload.Maker, payload.Frame, signer(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := frame.VerifySignature(payload.Domain, signed, expectedSigner); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrepareSigningPayloadSkipsReadableDecimalLimits(t *testing.T) {
	for name, change := range map[string]func(*frame.FrameSpec){
		"price": func(s *frame.FrameSpec) { s.Pair.QuoteDecimals = 255 },
		"amount": func(s *frame.FrameSpec) {
			s.Pair.BaseDecimals = 255
			s.Pair.QuoteDecimals = 237
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := spec()
			change(&s)
			if _, err := frame.Prepare(s); err == nil || !strings.Contains(err.Error(), "64-character limit") {
				t.Fatalf("expected readable decimal limit, got %v", err)
			}
			payload, err := frame.PrepareSigningPayload(s)
			if err != nil {
				t.Fatal(err)
			}
			if want := mustPrepare(t, spec()).SigningPayload(); payload != want {
				t.Fatal("display-only decimal metadata changed the signed payload")
			}
			signed, err := frame.SignFrame(context.Background(), payload.Domain, payload.Maker, payload.Frame, signer(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := frame.VerifySignature(payload.Domain, signed, expectedSigner); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSignatureDomainAndEverySignedFieldAreBound(t *testing.T) {
	p := mustPrepare(t, spec()).SigningPayload()
	signed := frame.SignedFrame{Frame: p.Frame, Maker: p.Maker, Signature: common.FromHex(signatureHex)}
	tests := map[string]func(*frame.SigningDomain, *frame.SignedFrame){
		"chain":  func(d *frame.SigningDomain, _ *frame.SignedFrame) { d.ChainID++ },
		"proxy":  func(d *frame.SigningDomain, _ *frame.SignedFrame) { d.Protocol[0]++ },
		"maker":  func(_ *frame.SigningDomain, s *frame.SignedFrame) { s.Maker[0]++ },
		"header": func(_ *frame.SigningDomain, s *frame.SignedFrame) { s.Frame.Header[31]++ },
		"bids":   func(_ *frame.SigningDomain, s *frame.SignedFrame) { s.Frame.BidLevels[31]++ },
		"asks":   func(_ *frame.SigningDomain, s *frame.SignedFrame) { s.Frame.AskLevels[31]++ },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			d, s := p.Domain, signed
			change(&d, &s)
			if err := frame.VerifySignature(d, s, expectedSigner); err == nil {
				t.Fatal("tampered quote verified")
			}
		})
	}
}

func TestRemoteSigningCopiesAndNormalizes(t *testing.T) {
	s := spec()
	p := mustPrepare(t, s)
	original := p.SigningPayload()
	s.Bids[0].QtyLots = 200
	s.Pair.PriceTickSize.SetInt64(1)
	s.Pair.LotSize.SetInt64(2)
	payload := p.SigningPayload()
	payload.Frame.Header[31]++
	if p.SigningPayload() != original {
		t.Fatal("mutable input or payload altered prepared frame")
	}
	sig := common.FromHex(signatureHex)
	sig[64] -= 27
	f, err := p.AttachSignature(sig, expectedSigner)
	if err != nil {
		t.Fatal(err)
	}
	if f.Signature[64] != 28 || sig[64] != 1 {
		t.Fatal("signature normalization modified input or failed")
	}
	f.Signature[0] ^= 1
	f.Bids[0].Price = "1"
	f.Frame.Header[31] ^= 1
	again, err := p.AttachSignature(sig, expectedSigner)
	if err != nil {
		t.Fatal(err)
	}
	if again.Bids[0].Price != "498" || !bytes.Equal(again.Signature, common.FromHex(signatureHex)) || [32]byte(again.Frame.Header) != original.Frame.Header {
		t.Fatal("returned frame aliased prepared state")
	}
	if _, err := (*frame.PreparedFrame)(nil).AttachSignature(sig, expectedSigner); err == nil {
		t.Fatal("nil preparation accepted")
	}
	if _, err := new(frame.PreparedFrame).AttachSignature(sig, expectedSigner); err == nil {
		t.Fatal("zero preparation accepted")
	}
}

func TestExactDecimalScalingAndUint128(t *testing.T) {
	s := spec()
	s.Pair.BaseDecimals = 18
	s.Pair.QuoteDecimals = 6
	s.Pair.PriceTickSize = big.NewInt(10000)
	s.Pair.LotSize = big.NewInt(10000000000000000)
	f, err := frame.BuildAndSign(context.Background(), s, signer(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.Bids[0].Price != "4.98" || f.Bids[0].Amount != "1" || f.Asks[0].Price != "5.03" || f.Asks[0].Amount != "0.5" {
		t.Fatalf("bad decimal conversion: %v", f)
	}
	// Values exceed uint64, but fit uint128 and must not truncate.
	s.Pair.BaseDecimals = 0
	s.Pair.QuoteDecimals = 0
	s.Pair.PriceTickSize = new(big.Int).Lsh(big.NewInt(1), 100)
	s.Pair.LotSize = new(big.Int).Lsh(big.NewInt(1), 100)
	f, err = frame.BuildAndSign(context.Background(), s, signer(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.Bids[0].Price != "631289998913658.241945358196277248" || f.Asks[0].Amount != "63382530011411470074835160268800" {
		t.Fatalf("uint128 values truncated: %v", f)
	}
	// Negative decimal exponent yields an integer, without scientific notation.
	s.Pair.BaseDecimals = 30
	s.Pair.QuoteDecimals = 0
	s.Pair.PriceTickSize = big.NewInt(1)
	s.Pair.LotSize = big.NewInt(1)
	f, err = frame.BuildAndSign(context.Background(), s, signer(t))
	if err != nil {
		t.Fatal(err)
	}
	if f.Bids[0].Price != "498000000000000" || f.Asks[0].Amount != "0.00000000000000000000000000005" {
		t.Fatalf("negative scale: %v", f)
	}
}

func TestPrepareInputRejections(t *testing.T) {
	tests := map[string]func(*frame.FrameSpec){
		"chain": func(s *frame.FrameSpec) { s.ChainID = 0 }, "protocol": func(s *frame.FrameSpec) { s.Protocol = common.Address{} }, "maker": func(s *frame.FrameSpec) { s.Maker = common.Address{} },
		"pair": func(s *frame.FrameSpec) { s.Pair.PairID = 0 }, "zero token": func(s *frame.FrameSpec) { s.Pair.BaseToken = common.Address{} }, "same token": func(s *frame.FrameSpec) { s.Pair.QuoteToken = s.Pair.BaseToken },
		"tick nil": func(s *frame.FrameSpec) { s.Pair.PriceTickSize = nil }, "lot nil": func(s *frame.FrameSpec) { s.Pair.LotSize = nil }, "tick zero": func(s *frame.FrameSpec) { s.Pair.PriceTickSize = big.NewInt(0) }, "negative lot": func(s *frame.FrameSpec) { s.Pair.LotSize = big.NewInt(-1) },
		"uint128 overflow": func(s *frame.FrameSpec) { s.Pair.PriceTickSize = new(big.Int).Lsh(big.NewInt(1), 128) },
		"codec":            func(s *frame.FrameSpec) { s.CodecVersion = 2 }, "zero base": func(s *frame.FrameSpec) { s.BaseTick = 0 }, "base overflow": func(s *frame.FrameSpec) { s.BaseTick = 1 << 24 },
		"six levels": func(s *frame.FrameSpec) { s.Bids = make([]frame.Level, 6) }, "zero lot": func(s *frame.FrameSpec) { s.Bids[0].QtyLots = 0 }, "lot overflow": func(s *frame.FrameSpec) { s.Bids[0].QtyLots = 1 << 24 }, "offset overflow": func(s *frame.FrameSpec) { s.Bids[0].Offset = 1 << 24 },
		"bid zero price": func(s *frame.FrameSpec) { s.Bids[0].Offset = s.BaseTick }, "ask overflow": func(s *frame.FrameSpec) { s.Asks[0].Offset = frame.MaxTick }, "locked book": func(s *frame.FrameSpec) {
			s.Bids[0].Offset = 0
			s.Asks[0].Offset = 0
		},
		"duplicate price": func(s *frame.FrameSpec) { s.Bids = append(s.Bids, frame.Level{Offset: 2, QtyLots: 1}) }, "wrong order": func(s *frame.FrameSpec) { s.Bids = append(s.Bids, frame.Level{Offset: 1, QtyLots: 1}) },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			s := spec()
			change(&s)
			if _, err := frame.Prepare(s); err == nil {
				t.Fatal("invalid spec accepted")
			}
			if payload, err := frame.PrepareSigningPayload(s); err == nil || payload != (frame.Payload{}) {
				t.Fatalf("invalid signing spec returned payload %+v, error %v", payload, err)
			}
		})
	}
}

func TestEmptySingleSidedAndBoundaryBooks(t *testing.T) {
	for _, sides := range []struct {
		name     string
		bid, ask bool
	}{{"empty", false, false}, {"bid", true, false}, {"ask", false, true}} {
		t.Run(sides.name, func(t *testing.T) {
			s := spec()
			if !sides.bid {
				s.Bids = nil
			}
			if !sides.ask {
				s.Asks = nil
			}
			f, err := frame.BuildAndSign(context.Background(), s, signer(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(f.Bids) != len(s.Bids) || len(f.Asks) != len(s.Asks) {
				t.Fatal("signed frame changed book depth")
			}
		})
	}
	s := spec()
	s.BaseTick = 1
	s.Bids = nil
	s.Asks = []frame.Level{{Offset: frame.MaxTick - 1, QtyLots: frame.MaxTick}}
	mustPrepare(t, s)
	s = spec()
	s.BaseTick = frame.MaxTick
	s.Asks = nil
	s.Bids = []frame.Level{{Offset: frame.MaxTick - 1, QtyLots: frame.MaxTick}}
	mustPrepare(t, s)
	s = spec()
	s.Bids = []frame.Level{{Offset: 0, QtyLots: 1}, {Offset: 1, QtyLots: 2}, {Offset: 2, QtyLots: 3}, {Offset: 3, QtyLots: 4}, {Offset: 499, QtyLots: frame.MaxTick}}
	s.Asks = nil
	p := mustPrepare(t, s)
	levels, err := frame.DecodeLevels(p.SigningPayload().Frame.BidLevels)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 5 || levels[4] != s.Bids[4] {
		t.Fatal("five levels did not roundtrip")
	}
	s = spec()
	s.UpdatedAt = ^uint32(0)
	s.MajorVersion = ^uint32(0)
	s.MinorVersion = 255
	s.Pair.PairID = ^uint32(0)
	h := frame.DecodeHeader(mustPrepare(t, s).SigningPayload().Frame)
	if h.UpdatedAt != s.UpdatedAt || h.MajorVersion != s.MajorVersion || h.MinorVersion != s.MinorVersion || h.PairID != s.Pair.PairID {
		t.Fatal("maximum header fields did not roundtrip")
	}
}

func TestContractCanonicalBitAndLadderRules(t *testing.T) {
	valid := mustPrepare(t, spec()).SigningPayload().Frame
	// Every protocol or reserved bit in header [128,256) must be canonical;
	// only the single codec-v1 bit (byte 15, bit 0) may be set.
	for i := 0; i < 16; i++ {
		for bit := uint(0); bit < 8; bit++ {
			f := valid
			f.Header[i] ^= 1 << bit
			if err := frame.ValidateCompactFrame(f); err == nil {
				t.Fatalf("noncanonical header byte=%d bit=%d accepted", i, bit)
			}
		}
	}
	for i := 0; i < 2; i++ {
		for bit := uint(0); bit < 8; bit++ {
			for side := 0; side < 2; side++ {
				f := valid
				if side == 0 {
					f.BidLevels[i] |= 1 << bit
				} else {
					f.AskLevels[i] |= 1 << bit
				}
				if err := frame.ValidateCompactFrame(f); err == nil {
					t.Fatalf("reserved levels byte=%d bit=%d side=%d accepted", i, bit, side)
				}
			}
		}
	}
	tests := map[string]func(*frame.CompactFrame){
		"noncanonical empty": func(f *frame.CompactFrame) { f.BidLevels[31] = 0 },
		"hole":               func(f *frame.CompactFrame) { copy(f.BidLevels[14:20], f.BidLevels[26:32]) },
		"duplicate offset":   func(f *frame.CompactFrame) { copy(f.BidLevels[20:26], f.BidLevels[26:32]) },
		"decreasing offset": func(f *frame.CompactFrame) {
			copy(f.BidLevels[20:26], f.BidLevels[26:32])
			f.BidLevels[22] = 1
		},
		"zero tick": func(f *frame.CompactFrame) {
			f.Header[20] = 0
			f.Header[21] = 0
			f.Header[22] = 0
		},
		"zero pair": func(f *frame.CompactFrame) { clear(f.Header[16:20]) },
		"bid price zero": func(f *frame.CompactFrame) {
			f.BidLevels[27] = 1
			f.BidLevels[28] = 244
		},
		"ask price overflow": func(f *frame.CompactFrame) {
			f.AskLevels[26] = 255
			f.AskLevels[27] = 255
			f.AskLevels[28] = 255
		},
		"locked": func(f *frame.CompactFrame) {
			f.BidLevels[28] = 0
			f.AskLevels[28] = 0
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			f := valid
			change(&f)
			if err := frame.ValidateCompactFrame(f); err == nil {
				t.Fatal("noncanonical frame accepted")
			}
		})
	}
}

func TestSignatureRejections(t *testing.T) {
	p := mustPrepare(t, spec())
	payload := p.SigningPayload()
	cases := map[string][]byte{"nil": nil, "short": make([]byte, 64), "long": make([]byte, 66), "zero": make([]byte, 65), "high-s": common.FromHex(highSHex), "wrong-signer": common.FromHex(wrongSignerHex)}
	invalidV := common.FromHex(signatureHex)
	invalidV[64] = 29
	cases["v"] = invalidV
	for name, sig := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.AttachSignature(sig, expectedSigner); err == nil {
				t.Fatal("bad signature accepted")
			}
		})
	}
	if _, err := p.AttachSignature(common.FromHex(signatureHex), common.Address{}); err == nil {
		t.Fatal("zero expected signer accepted")
	}
	if _, err := frame.SignFrame(context.Background(), payload.Domain, payload.Maker, payload.Frame, nil); err == nil {
		t.Fatal("nil signer accepted")
	}
	var typedNil *failingSigner
	if _, err := frame.SignFrame(context.Background(), payload.Domain, payload.Maker, payload.Frame, typedNil); err == nil {
		t.Fatal("typed nil signer accepted")
	}
	s := &failingSigner{}
	if _, err := frame.BuildAndSign(context.Background(), spec(), s); !errors.Is(err, errSigner) {
		t.Fatalf("signer error not preserved: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := frame.BuildAndSign(ctx, spec(), signer(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled signing: %v", err)
	}
	if _, err := frame.BuildAndSign(nil, spec(), signer(t)); err == nil { //nolint:staticcheck // Verify that a nil context is rejected.
		t.Fatal("nil context accepted")
	}
	bad := &wrongSigner{keySigner: signer(t)}
	if _, err := frame.BuildAndSign(context.Background(), spec(), bad); err == nil {
		t.Fatal("unexpected recovered signer accepted")
	}
}

var errSigner = errors.New("remote signer unavailable")

type failingSigner struct{}

func (*failingSigner) Address() common.Address {
	return expectedSigner
}

func (*failingSigner) SignDigest(context.Context, common.Hash) ([]byte, error) {
	return nil, errSigner
}

type wrongSigner struct{ keySigner }

func (*wrongSigner) SignDigest(context.Context, common.Hash) ([]byte, error) {
	return common.FromHex(wrongSignerHex), nil
}
