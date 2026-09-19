package maker

import (
	"context"
	"errors"
	"fmt"

	stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Maker publishes signed market frames over WebSocket or directly on-chain.
// Its methods are safe for concurrent use. Construct it with Dial for streaming;
// a zero-value Maker supports only SubmitFrameUpdate, which uses the caller's
// protocol client independently of the WebSocket connection.
type Maker struct {
	impl *stream.Maker
}

// Dial connects and authenticates before returning. Canceling
// lifetimeCtx stops the maker; Close also waits for streaming cleanup.
func Dial(lifetimeCtx context.Context, cfg MakerConfig) (*Maker, error) {
	impl, err := stream.DialMaker(lifetimeCtx, cfg)
	if err != nil {
		return nil, err
	}
	return &Maker{impl: impl}, nil
}

// Publish rounds decimal quotes to protocol ticks/lots, builds and signs the
// frame, then writes it. Bid prices round down, ask prices round up, and amounts
// round down. Invalid rounded values or duplicate ticks are rejected.
// Success confirms only a network write,
// not acceptance, delivery, or execution. Frames are never replayed on reconnect.
// A PublishError distinguishes failures before sending (including conversion and
// signing) from uncertain delivery. ctx covers signing and the network write.
// Level amounts are raw depth as described by PriceLevel.Amount; Publish does
// not adjust them for on-chain fills.
// The caller owns inventory checks, timestamps and version progression, and must
// not mutate params while this call is running. Shared signers must be safe for
// concurrent use. Publish never queries chain state or advances versions.
func (p *Maker) Publish(ctx context.Context, params PublishParams) error {
	fail := func(err error) error { return &PublishError{Outcome: NotSent, Err: err} }
	if ctx == nil {
		return fail(errors.New("maker: context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	spec, err := quoteSpec(params)
	if err != nil {
		return fail(err)
	}
	value, err := frame.BuildAndSign(ctx, spec, params.Signer)
	if err != nil {
		return fail(err)
	}
	return p.impl.Publish(ctx, value)
}

// Next returns the next event, draining buffered events before EOF or a terminal
// error. Use one event consumer and call Next continuously: buffer overflow stops
// the maker with ErrEventBufferFull. ctx controls only this wait.
func (p *Maker) Next(ctx context.Context) (MakerEvent, error) {
	return p.impl.Next(ctx)
}

// Close stops streaming and waits for its goroutines to exit. It is safe to call
// repeatedly and returns the terminal error, if any. Listing and
// SubmitFrameUpdate remain usable.
func (p Maker) Close() error {
	return p.impl.Close()
}

// Done closes after streaming has stopped and its goroutines have exited.
func (p Maker) Done() <-chan struct{} {
	return p.impl.Done()
}

// Err reports the terminal cause, or nil before shutdown or after a clean Close.
func (p Maker) Err() error {
	return p.impl.Err()
}

// Status returns a snapshot of the connection state.
func (p Maker) Status() ClientStatus {
	return p.impl.Status()
}

// Stats returns counters accumulated across all connection generations.
func (p Maker) Stats() ClientStats {
	return p.impl.Stats()
}

// Reconnect requests a fresh connection and waits for authentication. Canceling the wait does not cancel the client's lifetime.
func (p Maker) Reconnect(ctx context.Context) error {
	return p.impl.Reconnect(ctx)
}

// Listing returns the HTTP client, or nil when ListingConfig is zero.
// It uses fixed credentials and each query's context, so it remains usable after
// Close or lifetime cancellation. Create a new Maker to change
// credentials. Shared HTTP transports are never closed by the streaming client.
func (p Maker) Listing() *listing.Client {
	return p.impl.Listing()
}

// ListChains returns the supported chain name to chain ID mapping using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (p Maker) ListChains(ctx context.Context) (map[string]uint64, error) {
	return p.impl.ListChains(ctx)
}

// ListPairs lists public pairs for one chain using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (p Maker) ListPairs(ctx context.Context, chainID uint64) ([]listing.Pair, error) {
	return p.impl.ListPairs(ctx, chainID)
}

// ListPair refreshes a public pair by chain ID and on-chain pair ID.
func (p Maker) ListPair(ctx context.Context, chainID uint64, pairID uint32) (listing.Pair, error) {
	return p.impl.ListPair(ctx, chainID, pairID)
}

// SubmitFrameUpdate converts and signs a decimal quote, then submits it directly
// to the protocol proxy's updateFrameBySig entry point through client.
// Bid prices round down to ticks, ask prices round up, and amounts round down to
// lots. Invalid rounded values or duplicate ticks are rejected.
// It uses the same rounding and input validation as Publish, and prepares only
// the signing payload without generating readable marketstream prices/amounts.
//
// Makers normally update quotes through Publish over WebSocket. This method is
// intended for exceptional cases, such as emergency updates that need to bypass
// marketstream and submit a frame directly on-chain.
func (m *Maker) SubmitFrameUpdate(client *protocol.Client, opts *bind.TransactOpts, params PublishParams) (*types.Transaction, error) {
	if m == nil {
		return nil, errors.New("maker: maker is nil")
	}
	if client == nil || client.ChainID() == 0 || client.Proxy() == (common.Address{}) {
		return nil, errors.New("maker: initialized protocol client is required")
	}
	if opts == nil || opts.Signer == nil || opts.From == (common.Address{}) {
		return nil, errors.New("maker: transaction options require sender and signer")
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.ChainID != client.ChainID() {
		return nil, fmt.Errorf("maker: quote chain ID %d does not match protocol client %d", params.ChainID, client.ChainID())
	}
	if params.Protocol != client.Proxy() {
		return nil, fmt.Errorf("maker: quote protocol %s does not match client proxy %s", params.Protocol, client.Proxy())
	}
	spec, err := quoteSpec(params)
	if err != nil {
		return nil, err
	}
	payload, err := frame.PrepareSigningPayload(spec)
	if err != nil {
		return nil, err
	}
	value, err := frame.SignFrame(ctx, payload.Domain, payload.Maker, payload.Frame, params.Signer)
	if err != nil {
		return nil, err
	}
	return client.UpdateFrameBySig(opts, value)
}
