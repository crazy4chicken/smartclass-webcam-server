package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

const (
	// wsHandshakeTimeout bounds the device WebSocket upgrade handshake.
	wsHandshakeTimeout = 10 * time.Second
	// deviceDrainTimeout bounds how long a disconnected device's recordings
	// may take to drain and finalize.
	deviceDrainTimeout = 30 * time.Second
)

// deviceUpgrader upgrades device HTTP requests to WebSocket connections.
// Devices are not browsers, so every origin is accepted; the connection is
// authenticated by its registration ticket before the upgrade.
var deviceUpgrader = websocket.Upgrader{
	HandshakeTimeout: wsHandshakeTimeout,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	CheckOrigin:      func(*http.Request) bool { return true },
}

// deviceRegisterRequest is the JSON body of GET /ws/register.
type deviceRegisterRequest struct {
	DeviceID string                `json:"device_id"`
	Cameras  []ws.CameraCapability `json:"cameras"`
}

// deviceRegisterResponse is the ticket returned by GET /ws/register.
type deviceRegisterResponse struct {
	DeviceWebsocketID string    `json:"device_websocket_id"`
	ExpiresAt         time.Time `json:"expires_at"`
	WebsocketPath     string    `json:"websocket_path"`
}

// handleDeviceRegister handles GET /ws/register. The device presents its
// long-lived token and its camera parameters and receives a single-use
// WebSocket ticket.
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	var req deviceRegisterRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	token, ok := auth.DeviceTokenFromRequest(r)
	if !ok {
		writeDeviceAuthFailure(w, r, deviceHeaderReason(r))
		return
	}

	device, err := s.store.Devices.Get(r.Context(), req.DeviceID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		writeDeviceAuthFailure(w, r, deviceTokenRejected)
		return
	default:
		s.fail(w, r, "load device for registration", err)
		return
	}

	// Compare hashes so neither an unknown device nor a wrong token leaks
	// whether the device exists.
	if subtle.ConstantTimeCompare(device.TokenHash, auth.HashDeviceToken(token)) != 1 {
		writeDeviceAuthFailure(w, r, deviceTokenRejected)
		return
	}

	if err := validateCameraCapabilities(req.Cameras); err != nil {
		writeProblem(w, r, http.StatusBadRequest, err.Error())
		return
	}

	reg, err := s.registry.Create(device.ID, normalizedCameras(req.Cameras))
	if err != nil {
		s.fail(w, r, "issue device websocket ticket", err)
		return
	}

	if err := s.store.Devices.Touch(r.Context(), device.ID, time.Now().UTC()); err != nil {
		slog.Error("touch device", "device_id", device.ID, "error", err)
	}

	writeJSON(w, http.StatusOK, deviceRegisterResponse{
		DeviceWebsocketID: reg.WebsocketID,
		ExpiresAt:         reg.ExpiresAt,
		WebsocketPath:     "/ws/device/" + reg.WebsocketID,
	})
}

// handleDeviceWS handles GET /ws/device/{device_websocket_id}. The ticket is
// consumed by the upgrade and bound to exactly one connection.
func (s *Server) handleDeviceWS(w http.ResponseWriter, r *http.Request) {
	websocketID := chi.URLParam(r, "device_websocket_id")

	reg, ok := s.registry.Get(websocketID)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "device websocket not found")
		return
	}
	// A ticket already bound to the device's live session cannot be replayed.
	// Attach stays the single point of truth for the race-free case below.
	if current, ok := s.registry.Current(reg.DeviceID); ok && current.WebsocketID == websocketID {
		writeProblem(w, r, http.StatusConflict, "device websocket ticket already attached")
		return
	}

	conn, err := deviceUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		slog.Warn("device websocket upgrade failed", "device_id", reg.DeviceID, "error", err)
		return
	}

	client := ws.NewClient(reg.DeviceID, conn, s.hub)
	if _, ok := s.registry.Attach(websocketID, client); !ok {
		// The ticket was already consumed by another connection.
		slog.Warn("device websocket ticket already attached", "device_id", reg.DeviceID)
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "ticket already attached"),
			time.Now().Add(wsHandshakeTimeout))
		_ = conn.Close()
		return
	}

	s.hub.Register(client)
	if err := s.store.Devices.Touch(r.Context(), reg.DeviceID, time.Now().UTC()); err != nil {
		slog.Error("touch device", "device_id", reg.DeviceID, "error", err)
	}

	go client.WritePump()
	// ReadPump blocks until the connection closes.
	client.ReadPump()

	// The ticket is single-use: it dies with the connection. Releasing it never
	// clears a newer ticket or session of the same device.
	s.registry.Release(websocketID)

	// Only the newest connection of a device finalizes its recordings; a
	// replaced connection must leave the live session's media alone.
	if current, ok := s.hub.Client(reg.DeviceID); ok && current == client {
		ctx, cancel := context.WithTimeout(context.Background(), deviceDrainTimeout)
		s.media.StopDevice(ctx, reg.DeviceID)
		cancel()
	}
	s.hub.Remove(client)
}

