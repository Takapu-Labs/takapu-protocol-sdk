// listing discovers supported chains and usable pair configurations through the public HTTP API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
)

func main() {
	configPath := flag.String("config", "config.local.json", "Path to the Listing JSON configuration")
	pairID := flag.Uint64("pair-id", 0, "Query an on-chain pair ID directly; discover one from the list if omitted")
	timeout := flag.Duration("timeout", 2*time.Minute, "Overall query timeout")
	requestTimeout := flag.Duration("request-timeout", 10*time.Second, "Timeout for each HTTP request")
	flag.Parse()

	var cfg config
	err := func() error {
		if *pairID > math.MaxInt32 || *timeout <= 0 || *requestTimeout <= 0 {
			return errors.New("pair-id must fit a positive int32 or be omitted; timeouts must be positive")
		}
		var err error
		cfg, err = loadConfig(*configPath)
		if err != nil {
			return err
		}

		// The SDK uses its default Listing endpoint and authenticates each HTTP
		// request using this API key.
		client, err := listing.NewClient(listing.Config{
			APIKey:  cfg.APIKey,
			Timeout: *requestTimeout,
		})
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		return runListing(ctx, cfg, client, uint32(*pairID), os.Stdout)
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "[failed]", cfg.redact(err.Error()))
		os.Exit(1)
	}
}

func runListing(ctx context.Context, cfg config, client *listing.Client, pairID uint32, output io.Writer) error {
	chains, err := client.ListChains(ctx)
	if err != nil {
		return err
	}
	chainData, err := cfg.marshalRedacted(chains)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Supported chains:"); err != nil {
		return err
	}
	if _, err := output.Write(append(chainData, '\n')); err != nil {
		return err
	}

	if pairID == 0 {
		// The public API returns approved, active pairs with active tax
		// configurations for this chain in one request.
		pairs, err := listUsablePairs(ctx, client, cfg.ChainID)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "Found %d usable pairs (approved, active, not gray, registered on-chain)\n", len(pairs)); err != nil {
			return err
		}
		if len(pairs) == 0 {
			return nil
		}
		for _, pair := range pairs {
			if _, err := fmt.Fprintln(output, cfg.redact(fmt.Sprintf("chain=%d pair=%d %s/%s", pair.ChainID, pair.PairID, pair.BaseSymbol, pair.QuoteSymbol))); err != nil {
				return err
			}
		}
		pairID = pairs[0].PairID
	}

	// Refresh the chosen pair before using its configuration.
	pair, err := client.ListPair(ctx, cfg.ChainID, pairID)
	if err != nil {
		return err
	}
	if !usablePair(pair, cfg.ChainID) {
		return errors.New("pair details do not meet this example's usability criteria; choose another pair or refresh the list")
	}
	data, err := cfg.marshalRedacted(pair)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Latest pair configuration (PriceTickSize and LotSize are raw uint128 integer strings; verify against pairConfigs before use):"); err != nil {
		return err
	}
	_, err = output.Write(append(data, '\n'))
	return err
}
