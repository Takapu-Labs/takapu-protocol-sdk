package listing

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseBodyLimit(t *testing.T) {
	const success = `{"success":true,"data":[]}`
	const failure = `{"success":false,"error":{"code":"UNAVAILABLE","message":"try later"}}`
	padding := strings.Repeat(" ", maxResponseBytes)
	for _, tc := range []struct {
		name   string
		status int
		prefix string
		extra  int
	}{
		{"exact limit", http.StatusOK, success, 0},
		{"trailing whitespace", http.StatusOK, success, 1},
		{"oversized JSON", http.StatusOK, `{"padding":"`, 1},
		{"error response", http.StatusServiceUnavailable, failure, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedResponseBody{Reader: io.MultiReader(
				strings.NewReader(tc.prefix),
				strings.NewReader(padding[:maxResponseBytes-len(tc.prefix)+tc.extra]),
			)}
			client, err := NewClient(Config{
				APIKey: "key",
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, ContentLength: -1, Body: body}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ListPairs(context.Background(), testChainID)
			if !body.closed || body.read > maxResponseBytes+1 {
				t.Fatalf("response body: closed=%v, bytes read=%d", body.closed, body.read)
			}
			if tc.extra == 0 {
				if err != nil {
					t.Fatalf("exact-size response rejected: %v", err)
				}
				return
			}
			var cause error
			if tc.status == http.StatusOK {
				var responseErr *ResponseError
				if !errors.As(err, &responseErr) || responseErr.StatusCode != tc.status || !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("oversized success response: %v", err)
				}
				cause = responseErr.Cause
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || apiErr.Code != "UNAVAILABLE" {
					t.Fatalf("oversized API error lost metadata: %v", err)
				}
				cause = apiErr.Cause
			}
			if cause == nil || !strings.Contains(cause.Error(), fmt.Sprintf("exceeds %d bytes", maxResponseBytes)) {
				t.Fatalf("missing response size error: %v", err)
			}
		})
	}
}

func TestResponseBodyReadFailure(t *testing.T) {
	want := errors.New("response interrupted")
	body := &trackedResponseBody{Reader: io.MultiReader(
		strings.NewReader(`{"success":true,"data":[]}`),
		failingReader{err: want},
	)}
	client, err := NewClient(Config{
		APIKey: "key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListPairs(context.Background(), testChainID)
	if !errors.Is(err, want) || !errors.Is(err, ErrInvalidResponse) || !body.closed {
		t.Fatalf("body read failure: err=%v, closed=%v", err, body.closed)
	}
}

func TestResponseBodyLimitAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		defer compressed.Close()
		_, _ = io.WriteString(compressed, `{"success":true,"data":[]}`)
		_, _ = io.WriteString(compressed, strings.Repeat(" ", maxResponseBytes))
	}))
	defer server.Close()
	_, err := testClient(t, server, nil).ListPairs(context.Background(), testChainID)
	if !errors.Is(err, ErrInvalidResponse) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("compressed response bypassed body limit: %v", err)
	}
}

type trackedResponseBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *trackedResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackedResponseBody) Close() error {
	b.closed = true
	return nil
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
