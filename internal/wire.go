package stream

import (
	"fmt"
	"time"

	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func hasUnknownFields(m protoreflect.Message) bool {
	if len(m.GetUnknown()) > 0 {
		return true
	}
	bad := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsList() && fd.Message() != nil {
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				if hasUnknownFields(list.Get(i).Message()) {
					bad = true
					return false
				}
			}
		} else if fd.Message() != nil && !fd.IsMap() {
			bad = hasUnknownFields(v.Message())
		}
		return !bad
	})
	return bad
}

func decode(data []byte, m proto.Message) error {
	if err := proto.Unmarshal(data, m); err != nil {
		return fmt.Errorf("%w: invalid protobuf: %v", ErrProtocol, err)
	}
	if hasUnknownFields(m.ProtoReflect()) {
		return fmt.Errorf("%w: unknown protobuf fields", ErrProtocol)
	}
	return nil
}

func (c *client) connectionAck(data []byte) (string, error) {
	var ack *pb.ConnectionAck
	if c.maker {
		e := new(pb.MakerEnvelope)
		if err := decode(data, e); err != nil {
			return "", err
		}
		if e.Type != pb.MakerMessageType_MAKER_MESSAGE_TYPE_CONNECTION_ACK {
			return "", fmt.Errorf("%w: missing connection acknowledgement", ErrProtocol)
		}
		ack = e.GetConnectionAck()
	} else {
		e := new(pb.RouterEnvelope)
		if err := decode(data, e); err != nil {
			return "", err
		}
		if e.Type != pb.RouterMessageType_ROUTER_MESSAGE_TYPE_CONNECTION_ACK {
			return "", fmt.Errorf("%w: missing connection acknowledgement", ErrProtocol)
		}
		ack = e.GetConnectionAck()
	}
	if ack == nil {
		return "", fmt.Errorf("%w: missing connection acknowledgement payload", ErrProtocol)
	}
	if !ack.Success {
		return "", ErrAuthentication
	}
	if c.maker && ack.Identity == "" {
		return "", fmt.Errorf("%w: missing maker identity", ErrProtocol)
	}
	return ack.Identity, nil
}

func (c *client) sendHeartbeat(s *session, ping, pong bool) error {
	var msg proto.Message
	h := &pb.Heartbeat{Ping: ping, Pong: pong}
	if c.maker {
		msg = &pb.MakerEnvelope{
			Type:      pb.MakerMessageType_MAKER_MESSAGE_TYPE_HEARTBEAT,
			Timestamp: time.Now().UnixMilli(),
			Payload:   &pb.MakerEnvelope_Heartbeat{Heartbeat: h},
		}
	} else {
		msg = &pb.RouterEnvelope{
			Type:      pb.RouterMessageType_ROUTER_MESSAGE_TYPE_HEARTBEAT,
			Timestamp: time.Now().UnixMilli(),
			Payload:   &pb.RouterEnvelope_Heartbeat{Heartbeat: h},
		}
	}
	data, err := c.marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.write(s.ctx, s, data, nil)
	return err
}

func (c *client) heartbeat(s *session, h *pb.Heartbeat) error {
	if h == nil || (!h.Ping && !h.Pong) {
		return fmt.Errorf("%w: invalid heartbeat", ErrProtocol)
	}
	if h.Ping {
		return c.sendHeartbeat(s, false, true)
	}
	return nil
}

func (c *client) handle(s *session, data []byte) error {
	if c.maker {
		e := new(pb.MakerEnvelope)
		if err := decode(data, e); err != nil {
			return err
		}
		switch e.Type {
		case pb.MakerMessageType_MAKER_MESSAGE_TYPE_HEARTBEAT:
			return c.heartbeat(s, e.GetHeartbeat())
		case pb.MakerMessageType_MAKER_MESSAGE_TYPE_FRAME_ACK:
			a := e.GetFrameAck()
			if a == nil || a.Accepted || a.Pair == nil || a.MinorVersion > 255 {
				return fmt.Errorf("%w: invalid frame rejection", ErrProtocol)
			}
			c.mu.Lock()
			if c.current == s {
				c.emitMakerLocked(MakerEvent{Kind: FrameRejected, Rejection: a})
			}
			c.mu.Unlock()
			return nil
		default:
			return fmt.Errorf("%w: unexpected maker message", ErrProtocol)
		}
	}
	e := new(pb.RouterEnvelope)
	if err := decode(data, e); err != nil {
		return err
	}
	switch e.Type {
	case pb.RouterMessageType_ROUTER_MESSAGE_TYPE_HEARTBEAT:
		return c.heartbeat(s, e.GetHeartbeat())
	case pb.RouterMessageType_ROUTER_MESSAGE_TYPE_SUBSCRIBE_ACK:
		if e.GetSubscribeAck() == nil {
			return fmt.Errorf("%w: missing subscribe ack", ErrProtocol)
		}
		return c.router.ack(s, e.GetSubscribeAck())
	case pb.RouterMessageType_ROUTER_MESSAGE_TYPE_FRAME_UPDATE:
		if e.GetUpdate() == nil {
			return fmt.Errorf("%w: missing frame", ErrProtocol)
		}
		return c.router.update(s, e.GetUpdate())
	case pb.RouterMessageType_ROUTER_MESSAGE_TYPE_ERROR:
		if e.GetError() == nil || e.GetError().Code == "" {
			return fmt.Errorf("%w: invalid server error", ErrProtocol)
		}
		return c.router.serverError(s, e.GetError())
	default:
		return fmt.Errorf("%w: unexpected router message", ErrProtocol)
	}
}

func (c *client) marshal(message proto.Message) ([]byte, error) {
	// Check the encoded size before allocating the output buffer.
	if int64(proto.Size(message)) > c.cfg.MaxWriteMessageBytes {
		return nil, ErrMessageTooLarge
	}
	if hasUnknownFields(message.ProtoReflect()) {
		return nil, fmt.Errorf("%w: unknown protobuf fields", ErrProtocol)
	}
	return proto.Marshal(message)
}
