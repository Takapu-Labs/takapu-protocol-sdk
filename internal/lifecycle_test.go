package stream

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func lifecycleAck(ws *websocket.Conn) error {
	data, _ := proto.Marshal(&pb.MakerEnvelope{Type: pb.MakerMessageType_MAKER_MESSAGE_TYPE_CONNECTION_ACK, Payload: &pb.MakerEnvelope_ConnectionAck{ConnectionAck: &pb.ConnectionAck{Success: true, Identity: "maker"}}})
	return ws.WriteMessage(websocket.BinaryMessage, data)
}

func lifecycleConfig(url string) ConnectionConfig {
	return ConnectionConfig{URL: "ws" + strings.TrimPrefix(url, "http"), APIKey: "key", Reconnect: ReconnectConfig{Policy: Disabled}}
}

func waitStopped(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not stop")
	}
}

func TestHandshakeIncludesConnectionAckDeadline(t *testing.T) {
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		<-stop
	}))
	defer server.Close()
	defer close(stop)
	cfg := lifecycleConfig(server.URL)
	cfg.HandshakeTimeout = 30 * time.Millisecond
	started := time.Now()
	p, err := DialMaker(context.Background(), MakerConfig{Connection: cfg})
	if err == nil || p != nil {
		t.Fatal("missing ack was accepted")
	}
	if time.Since(started) > time.Second {
		t.Fatal("ack was outside handshake timeout")
	}
}

func TestHandshakeCancellationPreservesContextError(t *testing.T) {
	upgraded := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		close(upgraded)
		// Withhold the application acknowledgement until the client cancels.
		_, _, _ = ws.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		p, err := DialMaker(ctx, MakerConfig{Connection: lifecycleConfig(server.URL)})
		if p != nil {
			_ = p.Close()
		}
		result <- err
	}()
	select {
	case <-upgraded:
	case <-time.After(time.Second):
		t.Fatal("WebSocket upgrade did not complete")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake lost cancellation cause: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake did not stop after cancellation")
	}
}

func TestCancellationInterruptsHTTPUpgrade(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		// Leave the HTTP upgrade response pending on an established connection.
		<-release
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		p, err := DialMaker(ctx, MakerConfig{Connection: lifecycleConfig(server.URL)})
		if p != nil {
			_ = p.Close()
		}
		result <- err
	}()
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("HTTP upgrade request did not arrive")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HTTP upgrade lost cancellation cause: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled HTTP upgrade waited for the handshake timeout")
	}
}

func TestClassifiedCloseErrorPreservesCode(t *testing.T) {
	for _, test := range []struct {
		code  int
		cause error
	}{
		{websocket.ClosePolicyViolation, ErrAuthentication},
		{4001, ErrAuthentication},
		{websocket.CloseProtocolError, ErrProtocol},
		{websocket.CloseMessageTooBig, ErrProtocol},
	} {
		original := &websocket.CloseError{Code: test.code, Text: "server closed connection"}
		err := classifyReadError(original)
		var closeError *websocket.CloseError
		if !errors.Is(err, test.cause) || !errors.As(err, &closeError) || closeError != original {
			t.Fatalf("close code %d lost its cause: %v", test.code, err)
		}
	}
}

