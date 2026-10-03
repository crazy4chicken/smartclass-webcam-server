package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// cameraCreateRequest is the body of POST /api/cameras.
type cameraCreateRequest struct {
	Name     string               `json:"name"`
	Location string               `json:"location,omitempty"`
	Config   *domain.CameraConfig `json:"config,omitempty"`
}

// cameraUpdateRequest is the body of PUT /api/cameras/{id}. All fields are
// optional; absent fields keep their stored value.
type cameraUpdateRequest struct {
	Name     *string              `json:"name,omitempty"`
	Location *string              `json:"location,omitempty"`
	Config   *domain.CameraConfig `json:"config,omitempty"`
}

// configureResponse is the body of POST /api/cameras/{id}/configure.
type configureResponse struct {
	CameraID string              `json:"camera_id"`
	Config   domain.CameraConfig `json:"config"`
}

// listCameras handles GET /api/cameras.
func (s *server) listCameras(w http.ResponseWriter, r *http.Request) {
	cameras, err := s.store.Camera.List(r.Context())
	if err != nil {
		writeStoreError(w, r, err, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(cameras))
}

// requireCamera loads the camera identified by the {id} path parameter. It
// writes a problem response and reports false when the camera is unavailable.
func (s *server) requireCamera(w http.ResponseWriter, r *http.Request) (*domain.Camera, bool) {
	id := chi.URLParam(r, "id")
	cam, err := s.store.Camera.Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("camera %q not found", id))
		return nil, false
	}
	return cam, true
}

// createCamera handles POST /api/cameras.
func (s *server) createCamera(w http.ResponseWriter, r *http.Request) {
	var req cameraCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}

	var cfg domain.CameraConfig
	if req.Config != nil {
		cfg = *req.Config
	}
	cam, err := s.store.Camera.Create(r.Context(), name, strings.TrimSpace(req.Location), cfg)
	if err != nil {
		writeStoreError(w, r, err, "camera not found")
		return
	}

	w.Header().Set("Location", "/api/cameras/"+cam.ID)
	writeJSON(w, http.StatusCreated, cam)
}

// getCamera handles GET /api/cameras/{id}.
func (s *server) getCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, cam)
}

// updateCamera handles PUT /api/cameras/{id}. The request body is a partial
// camera: present fields overwrite the stored values, absent fields are kept.
func (s *server) updateCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}

	var req cameraUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil && req.Location == nil && req.Config == nil {
		writeProblem(w, r, http.StatusBadRequest, "request body must contain at least one of name, location or config")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeProblem(w, r, http.StatusBadRequest, "name must not be empty")
			return
		}
		cam.Name = name
	}
	if req.Location != nil {
		cam.Location = strings.TrimSpace(*req.Location)
	}
	if req.Config != nil {
		cam.Config = *req.Config
	}

	if err := s.store.Camera.Update(r.Context(), cam); err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("camera %q not found", cam.ID))
		return
	}
	writeJSON(w, http.StatusOK, cam)
}

// deleteCamera handles DELETE /api/cameras/{id}. Deleting a camera also removes
// its streams and segments.
func (s *server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Camera.Delete(r.Context(), id); err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("camera %q not found", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireOnline reports whether the camera has a live WebSocket connection,
// writing a 409 problem response when it does not.
func (s *server) requireOnline(w http.ResponseWriter, r *http.Request, cameraID string) bool {
	if s.hub.GetClient(cameraID) != nil {
		return true
	}
	writeProblem(w, r, http.StatusConflict, fmt.Sprintf("camera %q is offline", cameraID))
	return false
}

// sendCommand queues msg for the camera, writing a 502 problem response when
// the command cannot be delivered.
func (s *server) sendCommand(w http.ResponseWriter, r *http.Request, cameraID string, msg ws.Message) bool {
	if err := s.hub.SendCommand(cameraID, msg); err != nil {
		slog.Error("send camera command", "camera_id", cameraID, "type", msg.Type, "error", err)
		writeProblem(w, r, http.StatusBadGateway, "camera connection is unavailable")
		return false
	}
	return true
}

// configureCamera handles POST /api/cameras/{id}/configure. The configuration
// is queued for the camera and stored once the command has been delivered.
func (s *server) configureCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}

	var cfg domain.CameraConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if !s.requireOnline(w, r, cam.ID) {
		return
	}
	if !s.sendCommand(w, r, cam.ID, ws.Message{Type: ws.MsgConfigure, Payload: configPayload(cfg)}) {
		return
	}

	if err := s.store.Camera.UpdateConfig(r.Context(), cam.ID, cfg); err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("camera %q not found", cam.ID))
		return
	}
	writeJSON(w, http.StatusOK, configureResponse{CameraID: cam.ID, Config: cfg})
}

