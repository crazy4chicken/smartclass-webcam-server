package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// cameraCommandRequest is the body of the endpoints that act on the camera the
// device is already using: stop-recording and take-photo.
type cameraCommandRequest struct {
	CameraEnum *int `json:"camera_enum"`
}

// switchRequest is the body of the switch-camera endpoint: the camera to
// select and, optionally, the parameters to select it at. Both parameters must
// be ones the camera declared during registration; leaving one out switches
// without changing it, so a switch to another camera leaves that camera at the
// parameters it reported.
type switchRequest struct {
	CameraEnum *int   `json:"camera_enum"`
	Resolution string `json:"resolution"`
	FPS        *int   `json:"fps"`
}

// recordingStartRequest is the body of the recording-start endpoint: the
// camera to record and, optionally, the codec to record it with. An omitted
// codec leaves the choice to the device, which uses its preferred one - the
// first entry of the camera's supported_codec.
type recordingStartRequest struct {
	CameraEnum *int   `json:"camera_enum"`
	Codec      string `json:"codec"`
}

// commandResponse is the body of the switch-camera command response.
type commandResponse struct {
	CommandID  string `json:"command_id"`
	CameraEnum int    `json:"camera_enum"`
}

// photoCommandResponse is the body of the take-photo command response.
type photoCommandResponse struct {
	CommandID  string `json:"command_id"`
	RequestID  string `json:"request_id"`
	CameraEnum int    `json:"camera_enum"`
}

// decodeCameraEnum decodes the command body and returns the requested camera.
func (s *Server) decodeCameraEnum(w http.ResponseWriter, r *http.Request) (int, bool) {
	var req cameraCommandRequest
	if !s.decodeJSON(w, r, &req) {
		return 0, false
	}
	if req.CameraEnum == nil {
		writeProblem(w, r, http.StatusBadRequest, "camera_enum is required")
		return 0, false
	}
	return *req.CameraEnum, true
}

// requireLiveDevice returns the device's live registration, writing a 409
// problem response naming which check failed when the device has no live
// session.
func (s *Server) requireLiveDevice(w http.ResponseWriter, r *http.Request, deviceID string) (*ws.Registration, bool) {
	registration, ok := s.registry.Current(deviceID)
	if !ok {
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("device %q is offline: no live registration", deviceID))
		return nil, false
	}
	if _, ok := s.hub.Client(deviceID); !ok {
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("device %q is offline: no live websocket", deviceID))
		return nil, false
	}
	return registration, true
}

// requireRegisteredCamera validates cameraEnum against the device's current
// registration, writing a 400 problem response for unknown cameras.
func requireRegisteredCamera(w http.ResponseWriter, r *http.Request, deviceID string, registration *ws.Registration, cameraEnum int) (ws.CameraCapability, bool) {
	camera, ok := registration.Camera(cameraEnum)
	if !ok {
		writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("camera_enum %d is not registered for device %q", cameraEnum, deviceID))
		return ws.CameraCapability{}, false
	}
	return camera, true
}

// sendCommand queues msg for the device, writing a 409 problem response when
// the device has no live connection and a 502 when it cannot accept the
// command.
func (s *Server) sendCommand(w http.ResponseWriter, r *http.Request, deviceID string, msg ws.Message) bool {
	if err := s.hub.Send(deviceID, msg); err != nil {
		if errors.Is(err, ws.ErrDeviceNotConnected) {
			writeProblem(w, r, http.StatusConflict, fmt.Sprintf("device %q is offline: no live websocket", deviceID))
			return false
		}
		slog.Error("send device command", "device_id", deviceID, "type", msg.Type, "error", err)
		// The hub error wraps the device id and the client's state, so the
		// response carries the classified cause instead of the raw text.
		cause := redact.Text(s.sanitize, err.Error())
		switch {
		case errors.Is(err, ws.ErrClientClosed):
			cause = "the device websocket is closed"
		case errors.Is(err, ws.ErrSendBufferFull):
			cause = "the device websocket send buffer is full"
		}
		writeProblem(w, r, http.StatusBadGateway,
			redact.Trim(fmt.Sprintf("sending %s failed: %s", msg.Type, cause), 300))
		return false
	}
	return true
}

