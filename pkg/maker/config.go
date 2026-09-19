package maker

import internal "github.com/Takapu-Labs/takapu-protocol-sdk/internal"

// ReconnectPolicy controls retries after a connection is lost.
type ReconnectPolicy = internal.ReconnectPolicy

// ReconnectConfig controls exponential backoff with jitter. Zero durations use
// defaults of 500 milliseconds, 30 seconds, and 60 seconds respectively.
type ReconnectConfig = internal.ReconnectConfig

// ConnectionConfig configures authentication, transport limits, and reconnects.
// An empty URL uses DefaultMakerURL.
// Zero timeouts and capacities use defaults; negative values are invalid.
// Credentials are fixed at dialing and reused on reconnect; changes require a new client.
type ConnectionConfig = internal.ConnectionConfig

// ListingConfig contains only HTTP settings. Makers reuse their
// Connection.APIKey for Listing authentication.
// The zero value disables Listing while preserving WebSocket-only clients.
type ListingConfig = internal.ListingConfig

// MakerConfig configures a maker and its optional Listing client.
type MakerConfig = internal.MakerConfig
