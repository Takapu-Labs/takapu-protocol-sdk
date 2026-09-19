package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

var proxy = common.HexToAddress("0x1234567890123456789012345678901234567890")

var maker = common.HexToAddress("0x2234567890123456789012345678901234567890")

var recipient = common.HexToAddress("0x3234567890123456789012345678901234567890")

var token = common.HexToAddress("0x4234567890123456789012345678901234567890")

type rpcFailure struct {
	message string
	data    any
}

func (e rpcFailure) Error() string {
	return e.message
}

func (e rpcFailure) ErrorCode() int {
	return -32000
}

func (e rpcFailure) ErrorData() any {
	return e.data
}

type rpcTestAPI struct {
	chain       hexutil.Big
	blockNumber func() (hexutil.Uint64, error)
	call        func(map[string]json.RawMessage, string) (hexutil.Bytes, error)
	sendErr     error
	receipt     *types.Receipt
	receiptErr  error
	mu          sync.Mutex
	sent        []*types.Transaction
}

func (s *rpcTestAPI) ChainId() *hexutil.Big {
	return &s.chain
}

func (s *rpcTestAPI) BlockNumber() (hexutil.Uint64, error) {
	if s.blockNumber == nil {
		return 0, errors.New("unexpected eth_blockNumber")
	}
	return s.blockNumber()
}

func (s *rpcTestAPI) Call(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
	if s.call == nil {
		return nil, errors.New("unexpected eth_call")
	}
	return s.call(args, block)
}

func (s *rpcTestAPI) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	s.mu.Lock()
	s.sent = append(s.sent, tx)
	s.mu.Unlock()
	return tx.Hash(), s.sendErr
}

func (s *rpcTestAPI) GetTransactionReceipt(common.Hash) (*types.Receipt, error) {
	return s.receipt, s.receiptErr
}

func (s *rpcTestAPI) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func newRPC(t *testing.T, api *rpcTestAPI) *ethclient.Client {
	t.Helper()
	server := rpc.NewServer()
	if err := server.RegisterName("eth", api); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	client, err := rpc.DialHTTP(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return ethclient.NewClient(client)
}

func newClient(t *testing.T, api *rpcTestAPI) *Client {
	t.Helper()
	api.chain = hexutil.Big(*big.NewInt(56))
	c, err := NewClient(context.Background(), newRPC(t, api), Config{56, proxy})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func swapParams() SwapParams {
	return SwapParams{PairID: 7, IsBuy: true, AmountIn: big.NewInt(1000), MinAmountOut: big.NewInt(50), Maker: maker, Recipient: recipient}
}

func decodeCall(t *testing.T, args map[string]json.RawMessage) (common.Address, common.Address, []byte) {
	t.Helper()
	var from, to common.Address
	var data hexutil.Bytes
	if raw := args["from"]; raw != nil {
		if err := json.Unmarshal(raw, &from); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(args["to"], &to); err != nil {
		t.Fatal(err)
	}
	raw := args["input"]
	if raw == nil {
		raw = args["data"]
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return from, to, data
}

func TestClientChainBinding(t *testing.T) {
	api := &rpcTestAPI{chain: hexutil.Big(*big.NewInt(1))}
	rpcClient := newRPC(t, api)
	for _, cfg := range []Config{{}, {56, proxy}, {1, common.Address{}}} {
		if _, err := NewClient(context.Background(), rpcClient, cfg); err == nil {
			t.Fatalf("accepted %+v", cfg)
		}
	}
	c, err := NewClient(context.Background(), rpcClient, Config{1, proxy})
	if err != nil || c.ChainID() != 1 || c.Proxy() != proxy {
		t.Fatalf("%v %v", c, err)
	}
	if _, err := NewClient(context.Background(), nil, Config{1, proxy}); err == nil {
		t.Fatal("nil RPC")
	}
	if _, err := NewClient(nil, rpcClient, Config{1, proxy}); err == nil { //nolint:staticcheck // Verify that a nil context is rejected.
		t.Fatal("nil context")
	}
}

func TestRPCReadsSimulationAndReverts(t *testing.T) {
	api := &rpcTestAPI{}
	c := newClient(t, api)
	ctx := context.Background()
	api.call = func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
		_, to, data := decodeCall(t, args)
		if to != proxy || block != "0x7b" || !bytes.Equal(data, contractABI.Methods["globalDecayConfig"].ID) {
			t.Errorf("wrong decay call %s %s %x", to, block, data)
		}
		return contractABI.Methods["globalDecayConfig"].Outputs.Pack(uint32(2), uint32(5), uint32(10), uint32(500000))
	}
	cfg, err := c.GlobalDecayConfig(ctx, big.NewInt(123))
	if err != nil || cfg.MaxAge != 10 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := c.GlobalDecayConfig(ctx, big.NewInt(-1)); err == nil {
		t.Fatal("accepted negative block")
	}
	api.call = func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
		return contractABI.Methods["globalDecayConfig"].Outputs.Pack(uint32(0), uint32(0), uint32(0), uint32(0))
	}
	if _, err := c.GlobalDecayConfig(ctx, nil); !errors.Is(err, ErrInvalidDecayConfig) {
		t.Fatal(err)
	}
	for _, method := range []SwapMethod{MethodSwap, MethodSwapExactIn} {
		api.call = func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
			from, to, data := decodeCall(t, args)
			want, _ := EncodeSwapCall(method, swapParams())
			if from != recipient || to != proxy || block != "latest" || !bytes.Equal(want, data) {
				t.Error("wrong simulation call")
			}
			return contractABI.Methods[string(method)].Outputs.Pack(big.NewInt(99), big.NewInt(88))
		}
		result, err := c.SimulateSwap(ctx, recipient, method, swapParams(), nil)
		if err != nil || result.ActualAmountIn.Int64() != 99 || result.AmountOut.Int64() != 88 {
			t.Fatalf("%+v %v", result, err)
		}
	}
	if _, err := c.SimulateSwap(ctx, recipient, MethodSwapWithCallback, swapParams(), nil); err == nil {
		t.Fatal("callback simulated as EOA")
	}
	definition := contractABI.Errors["SlippageExceeded"]
	payload, _ := definition.Inputs.Pack(big.NewInt(42), big.NewInt(50))
	api.call = func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
		return nil, rpcFailure{"execution reverted", hexutil.Encode(append(definition.ID[:4], payload...))}
	}
	_, err = c.SimulateSwap(ctx, recipient, MethodSwap, swapParams(), nil)
	var revert *RevertError
	var rpcErr rpc.Error
	if !errors.As(err, &revert) || revert.Name != "SlippageExceeded" || revert.Arguments[0].(*big.Int).Int64() != 42 || !errors.As(err, &rpcErr) {
		t.Fatalf("%#v", err)
	}
}

