package ws

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait is the deadline for a single write to a device.
	writeWait = 10 * time.Second
	// pongWait is how long a device may stay silent before the connection is
	// considered dead. It must exceed pingInterval.
	pongWait = 60 * time.Second
	// pingInterval is how often the server pings the device.
	pingInterval = 30 * time.Second
	// sendBufferSize is the outbound command queue depth per device.
	sendBufferSize = 16
	// maxMessageSize bounds an inbound message and therefore the size of a
	// single media frame.
	maxMessageSize = 16 << 20
)

// Client is a single device WebSocket connection registered with a Hub.
type Client struct {
	deviceID string
	conn     *websocket.Conn
	hub      *Hub
	outbound chan Message

	mu     sync.Mutex
	closed bool
}

// NewClient wraps conn as the connection of deviceID. The returned client is
// not yet registered with the hub.
func NewClient(deviceID string, conn *websocket.Conn, hub *Hub) *Client {
	return &Client{
		deviceID: deviceID,
		conn:     conn,
		hub:      hub,
		outbound: make(chan Message, sendBufferSize),
	}
}

// DeviceID returns the device this connection belongs to.
func (c *Client) DeviceID() string {
	return c.deviceID
}

// send queues msg for delivery without blocking the caller.
func (c *Client) send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClientClosed
	}
	select {
	case c.outbound <- msg:
		return nil
	default:
		return ErrSendBufferFull
	}
}

// Close marks the client closed, stops its write pump and force-closes the
// underlying connection. It is safe to call more than once.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	close(c.outbound)
	c.mu.Unlock()

	_ = c.conn.Close()
}

// ReadPump reads from the device until the connection fails or closes. Text
// frames carry control messages and binary frames carry media; both are
// dispatched on this goroutine, so handlers must not block. ReadPump blocks and
// is meant to run on the connection's own goroutine.
func (c *Client) ReadPump() {
	defer func() {
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		messageType, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
				websocket.ClosePolicyViolation,
			) {
				slog.Warn("device read failed", "device_id", c.deviceID, "error", err)
			}
			return
		}
		// Any inbound traffic proves the peer is still alive.
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

		switch messageType {
		case websocket.BinaryMessage:
			c.handleBinary(raw)
		case websocket.TextMessage:
			c.handleText(raw)
		}
	}
}

// handleText dispatches one control message received from the device.
func (c *Client) handleText(raw []byte) {
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		slog.Debug("discarding malformed control message", "device_id", c.deviceID, "error", err)
		return
	}

	switch msg.Type {
	case ControlPong:
		// Keepalive only; the read deadline was already refreshed.
	case ControlAck:
		slog.Debug("device acknowledged command", "device_id", c.deviceID, "id", msg.ID, "payload", msg.Payload)
		c.hub.dispatchAck(c.deviceID, msg.ID, msg.Payload)
	case ControlStatus:
		slog.Info("device status", "device_id", c.deviceID, "payload", msg.Payload)
	case ControlError:
		slog.Info("device reported an error", "device_id", c.deviceID, "id", msg.ID, "payload", msg.Payload)
	default:
		slog.Debug("ignoring unknown control message", "device_id", c.deviceID, "type", msg.Type)
	}
}

// handleBinary dispatches one decoded media frame to the hub handlers.
func (c *Client) handleBinary(raw []byte) {
	msg, data, err := DecodeBinary(raw)
	if err != nil {
		slog.Debug("discarding malformed media frame", "device_id", c.deviceID, "error", err)
		return
	}

	switch msg.Channel {
	case ChannelRecording:
		if msg.Type != MediaFrame {
			slog.Debug("ignoring unknown recording message", "device_id", c.deviceID, "type", msg.Type)
			return
		}
		streamID := stringField(msg.Payload, PKStreamID)
		cameraEnum, ok := numberField(msg.Payload, PKCameraEnum)
		if streamID == "" || !ok {
			slog.Debug("discarding frame without stream_id or camera_enum",
				"device_id", c.deviceID, "stream_id", streamID, "payload", msg.Payload)
			return
		}
		seq, _ := numberField(msg.Payload, PKSeq)
		c.hub.dispatchRecording(c.deviceID, streamID, int(cameraEnum), seq, timeField(msg.Payload, PKTS), data)

	case ChannelPhoto:
		if msg.Type != MediaPhoto {
			slog.Debug("ignoring unknown photo message", "device_id", c.deviceID, "type", msg.Type)
			return
		}
		cameraEnum, ok := numberField(msg.Payload, PKCameraEnum)
		if !ok {
			slog.Debug("discarding photo without camera_enum", "device_id", c.deviceID, "payload", msg.Payload)
			return
		}
		c.hub.dispatchPhoto(c.deviceID, int(cameraEnum),
			stringField(msg.Payload, PKRequestID),
			stringField(msg.Payload, PKContentType),
			timeField(msg.Payload, PKTS),
			data)

	default:
		slog.Debug("ignoring media frame on unknown channel",
			"device_id", c.deviceID, "channel", msg.Channel, "type", msg.Type)
	}
}

// WritePump writes queued commands and periodic keepalives to the device. It
// stops when the client is closed or a write fails, and is meant to run on its
// own goroutine.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.outbound:
			if !ok {
				// The client was closed while unregistering.
				_ = c.conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(writeWait))
				return
			}
			if err := c.writeJSON(msg); err != nil {
				slog.Warn("device write failed", "device_id", c.deviceID, "error", err)
				return
			}
		case <-ticker.C:
			if err := c.ping(); err != nil {
				slog.Warn("device ping failed", "device_id", c.deviceID, "error", err)
				return
			}
		}
	}
}

// ping sends a protocol-level ping followed by an application-level control
// ping so the device keeps both its socket and its read deadline alive.
func (c *Client) ping() error {
	if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.writeJSON(Message{
		Channel: ChannelControl,
		Type:    ControlPing,
		Payload: map[string]any{PKTS: time.Now().UTC().Format(time.RFC3339Nano)},
	})
}

// writeJSON writes msg with a fresh write deadline.
func (c *Client) writeJSON(msg Message) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.conn.WriteJSON(msg)
}

// stringField returns the string value at key, or "" when it is absent or not a
// string.
func stringField(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}

// numberField returns the numeric value at key as an int64. It reports false
// when the key is absent or not a number.
func numberField(payload map[string]any, key string) (int64, bool) {
	switch v := payload[key].(type) {
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case int:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

// timeField returns the RFC3339Nano timestamp at key, or the zero time when it
// is absent or malformed.
func timeField(payload map[string]any, key string) time.Time {
	raw := stringField(payload, key)
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		slog.Debug("media timestamp is not RFC3339Nano", "ts", raw, "error", err)
		return time.Time{}
	}
	return ts
}
