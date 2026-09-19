package listing

import "time"

// DefaultBaseURL is the hosted Listing API used when Config.BaseURL is empty.
const DefaultBaseURL = "https://api.takapu.org/listing"

const (
	defaultTimeout   = 10 * time.Second
	maxResponseBytes = 8 << 20 // Includes decompressed JSON and trailing whitespace.
	chainsPath       = "/chains"
	pairsPath        = "/pairs"
	pairPath         = "/pair"
	headerAccept     = "Accept"
	mediaTypeJSON    = "application/json"
	jsonNull         = "null"
)

// Field names match the public API's wire representation.
const (
	fieldChainID        = "chain_id"
	fieldPairID         = "pair_id"
	fieldBaseToken      = "base_token"
	fieldQuoteToken     = "quote_token"
	fieldBaseSymbol     = "base_symbol"
	fieldQuoteSymbol    = "quote_symbol"
	fieldBaseDecimals   = "base_decimals"
	fieldQuoteDecimals  = "quote_decimals"
	fieldPriceTickSize  = "price_tick_size"
	fieldLotSize        = "lot_size"
	fieldTax            = "tax"
	fieldTaxToken       = "tax_token"
	fieldTaxTokenSymbol = "tax_token_symbol"
	fieldTaxRatePPM     = "tax_rate_ppm"
	fieldActive         = "active"
	fieldGray           = "gray"
	fieldData           = "data"
)

const (
	headerAPIKey    = "X-API-Key"
	decimalBase     = 10
	partsPerMillion = 1_000_000
)
