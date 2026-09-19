package maker_test

import (
	"context"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/core/types"
)

type clientAPI interface {
	Close() error
	Done() <-chan struct{}
	Err() error
	Status() maker.ClientStatus
	Stats() maker.ClientStats
	Reconnect(context.Context) error
	Listing() *listing.Client
	ListChains(context.Context) (map[string]uint64, error)
	ListPairs(context.Context, uint64) ([]listing.Pair, error)
	ListPair(context.Context, uint64, uint32) (listing.Pair, error)
}

type makerAPI interface {
	clientAPI
	Publish(context.Context, maker.PublishParams) error
	SubmitFrameUpdate(*protocol.Client, *bind.TransactOpts, maker.PublishParams) (*types.Transaction, error)
	Next(context.Context) (maker.MakerEvent, error)
}

// The original embedded client promoted its pointer methods onto both values
// and pointers. Keep those method sets when replacing embedding with wrappers.
// These compile-time checks do not call the unusable zero-value clients.
var (
	_ clientAPI = maker.Maker{}
	_ makerAPI  = (*maker.Maker)(nil)
)
