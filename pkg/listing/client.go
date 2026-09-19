package listing

import (
	"net/http"
	"time"
)

// Config sets the HTTP configuration and credentials of a Listing
// client. NewClient validates timeouts without network requests.
// Credentials are fixed at construction and passed through without validation.
type Config struct {
	BaseURL    string        // Empty uses DefaultBaseURL; endpoint paths are appended when making requests.
	APIKey     string        // Sent as supplied in the X-API-Key header.
	HTTPClient *http.Client  // Nil uses the standard HTTP transport; otherwise its configuration is copied.
	Timeout    time.Duration // Per-request deadline, including body reads. Zero uses ten seconds.
}

// Client is a concurrency-safe public API client with immutable HTTP
// configuration and credentials. The standard or provided HTTP transport
// is reused; the client does not close shared transport resources.
// Responses larger than 8 MiB after decompression are rejected.
// A Client must not be copied after first use.
type Client struct {
	baseURL string
	http    http.Client
	timeout time.Duration

	credentials credentials
}

// NewClient validates timeouts without requests or background work.
// Empty BaseURL uses DefaultBaseURL; nonempty URLs are retained as supplied.
// URL errors are returned when making requests. When HTTPClient is nil, it
// creates an HTTP client using the effective Timeout and standard transport.
// Otherwise it copies HTTPClient's configuration. Redirect following is always
// disabled without modifying the caller's HTTPClient or redirect policy.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Timeout < 0 {
		return nil, &ConfigurationError{Field: "Timeout", Message: "must not be negative"}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	hc := http.Client{Timeout: cfg.Timeout}
	if cfg.HTTPClient != nil {
		hc = *cfg.HTTPClient
	}
	if hc.Timeout < 0 {
		return nil, &ConfigurationError{Field: "HTTPClient.Timeout", Message: "must not be negative"}
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		baseURL:     cfg.BaseURL,
		http:        hc,
		timeout:     cfg.Timeout,
		credentials: credentials{apiKey: cfg.APIKey},
	}, nil
}
