package maker

import stream "github.com/Takapu-Labs/takapu-protocol-sdk/internal"

// PublishOutcome states whether a failed Publish could have reached the server.
type PublishOutcome = stream.PublishOutcome

// PublishError preserves the write outcome and wraps the underlying failure.
// Use errors.As to inspect Outcome and errors.Is to inspect the cause.
type PublishError = stream.PublishError

// Sentinel errors can be inspected with errors.Is.
var (
	ErrClosed               = stream.ErrClosed
	ErrDisconnected         = stream.ErrDisconnected
	ErrAuthentication       = stream.ErrAuthentication
	ErrProtocol             = stream.ErrProtocol
	ErrEventBufferFull      = stream.ErrEventBufferFull
	ErrMessageTooLarge      = stream.ErrMessageTooLarge
	ErrListingNotConfigured = stream.ErrListingNotConfigured
)
