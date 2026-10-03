package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

const (
	// wsHandshakeTimeout bounds the WebSocket upgrade handshake.
	wsHandshakeTimeout = 10 * time.Second
	// statusWriteTimeout bounds camera status writes made after a connection
	// ends, which must not use the cancelled request context.
	statusWriteTimeout = 5 * time.Second
	// drainTimeout bounds how long a stopping stream waits for its buffered
	// frames to reach object storage before the response is returned.
	drainTimeout = 10 * time.Second
)

// cameraUpgrader upgrades camera HTTP requests to WebSocket connections. Camera
// clients are not browsers, so every origin is accepted; requests are
// authenticated by the handler before the upgrade.
var cameraUpgrader = websocket.Upgrader{
	HandshakeTimeout: wsHandshakeTimeout,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	CheckOrigin:      func(*http.Request) bool { return true },
}

// handleCameraWS handles GET /ws/camera/{id}. The access token travels in the
// token query parameter because WebSocket clients cannot set headers.
func (s *server) handleCameraWS(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	if _, err := s.auth.WSUpgradeAuth(r); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="teamusers"`)
		writeProblem(w, r, http.StatusUnauthorized, "websocket authentication failed")
		slog.Warn("websocket authentication failed", "camera_id", cameraID, "error", err)
		return
	}
	if _, err := s.store.Camera.Get(r.Context(), cameraID); err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("camera %q not found", cameraID))
		return
	}

	conn, err := cameraUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already sent an HTTP error response.
		slog.Warn("websocket upgrade failed", "camera_id", cameraID, "error", err)
		return
	}

	client := ws.NewClient(cameraID, conn, s.hub)
	s.hub.Register(client)
	s.setCameraStatus(cameraID, domain.CameraStatusOnline)

	go client.WritePump()
	// ReadPump blocks until the connection closes and unregisters the client.
	client.ReadPump()

	// Only the newest connection owns a camera's status: a reconnect may have
	// replaced this one while it was shutting down.
	if s.hub.GetClient(cameraID) == nil {
		s.setCameraStatus(cameraID, domain.CameraStatusOffline)
		// Close any in-flight frame accumulators so they flush and exit.
		s.stopCameraAccumulators(cameraID)
	}
}

// stopCameraAccumulators closes the done channel of every accumulator tied to
// an active stream on cameraID. Called on camera disconnect to drain buffered
// frames.
func (s *server) stopCameraAccumulators(cameraID string) {
	ctx, cancel := context.WithTimeout(context.Background(), statusWriteTimeout)
	defer cancel()

	streams, err := s.store.Stream.ListByCamera(ctx, cameraID, 100)
	if err != nil {
		slog.Error("list streams for accumulator cleanup", "camera_id", cameraID, "error", err)
		return
	}

	s.accMu.Lock()
	defer s.accMu.Unlock()
	for _, st := range streams {
		if st.Status != domain.StreamStatusActive {
			continue
		}
		if acc, ok := s.streamAccumulators[st.ID]; ok {
			acc.stopAndDrain()
			delete(s.streamAccumulators, st.ID)
		}
	}
}

// setCameraStatus stores the connection status of a camera. Errors are logged:
// the status is informative and never fails the connection itself.
func (s *server) setCameraStatus(cameraID string, status domain.CameraStatus) {
	ctx, cancel := context.WithTimeout(context.Background(), statusWriteTimeout)
	defer cancel()

	if err := s.store.Camera.UpdateStatus(ctx, cameraID, status); err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("update camera status", "camera_id", cameraID, "status", status, "error", err)
	}
}
