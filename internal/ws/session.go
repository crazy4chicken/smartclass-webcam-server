package ws

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CameraCapability describes one camera a device announces during
// registration. Resolution and FPS are the parameters this camera is at for
// the current connection: a device captures from exactly one camera at exactly
// one resolution and frame rate at a time, and only a switch_camera command
// selects another camera or another pair. SupportedResolutions and
// SupportedFramerates bound what a switch may select, and SupportedCodec is
// ordered by preference, so its first entry is the codec a recording uses when
// the server names none. Camera parameters are ephemeral: they live in the
// registration and are never persisted. Resolution and FPS are rewritten by the
// server when the device acknowledges a switch_camera that names them.
type CameraCapability struct {
	CameraEnum           int            `json:"camera_enum"`
	Resolution           string         `json:"resolution"`
	FPS                  int            `json:"fps"`
	SupportedResolutions []string       `json:"supported_resolutions"`
	SupportedFramerates  []int          `json:"supported_framerates"`
	SupportedCodec       []string       `json:"supported_codec"`
	Attrs                map[string]any `json:"attrs,omitempty"`
}

// Registration is one device registration. It is a pending, single-use ticket
// until it is attached to a connection; afterwards it is the device's live
// session.
type Registration struct {
	WebsocketID string
	DeviceID    string
	CreatedAt   time.Time
	ExpiresAt   time.Time

	client *Client // set once by Attach, never cleared

	// cameras and pending are guarded by mu: the registry mutex protects the
	// maps, not the registration a caller already holds, and the ack handler
	// rewrites the current parameters while detail reads and media frames look
	// the cameras up.
	mu      sync.RWMutex
	cameras []CameraCapability
	// pending holds the switches queued to the device and not yet resolved by
	// an ack, keyed by command id. It dies with the registration, so a device
	// that never acks leaves no trace beyond a stale entry.
	pending map[string]pendingSwitch
}

// pendingSwitch is a switch_camera waiting for its ack. A parameter left out of
// the request is not stored, so resolving the ack keeps the value the camera is
// at when the ack arrives - the same rule the device applies on receipt.
type pendingSwitch struct {
	cameraEnum int
	resolution string // "" keeps the camera's current resolution
	fps        *int   // nil keeps the camera's current frame rate
}

// QueueSwitch records the switch identified by commandID as pending. It does
// not touch the camera: only an acknowledgement applies it. fps is copied, so
// the caller may reuse it.
func (r *Registration) QueueSwitch(commandID string, cameraEnum int, resolution string, fps *int) {
	var requested *int
	if fps != nil {
		value := *fps
		requested = &value
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = make(map[string]pendingSwitch)
	}
	r.pending[commandID] = pendingSwitch{cameraEnum: cameraEnum, resolution: resolution, fps: requested}
}

// ResolveSwitch resolves the pending switch identified by commandID. It reports
// false when the registration holds no such switch, which is the case for an
// unknown or already resolved command id and after the connection was replaced.
// An acknowledged switch (ok) rewrites the camera's current parameters; an
// acknowledgement reporting failure drops the pending switch and changes
// nothing.
func (r *Registration) ResolveSwitch(commandID string, ok bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	pending, found := r.pending[commandID]
	if !found {
		return false
	}
	delete(r.pending, commandID)
	if !ok {
		return false
	}

	for i, cam := range r.cameras {
		if cam.CameraEnum != pending.cameraEnum {
			continue
		}
		if pending.resolution != "" {
			r.cameras[i].Resolution = pending.resolution
		}
		if pending.fps != nil {
			r.cameras[i].FPS = *pending.fps
		}
		return true
	}
	return false
}

// DiscardSwitch drops the pending switch identified by commandID, for a command
// the server could not queue and that therefore no acknowledgement can name.
func (r *Registration) DiscardSwitch(commandID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, commandID)
}

// Cameras returns a copy of the live session's camera capabilities.
func (r *Registration) Cameras() []CameraCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]CameraCapability(nil), r.cameras...)
}

// Camera returns the capability of the camera with the given enum.
func (r *Registration) Camera(enum int) (CameraCapability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, cam := range r.cameras {
		if cam.CameraEnum == enum {
			return cam, true
		}
	}
	return CameraCapability{}, false
}

