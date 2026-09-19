// maker signs and publishes a bounded set of quotes with the SDK.
// By default it publishes through marketstream; -onchain submits a transaction.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	configPath := flag.String("config", "config.local.json", "JSON configuration; next_version is saved here before every send")
	count := flag.Int("count", 1, "number of frames to send (1-60)")
	interval := flag.Duration("interval", 2*time.Second, "wait between frames")
	observe := flag.Duration("observe", 3*time.Second, "wait for rejection events after the final send")
	timeout := flag.Duration("timeout", 3*time.Minute, "total RPC/WebSocket timeout")
	loadOnly := flag.Bool("load-only", false, "load chain state and balances without connecting or publishing")
	onchain := flag.Bool("onchain", false, "submit frames directly on chain, without marketstream or API credentials")
	noSend := flag.Bool("no-send", false, "with -onchain, sign transactions without broadcasting; versions are still reserved")
	flag.Parse()
	if *noSend && !*onchain {
		fmt.Fprintln(os.Stderr, "-no-send requires -onchain")
		os.Exit(1)
	}
	if *count < 1 || *count > 60 || *interval <= 0 || *observe <= 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "count must be 1-60; durations must be positive")
		os.Exit(1)
	}
	var cfg config
	err := func() error {
		path, err := filepath.Abs(*configPath)
		if err != nil {
			return err
		}
		unlock, err := lockConfig(path)
		if err != nil {
			return err
		}
		defer unlock()
		cfg, err = readConfig(path)
		if err != nil {
			return err
		}
		if err := cfg.validate(*onchain); err != nil {
			return err
		}
		if !*loadOnly && int(cfg.NextVersion.Minor)+*count > 256 {
			return errors.New("minor version would exceed 255; select the next major explicitly according to the inventory strategy")
		}
		key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.PrivateKey, "0x"))
		if err != nil {
			return errors.New("invalid frame_private_key")
		}
		signer := localSigner{key: key}
		if signer.Address() != common.HexToAddress(cfg.Signer) {
			return errors.New("private key does not match frame_signer")
		}
		cfg.logf("start", "Maker quote example; local time=%s", time.Now().Format(time.RFC3339))
		cfg.logf("config", "file=%s; chain=%d; pair=%d", path, cfg.ChainID, cfg.Pair.ID)
		cfg.logf("config", "Maker=%s; frameSigner=%s; Protocol=%s", cfg.Maker, cfg.Signer, cfg.Protocol)
		cfg.logf("config", "RPC host=%s; private keys and API credentials are never printed", endpointHost(cfg.RPCURL))
		if !*onchain {
			cfg.logf("config", "Default Maker host=%s", endpointHost(maker.DefaultMakerURL))
		}
		if *loadOnly {
			cfg.logf("plan", "Load on-chain state and balances only; no WebSocket connection or version advancement")
		} else {
			cfg.logf("plan", "Prepare %d frames, versions (%d,%d) -> (%d,%d), interval %s, total timeout %s", *count, cfg.NextVersion.Major, cfg.NextVersion.Minor, cfg.NextVersion.Major, int(cfg.NextVersion.Minor)+*count-1, *interval, *timeout)
			cfg.logf("note", "Bids are buy prices; asks are sell prices. Price unit: quote/base; quantity unit: base")
			if *onchain {
				cfg.logf("plan", "Submit directly through RPC and wait for each successful receipt; no WebSocket connection or API credentials; -observe is unused")
				if *noSend {
					cfg.logf("plan", "NoSend: sign only, without broadcasting or waiting for receipts; versions remain reserved even though no frame is published")
				}
			} else {
				cfg.logf("note", "SENT means a network write only; REJECTED is an explicit server rejection; final observation %s", *observe)
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		rpc, err := ethclient.DialContext(ctx, cfg.RPCURL)
		if err != nil {
			return err
		}
		defer rpc.Close()
		cfg.logf("RPC", "Checking the chain ID returned by RPC...")
		contract, err := protocol.NewClient(ctx, rpc, protocol.Config{ChainID: cfg.ChainID, Proxy: common.HexToAddress(cfg.Protocol)})
		if err != nil {
			return err
		}
		cfg.logf("RPC", "chain ID=%d; network verified", cfg.ChainID)
		state, err := loadState(ctx, rpc, cfg)
		if err != nil {
			return err
		}
		cfg.logf("inventory", "Maker balances: base=%s, quote=%s; amounts in smallest units: %s, %s", tokenUnits(state.BaseBalance, state.Pair.BaseDecimals), tokenUnits(state.QuoteBalance, state.Pair.QuoteDecimals), state.BaseBalance, state.QuoteBalance)
		cfg.logf("state", "Loaded at block=%s", state.Block)
		if *loadOnly {
			cfg.logf("complete", "State loaded; next quote version remains (%d,%d)", cfg.NextVersion.Major, cfg.NextVersion.Minor)
			return nil
		}
		if *onchain {
			return submitOnChain(ctx, rpc, contract, state, signer, &cfg, path, *count, *interval, *noSend)
		}
		return publish(ctx, rpc, contract, state, signer, &cfg, path, *count, *interval, *observe)
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s [error] %s\n", time.Now().Format("15:04:05.000"), cfg.redact(err))
		os.Exit(1)
	}
}
