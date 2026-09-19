package listing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func assertRequestCredentials(t *testing.T, r *http.Request, apiKey string) {
	t.Helper()
	if values := r.Header.Values("X-API-Key"); len(values) != 1 || values[0] != apiKey {
		t.Error("request must contain exactly the configured API key")
	}
	if strings.Contains(r.URL.String(), apiKey) {
		t.Error("API key was included in the request URL")
	}
}

func emptyListResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":[]}`)),
		Header:     make(http.Header),
	}
}

func TestNewClientPreservesAPIKeyWithoutValidation(t *testing.T) {
	for _, tc := range []struct{ name, apiKey string }{
		{"empty API key", ""},
		{"whitespace", " my credential "},
		{"control character", "my\tcredential"},
		{"newline", "my\r\ncredential"},
		{"non-ASCII", "my\u00e9credential"},
		{"DEL", "credential\x7f"},
		{"arbitrary API key", "a_b.c-d/+="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(Config{APIKey: tc.apiKey})
			if err != nil {
				t.Fatalf("constructor rejected API key: %v", err)
			}
			if client.credentials.apiKey != tc.apiKey {
				t.Fatal("constructor changed the API key")
			}
		})
	}
}

func TestCredentialsRemainFixedAfterConstruction(t *testing.T) {
	var calls int
	cfg := Config{
		BaseURL: "https://api.example/listing", APIKey: "original-api-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			assertRequestCredentials(t, r, "original-api-key")
			calls++
			var body any
			switch r.URL.Path {
			case "/listing/chains":
				body = chainsEnvelope()
			case "/listing/pairs":
				body = listEnvelope()
			case "/listing/pair":
				body = detailEnvelope()
			default:
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
		})},
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "different-api-key"
	for range 2 {
		if _, err := client.ListChains(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListPairs(context.Background(), testChainID); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListPair(context.Background(), testChainID, testPairID); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 6 {
		t.Fatalf("requests=%d, want 6", calls)
	}
}
