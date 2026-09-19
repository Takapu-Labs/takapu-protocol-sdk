package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type runOptions struct {
	includeTestGray bool
	poll            time.Duration
	statusEvery     time.Duration
	maxCache        int
	frameBuffer     int
}

type subscriptionReply struct {
	frames <-chan router.FrameUpdate
	result router.SubscriptionResult
	err    error
}

// run shows the Router lifecycle: connect, replace the subscription, consume
// frames and control events, cache frames in the application, then close.
func run(ctx context.Context, cfg config, options runOptions) (err error) {
	filters, err := cfg.subscriptionFilters(options.includeTestGray)
	if err != nil {
		return err
	}

	// Pair, fee and decay parameters come from the protocol proxy; token
	// decimals come from the ERC-20 contracts.
	// Observation alone needs no RPC.
	var contract *protocol.Client
	var rpcClient *ethclient.Client
	if cfg.Swap != nil {
		rpcClient, err = ethclient.DialContext(ctx, cfg.Swap.RPCURL)
		if err != nil {
			return err
		}
		defer rpcClient.Close()
		contract, err = protocol.NewClient(ctx, rpcClient, protocol.Config{
			ChainID: cfg.Swap.ChainID,
			Proxy:   common.HexToAddress(cfg.Swap.Protocol),
		})
		if err != nil {
			return err
		}
	}

	// 1. Authenticate with a Router API key. No wallet or Maker is needed.
	client, err := router.Dial(ctx, router.RouterConfig{
		Connection:      router.ConnectionConfig{APIKey: cfg.APIKey},
		FrameBufferSize: options.frameBuffer,
	})
	if err != nil {
		return err
	}
	cfg.logf("connection", "Router authenticated; the SDK reconnects automatically and restores the complete subscription by default")

	// 2. Submit the complete desired scope. Keep consuming Events while waiting
	// for the acknowledgement so control events cannot fill their bounded queue.
	subscriptions := make(chan subscriptionReply, 1)
	subscriptionDone := make(chan struct{})
	go func() {
		defer close(subscriptionDone)
		frames, result, err := client.Subscribe(ctx, filters)
		subscriptions <- subscriptionReply{frames: frames, result: result, err: err}
	}()

	quotes := quoteObserver{
		latest: make(map[router.FrameKey]router.FrameUpdate),
		seen:   make(map[router.FrameKey][32]byte),
	}
	var frames <-chan router.FrameUpdate
	// 4. Close the connection and join the subscription goroutine on every exit.
	defer func() {
		closeErr := client.Close()
		if ctx.Err() == nil || !errors.Is(closeErr, ctx.Err()) {
			err = errors.Join(err, closeErr)
		}
		<-subscriptionDone
		cfg.logf("closed", "Router closed")
	}()
	cfg.logf("subscription", "Subscribe: complete list of %d filters", len(filters))

	// 3. The application owns the latest-frame map. The SDK only queues updates.
	ticker, statusTicker := time.NewTicker(options.poll), time.NewTicker(options.statusEvery)
	defer ticker.Stop()
	defer statusTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case reply := <-subscriptions:
			subscriptions = nil
			if reply.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return reply.err
			}
			// A successful request may still contain rejected individual filters.
			if err := validateSubscription(reply.result); err != nil {
				return err
			}
			frames = reply.frames
		case update, ok := <-frames:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return client.Err()
			}
			if err := quotes.accept(update, client.Status(), options.maxCache); err != nil {
				return err
			}
		case event, ok := <-client.Events():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				if err := client.Err(); err != nil {
					return err
				}
				return errors.New("router event stream closed unexpectedly")
			}
			// Events can lag behind the current status; invalidate using the current
			// snapshot so an older event cannot clear frames from a newer revision.
			quotes.sync(client.Status())
			switch event.Kind {
			case router.ConnectionChanged:
				message := ""
				if event.Status.LastError != nil {
					message = cfg.redact(event.Status.LastError.Error())
				}
				cfg.logf("connection-event", "phase=%s generation=%d error=%s", event.Status.Phase, event.Status.Generation, message)
			case router.SubscriptionChanged:
				if err := validateSubscription(event.Subscription); err != nil {
					return err
				}
				cfg.logf("subscription-ack", "mode=%s generation=%d revision=%d; waiting for new quotes", event.Subscription.Kind, event.Subscription.Generation, event.Subscription.SubscriptionRevision)
			case router.ServerError:
				if event.ServerError != nil {
					return event.ServerError
				}
			}
		case <-ticker.C:
			if err := quotes.observe(ctx, client, cfg, contract, rpcClient); err != nil {
				return err
			}
		case <-statusTicker.C:
			stats, status := client.Stats(), client.Status()
			quotes.sync(status)
			cfg.logf("status", "phase=%s generation=%d revision=%d cached=%d queued=%d messages_received=%d reconnects=%d", status.Phase, status.Generation, status.SubscriptionRevision, len(quotes.latest), stats.FrameQueueLength, stats.MessagesReceived, stats.Reconnects)
			if len(quotes.latest) == 0 {
				cfg.logf("waiting", "No quotes cached; subscriptions do not replay historical snapshots, and gray data requires explicit filters")
			}
		}
	}
}

func validateSubscription(result router.SubscriptionResult) error {
	for _, item := range result.Results {
		if !item.Accepted {
			return fmt.Errorf("subscription filter %d was rejected: %s", item.Index, item.Reason)
		}
	}
	return nil
}
