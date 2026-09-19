package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Level uses a uint24 price offset and a positive uint24 quantity of lots.
// Bid ticks are BaseTick-Offset; ask ticks are BaseTick+Offset. Best prices
// come first, so offsets must increase strictly within each side.
type Level struct{ Offset, QtyLots uint32 }

// CompactFrame contains the three big-endian words submitted to the protocol.
// Header, BidLevels and AskLevels are copied by value, without slice aliases.
type CompactFrame struct{ Header, BidLevels, AskLevels [32]byte }

// Header exposes all codec-v1 fields, including protocol-owned storage fields.
// ValidateCompactFrame checks whether a header is valid for quote submission;
// it does not verify the quote's signature or freshness.
type Header struct {
	UpdatedAt, MajorVersion                 uint32
	MinorVersion                            uint8
	BaseTick, PairID                        uint32
	CodecVersion                            uint16
	BidFilledLots, AskFilledLots, WrittenAt uint32
	Reserved                                uint16
}

// DecodeHeader reads all header fields without validating protocol constraints.
// Use ValidateCompactFrame before trusting a header from an external source.
func DecodeHeader(value CompactFrame) Header {
	b := value.Header
	return Header{
		UpdatedAt:     binary.BigEndian.Uint32(b[28:32]),
		MajorVersion:  binary.BigEndian.Uint32(b[24:28]),
		MinorVersion:  b[23],
		BaseTick:      get24(b[20:23]),
		PairID:        binary.BigEndian.Uint32(b[16:20]),
		CodecVersion:  binary.BigEndian.Uint16(b[14:16]),
		BidFilledLots: binary.BigEndian.Uint32(b[10:14]),
		AskFilledLots: binary.BigEndian.Uint32(b[6:10]),
		WrittenAt:     binary.BigEndian.Uint32(b[2:6]),
		Reserved:      binary.BigEndian.Uint16(b[:2]),
	}
}

// DecodeLevels validates and decodes a single side. The first level occupies
// bits [0,48), and only an all-zero tail may follow active levels.
func DecodeLevels(word [32]byte) ([]Level, error) {
	if word[0] != 0 || word[1] != 0 {
		return nil, errors.New("frame: reserved level bits must be zero")
	}
	var out []Level
	closed := false
	for i := 0; i < MaxLevels; i++ {
		start := 32 - (i+1)*6
		level := Level{
			Offset:  get24(word[start : start+3]),
			QtyLots: get24(word[start+3 : start+6]),
		}
		if level.Offset == 0 && level.QtyLots == 0 {
			closed = true
			continue
		}
		if level.QtyLots == 0 {
			return nil, fmt.Errorf("frame: level %d has offset but no quantity", i)
		}
		if closed {
			return nil, fmt.Errorf("frame: level %d follows an empty level", i)
		}
		if len(out) > 0 && level.Offset <= out[len(out)-1].Offset {
			return nil, fmt.Errorf("frame: level %d offsets must increase strictly", i)
		}
		out = append(out, level)
	}
	return out, nil
}

// ValidateCompactFrame checks codec-v1 calldata encoding and price boundaries.
// It accepts one-sided or empty books. Time freshness, version progression,
// registered pairs/makers, and chain state require the caller's context.
func ValidateCompactFrame(value CompactFrame) error {
	_, err := decodeCompactFrame(value)
	return err
}

// decodedCompactFrame carries validated values into wire validation, so the
// compact words need to be decoded only once and decoding errors stay explicit.
type decodedCompactFrame struct {
	header Header
	bids   []Level
	asks   []Level
}

func decodeCompactFrame(value CompactFrame) (decodedCompactFrame, error) {
	h := DecodeHeader(value)
	if h.CodecVersion != CodecVersion {
		return decodedCompactFrame{}, errors.New("frame: codec version must be 1")
	}
	if h.BidFilledLots != 0 || h.AskFilledLots != 0 || h.WrittenAt != 0 || h.Reserved != 0 {
		return decodedCompactFrame{}, errors.New("frame: protocol-owned header fields and reserved bits must be zero")
	}
	if h.PairID == 0 {
		return decodedCompactFrame{}, errors.New("frame: pair ID must be positive")
	}
	if h.BaseTick == 0 {
		return decodedCompactFrame{}, errors.New("frame: base tick must be positive")
	}
	bids, err := DecodeLevels(value.BidLevels)
	if err != nil {
		return decodedCompactFrame{}, fmt.Errorf("bids: %w", err)
	}
	asks, err := DecodeLevels(value.AskLevels)
	if err != nil {
		return decodedCompactFrame{}, fmt.Errorf("asks: %w", err)
	}
	if len(bids) > 0 && bids[len(bids)-1].Offset >= h.BaseTick {
		return decodedCompactFrame{}, errors.New("frame: bid price must be at least one tick")
	}
	if len(asks) > 0 && uint64(asks[len(asks)-1].Offset)+uint64(h.BaseTick) > uint64(MaxTick) {
		return decodedCompactFrame{}, errors.New("frame: ask price exceeds uint24")
	}
	if len(bids) > 0 && len(asks) > 0 && bids[0].Offset == 0 && asks[0].Offset == 0 {
		return decodedCompactFrame{}, errors.New("frame: best bid must be below best ask")
	}
	return decodedCompactFrame{header: h, bids: bids, asks: asks}, nil
}

func encodeCompactFrame(spec FrameSpec) (CompactFrame, error) {
	if spec.CodecVersion != CodecVersion {
		return CompactFrame{}, errors.New("frame: codec version must be 1")
	}
	if spec.BaseTick == 0 || spec.BaseTick > MaxTick {
		return CompactFrame{}, errors.New("frame: base tick outside uint24 positive range")
	}
	b, err := encodeLevels(spec.Bids)
	if err != nil {
		return CompactFrame{}, fmt.Errorf("frame: bids: %w", err)
	}
	a, err := encodeLevels(spec.Asks)
	if err != nil {
		return CompactFrame{}, fmt.Errorf("frame: asks: %w", err)
	}
	f := CompactFrame{BidLevels: b, AskLevels: a}
	binary.BigEndian.PutUint16(f.Header[14:16], spec.CodecVersion)
	binary.BigEndian.PutUint32(f.Header[16:20], spec.Pair.PairID)
	put24(f.Header[20:23], spec.BaseTick)
	f.Header[23] = spec.MinorVersion
	binary.BigEndian.PutUint32(f.Header[24:28], spec.MajorVersion)
	binary.BigEndian.PutUint32(f.Header[28:32], spec.UpdatedAt)
	if err := ValidateCompactFrame(f); err != nil {
		return CompactFrame{}, err
	}
	return f, nil
}

func put24(dst []byte, v uint32) {
	dst[0] = byte(v >> 16)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v)
}

func get24(src []byte) uint32 {
	return uint32(src[0])<<16 | uint32(src[1])<<8 | uint32(src[2])
}

func encodeLevels(levels []Level) ([32]byte, error) {
	var out [32]byte
	if len(levels) > MaxLevels {
		return out, errors.New("at most five levels are allowed")
	}
	for i, l := range levels {
		if l.Offset > MaxTick || l.QtyLots == 0 || l.QtyLots > MaxTick {
			return out, fmt.Errorf("level %d: offset must fit uint24; quantity must be a positive uint24", i)
		}
		if i > 0 && l.Offset <= levels[i-1].Offset {
			return out, fmt.Errorf("level %d: offsets must increase strictly", i)
		}
		start := 32 - (i+1)*6
		put24(out[start:start+3], l.Offset)
		put24(out[start+3:start+6], l.QtyLots)
	}
	return out, nil
}
