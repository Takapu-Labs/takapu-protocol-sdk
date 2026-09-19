package router

import (
	stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/common"
)

// Router maintains subscriptions and streams matching market frames to callers.
// Its methods are safe for concurrent use. Construct it with Dial;
// the zero value is not ready for use.
type Router struct {
	impl *stream.Router
}

// ReconnectPolicy controls retries after a connection is lost.
type ReconnectPolicy = stream.ReconnectPolicy

// ReconnectConfig controls exponential backoff with jitter. Zero durations use
// defaults of 500 milliseconds, 30 seconds, and 60 seconds respectively.
type ReconnectConfig = stream.ReconnectConfig

// ConnectionConfig configures authentication, transport limits, and reconnects.
// An empty URL uses DefaultRouterURL.
// Zero timeouts and capacities use defaults; negative values are invalid.
// Credentials are fixed at dialing and reused on reconnect; changes require a new client.
type ConnectionConfig = stream.ConnectionConfig

// ListingConfig contains only HTTP settings. Routers reuse their
// Connection.APIKey for Listing authentication.
// The zero value disables Listing while preserving WebSocket-only clients.
type ListingConfig = stream.ListingConfig

// RouterConfig configures subscriptions and the frame delivery buffer.
type RouterConfig = stream.RouterConfig

// QuoteConfig supplies pair metadata, effective fees and the decay start for
// local quotes; BuildSwap and BuildSwapExactIn do not use it. Pair.LotSize controls
// whole-lot truncation. Pair.BaseDecimals and Pair.QuoteDecimals convert the frame's
// readable prices and amounts to native units. Pair.PriceTickSize is validated
// as part of the pair configuration but is not used to reconstruct prices.
// The listing API's Tax is the same protocol fee described by FeeToken and
// FeeRatePPM, not an additional deduction. Callers supply current configuration;
// quoting does not fetch it or retain it. When protocol fee collection is
// disabled, callers must set both FeeToken and FeeRatePPM to zero.
// protocol.Client.EffectivePairConfig provides fee fields with this applied.
type QuoteConfig struct {
	Pair frame.PairParams
	// FeeToken is either pair token, or zero when FeeRatePPM is zero.
	FeeToken common.Address
	// FeeRatePPM is the effective fee rate, below 1,000,000. Zero disables fees.
	FeeRatePPM uint32
	// DecayStartOffsetSeconds is the inclusive maximum age for an undecayed
	// quote, from protocol.Client.GlobalDecayConfig. Zero permits only frames
	// whose UpdatedAt equals the current Unix second; there is no default limit.
	DecayStartOffsetSeconds uint32
}

// ClientPhase identifies the current connection lifecycle state.
type ClientPhase = stream.ClientPhase

// ClientStatus is a snapshot; reconnecting invalidates quotes from older
// generations, and replacing subscriptions invalidates older revisions.
type ClientStatus = stream.ClientStatus

// ClientStats combines lifetime counters with current queue sizes.
type ClientStats = stream.ClientStats

// EventKind identifies which event payload is populated.
type EventKind = stream.EventKind

// RouterEvent reports a connection change, subscription result, or server error.
// Frame updates arrive on the channel returned by Subscribe.
type RouterEvent = stream.RouterEvent

// PairFilter selects pairs and makers using the wire protocol's filter type.
type PairFilter = stream.PairFilter

// PairKey identifies a pair by its chain and on-chain pair ID.
type PairKey = stream.PairKey

// FrameKey identifies one maker's frame stream for an on-chain pair.
type FrameKey = stream.FrameKey

// FrameUpdate transfers ownership of a market frame to its receiver, who may
// mutate or cache it. Generation and SubscriptionRevision identify its source;
// callers must invalidate retained frames when either changes or the router
// disconnects. Updates are delivered in receive order, including repeated keys.
type FrameUpdate = stream.FrameUpdate

// SubscriptionKind identifies the subscription operation. Subscribe uses Replace.
type SubscriptionKind = stream.SubscriptionKind

// SubscriptionItemResult describes the server's decision for one requested filter.
type SubscriptionItemResult = stream.SubscriptionItemResult

// SubscriptionResult is the acknowledgement for a complete subscription request.
// Results preserves filter order. Rejected filters
// remain part of the saved request used for restoration after reconnect.
type SubscriptionResult = stream.SubscriptionResult

// ServerErrorInfo is an error reported by the router service.
type ServerErrorInfo = stream.ServerErrorInfo
