package protocol

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// PairConfig contains the pair's on-chain tokens, native-unit scales and fee.
// FeeRate is measured in parts per million. Inactive pairs are returned as data;
// callers must check Active before constructing a swap.
type PairConfig struct {
	BaseToken     common.Address
	QuoteToken    common.Address
	PriceTickSize *big.Int
	LotSize       *big.Int
	Active        bool
	FeeToken      common.Address
	FeeRate       uint32
}

// PairConfig reads a registered pair at one explicit block (nil means latest).
// Its fee fields are the stored pair settings, regardless of the global fee
// switch. Use EffectivePairConfig for quotes. It rejects missing or invalid
// configuration and does not cache the result.
func (c *Client) PairConfig(ctx context.Context, pairID uint32, blockNumber *big.Int) (PairConfig, error) {
	if err := validateBlock(ctx, blockNumber); err != nil {
		return PairConfig{}, err
	}
	if pairID == 0 {
		return PairConfig{}, errors.New("protocol: pair ID must be positive")
	}
	data, err := contractABI.Pack("pairConfigs", pairID)
	if err != nil {
		return PairConfig{}, err
	}
	raw, err := c.rpc.CallContract(ctx, ethereum.CallMsg{To: &c.cfg.Proxy, Data: data}, blockNumber)
	if err != nil {
		return PairConfig{}, ParseRevert(err)
	}
	values, err := contractABI.Unpack("pairConfigs", raw)
	if err != nil {
		return PairConfig{}, fmt.Errorf("protocol: decode pair configuration: %w", err)
	}
	if len(values) != 7 {
		return PairConfig{}, errors.New("protocol: malformed pair configuration")
	}
	cfg := PairConfig{
		BaseToken: values[0].(common.Address), QuoteToken: values[1].(common.Address),
		PriceTickSize: values[2].(*big.Int), LotSize: values[3].(*big.Int),
		Active: values[4].(bool), FeeToken: values[5].(common.Address),
	}
	if cfg.BaseToken == (common.Address{}) || cfg.QuoteToken == (common.Address{}) || cfg.BaseToken == cfg.QuoteToken {
		return PairConfig{}, errors.New("protocol: pair is unregistered or has invalid tokens")
	}
	if cfg.PriceTickSize.Sign() <= 0 || cfg.PriceTickSize.BitLen() > 128 || cfg.LotSize.Sign() <= 0 || cfg.LotSize.BitLen() > 128 {
		return PairConfig{}, errors.New("protocol: pair scales must be positive uint128 values")
	}
	// The ABI decoder represents uint24 as *big.Int. Check the full value
	// before narrowing it so malformed responses cannot silently truncate.
	feeRate := values[6].(*big.Int)
	if !feeRate.IsUint64() || feeRate.Uint64() >= uint64(PPMDenominator) {
		return PairConfig{}, errors.New("protocol: pair fee rate must be below 1000000 PPM")
	}
	cfg.FeeRate = uint32(feeRate.Uint64())
	if (cfg.FeeRate == 0) != (cfg.FeeToken == (common.Address{})) ||
		(cfg.FeeToken != (common.Address{}) && cfg.FeeToken != cfg.BaseToken && cfg.FeeToken != cfg.QuoteToken) {
		return PairConfig{}, errors.New("protocol: invalid pair fee token")
	}
	return cfg, nil
}

// EffectivePairConfig reads the pair settings and global fee switch at the same
// block. Nil resolves the latest block number once for both reads. FeeRate and
// FeeToken are zero when global fee collection is disabled; otherwise they are
// the pair's configured fee. All other pair fields are returned unchanged.
// No result is cached, and read failures are returned without a fallback.
func (c *Client) EffectivePairConfig(ctx context.Context, pairID uint32, blockNumber *big.Int) (PairConfig, error) {
	if err := validateBlock(ctx, blockNumber); err != nil {
		return PairConfig{}, err
	}
	if pairID == 0 {
		return PairConfig{}, errors.New("protocol: pair ID must be positive")
	}
	if blockNumber == nil {
		number, err := c.rpc.BlockNumber(ctx)
		if err != nil {
			return PairConfig{}, fmt.Errorf("protocol: read latest block number: %w", err)
		}
		blockNumber = new(big.Int).SetUint64(number)
	}
	cfg, err := c.PairConfig(ctx, pairID, blockNumber)
	if err != nil {
		return PairConfig{}, err
	}
	recipient, err := c.FeeRecipient(ctx, blockNumber)
	if err != nil {
		return PairConfig{}, err
	}
	if recipient == (common.Address{}) {
		cfg.FeeToken = common.Address{}
		cfg.FeeRate = 0
	}
	return cfg, nil
}

