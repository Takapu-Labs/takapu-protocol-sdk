// Package maker provides a Maker client for WebSocket and direct on-chain quote
// publishing, with an optional
// Listing HTTP client that shares its API credentials. An empty connection URL
// uses DefaultMakerURL; explicit URLs support other deployments and tests.
//
// Dial returns only after authentication. The supplied lifetime context
// controls streaming; each operation also accepts its own context. Close stops
// streaming and waits for cleanup. The optional Listing client remains usable
// after streaming stops. Credentials are fixed at dialing and reused on reconnect;
// changing them requires a new client.
//
// A successful Publish is a network write, never a delivery or execution receipt.
// Publish accepts decimal price/amount levels and trusted pair/signing metadata,
// rounds bid prices down, ask prices up and amounts down to protocol ticks/lots,
// then builds and signs the frame. Published readable prices and amounts match
// the rounded, signed integers. Rounding uses exact decimal arithmetic; duplicate
// ticks, zero quantities and counts outside the positive uint24 range are rejected.
// Amounts describe raw per-level depth. When applied on-chain within the same
// maker lifecycle, minor updates within the same major preserve filled-lot
// counters, which reduce remaining executable depth; increasing major resets
// both counters. See PriceLevel.Amount and PublishParams.
// Callers own inventory checks, quote timestamps and version progression.
// Makers do not replay frames on reconnect.
//
// Makers normally update quotes through Maker.Publish over WebSocket. Use
// Maker.SubmitFrameUpdate for exceptional cases, such as emergency updates that
// need to bypass marketstream and submit a frame directly on-chain.
// It accepts the same PublishParams and a caller-owned
// protocol.Client to sign and submit a quote directly over chain RPC. It works
// on a zero-value Maker and after Close, without dialing marketstream.
// Its transaction sender pays gas and may differ from the frame signer.
// It returns a signed transaction, including when broadcasting fails; use its
// hash to resolve the outcome. Check receipt Status after inclusion. Transaction
// options control cancellation, fees, nonces and signing without broadcasting.
//
// Continuously consume events through Maker.Next. Event buffer overflow stops
// the client; Err reports the terminal cause even when no final event fits in the
// buffer. Methods are safe for concurrent use, but callers must not mutate
// quote inputs while an operation is using them.
package maker