// errRegistrationInvalid is returned when a registration is missing its device
// id or its camera list.
var errRegistrationInvalid = errors.New("ws: device id and at least one camera are required")

// Registry mints single-use device WebSocket tickets and tracks the live
// session of every device.
type Registry struct {
	ttl time.Duration

	mu       sync.Mutex
	byID     map[string]*Registration // ticket id -> registration
	pending  map[string]*Registration // device id -> unused ticket
	byDevice map[string]*Registration // device id -> attached session
}

// NewRegistry creates a registry whose unused tickets expire after ttl.
func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{
		ttl:      ttl,
		byID:     make(map[string]*Registration),
		pending:  make(map[string]*Registration),
		byDevice: make(map[string]*Registration),
	}
}

// Create mints a ticket for deviceID with the announced cameras. Registering
// again invalidates the device's previous unused ticket; a live session is left
// untouched until a new connection replaces it.
func (g *Registry) Create(deviceID string, cameras []CameraCapability) (*Registration, error) {
	if deviceID == "" || len(cameras) == 0 {
		return nil, errRegistrationInvalid
	}

	id, err := newWebsocketID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	reg := &Registration{
		WebsocketID: id,
		DeviceID:    deviceID,
		cameras:     append([]CameraCapability(nil), cameras...),
		CreatedAt:   now,
		ExpiresAt:   now.Add(g.ttl),
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if prev := g.pending[deviceID]; prev != nil {
		delete(g.byID, prev.WebsocketID)
	}
	g.pending[deviceID] = reg
	g.byID[id] = reg
	return reg, nil
}

// Get returns the registration of a ticket, pending or attached. Tickets that
// were never attached expire after the registry TTL and are dropped lazily.
func (g *Registry) Get(websocketID string) (*Registration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	reg, ok := g.byID[websocketID]
	if !ok {
		return nil, false
	}
	if reg.client == nil && !time.Now().Before(reg.ExpiresAt) {
		g.drop(reg)
		return nil, false
	}
	return reg, true
}

// Attach binds a pending ticket to its connection and makes it the device's
// live session. It is single-use: it reports false when the ticket is unknown,
// expired, already attached or belongs to another device.
func (g *Registry) Attach(websocketID string, c *Client) (*Registration, bool) {
	if c == nil {
		return nil, false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	reg, ok := g.byID[websocketID]
	if !ok || reg.client != nil || reg.DeviceID != c.DeviceID() {
		return nil, false
	}
	if !time.Now().Before(reg.ExpiresAt) {
		g.drop(reg)
		return nil, false
	}

	reg.client = c
	if g.pending[reg.DeviceID] == reg {
		delete(g.pending, reg.DeviceID)
	}
	g.byDevice[reg.DeviceID] = reg
	return reg, true
}

// Release drops the registration of a closed connection. It is idempotent and
// never clears a newer ticket or session of the same device.
func (g *Registry) Release(websocketID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if reg, ok := g.byID[websocketID]; ok {
		g.drop(reg)
	}
}

// Current returns the attached, live session of deviceID.
func (g *Registry) Current(deviceID string) (*Registration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	reg := g.byDevice[deviceID]
	if reg == nil {
		return nil, false
	}
	return reg, true
}

// Invalidate drops the device's pending ticket, if any, and returns its live
// connection so the caller can close it. That connection's handler releases the
// live registration when it exits.
func (g *Registry) Invalidate(deviceID string) *Client {
	g.mu.Lock()
	defer g.mu.Unlock()

	if pending := g.pending[deviceID]; pending != nil {
		delete(g.byID, pending.WebsocketID)
		delete(g.pending, deviceID)
	}
	if reg := g.byDevice[deviceID]; reg != nil {
		return reg.client
	}
	return nil
}

// drop removes reg from every index. The caller must hold g.mu.
func (g *Registry) drop(reg *Registration) {
	delete(g.byID, reg.WebsocketID)
	if g.pending[reg.DeviceID] == reg {
		delete(g.pending, reg.DeviceID)
	}
	if g.byDevice[reg.DeviceID] == reg {
		delete(g.byDevice, reg.DeviceID)
	}
}

// newWebsocketID returns 32 random bytes as a 64-character hex string.
func newWebsocketID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ws: generate websocket id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
