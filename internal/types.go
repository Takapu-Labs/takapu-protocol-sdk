package stream

import (
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

// ClientPhase identifies the current connection lifecycle state.
type ClientPhase string

// ClientStatus is a snapshot; reconnecting invalidates quotes from older
// generations, and replacing subscriptions invalidates older revisions.
type ClientStatus struct {
	Phase                ClientPhase
	Generation           uint64 // Increments when an authenticated connection is installed.
	SubscriptionRevision uint64 // Increments when a subscription request starts writing.
	Identity             string
	LastError            error // Most recent failure, retained after a successful reconnect.
}

// ClientStats combines lifetime counters with current queue sizes.
type ClientStats struct {
	MessagesSent     uint64
	MessagesReceived uint64
	BytesSent        uint64
	BytesReceived    uint64
	Reconnects       uint64 // Reconnection attempts, including failed handshakes.
	EventQueueLength int
	FrameQueueLength int // Router only; frames waiting for the caller to receive them.
}

// EventKind identifies which event payload is populated.
type EventKind string

// MakerEvent reports a connection change or an asynchronous frame rejection.
type MakerEvent struct {
	Kind      EventKind
	Rejection *pb.FrameAck
	Status    ClientStatus
}

// RouterEvent reports a connection change, subscription result, or server error.
// Frame updates arrive on the channel returned by Subscribe.
type RouterEvent struct {
	Kind         EventKind
	Status       ClientStatus
	Subscription SubscriptionResult
	ServerError  *ServerErrorInfo
}

// PairFilter selects pairs and makers using the wire protocol's filter type.
type PairFilter = pb.PairFilter

// PairKey identifies a pair by its chain and on-chain pair ID.
type PairKey = pb.PairKey

// FrameKey identifies one maker's frame stream for an on-chain pair.
type FrameKey struct {
	ChainID uint64
	PairID  uint32
	Maker   common.Address
}

// FrameUpdate transfers ownership of a market frame to its receiver, who may
// mutate or cache it. Generation and SubscriptionRevision identify its source;
// callers must invalidate retained frames when either changes or the router
// disconnects. Updates are delivered in receive order, including repeated keys.
type FrameUpdate struct {
	Key                  FrameKey
	Frame                *pb.MarketFrame
	Generation           uint64
	SubscriptionRevision uint64
}

// SubscriptionKind identifies the subscription operation. Subscribe uses Replace.
type SubscriptionKind string

// SubscriptionItemResult describes the server's decision for one requested filter.
type SubscriptionItemResult struct {
	Index    int
	Filter   *PairFilter
	Accepted bool
	Reason   string
}

// SubscriptionResult is the acknowledgement for a complete subscription request.
// Results preserves filter order. Rejected filters
// remain part of the saved request used for restoration after reconnect.
type SubscriptionResult struct {
	Kind                 SubscriptionKind
	Generation           uint64
	SubscriptionRevision uint64
	Results              []SubscriptionItemResult
}
