package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestPairConfigRead(t *testing.T) {
	for _, active := range []bool{true, false} {
		name := "active"
		var blockNumber *big.Int
		wantBlock := "latest"
		if !active {
			name = "inactive"
			blockNumber = big.NewInt(123)
			wantBlock = "0x7b"
		}
		t.Run(name, func(t *testing.T) {
			tick := new(big.Int).Lsh(big.NewInt(1), 100)
			lot := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
			api := &rpcTestAPI{call: func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
				_, to, data := decodeCall(t, args)
				want, err := contractABI.Pack("pairConfigs", uint32(7))
				if err != nil {
					t.Fatal(err)
				}
				if to != proxy || block != wantBlock || !bytes.Equal(data, want) {
					t.Errorf("wrong pair call: to=%s block=%s data=%x", to, block, data)
				}
				return contractABI.Methods["pairConfigs"].Outputs.Pack(token, maker, tick, lot, active, maker, big.NewInt(999_999))
			}}
			got, err := newClient(t, api).PairConfig(context.Background(), 7, blockNumber)
			if err != nil {
				t.Fatal(err)
			}
			if got.BaseToken != token || got.QuoteToken != maker || got.PriceTickSize.Cmp(tick) != 0 || got.LotSize.Cmp(lot) != 0 ||
				got.Active != active || got.FeeToken != maker || got.FeeRate != 999_999 {
				t.Fatalf("incorrect pair configuration: %+v", got)
			}
		})
	}
}

func TestPairConfigRejectsInvalidResponses(t *testing.T) {
	valid := func() []any {
		return []any{token, maker, big.NewInt(10), big.NewInt(20), true, token, big.NewInt(1000)}
	}
	tests := map[string]func([]any){
		"unregistered": func(v []any) {
			v[0], v[1], v[2], v[3], v[4], v[5], v[6] = common.Address{}, common.Address{}, big.NewInt(0), big.NewInt(0), false, common.Address{}, big.NewInt(0)
		},
		"zero base token":     func(v []any) { v[0] = common.Address{} },
		"zero quote token":    func(v []any) { v[1] = common.Address{} },
		"identical tokens":    func(v []any) { v[1] = token },
		"zero tick":           func(v []any) { v[2] = big.NewInt(0) },
		"zero lot":            func(v []any) { v[3] = big.NewInt(0) },
		"oversized tick":      func(v []any) { v[2] = new(big.Int).Lsh(big.NewInt(1), 128) },
		"oversized lot":       func(v []any) { v[3] = new(big.Int).Lsh(big.NewInt(1), 128) },
		"foreign fee token":   func(v []any) { v[5] = recipient },
		"fee without token":   func(v []any) { v[5] = common.Address{} },
		"token without fee":   func(v []any) { v[6] = big.NewInt(0) },
		"one hundred percent": func(v []any) { v[6] = big.NewInt(1_000_000) },
		"oversized fee":       func(v []any) { v[6] = new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1)) },
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			values := valid()
			edit(values)
			api := &rpcTestAPI{call: func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
				return contractABI.Methods["pairConfigs"].Outputs.Pack(values...)
			}}
			got, err := newClient(t, api).PairConfig(context.Background(), 7, nil)
			if err == nil || got.PriceTickSize != nil {
				t.Fatalf("invalid response returned %+v, %v", got, err)
			}
		})
	}
	t.Run("fee disabled", func(t *testing.T) {
		values := valid()
		values[5], values[6] = common.Address{}, big.NewInt(0)
		api := &rpcTestAPI{call: func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
			return contractABI.Methods["pairConfigs"].Outputs.Pack(values...)
		}}
		got, err := newClient(t, api).PairConfig(context.Background(), 7, nil)
		if err != nil || got.FeeToken != (common.Address{}) || got.FeeRate != 0 {
			t.Fatalf("disabled fee returned %+v, %v", got, err)
		}
	})
}

func TestFeeRecipientRead(t *testing.T) {
	for _, want := range []common.Address{recipient, {}} {
		t.Run(want.Hex(), func(t *testing.T) {
			var blockNumber *big.Int
			wantBlock := "latest"
			if want == (common.Address{}) {
				blockNumber, wantBlock = big.NewInt(42), "0x2a"
			}
			api := &rpcTestAPI{call: func(args map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
				_, to, data := decodeCall(t, args)
				if to != proxy || block != wantBlock || !bytes.Equal(data, contractABI.Methods["feeRecipient"].ID) {
					t.Errorf("wrong fee recipient call: to=%s block=%s data=%x", to, block, data)
				}
				return contractABI.Methods["feeRecipient"].Outputs.Pack(want)
			}}
			got, err := newClient(t, api).FeeRecipient(context.Background(), blockNumber)
			if err != nil || got != want {
				t.Fatalf("fee recipient=%s, error=%v", got, err)
			}
		})
	}
}

func TestPairAndFeeRecipientReadErrors(t *testing.T) {
	api := &rpcTestAPI{}
	c := newClient(t, api)
	reads := map[string]func(context.Context, *big.Int) error{
		"pair": func(ctx context.Context, block *big.Int) error {
			_, err := c.PairConfig(ctx, 7, block)
			return err
		},
		"fee recipient": func(ctx context.Context, block *big.Int) error {
			_, err := c.FeeRecipient(ctx, block)
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			api.call = func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
				t.Error("invalid input reached RPC")
				return nil, nil
			}
			if err := read(nil, nil); err == nil {
				t.Fatal("nil context accepted")
			}
			if err := read(context.Background(), big.NewInt(-1)); err == nil {
				t.Fatal("negative block accepted")
			}
			if name == "pair" {
				if _, err := c.PairConfig(context.Background(), 0, nil); err == nil {
					t.Fatal("zero pair ID accepted")
				}
			}
			api.call = func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
				return make([]byte, 31), nil
			}
			if err := read(context.Background(), nil); err == nil {
				t.Fatal("truncated response accepted")
			}
			definition := contractABI.Errors["PairDisabled"]
			api.call = func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
				return nil, rpcFailure{"execution reverted", hexutil.Encode(definition.ID[:4])}
			}
			err := read(context.Background(), nil)
			var revert *RevertError
			var rpcErr rpc.Error
			if !errors.As(err, &revert) || revert.Name != "PairDisabled" || !errors.As(err, &rpcErr) {
				t.Fatalf("RPC revert was not preserved: %v", err)
			}
			api.call = func(map[string]json.RawMessage, string) (hexutil.Bytes, error) {
				return nil, rpcFailure{"node unavailable", nil}
			}
			err = read(context.Background(), nil)
			if !errors.As(err, &rpcErr) || err.Error() != "node unavailable" {
				t.Fatalf("RPC failure was not preserved: %v", err)
			}
		})
	}
}
