package maker

import stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"

// APIKeyHeader carries the raw API key in WebSocket handshakes.
const APIKeyHeader = stream.APIKeyHeader

// DefaultMakerURL is used when MakerConfig.Connection.URL is empty.
const DefaultMakerURL = stream.DefaultMakerURL

const (
	Enabled  = stream.Enabled // zero value enables automatic reconnect
	Disabled = stream.Disabled
)

const (
	Connecting   = stream.Connecting
	Connected    = stream.Connected
	Reconnecting = stream.Reconnecting
	Stopped      = stream.Stopped
)

const (
	ConnectionChanged = stream.ConnectionChanged
	FrameRejected     = stream.FrameRejected
)

const (
	// NotSent means the frame failed before a network write was attempted.
	NotSent = stream.NotSent
	// Unknown means writing started; the server may have received the frame.
	Unknown = stream.Unknown
)
