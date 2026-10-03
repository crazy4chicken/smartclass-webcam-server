package ws

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait is the deadline for a single write to the camera.
	writeWait = 10 * time.Second
	// pongWait is how long a camera may stay silent before the connection is
	// considered dead. It must exceed pingInterval.
	pongWait = 60 * time.Second
	// pingInterval is how often the server pings the camera.
	pingInterval = 30 * time.Second
	// sendBufferSize is the outbound command queue depth per camera.
	sendBufferSize = 16
	// maxMessageSize bounds an inbound message; frames are base64 JPEG data.
	maxMessageSize = 16 << 20
)

// Client is a single camera WebSocket connection registered with a Hub.
type Client struct {
	CameraID string
	Conn     *websocket.Conn
	Hub      *Hub
	Send     chan Message

	mu     sync.Mutex
	closed bool
}

// NewClient wraps conn as a camera client. The returned client is not yet
// registered with the hub.
func NewClient(cameraID string, conn *websocket.Conn, hub *Hub) *Client {
	return &Client{
		CameraID: cameraID,
		Conn:     conn,
		Hub:      hub,
		Send:     make(chan Message, sendBufferSize),
	}
}

// send queues msg for delivery without blocking the caller.
func (c *Client) send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClientClosed
	}
	select {
	case c.Send <- msg:
		return nil
	default:
		return ErrSendBufferFull
	}
}

// close marks the client as closed and closes Send so its write pump stops. It
// is safe to call multiple times.
func (c *Client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	close(c.Send)
}

// ReadPump reads messages from the camera until the connection fails or closes,
// then unregisters the client and closes the connection. It must run in its own
// goroutine.
func (c *Client) ReadPump() {
	defer func() {
		// Remove synchronously from the hub map so the camera status can
		// be updated immediately after ReadPump returns.
		c.Hub.removeClient(c)
		_ = c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		return c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var msg Message
		if err := c.Conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseNoStatusReceived,
			) {
				slog.Warn("camera read failed", "camera_id", c.CameraID, "error", err)
			}
			return
		}
		// Any inbound traffic proves the peer is alive.
		_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))

		switch msg.Type {
		case MsgFrame:
			c.handleFrame(msg)
		case MsgPong:
			// Keepalive only; the read deadline was already reset above.
		case MsgStreamStarted, MsgStreamStopped:
			slog.Info("camera stream state changed",
				"camera_id", c.CameraID,
				"type", msg.Type,
				"stream_id", stringField(msg.Payload, payloadKeyStreamID),
			)
		case MsgStatus:
			slog.Info("camera status", "camera_id", c.CameraID, "payload", msg.Payload)
		case MsgError:
			slog.Warn("camera reported an error", "camera_id", c.CameraID, "payload", msg.Payload)
		default:
			slog.Debug("ignoring unknown camera message", "camera_id", c.CameraID, "type", msg.Type)
		}
	}
}

// handleFrame decodes and dispatches a single frame message.
func (c *Client) handleFrame(msg Message) {
	streamID := stringField(msg.Payload, payloadKeyStreamID)
	if streamID == "" {
		slog.Warn("discarding frame without stream_id", "camera_id", c.CameraID)
		return
	}

	encoded := stringField(msg.Payload, payloadKeyData)
	if encoded == "" {
		slog.Warn("discarding frame without data", "camera_id", c.CameraID, "stream_id", streamID)
		return
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		slog.Warn("discarding frame with invalid base64 data",
			"camera_id", c.CameraID, "stream_id", streamID, "error", err)
		return
	}

	ts := time.Now().UTC()
	if raw := stringField(msg.Payload, payloadKeyTS); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			slog.Debug("frame timestamp not RFC3339, using receive time",
				"camera_id", c.CameraID, "stream_id", streamID, "ts", raw, "error", err)
		} else {
			ts = parsed
		}
	}

	c.Hub.dispatchFrame(c.CameraID, streamID, intField(msg.Payload, payloadKeySeq), data, ts)
}

// WritePump writes queued messages and periodic pings to the camera, then
// closes the connection when the client is closed or a write fails. It must run
// in its own goroutine.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		_ = c.Conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.Send:
			if !ok {
				// The hub closed Send while unregistering this client.
				_ = c.Conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(writeWait))
				return
			}
			if err := c.writeJSON(msg); err != nil {
				slog.Warn("camera write failed", "camera_id", c.CameraID, "error", err)
				return
			}
		case <-ticker.C:
			if err := c.ping(); err != nil {
				slog.Warn("camera ping failed", "camera_id", c.CameraID, "error", err)
				return
			}
		}
	}
}

// ping sends a protocol-level ping followed by an application-level ping
// message so the camera keeps both its socket and its read deadline alive.
func (c *Client) ping() error {
	if err := c.Conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.writeJSON(Message{
		Type:    MsgPing,
		Payload: map[string]any{payloadKeyTS: time.Now().UTC().Format(time.RFC3339Nano)},
	})
}

// writeJSON writes msg with a fresh write deadline.
func (c *Client) writeJSON(msg Message) error {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.Conn.WriteJSON(msg)
}

// stringField returns the string value at key, or "" when absent or not a string.
func stringField(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}

// intField returns the numeric value at key as an int, or 0 when absent or not
// a number.
func intField(payload map[string]any, key string) int {
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}
