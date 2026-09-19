package stream

import (
	"context"
	"sync"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/listing"
)

// client owns the lifecycle shared by Maker and Router. Methods ending in
// Locked require mu; it also protects Router's subscription state and frame queue.
// Configuration, including credentials, is immutable after dialing.
// run is the sole owner of closing event channels and done.
type client struct {
	mu            sync.Mutex
	cfg           ConnectionConfig
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	changed       chan struct{}
	status        ClientStatus
	stats         ClientStats
	stopped       bool
	err           error
	current       *session
	maker         bool
	makerEvents   chan MakerEvent
	routerEvents  chan RouterEvent
	router        *Router
	listingClient *listing.Client // Stable pointer; HTTP credentials are fixed at creation.
}

func newClient(ctx context.Context, cfg ConnectionConfig, maker bool) *client {
	life, cancel := context.WithCancel(ctx)
	c := &client{
		cfg:     cfg,
		ctx:     life,
		cancel:  cancel,
		done:    make(chan struct{}),
		changed: make(chan struct{}),
		maker:   maker,
		status:  ClientStatus{Phase: Connecting},
	}
	if maker {
		c.makerEvents = make(chan MakerEvent, cfg.EventBufferSize)
	} else {
		c.routerEvents = make(chan RouterEvent, cfg.EventBufferSize)
	}
	return c
}

// signalLocked wakes every status waiter without blocking a state transition.
func (c *client) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *client) connectionEventLocked() {
	if c.maker {
		c.emitMakerLocked(MakerEvent{Kind: ConnectionChanged, Status: c.status})
	} else {
		c.emitRouterLocked(RouterEvent{Kind: ConnectionChanged, Status: c.status})
	}
}

func (c *client) emitMakerLocked(e MakerEvent) {
	if c.stopped {
		return
	}
	select {
	case c.makerEvents <- e:
	default:
		c.stopLocked(ErrEventBufferFull)
	}
}

func (c *client) emitRouterLocked(e RouterEvent) {
	if c.stopped {
		return
	}
	select {
	case c.routerEvents <- e:
	default:
		c.stopLocked(ErrEventBufferFull)
	}
}

func (c *client) stopLocked(err error) {
	if c.stopped {
		return
	}
	c.stopped = true
	c.err = err
	c.status.Phase = Stopped
	if err != nil {
		c.status.LastError = err
	}
	if c.router != nil {
		c.router.clearLocked()
	}
	if c.current != nil {
		c.current.cancel()
		_ = c.current.conn.Close()
		c.current = nil
	}
	c.cancel()
	c.signalLocked()
	// The final cause is available through Err even when no event slot remains.
	if c.maker {
		select {
		case c.makerEvents <- MakerEvent{Kind: ConnectionChanged, Status: c.status}:
		default:
		}
	} else {
		select {
		case c.routerEvents <- RouterEvent{Kind: ConnectionChanged, Status: c.status}:
		default:
		}
	}
}

func (c *client) stop(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked(err)
}

// Close stops streaming and waits for its goroutines to exit. It is safe to call
// repeatedly and returns the terminal error, if any. Listing remains usable.
func (c *client) Close() error {
	c.stop(nil)
	<-c.done
	return c.Err()
}

// Done closes after streaming has stopped and its goroutines have exited.
func (c *client) Done() <-chan struct{} {
	return c.done
}

// Err reports the terminal cause, or nil before shutdown or after a clean Close.
func (c *client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Status returns a snapshot of the connection and subscription state.
func (c *client) Status() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Stats returns counters accumulated across all connection generations.
func (c *client) Stats() ClientStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats
	if c.maker {
		stats.EventQueueLength = len(c.makerEvents)
	} else {
		stats.EventQueueLength = len(c.routerEvents)
		stats.FrameQueueLength = len(c.router.frames)
	}
	return stats
}

func (c *client) stoppedErrorLocked() error {
	if c.err != nil {
		return c.err
	}
	return ErrClosed
}
