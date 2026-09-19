package maker_test

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Only chain verification and transaction broadcast are available. These tests
// need no marketstream connection or RPC reads to construct the transaction.
type submitRPC struct {
	chain   hexutil.Big
	sendErr error
	mu      sync.Mutex
	sent    []*types.Transaction
}

func (s *submitRPC) ChainId() *hexutil.Big { return &s.chain }

func (s *submitRPC) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	s.mu.Lock()
	s.sent = append(s.sent, tx)
	s.mu.Unlock()
	return tx.Hash(), s.sendErr
}

func (s *submitRPC) broadcasts() []*types.Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*types.Transaction(nil), s.sent...)
}

func submitClient(t *testing.T, params maker.PublishParams, sendErr error) (*protocol.Client, *submitRPC) {
	t.Helper()
	api := &submitRPC{chain: hexutil.Big(*new(big.Int).SetUint64(params.ChainID)), sendErr: sendErr}
	server := rpc.NewServer()
	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	rpcClient, err := rpc.DialHTTP(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rpcClient.Close)
	client, err := protocol.NewClient(context.Background(), ethclient.NewClient(rpcClient), protocol.Config{
		ChainID: params.ChainID, Proxy: params.Protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, api
}

func submitOpts(t *testing.T, chainID uint64) *bind.TransactOpts {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	opts := bind.NewKeyedTransactor(key, new(big.Int).SetUint64(chainID))
	opts.Nonce = big.NewInt(3)
	opts.Value = big.NewInt(0)
	opts.GasPrice = big.NewInt(5)
	opts.GasLimit = 500000
	opts.Context = context.Background()
	return opts
}

func checkSubmitOptionsUnchanged(t *testing.T, opts *bind.TransactOpts) {
	t.Helper()
	before := *opts
	// Snapshot the values too, so mutating a caller-owned big.Int is detected.
	before.Nonce = new(big.Int).Set(opts.Nonce)
	before.Value = new(big.Int).Set(opts.Value)
	before.GasPrice = new(big.Int).Set(opts.GasPrice)
	signer := reflect.ValueOf(opts.Signer).Pointer()
	before.Signer = nil
	t.Cleanup(func() {
		after := *opts
		after.Signer = nil
		if !reflect.DeepEqual(before, after) || opts.Signer == nil || reflect.ValueOf(opts.Signer).Pointer() != signer {
			t.Error("SubmitFrameUpdate mutated transaction options")
		}
	})
}

func decodeSubmittedFrame(t *testing.T, tx *types.Transaction, params maker.PublishParams, from common.Address) frame.SignedFrame {
	t.Helper()
	if tx.To() == nil || *tx.To() != params.Protocol || tx.ChainId().Uint64() != params.ChainID || tx.Nonce() != 3 || tx.Value().Sign() != 0 {
		t.Fatalf("unexpected frame transaction: %v", tx)
	}
	sender, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(params.ChainID)), tx)
	if err != nil || sender != from {
		t.Fatalf("transaction sender = %s, want %s: %v", sender, from, err)
	}
	selector := crypto.Keccak256([]byte("updateFrameBySig((bytes32,bytes32,bytes32),address,bytes)"))[:4]
	if len(tx.Data()) < 4 || !bytes.Equal(tx.Data()[:4], selector) {
		t.Fatalf("wrong updateFrameBySig selector: %x", tx.Data())
	}
	contractABI, err := protocol.ABI()
	if err != nil {
		t.Fatal(err)
	}
	values, err := contractABI.Methods["updateFrameBySig"].Inputs.Unpack(tx.Data()[4:])
	if err != nil {
		t.Fatal(err)
	}
	signed := frame.SignedFrame{
		Frame:     *abi.ConvertType(values[0], new(frame.CompactFrame)).(*frame.CompactFrame),
		Maker:     values[1].(common.Address),
		Signature: values[2].([]byte),
	}
	if signed.Maker != params.Maker || len(signed.Signature) != 65 || (signed.Signature[64] != 27 && signed.Signature[64] != 28) {
		t.Fatalf("unexpected maker/signature: %+v", signed)
	}
	if err := frame.VerifySignature(frame.SigningDomain{ChainID: params.ChainID, Protocol: params.Protocol}, signed, params.Signer.Address()); err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSubmitFrameUpdateBuildsSignsAndBroadcasts(t *testing.T) {
	for _, name := range []string{"quotes", "rounded quotes", "long decimal presentation", "withdrawal"} {
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			switch name {
			case "rounded quotes":
				params.Bids = []maker.PriceLevel{{Price: "99.999", Amount: "0.0029"}, {Price: "99.989", Amount: "0.0039"}}
				params.Asks = []maker.PriceLevel{{Price: "100.001", Amount: "0.0049"}, {Price: "100.011", Amount: "0.0059"}}
			case "long decimal presentation":
				// The input fits the decimal limit, but rounding to tick 3333
				// would exceed it when converted back for marketstream display.
				params.Pair.QuoteDecimals, params.Pair.PriceTickSize = 66, big.NewInt(3)
				params.Bids = []maker.PriceLevel{{Price: "0." + strings.Repeat("0", 61) + "1", Amount: "0.002"}}
				params.Asks = nil
			case "withdrawal":
				params.Bids, params.Asks = nil, nil
			}
			checkQuoteParamsUnchanged(t, &params)
			client, api := submitClient(t, params, nil)
			m := new(maker.Maker)
			opts := submitOpts(t, params.ChainID)
			checkSubmitOptionsUnchanged(t, opts)
			if opts.From == params.Signer.Address() || opts.From == params.Maker || params.Signer.Address() == params.Maker {
				t.Fatal("fixture must use distinct transaction signer, frame signer and maker")
			}
			tx, err := m.SubmitFrameUpdate(client, opts, params)
			if err != nil || tx == nil {
				t.Fatalf("tx = %v, error = %v", tx, err)
			}
			sent := api.broadcasts()
			if len(sent) != 1 || sent[0].Hash() != tx.Hash() || params.Signer.(*quoteSigner).calls != 1 {
				t.Fatalf("expected one signed frame and one matching broadcast, got %d broadcasts", len(sent))
			}
			signed := decodeSubmittedFrame(t, sent[0], params, opts.From)
			wantHeader := frame.Header{
				PairID: params.Pair.PairID, UpdatedAt: params.UpdatedAt, MajorVersion: params.MajorVersion,
				MinorVersion: params.MinorVersion, CodecVersion: frame.CodecVersion, BaseTick: 9999,
			}
			wantBids := []frame.Level{{Offset: 0, QtyLots: 2}, {Offset: 1, QtyLots: 3}}
			wantAsks := []frame.Level{{Offset: 2, QtyLots: 4}, {Offset: 3, QtyLots: 5}}
			switch name {
			case "withdrawal":
				wantHeader.BaseTick = 1
				wantBids, wantAsks = nil, nil
			case "long decimal presentation":
				wantHeader.BaseTick = 3333
				wantBids = []frame.Level{{Offset: 0, QtyLots: 2}}
				wantAsks = nil
			}
			if got := frame.DecodeHeader(signed.Frame); got != wantHeader {
				t.Fatalf("header = %+v, want %+v", got, wantHeader)
			}
			bids, err := frame.DecodeLevels(signed.Frame.BidLevels)
			if err != nil || !reflect.DeepEqual(bids, wantBids) {
				t.Fatalf("bids = %+v, want %+v: %v", bids, wantBids, err)
			}
			asks, err := frame.DecodeLevels(signed.Frame.AskLevels)
			if err != nil || !reflect.DeepEqual(asks, wantAsks) {
				t.Fatalf("asks = %+v, want %+v: %v", asks, wantAsks, err)
			}
		})
	}
}

