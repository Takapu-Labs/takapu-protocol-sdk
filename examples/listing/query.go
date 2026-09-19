package main

import (
	"context"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
)

// This example selects registered pairs that are active and not gray.
// Listing configuration alone does not establish whether a swap can execute.
func usablePair(pair listing.Pair, chainID uint64) bool {
	return pair.ChainID == chainID &&
		pair.Active && !pair.Gray && pair.PairID != 0
}

func listUsablePairs(ctx context.Context, client *listing.Client, chainID uint64) ([]listing.Pair, error) {
	pairs, err := client.ListPairs(ctx, chainID)
	if err != nil {
		return nil, err
	}
	usable := make([]listing.Pair, 0, len(pairs))
	for _, pair := range pairs {
		if usablePair(pair, chainID) {
			usable = append(usable, pair)
		}
	}
	return usable, nil
}
