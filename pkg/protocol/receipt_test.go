package protocol

import (
	"bytes"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestSwapReceiptUsesNetSwapEventAndProxy(t *testing.T) {
	c := &Client{cfg: Config{56, proxy}}
	definition := contractABI.Events["Swap"]
	data, err := definition.Inputs.NonIndexed().Pack(big.NewInt(100), big.NewInt(93), uint32(7), maker, recipient)
	if err != nil {
		t.Fatal(err)
	}
	log := &types.Log{Address: proxy, Topics: []common.Hash{definition.ID, common.BytesToHash(recipient.Bytes()), common.BytesToHash(token.Bytes()), common.BytesToHash(maker.Bytes())}, Data: data, Index: 3}
	unrelated := *log
	unrelated.Address = maker
	makerFill := contractABI.Events["MakerFill"]
	makerData, err := makerFill.Inputs.NonIndexed().Pack(big.NewInt(99), big.NewInt(97), uint32(7), uint32(12), uint8(2))
	if err != nil {
		t.Fatal(err)
	}
	makerLog := &types.Log{Address: proxy, Topics: []common.Hash{makerFill.ID, common.BytesToHash(maker.Bytes()), log.Topics[2], log.Topics[3]}, Data: makerData}
	receipt := &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{nil, &unrelated, makerLog, log}}
	fills, err := c.ParseSwapReceipt(receipt)
	if err != nil || len(fills) != 1 {
		t.Fatalf("%+v %v", fills, err)
	}
	fill := fills[0]
	if fill.Sender != recipient || fill.TokenIn != token || fill.TokenOut != maker || fill.AmountIn.Int64() != 100 || fill.AmountOut.Int64() != 93 || fill.PairID != 7 || fill.Maker != maker || fill.Recipient != recipient || fill.Raw.Index != 3 {
		t.Fatalf("wrong event: %+v", fill)
	}
	fill.Raw.Data[0] ^= 0xff
	fill.Raw.Topics[0] = common.Hash{}
	if bytes.Equal(fill.Raw.Data, log.Data) || fill.Raw.Topics[0] == log.Topics[0] {
		t.Fatal("event aliases receipt")
	}
	for _, bad := range []*types.Receipt{nil, {Status: types.ReceiptStatusFailed}, {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{{Address: proxy, Topics: []common.Hash{definition.ID}}}}, {Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{{Address: proxy, Topics: log.Topics, Data: log.Data, Removed: true}}}} {
		if _, err := c.ParseSwapReceipt(bad); err == nil {
			t.Fatal("accepted invalid receipt/log")
		}
	}
}

func signedFixture(t *testing.T) frame.SignedFrame {
	t.Helper()
	prepared, err := frame.Prepare(frame.FrameSpec{
		ChainID: 56, Protocol: proxy, Maker: maker,
		Pair:     frame.PairParams{PairID: 7, BaseToken: token, QuoteToken: recipient, PriceTickSize: big.NewInt(1000000000000000), LotSize: big.NewInt(1)},
		BaseTick: 100, UpdatedAt: 1000, MajorVersion: 12, MinorVersion: 2, CodecVersion: 1,
		Bids: []frame.Level{{Offset: 1, QtyLots: 2}}, Asks: []frame.Level{{Offset: 2, QtyLots: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := prepared.SigningPayload()
	key, _ := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	sig, err := crypto.Sign(payload.Digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	return frame.SignedFrame{Frame: payload.Frame, Maker: maker, Signature: sig}
}

func TestSignedFrameABIRoundTripAndUpdateTransaction(t *testing.T) {
	value := signedFixture(t)
	params := swapParams()
	// Best-effort updates may target a different maker from the swap itself.
	params.Maker = common.HexToAddress("0x5234567890123456789012345678901234567890")
	params.PriceUpdates = []frame.SignedFrame{value}
	for _, method := range []SwapMethod{MethodSwap, MethodSwapExactIn, MethodSwapWithCallback} {
		data, err := EncodeSwapCall(method, params)
		if err != nil {
			t.Fatal(err)
		}
		values, err := contractABI.Methods[string(method)].Inputs.Unpack(data[4:])
		if err != nil {
			t.Fatal(err)
		}
		if values[4].(common.Address) != params.Maker {
			t.Fatal("swap maker changed")
		}
		decoded := *abi.ConvertType(values[6], new([]frame.SignedFrame)).(*[]frame.SignedFrame)
		if !reflect.DeepEqual(decoded, params.PriceUpdates) {
			t.Fatal("nested frame calldata changed")
		}
	}
	api := &rpcTestAPI{}
	c := newClient(t, api)
	opts := testOpts(t)
	opts.NoSend = true
	tx, err := c.UpdateFrameBySig(opts, value)
	if err != nil {
		t.Fatal(err)
	}
	method := contractABI.Methods["updateFrameBySig"]
	if *tx.To() != proxy || !bytes.Equal(tx.Data()[:4], method.ID) {
		t.Fatal("wrong update call")
	}
	values, err := method.Inputs.Unpack(tx.Data()[4:])
	if err != nil {
		t.Fatal(err)
	}
	decoded := *abi.ConvertType(values[0], new(frame.CompactFrame)).(*frame.CompactFrame)
	if decoded != value.Frame || values[1].(common.Address) != maker || !bytes.Equal(values[2].([]byte), value.Signature) {
		t.Fatal("update frame changed")
	}
	value.Signature[64] = 2
	if _, err := c.UpdateFrameBySig(opts, value); err == nil {
		t.Fatal("accepted malformed signature")
	}
}

func TestTransactionsRequireContractSignatureRecoveryID(t *testing.T) {
	value := signedFixture(t)
	value.Signature[64] -= 27
	original := append([]byte(nil), value.Signature...)
	params := swapParams()
	params.PriceUpdates = []frame.SignedFrame{value}
	for _, method := range []SwapMethod{MethodSwap, MethodSwapExactIn, MethodSwapWithCallback} {
		if _, err := EncodeSwapCall(method, params); err == nil || !strings.Contains(err.Error(), "NormalizeSignature") {
			t.Fatalf("%s accepted v=0/1 or omitted normalization guidance: %v", method, err)
		}
	}
	api := &rpcTestAPI{}
	c := newClient(t, api)
	if tx, err := c.UpdateFrameBySig(testOpts(t), value); err == nil || tx != nil || api.sendCount() != 0 {
		t.Fatalf("broadcast a noncanonical signature: tx=%v err=%v", tx, err)
	}
	if !bytes.Equal(value.Signature, original) {
		t.Fatal("validation mutated the signature")
	}
	normalized, err := frame.NormalizeSignature(value.Signature)
	if err != nil {
		t.Fatal(err)
	}
	params.PriceUpdates[0].Signature = normalized
	if _, err := EncodeSwapCall(MethodSwap, params); err != nil {
		t.Fatalf("rejected normalized signature: %v", err)
	}
}
