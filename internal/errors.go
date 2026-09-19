package stream

import (
	"errors"
	"fmt"
)

// ServerErrorInfo is an error reported by the router service.
type ServerErrorInfo struct{ Code, Message string }

func (e *ServerErrorInfo) Error() string {
	return fmt.Sprintf("marketstream: %s: %s", e.Code, e.Message)
}

// PublishOutcome states whether a failed Publish could have reached the server.
type PublishOutcome string

// PublishError preserves the write outcome and wraps the underlying failure.
// Use errors.As to inspect Outcome and errors.Is to inspect the cause.
type PublishError struct {
	Outcome PublishOutcome
	Err     error
}

func (e *PublishError) Error() string {
	return fmt.Sprintf("publish %s: %v", e.Outcome, e.Err)
}

func (e *PublishError) Unwrap() error {
	return e.Err
}

// Sentinel errors can be inspected with errors.Is, including through PublishError.
var (
	ErrClosed               = errors.New("marketstream: client stopped")
	ErrDisconnected         = errors.New("marketstream: not connected")
	ErrAuthentication       = errors.New("marketstream: authentication failed")
	ErrProtocol             = errors.New("marketstream: protocol violation")
	ErrEventBufferFull      = errors.New("marketstream: event buffer full")
	ErrFrameBufferFull      = errors.New("marketstream: frame buffer full")
	ErrMessageTooLarge      = errors.New("marketstream: message exceeds configured limit")
	ErrListingNotConfigured = errors.New("marketstream: Listing is not configured")
)

// errManualReconnect bypasses automatic-reconnect policy for an explicit request.
var errManualReconnect = errors.New("marketstream: requested reconnect")