// handleCameraSwitch handles POST /api/devices/{device_id}/camera/switch. The
// device switches to the requested camera and, when the request names them, to
// the requested resolution and frame rate; both must be ones the camera
// declared during registration.
func (s *Server) handleCameraSwitch(w http.ResponseWriter, r *http.Request) {
	var req switchRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.CameraEnum == nil {
		writeProblem(w, r, http.StatusBadRequest, "camera_enum is required")
		return
	}
	deviceID := chi.URLParam(r, "device_id")
	registration, ok := s.requireLiveDevice(w, r, deviceID)
	if !ok {
		return
	}
	camera, ok := requireRegisteredCamera(w, r, deviceID, registration, *req.CameraEnum)
	if !ok {
		return
	}

	payload := map[string]any{ws.PKCameraEnum: camera.CameraEnum}
	if resolution := strings.TrimSpace(req.Resolution); resolution != "" {
		if !slices.Contains(camera.SupportedResolutions, resolution) {
			writeProblem(w, r, http.StatusBadRequest,
				fmt.Sprintf("resolution %q is not supported by camera_enum %d on device %q", resolution, camera.CameraEnum, deviceID))
			return
		}
		payload[ws.PKResolution] = resolution
	}
	if req.FPS != nil {
		if !slices.Contains(camera.SupportedFramerates, *req.FPS) {
			writeProblem(w, r, http.StatusBadRequest,
				fmt.Sprintf("fps %d is not supported by camera_enum %d on device %q", *req.FPS, camera.CameraEnum, deviceID))
			return
		}
		payload[ws.PKFPS] = *req.FPS
	}

	msg := ws.Message{
		Channel: ws.ChannelControl,
		Type:    ws.CommandSwitchCamera,
		ID:      ulid.Make().String(),
		Payload: payload,
	}
	if !s.sendCommand(w, r, deviceID, msg) {
		return
	}
	writeJSON(w, http.StatusAccepted, commandResponse{CommandID: msg.ID, CameraEnum: camera.CameraEnum})
}

// handleRecordingStart handles POST /api/devices/{device_id}/recording/start.
// It creates the active stream row first and marks it failed when the command
// cannot be delivered. The recording runs on the camera's current parameters;
// only the codec can be chosen, and only here.
func (s *Server) handleRecordingStart(w http.ResponseWriter, r *http.Request) {
	var req recordingStartRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.CameraEnum == nil {
		writeProblem(w, r, http.StatusBadRequest, "camera_enum is required")
		return
	}
	cameraEnum := *req.CameraEnum
	deviceID := chi.URLParam(r, "device_id")
	registration, ok := s.requireLiveDevice(w, r, deviceID)
	if !ok {
		return
	}
	camera, ok := requireRegisteredCamera(w, r, deviceID, registration, cameraEnum)
	if !ok {
		return
	}

	codec := strings.TrimSpace(req.Codec)
	if codec != "" && !slices.Contains(camera.SupportedCodec, codec) {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("codec %q is not supported by camera_enum %d on device %q", codec, camera.CameraEnum, deviceID))
		return
	}

	stream, err := s.store.Streams.Create(r.Context(), deviceID, cameraEnum, domain.StreamMetadata{
		Resolution: camera.Resolution,
		FPS:        camera.FPS,
		Codec:      codec,
		Codecs:     append([]string(nil), camera.SupportedCodec...),
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeProblem(w, r, http.StatusConflict, fmt.Sprintf("camera_enum %d is already streaming on device %q", cameraEnum, deviceID))
			return
		}
		s.writeStoreError(w, r, "create stream", err, "device not found", "")
		return
	}

	// Register the accumulator before sending the command so frames arriving
	// immediately after the device acknowledges are captured.
	s.media.StartStream(*stream)

	payload := map[string]any{
		ws.PKCameraEnum: cameraEnum,
		ws.PKStreamID:   stream.ID,
	}
	if codec != "" {
		payload[ws.PKCodec] = codec
	}
	msg := ws.Message{
		Channel: ws.ChannelControl,
		Type:    ws.CommandStartRecording,
		ID:      ulid.Make().String(),
		Payload: payload,
	}
	if !s.sendCommand(w, r, deviceID, msg) {
		if err := s.media.StopStream(r.Context(), *stream); err != nil {
			slog.Error("discard media of failed stream", "stream_id", stream.ID, "error", err)
		}
		if _, err := s.store.Streams.Finish(r.Context(), stream.ID, domain.StreamStatusFailed); err != nil {
			slog.Error("mark stream failed", "stream_id", stream.ID, "error", err)
		}
		return
	}

	w.Header().Set("Location", "/api/streams/"+stream.ID)
	writeJSON(w, http.StatusCreated, stream)
}

