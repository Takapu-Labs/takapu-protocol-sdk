package stream

import (
	"fmt"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

func (r *Router) matchesLocked(key FrameKey) bool {
	for _, f := range r.accepted {
		if f.Pair != nil && (f.Pair.ChainId != key.ChainID || f.Pair.PairId != key.PairID) {
			continue
		}
		if f.Maker != "" && common.HexToAddress(f.Maker) != key.Maker {
			continue
		}
		return true
	}
	return false
}

func (r *Router) update(s *session, value *pb.MarketFrame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != s || !r.ready || r.stopped {
		// Pre-ack updates are deliberately discarded, never staged.
		return nil
	}
	if value == nil || value.Pair == nil {
		return fmt.Errorf("%w: missing frame or pair", ErrProtocol)
	}
	// Marketstream has already validated the frame. Read only its routing key
	// and pass the payload through without decoding or revalidating the book.
	key := FrameKey{
		ChainID: value.Pair.ChainId,
		PairID:  value.Pair.PairId,
		Maker:   common.HexToAddress(value.MakerAddr),
	}
	if !r.matchesLocked(key) {
		return nil
	}
	update := FrameUpdate{
		Key:                  key,
		Frame:                value,
		Generation:           s.generation,
		SubscriptionRevision: r.status.SubscriptionRevision,
	}
	select {
	case r.frames <- update:
		// The decoder allocated this frame for this message. The SDK does not
		// retain it after delivery, so ownership passes directly to the receiver.
		return nil
	default:
		r.stopLocked(ErrFrameBufferFull)
		return ErrFrameBufferFull
	}
}
