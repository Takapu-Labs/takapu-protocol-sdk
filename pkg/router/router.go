// Package router implements a Router WebSocket client with subscriptions, frame
// delivery, and an optional Listing HTTP client that shares its API
// credentials. An empty connection URL uses DefaultRouterURL; explicit URLs
// support other deployments and tests.
//
// Dial returns only after authentication. The supplied lifetime context
// controls streaming; each operation also accepts its own context. Close stops
// streaming and waits for cleanup. The optional Listing client remains usable
// after streaming stops. Credentials are fixed at dialing and reused on reconnect;
// changing them requires a new client.
//
// Subscribe returns a single-consumer frame channel after the
// server acknowledges the request. All subscriptions share that channel; it
// stays open across reconnects and closes after teardown. Each matching update
// transfers ownership of its protobuf frame to the caller, who may cache it.
// Marketstream validates frames; the client forwards them without revalidation.
// The router does not retain the latest frame or replay updates. Continuously
// consume both frames and events from Router.Events.
//
// Replacing subscriptions and disconnecting discard pending frames. Routers
// restore their complete subscription request on reconnect. Callers invalidate
// their own cached frames on disconnection, generation changes, and subscription
// revision changes; FrameUpdate carries both identifiers for checking against
// Router.Status, including updates already received when invalidation occurred.
//
// Quote computes an output amount from a caller-owned frame and explicit pair,
// fee and decay-start settings. It rejects frames past the configured decay start
// and needs no connection. BuildSwap and BuildSwapExactIn assemble swap parameters
// and calldata from the original signed update; see examples/router for the full flow.
//
// Event or frame-buffer overflow stops the client; Err reports the terminal cause
// even when no final event fits in the buffer. Methods are safe for concurrent use,
// but callers must not mutate protobuf inputs while an operation is using them.
package router

