package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/proto"
)

func (c config) redact(message string) string {
	secrets := []string{c.APIKey}
	if c.Swap != nil {
		secrets = append(secrets, c.Swap.RPCURL)
		if endpoint, err := url.Parse(c.Swap.RPCURL); err == nil {
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
	}
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func (c config) logf(stage, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s [%s] %s\n", time.Now().Format("15:04:05.000"), stage, c.redact(fmt.Sprintf(format, args...)))
}

// The event loop owns this cache. No locks are needed because only that loop
// reads or writes it; a concurrent application would provide its own locking.
type quoteObserver struct {
	latest     map[router.FrameKey]router.FrameUpdate
	seen       map[router.FrameKey][32]byte
	generation uint64
	revision   uint64
}

func (o *quoteObserver) sync(status router.ClientStatus) {
	if status.Phase != router.Connected || status.Generation != o.generation || status.SubscriptionRevision != o.revision {
		clear(o.latest)
		clear(o.seen)
		o.generation, o.revision = status.Generation, status.SubscriptionRevision
	}
}

func (o *quoteObserver) accept(update router.FrameUpdate, status router.ClientStatus, limit int) error {
	o.sync(status)
	if !o.current(status, update) {
		return nil
	}
	if _, exists := o.latest[update.Key]; !exists && len(o.latest) >= limit {
		return fmt.Errorf("application frame cache reached its limit of %d streams", limit)
	}
	o.latest[update.Key] = update
	return nil
}

func (o *quoteObserver) observe(ctx context.Context, client *router.Router, cfg config, contract *protocol.Client, rpcClient *ethclient.Client) error {
	o.sync(client.Status())
	quotes := make([]router.FrameUpdate, 0, len(o.latest))
	for _, update := range o.latest {
		quotes = append(quotes, update)
	}
	sort.Slice(quotes, func(i, j int) bool {
		a, b := quotes[i].Key, quotes[j].Key
		if a.ChainID != b.ChainID {
			return a.ChainID < b.ChainID
		}
		if a.PairID != b.PairID {
			return a.PairID < b.PairID
		}
		return a.Maker.Hex() < b.Maker.Hex()
	})
	for _, quote := range quotes {
		// A reconnect or subscription replacement invalidates previously received
		// frames, even if its control event has not reached the application yet.
		if !o.current(client.Status(), quote) {
			continue
		}
		wire, err := proto.Marshal(quote.Frame)
		if err != nil {
			return err
		}
		fingerprint := sha256.Sum256(wire)
		if previous, ok := o.seen[quote.Key]; ok && previous == fingerprint {
			continue
		}
		header := frame.DecodeHeader(frame.CompactFrame{Header: [32]byte(quote.Frame.Frame.Header)})
		// Recheck immediately before emitting the snapshot.
		if !o.current(client.Status(), quote) {
			continue
		}
		o.seen[quote.Key] = fingerprint
		cfg.logf("quote", "chain=%d pair=%d maker=%s version=(%d,%d) age=%ds", quote.Key.ChainID, quote.Key.PairID, quote.Key.Maker.Hex(), header.MajorVersion, header.MinorVersion, time.Now().Unix()-int64(quote.Frame.UpdatedAt))
		cfg.logf("book", "bid=[%s] ask=[%s]; format: price in quote/base × amount in base", book(quote.Frame.Bids), book(quote.Frame.Asks))
		s := cfg.Swap
		if s == nil || quote.Key.ChainID != s.ChainID || quote.Key.PairID != s.PairID || quote.Key.Maker != common.HexToAddress(s.Maker) {
			continue
		}
		// Read current contract settings, then quote and build calldata directly.
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		params, err := loadQuoteConfig(readCtx, contract, rpcClient, s.PairID)
		cancel()
		if err != nil {
			cfg.logf("quote-unavailable", "read quote configuration: %v", err)
			continue
		}
		if !o.current(client.Status(), quote) {
			continue
		}
		if err := cfg.showSwap(quote, params); err != nil {
			cfg.logf("quote-unavailable", "%v", err)
		}
	}
	return nil
}

func (o *quoteObserver) current(status router.ClientStatus, quote router.FrameUpdate) bool {
	return status.Phase == router.Connected &&
		status.Generation == o.generation && status.SubscriptionRevision == o.revision &&
		quote.Generation == status.Generation && quote.SubscriptionRevision == status.SubscriptionRevision
}

func book(values []*pb.PriceLevel) string {
	if len(values) == 0 {
		return "empty"
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		items = append(items, value.GetPrice()+" × "+value.GetAmount())
	}
	return strings.Join(items, "; ")
}
