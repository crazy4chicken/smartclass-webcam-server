package ws

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// RecordingHandler receives a recording frame pushed by a device. Handlers run
// on the device's read goroutine and must not block for long.
type RecordingHandler func(deviceID, streamID string, cameraEnum int, seq int64, ts time.Time, data []byte)

// PhotoHandler receives a still image pushed by a device. Handlers run on the
// device's read goroutine and must not block for long.
type PhotoHandler func(deviceID string, cameraEnum int, requestID, contentType string, ts time.Time, data []byte)

// Errors reported by Hub.Send and Client acknowledgements.
var (
	// ErrDeviceNotConnected is returned when the device has no live connection.
	ErrDeviceNotConnected = errors.New("device not connected")
	// ErrClientClosed is returned when the device connection is shutting down.
	ErrClientClosed = errors.New("client closed")
	// ErrSendBufferFull is returned when the client's outbound buffer is full.
	ErrSendBufferFull = errors.New("client send buffer full")
)

// Hub tracks the live device connections and routes control messages to them.
// Clients are keyed by device id: a device holds at most one live connection.
type Hub struct {
	mu        sync.RWMutex
	clients   map[string]*Client
	recording RecordingHandler
	photo     PhotoHandler
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{clients: make(map[string]*Client)}
}

// Run blocks until the process exits. Clients are registered and removed
// synchronously under the hub mutex, so Run only keeps the long-lived server
// goroutine it is started in alive.
func (h *Hub) Run() {
	select {}
}

// Register adds c to the hub. An existing live connection of the same device is
// replaced and force-closed: the newest connection owns the device.
func (h *Hub) Register(c *Client) {
	if c == nil {
		return
	}

	h.mu.Lock()
	old := h.clients[c.deviceID]
	h.clients[c.deviceID] = c
	h.mu.Unlock()

	if old != nil && old != c {
		slog.Warn("replacing live device connection", "device_id", c.deviceID)
		old.Close()
	}
	slog.Info("device connected", "device_id", c.deviceID)
}

// Remove drops c from the hub and closes it. It is a no-op when c was already
// replaced by a newer connection of the same device.
func (h *Hub) Remove(c *Client) {
	if c == nil {
		return
	}

	h.mu.Lock()
	current, ok := h.clients[c.deviceID]
	if !ok || current != c {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c.deviceID)
	h.mu.Unlock()

	c.Close()
	slog.Info("device disconnected", "device_id", c.deviceID)
}

// Send queues msg for the device identified by deviceID. It returns
// ErrDeviceNotConnected when the device is offline, ErrSendBufferFull when its
// queue is saturated and ErrClientClosed while the connection shuts down.
func (h *Hub) Send(deviceID string, msg Message) error {
	c, ok := h.Client(deviceID)
	if !ok {
		return fmt.Errorf("device %s: %w", deviceID, ErrDeviceNotConnected)
	}
	if err := c.send(msg); err != nil {
		return fmt.Errorf("device %s: %w", deviceID, err)
	}
	return nil
}

// Client returns the live connection of deviceID.
func (h *Hub) Client(deviceID string) (*Client, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	c, ok := h.clients[deviceID]
	return c, ok
}

// SetRecordingHandler replaces the handler invoked for recording frames.
func (h *Hub) SetRecordingHandler(fn RecordingHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recording = fn
}

// SetPhotoHandler replaces the handler invoked for photo frames.
func (h *Hub) SetPhotoHandler(fn PhotoHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.photo = fn
}

// dispatchRecording delivers a recording frame to the registered handler.
func (h *Hub) dispatchRecording(deviceID, streamID string, cameraEnum int, seq int64, ts time.Time, data []byte) {
	h.mu.RLock()
	fn := h.recording
	h.mu.RUnlock()

	if fn != nil {
		fn(deviceID, streamID, cameraEnum, seq, ts, data)
	}
}

// dispatchPhoto delivers a still image to the registered handler.
func (h *Hub) dispatchPhoto(deviceID string, cameraEnum int, requestID, contentType string, ts time.Time, data []byte) {
	h.mu.RLock()
	fn := h.photo
	h.mu.RUnlock()

	if fn != nil {
		fn(deviceID, cameraEnum, requestID, contentType, ts, data)
	}
}
