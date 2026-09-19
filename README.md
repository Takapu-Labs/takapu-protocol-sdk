# Takapu Protocol SDK

SDK for Takapu quote publishing, market data subscriptions, pair discovery and on-chain transactions.

## Features

- [Maker](pkg/maker/doc.go): quote publishing over WebSocket with reconnection
- [Router](pkg/router/doc.go): frame subscriptions over WebSocket with reconnection and local quote calculation
- [Listing](pkg/listing/doc.go): supported chains and pair discovery through the public HTTP API
- [Frame](pkg/frame/frame.go): integer order books and EIP-712 signing
- [Protocol](pkg/protocol/doc.go): swap simulation, approvals, transactions, and receipt parsing

## Examples

Run from the module root:

| Example | Command |
|---|---|
| [Protocol: signing and swaps](examples/protocol) | `go run ./examples/protocol` |
| [Discover chains and pairs](examples/listing/README.md) | `go run ./examples/listing -config examples/listing/config.local.json` |
| [Publish quotes](examples/maker) | `go run ./examples/maker -config examples/maker/config.local.json` |
| [Subscribe to quotes](examples/router) | `go run ./examples/router -config examples/router/config.local.json` |

Protocol runs offline by default, with optional read-only RPC simulation. Copy an example's `config.example.json` to `config.local.json` and fill in credentials and contract settings. Takapu service URLs use SDK defaults; only chain RPC URLs need configuration. The maker example sends real signed quotes and persists the next version in its configuration.

Default service endpoints:

| Service | URL |
|---|---|
| Listing HTTP API | `https://api.takapu.org/listing` |
| Maker WebSocket | `wss://api.takapu.org/marketstream/maker` |
| Router WebSocket | `wss://api.takapu.org/marketstream/router` |

## Listing size units

Listing `price_tick_size` / `Pair.PriceTickSize` and `lot_size` / `Pair.LotSize` are raw positive `uint128` **base-10 integer strings**, using the same units as on-chain `pairConfigs`. The existing field names are preserved for compatibility; they are not readable prices or token amounts. The SDK rejects fractional, signed, exponent, zero, and overflowing size values.

Call `pair.FrameParams()` to parse them into `frame.PairParams` (also `maker.PairParams`) without scaling. It validates the static metadata and returns independent `*big.Int` values. Check Listing `Active`/`Gray`, then compare token addresses and both raw integers with `protocol.Client.PairConfig` on your selected chain and proxy. Verify base and quote decimals against each ERC-20 `decimals()` at the same block. A Listing response can differ from on-chain configuration; stop on a mismatch. The [compiled verification example](pkg/listing/example_test.go) shows these checks.

For display or readable maker quotes, use exact decimal arithmetic:

```text
readable price tick = raw price_tick_size / 10^(18 + quote_decimals - base_decimals)
readable lot        = raw lot_size / 10^base_decimals
```

For an 8-decimal base token and 18-decimal quote token, raw tick `100000000000000000000000000` (`10^26`) means `0.01` quote tokens per base token, and raw lot `10000` means `0.0001` base tokens. A quote at price `100000` and amount `0.0001` is therefore `10000000` ticks and `1` lot. Supply raw sizes to `PairParams`, and readable price/amount strings to `maker.PriceLevel`.

## Development

```sh
make fmt       # Format Go code
make check     # Race tests, vet, build, and lint
```

Regenerate Protobuf bindings with `make generate`; see the [generation script](proto/generate.sh).
