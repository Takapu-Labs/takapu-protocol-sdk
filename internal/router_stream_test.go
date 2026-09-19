package stream

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/frame"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

type framePeer struct {
	conn     *websocket.Conn
	requests chan *pb.SubscribeRequest
}

func frameTestRouter(t *testing.T, capacity int) (*Router, <-chan *framePeer) {
	t.Helper()
	peers := make(chan *framePeer, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, request, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		ack, _ := proto.Marshal(&pb.RouterEnvelope{
			Type:    pb.RouterMessageType_ROUTER_MESSAGE_TYPE_CONNECTION_ACK,
			Payload: &pb.RouterEnvelope_ConnectionAck{ConnectionAck: &pb.ConnectionAck{Success: true}},
		})
		if ws.WriteMessage(websocket.BinaryMessage, ack) != nil {
			return
		}
		peer := &framePeer{conn: ws, requests: make(chan *pb.SubscribeRequest, 4)}
		peers <- peer
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var env pb.RouterEnvelope
			if proto.Unmarshal(data, &env) != nil {
				return
			}
			if subscription := env.GetSubscribe(); subscription != nil {
				peer.requests <- subscription
			}
		}
	}))
	t.Cleanup(server.Close)
	r, err := DialRouter(context.Background(), RouterConfig{Connection: lifecycleConfig(server.URL), FrameBufferSize: capacity})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, peers
}

func frameReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before expected value")
		}
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream test value")
		var zero T
		return zero
	}
}

