package stream

// APIKeyHeader carries the raw API key in WebSocket handshakes.
const APIKeyHeader = "X-API-Key"

const (
	// DefaultMakerURL is used when a Maker connection URL is empty.
	DefaultMakerURL = "wss://api.takapu.org/marketstream/maker"
	// DefaultRouterURL is used when a Router connection URL is empty.
	DefaultRouterURL = "wss://api.takapu.org/marketstream/router"
)

const (
	Enabled ReconnectPolicy = iota // zero value enables automatic reconnect
	Disabled
)

const (
	Connecting   ClientPhase = "connecting"
	Connected    ClientPhase = "connected"
	Reconnecting ClientPhase = "reconnecting"
	Stopped      ClientPhase = "stopped"
)

const (
	FrameRejected       EventKind = "frame_rejected"
	ConnectionChanged   EventKind = "connection_changed"
	SubscriptionChanged EventKind = "subscription_changed"
	ServerError         EventKind = "server_error"
)

const Replace SubscriptionKind = "replace"

const (
	// NotSent means the frame failed before a network write was attempted.
	NotSent PublishOutcome = "not_sent"
	// Unknown means writing started; the server may have received the frame.
	Unknown PublishOutcome = "unknown"
)
