package frame

import (
	"context"
	"errors"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"
)

// Payload contains the exact EIP-712 digest for remote signing. Sign Digest
// directly, without personal_sign or a second hash. All fields are value types.
type Payload struct {
	Domain SigningDomain
	Maker  common.Address
	Frame  CompactFrame
	Digest common.Hash
}

// PreparedFrame is an immutable snapshot of a validated quote. Input slices and
// big.Int values are never retained. Its methods may be called concurrently.
type PreparedFrame struct {
	payload Payload
	wire    *pb.MarketFrame
}

// PrepareSigningPayload validates and encodes a quote for signing or on-chain
// submission, without building its decimal presentation. The returned payload
// is an independent snapshot and is not subject to readable decimal limits.
func PrepareSigningPayload(spec FrameSpec) (Payload, error) {
	domain := SigningDomain{ChainID: spec.ChainID, Protocol: spec.Protocol}
	if err := validateDomain(domain); err != nil {
		return Payload{}, err
	}
	if spec.Maker == (common.Address{}) {
		return Payload{}, errors.New("frame: maker is zero")
	}
	if err := ValidatePair(spec.Pair); err != nil {
		return Payload{}, err
	}
	f, err := encodeCompactFrame(spec)
	if err != nil {
		return Payload{}, err
	}
	return Payload{
		Domain: domain,
		Maker:  spec.Maker,
		Frame:  f,
		Digest: digest(domain, spec.Maker, f),
	}, nil
}

// Prepare encodes a quote and its exact decimal presentation, without signing.
func Prepare(spec FrameSpec) (*PreparedFrame, error) {
	payload, err := PrepareSigningPayload(spec)
	if err != nil {
		return nil, err
	}
	bids, err := humanLevels(spec.Bids, spec.BaseTick, true, spec.Pair)
	if err != nil {
		return nil, err
	}
	asks, err := humanLevels(spec.Asks, spec.BaseTick, false, spec.Pair)
	if err != nil {
		return nil, err
	}
	wire := &pb.MarketFrame{
		UpdatedAt: spec.UpdatedAt,
		Pair: &pb.PairKey{
			ChainId:    spec.ChainID,
			PairId:     spec.Pair.PairID,
			BaseToken:  spec.Pair.BaseToken.Hex(),
			QuoteToken: spec.Pair.QuoteToken.Hex(),
		},
		Bids: bids,
		Asks: asks,
		Frame: &pb.CompactFrame{
			Header:    payload.Frame.Header[:],
			BidLevels: payload.Frame.BidLevels[:],
			AskLevels: payload.Frame.AskLevels[:],
		},
		MakerAddr: spec.Maker.Hex(),
	}
	return &PreparedFrame{
		payload: payload,
		wire:    wire,
	}, nil
}

// SigningPayload returns an independent payload snapshot. A zero-value
// PreparedFrame produces an empty payload and cannot attach a signature.
func (p *PreparedFrame) SigningPayload() Payload {
	if p == nil {
		return Payload{}
	}
	return p.payload
}

// AttachSignature verifies the external signature against expectedSigner and
// returns a new MarketFrame. v is normalized to 27/28. No chain is queried.
func (p *PreparedFrame) AttachSignature(sig []byte, expectedSigner common.Address) (*pb.MarketFrame, error) {
	if p == nil || p.wire == nil {
		return nil, errors.New("frame: uninitialized prepared frame")
	}
	normalized, err := NormalizeSignature(sig)
	if err != nil {
		return nil, err
	}
	value := SignedFrame{Frame: p.payload.Frame, Maker: p.payload.Maker, Signature: normalized}
	if err := VerifySignature(p.payload.Domain, value, expectedSigner); err != nil {
		return nil, err
	}
	out := proto.Clone(p.wire).(*pb.MarketFrame)
	out.Signature = normalized
	return out, nil
}

// BuildAndSign prepares a quote and uses SignFrame to sign it. It neither
// publishes the quote nor advances timestamps or versions.
func BuildAndSign(ctx context.Context, spec FrameSpec, signer Signer) (*pb.MarketFrame, error) {
	p, err := Prepare(spec)
	if err != nil {
		return nil, err
	}
	signed, err := SignFrame(ctx, p.payload.Domain, p.payload.Maker, p.payload.Frame, signer)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(p.wire).(*pb.MarketFrame)
	out.Signature = signed.Signature
	return out, nil
}