// validateCameraCapabilities enforces the registration rules: cameras are
// numbered 0..n-1 in order, each declaring the parameters it is currently at
// together with the resolutions and frame rates it supports - the current pair
// among them, since a device captures from one camera at one resolution and
// one frame rate at a time - and at least one codec from the closed vocabulary
// (ws.SupportedCodecNames) without duplicates. Registration is the only gate,
// so a live registration never carries a parameter or codec the camera did not
// declare.
func validateCameraCapabilities(cameras []ws.CameraCapability) error {
	if len(cameras) == 0 {
		return errors.New("cameras must not be empty")
	}
	for i, cam := range cameras {
		switch {
		case cam.CameraEnum != i:
			return fmt.Errorf("cameras[%d].camera_enum must be %d", i, i)
		case strings.TrimSpace(cam.Resolution) == "":
			return fmt.Errorf("cameras[%d].resolution must not be empty", i)
		case len(cam.SupportedResolutions) == 0:
			return fmt.Errorf("cameras[%d].supported_resolutions must not be empty", i)
		case cam.FPS <= 0:
			return fmt.Errorf("cameras[%d].fps must be positive", i)
		case len(cam.SupportedFramerates) == 0:
			return fmt.Errorf("cameras[%d].supported_framerates must not be empty", i)
		case len(cam.SupportedCodec) == 0:
			return fmt.Errorf("cameras[%d].supported_codec must not be empty", i)
		}

		// The supported resolutions list every resolution the camera accepts,
		// the one it is at included, without repetitions.
		resolutions := make(map[string]struct{}, len(cam.SupportedResolutions))
		for j, resolution := range cam.SupportedResolutions {
			resolution = strings.TrimSpace(resolution)
			if resolution == "" {
				return fmt.Errorf("cameras[%d].supported_resolutions[%d] must not be empty", i, j)
			}
			resolutions[resolution] = struct{}{}
		}
		if len(resolutions) != len(cam.SupportedResolutions) {
			return fmt.Errorf("cameras[%d].supported_resolutions must not contain duplicates", i)
		}
		if _, ok := resolutions[strings.TrimSpace(cam.Resolution)]; !ok {
			return fmt.Errorf("cameras[%d].resolution must be one of the supported_resolutions", i)
		}

		// The supported frame rates follow the same rules.
		framerates := make(map[int]struct{}, len(cam.SupportedFramerates))
		for j, fps := range cam.SupportedFramerates {
			if fps <= 0 {
				return fmt.Errorf("cameras[%d].supported_framerates[%d] must be positive", i, j)
			}
			framerates[fps] = struct{}{}
		}
		if len(framerates) != len(cam.SupportedFramerates) {
			return fmt.Errorf("cameras[%d].supported_framerates must not contain duplicates", i)
		}
		if _, ok := framerates[cam.FPS]; !ok {
			return fmt.Errorf("cameras[%d].fps must be one of the supported_framerates", i)
		}

		// Every codec must come from the closed vocabulary, and none may be
		// listed twice.
		seen := make(map[string]struct{}, len(cam.SupportedCodec))
		for j, codec := range cam.SupportedCodec {
			if !ws.IsSupportedCodec(codec) {
				return fmt.Errorf("cameras[%d].supported_codec[%d] must be one of %s", i, j, strings.Join(ws.SupportedCodecNames(), ", "))
			}
			seen[codec] = struct{}{}
		}
		if len(seen) != len(cam.SupportedCodec) {
			return fmt.Errorf("cameras[%d].supported_codec must not contain duplicates", i)
		}
	}
	return nil
}

// normalizedCameras trims the resolution strings of an accepted registration,
// so the live session holds the values the validation compared: a switch may
// then select any entry the device listed, whatever padding it sent.
func normalizedCameras(cameras []ws.CameraCapability) []ws.CameraCapability {
	normalized := make([]ws.CameraCapability, 0, len(cameras))
	for _, camera := range cameras {
		camera.Resolution = strings.TrimSpace(camera.Resolution)
		if camera.SupportedResolutions != nil {
			resolutions := make([]string, 0, len(camera.SupportedResolutions))
			for _, resolution := range camera.SupportedResolutions {
				resolutions = append(resolutions, strings.TrimSpace(resolution))
			}
			camera.SupportedResolutions = resolutions
		}
		normalized = append(normalized, camera)
	}
	return normalized
}

// deviceTokenRejected is the one reason a well-formed device credential is
// rejected, shared by an unknown device id and a wrong or rotated token so the
// endpoint never leaks whether a device exists.
const deviceTokenRejected = "the device token is unknown or has been rotated"

// deviceHeaderReason names why the Authorization header carried no device
// token, mirroring the parse auth.DeviceTokenFromRequest applies; the wdt_
// prefix is the auth package's deviceTokenPrefix. The header value itself is
// never repeated.
func deviceHeaderReason(r *http.Request) string {
	fields := strings.Fields(r.Header.Get("Authorization"))
	switch {
	case len(fields) == 0:
		return "the Authorization header is missing"
	case len(fields) != 2 || !strings.EqualFold(fields[0], "bearer"):
		return "the Authorization header does not carry a Bearer token"
	case !strings.HasPrefix(fields[1], "wdt_"):
		return "the Authorization header does not carry a device token"
	default:
		return deviceTokenRejected
	}
}

// writeDeviceAuthFailure writes the 401 response for a device credential the
// request did not present in a usable shape.
func writeDeviceAuthFailure(w http.ResponseWriter, r *http.Request, reason string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="device"`)
	writeProblem(w, r, http.StatusUnauthorized, reason)
}
