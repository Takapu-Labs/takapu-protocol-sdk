package listing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type envelope struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) get(ctx context.Context, path string, query url.Values) (envelope, int, error) {
	if ctx == nil {
		return envelope{}, 0, &InputError{Field: "ctx", Message: "must not be nil"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return envelope{}, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return envelope{}, 0, err
	}
	// Append the endpoint without cleaning the supplied path or re-encoding it.
	if strings.HasSuffix(req.URL.EscapedPath(), "/") {
		path = strings.TrimPrefix(path, "/")
	}
	req.URL.Path += path
	if req.URL.RawPath != "" {
		req.URL.RawPath += path
	}
	req.URL.RawQuery = mergeQuery(req.URL.RawQuery, query)
	c.credentials.authenticate(req)
	req.Header.Set(headerAccept, mediaTypeJSON)
	res, err := c.http.Do(req)
	if err != nil {
		return envelope{}, 0, fmt.Errorf("listing: GET: %w", err)
	}
	defer res.Body.Close()
	status := res.StatusCode
	env, decodeErr := decodeEnvelope(res.Body)
	if status < http.StatusOK || status >= http.StatusMultipleChoices || (env.Success != nil && !*env.Success) {
		apiErr := &APIError{StatusCode: status, Cause: decodeErr}
		if env.Error != nil {
			apiErr.Code, apiErr.Message = env.Error.Code, env.Error.Message
		}
		return envelope{}, status, apiErr
	}
	if decodeErr != nil {
		return envelope{}, status, &ResponseError{StatusCode: status, Cause: decodeErr}
	}
	if env.Success == nil || !*env.Success || env.Error != nil || len(env.Data) == 0 || isNull(env.Data) {
		return envelope{}, status, invalidResponse(status, "expected success=true, non-null data, and no error")
	}
	return env, status, nil
}

func mergeQuery(raw string, query url.Values) string {
	params := query.Encode()
	if params == "" {
		return raw
	}
	// Method arguments take precedence over BaseURL defaults. Preserve unrelated
	// components verbatim, including repeated keys and their original escaping.
	parts := strings.Split(raw, "&")
	retained := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(key)
		if err == nil && query.Has(name) {
			continue
		}
		retained = append(retained, part)
	}
	if raw = strings.Join(retained, "&"); raw == "" {
		return params
	}
	return raw + "&" + params
}

func decodeEnvelope(body io.Reader) (envelope, error) {
	// Read one extra byte to distinguish an exact-size response from truncation.
	// Limit the decoded stream so gzip and chunked responses have the same bound.
	limited := &io.LimitedReader{R: body, N: maxResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	var env envelope
	err := decoder.Decode(&env)
	if err == nil {
		var extra json.RawMessage
		if err = decoder.Decode(&extra); errors.Is(err, io.EOF) {
			err = nil
		} else if err == nil {
			err = errors.New("multiple JSON values")
		}
	}
	if limited.N == 0 {
		err = fmt.Errorf("response body exceeds %d bytes", maxResponseBytes)
	}
	return env, err
}

func isNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte(jsonNull))
}
