package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// deviceCreateRequest is the body of POST /api/devices. team_id and owner_id
// default to the caller's team and subject.
type deviceCreateRequest struct {
	Name     string  `json:"name"`
	Location string  `json:"location,omitempty"`
	TeamID   *string `json:"team_id,omitempty"`
	OwnerID  *string `json:"owner_id,omitempty"`
}

// deviceUpdateRequest is the body of PUT /api/devices/{device_id}. All fields
// are optional; nil fields keep their stored value.
type deviceUpdateRequest struct {
	Name     *string `json:"name,omitempty"`
	Location *string `json:"location,omitempty"`
	TeamID   *string `json:"team_id,omitempty"`
	OwnerID  *string `json:"owner_id,omitempty"`
}

// deviceTokenResponse is the body of device registration and token rotation.
// The token is returned exactly once.
type deviceTokenResponse struct {
	Device *domain.Device `json:"device"`
	Token  string         `json:"token"`
}

// deviceDetail is the GET /api/devices/{device_id}/ response: the device with
// the state of its live session.
type deviceDetail struct {
	domain.Device
	Online  bool                  `json:"online"`
	Cameras []ws.CameraCapability `json:"cameras"`
}

// requireGrant returns the access grant attached by the authorization
// middleware. Its absence means the request reached the handler without an
// access grant, which only a route mounted without RequireCollection or
// RequireDevice can produce.
func (s *Server) requireGrant(w http.ResponseWriter, r *http.Request) (auth.AccessGrant, bool) {
	grant, ok := auth.GrantFromContext(r.Context())
	if !ok {
		s.fail(w, r, "authorize request", errors.New(
			"the request reached the handler without an access grant: the route is mounted without its authorization middleware"))
		return auth.AccessGrant{}, false
	}
	return grant, true
}

// loadDeviceFromPath loads the device identified by the {device_id} path
// parameter for RequireDevice.
func (s *Server) loadDeviceFromPath(r *http.Request) (*domain.Device, error) {
	return s.store.Devices.Get(r.Context(), chi.URLParam(r, "device_id"))
}

// handleDeviceCreate handles POST /api/devices.
func (s *Server) handleDeviceCreate(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.requireGrant(w, r)
	if !ok {
		return
	}
	var req deviceCreateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}

	token, hash := auth.GenerateDeviceToken()
	device := &domain.Device{
		Name:      name,
		Location:  strings.TrimSpace(req.Location),
		TeamID:    grant.Claims.Team,
		OwnerID:   grant.Claims.Subject,
		TokenHash: hash,
	}
	// Ownership defaults to the caller; only any-scope callers may override it.
	if grant.Scope == auth.ScopeAny {
		if req.TeamID != nil {
			device.TeamID = strings.TrimSpace(*req.TeamID)
		}
		if req.OwnerID != nil {
			device.OwnerID = strings.TrimSpace(*req.OwnerID)
		}
	}

	created, err := s.store.Devices.Create(r.Context(), device)
	if err != nil {
		s.writeStoreError(w, r, "create device", err, "device not found", "a device with this id already exists")
		return
	}
	w.Header().Set("Location", "/api/devices/"+created.ID)
	writeJSON(w, http.StatusCreated, deviceTokenResponse{Device: created, Token: token})
}

// handleDeviceList handles GET /api/devices.
func (s *Server) handleDeviceList(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.requireGrant(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}

	filter := store.DeviceFilter{Scope: grant.Scope, Limit: limit}
	switch grant.Scope {
	case auth.ScopeTeam:
		filter.TeamID = grant.Claims.Team
	case auth.ScopeOwn:
		filter.Subject = grant.Claims.Subject
	}

	devices, err := s.store.Devices.List(r.Context(), filter)
	if err != nil {
		s.writeStoreError(w, r, "list devices", err, "devices not found", "")
		return
	}
	writeJSON(w, http.StatusOK, newListResponse(devices))
}

// handleDeviceGet handles GET /api/devices/{device_id}/.
func (s *Server) handleDeviceGet(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.requireGrant(w, r)
	if !ok {
		return
	}
	if grant.Device == nil {
		writeProblem(w, r, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, s.deviceDetail(grant.Device))
}

// deviceDetail converts a device into its API representation, adding the live
// session state from the registration registry.
func (s *Server) deviceDetail(device *domain.Device) deviceDetail {
	detail := deviceDetail{Device: *device, Cameras: []ws.CameraCapability{}}
	if registration, ok := s.registry.Current(device.ID); ok {
		detail.Online = true
		detail.Cameras = append(detail.Cameras, registration.Cameras...)
	}
	return detail
}

// handleDeviceUpdate handles PUT /api/devices/{device_id}/. The request body
// is partial: present fields overwrite the stored values, absent fields are
// kept.
func (s *Server) handleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.requireGrant(w, r)
	if !ok {
		return
	}
	var req deviceUpdateRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	var upd store.DeviceUpdate
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeProblem(w, r, http.StatusBadRequest, "name must not be empty")
			return
		}
		upd.Name = &name
	}
	if req.Location != nil {
		location := strings.TrimSpace(*req.Location)
		upd.Location = &location
	}
	// Ownership may only be changed by any-scope callers.
	if grant.Scope == auth.ScopeAny {
		if req.TeamID != nil {
			teamID := strings.TrimSpace(*req.TeamID)
			upd.TeamID = &teamID
		}
		if req.OwnerID != nil {
			ownerID := strings.TrimSpace(*req.OwnerID)
			upd.OwnerID = &ownerID
		}
	}
	if upd.Name == nil && upd.Location == nil && upd.TeamID == nil && upd.OwnerID == nil {
		writeProblem(w, r, http.StatusBadRequest, "request body must contain at least one of name, location, team_id or owner_id")
		return
	}

	device, err := s.store.Devices.Update(r.Context(), chi.URLParam(r, "device_id"), upd)
	if err != nil {
		s.writeStoreError(w, r, "update device", err, "device not found", "")
		return
	}
	writeJSON(w, http.StatusOK, device)
}

// handleDeviceDelete handles DELETE /api/devices/{device_id}/. Deleting a
// device also drops its streams, segments and photos, and closes its live
// connection.
func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "device_id")
	if err := s.store.Devices.Delete(r.Context(), id); err != nil {
		s.writeStoreError(w, r, "delete device", err, "device not found", "")
		return
	}
	if client := s.registry.Invalidate(id); client != nil {
		client.Close()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceTokenRotate handles POST /api/devices/{device_id}/token. The old
// token stops working immediately and the new token is returned once.
func (s *Server) handleDeviceTokenRotate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "device_id")
	token, hash := auth.GenerateDeviceToken()
	device, err := s.store.Devices.SetTokenHash(r.Context(), id, hash)
	if err != nil {
		s.writeStoreError(w, r, "rotate device token", err, "device not found", "")
		return
	}
	if client := s.registry.Invalidate(id); client != nil {
		client.Close()
	}
	writeJSON(w, http.StatusOK, deviceTokenResponse{Device: device, Token: token})
}
