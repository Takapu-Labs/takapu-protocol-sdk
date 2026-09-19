package listing

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidInput identifies invalid method arguments, before network I/O.
	ErrInvalidInput = errors.New("listing: invalid input")
	// ErrInvalidConfiguration identifies invalid client or returned pair config.
	ErrInvalidConfiguration = errors.New("listing: invalid configuration")
	// ErrInvalidResponse identifies malformed or oversized successful responses.
	ErrInvalidResponse = errors.New("listing: invalid response")
)

// InputError identifies invalid input without echoing credentials.
type InputError struct {
	Field   string
	Message string
}

func (e *InputError) Error() string {
	return fmt.Sprintf("listing: %s: %s", e.Field, e.Message)
}

func (e *InputError) Unwrap() error {
	return ErrInvalidInput
}

// ConfigurationError identifies invalid client configuration or pair fields.
// For returned pairs StatusCode preserves the successful HTTP response status.
type ConfigurationError struct {
	Field      string
	Message    string
	StatusCode int
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("listing: configuration %s: %s", e.Field, e.Message)
}

func (e *ConfigurationError) Unwrap() error {
	return ErrInvalidConfiguration
}

// APIError preserves a failed HTTP or business response, including 401, 403 and
// 404. Code and Message are the server's unmodified error fields when present.
// Cause records JSON, response-size, or body-reading failures.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

func (e *APIError) Error() string {
	if e.Code != "" || e.Message != "" {
		return fmt.Sprintf("listing: HTTP %d: %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("listing: HTTP %d request failed", e.StatusCode)
}

func (e *APIError) Unwrap() error {
	return e.Cause
}

// ResponseError preserves the HTTP status for malformed or oversized responses.
type ResponseError struct {
	StatusCode int
	Cause      error
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("listing: HTTP %d: invalid response: %v", e.StatusCode, e.Cause)
}

func (e *ResponseError) Unwrap() error {
	return e.Cause
}

func (e *ResponseError) Is(target error) bool {
	return target == ErrInvalidResponse
}

func invalidResponse(status int, reason string) error {
	return &ResponseError{StatusCode: status, Cause: errors.New(reason)}
}

func pairResponseError(status int, path string, err error) error {
	var configErr *ConfigurationError
	if errors.As(err, &configErr) {
		configErr.StatusCode = status
		configErr.Field = path + "." + configErr.Field
		return configErr
	}
	return &ResponseError{StatusCode: status, Cause: err}
}
