package stream

import (
	"context"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
)

func newListingClient(connection ConnectionConfig, cfg ListingConfig) (*listing.Client, error) {
	if cfg.BaseURL == "" && cfg.HTTPClient == nil && cfg.Timeout == 0 {
		return nil, nil
	}
	return listing.NewClient(listing.Config{
		BaseURL:    cfg.BaseURL,
		APIKey:     connection.APIKey,
		HTTPClient: cfg.HTTPClient,
		Timeout:    cfg.Timeout,
	})
}

// Listing returns the HTTP client, or nil when ListingConfig is zero.
// It uses fixed credentials and each query's context, so it remains usable after
// Close or lifetime cancellation. Create a new Maker or Router to change
// credentials. Shared HTTP transports are never closed by the streaming client.
func (c *client) Listing() *listing.Client {
	return c.listingClient
}

// ListChains returns the supported chain name to chain ID mapping using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (c *client) ListChains(ctx context.Context) (map[string]uint64, error) {
	if c.listingClient == nil {
		return nil, ErrListingNotConfigured
	}
	return c.listingClient.ListChains(ctx)
}

// ListPairs lists public pairs for one chain using the shared API key.
// It returns ErrListingNotConfigured if ListingConfig is zero.
func (c *client) ListPairs(ctx context.Context, chainID uint64) ([]listing.Pair, error) {
	if c.listingClient == nil {
		return nil, ErrListingNotConfigured
	}
	return c.listingClient.ListPairs(ctx, chainID)
}

// ListPair refreshes a public pair by chain ID and on-chain pair ID.
func (c *client) ListPair(ctx context.Context, chainID uint64, pairID uint32) (listing.Pair, error) {
	if c.listingClient == nil {
		return listing.Pair{}, ErrListingNotConfigured
	}
	return c.listingClient.ListPair(ctx, chainID, pairID)
}
