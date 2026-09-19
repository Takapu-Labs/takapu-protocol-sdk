package listing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

const (
	testChainID = uint64(56)
	testPairID  = uint32(7)
	baseAddr    = "0x1111111111111111111111111111111111111111"
	quoteAddr   = "0x2222222222222222222222222222222222222222"
	zeroAddr    = "0x0000000000000000000000000000000000000000"
)

func pairObject() map[string]any {
	return map[string]any{
		"chain_id": testChainID, "pair_id": testPairID,
		"base_token": baseAddr, "quote_token": quoteAddr,
		"base_symbol": "BASE", "quote_symbol": "QUOTE", "base_decimals": 8, "quote_decimals": 18,
		"price_tick_size": "100000000000000000000000000", "lot_size": "10000",
		"tax": nil, "active": true, "gray": false,
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func detailEnvelope() map[string]any {
	return map[string]any{"success": true, "data": pairObject()}
}

func listEnvelope() map[string]any {
	pair := pairObject()
	pair["tax"] = taxObject()
	return map[string]any{"success": true, "data": []any{pair}}
}

func testClient(t *testing.T, server *httptest.Server, change func(*Config)) *Client {
	t.Helper()
	cfg := Config{BaseURL: server.URL + "/listing", APIKey: "maker-api-key"}
	if change != nil {
		change(&cfg)
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListPairsAuthenticationAndChain(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != "GET" || r.URL.Path != "/listing/pairs" || r.URL.RawQuery != "chain_id=56" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("GET body=%q err=%v", body, err)
		}
		assertRequestCredentials(t, r, "maker-api-key")
		writeJSON(w, listEnvelope())
	}))
	defer srv.Close()
	c := testClient(t, srv, nil)
	if count.Load() != 0 {
		t.Fatal("constructor made a request")
	}
	for range 2 {
		pairs, err := c.ListPairs(context.Background(), testChainID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pairs) != 1 {
			t.Fatalf("bad list: %+v", pairs)
		}
		p := pairs[0]
		if p.ChainID != testChainID || p.PairID != testPairID || p.BaseToken != common.HexToAddress(baseAddr) || p.BaseDecimals != 8 || p.QuoteDecimals != 18 || p.Tax == nil || p.Tax.RatePPM != 1000 {
			t.Fatalf("bad pair: %+v", p)
		}
		if p.PriceTickSize != "100000000000000000000000000" || p.LotSize != "10000" {
			t.Fatal("raw integer strings changed")
		}
	}
	if count.Load() != 2 {
		t.Fatalf("requests=%d; queries must not retry or cache", count.Load())
	}
}

func TestDefaultsAndEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "chain_id=56" {
			t.Errorf("query=%q", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv, nil)
	if c.timeout != 10*time.Second {
		t.Fatalf("timeout=%v", c.timeout)
	}
	pairs, err := c.ListPairs(context.Background(), testChainID)
	if err != nil || pairs == nil || len(pairs) != 0 {
		t.Fatalf("pairs=%+v err=%v", pairs, err)
	}
}

func TestMalformedBusinessFailurePreservesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":false,"error":{"code":"PAIR_NOT_FOUND","message":"not here"}} trailing garbage`)
	}))
	defer srv.Close()
	_, err := testClient(t, srv, nil).ListPair(context.Background(), testChainID, testPairID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 200 || apiErr.Code != "PAIR_NOT_FOUND" || apiErr.Message != "not here" || apiErr.Cause == nil {
		t.Fatalf("business error metadata or parse cause lost: %v", err)
	}
}

func TestListPairPreservesTaxAndUnknownFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/listing/pair" || r.URL.RawQuery != "chain_id=56&pair_id=7" {
			t.Errorf("detail URL=%s", r.URL)
		}
		assertRequestCredentials(t, r, "maker-api-key")
		p := pairObject()
		p["active"], p["gray"] = false, true
		p["tax"] = taxObject()
		p["new_server_field"] = "ignored"
		writeJSON(w, map[string]any{"success": true, "data": p})
	}))
	defer srv.Close()
	p, err := testClient(t, srv, nil).ListPair(context.Background(), testChainID, testPairID)
	if err != nil {
		t.Fatal(err)
	}
	if p.PairID != testPairID || p.Active || !p.Gray || p.Tax == nil || p.Tax.RatePPM != 1000 || p.Tax.Token != common.HexToAddress(quoteAddr) || p.Tax.Symbol != "QUOTE" {
		t.Fatalf("bad pair: %+v", p)
	}
}