import (
	"context"
	"errors"
	"math/big"

	stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

// APIKeyHeader carries the raw API key in WebSocket handshakes.
const APIKeyHeader = stream.APIKeyHeader

// DefaultRouterURL is used when RouterConfig.Connection.URL is empty.
const DefaultRouterURL = stream.DefaultRouterURL

const (
	Enabled  = stream.Enabled // zero value enables automatic reconnect
	Disabled = stream.Disabled
)

const (
	Connecting   = stream.Connecting
	Connected    = stream.Connected
	Reconnecting = stream.Reconnecting
	Stopped      = stream.Stopped
)

const (
	ConnectionChanged   = stream.ConnectionChanged
	SubscriptionChanged = stream.SubscriptionChanged
	ServerError         = stream.ServerError
)

const Replace = stream.Replace

// Sentinel errors can be inspected with errors.Is.
var (
	ErrClosed               = stream.ErrClosed
	ErrDisconnected         = stream.ErrDisconnected
	ErrAuthentication       = stream.ErrAuthentication
	ErrProtocol             = stream.ErrProtocol
	ErrEventBufferFull      = stream.ErrEventBufferFull
	ErrFrameBufferFull      = stream.ErrFrameBufferFull
	ErrMessageTooLarge      = stream.ErrMessageTooLarge
	ErrListingNotConfigured = stream.ErrListingNotConfigured
)

var (
	// ErrExpiredFrame means Quote's frame is past the configured decay start.
	ErrExpiredFrame = errors.New("router: frame is past the decay start")
	// ErrFutureFrame means the frame timestamp is later than the evaluation time.
	ErrFutureFrame = errors.New("router: frame timestamp is in the future")
	// ErrZeroOutput means the budget and book cannot produce a nonzero net fill.
	ErrZeroOutput = errors.New("router: quote produces no output")
)

// Dial connects and authenticates before returning. Canceling lifetimeCtx
// stops the router; use Close to stop it explicitly and wait for cleanup.
// Frames become available after Subscribe succeeds.
func Dial(lifetimeCtx context.Context, cfg RouterConfig) (*Router, error) {
	impl, err := stream.DialRouter(lifetimeCtx, cfg)
	if err != nil {
		return nil, err
	}
	return &Router{impl: impl}, nil
}

// Subscribe replaces the complete filter set and waits for the
// server's acknowledgement. Individual filters may be rejected without an error;
// inspect each returned result. Local validation failures leave delivery intact.
// Use a single empty PairFilter to select all visible pairs and makers.
// Queued frames are discarded once writing starts; delivery resumes after ack.
// Successful calls return the same single-consumer channel, with no replay.
// Drain it continuously: overflow stops the router with ErrFrameBufferFull.
// The channel stays open across subscriptions and reconnects and closes on teardown.
// Frames already received remain caller-owned; use their generation and revision
// to invalidate retained frames when the router's status changes.
// Callers must not mutate filters during this call; the router retains copies.
func (r *Router) Subscribe(ctx context.Context, filters []*PairFilter) (<-chan FrameUpdate, SubscriptionResult, error) {
	return r.impl.Subscribe(ctx, filters)
}

// Events returns the router's event stream. Drain it continuously: a full buffer
// stops the client with ErrEventBufferFull. The channel closes after shutdown.
func (r *Router) Events() <-chan RouterEvent {
	return r.impl.Events()
}

// Close stops streaming and waits for its goroutines to exit. It is safe to call
// repeatedly and returns the terminal error, if any. Listing remains usable.
func (r Router) Close() error {
	return r.impl.Close()
}

// Done closes after streaming has stopped and its goroutines have exited.
func (r Router) Done() <-chan struct{} {
	return r.impl.Done()
}

// Err reports the terminal cause, or nil before shutdown or after a clean Close.
func (r Router) Err() error {
	return r.impl.Err()
}

// Status returns a snapshot of the connection and subscription state.
func (r Router) Status() ClientStatus {
	return r.impl.Status()
}

// Stats returns counters accumulated across all connection generations.
func (r Router) Stats() ClientStats {
	return r.impl.Stats()
}

// Reconnect requests a fresh connection and waits for authentication and subscription
// restoration. Canceling the wait does not cancel the client's lifetime.
func (r Router) Reconnect(ctx context.Context) error {
	return r.impl.Reconnect(ctx)
}

// Listing returns the HTTP client, or nil when ListingConfig is zero.
// It uses fixed credentials and each query's context, so it remains usable after
// Close or lifetime cancellation. Create a new Router to change
// credentials. Shared HTTP transports are never closed by the streaming client.
func (r Router) Listing() *listing.Client {
	return r.impl.Listing()
}

// ListChains returns the supported chain name to chain ID mapping using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (r Router) ListChains(ctx context.Context) (map[string]uint64, error) {
	return r.impl.ListChains(ctx)
}

// ListPairs lists public pairs for one chain using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (r Router) ListPairs(ctx context.Context, chainID uint64) ([]listing.Pair, error) {
	return r.impl.ListPairs(ctx, chainID)
}

// ListPair refreshes a public pair by chain ID and on-chain pair ID.
func (r Router) ListPair(ctx context.Context, chainID uint64, pairID uint32) (listing.Pair, error) {
	return r.impl.ListPair(ctx, chainID, pairID)
}

// Quote delegates to the package function and uses no router connection state.
func (r *Router) Quote(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn *big.Int, cfg QuoteConfig) (*big.Int, error) {
	return Quote(value, tokenIn, tokenOut, amountIn, cfg)
}

// BuildSwap delegates to the package function and uses no router connection state.
// It returns protocol.swap parameters and calldata for the supplied signed frame.
func (r *Router) BuildSwap(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn, minAmountOut *big.Int, recipient common.Address) (protocol.SwapParams, []byte, error) {
	return BuildSwap(value, tokenIn, tokenOut, amountIn, minAmountOut, recipient)
}

// BuildSwapExactIn delegates to the package function and uses no router connection
// state. It returns protocol.swapExactIn parameters and calldata for the supplied
// signed frame. Any input left after matching and input fees goes to the protocol
// fee recipient.
func (r *Router) BuildSwapExactIn(value *pb.MarketFrame, tokenIn, tokenOut common.Address, amountIn, minAmountOut *big.Int, recipient common.Address) (protocol.SwapParams, []byte, error) {
	return BuildSwapExactIn(value, tokenIn, tokenOut, amountIn, minAmountOut, recipient)
}
