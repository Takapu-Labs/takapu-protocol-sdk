package listing

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListChains fetches /chains and returns the supported chain name to chain ID
// mapping. Each chain ID is positive and fits the API's int64 range.
func (c *Client) ListChains(ctx context.Context) (map[string]uint64, error) {
	env, status, err := c.get(ctx, chainsPath, nil)
	if err != nil {
		return nil, err
	}
	var chains map[string]uint64
	if err := json.Unmarshal(env.Data, &chains); err != nil {
		return nil, &ResponseError{StatusCode: status, Cause: err}
	}
	for name, chainID := range chains {
		if err := validateChainID(chainID); err != nil {
			return nil, invalidResponse(status, fmt.Sprintf("data[%q] must be positive and fit int64", name))
		}
	}
	return chains, nil
}
