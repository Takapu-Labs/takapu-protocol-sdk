package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

type config struct {
	ChainID uint64 `json:"chain_id"` // Pair queries require one chain ID from ListChains.
	APIKey  string `json:"api_key"`
}

func loadConfig(path string) (config, error) {
	var cfg config
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("read configuration: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return cfg, errors.New("configuration must contain exactly one JSON object")
	}
	if cfg.ChainID == 0 || cfg.ChainID > math.MaxInt64 {
		return cfg, errors.New("chain_id must be a positive int64")
	}
	return cfg, nil
}

func (c config) redact(message string) string {
	if c.APIKey != "" {
		message = strings.ReplaceAll(message, c.APIKey, "[redacted]")
	}
	return message
}

// Redact string values before encoding, preserving JSON keys and numeric values
// even when a valid short credential happens to look like a number or field name.
func (c config) marshalRedacted(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var redactValue func(any) any
	redactValue = func(value any) any {
		switch value := value.(type) {
		case string:
			return c.redact(value)
		case []any:
			for i := range value {
				value[i] = redactValue(value[i])
			}
		case map[string]any:
			for key := range value {
				value[key] = redactValue(value[key])
			}
		}
		return value
	}
	return json.MarshalIndent(redactValue(value), "", "  ")
}
