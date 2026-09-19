package listing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientRetainsBaseURLWithoutValidation(t *testing.T) {
	for _, baseURL := range []string{
		"/custom/api",
		"https://example.test",
		"custom://example.test/root",
		"https://user:pass@example.test/root?key=value#section",
		"https://example.test/../root//",
		"https://example.test/root?",
		"https://example.test/root#",
		"https://example.test/%invalid",
	} {
		t.Run(baseURL, func(t *testing.T) {
			client, err := NewClient(Config{BaseURL: baseURL, APIKey: "key"})
			if err != nil {
				t.Fatal(err)
			}
			if client.baseURL != baseURL {
				t.Fatalf("BaseURL changed: got %q, want %q", client.baseURL, baseURL)
			}
		})
	}
}

func TestRequestsPreserveConfiguredURL(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		endpoint string
		query    string
		fragment string
	}{
		{name: "host", baseURL: "https://example.test", endpoint: "https://example.test/"},
		{name: "custom path", baseURL: "https://example.test/custom/v2", endpoint: "https://example.test/custom/v2/"},
		{name: "trailing slash", baseURL: "https://example.test/custom/", endpoint: "https://example.test/custom/"},
		{name: "repeated slash", baseURL: "https://example.test/custom//", endpoint: "https://example.test/custom//"},
		{name: "dot path", baseURL: "https://example.test/proxy/../custom", endpoint: "https://example.test/proxy/../custom/"},
		{name: "escaped path", baseURL: "https://example.test/proxy%2fcustom", endpoint: "https://example.test/proxy%2fcustom/"},
		{name: "escaped trailing slash", baseURL: "https://example.test/custom%2f", endpoint: "https://example.test/custom%2f/"},
		{name: "custom transport scheme", baseURL: "custom://service/root", endpoint: "custom://service/root/"},
		{
			name:     "userinfo query fragment",
			baseURL:  "https://user:pass@example.test/custom?token=a%2fb&flag#section",
			endpoint: "https://user:pass@example.test/custom/",
			query:    "token=a%2fb&flag",
			fragment: "#section",
		},
	}
	operations := []struct {
		path  string
		query string
		body  any
		call  func(*Client) error
	}{
		{"chains", "", chainsEnvelope(), func(c *Client) error {
			_, err := c.ListChains(context.Background())
			return err
		}},
		{"pairs", "chain_id=56", listEnvelope(), func(c *Client) error {
			_, err := c.ListPairs(context.Background(), testChainID)
			return err
		}},
		{"pair", "chain_id=56&pair_id=7", detailEnvelope(), func(c *Client) error {
			_, err := c.ListPair(context.Background(), testChainID, testPairID)
			return err
		}},
	}
	for _, tc := range cases {
		for _, operation := range operations {
			t.Run(tc.name+"/"+operation.path, func(t *testing.T) {
				wantURL := tc.endpoint + operation.path
				query := tc.query
				if operation.query != "" {
					if query != "" {
						query += "&"
					}
					query += operation.query
				}
				if query != "" {
					wantURL += "?" + query
				}
				wantURL += tc.fragment
				data, err := json.Marshal(operation.body)
				if err != nil {
					t.Fatal(err)
				}
				client, err := NewClient(Config{
					BaseURL: tc.baseURL, APIKey: "key",
					HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if got := req.URL.String(); got != wantURL {
							t.Errorf("request URL = %q, want %q", got, wantURL)
						}
						assertRequestCredentials(t, req, "key")
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
					})},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := operation.call(client); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMalformedURLFailsWhenRequesting(t *testing.T) {
	client, err := NewClient(Config{
		BaseURL: "https://example.test/%invalid", APIKey: "key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("malformed URL reached transport")
			return nil, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListPairs(context.Background(), testChainID); err == nil {
		t.Fatal("expected request construction to reject malformed URL")
	}
}

func TestListChainOverridesConfiguredQuery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		baseQuery string
		wantQuery string
	}{
		{"plain key", "chain_id=1", "chain_id=56"},
		{"encoded key", "%63hain_id=1", "chain_id=56"},
		{"bare key", "chain_id&flag", "flag&chain_id=56"},
		{
			"duplicates and preserved encoding",
			"flag&chain_id=1&token=a%2fb&chain%5fid=2&chain_id=3&token=next+value",
			"flag&token=a%2fb&token=next+value&chain_id=56",
		},
		{"case-sensitive keys", "Chain_ID=1&chain_id=2", "Chain_ID=1&chain_id=56"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertRequestCredentials(t, r, "maker-api-key")
				if r.URL.Path == "/listing/pair" {
					if want := tc.wantQuery + "&pair_id=7"; r.URL.RawQuery != want {
						t.Errorf("detail query = %q, want %q", r.URL.RawQuery, want)
					}
					writeJSON(w, detailEnvelope())
					return
				}
				if r.URL.Path != "/listing/pairs" || r.URL.RawQuery != tc.wantQuery {
					t.Errorf("list query = %q, want %q", r.URL.RawQuery, tc.wantQuery)
				}
				// The backend selects the first value. An empty result has no record
				// identity to reveal that a conflicting BaseURL selected the wrong chain.
				if chain := r.URL.Query().Get("chain_id"); chain != "56" {
					t.Errorf("backend selected chain %q instead of the method argument", chain)
				}
				_, _ = io.WriteString(w, `{"success":true,"data":[]}`)
			}))
			defer server.Close()
			client := testClient(t, server, func(cfg *Config) {
				cfg.BaseURL += "?" + tc.baseQuery
			})
			pairs, err := client.ListPairs(context.Background(), testChainID)
			if err != nil || pairs == nil || len(pairs) != 0 {
				t.Fatalf("empty list: pairs=%v, err=%v", pairs, err)
			}
			if _, err := client.ListPair(context.Background(), testChainID, testPairID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDetailArgumentsOverrideConfiguredQuery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		baseQuery string
		wantQuery string
	}{
		{"plain keys", "chain_id=1&pair_id=2", "chain_id=56&pair_id=7"},
		{"encoded keys", "%63hain_id=1&%70air%5fid=2", "chain_id=56&pair_id=7"},
		{"bare keys", "chain_id&pair_id&flag", "flag&chain_id=56&pair_id=7"},
		{
			"duplicates and preserved encoding",
			"flag&pair_id=1&chain_id=1&token=a%2fb&chain%5fid=2&pair%5fid=3&token=next+value",
			"flag&token=a%2fb&token=next+value&chain_id=56&pair_id=7",
		},
		{"case-sensitive keys", "Pair_ID=2&Chain_ID=1&pair_id=3&chain_id=2", "Pair_ID=2&Chain_ID=1&chain_id=56&pair_id=7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/listing/pair" || r.URL.RawQuery != tc.wantQuery {
					t.Errorf("detail URL = %s, want query %q", r.URL, tc.wantQuery)
				}
				if chain, pair := r.URL.Query().Get("chain_id"), r.URL.Query().Get("pair_id"); chain != "56" || pair != "7" {
					t.Errorf("backend selected chain=%q pair=%q instead of method arguments", chain, pair)
				}
				assertRequestCredentials(t, r, "maker-api-key")
				writeJSON(w, detailEnvelope())
			}))
			defer server.Close()
			client := testClient(t, server, func(cfg *Config) { cfg.BaseURL += "?" + tc.baseQuery })
			if _, err := client.ListPair(context.Background(), testChainID, testPairID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