func TestInputValidationDoesNotSend(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := testClient(t, srv, nil)
	for _, chainID := range []uint64{0, math.MaxInt64 + 1} {
		if _, err := c.ListPairs(context.Background(), chainID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("chain=%d err=%v", chainID, err)
		}
		if _, err := c.ListPair(context.Background(), chainID, testPairID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("chain=%d err=%v", chainID, err)
		}
	}
	for _, pairID := range []uint32{0, math.MaxInt32 + 1} {
		if _, err := c.ListPair(context.Background(), testChainID, pairID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("pair=%d err=%v", pairID, err)
		}
	}
	if _, err := c.ListPair(nil, testChainID, testPairID); !errors.Is(err, ErrInvalidInput) { //nolint:staticcheck // Verify that a nil context is rejected.
		t.Errorf("nil ctx err=%v", err)
	}
	if _, err := c.ListPairs(nil, testChainID); !errors.Is(err, ErrInvalidInput) { //nolint:staticcheck // Verify that a nil context is rejected.
		t.Errorf("nil ctx err=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests sent: %d", calls.Load())
	}
}

func TestConfigurationValidation(t *testing.T) {
	valid := Config{BaseURL: "https://api.example/listing", APIKey: "key"}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"timeout", func(c *Config) { c.Timeout = -time.Second }},
		{"HTTP timeout", func(c *Config) { c.HTTPClient = &http.Client{Timeout: -time.Second} }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			if _, err := NewClient(cfg); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestClientNeedsOnlyAPIKey(t *testing.T) {
	for _, timeout := range []time.Duration{0, 3 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			client, err := NewClient(Config{APIKey: "key", Timeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			if client.baseURL != "https://api.takapu.org/listing" {
				t.Fatalf("default BaseURL = %q", client.baseURL)
			}
			wantTimeout := timeout
			if wantTimeout == 0 {
				wantTimeout = 10 * time.Second
			}
			if client.timeout != wantTimeout || client.http.Timeout != wantTimeout {
				t.Fatalf("SDK timeout=%s HTTP timeout=%s, want %s", client.timeout, client.http.Timeout, wantTimeout)
			}
			if client.http.CheckRedirect == nil || client.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
				t.Fatal("default HTTP client must not follow redirects")
			}
		})
	}
}

func TestDefaultBaseURLUsesInjectedHTTPClient(t *testing.T) {
	var calls int
	httpClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if got := r.URL.String(); got != "https://api.takapu.org/listing/pairs?chain_id=56" {
				t.Errorf("default request URL = %q", got)
			}
			assertRequestCredentials(t, r, "key")
			return emptyListResponse(), nil
		}),
	}
	client, err := NewClient(Config{APIKey: "key", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("constructor made an HTTP request")
	}
	if _, err := client.ListPairs(context.Background(), testChainID); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || client.http.Timeout != httpClient.Timeout || httpClient.CheckRedirect != nil {
		t.Fatalf("injected client configuration changed: calls=%d timeout=%s", calls, client.http.Timeout)
	}
}