func TestSubmitFrameUpdateAfterMakerClose(t *testing.T) {
	m, frames := captureMaker(t)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	params := quoteParams(t)
	client, api := submitClient(t, params, nil)
	opts := submitOpts(t, params.ChainID)
	tx, err := m.SubmitFrameUpdate(client, opts, params)
	if err != nil || tx == nil {
		t.Fatalf("submission after Maker.Close: tx = %v, error = %v", tx, err)
	}
	sent := api.broadcasts()
	if len(sent) != 1 || sent[0].Hash() != tx.Hash() {
		t.Fatalf("expected one matching broadcast, got %d broadcasts", len(sent))
	}
	decodeSubmittedFrame(t, sent[0], params, opts.From)
	select {
	case <-frames:
		t.Fatal("on-chain submission sent a marketstream frame")
	default:
	}
}

func TestSubmitFrameUpdateRejectsInputsBeforeSigning(t *testing.T) {
	for _, name := range []string{
		"nil maker", "nil client", "uninitialized client", "chain mismatch", "proxy mismatch", "invalid quote",
		"nil options", "missing transaction signer", "missing transaction sender", "missing frame signer",
		"amount rounds to zero", "ask rounds past uint24", "lot overflow", "rounded duplicate bids", "rounded duplicate asks",
		"unaligned locked book", "unaligned crossed book",
	} {
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			signer := params.Signer.(*quoteSigner)
			client, api := submitClient(t, params, nil)
			m := new(maker.Maker)
			opts := submitOpts(t, params.ChainID)
			txSigner := opts.Signer
			txSignCalls := 0
			opts.Signer = func(address common.Address, tx *types.Transaction) (*types.Transaction, error) {
				txSignCalls++
				return txSigner(address, tx)
			}
			switch name {
			case "nil maker":
				m = nil
			case "nil client":
				client = nil
			case "uninitialized client":
				client = new(protocol.Client)
			case "chain mismatch":
				params.ChainID++
			case "proxy mismatch":
				params.Protocol[0]++
			case "invalid quote":
				params.Bids[0].Price = "0.009"
			case "amount rounds to zero":
				params.Bids[0].Amount = "0.000999999"
			case "ask rounds past uint24":
				params.Asks[0].Price = "167772.150001"
			case "lot overflow":
				params.Bids[0].Amount = "16777.216"
			case "rounded duplicate bids":
				params.Bids[0].Price, params.Bids[1].Price = "99.999", "99.991"
			case "rounded duplicate asks":
				params.Asks[0].Price, params.Asks[1].Price = "100.001", "100.009"
			case "unaligned locked book":
				params.Bids[0].Price, params.Asks[0].Price = "100.005", "100.005"
			case "unaligned crossed book":
				params.Bids[0].Price, params.Asks[0].Price = "100.005", "100.004"
			case "nil options":
				opts = nil
			case "missing transaction signer":
				opts.Signer = nil
			case "missing transaction sender":
				opts.From = common.Address{}
			case "missing frame signer":
				params.Signer = nil
			}
			tx, err := m.SubmitFrameUpdate(client, opts, params)
			if err == nil || tx != nil || signer.calls != 0 || txSignCalls != 0 || len(api.broadcasts()) != 0 {
				t.Fatalf("invalid input reached signing or broadcast: tx = %v, error = %v, frame signs = %d, tx signs = %d", tx, err, signer.calls, txSignCalls)
			}
		})
	}
}

