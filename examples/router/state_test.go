package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestLoadQuoteConfigReadsDecayAndDecimalsAtOneBlock(t *testing.T) {
	for _, test := range []struct {
		name       string
		decayStart uint32
		active     bool
		decimals   int64
		wantError  string
	}{
		{name: "configured decay", decayStart: 17, active: true, decimals: 18},
		{name: "zero decay start", active: true, decimals: 18},
		{name: "zero token decimals", decayStart: 17, active: true},
		{name: "inactive pair", decimals: 18, wantError: "inactive"},
		{name: "invalid decimals", active: true, decimals: 256, wantError: "invalid uint8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &quoteStateRPC{t: t, active: test.active, decayStart: test.decayStart, baseDecimals: test.decimals}
			server := rpc.NewServer()
			if err := server.RegisterName("eth", api); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Stop)
			rpcClient := ethclient.NewClient(rpc.DialInProc(server))
			t.Cleanup(rpcClient.Close)
			contract, err := protocol.NewClient(context.Background(), rpcClient, protocol.Config{
				ChainID: 1, Proxy: common.HexToAddress("0x1111111111111111111111111111111111111111"),
			})
			if err != nil {
				t.Fatal(err)
			}
			params, err := loadQuoteConfig(context.Background(), contract, rpcClient, 7)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("loadQuoteConfig error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if params.Pair.BaseDecimals != uint8(test.decimals) || params.Pair.QuoteDecimals != 6 || params.DecayStartOffsetSeconds != test.decayStart {
				t.Fatalf("wrong quote scales or decay: %+v", params)
			}
			if params.FeeToken != params.Pair.BaseToken || params.FeeRatePPM != 100_000 {
				t.Fatalf("wrong quote fee: %+v", params)
			}
		})
	}
}

type quoteStateRPC struct {
	t            *testing.T
	active       bool
	decayStart   uint32
	baseDecimals int64
}

func (s *quoteStateRPC) ChainId() hexutil.Uint64 { return 1 }

func (s *quoteStateRPC) BlockNumber() hexutil.Uint64 { return 123 }

func (s *quoteStateRPC) Call(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
	if block != "0x7b" {
		s.t.Errorf("read used block %s, want 0x7b", block)
	}
	var token common.Address
	if err := json.Unmarshal(args["to"], &token); err != nil {
		return nil, err
	}
	input := args["input"]
	if input == nil {
		input = args["data"]
	}
	var data hexutil.Bytes
	if err := json.Unmarshal(input, &data); err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("missing call selector")
	}
	selector := func(signature string) string { return string(crypto.Keccak256([]byte(signature))[:4]) }
	words := func(values ...int64) []byte {
		var result []byte
		for _, value := range values {
			result = append(result, common.LeftPadBytes(big.NewInt(value).Bytes(), 32)...)
		}
		return result
	}
	switch string(data[:4]) {
	case selector("pairConfigs(uint32)"):
		active := int64(0)
		if s.active {
			active = 1
		}
		return words(1, 2, 1_000_000_000_000_000_000, 1, active, 1, 100_000), nil
	case selector("feeRecipient()"):
		return words(3), nil
	case selector("globalDecayConfig()"):
		return words(int64(s.decayStart), 30, 60, 100_000), nil
	case selector("decimals()"):
		switch token {
		case common.HexToAddress("0x1"):
			return words(s.baseDecimals), nil
		case common.HexToAddress("0x2"):
			return words(6), nil
		}
	}
	return nil, fmt.Errorf("unexpected call to %s: %x", token, data)
}
