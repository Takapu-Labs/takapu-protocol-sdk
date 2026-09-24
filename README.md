# Takapu Protocol SDK

SDK for Takapu quote publishing, market data subscriptions, pair discovery and on-chain transactions.

Read the [Go SDK documentation](https://docs.takapu.org/build/go-sdk/go-sdk) for setup and usage.

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

## Development

```sh
make fmt       # Format Go code
make check     # Race tests, vet, build, and lint
```

Regenerate Protobuf bindings with `make generate`; see the [generation script](proto/generate.sh).