func TestHeartbeatAndReadIdleTimeout(t *testing.T) {
	ping := make(chan struct{}, 1)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if lifecycleAck(ws) != nil {
			return
		}
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			var env pb.MakerEnvelope
			if proto.Unmarshal(data, &env) == nil && env.GetHeartbeat().GetPing() {
				select {
				case ping <- struct{}{}:
				default:
				}
			}
		}
		<-stop
	}))
	defer server.Close()
	defer close(stop)
	cfg := lifecycleConfig(server.URL)
	cfg.HeartbeatInterval = 10 * time.Millisecond
	cfg.ReadIdleTimeout = 80 * time.Millisecond
	p, err := DialMaker(context.Background(), MakerConfig{Connection: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	select {
	case <-ping:
	case <-time.After(time.Second):
		t.Fatal("no application ping")
	}
	waitStopped(t, p.Done())
	var ne net.Error
	if !errors.As(p.Err(), &ne) || !ne.Timeout() {
		t.Fatalf("expected read timeout, got %v", p.Err())
	}
}

func TestReadTimeoutSurvivesSessionCancellationCleanup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if lifecycleAck(ws) != nil {
			return
		}
		// Read client heartbeats without replying, producing a real read timeout.
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	cfg := lifecycleConfig(server.URL)
	cfg.HeartbeatInterval = 5 * time.Millisecond
	cfg.ReadIdleTimeout = 30 * time.Millisecond
	cfg, err := normalizeConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(context.Background(), cfg, true)
	s, err := c.dial()
	if err != nil {
		c.cancel()
		t.Fatal(err)
	}
	if !c.install(s) {
		c.cancel()
		t.Fatal("could not install test session")
	}
	// An active write registers this cancellation callback. Force it to finish
	// during session teardown so the regression does not depend on whether a
	// short heartbeat write happens to overlap the read timeout.
	cleanupDone := make(chan struct{})
	context.AfterFunc(s.ctx, func() {
		c.discard(s, context.Canceled)
		close(cleanupDone)
	})
	cancelSession := s.cancel
	var waiting atomic.Bool
	s.cancel = func() {
		wait := waiting.CompareAndSwap(false, true)
		cancelSession()
		if wait {
			<-cleanupDone
		}
	}
	go c.run(s)
	defer c.Close()
	waitStopped(t, c.Done())
	var networkError net.Error
	if !errors.As(c.Err(), &networkError) || !networkError.Timeout() {
		t.Fatalf("session cancellation cleanup replaced the read timeout: %v", c.Err())
	}
}

func TestMessageLimitsAndReconnectCredentials(t *testing.T) {
	connections := make(chan *websocket.Conn, 4)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(APIKeyHeader) != "key" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if lifecycleAck(ws) != nil {
			return
		}
		connections <- ws
		<-stop
	}))
	defer server.Close()
	defer close(stop)
	cfg := lifecycleConfig(server.URL)
	cfg.MaxReadMessageBytes = 128
	cfg.MaxWriteMessageBytes = 128
	p, err := DialMaker(context.Background(), MakerConfig{Connection: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	<-connections
	if _, err = p.marshal(&pb.MakerEnvelope{Payload: &pb.MakerEnvelope_FramePush{FramePush: &pb.MarketFrame{Signature: make([]byte, 256)}}}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal("oversized send not rejected", err)
	}
	// Mutating the caller's config must not change credentials on reconnect.
	cfg.APIKey = "changed-after-dial"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = p.Reconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Status().Generation != 2 {
		t.Fatal(p.Status())
	}
	conn := <-connections
	_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 256))
	waitStopped(t, p.Done())
	if !errors.Is(p.Err(), ErrProtocol) {
		t.Fatal(p.Err())
	}
	if p.Stats().Reconnects != 1 {
		t.Fatal("oversized receive retried")
	}
}

func TestWriteCancellationBeforeAndAfterStarting(t *testing.T) {
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = lifecycleAck(ws)
		<-stop
	}))
	defer server.Close()
	defer close(stop)
	cfg := lifecycleConfig(server.URL)
	cfg.MaxWriteMessageBytes = 32 << 20
	p, err := DialMaker(context.Background(), MakerConfig{Connection: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.mu.Lock()
	s := p.current
	p.mu.Unlock()
	<-s.writeGate
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started, err := p.write(ctx, s, []byte{1}, nil)
	if started || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(started, err)
	}
	s.writeGate <- struct{}{}
	if p.Status().Phase != Connected {
		t.Fatal("queued cancel discarded connection")
	}
	if tcp, ok := s.conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(1024)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	began := make(chan struct{})
	result := make(chan struct {
		started bool
		err     error
	}, 1)
	go func() {
		started, err := p.write(ctx2, s, bytes.Repeat([]byte{1}, 16<<20), func() error {
			close(began)
			return nil
		})
		result <- struct {
			started bool
			err     error
		}{started, err}
	}()
	<-began
	cancel2()
	select {
	case out := <-result:
		if !out.started || !errors.Is(out.err, context.Canceled) {
			t.Fatal(out)
		}
	case <-time.After(time.Second):
		t.Fatal("active write not interrupted")
	}
	waitStopped(t, p.Done())
	if p.Err() == nil {
		t.Fatal("active write cancel lost cause")
	}
}