func frameWrite(t *testing.T, peer *framePeer, env *pb.RouterEnvelope) {
	t.Helper()
	data, err := proto.Marshal(env)
	if err == nil {
		err = peer.conn.WriteMessage(websocket.BinaryMessage, data)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func frameAck(t *testing.T, peer *framePeer, accepted ...bool) {
	t.Helper()
	ack := &pb.SubscribeAck{}
	for _, ok := range accepted {
		ack.Results = append(ack.Results, &pb.PairFilterResult{Ok: ok})
	}
	frameWrite(t, peer, &pb.RouterEnvelope{Type: pb.RouterMessageType_ROUTER_MESSAGE_TYPE_SUBSCRIBE_ACK, Payload: &pb.RouterEnvelope_SubscribeAck{SubscribeAck: ack}})
}

func frameSend(t *testing.T, peer *framePeer, value *pb.MarketFrame) {
	t.Helper()
	frameWrite(t, peer, &pb.RouterEnvelope{Type: pb.RouterMessageType_ROUTER_MESSAGE_TYPE_FRAME_UPDATE, Payload: &pb.RouterEnvelope_Update{Update: value}})
}

func streamFrame(t *testing.T, pairID, updatedAt uint32) *pb.MarketFrame {
	t.Helper()
	prepared, err := frame.Prepare(frame.FrameSpec{
		ChainID: 1, Protocol: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		Maker: common.HexToAddress("0x2222222222222222222222222222222222222222"),
		Pair: frame.PairParams{
			PairID:    pairID,
			BaseToken: common.HexToAddress("0x0000000000000000000000000000000000000001"), QuoteToken: common.HexToAddress("0x0000000000000000000000000000000000000002"),
			PriceTickSize: big.NewInt(1e18), LotSize: big.NewInt(1),
		},
		BaseTick: 500, Bids: []frame.Level{{Offset: 2, QtyLots: 100}}, Asks: []frame.Level{{Offset: 3, QtyLots: 50}},
		UpdatedAt: updatedAt, MajorVersion: 1, CodecVersion: frame.CodecVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.HexToECDSA("ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80")
	if err != nil {
		t.Fatal(err)
	}
	payload := prepared.SigningPayload()
	sig, err := crypto.Sign(payload.Digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	value, err := prepared.AttachSignature(sig, crypto.PubkeyToAddress(key.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type frameSubscription struct {
	frames <-chan FrameUpdate
	result SubscriptionResult
	err    error
}

func frameSubscribe(r *Router, filters []*PairFilter) <-chan frameSubscription {
	completed := make(chan frameSubscription, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var out frameSubscription
		out.frames, out.result, out.err = r.Subscribe(ctx, filters)
		completed <- out
	}()
	return completed
}

func frameSubscribeAck(t *testing.T, r *Router, peer *framePeer, filters []*PairFilter) frameSubscription {
	t.Helper()
	pending := frameSubscribe(r, filters)
	request := frameReceive(t, peer.requests)
	accepted := make([]bool, len(request.Filters))
	for i := range accepted {
		accepted[i] = true
	}
	frameAck(t, peer, accepted...)
	out := frameReceive(t, pending)
	if out.err != nil {
		t.Fatal(out.err)
	}
	return out
}

func frameWaitQueued(t *testing.T, r *Router, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.Stats().FrameQueueLength != count {
		if time.Now().After(deadline) {
			t.Fatalf("frame queue length = %d, want %d", r.Stats().FrameQueueLength, count)
		}
		time.Sleep(time.Millisecond)
	}
}

func frameAssertEmptyOpen(t *testing.T, frames <-chan FrameUpdate) {
	t.Helper()
	select {
	case value, ok := <-frames:
		t.Fatalf("expected empty open frame channel, received %+v, open=%t", value, ok)
	default:
	}
}

func TestRouterFrameFilteringFIFOAndOwnership(t *testing.T) {
	r, peers := frameTestRouter(t, 8)
	peer := frameReceive(t, peers)
	first := streamFrame(t, 7, 1000)
	filters := []*PairFilter{
		{Pair: &PairKey{ChainId: 1, PairId: 7}, Maker: first.MakerAddr},
		{Pair: &PairKey{ChainId: 1, PairId: 8}},
	}
	pending := frameSubscribe(r, filters)
	frameReceive(t, peer.requests)
	frameSend(t, peer, &pb.MarketFrame{}) // Even invalid pre-ack frames are discarded.
	frameSend(t, peer, first)
	frameAck(t, peer, true, false)
	sub := frameReceive(t, pending)
	if sub.err != nil || len(sub.result.Results) != 2 || sub.result.Results[1].Accepted {
		t.Fatalf("subscription: %+v", sub)
	}
	frameAssertEmptyOpen(t, sub.frames)
	frameSend(t, peer, streamFrame(t, 8, 1000)) // Rejected filter.
	otherMaker := proto.Clone(first).(*pb.MarketFrame)
	otherMaker.MakerAddr = "0x3333333333333333333333333333333333333333"
	frameSend(t, peer, otherMaker)
	frameSend(t, peer, first)
	frameSend(t, peer, streamFrame(t, 7, 1001))
	one, two := frameReceive(t, sub.frames), frameReceive(t, sub.frames)
	if one.Frame.UpdatedAt != 1000 || two.Frame.UpdatedAt != 1001 || one.Key != two.Key {
		t.Fatalf("matching frames were filtered or coalesced: %+v, %+v", one, two)
	}
	if one.Generation != sub.result.Generation || one.SubscriptionRevision != sub.result.SubscriptionRevision {
		t.Fatalf("frame provenance differs from acknowledgement: %+v, %+v", one, sub.result)
	}
	one.Frame.Pair.PairId = 99
	one.Frame.Frame.Header[0] = 99
	one.Frame.Bids[0].Price = "changed by caller"
	frameSend(t, peer, first)
	again := frameReceive(t, sub.frames)
	if !proto.Equal(again.Frame, first) || two.Frame.Pair.PairId != 7 {
		t.Fatal("caller mutation affected another delivered frame")
	}
	frameAssertEmptyOpen(t, sub.frames)
}

func TestRouterForwardsFrameWithoutLocalValidation(t *testing.T) {
	r, peers := frameTestRouter(t, 1)
	peer := frameReceive(t, peers)
	value := &pb.MarketFrame{
		Pair: &pb.PairKey{
			ChainId: 1, PairId: 7,
			BaseToken:  "0x0000000000000000000000000000000000000001",
			QuoteToken: "0x0000000000000000000000000000000000000002",
		},
		MakerAddr: "0x2222222222222222222222222222222222222222",
		UpdatedAt: 1000,
		// The server owns validation. Forward payloads even when this SDK's
		// compact codec, signature parser and decimal parser cannot read them.
		Frame: &pb.CompactFrame{
			Header: []byte("opaque header"), BidLevels: []byte("opaque bids"), AskLevels: []byte("opaque asks"),
		},
		Signature: []byte("opaque signature"),
		Bids:      []*pb.PriceLevel{{Price: "server bid", Amount: "server amount"}},
		Asks:      []*pb.PriceLevel{{Price: "server ask", Amount: "server amount"}},
	}
	sub := frameSubscribeAck(t, r, peer, []*PairFilter{{
		Pair: &PairKey{ChainId: value.Pair.ChainId, PairId: value.Pair.PairId}, Maker: value.MakerAddr,
	}})
	frameSend(t, peer, value)
	update := frameReceive(t, sub.frames)
	wantKey := FrameKey{ChainID: 1, PairID: 7, Maker: common.HexToAddress(value.MakerAddr)}
	if update.Key != wantKey || !proto.Equal(update.Frame, value) {
		t.Fatalf("forwarded frame differs from server payload: %+v", update)
	}
	if update.Generation != sub.result.Generation || update.SubscriptionRevision != sub.result.SubscriptionRevision {
		t.Fatalf("frame provenance differs from acknowledgement: %+v, %+v", update, sub.result)
	}
}

func TestRouterFrameInvalidationAndReconnect(t *testing.T) {
	r, peers := frameTestRouter(t, 8)
	peer := frameReceive(t, peers)
	initial := frameSubscribeAck(t, r, peer, []*PairFilter{{}})
	value := streamFrame(t, 7, 1000)
	frameSend(t, peer, value)
	retained := frameReceive(t, initial.frames)
	frameSend(t, peer, value)
	frameWaitQueued(t, r, 1)
	filters := []*PairFilter{{Pair: &PairKey{ChainId: 1, PairId: 7}}}
	pending := frameSubscribe(r, filters)
	frameReceive(t, peer.requests) // Replacement has started writing and invalidated old frames.
	frameAssertEmptyOpen(t, initial.frames)
	if retained.SubscriptionRevision == r.Status().SubscriptionRevision {
		t.Fatal("caller cannot identify a frame retained across replacement")
	}
	frameSend(t, peer, value)
	frameAck(t, peer, true)
	replaced := frameReceive(t, pending)
	if replaced.err != nil || replaced.frames != initial.frames {
		t.Fatalf("replacement did not return the shared channel: %+v", replaced)
	}
	frameAssertEmptyOpen(t, initial.frames)
	frameSend(t, peer, value)
	frameWaitQueued(t, r, 1)
	if ch, _, err := r.Subscribe(context.Background(), nil); err == nil || ch != nil {
		t.Fatal("invalid subscription must return nil channel and an error")
	}
	if r.Stats().FrameQueueLength != 1 {
		t.Fatal("local validation failure discarded queued frames")
	}
	connected := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		connected <- r.Reconnect(ctx)
	}()
	next := frameReceive(t, peers)
	restored := frameReceive(t, next.requests)
	if len(restored.Filters) != 1 || !proto.Equal(restored.Filters[0], filters[0]) {
		t.Fatalf("restored request = %v", restored)
	}
	frameAssertEmptyOpen(t, initial.frames)
	frameSend(t, next, value)
	frameAck(t, next, true)
	if err := frameReceive(t, connected); err != nil {
		t.Fatal(err)
	}
	frameAssertEmptyOpen(t, initial.frames)
	frameSend(t, next, value)
	current := frameReceive(t, initial.frames)
	status := r.Status()
	if current.Generation != status.Generation || current.Generation == retained.Generation || current.SubscriptionRevision != status.SubscriptionRevision {
		t.Fatalf("restored frame provenance: %+v, status %+v", current, status)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-initial.frames; ok {
		t.Fatal("frame channel remained open after full teardown")
	}
}

func TestRouterFrameOverflowStopsAndClosesChannel(t *testing.T) {
	r, peers := frameTestRouter(t, 1)
	peer := frameReceive(t, peers)
	sub := frameSubscribeAck(t, r, peer, []*PairFilter{{}})
	value := streamFrame(t, 7, 1000)
	frameSend(t, peer, value)
	frameSend(t, peer, value) // Repeated keys also consume buffer slots.
	waitStopped(t, r.Done())
	if !errors.Is(r.Err(), ErrFrameBufferFull) || r.Status().Phase != Stopped {
		t.Fatalf("frame overflow did not report terminal cause: %v, %+v", r.Err(), r.Status())
	}
	if _, ok := <-sub.frames; ok {
		t.Fatal("overflow teardown retained queued frames or left channel open")
	}
	if r.Stats().FrameQueueLength != 0 || r.Stats().Reconnects != 0 {
		t.Fatalf("overflow retried or retained frames: %+v", r.Stats())
	}
}

func TestRouterFrameBufferConfiguration(t *testing.T) {
	cfg, err := normalizeRouter(RouterConfig{Connection: ConnectionConfig{APIKey: "secret"}})
	if err != nil || cfg.FrameBufferSize != 256 {
		t.Fatalf("frame buffer default = %d, error %v", cfg.FrameBufferSize, err)
	}
	cfg.FrameBufferSize = -1
	if _, err := normalizeRouter(cfg); err == nil {
		t.Fatal("negative frame buffer accepted")
	}
}
