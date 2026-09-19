package listing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func taxObject() map[string]any {
	return map[string]any{"tax_token": quoteAddr, "tax_token_symbol": "QUOTE", "tax_rate_ppm": 1000}
}

func TestPairConfigurationValidation(t *testing.T) {
	cases := []struct {
		field string
		value any
	}{
		{"chain_id", 0},
		{"chain_id", -1},
		{"chain_id", json.Number("9223372036854775808")},
		{"pair_id", -1},
		{"pair_id", json.Number("2147483648")},
		{"pair_id", "7"},
		{"pair_id", 1.5},
		{"base_token", "0x123"},
		{"quote_token", "invalid"},
		{"base_token", zeroAddr},
		{"quote_token", baseAddr},
		{"base_decimals", -1},
		{"base_decimals", 256},
		{"quote_decimals", "18"},
		{"quote_decimals", 1.5},
		{"price_tick_size", ""},
		{"price_tick_size", "0"},
		{"price_tick_size", "0.000"},
		{"price_tick_size", "0.01"},
		{"price_tick_size", "1.0"},
		{"price_tick_size", ".001"},
		{"price_tick_size", "1."},
		{"price_tick_size", "1.2.3"},
		{"price_tick_size", "NaN"},
		{"price_tick_size", "1/1000"},
		{"price_tick_size", 0.001},
		{"price_tick_size", "-1"},
		{"price_tick_size", "+1"},
		{"price_tick_size", "1e3"},
		{"price_tick_size", " 1"},
		{"price_tick_size", "340282366920938463463374607431768211456"},
		{"price_tick_size", strings.Repeat("9", 100_000)},
		{"lot_size", "0"},
		{"lot_size", "0.0001"},
		{"lot_size", "340282366920938463463374607431768211456"},
		{"lot_size", 1},
		{"tax", "invalid"},
		{"tax", []any{}},
		{"tax", map[string]any{}},
		{"active", "true"},
		{"gray", 0},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.field, i), func(t *testing.T) {
			p := pairObject()
			p[tc.field] = tc.value
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodePair(data); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("%s=%v err=%v", tc.field, tc.value, err)
			}
		})
	}
	for field := range pairObject() {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("required/%s/missing=%v", field, missing), func(t *testing.T) {
				p := pairObject()
				if missing {
					delete(p, field)
				} else {
					p[field] = nil
				}
				data, _ := json.Marshal(p)
				_, err := decodePair(data)
				if !missing && field == "tax" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if !errors.Is(err, ErrInvalidConfiguration) {
					t.Fatalf("field=%s missing=%v err=%v", field, missing, err)
				}
			})
		}
	}
}

func TestTaxConfigurationValidation(t *testing.T) {
	cases := []struct {
		field string
		value any
	}{
		{"tax_token", "invalid"},
		{"tax_token", zeroAddr},
		{"tax_token", "0x3333333333333333333333333333333333333333"},
		{"tax_token_symbol", 1},
		{"tax_rate_ppm", -1},
		{"tax_rate_ppm", 1_000_000},
		{"tax_rate_ppm", 1.5},
		{"tax_rate_ppm", "1000"},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.field, i), func(t *testing.T) {
			tax := taxObject()
			tax[tc.field] = tc.value
			p := pairObject()
			p["tax"] = tax
			data, _ := json.Marshal(p)
			_, err := decodePair(data)
			var configErr *ConfigurationError
			if !errors.As(err, &configErr) || configErr.Field != "tax."+tc.field {
				t.Fatalf("tax field path lost: %v", err)
			}
		})
	}
	for field := range taxObject() {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("required/%s/missing=%v", field, missing), func(t *testing.T) {
				tax := taxObject()
				if missing {
					delete(tax, field)
				} else {
					tax[field] = nil
				}
				p := pairObject()
				p["tax"] = tax
				data, _ := json.Marshal(p)
				_, err := decodePair(data)
				var configErr *ConfigurationError
				if !errors.As(err, &configErr) || configErr.Field != "tax."+field {
					t.Fatalf("err=%v", err)
				}
			})
		}
	}
	for _, token := range []string{baseAddr, quoteAddr} {
		for _, rate := range []uint32{0, 999_999} {
			p := pairObject()
			p["tax"] = map[string]any{"tax_token": token, "tax_token_symbol": "", "tax_rate_ppm": rate}
			data, _ := json.Marshal(p)
			got, err := decodePair(data)
			if err != nil || got.Tax == nil || got.Tax.RatePPM != rate {
				t.Fatalf("valid tax rejected: %v", err)
			}
		}
	}
}