// startStream handles POST /api/cameras/{id}/stream/start. It creates the
// stream record first and marks it failed when the command cannot be delivered.
func (s *server) startStream(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}
	if !s.requireOnline(w, r, cam.ID) {
		return
	}

	stream, err := s.store.Stream.Create(r.Context(), cam.ID, domain.StreamMetadata{
		Resolution: cam.Config.Resolution,
		FPS:        cam.Config.FPS,
		Codec:      cam.Config.Codec,
	})
	if err != nil {
		writeStoreError(w, r, err, "camera not found")
		return
	}

	// Start the frame accumulator before sending the stream command so
	// frames arriving immediately after the camera responds are captured.
	acc := s.startFramePipeline(stream)
	s.accMu.Lock()
	s.streamAccumulators[stream.ID] = acc
	s.accMu.Unlock()

	msg := ws.Message{Type: ws.MsgStartStream, Payload: map[string]any{
		"stream_id": stream.ID,
		"config":    configPayload(cam.Config),
	}}
	if !s.sendCommand(w, r, cam.ID, msg) {
		// Signal the accumulator to stop and remove it.
		s.accMu.Lock()
		delete(s.streamAccumulators, stream.ID)
		s.accMu.Unlock()
		acc.stopAndDrain()

		if err := s.store.Stream.UpdateStatus(r.Context(), stream.ID, domain.StreamStatusFailed); err != nil {
			slog.Error("mark stream failed", "stream_id", stream.ID, "error", err)
		}
		return
	}

	w.Header().Set("Location", "/api/streams/"+stream.ID)
	writeJSON(w, http.StatusCreated, stream)
}

// stopStream handles POST /api/cameras/{id}/stream/stop. It stops the camera's
// active stream and marks it completed.
func (s *server) stopStream(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.requireCamera(w, r)
	if !ok {
		return
	}
	if !s.requireOnline(w, r, cam.ID) {
		return
	}

	streams, err := s.store.Stream.ListByCamera(r.Context(), cam.ID, 1)
	if err != nil {
		writeStoreError(w, r, err, "camera not found")
		return
	}
	if len(streams) == 0 || streams[0].Status != domain.StreamStatusActive {
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("camera %q is not streaming", cam.ID))
		return
	}
	active := streams[0]

	// Stop the frame accumulator and drain any buffered frames.
	s.accMu.Lock()
	acc, hasAcc := s.streamAccumulators[active.ID]
	if hasAcc {
		delete(s.streamAccumulators, active.ID)
	}
	s.accMu.Unlock()
	if hasAcc {
		acc.stopAndDrain()
		// Wait for the final drain so the closing segments are already
		// stored when this response reaches the caller.
		select {
		case <-acc.finished:
		case <-time.After(drainTimeout):
			slog.Warn("frame accumulator drain timed out", "stream_id", active.ID)
		}
	}

	msg := ws.Message{Type: ws.MsgStopStream, Payload: map[string]any{"stream_id": active.ID}}
	if !s.sendCommand(w, r, cam.ID, msg) {
		return
	}
	if err := s.store.Stream.UpdateStatus(r.Context(), active.ID, domain.StreamStatusCompleted); err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("stream %q not found", active.ID))
		return
	}

	stopped, err := s.store.Stream.Get(r.Context(), active.ID)
	if err != nil {
		writeStoreError(w, r, err, fmt.Sprintf("stream %q not found", active.ID))
		return
	}
	writeJSON(w, http.StatusOK, stopped)
}

// configPayload converts cfg into the free-form payload sent to a camera. It
// goes through JSON so the wire field names and omitempty behavior stay in sync
// with the domain type.
func configPayload(cfg domain.CameraConfig) map[string]any {
	raw, err := json.Marshal(cfg)
	if err != nil {
		slog.Error("encode camera config", "error", err)
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Error("decode camera config payload", "error", err)
		return map[string]any{}
	}
	return payload
}
