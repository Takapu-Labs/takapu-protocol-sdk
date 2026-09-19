package protocol

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

func TestParseMakerReverts(t *testing.T) {
	// Use raw on-chain selectors rather than packing with the embedded ABI.
	for name, data := range map[string]string{
		"MakerNotRegistered":  "0x785be6f1",
		"MakerInactive":       "0x41b5b284",
		"MakerNotContract":    "0x48dea83a",
		"MakerStateUnchanged": "0x58b2448c",
		"PayerIsMaker":        "0x263e700d",
	} {
		t.Run(name, func(t *testing.T) {
			cause := &rpcFailure{message: "execution reverted", data: data}
			var decoded *RevertError
			got := ParseRevert(cause)
			if !errors.As(got, &decoded) || decoded.Name != name || len(decoded.Arguments) != 0 || !errors.Is(got, cause) {
				t.Fatalf("incorrect maker revert: %v", got)
			}
		})
	}
	data := append(common.FromHex("0x6a8dae1e"), common.LeftPadBytes(maker.Bytes(), 32)...)
	cause := &rpcFailure{message: "execution reverted", data: data}
	var decoded *RevertError
	got := ParseRevert(cause)
	if !errors.As(got, &decoded) || decoded.Name != "SignerQueryFailed" || len(decoded.Arguments) != 1 || decoded.Arguments[0] != maker {
		t.Fatalf("incorrect maker signer-query revert: %v", got)
	}
}

func TestParseRevertMalformedKnownArguments(t *testing.T) {
	custom := contractABI.Errors["SlippageExceeded"].ID
	for _, data := range [][]byte{
		custom[:4],
		{0x08, 0xc3, 0x79, 0xa0}, // Error(string) without arguments.
		{0x4e, 0x48, 0x7b, 0x71}, // Panic(uint256) without its code.
	} {
		cause := &rpcFailure{message: "execution reverted", data: hexutil.Encode(data)}
		if got := ParseRevert(cause); got != cause {
			t.Fatalf("malformed payload %x changed the original error: %v", data, got)
		}
	}
}

func TestParseRevertNestedPayloads(t *testing.T) {
	for _, data := range []any{
		map[string]any{"data": "0x", "return": "0xdeadbeef"},
		map[string]any{"data": []byte{1}, "result": map[string]any{"data": "0xdeadbeef"}},
	} {
		cause := &rpcFailure{message: "execution reverted", data: data}
		var decoded *RevertError
		if got := ParseRevert(cause); !errors.As(got, &decoded) || decoded.Name != "Unknown" || hexutil.Encode(decoded.Data) != "0xdeadbeef" {
			t.Fatalf("lost nested payload: %v", got)
		}
	}

	cyclic := make(map[string]any)
	for _, key := range []string{"data", "return", "result"} {
		cyclic[key] = cyclic
	}
	cause := &rpcFailure{message: "execution reverted", data: cyclic}
	if got := ParseRevert(cause); got != cause {
		t.Fatal("cyclic data changed the original error")
	}
}
