package protocol

import (
	_ "embed"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

//go:embed abi/takapu_protocol.json
var protocolABIJSON string

var contractABI = mustABI(protocolABIJSON)

var tokenABI = mustABI(erc20ABIJSON)

func mustABI(s string) abi.ABI {
	a, err := abi.JSON(strings.NewReader(s))
	if err != nil {
		panic(err)
	}
	return a
}

// ABI returns a separately parsed ABI, so callers cannot mutate the client's ABI.
func ABI() (abi.ABI, error) {
	return abi.JSON(strings.NewReader(protocolABIJSON))
}
