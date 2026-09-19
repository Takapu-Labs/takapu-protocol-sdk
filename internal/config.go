package stream

import (
	"errors"
	"net/http"
	"time"
)

// ReconnectPolicy controls retries after a connection is lost.
type ReconnectPolicy uint8

// ReconnectConfig controls exponential backoff with jitter. Zero durations use
// defaults of 500 milliseconds, 30 seconds, and 60 seconds respectively.
type ReconnectConfig struct {
	Policy       ReconnectPolicy
	InitialDelay time.Duration
	MaxDelay     time.Duration
	ResetAfter   time.Duration // A session this long resets the retry delay.
}

// ConnectionConfig configures authentication, transport limits, and reconnects.
// Zero timeouts and capacities use defaults; negative values are invalid.
// Credentials are fixed at dialing and reused on reconnect; changes require a new client.
type ConnectionConfig struct {
	// Empty uses DefaultMakerURL or DefaultRouterURL according to the client.
	// Nonempty values are passed unchanged to the WebSocket dialer.
	URL    string
	APIKey string // API key shared by WebSocket and Listing authentication.

	HandshakeTimeout  time.Duration // Includes the connection acknowledgement; default 10 seconds.
	WriteTimeout      time.Duration // Includes waiting for the writer; default 5 seconds.
	HeartbeatInterval time.Duration // Default 10 seconds.
	ReadIdleTimeout   time.Duration // Must exceed HeartbeatInterval; default 30 seconds.

	EventBufferSize      int   // Default 256; overflow stops the client.
	MaxReadMessageBytes  int64 // Default 512 KiB.
	MaxWriteMessageBytes int64 // Default 512 KiB.
	Reconnect            ReconnectConfig
}

// ListingConfig contains only HTTP settings. Both clients reuse their
// Connection.APIKey for Listing authentication.
// The zero value disables Listing while preserving WebSocket-only clients.
type ListingConfig struct {
	BaseURL    string        // Empty uses listing.DefaultBaseURL when Listing is enabled.
	HTTPClient *http.Client  // Nil uses an SDK-provided client when Listing is enabled.
	Timeout    time.Duration // Zero uses the Listing default of ten seconds.
}

// MakerConfig configures a maker and its optional Listing client.
type MakerConfig struct {
	Connection ConnectionConfig
	Listing    ListingConfig
}

// RouterConfig configures subscriptions and the frame delivery buffer.
type RouterConfig struct {
	Connection          ConnectionConfig
	Listing             ListingConfig
	SubscribeTimeout    time.Duration // Includes queueing and acknowledgement; default 10 seconds.
	MaxSubscribeFilters int           // Default 100.
	FrameBufferSize     int           // Default 256; overflow stops the client with ErrFrameBufferFull.
}

func normalizeConnection(cfg ConnectionConfig) (ConnectionConfig, error) {
	// Keep each destination beside its default so adding a setting cannot
	// silently shift the defaults assigned to the remaining settings.
	durations := []struct {
		value        *time.Duration
		defaultValue time.Duration
	}{
		{&cfg.HandshakeTimeout, 10 * time.Second},
		{&cfg.WriteTimeout, 5 * time.Second},
		{&cfg.HeartbeatInterval, 10 * time.Second},
		{&cfg.ReadIdleTimeout, 30 * time.Second},
		{&cfg.Reconnect.InitialDelay, 500 * time.Millisecond},
		{&cfg.Reconnect.MaxDelay, 30 * time.Second},
		{&cfg.Reconnect.ResetAfter, 60 * time.Second},
	}
	for _, duration := range durations {
		if *duration.value < 0 {
			return cfg, errors.New("marketstream: negative duration")
		}
		if *duration.value == 0 {
			*duration.value = duration.defaultValue
		}
	}
	if cfg.ReadIdleTimeout <= cfg.HeartbeatInterval || cfg.Reconnect.MaxDelay < cfg.Reconnect.InitialDelay || cfg.Reconnect.Policy > Disabled {
		return cfg, errors.New("marketstream: invalid heartbeat or reconnect configuration")
	}
	if cfg.EventBufferSize < 0 || cfg.MaxReadMessageBytes < 0 || cfg.MaxWriteMessageBytes < 0 {
		return cfg, errors.New("marketstream: negative capacity")
	}
	if cfg.EventBufferSize == 0 {
		cfg.EventBufferSize = 256
	}
	if cfg.MaxReadMessageBytes == 0 {
		cfg.MaxReadMessageBytes = 512 * 1024
	}
	if cfg.MaxWriteMessageBytes == 0 {
		cfg.MaxWriteMessageBytes = 512 * 1024
	}
	return cfg, nil
}

func normalizeMaker(cfg MakerConfig) (MakerConfig, error) {
	if cfg.Connection.URL == "" {
		cfg.Connection.URL = DefaultMakerURL
	}
	connection, err := normalizeConnection(cfg.Connection)
	if err != nil {
		return cfg, err
	}
	cfg.Connection = connection
	return cfg, nil
}

func normalizeRouter(cfg RouterConfig) (RouterConfig, error) {
	if cfg.Connection.URL == "" {
		cfg.Connection.URL = DefaultRouterURL
	}
	connection, err := normalizeConnection(cfg.Connection)
	if err != nil {
		return cfg, err
	}
	cfg.Connection = connection
	if cfg.SubscribeTimeout < 0 || cfg.MaxSubscribeFilters < 0 || cfg.FrameBufferSize < 0 {
		return cfg, errors.New("marketstream: negative router configuration")
	}
	if cfg.SubscribeTimeout == 0 {
		cfg.SubscribeTimeout = 10 * time.Second
	}
	if cfg.MaxSubscribeFilters == 0 {
		cfg.MaxSubscribeFilters = 100
	}
	if cfg.FrameBufferSize == 0 {
		cfg.FrameBufferSize = 256
	}
	return cfg, nil
}
