package listing

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
)

func decodePairs(data json.RawMessage, status int, chainID uint64) ([]Pair, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, &ResponseError{StatusCode: status, Cause: err}
	}
	result := make([]Pair, 0, len(items))
	for i, item := range items {
		pair, err := decodePair(item)
		if err != nil {
			return nil, pairResponseError(status, fmt.Sprintf("%s[%d]", fieldData, i), err)
		}
		if pair.ChainID != chainID {
			return nil, invalidResponse(status, fmt.Sprintf("data[%d].chain_id does not match request", i))
		}
		result = append(result, pair)
	}
	return result, nil
}

func decodePair(data json.RawMessage) (Pair, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Pair{}, err
	}
	if fields == nil {
		return Pair{}, fmt.Errorf("pair must be an object")
	}
	var p Pair
	var err error
	if p.ChainID, err = required[uint64](fields, fieldChainID); err != nil {
		return Pair{}, err
	}
	if p.ChainID == 0 || p.ChainID > math.MaxInt64 {
		return Pair{}, configError(fieldChainID, "must be positive and fit int64")
	}
	if p.PairID, err = required[uint32](fields, fieldPairID); err != nil {
		return Pair{}, err
	}
	if p.PairID > math.MaxInt32 {
		return Pair{}, configError(fieldPairID, "must fit int32")
	}
	if p.BaseToken, err = addressField(fields, fieldBaseToken); err != nil {
		return Pair{}, err
	}
	if p.QuoteToken, err = addressField(fields, fieldQuoteToken); err != nil {
		return Pair{}, err
	}
	if p.BaseToken == (common.Address{}) || p.QuoteToken == (common.Address{}) || p.BaseToken == p.QuoteToken {
		return Pair{}, configError("tokens", "base and quote must be distinct nonzero token addresses")
	}
	if p.BaseSymbol, err = required[string](fields, fieldBaseSymbol); err != nil {
		return Pair{}, err
	}
	if p.QuoteSymbol, err = required[string](fields, fieldQuoteSymbol); err != nil {
		return Pair{}, err
	}
	if p.BaseDecimals, err = required[uint8](fields, fieldBaseDecimals); err != nil {
		return Pair{}, err
	}
	if p.QuoteDecimals, err = required[uint8](fields, fieldQuoteDecimals); err != nil {
		return Pair{}, err
	}
	if p.PriceTickSize, err = positiveUint128String(fields, fieldPriceTickSize); err != nil {
		return Pair{}, err
	}
	if p.LotSize, err = positiveUint128String(fields, fieldLotSize); err != nil {
		return Pair{}, err
	}
	if p.Tax, err = decodeTax(fields, p.BaseToken, p.QuoteToken); err != nil {
		return Pair{}, err
	}
	if p.Active, err = required[bool](fields, fieldActive); err != nil {
		return Pair{}, err
	}
	if p.Gray, err = required[bool](fields, fieldGray); err != nil {
		return Pair{}, err
	}
	return p, nil
}

func decodeTax(fields map[string]json.RawMessage, base, quote common.Address) (*Tax, error) {
	raw, ok := fields[fieldTax]
	if !ok {
		return nil, configError(fieldTax, "required nullable field is missing")
	}
	if isNull(raw) {
		return nil, nil
	}
	taxFields, err := required[map[string]json.RawMessage](fields, fieldTax)
	if err != nil {
		return nil, err
	}
	var tax Tax
	if tax.Token, err = addressField(taxFields, fieldTaxToken); err != nil {
		return nil, pairResponseError(0, fieldTax, err)
	}
	if tax.Token != base && tax.Token != quote {
		return nil, configError(fieldTax+"."+fieldTaxToken, "must be the base or quote token")
	}
	if tax.Symbol, err = required[string](taxFields, fieldTaxTokenSymbol); err != nil {
		return nil, pairResponseError(0, fieldTax, err)
	}
	if tax.RatePPM, err = required[uint32](taxFields, fieldTaxRatePPM); err != nil {
		return nil, pairResponseError(0, fieldTax, err)
	}
	if tax.RatePPM >= partsPerMillion {
		return nil, configError(fieldTax+"."+fieldTaxRatePPM, fmt.Sprintf("must be less than %d PPM", partsPerMillion))
	}
	return &tax, nil
}

func required[T any](fields map[string]json.RawMessage, name string) (T, error) {
	var value T
	raw, ok := fields[name]
	if !ok || isNull(raw) {
		return value, configError(name, "required field is missing or null")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, configError(name, "invalid field type, format, or numeric range")
	}
	return value, nil
}

func addressField(fields map[string]json.RawMessage, name string) (common.Address, error) {
	value, err := required[string](fields, name)
	if err != nil {
		return common.Address{}, err
	}
	if !common.IsHexAddress(value) {
		return common.Address{}, configError(name, "must be a 20-byte hex address")
	}
	return common.HexToAddress(value), nil
}

func positiveUint128String(fields map[string]json.RawMessage, name string) (string, error) {
	value, err := required[string](fields, name)
	if err != nil {
		return "", err
	}
	if _, err := parseRawSize(value, name); err != nil {
		return "", err
	}
	return value, nil
}

func configError(field, message string) error {
	return &ConfigurationError{Field: field, Message: message}
}