func TestAllowanceAndApproveSpender(t *testing.T) {
	api := &rpcTestAPI{}
	c := newClient(t, api)
	api.call = func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
		_, to, data := decodeCall(t, args)
		if to != token || block != "latest" {
			t.Error("wrong token call")
		}
		decoded, err := tokenABI.Methods["allowance"].Inputs.Unpack(data[4:])
		if err != nil {
			t.Fatal(err)
		}
		if decoded[0].(common.Address) != recipient || decoded[1].(common.Address) != proxy {
			t.Error("wrong owner/spender")
		}
		return tokenABI.Methods["allowance"].Outputs.Pack(big.NewInt(321))
	}
	got, err := c.Allowance(context.Background(), token, recipient)
	if err != nil || got.Int64() != 321 {
		t.Fatalf("%v %v", got, err)
	}
	opts := testOpts(t)
	opts.NoSend = true
	for _, amount := range []*big.Int{big.NewInt(0), big.NewInt(123)} {
		tx, err := c.ApproveToken(opts, token, amount)
		if err != nil {
			t.Fatal(err)
		}
		if *tx.To() != token {
			t.Fatal("wrong approval token")
		}
		decoded, err := tokenABI.Methods["approve"].Inputs.Unpack(tx.Data()[4:])
		if err != nil {
			t.Fatal(err)
		}
		if decoded[0].(common.Address) != proxy || decoded[1].(*big.Int).Cmp(amount) != 0 {
			t.Fatal("wrong approval spender/amount")
		}
	}
	if api.sendCount() != 0 {
		t.Fatal("NoSend sent transaction")
	}
}

func testOpts(t *testing.T) *bind.TransactOpts {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	opts := bind.NewKeyedTransactor(key, big.NewInt(56))
	opts.Nonce = big.NewInt(3)
	opts.GasPrice = big.NewInt(5)
	opts.GasLimit = 500000
	opts.Context = context.Background()
	return opts
}

