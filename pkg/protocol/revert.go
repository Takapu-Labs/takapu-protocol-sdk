package protocol

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

// RevertError retains the original RPC error for errors.Is/As, along with the
// decoded Solidity custom error (or standard Error/Panic). Unknown selectors
// retain their raw bytes with Name="Unknown".
type RevertError struct {
	Name      string
	Arguments []any
	Data      []byte
	Cause     error
}

func (e *RevertError) Error() string {
	return fmt.Sprintf("protocol: contract reverted: %s%v: %v", e.Name, e.Arguments, e.Cause)
}

func (e *RevertError) Unwrap() error {
	return e.Cause
}

// ParseRevert recognizes RPC data errors regardless of provider-specific error
// code. Missing data, invalid hex and malformed arguments for known selectors
// leave the original error unchanged.
func ParseRevert(err error) error {
	if err == nil {
		return nil
	}
	var existing *RevertError
	if errors.As(err, &existing) {
		return err
	}
	var dataError rpc.DataError
	if !errors.As(err, &dataError) {
		return err
	}
	raw, ok := revertBytes(dataError.ErrorData())
	if !ok || len(raw) < 4 {
		return err
	}
	parsed := &RevertError{Name: "Unknown", Data: append([]byte(nil), raw...), Cause: err}
	if definition, decodeErr := contractABI.ErrorByID([4]byte(raw[:4])); decodeErr == nil {
		values, unpackErr := definition.Inputs.Unpack(raw[4:])
		if unpackErr != nil {
			return err
		}
		parsed.Name, parsed.Arguments = definition.Name, values
		return parsed
	}
	switch [4]byte(raw[:4]) {
	case [4]byte{0x08, 0xc3, 0x79, 0xa0}:
		parsed.Name = "Error"
	case [4]byte{0x4e, 0x48, 0x7b, 0x71}:
		parsed.Name = "Panic"
	default:
		return parsed
	}
	reason, decodeErr := abi.UnpackRevert(raw)
	if decodeErr != nil {
		return err
	}
	parsed.Arguments = []any{reason}
	return parsed
}

func revertBytes(value any) ([]byte, bool) {
	remaining := 32
	return nestedRevertBytes(value, &remaining)
}

func nestedRevertBytes(value any, remaining *int) ([]byte, bool) {
	// ErrorData may be supplied by caller-defined errors, including cyclic maps.
	// Bound total work as well as depth when nested maps share children.
	if *remaining == 0 {
		return nil, false
	}
	*remaining -= 1
	switch value := value.(type) {
	case string:
		raw, err := hexutil.Decode(value)
		return raw, err == nil && len(raw) >= 4
	case []byte:
		return value, len(value) >= 4
	case map[string]any:
		// Some RPC implementations nest the payload under data/return/result.
		for _, key := range []string{"data", "return", "result"} {
			if inner, found := value[key]; found {
				if raw, ok := nestedRevertBytes(inner, remaining); ok {
					return raw, true
				}
			}
		}
	}
	return nil, false
}
