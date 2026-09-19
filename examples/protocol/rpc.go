package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"strings"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type rpcConfig struct {
	ChainID      uint64 `json:"chain_id"`
	RPCURL       string `json:"rpc_url"`
	Protocol     string `json:"protocol"`
	Payer        string `json:"payer"`
	InputToken   string `json:"input_token"`
	PairID       uint32 `json:"pair_id"`
	Maker        string `json:"maker"`
	Recipient    string `json:"recipient"`
	IsBuy        *bool  `json:"is_buy"`
	AmountIn     string `json:"amount_in"`
	MinAmountOut string `json:"min_amount_out"`
	Method       string `json:"method"`
}

// runRPC reads configuration and simulates against existing on-chain quotes.
// The caller owns the context; this function owns and closes its RPC connection.
func runRPC(ctx context.Context, configPath string, output io.Writer) (err error) {
	file, err := os.Open(configPath)
	if err != nil {
		return err
	}
	cfg, err := readRPCConfig(file)
	err = errors.Join(err, file.Close())
	// Keep errors.Is/As available while preventing endpoint credentials from
	// appearing in the error printed by main, including nested transport errors.
	defer func() {
		if err != nil {
			err = &rpcExampleError{cause: err, message: cfg.redact(err.Error())}
		}
	}()
	if err != nil {
		return err
	}
	params, err := cfg.swapParams()
	if err != nil {
		return err
	}

	// 1. The caller selects the RPC and deployment; NewClient verifies the chain.
	rpcClient, err := ethclient.DialContext(ctx, cfg.RPCURL)
	if err != nil {
		return fmt.Errorf("connect RPC: %w", err)
	}
	defer rpcClient.Close()
	client, err := protocol.NewClient(ctx, rpcClient, protocol.Config{
		ChainID: cfg.ChainID,
		Proxy:   common.HexToAddress(cfg.Protocol),
	})
	if err != nil {
		return err
	}

	// 2. Pin decay configuration and simulation to the same observed block number.
	head, err := rpcClient.HeaderByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("read latest block: %w", err)
	}
	if head == nil || head.Number == nil {
		return errors.New("RPC returned a block without a number")
	}
	decay, err := client.GlobalDecayConfig(ctx, head.Number)
	if err != nil {
		return fmt.Errorf("read global decay configuration: %w", err)
	}
	payer := common.HexToAddress(cfg.Payer)
	// Allowance's SDK API reads latest, so this observation may come from a
	// different block. It is informational and does not gate the simulation.
	allowance, err := client.Allowance(ctx, common.HexToAddress(cfg.InputToken), payer)
	if err != nil {
		return fmt.Errorf("read latest allowance: %w", err)
	}

	// 3. Use the payer's real balance/allowance and existing on-chain prices.
	// PriceUpdates is empty. SimulateSwap already returns output net of decay
	// and output fees; applying either deduction again would be incorrect.
	method := protocol.SwapMethod(cfg.Method)
	result, err := client.SimulateSwap(ctx, payer, method, params, head.Number)
	if err != nil {
		return fmt.Errorf("simulate %s: %w", method, err)
	}

	_, err = fmt.Fprintf(output,
		"block=%s decay_start=%ds decay_end=%ds max_age=%ds max_decay=%d ppm\n"+
			"allowance (latest)=%s input token native units\n"+
			"%s simulated: spent=%s net_output=%s (token native units)\n",
		head.Number, decay.DecayStartOffsetSeconds, decay.DecayEndOffsetSeconds, decay.MaxAge, decay.MaxDecayPpm,
		allowance, method, result.ActualAmountIn, result.AmountOut)
	return err
}

func readRPCConfig(reader io.Reader) (rpcConfig, error) {
	var cfg *rpcConfig
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&cfg)
	if cfg == nil {
		return rpcConfig{}, errors.New("protocol config must be a JSON object")
	}
	if err != nil {
		return *cfg, fmt.Errorf("decode protocol config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return *cfg, errors.New("protocol config must contain exactly one JSON object")
	}
	if cfg.Method == "" {
		cfg.Method = string(protocol.MethodSwap)
	}
	return *cfg, nil
}

func (c rpcConfig) swapParams() (protocol.SwapParams, error) {
	endpoint, err := url.Parse(c.RPCURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.Fragment != "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https" && endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
		// URL parser errors include the input, which can contain credentials.
		return protocol.SwapParams{}, errors.New("rpc_url must be an HTTP(S) or WS(S) URL without a fragment")
	}
	if c.ChainID == 0 || c.PairID == 0 {
		return protocol.SwapParams{}, errors.New("chain_id and pair_id must be positive")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "protocol", value: c.Protocol},
		{name: "payer", value: c.Payer},
		{name: "input_token", value: c.InputToken},
		{name: "maker", value: c.Maker},
		{name: "recipient", value: c.Recipient},
	} {
		if !common.IsHexAddress(field.value) || common.HexToAddress(field.value) == (common.Address{}) {
			return protocol.SwapParams{}, fmt.Errorf("%s must be a nonzero EVM address", field.name)
		}
	}
	if c.IsBuy == nil {
		return protocol.SwapParams{}, errors.New("is_buy must be explicitly set to true or false")
	}
	if c.Method != string(protocol.MethodSwap) && c.Method != string(protocol.MethodSwapExactIn) {
		return protocol.SwapParams{}, errors.New("method must be swap or swapExactIn")
	}
	amount, err := positiveUint256("amount_in", c.AmountIn)
	if err != nil {
		return protocol.SwapParams{}, err
	}
	minimum, err := positiveUint256("min_amount_out", c.MinAmountOut)
	if err != nil {
		return protocol.SwapParams{}, err
	}
	return protocol.SwapParams{
		PairID: c.PairID, IsBuy: *c.IsBuy, AmountIn: amount, MinAmountOut: minimum,
		Maker: common.HexToAddress(c.Maker), Recipient: common.HexToAddress(c.Recipient),
	}, nil
}

func positiveUint256(field, text string) (*big.Int, error) {
	if text != "" && strings.IndexFunc(text, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		if value, ok := new(big.Int).SetString(text, 10); ok && value.Sign() > 0 && value.BitLen() <= 256 {
			return value, nil
		}
	}
	return nil, fmt.Errorf("%s must be a positive decimal uint256 string in token native units", field)
}

type rpcExampleError struct {
	cause   error
	message string
}

func (e *rpcExampleError) Error() string { return e.message }

func (e *rpcExampleError) Unwrap() error { return e.cause }

func (c rpcConfig) redact(message string) string {
	secrets := []string{c.RPCURL}
	if endpoint, err := url.Parse(c.RPCURL); err == nil {
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
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}
