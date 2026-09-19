package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Config identifies the chain and deployed protocol proxy used by a Client.
// Both fields are required; the SDK does not select deployments automatically.
type Config struct {
	ChainID uint64
	Proxy   common.Address
}

// Client binds protocol operations to one chain and proxy. It is safe for
// concurrent calls; callers own the RPC connection, signers and nonce management.
// Construct a Client with NewClient; its zero value is not ready for use.
type Client struct {
	rpc *ethclient.Client
	cfg Config
}

// NewClient verifies the configured chain against the RPC endpoint. Proxy must
// be the deployed protocol proxy; the library does not choose deployments.
func NewClient(ctx context.Context, rpc *ethclient.Client, cfg Config) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("protocol: context is nil")
	}
	if rpc == nil || cfg.ChainID == 0 || cfg.Proxy == (common.Address{}) {
		return nil, errors.New("protocol: RPC, chain ID and proxy are required")
	}
	id, err := rpc.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("protocol: read chain ID: %w", err)
	}
	if id == nil || id.Cmp(new(big.Int).SetUint64(cfg.ChainID)) != 0 {
		return nil, fmt.Errorf("protocol: RPC chain ID %v does not match configured %d", id, cfg.ChainID)
	}
	return &Client{rpc: rpc, cfg: cfg}, nil
}

// ChainID returns the chain ID verified when the client was constructed.
func (c *Client) ChainID() uint64 {
	return c.cfg.ChainID
}

// Proxy returns the protocol proxy used for calls and token allowances.
func (c *Client) Proxy() common.Address {
	return c.cfg.Proxy
}
