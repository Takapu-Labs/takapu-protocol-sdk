package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// UpdateFrameBySig submits a signed frame to the protocol proxy without changing
// its bytes. Signatures must use v=27/28. Transaction options and broadcast errors
// follow the rules of Swap.
func (c *Client) UpdateFrameBySig(opts *bind.TransactOpts, value frame.SignedFrame) (*types.Transaction, error) {
	if err := validateSigned(value); err != nil {
		return nil, err
	}
	data, err := contractABI.Pack("updateFrameBySig", value.Frame, value.Maker, value.Signature)
	if err != nil {
		return nil, err
	}
	return c.transact(opts, c.cfg.Proxy, data)
}

// ApproveToken submits an approval for the explicit amount, including zero.
// Tokens that require clearing existing allowance need separate confirmed
// transactions. Check Allowance after inclusion: a token may return false without
// reverting, leaving the allowance unchanged despite a successful receipt.
// The spender is always this client's proxy. Transaction options and broadcast
// errors follow the same rules as Swap.
func (c *Client) ApproveToken(opts *bind.TransactOpts, token common.Address, amount *big.Int) (*types.Transaction, error) {
	if token == (common.Address{}) {
		return nil, errors.New("protocol: token must be nonzero")
	}
	if err := uint256("approval amount", amount, false); err != nil {
		return nil, err
	}
	data, err := tokenABI.Pack("approve", c.cfg.Proxy, amount)
	if err != nil {
		return nil, err
	}
	return c.transact(opts, token, data)
}

// transact separates construction from broadcast: bind's default broadcast path
// discards the signed transaction when SendTransaction errors, which would hide
// the hash needed to resolve an ambiguous broadcast.
func (c *Client) transact(opts *bind.TransactOpts, to common.Address, data []byte) (*types.Transaction, error) {
	local, err := copyOpts(opts)
	if err != nil {
		return nil, err
	}
	if err := local.Context.Err(); err != nil {
		return nil, err
	}
	noSend := local.NoSend
	local.NoSend = true
	originalSigner := local.Signer
	chain := new(big.Int).SetUint64(c.cfg.ChainID)
	signer := types.LatestSignerForChainID(chain)
	local.Signer = func(address common.Address, unsigned *types.Transaction) (*types.Transaction, error) {
		// Capture this before the callback, which can mutate unsigned in place.
		expectedHash := signer.Hash(unsigned)
		signed, err := originalSigner(address, unsigned)
		if err != nil {
			return nil, err
		}
		if signed == nil {
			return nil, errors.New("protocol: signer returned nil transaction")
		}
		if signed.ChainId().Cmp(chain) != 0 {
			return nil, errors.New("protocol: signed transaction chain ID mismatch")
		}
		sender, err := types.Sender(signer, signed)
		if err != nil {
			return nil, fmt.Errorf("protocol: recover transaction sender: %w", err)
		}
		if sender != local.From {
			return nil, errors.New("protocol: signed transaction sender mismatch")
		}
		// A signing callback may only add a signature, not redirect or change a call.
		if expectedHash != signer.Hash(signed) ||
			signed.To() == nil || *signed.To() != to ||
			signed.Value().Sign() != 0 || !bytes.Equal(signed.Data(), data) {
			return nil, errors.New("protocol: signer changed transaction contents")
		}
		return signed, nil
	}
	bound := bind.NewBoundContract(to, contractABI, c.rpc, c.rpc, c.rpc)
	tx, err := bound.RawTransact(local, data)
	if err != nil {
		return nil, ParseRevert(err)
	}
	if noSend {
		return tx, nil
	}
	if err := c.rpc.SendTransaction(local.Context, tx); err != nil {
		return tx, ParseRevert(err)
	}
	return tx, nil
}

// copyOpts validates transaction inputs and isolates mutable options from bind.
// Signer and Context remain caller-owned; numeric fields and access lists do not.
func copyOpts(opts *bind.TransactOpts) (*bind.TransactOpts, error) {
	if opts == nil || opts.Signer == nil || opts.From == (common.Address{}) {
		return nil, errors.New("protocol: transaction options require sender and signer")
	}
	if opts.Value != nil && opts.Value.Sign() != 0 {
		return nil, errors.New("protocol: native currency value must be zero")
	}
	if opts.Nonce != nil && (opts.Nonce.Sign() < 0 || opts.Nonce.BitLen() > 64) {
		return nil, errors.New("protocol: nonce must fit uint64")
	}
	for name, value := range map[string]*big.Int{
		"gas price":   opts.GasPrice,
		"gas fee cap": opts.GasFeeCap,
		"gas tip cap": opts.GasTipCap,
	} {
		if value != nil {
			if err := uint256(name, value, false); err != nil {
				return nil, err
			}
		}
	}
	copy := *opts
	copy.Nonce = cloneInt(opts.Nonce)
	copy.Value = cloneInt(opts.Value)
	copy.GasPrice = cloneInt(opts.GasPrice)
	copy.GasFeeCap = cloneInt(opts.GasFeeCap)
	copy.GasTipCap = cloneInt(opts.GasTipCap)
	if opts.AccessList != nil {
		copy.AccessList = make(types.AccessList, len(opts.AccessList))
	}
	for i, tuple := range opts.AccessList {
		copy.AccessList[i] = types.AccessTuple{
			Address:     tuple.Address,
			StorageKeys: append([]common.Hash(nil), tuple.StorageKeys...),
		}
	}
	if copy.Context == nil {
		copy.Context = context.Background()
	}
	return &copy, nil
}

func cloneInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}
