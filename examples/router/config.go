package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	"github.com/ethereum/go-ethereum/common"
)

type config struct {
	APIKey  string         `json:"api_key"`
	Filters []filterConfig `json:"filters"`
	Swap    *swapConfig    `json:"swap,omitempty"`
	// TestGray is retained for existing local configurations. Prefer Filters.
	TestGray struct {
		ChainID uint64 `json:"chain_id"`
		PairID  uint32 `json:"pair_id"`
		Maker   string `json:"maker"`
	} `json:"test_gray"`
}

// Omitted chain_id and pair_id select all pairs. Pointers distinguish omission
// from an explicitly supplied zero, which is not a valid pair identifier.
type filterConfig struct {
	ChainID        *uint64 `json:"chain_id,omitempty"`
	PairID         *uint32 `json:"pair_id,omitempty"`
	Maker          string  `json:"maker,omitempty"`
	AllowPairGray  bool    `json:"allow_pair_gray,omitempty"`
	AllowMakerGray bool    `json:"allow_maker_gray,omitempty"`
}

func readConfig(reader io.Reader) (config, error) {
	var cfg config
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return cfg, errors.New("config must contain exactly one JSON object")
	}
	return cfg, nil
}

func (c config) validate(includeGray bool) error {
	if c.APIKey == "" {
		return errors.New("set a Router api_key in the Router config")
	}
	if _, err := c.subscriptionFilters(includeGray); err != nil {
		return err
	}
	if c.Swap != nil {
		return c.Swap.validate()
	}
	return nil
}

func (c config) subscriptionFilters(includeGray bool) ([]*router.PairFilter, error) {
	filters := make([]*router.PairFilter, 0, len(c.Filters)+1)
	for i, item := range c.Filters {
		filter, err := item.pairFilter()
		if err != nil {
			return nil, fmt.Errorf("filters[%d]: %w", i, err)
		}
		filters = append(filters, filter)
	}
	if len(filters) == 0 {
		filters = append(filters, &router.PairFilter{})
	}
	if includeGray {
		legacy := filterConfig{
			ChainID: &c.TestGray.ChainID,
			PairID:  &c.TestGray.PairID,
			Maker:   c.TestGray.Maker,
		}
		filter, err := legacy.pairFilter()
		if err != nil {
			return nil, fmt.Errorf("test_gray config for -include-test-gray: %w", err)
		}
		filters = append(filters, filter)
	}
	return filters, nil
}

func (f filterConfig) pairFilter() (*router.PairFilter, error) {
	filter := &router.PairFilter{
		Maker:          f.Maker,
		AllowPairGray:  f.AllowPairGray,
		AllowMakerGray: f.AllowMakerGray,
	}
	if f.ChainID != nil || f.PairID != nil {
		if f.ChainID == nil || f.PairID == nil || *f.ChainID == 0 || *f.PairID == 0 {
			return nil, errors.New("chain_id and pair_id must both be positive integers, or both be omitted to match all pairs")
		}
		filter.Pair = &router.PairKey{ChainId: *f.ChainID, PairId: *f.PairID}
	}
	if f.Maker != "" && (!common.IsHexAddress(f.Maker) || common.HexToAddress(f.Maker) == (common.Address{})) {
		return nil, errors.New("maker must be a nonzero EVM address, or omitted to match all makers")
	}
	if f.AllowPairGray && filter.Pair == nil {
		return nil, errors.New("allow_pair_gray requires explicit chain_id and pair_id values")
	}
	if f.AllowMakerGray && f.Maker == "" {
		return nil, errors.New("allow_maker_gray requires an explicit maker")
	}
	return filter, nil
}