func TestTransactionPreservesOptionsAndBroadcastError(t *testing.T) {
	api := &rpcTestAPI{sendErr: errors.New("upstream connection lost")}
	c := newClient(t, api)
	opts := testOpts(t)
	opts.Value = big.NewInt(0)
	before := *opts
	tx, err := c.Swap(opts, swapParams())
	if tx == nil || err == nil || api.sendCount() != 1 {
		t.Fatalf("tx %v, error %v, sends %d", tx, err, api.sendCount())
	}
	if *tx.To() != proxy || tx.Nonce() != 3 || tx.ChainId().Uint64() != 56 {
		t.Fatal("wrong signed transaction")
	}
	if opts.NoSend != before.NoSend || opts.Nonce != before.Nonce || opts.Value != before.Value || opts.GasPrice != before.GasPrice || opts.GasPrice.Int64() != 5 || opts.Context != before.Context {
		t.Fatal("mutated transaction options")
	}
	api.sendErr = nil
	tx, err = c.SwapExactIn(opts, swapParams())
	if err != nil || tx == nil || !bytes.Equal(tx.Data()[:4], contractABI.Methods["swapExactIn"].ID) {
		t.Fatalf("%v %v", tx, err)
	}
	opts.NoSend = true
	opts.GasPrice = nil
	opts.GasFeeCap = big.NewInt(100)
	opts.GasTipCap = big.NewInt(1)
	tx, err = c.Swap(opts, swapParams())
	if err != nil || tx.Type() != types.DynamicFeeTxType || api.sendCount() != 2 {
		t.Fatalf("dynamic tx %v %v", tx, err)
	}
}

func TestTransactionGuards(t *testing.T) {
	api := &rpcTestAPI{}
	c := newClient(t, api)
	for _, test := range []struct {
		name string
		edit func(*bind.TransactOpts)
	}{
		{"value", func(o *bind.TransactOpts) { o.Value = big.NewInt(1) }},
		{"negative value", func(o *bind.TransactOpts) { o.Value = big.NewInt(-1) }},
		{"bad nonce", func(o *bind.TransactOpts) { o.Nonce = new(big.Int).Lsh(big.NewInt(1), 65) }},
		{"missing signer", func(o *bind.TransactOpts) { o.Signer = nil }},
		{"wrong chain", func(o *bind.TransactOpts) {
			key, _ := crypto.HexToECDSA(strings.Repeat("11", 32))
			o.Signer = bind.NewKeyedTransactor(key, big.NewInt(1)).Signer
		}},
		{"wrong sender", func(o *bind.TransactOpts) {
			key, _ := crypto.HexToECDSA(strings.Repeat("22", 32))
			o.Signer = func(_ common.Address, tx *types.Transaction) (*types.Transaction, error) {
				return types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(56)), key)
			}
		}},
		{"nil signed transaction", func(o *bind.TransactOpts) {
			o.Signer = func(common.Address, *types.Transaction) (*types.Transaction, error) { return nil, nil }
		}},
		{"changed payload", func(o *bind.TransactOpts) {
			key, _ := crypto.HexToECDSA(strings.Repeat("11", 32))
			o.Signer = func(_ common.Address, tx *types.Transaction) (*types.Transaction, error) {
				changed := types.NewTx(&types.LegacyTx{Nonce: tx.Nonce() + 1, To: tx.To(), Gas: tx.Gas(), GasPrice: tx.GasPrice(), Data: tx.Data()})
				return types.SignTx(changed, types.LatestSignerForChainID(big.NewInt(56)), key)
			}
		}},
		{"changed unsigned transaction in place", func(o *bind.TransactOpts) {
			key, _ := crypto.HexToECDSA(strings.Repeat("11", 32))
			o.Signer = func(_ common.Address, tx *types.Transaction) (*types.Transaction, error) {
				changed := types.NewTx(&types.LegacyTx{Nonce: tx.Nonce() + 1, To: tx.To(), Gas: tx.Gas(), GasPrice: tx.GasPrice(), Data: tx.Data()})
				signed, err := types.SignTx(changed, types.LatestSignerForChainID(big.NewInt(56)), key)
				if err != nil {
					return nil, err
				}
				raw, err := signed.MarshalBinary()
				if err != nil {
					return nil, err
				}
				if err := tx.UnmarshalBinary(raw); err != nil {
					return nil, err
				}
				return tx, nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOpts(t)
			test.edit(opts)
			tx, err := c.Swap(opts, swapParams())
			if err == nil || tx != nil {
				t.Fatalf("%v %v", tx, err)
			}
		})
	}
	if api.sendCount() != 0 {
		t.Fatal("guard failure broadcast")
	}
}