// handleRecordingStop handles POST /api/devices/{device_id}/recording/stop. It
// stops the camera's active stream and marks it completed.
func (s *Server) handleRecordingStop(w http.ResponseWriter, r *http.Request) {
	cameraEnum, ok := s.decodeCameraEnum(w, r)
	if !ok {
		return
	}
	deviceID := chi.URLParam(r, "device_id")

	stream, err := s.store.Streams.Active(r.Context(), deviceID, cameraEnum)
	if err != nil {
		s.writeStoreError(w, r, "find active stream", err,
			fmt.Sprintf("no active stream for camera_enum %d on device %q", cameraEnum, deviceID), "")
		return
	}

	msg := ws.Message{
		Channel: ws.ChannelControl,
		Type:    ws.CommandStopRecording,
		ID:      ulid.Make().String(),
		Payload: map[string]any{
			ws.PKCameraEnum: cameraEnum,
			ws.PKStreamID:   stream.ID,
		},
	}
	if !s.sendCommand(w, r, deviceID, msg) {
		return
	}

	if err := s.media.StopStream(r.Context(), *stream); err != nil {
		slog.Error("drain stream media", "stream_id", stream.ID, "error", err)
	}
	stopped, err := s.store.Streams.Finish(r.Context(), stream.ID, domain.StreamStatusCompleted)
	if err != nil {
		s.writeStoreError(w, r, "finish stream", err, fmt.Sprintf("stream %q not found", stream.ID), "")
		return
	}
	writeJSON(w, http.StatusOK, stopped)
}

// handlePhotoCapture handles POST /api/devices/{device_id}/photo.
func (s *Server) handlePhotoCapture(w http.ResponseWriter, r *http.Request) {
	cameraEnum, ok := s.decodeCameraEnum(w, r)
	if !ok {
		return
	}
	deviceID := chi.URLParam(r, "device_id")
	registration, ok := s.requireLiveDevice(w, r, deviceID)
	if !ok {
		return
	}
	if _, ok := requireRegisteredCamera(w, r, deviceID, registration, cameraEnum); !ok {
		return
	}

	requestID := ulid.Make().String()
	msg := ws.Message{
		Channel: ws.ChannelControl,
		Type:    ws.CommandTakePhoto,
		ID:      ulid.Make().String(),
		Payload: map[string]any{
			ws.PKCameraEnum: cameraEnum,
			ws.PKRequestID:  requestID,
		},
	}
	if !s.sendCommand(w, r, deviceID, msg) {
		return
	}
	writeJSON(w, http.StatusAccepted, photoCommandResponse{CommandID: msg.ID, RequestID: requestID, CameraEnum: cameraEnum})
}