func TestSubmitFrameUpdateSigningAndCancellationFailures(t *testing.T) {
	for _, name := range []string{"canceled context", "frame signing error", "canceled during frame signing", "transaction signing error"} {
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			signer := params.Signer.(*quoteSigner)
			client, api := submitClient(t, params, nil)
			m := new(maker.Maker)
			opts := submitOpts(t, params.ChainID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts.Context = ctx
			cause := errors.New("signer unavailable")
			wantFrameSigns := 1
			switch name {
			case "canceled context":
				cancel()
				cause, wantFrameSigns = context.Canceled, 0
			case "frame signing error":
				signer.err = cause
			case "canceled during frame signing":
				signer.cancel = cancel
				cause = context.Canceled
			case "transaction signing error":
				opts.Signer = func(common.Address, *types.Transaction) (*types.Transaction, error) { return nil, cause }
			}
			tx, err := m.SubmitFrameUpdate(client, opts, params)
			if !errors.Is(err, cause) || tx != nil || signer.calls != wantFrameSigns || len(api.broadcasts()) != 0 {
				t.Fatalf("signing/cancellation failure: tx = %v, error = %v, frame signs = %d", tx, err, signer.calls)
			}
		})
	}
}

func TestSubmitFrameUpdateNoSendAndAmbiguousBroadcast(t *testing.T) {
	for _, noSend := range []bool{true, false} {
		name := "ambiguous broadcast"
		if noSend {
			name = "NoSend with nil context"
		}
		t.Run(name, func(t *testing.T) {
			params := quoteParams(t)
			cause := errors.New("upstream connection lost")
			client, api := submitClient(t, params, cause)
			m := new(maker.Maker)
			opts := submitOpts(t, params.ChainID)
			opts.NoSend = noSend
			opts.Context = nil
			checkSubmitOptionsUnchanged(t, opts)
			tx, err := m.SubmitFrameUpdate(client, opts, params)
			if tx == nil {
				t.Fatalf("signed transaction was discarded: %v", err)
			}
			decodeSubmittedFrame(t, tx, params, opts.From)
			sent := api.broadcasts()
			if noSend {
				if err != nil || len(sent) != 0 {
					t.Fatalf("NoSend broadcast or failed: %v, broadcasts = %d", err, len(sent))
				}
			} else if err == nil || !strings.Contains(err.Error(), cause.Error()) || len(sent) != 1 || sent[0].Hash() != tx.Hash() {
				t.Fatalf("broadcast error lost transaction or cause: %v, broadcasts = %d", err, len(sent))
			}
		})
	}
}
