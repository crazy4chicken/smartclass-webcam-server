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
// registration. Camera parameters are ephemeral: they live in the registration
// and are never persisted.
type CameraCapability struct {
	CameraEnum     int            `json:"camera_enum"`
	Resolution     string         `json:"resolution"`
	FPS            int            `json:"fps"`
	SupportedCodec []string       `json:"supported_codec"`
	Attrs          map[string]any `json:"attrs,omitempty"`
}

// Registration is one device registration. It is a pending, single-use ticket
// until it is attached to a connection; afterwards it is the device's live
// session.
type Registration struct {
	WebsocketID string
	DeviceID    string
	Cameras     []CameraCapability
	CreatedAt   time.Time
	ExpiresAt   time.Time

	client *Client // set once by Attach, never cleared
}

// Camera returns the capability of the camera with the given enum.
func (r *Registration) Camera(enum int) (CameraCapability, bool) {
	for _, cam := range r.Cameras {
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
		Cameras:     append([]CameraCapability(nil), cameras...),
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