func TestConnectionConfigDefaultsAndInvalidCombinations(t *testing.T) {
	base := ConnectionConfig{URL: "wss://example.test/ws", APIKey: "secret"}
	cfg, err := normalizeConnection(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HandshakeTimeout != 10*time.Second || cfg.WriteTimeout != 5*time.Second || cfg.EventBufferSize != 256 || cfg.MaxReadMessageBytes != 512<<10 || cfg.MaxWriteMessageBytes != 512<<10 || cfg.HeartbeatInterval != 10*time.Second || cfg.ReadIdleTimeout != 30*time.Second || cfg.Reconnect.InitialDelay != 500*time.Millisecond || cfg.Reconnect.MaxDelay != 30*time.Second || cfg.Reconnect.ResetAfter != 60*time.Second || cfg.Reconnect.Policy != Enabled {
		t.Fatal(cfg)
	}
	for _, mutate := range []func(*ConnectionConfig){func(c *ConnectionConfig) { c.ReadIdleTimeout = time.Second }, func(c *ConnectionConfig) { c.WriteTimeout = -1 }, func(c *ConnectionConfig) { c.EventBufferSize = -1 }, func(c *ConnectionConfig) { c.MaxReadMessageBytes = -1 }, func(c *ConnectionConfig) { c.Reconnect.MaxDelay = time.Millisecond }, func(c *ConnectionConfig) { c.Reconnect.Policy = 99 }} {
		cfg := base
		mutate(&cfg)
		if _, err := normalizeConnection(cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestConnectionConfigPreservesURL(t *testing.T) {
	for _, endpoint := range []string{
		"wss://host/custom/path?region=a%2Fb&region=c",
		"wss://user:password@host/ws#fragment",
		"wss://host/ws?",
		"https://host/ws",
		"/relative/path",
		" malformed URL% ",
		"",
	} {
		t.Run(endpoint, func(t *testing.T) {
			cfg, err := normalizeConnection(ConnectionConfig{URL: endpoint, APIKey: "secret"})
			if err != nil {
				t.Fatalf("configuration rejected URL before dialing: %v", err)
			}
			if cfg.URL != endpoint {
				t.Fatalf("URL = %q, want %q", cfg.URL, endpoint)
			}
		})
	}
}

func TestClientEndpointDefaults(t *testing.T) {
	for _, endpoint := range []string{"", "ws://localhost/custom?region=local", " malformed URL% "} {
		t.Run(endpoint, func(t *testing.T) {
			connection := ConnectionConfig{URL: endpoint, APIKey: "key"}
			maker, err := normalizeMaker(MakerConfig{Connection: connection})
			if err != nil {
				t.Fatal(err)
			}
			router, err := normalizeRouter(RouterConfig{Connection: connection})
			if err != nil {
				t.Fatal(err)
			}
			wantMaker, wantRouter := endpoint, endpoint
			if endpoint == "" {
				wantMaker = "wss://api.takapu.org/marketstream/maker"
				wantRouter = "wss://api.takapu.org/marketstream/router"
			}
			if maker.Connection.URL != wantMaker || router.Connection.URL != wantRouter {
				t.Fatalf("maker URL=%q router URL=%q, want %q and %q", maker.Connection.URL, router.Connection.URL, wantMaker, wantRouter)
			}
			if connection.URL != endpoint || maker.Connection.APIKey != connection.APIKey || router.Connection.APIKey != connection.APIKey {
				t.Fatal("endpoint defaults changed caller configuration or credentials")
			}
		})
	}
}
