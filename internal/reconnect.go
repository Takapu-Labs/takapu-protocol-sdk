package stream

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Reconnect requests a fresh connection and waits for authentication and subscription
// restoration. Canceling the wait does not cancel the client's lifetime.
func (c *client) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.stopped {
		err := c.stoppedErrorLocked()
		c.mu.Unlock()
		return err
	}
	generation := c.status.Generation
	s := c.current
	c.mu.Unlock()
	if s != nil {
		c.discard(s, errManualReconnect)
	}
	for {
		c.mu.Lock()
		if c.stopped {
			err := c.stoppedErrorLocked()
			c.mu.Unlock()
			return err
		}
		ready := c.status.Phase == Connected && c.status.Generation > generation
		if c.router != nil {
			ready = ready && !c.router.restoring
		}
		ch := c.changed
		c.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func terminalError(err error) bool {
	return errors.Is(err, ErrProtocol) ||
		errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrEventBufferFull) ||
		errors.Is(err, ErrFrameBufferFull)
}

// run owns the client lifetime: one session at a time, retries between sessions,
// and event-channel closure only after all session goroutines have exited.
func (c *client) run(first *session) {
	defer func() {
		c.mu.Lock()
		if !c.stopped {
			c.stopLocked(c.ctx.Err())
		}
		if c.maker {
			close(c.makerEvents)
		} else {
			close(c.routerEvents)
			close(c.router.frames)
		}
		c.mu.Unlock()
		close(c.done)
	}()
	s := first
	delay := c.cfg.Reconnect.InitialDelay
	for {
		err := c.runSession(s)
		c.mu.Lock()
		if s.failure != nil && !terminalError(err) {
			err = s.failure
		}
		stopped := c.stopped
		c.mu.Unlock()
		if stopped {
			return
		}
		if c.ctx.Err() != nil {
			c.stop(c.ctx.Err())
			return
		}
		c.discard(s, err)
		if terminalError(err) || (c.cfg.Reconnect.Policy == Disabled && !errors.Is(err, errManualReconnect)) {
			c.stop(err)
			return
		}
		if time.Since(s.started) >= c.cfg.Reconnect.ResetAfter {
			delay = c.cfg.Reconnect.InitialDelay
		}
		manual := errors.Is(err, errManualReconnect)
		for {
			if !manual {
				// Equal jitter stays positive and never exceeds the configured maximum.
				wait := delay/2 + time.Duration(rand.Int64N(int64(delay-delay/2)+1))
				timer := time.NewTimer(wait)
				select {
				case <-c.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if delay < c.cfg.Reconnect.MaxDelay {
					if delay > c.cfg.Reconnect.MaxDelay/2 {
						delay = c.cfg.Reconnect.MaxDelay
					} else {
						delay *= 2
					}
				}
			}
			manual = false
			c.mu.Lock()
			c.stats.Reconnects++
			c.mu.Unlock()
			next, dialErr := c.dial()
			if dialErr != nil {
				if terminalError(dialErr) {
					c.stop(dialErr)
					return
				}
				c.mu.Lock()
				c.status.LastError = dialErr
				c.signalLocked()
				c.mu.Unlock()
				if c.ctx.Err() != nil {
					return
				}
				continue
			}
			// Reserve the subscription sequencer before exposing the new session, so a
			// concurrent business request cannot overtake restoration of the saved request.
			if c.router != nil {
				select {
				case <-c.ctx.Done():
					next.cancel()
					_ = next.conn.Close()
					return
				case <-c.router.subscribeGate:
				}
				c.mu.Lock()
				c.router.restoring = true
				c.mu.Unlock()
			}
			if !c.install(next) {
				if c.router != nil {
					c.router.subscribeGate <- struct{}{}
				}
				return
			}
			s = next
			break
		}
	}
}
