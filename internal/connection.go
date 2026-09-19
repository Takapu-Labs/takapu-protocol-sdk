package stream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// session is one authenticated connection. runSession owns its read loop;
// writeGate serializes data writes from publishing, subscribing, and heartbeats.
// Reconnecting always creates a new session, so stale work can be rejected by
// comparing its session pointer with client.current under client.mu.
type session struct {
	conn       *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	writeGate  chan struct{}
	generation uint64
	started    time.Time
	failure    error // protected by client.mu
}

func classifyReadError(err error) error {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case websocket.ClosePolicyViolation, 4001, 4003:
			return fmt.Errorf("%w: %w", ErrAuthentication, err)
		case websocket.CloseProtocolError, websocket.CloseUnsupportedData, websocket.CloseInvalidFramePayloadData, websocket.CloseMessageTooBig:
			return fmt.Errorf("%w: %w", ErrProtocol, err)
		}
	}
	if errors.Is(err, websocket.ErrReadLimit) {
		return fmt.Errorf("%w: %w", ErrProtocol, ErrMessageTooLarge)
	}
	return err
}

func (c *client) dial() (*session, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.cfg.HandshakeTimeout)
	defer cancel()
	var stopClose func() bool
	defer func() {
		if stopClose != nil {
			stopClose()
		}
	}()
	netDialer := &net.Dialer{}
	dialer := websocket.Dialer{
		HandshakeTimeout: c.cfg.HandshakeTimeout,
		Proxy:            http.ProxyFromEnvironment,
		NetDialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			conn, err := netDialer.DialContext(dialCtx, network, address)
			if err == nil {
				// Gorilla applies cancellation to TCP/TLS dialing, but HTTP upgrade
				// and proxy reads need an explicit close. Use our context: Gorilla
				// cancels its own child context when the upgrade finishes.
				stopClose = context.AfterFunc(ctx, func() { _ = conn.Close() })
			}
			return conn, err
		},
	}
	ws, resp, err := dialer.DialContext(ctx, c.cfg.URL, http.Header{APIKeyHeader: []string{c.cfg.APIKey}})
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return nil, fmt.Errorf("%w (HTTP %d)", ErrAuthentication, resp.StatusCode)
		}
		return nil, err
	}
	ws.SetReadLimit(c.cfg.MaxReadMessageBytes)
	deadline, _ := ctx.Deadline()
	_ = ws.SetReadDeadline(deadline)
	typ, data, err := ws.ReadMessage()
	if err == nil && typ != websocket.BinaryMessage {
		err = fmt.Errorf("%w: expected binary connection acknowledgement", ErrProtocol)
	}
	identity := ""
	if err == nil {
		identity, err = c.connectionAck(data)
	}
	stopClose()
	// Closing the socket interrupts ReadMessage with a network error. Preserve
	// the context cause so callers can distinguish cancellation from a failure.
	if contextErr := ctx.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		_ = ws.Close()
		return nil, classifyReadError(err)
	}
	life, stop := context.WithCancel(c.ctx)
	s := &session{
		conn:      ws,
		ctx:       life,
		cancel:    stop,
		writeGate: make(chan struct{}, 1),
		started:   time.Now(),
	}
	s.writeGate <- struct{}{}
	c.mu.Lock()
	c.stats.MessagesReceived++
	c.stats.BytesReceived += uint64(len(data))
	c.status.Identity = identity
	c.mu.Unlock()
	return s, nil
}

// install publishes an authenticated session and advances the generation.
func (c *client) install(s *session) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped || c.ctx.Err() != nil {
		s.cancel()
		_ = s.conn.Close()
		return false
	}
	c.status.Generation++
	s.generation = c.status.Generation
	c.current = s
	c.status.Phase = Connected
	c.signalLocked()
	c.connectionEventLocked()
	return !c.stopped
}

// discard invalidates a failed session immediately. run decides whether to retry
// or stop; callbacks from an older session cannot discard a newer connection.
func (c *client) discard(s *session, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
	if c.current == s {
		c.current = nil
		if !c.stopped {
			c.status.Phase = Reconnecting
			c.status.LastError = err
			if c.router != nil {
				c.router.clearLocked()
			}
			c.signalLocked()
			c.connectionEventLocked()
		}
	}
	s.cancel()
	_ = s.conn.Close()
}

func (c *client) runSession(s *session) (result error) {
	var wg sync.WaitGroup
	closeOnCancel := context.AfterFunc(s.ctx, func() { _ = s.conn.Close() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(c.cfg.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if err := c.sendHeartbeat(s, true, false); err != nil {
					if s.ctx.Err() == nil {
						c.discard(s, err)
					}
					return
				}
			}
		}
	}()
	if c.router != nil {
		c.mu.Lock()
		restore := c.router.restoring
		c.mu.Unlock()
		if restore {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.router.restore(s)
			}()
		}
	}
	defer func() {
		// Preserve the read loop's cause before canceling active writes. Their
		// cancellation callbacks may call discard with context.Canceled during
		// teardown; that secondary error must not replace a read timeout. An
		// earlier write failure or manual reconnect retains its original cause.
		c.mu.Lock()
		if s.failure == nil {
			s.failure = result
		}
		c.mu.Unlock()
		s.cancel()
		_ = s.conn.Close()
		closeOnCancel()
		wg.Wait()
	}()
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(c.cfg.ReadIdleTimeout))
		typ, data, err := s.conn.ReadMessage()
		if err != nil {
			return classifyReadError(err)
		}
		if typ != websocket.BinaryMessage {
			return fmt.Errorf("%w: expected binary envelope", ErrProtocol)
		}
		c.mu.Lock()
		c.stats.MessagesReceived++
		c.stats.BytesReceived += uint64(len(data))
		c.mu.Unlock()
		if err := c.handle(s, data); err != nil {
			return err
		}
	}
}

// write serializes all data messages and includes queueing in WriteTimeout.
// before runs under c.mu exactly when the request starts writing.
// The returned boolean means a network write was attempted; failures after that
// point cannot establish whether the server received the message.
func (c *client) write(ctx context.Context, s *session, data []byte, before func() error) (bool, error) {
	if int64(len(data)) > c.cfg.MaxWriteMessageBytes {
		return false, ErrMessageTooLarge
	}
	call, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeout)
	defer cancel()
	select {
	case <-call.Done():
		return false, call.Err()
	case <-s.ctx.Done():
		return false, ErrDisconnected
	case <-s.writeGate:
	}
	defer func() { s.writeGate <- struct{}{} }()
	c.mu.Lock()
	if err := call.Err(); err != nil {
		c.mu.Unlock()
		return false, err
	}
	if c.stopped || c.current != s || s.ctx.Err() != nil {
		c.mu.Unlock()
		return false, ErrDisconnected
	}
	if before != nil {
		if err := before(); err != nil {
			c.mu.Unlock()
			return false, err
		}
	}
	c.mu.Unlock()
	deadline, _ := call.Deadline()
	_ = s.conn.SetWriteDeadline(deadline)
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(call, func() {
		c.discard(s, call.Err())
		close(cancelDone)
	})
	err := s.conn.WriteMessage(websocket.BinaryMessage, data)
	if !stopCancel() {
		<-cancelDone
	}
	if contextErr := call.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		c.discard(s, err)
		return true, err
	}
	c.mu.Lock()
	c.stats.MessagesSent++
	c.stats.BytesSent += uint64(len(data))
	c.mu.Unlock()
	return true, nil
}
