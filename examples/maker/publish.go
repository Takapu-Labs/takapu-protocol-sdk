package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/maker"
	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/protocol"
	"github.com/ethereum/go-ethereum/ethclient"
)

// publish owns the WebSocket connection and consumes control events throughout
// this bounded run. The caller retains ownership of RPC, config and version policy.
func publish(ctx context.Context, rpc *ethclient.Client, contract *protocol.Client, state chainState, signer localSigner, cfg *config, configPath string, count int, interval, observe time.Duration) (err error) {
	cfg.logf("connection", "Connecting to the Maker WebSocket and authenticating with an API key...")
	p, err := maker.Dial(ctx, maker.MakerConfig{
		Connection: maker.ConnectionConfig{
			APIKey:    cfg.APIKey,
			Reconnect: maker.ReconnectConfig{Policy: maker.Disabled},
		},
	})
	if err != nil {
		return err
	}
	// Drain SDK events while the main goroutine reads RPC and prepares frames.
	readerCtx, readerCancel := context.WithCancel(ctx)
	eventCh := make(chan maker.MakerEvent, 256)
	readerErr := make(chan error, 1)
	go func() {
		defer close(eventCh)
		for {
			event, err := p.Next(readerCtx)
			if err != nil {
				readerErr <- err
				return
			}
			select {
			case eventCh <- event:
			case <-readerCtx.Done():
				readerErr <- readerCtx.Err()
				return
			}
		}
	}()
	healthExplained := false
	handle := func(event maker.MakerEvent) {
		if event.Rejection != nil {
			cfg.logf("rejection", "REJECTED version=(%d,%d) pair=%d step=%s reason=%s", event.Rejection.MajorVersion, event.Rejection.MinorVersion, event.Rejection.GetPair().GetPairId(), event.Rejection.RejectStep, event.Rejection.RejectReason)
			if event.Rejection.RejectStep == "HEALTH" && !healthExplained {
				cfg.logf("rejection details", "HEALTH means this Maker/Pair has not passed the health gate; consult the backend for health status and sample details. This quote will not be replayed")
				healthExplained = true
			}
		} else {
			cfg.logf("connection event", "phase=%s generation=%d", event.Status.Phase, event.Status.Generation)
			if event.Status.LastError != nil {
				cfg.logf("connection error", "%s", cfg.redact(event.Status.LastError))
			}
		}
	}
	defer func() {
		cfg.logf("connection", "Observation ended; closing WebSocket")
		err = errors.Join(err, p.Close())
		for event := range eventCh {
			handle(event)
		}
		readerCancel()
	}()
	cfg.logf("connection", "Authenticated; maker_id=%s", p.Status().Identity)
	for i := 0; i < count; i++ {
		params, err := prepareNextQuote(ctx, rpc, contract, state, signer, cfg, configPath, i, count)
		if err != nil {
			return err
		}
		major, minor := params.MajorVersion, params.MinorVersion
		// Publish is one network write attempt; only the caller decides whether
		// to send another quote. This example stops on every send error.
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		sendStarted := time.Now()
		err = p.Publish(sendCtx, params)
		sendDuration := time.Since(sendStarted)
		cancel()
		if err != nil {
			outcome := publishOutcome(err)
			cfg.logf("send error", "outcome=%s version=(%d,%d): %s; version remains reserved, no automatic retry", outcome, major, minor, cfg.redact(err))
			return err
		}
		cfg.logf("send", "SENT %d/%d version=(%d,%d), quote signed and network write succeeded in %s; continuing to observe server events", i+1, count, major, minor, sendDuration.Round(time.Microsecond))
		delay := interval
		if i == count-1 {
			delay = observe
			cfg.logf("observe", "Final frame written; observing for %s before exiting", delay)
		} else {
			cfg.logf("wait", "Next frame in %s; continuing to observe rejection events while waiting", delay)
		}
		if err := consumeFor(ctx, eventCh, readerErr, delay, handle); err != nil {
			return err
		}
	}
	return nil
}

func consumeFor(ctx context.Context, events <-chan maker.MakerEvent, readerErr <-chan error, duration time.Duration, handle func(maker.MakerEvent)) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				err := <-readerErr
				if errors.Is(err, io.EOF) {
					return errors.New("maker connection closed before observation completed")
				}
				return err
			}
			handle(event)
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Unknown is the conservative fallback if an unexpected wrapper lacks the SDK's
// write outcome. Neither outcome triggers an automatic retry in this example.
func publishOutcome(err error) maker.PublishOutcome {
	var publishErr *maker.PublishError
	if errors.As(err, &publishErr) {
		return publishErr.Outcome
	}
	return maker.Unknown
}
