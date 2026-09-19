// Package listing discovers supported chains and pairs through the public HTTP API.
//
// NewClient requires a Maker or Router APIKey. An empty
// BaseURL uses https://api.takapu.org/listing; requests default to a
// ten-second timeout. Clients support concurrent calls, reuse HTTP transports,
// and need no Close method. Credentials are fixed at construction. Each request
// authenticates using the X-API-Key header. Redirects and SDK retries are disabled.
//
// ListChains calls GET /chains and returns a map of chain names to chain IDs.
// ListPairs calls GET /pairs?chain_id=... and returns the chain's complete
// public list without pagination. The server includes approved, active pairs
// with an active tax configuration. ListPair calls GET /pair?chain_id=...&pair_id=...
// using a chain ID and on-chain pair ID. Callers should verify Active, Gray,
// and a nonzero PairID before using a pair.
//
// PriceTickSize maps to price_tick_size and LotSize maps to lot_size. Both
// preserve raw positive uint128 base-10 integer strings in pairConfigs units;
// fractional, signed, exponent and out-of-range values are rejected. Use
// Pair.FrameParams to parse them directly without rescaling. For display only:
//
//	readable tick = PriceTickSize / 10^(18 + QuoteDecimals - BaseDecimals)
//	readable lot  = LotSize / 10^BaseDecimals
//
// With base/quote decimals 8/18, raw sizes "100000000000000000000000000" and
// "10000" mean a price tick of 0.01 quote/base and a lot of 0.0001 base tokens.
// Before signing, compare tokens and raw sizes with pairConfigs on the selected
// chain/proxy, and verify decimals with each token's decimals() method. Do not
// infer size units from whether a string contains a decimal point.
//
// Tax is nil when the server reports no active tax configuration; a missing
// tax field is an error. Tax describes the listing configuration and may differ
// from current on-chain fees. Confirm executable prices and fees by simulation.
//
// Listing does not establish streaming permissions or quote availability, and
// its results are not a snapshot of on-chain state. Refresh selected details
// before use. Queries remain independent of any parent Maker or Router's
// streaming lifetime; caller-owned HTTP transports are never closed by the SDK.
package listing
