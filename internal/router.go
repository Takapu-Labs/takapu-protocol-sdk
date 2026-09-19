package stream

import "context"

// Router maintains subscriptions and streams matching market frames to callers.
// Its methods are safe for concurrent use. A Router must not be copied.
type Router struct {
	*client
	cfg           RouterConfig
	subscribeGate chan struct{}
	frames        chan FrameUpdate // Stable channel; sends and closure require client.mu.

	// All fields below are protected by client.mu.
	ready     bool // Acknowledgement received for the current revision.
	restoring bool // Reconnect owns subscribeGate until restoration finishes.
	accepted  []*PairFilter
	saved     *subscriptionRequest // Complete request to restore after reconnect.
	pending   *pendingSubscription // At most one, serialized by subscribeGate.
}

// DialRouter connects and authenticates before returning. Canceling lifetimeCtx
// stops the router; use Close to stop it explicitly and wait for cleanup.
// Frames become available after Subscribe succeeds.
func DialRouter(lifetimeCtx context.Context, cfg RouterConfig) (*Router, error) {
	cfg, err := normalizeRouter(cfg)
	if err != nil {
		return nil, err
	}
	listingClient, err := newListingClient(cfg.Connection, cfg.Listing)
	if err != nil {
		return nil, err
	}
	c := newClient(lifetimeCtx, cfg.Connection, false)
	c.listingClient = listingClient
	r := &Router{
		client:        c,
		cfg:           cfg,
		subscribeGate: make(chan struct{}, 1),
		frames:        make(chan FrameUpdate, cfg.FrameBufferSize),
	}
	r.subscribeGate <- struct{}{}
	c.router = r
	s, err := c.dial()
	if err != nil {
		c.cancel()
		return nil, err
	}
	if !c.install(s) {
		c.cancel()
		if err := lifetimeCtx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrClosed
	}
	go c.run(s)
	return r, nil
}

// Events returns the router's event stream. Drain it continuously: a full buffer
// stops the client with ErrEventBufferFull. The channel closes after shutdown.
func (r *Router) Events() <-chan RouterEvent {
	return r.routerEvents
}
