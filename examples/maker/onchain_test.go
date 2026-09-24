package main

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type submissionStub struct {
	tx  *types.Transaction
	err error
}

func (s submissionStub) SubmitFrameUpdate(*protocol.Client, *bind.TransactOpts, maker.PublishParams) (*types.Transaction, error) {
	return s.tx, s.err
}

type receiptStub struct {
	receipt *types.Receipt
	err     error
	calls   int
	hash    common.Hash
}

func (s *receiptStub) WaitReceipt(_ context.Context, hash common.Hash) (*types.Receipt, error) {
	s.calls++
	s.hash = hash
	return s.receipt, s.err
}

func TestSubmitAndWait(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 42})
	for _, tc := range []struct {
		name       string
		tx         *types.Transaction
		submitErr  error
		noSend     bool
		receipt    *types.Receipt
		receiptErr error
		wantErr    string
		wantWait   bool
	}{
		{name: "included", tx: tx, receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1)}, wantWait: true},
		{name: "broadcast outcome unknown", tx: tx, submitErr: errors.New("broadcast interrupted"), wantErr: "broadcast interrupted"},
		{name: "signing failed", submitErr: errors.New("signer unavailable"), wantErr: "signer unavailable"},
		{name: "missing transaction", wantErr: "no transaction"},
		{name: "reverted", tx: tx, receipt: &types.Receipt{Status: types.ReceiptStatusFailed}, wantErr: "failed on chain", wantWait: true},
		{name: "receipt unavailable", tx: tx, receiptErr: context.DeadlineExceeded, wantErr: "deadline exceeded", wantWait: true},
		{name: "missing receipt", tx: tx, wantErr: "no transaction receipt", wantWait: true},
		{name: "signed without broadcasting", tx: tx, noSend: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "stderr")
			log, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			stderr := os.Stderr
			os.Stderr = log
			t.Cleanup(func() { os.Stderr = stderr; log.Close() })
			waiter := &receiptStub{receipt: tc.receipt, err: tc.receiptErr}
			got, err := submitAndWait(context.Background(), submissionStub{tx: tc.tx, err: tc.submitErr}, nil, waiter, &bind.TransactOpts{NoSend: tc.noSend}, maker.PublishParams{}, &config{})
			if got != tc.tx {
				t.Fatal("signed transaction was not preserved")
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v; want %q", err, tc.wantErr)
			}
			if tc.wantWait && (waiter.calls != 1 || waiter.hash != tx.Hash()) || !tc.wantWait && waiter.calls != 0 {
				t.Fatalf("receipt lookup calls=%d hash=%s", waiter.calls, waiter.hash)
			}
			output, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.tx != nil && !strings.Contains(string(output), tc.tx.Hash().Hex()) {
				t.Fatal("transaction hash was not logged")
			}
			if tc.noSend && !strings.Contains(string(output), "SIGNED ONLY") {
				t.Fatal("NoSend output does not describe signed-only outcome")
			}
		})
	}
}

func TestTransactionOptions(t *testing.T) {
	frameKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	txKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config{ChainID: 56}
	for _, separateSender := range []bool{false, true} {
		want := crypto.PubkeyToAddress(frameKey.PublicKey)
		if separateSender {
			cfg.TxPrivateKey = common.Bytes2Hex(crypto.FromECDSA(txKey))
			want = crypto.PubkeyToAddress(txKey.PublicKey)
		}
		opts, err := transactionOptions(ctx, cfg, localSigner{key: frameKey}, true)
		if err != nil {
			t.Fatal(err)
		}
		if opts.From != want || opts.Context != ctx || !opts.NoSend {
			t.Fatalf("unexpected transaction sender, context or NoSend: %+v", opts)
		}
	}
	cfg.TxPrivateKey = "invalid-test-key"
	if _, err := transactionOptions(ctx, cfg, localSigner{key: frameKey}, false); err == nil || strings.Contains(err.Error(), cfg.TxPrivateKey) {
		t.Fatalf("invalid transaction key must fail without exposing it: %v", err)
	}
}

func TestOnChainConfig(t *testing.T) {
	address := "0x1111111111111111111111111111111111111111"
	cfg := config{ChainID: 56, RPCURL: "https://example.invalid", Protocol: address, Maker: address, Signer: address}
	cfg.Pair.ID, cfg.Pair.BaseToken, cfg.Pair.QuoteToken = 1, address, address
	cfg.Quote.Bids = []maker.PriceLevel{{Price: "1", Amount: "1"}}
	cfg.Quote.Asks = []maker.PriceLevel{{Price: "2", Amount: "1"}}
	if err := cfg.validate(true); err != nil {
		t.Fatalf("on-chain mode must not require API credentials: %v", err)
	}
	if err := cfg.validate(false); err == nil {
		t.Fatal("WebSocket mode must require API credentials")
	}
	cfg.APIKey = "SYNTHETIC_API_KEY"
	if err := cfg.validate(false); err != nil {
		t.Fatalf("WebSocket mode must accept an API key without other API credentials: %v", err)
	}
	cfg.TxPrivateKey = "0xSYNTHETIC_TRANSACTION_PRIVATE_KEY"
	for _, secret := range []string{cfg.APIKey, cfg.TxPrivateKey, strings.TrimPrefix(cfg.TxPrivateKey, "0x")} {
		if got := cfg.redact(errors.New("failed: " + secret)); got != "failed: [redacted]" {
			t.Fatalf("credential not redacted: %s", got)
		}
	}
}
