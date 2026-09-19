package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

func submitOnChain(ctx context.Context, rpc *ethclient.Client, contract *protocol.Client, state chainState, signer localSigner, cfg *config, configPath string, count int, interval time.Duration, noSend bool) error {
	var m maker.Maker
	opts, err := transactionOptions(ctx, *cfg, signer, noSend)
	if err != nil {
		return err
	}
	cfg.logf("transaction", "Gas payer=%s; frame signer=%s", opts.From, signer.Address())
	for i := 0; i < count; i++ {
		params, err := prepareNextQuote(ctx, rpc, contract, state, signer, cfg, configPath, i, count)
		if err != nil {
			return err
		}
		tx, err := submitAndWait(ctx, &m, contract, contract, opts, params, cfg)
		if err != nil {
			cfg.logf("transaction error", "version=(%d,%d): %s; version remains reserved, no automatic retry", params.MajorVersion, params.MinorVersion, cfg.redact(err))
			return err
		}
		if noSend {
			// No broadcast means the pending nonce will not advance at the node.
			opts.Nonce = new(big.Int).Add(new(big.Int).SetUint64(tx.Nonce()), big.NewInt(1))
		}
		if i < count-1 {
			cfg.logf("wait", "Next frame in %s", interval)
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}
	return nil
}

func transactionOptions(ctx context.Context, cfg config, signer localSigner, noSend bool) (*bind.TransactOpts, error) {
	key := signer.key
	if cfg.TxPrivateKey != "" {
		var err error
		key, err = crypto.HexToECDSA(strings.TrimPrefix(cfg.TxPrivateKey, "0x"))
		if err != nil {
			return nil, errors.New("invalid tx_private_key")
		}
	}
	opts := bind.NewKeyedTransactor(key, new(big.Int).SetUint64(cfg.ChainID))
	opts.Context, opts.NoSend = ctx, noSend
	return opts, nil
}

type frameSubmitter interface {
	SubmitFrameUpdate(*protocol.Client, *bind.TransactOpts, maker.PublishParams) (*types.Transaction, error)
}

type receiptWaiter interface {
	WaitReceipt(context.Context, common.Hash) (*types.Receipt, error)
}

func submitAndWait(ctx context.Context, m frameSubmitter, contract *protocol.Client, receipts receiptWaiter, opts *bind.TransactOpts, params maker.PublishParams, cfg *config) (*types.Transaction, error) {
	tx, err := m.SubmitFrameUpdate(contract, opts, params)
	if tx != nil {
		// A failed broadcast can still return a signed transaction. Preserve its
		// hash so the caller can investigate an uncertain broadcast outcome.
		cfg.logf("transaction", "hash=%s; nonce=%d; version=(%d,%d)", tx.Hash(), tx.Nonce(), params.MajorVersion, params.MinorVersion)
	}
	if err != nil {
		return tx, err
	}
	if tx == nil {
		return nil, errors.New("submission returned no transaction")
	}
	if opts.NoSend {
		cfg.logf("transaction", "SIGNED ONLY; no broadcast or receipt wait; version remains reserved")
		return tx, nil
	}
	cfg.logf("transaction", "Broadcast succeeded; waiting for inclusion")
	receipt, err := receipts.WaitReceipt(ctx, tx.Hash())
	if err != nil {
		return tx, fmt.Errorf("wait for transaction receipt: %w", err)
	}
	if receipt == nil {
		return tx, errors.New("RPC returned no transaction receipt")
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return tx, fmt.Errorf("transaction %s failed on chain (receipt status=%d)", tx.Hash(), receipt.Status)
	}
	cfg.logf("transaction", "CONFIRMED hash=%s; block=%v; gas used=%d", tx.Hash(), receipt.BlockNumber, receipt.GasUsed)
	return tx, nil
}
