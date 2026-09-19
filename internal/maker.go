package stream

import (
	"context"
	"errors"
	"io"
	"time"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
)

// Maker sends signed market frames and reports asynchronous rejections.
// Its methods are safe for concurrent use. A Maker must not be copied.
type Maker struct{ *client }

// DialMaker connects and authenticates before returning. Canceling
// lifetimeCtx stops the maker; Close also waits for streaming cleanup.
func DialMaker(lifetimeCtx context.Context, cfg MakerConfig) (*Maker, error) {
	cfg, err := normalizeMaker(cfg)
	if err != nil {
		return nil, err
	}
	listingClient, err := newListingClient(cfg.Connection, cfg.Listing)
	if err != nil {
		return nil, err
	}
	c := newClient(lifetimeCtx, cfg.Connection, true)
	c.listingClient = listingClient
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
	return &Maker{c}, nil
}

// Publish writes a frame already prepared and signed by the public Maker API.
// Success confirms only a network write,
// not acceptance, delivery, or execution. Frames are never replayed on reconnect.
// A PublishError distinguishes failures before sending from uncertain delivery.
// Signatures must use recovery ID 27 or 28; frame.BuildAndSign produces this form.
// The caller may reuse value after return but must not mutate it during the call.
func (m *Maker) Publish(ctx context.Context, value *pb.MarketFrame) error {
	fail := func(err error) error { return &PublishError{Outcome: NotSent, Err: err} }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if value == nil || len(value.Signature) != 65 {
		return fail(errors.New("marketstream: frame and 65-byte signature are required"))
	}
	if value.Signature[64] != 27 && value.Signature[64] != 28 {
		return fail(errors.New("marketstream: wire signature recovery ID must be 27 or 28"))
	}
	// Marshaling owns the bytes before waiting for a writer, so the caller may
	// reuse the frame after Publish returns without affecting an active write.
	data, err := m.marshal(&pb.MakerEnvelope{
		Type:      pb.MakerMessageType_MAKER_MESSAGE_TYPE_FRAME_PUSH,
		Timestamp: time.Now().UnixMilli(),
		Payload:   &pb.MakerEnvelope_FramePush{FramePush: value},
	})
	if err != nil {
		return fail(err)
	}
	m.mu.Lock()
	s := m.current
	stopped := m.stopped
	m.mu.Unlock()
	if stopped {
		return fail(ErrClosed)
	}
	if s == nil {
		return fail(ErrDisconnected)
	}
	started, err := m.write(ctx, s, data, nil)
	if err != nil {
		outcome := NotSent
		if started {
			outcome = Unknown
		}
		return &PublishError{Outcome: outcome, Err: err}
	}
	return nil
}

// Next returns the next event, draining buffered events before EOF or a terminal
// error. Use one event consumer and call Next continuously: buffer overflow stops
// the maker with ErrEventBufferFull. ctx controls only this wait.
func (m *Maker) Next(ctx context.Context) (MakerEvent, error) {
	select {
	case <-ctx.Done():
		return MakerEvent{}, ctx.Err()
	case event, ok := <-m.makerEvents:
		if ok {
			return event, nil
		}
		if err := m.Err(); err != nil {
			return MakerEvent{}, err
		}
		return MakerEvent{}, io.EOF
	}
}
