package listing

import "net/http"

// Credentials remain fixed after creation.
type credentials struct {
	apiKey string
}

// authenticate uses the immutable API key configured when the client was created.
func (c credentials) authenticate(req *http.Request) {
	req.Header.Set(headerAPIKey, c.apiKey)
}
