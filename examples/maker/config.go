package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type config struct {
	ChainID      uint64 `json:"chain_id"`
	RPCURL       string `json:"rpc_url"`
	APIKey       string `json:"api_key"`
	PrivateKey   string `json:"frame_private_key"`
	TxPrivateKey string `json:"tx_private_key,omitempty"`
	Signer       string `json:"frame_signer"`
	Protocol     string `json:"protocol"`
	Maker        string `json:"maker"`
	Pair         struct {
		ID            uint32 `json:"pair_id"`
		BaseToken     string `json:"base_token"`
		QuoteToken    string `json:"quote_token"`
		BaseDecimals  uint8  `json:"base_decimals"`
		QuoteDecimals uint8  `json:"quote_decimals"`
		PriceTickSize string `json:"price_tick_size"`
		LotSize       string `json:"lot_size"`
	} `json:"pair"`
	Quote struct {
		Bids []maker.PriceLevel `json:"bids"`
		Asks []maker.PriceLevel `json:"asks"`
	} `json:"quote"`
	NextVersion struct {
		Major uint32 `json:"major"`
		Minor uint16 `json:"minor"` // 256 means exhausted; never wrap to zero.
	} `json:"next_version"`
}

type localSigner struct{ key *ecdsa.PrivateKey }

func (s localSigner) Address() common.Address { return crypto.PubkeyToAddress(s.key.PublicKey) }
func (s localSigner) SignDigest(ctx context.Context, digest common.Hash) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return crypto.Sign(digest.Bytes(), s.key)
}

// atomicJSON persists a private configuration file and its replacement directory
// entry before allowing the reserved version to send.
func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Syncing the temporary file alone does not persist the replacement name.
	return directory.Sync()
}

func (c config) redact(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	secrets := []string{c.RPCURL}
	if endpoint, err := url.Parse(c.RPCURL); err == nil {
		// Transport errors may quote a sanitized URL or individual credentials.
		secrets = append(secrets, endpoint.Redacted())
		if endpoint.User != nil {
			password, _ := endpoint.User.Password()
			secrets = append(secrets, endpoint.User.String(), endpoint.User.Username(), password)
		}
		for _, values := range endpoint.Query() {
			for _, value := range values {
				secrets = append(secrets, value, url.QueryEscape(value))
			}
		}
	}
	secrets = append(secrets, c.PrivateKey, strings.TrimPrefix(c.PrivateKey, "0x"), c.TxPrivateKey, strings.TrimPrefix(c.TxPrivateKey, "0x"), c.APIKey)
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func (c config) validate(onchain bool) error {
	if c.ChainID == 0 {
		return errors.New("chain_id must be positive")
	}
	for name, address := range map[string]string{"protocol": c.Protocol, "maker": c.Maker, "frame_signer": c.Signer, "base_token": c.Pair.BaseToken, "quote_token": c.Pair.QuoteToken} {
		if !common.IsHexAddress(address) || common.HexToAddress(address) == (common.Address{}) {
			return fmt.Errorf("invalid %s address", name)
		}
	}
	if c.Pair.ID == 0 || c.RPCURL == "" {
		return errors.New("pair_id and RPC URL are required")
	}
	if !onchain && c.APIKey == "" {
		return errors.New("api_key is required for WebSocket publishing")
	}
	if len(c.Quote.Bids) == 0 || len(c.Quote.Asks) == 0 {
		return errors.New("this example requires at least one bid and one ask")
	}
	return nil
}

// readConfig accepts one configuration object so an ignored second object cannot
// accidentally hide credentials or version state from a subsequent rewrite.
func readConfig(path string) (config, error) {
	var cfg config
	file, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("configuration must contain exactly one JSON object")
	}
	return cfg, nil
}

// lockConfig serializes processes sharing a configuration and its version state.
// The caller must hold the lock until publishing and connection cleanup finish.
func lockConfig(path string) (func(), error) {
	lock, err := os.OpenFile(path+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire config lock (another run or stale .lock file): %w", err)
	}
	_, writeErr := fmt.Fprintln(lock, os.Getpid())
	closeErr := lock.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		os.Remove(path + ".lock")
		return nil, err
	}
	return func() { os.Remove(path + ".lock") }, nil
}

// reserveVersion saves the next version before a network write is allowed.
// A successful reservation is never rolled back, even when submission fails or
// NoSend signs a transaction without broadcasting it.
// The caller owns the config lock; another process must not use a copied config
// for the same maker/pair, because the lock only protects this path.
func (c *config) reserveVersion(path string) error {
	if c.NextVersion.Minor > 255 {
		return errors.New("minor version exhausted; select the next major explicitly according to the inventory strategy")
	}
	minor := c.NextVersion.Minor
	c.NextVersion.Minor++
	if err := atomicJSON(path, c); err != nil {
		c.NextVersion.Minor = minor
		return fmt.Errorf("save next version before publish: %w", err)
	}
	return nil
}