func TestAPIErrorMetadataAndNoAutomaticRetry(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"success":false,"error":{"code":"PAIR_NOT_FOUND","message":"not here"}}`)
			}))
			defer srv.Close()
			_, err := testClient(t, srv, nil).ListPair(context.Background(), testChainID, testPairID)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.Code != "PAIR_NOT_FOUND" || apiErr.Message != "not here" {
				t.Fatalf("err=%#v", err)
			}
			if count.Load() != 1 {
				t.Fatalf("automatic retries: %d", count.Load())
			}
		})
	}
	for _, status := range []int{301, 404, 502} {
		t.Run("non-json-"+fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "<html>error</html>")
			}))
			defer srv.Close()
			_, err := testClient(t, srv, nil).ListPair(context.Background(), testChainID, testPairID)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.Cause == nil {
				t.Fatalf("err=%#v", err)
			}
		})
	}
}

func TestMalformedResponsesAreErrors(t *testing.T) {
	cases := []string{
		``, `not json`, `{}`, `null`, `[]`, `{"success":null,"data":[]}`, `{"success":true}`, `{"success":true,"data":null}`,
		`{"success":true,"data":[],"error":{"code":"bad","message":"bad"}}`,
		`{"success":true,"data":[]} {}`,
		`{"success":true,"data":{}}`,
	}
	for i, body := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			_, err := testClient(t, srv, nil).ListPairs(context.Background(), testChainID)
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("body=%s err=%v", body, err)
			}
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) || responseErr.StatusCode != 200 {
				t.Fatalf("status lost: %v", err)
			}
		})
	}
}

func TestRedirectsNeverForwardAuthentication(t *testing.T) {
	var targetCalls, policyCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		writeJSON(w, detailEnvelope())
	}))
	defer target.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, code) }))
			defer srv.Close()
			hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				policyCalls.Add(1)
				return nil
			}}
			c := testClient(t, srv, func(cfg *Config) { cfg.HTTPClient = hc })
			_, err := c.ListPair(context.Background(), testChainID, testPairID)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != code {
				t.Fatalf("err=%v", err)
			}
			if err := hc.CheckRedirect(nil, nil); err != nil {
				t.Fatal("caller redirect callback was modified")
			}
		})
	}
	if targetCalls.Load() != 0 || policyCalls.Load() != 5 {
		t.Fatalf("target=%d callback=%d", targetCalls.Load(), policyCalls.Load())
	}
}

func TestEarliestDeadlineAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	for _, kind := range []string{"call", "sdk", "http"} {
		t.Run(kind, func(t *testing.T) {
			cfgTimeout, httpTimeout, callTimeout := 2*time.Second, 2*time.Second, 2*time.Second
			switch kind {
			case "call":
				callTimeout = 35 * time.Millisecond
			case "sdk":
				cfgTimeout = 35 * time.Millisecond
			case "http":
				httpTimeout = 35 * time.Millisecond
			}
			c := testClient(t, srv, func(cfg *Config) {
				cfg.Timeout = cfgTimeout
				cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
			})
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			start := time.Now()
			_, err := c.ListPair(ctx, testChainID, testPairID)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("earlier timeout ignored: %v", elapsed)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testClient(t, srv, nil).ListPair(ctx, testChainID, testPairID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestConcurrentQueriesAndIndependentResults(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assertRequestCredentials(t, r, "maker-api-key")
		pair := pairObject()
		pair["tax"] = taxObject()
		writeJSON(w, map[string]any{"success": true, "data": pair})
	}))
	defer srv.Close()
	c := testClient(t, srv, nil)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.ListPair(context.Background(), testChainID, testPairID)
			if err != nil {
				t.Error(err)
				return
			}
			if p.PairID != testPairID || p.Tax == nil || p.Tax.RatePPM != 1000 {
				t.Errorf("modified pair leaked: %+v", p)
				return
			}
			p.Tax.RatePPM = 123
		}()
	}
	wg.Wait()
	if calls.Load() != 40 {
		t.Fatalf("requests=%d", calls.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNetworkErrorIsPreserved(t *testing.T) {
	want := errors.New("transport unavailable")
	c, err := NewClient(Config{
		BaseURL: "https://api.example/listing", APIKey: "id",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, want })},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListPairs(context.Background(), testChainID)
	if !errors.Is(err, want) {
		t.Fatalf("network cause lost: %v", err)
	}
}

func TestListPreservesOrderAndIndependentTax(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first, second := pairObject(), pairObject()
		first["pair_id"], second["pair_id"] = 9, 3
		first["tax"], second["tax"] = taxObject(), taxObject()
		second["gray"] = true
		writeJSON(w, map[string]any{"success": true, "data": []any{first, second}})
	}))
	defer server.Close()
	pairs, err := testClient(t, server, nil).ListPairs(context.Background(), testChainID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].PairID != 9 || pairs[1].PairID != 3 {
		t.Fatalf("list filtered or reordered: %+v", pairs)
	}
	if !pairs[1].Active || !pairs[1].Gray {
		t.Fatalf("pair flags changed: %+v", pairs[1])
	}
	if pairs[0].Tax == nil || pairs[1].Tax == nil {
		t.Fatal("tax configuration was lost")
	}
	pairs[0].Tax.RatePPM = 12
	if pairs[1].Tax.RatePPM != 1000 {
		t.Fatal("mutating one record changed another record's tax")
	}
}

func TestIdentifierUpperBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pair := pairObject()
		pair["chain_id"], pair["pair_id"] = uint64(math.MaxInt64), uint32(math.MaxInt32)
		pair["tax"] = taxObject()
		switch r.URL.RequestURI() {
		case "/listing/pairs?chain_id=9223372036854775807":
			writeJSON(w, map[string]any{"success": true, "data": []any{pair}})
		case "/listing/pair?chain_id=9223372036854775807&pair_id=2147483647":
			writeJSON(w, map[string]any{"success": true, "data": pair})
		default:
			t.Errorf("incorrect ID encoding: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	if _, err := client.ListPairs(context.Background(), math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListPair(context.Background(), math.MaxInt64, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
}
