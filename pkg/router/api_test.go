package router_test

import (
	"context"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

type clientAPI interface {
	Close() error
	Done() <-chan struct{}
	Err() error
	Status() router.ClientStatus
	Stats() router.ClientStats
	Reconnect(context.Context) error
	Listing() *listing.Client
	ListChains(context.Context) (map[string]uint64, error)
	ListPairs(context.Context, uint64) ([]listing.Pair, error)
	ListPair(context.Context, uint64, uint32) (listing.Pair, error)
}

type routerAPI interface {
	clientAPI
	Subscribe(context.Context, []*router.PairFilter) (<-chan router.FrameUpdate, router.SubscriptionResult, error)
	Quote(*pb.MarketFrame, common.Address, common.Address, *big.Int, router.QuoteConfig) (*big.Int, error)
	BuildSwap(*pb.MarketFrame, common.Address, common.Address, *big.Int, *big.Int, common.Address) (protocol.SwapParams, []byte, error)
	BuildSwapExactIn(*pb.MarketFrame, common.Address, common.Address, *big.Int, *big.Int, common.Address) (protocol.SwapParams, []byte, error)
	Events() <-chan router.RouterEvent
}

// The original embedded client promoted its pointer methods onto both values
// and pointers. Keep those method sets when replacing embedding with wrappers.
// These compile-time checks do not call the unusable zero-value clients.
var (
	_ clientAPI = router.Router{}
	_ routerAPI = (*router.Router)(nil)
)
