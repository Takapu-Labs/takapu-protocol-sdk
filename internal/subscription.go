package stream

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"
)

type subscriptionRequest struct {
	filters []*PairFilter
}

type subscriptionReply struct {
	result SubscriptionResult
	err    error
}

// pendingSubscription connects the sole in-flight request to its acknowledgement.
// reply is buffered so the read loop never waits for a canceled caller.
type pendingSubscription struct {
	request  subscriptionRequest
	session  *session
	revision uint64
	reply    chan subscriptionReply
}

// clearLocked invalidates the active subscription and drains queued frames. The saved
// complete request survives disconnects so restoration can retry every filter.
func (r *Router) clearLocked() {
drain:
	for {
		select {
		case <-r.frames:
		default:
			break drain
		}
	}
	r.ready = false
	r.accepted = nil
	r.pending = nil
}

func cloneFilters(filters []*PairFilter) []*PairFilter {
	out := make([]*PairFilter, len(filters))
	for i, f := range filters {
		out[i] = proto.Clone(f).(*PairFilter)
	}
	return out
}

// Subscribe replaces the complete filter set and waits for the
// server's acknowledgement. Individual filters may be rejected without an error;
// inspect each returned result. Local validation failures leave delivery intact.
// Use a single empty PairFilter to select all visible pairs and makers.
// Queued frames are discarded once writing starts; delivery resumes after ack.
// Successful calls return the same single-consumer channel, with no replay.
// Drain it continuously: overflow stops the router with ErrFrameBufferFull.
// The channel stays open across subscriptions and reconnects and closes on teardown.
// Frames already received remain caller-owned; use their generation and revision
// to invalidate retained frames when the router's status changes.
// Callers must not mutate filters during this call; the router retains copies.
func (r *Router) Subscribe(ctx context.Context, filters []*PairFilter) (<-chan FrameUpdate, SubscriptionResult, error) {
	if err := validateSubscriptionFilters(filters, r.cfg.MaxSubscribeFilters); err != nil {
		return nil, SubscriptionResult{}, err
	}
	return r.subscribeFrames(ctx, subscriptionRequest{filters: cloneFilters(filters)})
}

func validateSubscriptionFilters(filters []*PairFilter, limit int) error {
	if len(filters) == 0 {
		return errors.New("marketstream: Subscribe requires a nonempty complete list")
	}
	if len(filters) > limit {
		return errors.New("marketstream: too many subscription filters")
	}
	for i, f := range filters {
		if f == nil {
			return fmt.Errorf("marketstream: filter %d is nil", i)
		}
		if f.Pair != nil && (f.Pair.ChainId == 0 || f.Pair.PairId == 0) {
			return fmt.Errorf("marketstream: filter %d has invalid pair key", i)
		}
		if f.Maker != "" && (!common.IsHexAddress(f.Maker) || common.HexToAddress(f.Maker) == (common.Address{})) {
			return fmt.Errorf("marketstream: filter %d has invalid maker", i)
		}
		if (f.AllowPairGray && f.Pair == nil) || (f.AllowMakerGray && f.Maker == "") {
			return fmt.Errorf("marketstream: filter %d has invalid gray flags", i)
		}
		if hasUnknownFields(f.ProtoReflect()) {
			return fmt.Errorf("marketstream: filter %d has unknown fields", i)
		}
	}
	return nil
}

func (r *Router) subscribeFrames(ctx context.Context, request subscriptionRequest) (<-chan FrameUpdate, SubscriptionResult, error) {
	result, err := r.subscribe(ctx, request)
	if err != nil {
		return nil, result, err
	}
	return r.frames, result, nil
}

func (r *Router) subscribe(ctx context.Context, request subscriptionRequest) (SubscriptionResult, error) {
	call, cancel := context.WithTimeout(ctx, r.cfg.SubscribeTimeout)
	defer cancel()
	select {
	case <-call.Done():
		return SubscriptionResult{}, call.Err()
	case <-r.ctx.Done():
		r.mu.Lock()
		err := r.stoppedErrorLocked()
		r.mu.Unlock()
		return SubscriptionResult{}, err
	case <-r.subscribeGate:
	}
	defer func() { r.subscribeGate <- struct{}{} }()
	if err := call.Err(); err != nil {
		return SubscriptionResult{}, err
	}
	r.mu.Lock()
	s := r.current
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return SubscriptionResult{}, ErrClosed
	}
	if s == nil {
		return SubscriptionResult{}, ErrDisconnected
	}
	return r.sendSubscription(call, s, request)
}