// FeeRecipient reads the protocol's global fee recipient at one explicit block
// (nil means latest). The zero address is valid and disables fee collection.
func (c *Client) FeeRecipient(ctx context.Context, blockNumber *big.Int) (common.Address, error) {
	if err := validateBlock(ctx, blockNumber); err != nil {
		return common.Address{}, err
	}
	data, err := contractABI.Pack("feeRecipient")
	if err != nil {
		return common.Address{}, err
	}
	raw, err := c.rpc.CallContract(ctx, ethereum.CallMsg{To: &c.cfg.Proxy, Data: data}, blockNumber)
	if err != nil {
		return common.Address{}, ParseRevert(err)
	}
	values, err := contractABI.Unpack("feeRecipient", raw)
	if err != nil {
		return common.Address{}, fmt.Errorf("protocol: decode fee recipient: %w", err)
	}
	if len(values) != 1 {
		return common.Address{}, errors.New("protocol: malformed fee recipient")
	}
	return values[0].(common.Address), nil
}

// GlobalDecayConfig reads and validates current configuration at one explicit
// block (nil means latest), with no cache or fallback configuration.
func (c *Client) GlobalDecayConfig(ctx context.Context, blockNumber *big.Int) (GlobalDecayConfig, error) {
	if err := validateBlock(ctx, blockNumber); err != nil {
		return GlobalDecayConfig{}, err
	}
	data, err := contractABI.Pack("globalDecayConfig")
	if err != nil {
		return GlobalDecayConfig{}, err
	}
	raw, err := c.rpc.CallContract(ctx, ethereum.CallMsg{To: &c.cfg.Proxy, Data: data}, blockNumber)
	if err != nil {
		return GlobalDecayConfig{}, ParseRevert(err)
	}
	values, err := contractABI.Unpack("globalDecayConfig", raw)
	if err != nil {
		return GlobalDecayConfig{}, fmt.Errorf("protocol: decode decay configuration: %w", err)
	}
	if len(values) != 4 {
		return GlobalDecayConfig{}, errors.New("protocol: malformed decay configuration")
	}
	cfg := GlobalDecayConfig{
		DecayStartOffsetSeconds: values[0].(uint32),
		DecayEndOffsetSeconds:   values[1].(uint32),
		MaxAge:                  values[2].(uint32),
		MaxDecayPpm:             values[3].(uint32),
	}
	if err := cfg.Validate(); err != nil {
		return GlobalDecayConfig{}, err
	}
	return cfg, nil
}

// Allowance reads the owner's ERC-20 allowance at the latest block, always using
// this client's protocol proxy as spender. The amount is in token native units.
func (c *Client) Allowance(ctx context.Context, token, owner common.Address) (*big.Int, error) {
	if err := validateBlock(ctx, nil); err != nil {
		return nil, err
	}
	if token == (common.Address{}) || owner == (common.Address{}) {
		return nil, errors.New("protocol: token and owner must be nonzero")
	}
	data, err := tokenABI.Pack("allowance", owner, c.cfg.Proxy)
	if err != nil {
		return nil, err
	}
	raw, err := c.rpc.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return nil, ParseRevert(err)
	}
	values, err := tokenABI.Unpack("allowance", raw)
	if err != nil {
		return nil, fmt.Errorf("protocol: decode allowance: %w", err)
	}
	if len(values) != 1 {
		return nil, errors.New("protocol: malformed allowance result")
	}
	return values[0].(*big.Int), nil
}
