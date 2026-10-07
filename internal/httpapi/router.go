package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
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

// Server holds the dependencies shared by the HTTP handlers. sanitize strips
// configured secrets from any text echoed into an error body; it may be nil.
type Server struct {
	auth     *auth.Auth
	store    *store.Store
	hub      *ws.Hub
	registry *ws.Registry
	objects  storage.ObjectStorage
	sanitize func(string) string
	media    *mediaManager
}

// NewRouter builds the HTTP router for the webcam server.
//
// Health probes and the device WebSocket endpoints are public; /ws/register
// authenticates the device with its own long-lived token. Every route under
// /api requires a verified Bearer token and authorizes a cam:<action>:<scope>
// permission resolved against the device by RequireCollection or RequireDevice.
//
// sanitize removes configured secrets from any error text that becomes part of
// a problem detail; pass nil to leave the text untouched.
func NewRouter(authn *auth.Auth, st *store.Store, hub *ws.Hub, registry *ws.Registry, objects storage.ObjectStorage, sanitize func(string) string) *chi.Mux {
	s := &Server{
		auth:     authn,
		store:    st,
		hub:      hub,
		registry: registry,
		objects:  objects,
		sanitize: sanitize,
		media:    newMediaManager(st, objects, registry, hub),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Location", "X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// An unknown path and a known path with the wrong method answer the same
	// problem shape as every other error instead of chi's plain-text defaults,
	// naming the method and path that did not match. A custom 405 handler
	// drops the Allow header chi derives, so the handler rebuilds it from the
	// routing tree.
	mux := r
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		detail := fmt.Sprintf("method %s is not allowed on %s", r.Method, r.URL.Path)
		if allowed := allowedMethods(mux, r); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			detail += "; allowed: " + strings.Join(allowed, ", ")
		}
		writeProblem(w, r, http.StatusMethodNotAllowed, detail)
	})

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
			r.With(s.auth.RequireDevice("read", "device", s.loadDeviceFromPath)).Get("/", s.handleDeviceGet)
			r.With(s.auth.RequireDevice("manage", "device", s.loadDeviceFromPath)).Put("/", s.handleDeviceUpdate)
			r.With(s.auth.RequireDevice("manage", "device", s.loadDeviceFromPath)).Delete("/", s.handleDeviceDelete)
			r.With(s.auth.RequireDevice("manage", "device", s.loadDeviceFromPath)).Post("/token", s.handleDeviceTokenRotate)

			r.With(s.auth.RequireDevice("control", "device", s.loadDeviceFromPath)).Post("/camera/switch", s.handleCameraSwitch)
			r.With(s.auth.RequireDevice("control", "device", s.loadDeviceFromPath)).Post("/recording/start", s.handleRecordingStart)
			r.With(s.auth.RequireDevice("control", "device", s.loadDeviceFromPath)).Post("/recording/stop", s.handleRecordingStop)
			r.With(s.auth.RequireDevice("control", "device", s.loadDeviceFromPath)).Post("/photo", s.handlePhotoCapture)

			r.With(s.auth.RequireDevice("read", "device", s.loadDeviceFromPath)).Get("/streams", s.handleDeviceStreams)
			r.With(s.auth.RequireDevice("read", "device", s.loadDeviceFromPath)).Get("/photos", s.handleDevicePhotos)
		})

		r.With(s.auth.RequireDevice("read", "stream", s.loadDeviceFromStream)).Get("/streams/{stream_id}/", s.handleStreamGet)
		r.With(s.auth.RequireDevice("read", "stream", s.loadDeviceFromStream)).Get("/streams/{stream_id}/segments", s.handleStreamSegments)
		r.With(s.auth.RequireDevice("read", "photo", s.loadDeviceFromPhoto)).Get("/photos/{photo_id}/", s.handlePhotoGet)
	})

	return r
}

// routedMethods are the request methods the router can serve; the 405 probe
// checks each of them against the routing tree.
var routedMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect,
}

// allowedMethods reports which methods the routing tree serves for the request
// path, so a custom 405 handler answers the same Allow header chi's default
// handler emitted.
func allowedMethods(mux *chi.Mux, r *http.Request) []string {
	var allowed []string
	for _, method := range routedMethods {
		if method == r.Method {
			continue
		}
		if mux.Match(chi.NewRouteContext(), method, r.URL.Path) {
			allowed = append(allowed, method)
		}
	}
	return allowed
}

// recoverer turns a handler panic into a problem response and logs the panic
// with its stack. It replaces chi's middleware.Recoverer, which prints the
// stack to stderr and leaves the caller with an empty body.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// ErrAbortHandler is the documented way to abort a response on
			// purpose, so it is not a fault to report.
			if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}
			slog.Error("recovered from panic", "method", r.Method, "path", r.URL.Path,
				"panic", recovered, "stack", string(debug.Stack()))
			writeProblem(w, r, http.StatusInternalServerError,
				redact.Trim("panic: "+redact.Text(s.sanitize, fmt.Sprint(recovered)), 300))
		}()
		next.ServeHTTP(w, r)
	})
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
		// A marshal error names the response type it could not encode, never
		// request data, and is bounded like every other echoed cause.
		http.Error(w, redact.Trim("encode response failed: "+err.Error(), 300), http.StatusInternalServerError)
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
		// The problem body cannot be built here, so the fallback names the
		// failing operation and the marshal error, which names a Go type and
		// never request data.
		http.Error(w, redact.Trim("encode problem response failed: "+err.Error(), 300), status)
		return
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// fail answers a 500 with the failing operation and the sanitized cause, and
// logs the untouched error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, operation string, err error) {
	slog.Error(operation, "method", r.Method, "path", r.URL.Path, "error", err)
	writeProblem(w, r, http.StatusInternalServerError,
		redact.Trim(operation+" failed: "+redact.Text(s.sanitize, err.Error()), 300))
}

// writeStoreError maps a store error to a problem response: ErrNotFound becomes
// a 404 with notFound as detail, ErrConflict a 409 with conflict, and any other
// error a 500 naming operation and cause. A call site whose operation cannot
// conflict passes an empty conflict and falls through to the 500.
func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, operation string, err error, notFound, conflict string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, notFound)
	case conflict != "" && errors.Is(err, store.ErrConflict):
		writeProblem(w, r, http.StatusConflict, conflict)
	default:
		s.fail(w, r, operation, err)
	}
}

// decodeJSON decodes the request body into dst. It reports false after writing
// a 400 problem response when the body is not a single JSON object. The decoder
// message is sanitized and truncated because it can quote the body it rejected.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeProblem(w, r, http.StatusBadRequest,
			redact.Trim("invalid JSON request body: "+redact.Text(s.sanitize, err.Error()), 300))
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