// sendSubscription is called while holding subscribeGate, including restoration.
func (r *Router) sendSubscription(ctx context.Context, s *session, request subscriptionRequest) (SubscriptionResult, error) {
	data, err := r.marshal(&pb.RouterEnvelope{
		Type:      pb.RouterMessageType_ROUTER_MESSAGE_TYPE_SUBSCRIBE,
		Timestamp: time.Now().UnixMilli(),
		Payload: &pb.RouterEnvelope_Subscribe{
			Subscribe: &pb.SubscribeRequest{Filters: request.filters},
		},
	})
	if err != nil {
		return SubscriptionResult{}, err
	}
	pending := &pendingSubscription{request: request, session: s, reply: make(chan subscriptionReply, 1)}
	started, err := r.write(ctx, s, data, func() error {
		r.clearLocked()
		r.status.SubscriptionRevision++
		pending.revision = r.status.SubscriptionRevision
		r.pending = pending
		saved := request
		r.saved = &saved
		r.signalLocked()
		return nil
	})
	if err != nil {
		if started {
			r.discard(s, err)
		}
		return SubscriptionResult{}, err
	}
	select {
	case reply := <-pending.reply:
		return reply.result, reply.err
	case <-ctx.Done():
		select {
		case reply := <-pending.reply:
			return reply.result, reply.err
		default:
		}
		r.discard(s, ctx.Err())
		return SubscriptionResult{}, ctx.Err()
	case <-s.ctx.Done():
		select {
		case reply := <-pending.reply:
			return reply.result, reply.err
		default:
		}
		r.mu.Lock()
		err := s.failure
		if err == nil {
			err = ErrDisconnected
		}
		r.mu.Unlock()
		return SubscriptionResult{}, err
	}
}

func cloneResult(result SubscriptionResult) SubscriptionResult {
	out := result
	out.Results = make([]SubscriptionItemResult, len(result.Results))
	copy(out.Results, result.Results)
	for i := range out.Results {
		out.Results[i].Filter = proto.Clone(out.Results[i].Filter).(*PairFilter)
	}
	return out
}

func (r *Router) ack(s *session, ack *pb.SubscribeAck) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != s || r.stopped {
		return nil
	}
	pending := r.pending
	if pending == nil || pending.session != s {
		return fmt.Errorf("%w: unsolicited subscription acknowledgement", ErrProtocol)
	}
	if len(ack.Results) != len(pending.request.filters) {
		return fmt.Errorf("%w: subscription result count mismatch", ErrProtocol)
	}
	result := SubscriptionResult{
		Kind:                 Replace,
		Generation:           s.generation,
		SubscriptionRevision: pending.revision,
		Results:              make([]SubscriptionItemResult, 0, len(ack.Results)),
	}
	accepted := make([]*PairFilter, 0, len(ack.Results))
	for i, item := range ack.Results {
		if item == nil {
			return fmt.Errorf("%w: missing filter result", ErrProtocol)
		}
		filter := pending.request.filters[i]
		result.Results = append(result.Results, SubscriptionItemResult{
			Index:    i,
			Filter:   proto.Clone(filter).(*PairFilter),
			Accepted: item.Ok,
			Reason:   item.Reason,
		})
		if item.Ok {
			accepted = append(accepted, filter)
		}
	}
	r.accepted = accepted
	r.ready = true
	r.pending = nil
	r.emitRouterLocked(RouterEvent{Kind: SubscriptionChanged, Subscription: cloneResult(result)})
	if r.stopped {
		pending.reply <- subscriptionReply{err: r.stoppedErrorLocked()}
	} else {
		pending.reply <- subscriptionReply{result: result}
	}
	r.signalLocked()
	return nil
}

func (r *Router) serverError(s *session, value *pb.Error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != s || r.stopped {
		return nil
	}
	err := &ServerErrorInfo{Code: value.Code, Message: value.Message}
	if value.Code == "subscribe_request_too_large" {
		pending := r.pending
		if pending == nil {
			return fmt.Errorf("%w: unsolicited subscription rejection", ErrProtocol)
		}
		r.pending = nil // frame delivery stays paused; saved request stays complete
		pending.reply <- subscriptionReply{err: err}
	}
	r.emitRouterLocked(RouterEvent{Kind: ServerError, ServerError: &ServerErrorInfo{Code: err.Code, Message: err.Message}})
	return nil
}

func (r *Router) restore(s *session) {
	defer func() {
		r.mu.Lock()
		r.restoring = false
		r.signalLocked()
		r.mu.Unlock()
		r.subscribeGate <- struct{}{}
	}()
	r.mu.Lock()
	saved := r.saved
	r.mu.Unlock()
	if saved == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, r.cfg.SubscribeTimeout)
	defer cancel()
	_, err := r.sendSubscription(ctx, s, *saved)
	var serverErr *ServerErrorInfo
	if errors.As(err, &serverErr) {
		r.stop(err)
	} else if err != nil {
		// Restoration is mandatory. Even a failure before its write began must
		// not leave an apparently connected client silently unsubscribed.
		r.discard(s, err)
	}
}