func TestWaitReceipt(t *testing.T) {
	api := &rpcTestAPI{}
	c := newClient(t, api)
	hash := common.HexToHash("0x01")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.WaitReceipt(ctx, hash); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	api.receipt = &types.Receipt{Type: types.LegacyTxType, Status: types.ReceiptStatusFailed, CumulativeGasUsed: 42, Logs: []*types.Log{}, TxHash: hash, GasUsed: 42, BlockHash: common.HexToHash("0x02"), BlockNumber: big.NewInt(10)}
	got, err := c.WaitReceipt(context.Background(), hash)
	if err != nil || got.Status != types.ReceiptStatusFailed {
		t.Fatalf("%v %v", got, err)
	}
	api.receiptErr = errors.New("RPC down")
	if _, err := c.WaitReceipt(context.Background(), hash); err == nil {
		t.Fatal("lost RPC error")
	}
}

func TestEncodeSwapSelectorsAndValidation(t *testing.T) {
	for _, method := range []SwapMethod{MethodSwap, MethodSwapExactIn, MethodSwapWithCallback} {
		got, err := EncodeSwapCall(method, swapParams())
		if err != nil {
			t.Fatal(err)
		}
		signature := string(method) + "(uint32,bool,uint256,uint256,address,address,((bytes32,bytes32,bytes32),address,bytes)[])"
		if !bytes.Equal(got[:4], crypto.Keccak256([]byte(signature))[:4]) {
			t.Fatalf("wrong selector %x", got[:4])
		}
		values, err := contractABI.Methods[string(method)].Inputs.Unpack(got[4:])
		if err != nil {
			t.Fatal(err)
		}
		if values[0].(uint32) != 7 || values[2].(*big.Int).Int64() != 1000 || values[3].(*big.Int).Int64() != 50 {
			t.Fatal("wrong arguments")
		}
	}
	for _, edit := range []func(*SwapParams){func(p *SwapParams) { p.PairID = 0 }, func(p *SwapParams) { p.AmountIn = big.NewInt(0) }, func(p *SwapParams) { p.AmountIn = big.NewInt(-1) }, func(p *SwapParams) { p.AmountIn = new(big.Int).Lsh(big.NewInt(1), 256) }, func(p *SwapParams) { p.MinAmountOut = nil }, func(p *SwapParams) { p.Recipient = common.Address{} }, func(p *SwapParams) { p.Maker = common.Address{} }, func(p *SwapParams) { p.PriceUpdates = []frame.SignedFrame{{}} }} {
		params := swapParams()
		edit(&params)
		if _, err := EncodeSwapCall(MethodSwap, params); err == nil {
			t.Fatal("accepted malformed swap")
		}
	}
	if _, err := EncodeSwapCall("unknown", swapParams()); err == nil {
		t.Fatal("accepted unknown method")
	}
	a, _ := ABI()
	delete(a.Methods, "swap")
	if _, err := EncodeSwapCall(MethodSwap, swapParams()); err != nil {
		t.Fatal("external ABI changed internal state")
	}
}

func TestParseRevertVariants(t *testing.T) {
	plain := errors.New("transport")
	if ParseRevert(plain) != plain || ParseRevert(nil) != nil {
		t.Fatal("changed non-revert errors")
	}
	str, _ := abi.NewType("string", "", nil)
	body, _ := (abi.Arguments{{Type: str}}).Pack("bad value")
	for _, test := range []struct {
		data any
		name string
	}{{hexutil.Encode(append([]byte{0x08, 0xc3, 0x79, 0xa0}, body...)), "Error"}, {map[string]any{"data": "0xdeadbeef"}, "Unknown"}, {"0x4e487b710000000000000000000000000000000000000000000000000000000000000011", "Panic"}} {
		cause := rpcFailure{"revert", test.data}
		err := ParseRevert(cause)
		var decoded *RevertError
		if !errors.As(err, &decoded) || decoded.Name != test.name || !reflect.DeepEqual(decoded.Cause, cause) {
			t.Fatalf("%+v", err)
		}
	}
}