func TestReturnedFieldsPreservePrecisionAndIdentity(t *testing.T) {
	for _, pairID := range []uint32{0, math.MaxInt32} {
		p := pairObject()
		p["pair_id"] = pairID
		p["chain_id"], p["base_decimals"], p["quote_decimals"] = json.Number("9223372036854775807"), 0, 255
		p["price_tick_size"], p["lot_size"] = "0001", "340282366920938463463374607431768211455"
		data, _ := json.Marshal(p)
		got, err := decodePair(data)
		if err != nil {
			t.Fatal(err)
		}
		if got.PairID != pairID || got.ChainID != math.MaxInt64 || got.PriceTickSize != "0001" || got.QuoteDecimals != 255 {
			t.Fatalf("lost identity/precision: %+v", got)
		}
	}
}

func TestRawIntegerStringsPreservePrecision(t *testing.T) {
	for _, value := range []string{
		"1",
		"0001",
		"10000",
		"100000000000000000000000000",
		"340282366920938463463374607431768211455",
	} {
		p := pairObject()
		p["price_tick_size"], p["lot_size"] = value, value
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodePair(data)
		if err != nil {
			t.Fatal(err)
		}
		if got.PriceTickSize != value || got.LotSize != value {
			t.Fatal("raw integer strings were rounded or reformatted")
		}
	}
}

func TestTaxRequiredOnBothEndpoints(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(fmt.Sprintf("detail=%v", detail), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := pairObject()
				delete(p, "tax")
				if detail {
					writeJSON(w, map[string]any{"success": true, "data": p})
				} else {
					writeJSON(w, map[string]any{"success": true, "data": []any{p}})
				}
			}))
			defer srv.Close()
			c := testClient(t, srv, nil)
			var err error
			if detail {
				_, err = c.ListPair(context.Background(), testChainID, testPairID)
			} else {
				_, err = c.ListPairs(context.Background(), testChainID)
			}
			var configErr *ConfigurationError
			wantField := "data[0].tax"
			if detail {
				wantField = "data.tax"
			}
			if !errors.As(err, &configErr) || configErr.StatusCode != 200 || configErr.Field != wantField {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReturnedIdentityMustMatchRequest(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, field := range []string{"chain_id", "pair_id"} {
			if !detail && field == "pair_id" {
				continue
			}
			t.Run(fmt.Sprintf("detail=%v/%s", detail, field), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					p := pairObject()
					p[field] = 123
					if detail {
						writeJSON(w, map[string]any{"success": true, "data": p})
					} else {
						// A mismatched record must reject the whole result, including preceding valid records.
						writeJSON(w, map[string]any{"success": true, "data": []any{pairObject(), p}})
					}
				}))
				defer srv.Close()
				c := testClient(t, srv, nil)
				var err error
				if detail {
					_, err = c.ListPair(context.Background(), testChainID, testPairID)
				} else {
					var pairs []Pair
					pairs, err = c.ListPairs(context.Background(), testChainID)
					if pairs != nil {
						t.Fatalf("partial invalid result returned: %+v", pairs)
					}
				}
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("mismatched identity accepted: %v", err)
				}
			})
		}
	}
}
