package stream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestReviewUnprefixedMakerFilterMatchesAddress(t *testing.T) {
	address := common.HexToAddress("0x1111111111111111111111111111111111111111")
	r := &Router{accepted: []*PairFilter{{Maker: strings.TrimPrefix(address.Hex(), "0x")}}}
	if !r.matchesLocked(FrameKey{ChainID: 56, PairID: 7, Maker: address}) {
		t.Fatal("a valid accepted maker address without the optional 0x prefix must match its on-chain address")
	}
}

func TestReviewProtocolErrorDominatesHeartbeatCancellation(t *testing.T) {
	var connections atomic.Int32
	invalid := make(chan struct{})
	finish := make(chan struct{})
	reconnected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		n := connections.Add(1)
		ack, _ := proto.Marshal(&pb.MakerEnvelope{
			Type:    pb.MakerMessageType_MAKER_MESSAGE_TYPE_CONNECTION_ACK,
			Payload: &pb.MakerEnvelope_ConnectionAck{ConnectionAck: &pb.ConnectionAck{Success: true, Identity: "maker-identity"}},
		})
		if err := ws.WriteMessage(websocket.BinaryMessage, ack); err != nil {
			return
		}
		if n == 1 {
			<-invalid
			_ = ws.WriteMessage(websocket.TextMessage, []byte("invalid protocol message"))
		} else {
			select {
			case reconnected <- struct{}{}:
			default:
			}
		}
		<-finish
	}))
	defer srv.Close()
	defer close(finish)
	cfg := MakerConfig{Connection: ConnectionConfig{
		URL: "ws" + strings.TrimPrefix(srv.URL, "http"), APIKey: "test-secret",
		HeartbeatInterval: 5 * time.Millisecond, ReadIdleTimeout: time.Second,
		Reconnect: ReconnectConfig{InitialDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}}
	p, err := DialMaker(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.mu.Lock()
	s := p.current
	p.mu.Unlock()
	// Hold the data writer so the heartbeat is waiting when the read loop detects
	// a protocol violation. Session teardown cancels that wait with Disconnected;
	// this secondary error must never override the terminal protocol failure.
	<-s.writeGate
	time.Sleep(30 * time.Millisecond)
	close(invalid)
	select {
	case <-p.Done():
		if !errors.Is(p.Err(), ErrProtocol) {
			t.Fatalf("terminal protocol cause lost: %v", p.Err())
		}
	case <-reconnected:
		t.Fatal("a secondary heartbeat cancellation converted a terminal protocol error into an automatic reconnect")
	case <-time.After(time.Second):
		t.Fatal("client failed to stop after protocol error")
	}
}
