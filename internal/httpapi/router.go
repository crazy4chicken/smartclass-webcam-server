package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/storage"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

const (
	// jsonContentType is the content type of regular JSON responses.
	jsonContentType = "application/json; charset=utf-8"

	// problemContentType is the content type of RFC 9457 problem responses.
	problemContentType = "application/problem+json"

	// maxRequestBody bounds a decoded JSON request body.
	maxRequestBody = 1 << 20
)

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	auth     *auth.Auth
	store    *store.Store
	hub      *ws.Hub
	registry *ws.Registry
	objects  storage.ObjectStorage
	media    *mediaManager
}

// NewRouter builds the HTTP router for the webcam server.
//
// Health probes and the device WebSocket endpoints are public; /ws/register
// authenticates the device with its own long-lived token. Every route under
// /api requires a verified Bearer token and authorizes a cam:<action>:<scope>
// permission resolved against the device by RequireCollection or RequireDevice.
func NewRouter(authn *auth.Auth, st *store.Store, hub *ws.Hub, registry *ws.Registry, objects storage.ObjectStorage) *chi.Mux {
	s := &Server{
		auth:     authn,
		store:    st,
		hub:      hub,
		registry: registry,
		objects:  objects,
		media:    newMediaManager(st, objects, registry, hub),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Location", "X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	// The device plane registers over HTTP with its device token, then redeems
	// the returned single-use ticket on the WebSocket route.
	r.Get("/ws/register", s.handleDeviceRegister)
	r.Get("/ws/device/{device_websocket_id}", s.handleDeviceWS)

	r.Route("/api", func(r chi.Router) {
		// Middleware verifies Bearer tokens when present; the per-route
		// RequireCollection and RequireDevice middleware below enforce the
		// cam:<action>:<scope> permission of each route.
		r.Use(s.auth.Middleware())

		r.With(s.auth.RequireCollection("read")).Get("/devices", s.handleDeviceList)
		r.With(s.auth.RequireCollection("manage")).Post("/devices", s.handleDeviceCreate)

		r.Route("/devices/{device_id}", func(r chi.Router) {
			r.With(s.auth.RequireDevice("read", s.loadDeviceFromPath)).Get("/", s.handleDeviceGet)
			r.With(s.auth.RequireDevice("manage", s.loadDeviceFromPath)).Put("/", s.handleDeviceUpdate)
			r.With(s.auth.RequireDevice("manage", s.loadDeviceFromPath)).Delete("/", s.handleDeviceDelete)
			r.With(s.auth.RequireDevice("manage", s.loadDeviceFromPath)).Post("/token", s.handleDeviceTokenRotate)

			r.With(s.auth.RequireDevice("control", s.loadDeviceFromPath)).Post("/camera/switch", s.handleCameraSwitch)
			r.With(s.auth.RequireDevice("control", s.loadDeviceFromPath)).Post("/recording/start", s.handleRecordingStart)
			r.With(s.auth.RequireDevice("control", s.loadDeviceFromPath)).Post("/recording/stop", s.handleRecordingStop)
			r.With(s.auth.RequireDevice("control", s.loadDeviceFromPath)).Post("/photo", s.handlePhotoCapture)

			r.With(s.auth.RequireDevice("read", s.loadDeviceFromPath)).Get("/streams", s.handleDeviceStreams)
			r.With(s.auth.RequireDevice("read", s.loadDeviceFromPath)).Get("/photos", s.handleDevicePhotos)
		})

		r.With(s.auth.RequireDevice("read", s.loadDeviceFromStream)).Get("/streams/{stream_id}/", s.handleStreamGet)
		r.With(s.auth.RequireDevice("read", s.loadDeviceFromStream)).Get("/streams/{stream_id}/segments", s.handleStreamSegments)
		r.With(s.auth.RequireDevice("read", s.loadDeviceFromPhoto)).Get("/photos/{photo_id}/", s.handlePhotoGet)
	})

	return r
}

// statusResponse is the response body of the health endpoints.
type statusResponse struct {
	Status string `json:"status"`
}

// handleHealthz handles GET /healthz.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// handleReadyz handles GET /readyz.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ready"})
}

// listResponse is the envelope returned by every collection endpoint.
type listResponse[T any] struct {
	Items []T `json:"items"`
}

// newListResponse wraps items, encoding a nil slice as [].
func newListResponse[T any](items []T) listResponse[T] {
	if items == nil {
		items = []T{}
	}
	return listResponse[T]{Items: items}
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// problemDetails is an RFC 9457 problem response body.
type problemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem writes an RFC 9457 problem+json response.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	p := problemDetails{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
	if r != nil && r.URL != nil {
		p.Instance = r.URL.Path
	}

	body, err := json.Marshal(p)
	if err != nil {
		slog.Error("encode problem response", "error", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeStoreError maps a store error to a problem response: ErrNotFound becomes
// a 404 with notFound as detail, any other error a generic 500.
func writeStoreError(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, notFound)
		return
	}
	slog.Error("store operation failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeProblem(w, r, http.StatusInternalServerError, "internal server error")
}

// decodeJSON decodes the request body into dst. It reports false after writing
// a 400 problem response when the body is not a single JSON object.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, r, http.StatusBadRequest, "request body must contain a single JSON object")
		return false
	}
	return true
}

// parseLimit reads the optional limit query parameter. A missing parameter
// yields zero, which applies the store's default limit.
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		writeProblem(w, r, http.StatusBadRequest, "limit must be a positive integer")
		return 0, false
	}
	return limit, true
}
