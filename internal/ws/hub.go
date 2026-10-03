package ws

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// FrameHandler receives a decoded video frame pushed by a camera over its
// WebSocket connection. Handlers run on the camera's read goroutine and must
// not block for long.
type FrameHandler func(cameraID, streamID string, seq int, data []byte, ts time.Time)

// Errors reported by Hub.SendCommand.
var (
	// ErrCameraNotConnected is returned when the camera has no live connection.
	ErrCameraNotConnected = errors.New("camera not connected")
	// ErrClientClosed is returned when the camera connection is shutting down.
	ErrClientClosed = errors.New("client closed")
	// ErrSendBufferFull is returned when the client's outbound buffer is full.
	ErrSendBufferFull = errors.New("client send buffer full")
)

const (
	registerQueueSize   = 64
	unregisterQueueSize = 64
)

// Hub tracks the live camera connections and routes messages between the HTTP
// layer and the cameras. A Hub must be started with Run before clients register.
type Hub struct {
	clients       map[string]*Client
	register      chan *Client
	unregister    chan *Client
	frameHandlers []FrameHandler
	mu            sync.RWMutex
}

// NewHub creates an empty hub. Call Run in its own goroutine to process
// registration events.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		register:   make(chan *Client, registerQueueSize),
		unregister: make(chan *Client, unregisterQueueSize),
	}
}

// Run processes client registrations and unregistrations until the process
// exits. It must be running for Register, Unregister and SendCommand to work.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.handleRegister(client)
		case client := <-h.unregister:
			if h.removeClient(client) {
				slog.Info("camera disconnected", "camera_id", client.CameraID)
			}
		}
	}
}

// handleRegister adds client to the hub, replacing and closing any stale
// connection already registered under the same camera ID.
func (h *Hub) handleRegister(client *Client) {
	if client == nil {
		return
	}

	h.mu.Lock()
	old := h.clients[client.CameraID]
	h.clients[client.CameraID] = client
	h.mu.Unlock()

	if old != nil && old != client {
		slog.Warn("replacing existing camera connection", "camera_id", client.CameraID)
		old.close()
		_ = old.Conn.Close()
	}

	slog.Info("camera connected", "camera_id", client.CameraID)
}

// Register enqueues client for registration. It never blocks: when the hub is
// not draining registrations the client is closed and dropped.
func (h *Hub) Register(client *Client) {
	if client == nil {
		return
	}

	select {
	case h.register <- client:
	default:
		slog.Error("hub register queue full, dropping camera connection", "camera_id", client.CameraID)
		client.close()
		_ = client.Conn.Close()
	}
}

// Unregister enqueues client for removal. If the hub loop is not draining the
// request, the client is detached directly so its resources are released.
func (h *Hub) Unregister(client *Client) {
	if client == nil {
		return
	}

	select {
	case h.unregister <- client:
	default:
		h.removeClient(client)
	}
}

// removeClient deletes client from the hub and closes its send channel unless
// it has already been replaced by a newer connection for the same camera. It
// reports whether the client was still registered.
func (h *Hub) removeClient(client *Client) bool {
	h.mu.Lock()
	current, ok := h.clients[client.CameraID]
	if !ok || current != client {
		h.mu.Unlock()
		return false
	}
	delete(h.clients, client.CameraID)
	h.mu.Unlock()

	client.close()
	return true
}

// SendCommand queues msg for the camera identified by cameraID. It returns
// ErrCameraNotConnected when the camera has no live connection, wrapping
// ErrClientClosed or ErrSendBufferFull when the connection cannot accept it.
func (h *Hub) SendCommand(cameraID string, msg Message) error {
	client := h.GetClient(cameraID)
	if client == nil {
		return fmt.Errorf("camera %s: %w", cameraID, ErrCameraNotConnected)
	}
	if err := client.send(msg); err != nil {
		return fmt.Errorf("camera %s: %w", cameraID, err)
	}
	return nil
}

// GetClient returns the live connection for cameraID, or nil when the camera is
// not connected.
func (h *Hub) GetClient(cameraID string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[cameraID]
}

// ConnectedCameras returns the sorted IDs of all connected cameras.
func (h *Hub) ConnectedCameras() []string {
	h.mu.RLock()
	ids := make([]string, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	h.mu.RUnlock()

	sort.Strings(ids)
	return ids
}

// OnFrame registers a handler invoked for every frame received from any camera.
// The returned cancel function unregisters it; call it when the consumer shuts
// down so long-running servers do not accumulate dead handlers.
func (h *Hub) OnFrame(fn FrameHandler) (cancel func()) {
	if fn == nil {
		return func() {}
	}

	h.mu.Lock()
	h.frameHandlers = append(h.frameHandlers, fn)
	idx := len(h.frameHandlers) - 1
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if idx < len(h.frameHandlers) {
				h.frameHandlers[idx] = nil
			}
		})
	}
}

// dispatchFrame delivers a decoded frame to every registered handler. Handlers
// cancelled through the OnFrame cancel function are skipped.
func (h *Hub) dispatchFrame(cameraID, streamID string, seq int, data []byte, ts time.Time) {
	h.mu.RLock()
	handlers := make([]FrameHandler, len(h.frameHandlers))
	copy(handlers, h.frameHandlers)
	h.mu.RUnlock()

	for _, fn := range handlers {
		if fn == nil {
			continue
		}
		fn(cameraID, streamID, seq, data, ts)
	}
}
