package listing

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func chainsEnvelope() map[string]any {
	return map[string]any{
		"success": true,
		"data": map[string]uint64{
			"ethereum": 1, "bsc": 56, "polygon": 137,
			"base": 8453, "arbitrum": 42161, "sepolia": 11155111,
		},
	}
}

func TestListChainsAuthenticationAndIndependentResults(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/listing/chains" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if body, err := io.ReadAll(r.Body); err != nil || len(body) != 0 {
			t.Errorf("GET body=%q err=%v", body, err)
		}
		assertRequestCredentials(t, r, "maker-api-key")
		writeJSON(w, chainsEnvelope())
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	if calls.Load() != 0 {
		t.Fatal("constructor made a request")
	}
	want := map[string]uint64{
		"ethereum": 1, "bsc": 56, "polygon": 137,
		"base": 8453, "arbitrum": 42161, "sepolia": 11155111,
	}
	for range 2 {
		chains, err := client.ListChains(context.Background())
		if err != nil || !reflect.DeepEqual(chains, want) {
			t.Fatalf("chains=%v err=%v", chains, err)
		}
		chains["bsc"] = 999
		delete(chains, "ethereum")
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d; queries must not retry or cache", calls.Load())
	}
}

func TestListChainsEmptyAndUpperBound(t *testing.T) {
	for _, want := range []map[string]uint64{{}, {"custom": math.MaxInt64}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{"success": true, "data": want})
		}))
		client := testClient(t, server, nil)
		chains, err := client.ListChains(context.Background())
		server.Close()
		if err != nil || chains == nil || !reflect.DeepEqual(chains, want) {
			t.Fatalf("chains=%v err=%v, want=%v", chains, err, want)
		}
	}
}

func TestListChainsRejectsMalformedData(t *testing.T) {
	for name, data := range map[string]string{
		"null": "null", "array": "[]", "string": `"chains"`, "number": "56",
		"null ID": `{"bsc":null}`, "zero ID": `{"bsc":0}`, "negative ID": `{"bsc":-1}`,
		"string ID": `{"bsc":"56"}`, "fractional ID": `{"bsc":56.1}`, "boolean ID": `{"bsc":true}`,
		"out of int64 range":  `{"bsc":9223372036854775808}`,
		"out of uint64 range": `{"bsc":18446744073709551616}`,
		"mixed invalid ID":    `{"ethereum":1,"bsc":"56"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"success":true,"data":`+data+`}`)
			}))
			defer server.Close()
			chains, err := testClient(t, server, nil).ListChains(context.Background())
			var responseErr *ResponseError
			if chains != nil || !errors.Is(err, ErrInvalidResponse) || !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusOK {
				t.Fatalf("malformed chain data accepted or HTTP status lost: chains=%v err=%v", chains, err)
			}
		})
	}
}

func TestListChainsRejectsNilContextBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	_, err := testClient(t, server, nil).ListChains(nil) //nolint:staticcheck // Verify that a nil context is rejected.
	if !errors.Is(err, ErrInvalidInput) || calls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestListChainsPreservesAPIErrorWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"success":false,"error":{"code":"UNAUTHORIZED","message":"invalid API key"}}`)
	}))
	defer server.Close()
	_, err := testClient(t, server, nil).ListChains(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "UNAUTHORIZED" || apiErr.Message != "invalid API key" || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}
