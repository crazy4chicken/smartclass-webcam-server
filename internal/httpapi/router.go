package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/storage"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

const (
	// camerasManagePermission guards every management route under /api.
	camerasManagePermission = "webcam:cameras:any"

	// jsonContentType is the content type of regular JSON responses.
	jsonContentType = "application/json; charset=utf-8"

	// problemContentType is the content type of RFC 9457 problem responses.
	problemContentType = "application/problem+json"

	// maxRequestBody bounds a decoded JSON request body.
	maxRequestBody = 1 << 20
)

// server holds the dependencies shared by the HTTP handlers.
type server struct {
	auth               *auth.Auth
	store              *store.Store
	hub                *ws.Hub
	storage            storage.ObjectStorage
	streamAccumulators map[string]*frameAccumulator // streamID → accumulator
	accMu              sync.Mutex
}

// NewRouter builds the HTTP router for the webcam server.
//
// Health endpoints and the camera WebSocket endpoint are public; every route
// under /api requires a verified Bearer token carrying camerasManagePermission.
func NewRouter(auth *auth.Auth, store *store.Store, hub *ws.Hub, storage storage.ObjectStorage) chi.Router {
	s := &server{
		auth:               auth,
		store:              store,
		hub:                hub,
		storage:            storage,
		streamAccumulators: make(map[string]*frameAccumulator),
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

	r.Route("/api", func(r chi.Router) {
		// Middleware verifies Bearer tokens when present; Require rejects
		// requests without verified claims and authorizes the permission.
		r.Use(s.auth.Middleware())
		r.Use(s.auth.Require(camerasManagePermission, nil))

		r.Get("/cameras", s.listCameras)
		r.Post("/cameras", s.createCamera)

		r.Route("/cameras/{id}", func(r chi.Router) {
			r.Get("/", s.getCamera)
			r.Put("/", s.updateCamera)
			r.Delete("/", s.deleteCamera)
			r.Post("/configure", s.configureCamera)
			r.Post("/stream/start", s.startStream)
			r.Post("/stream/stop", s.stopStream)
			r.Get("/streams", s.listStreams)
		})

		r.Route("/streams/{id}", func(r chi.Router) {
			r.Get("/", s.getStream)
			r.Get("/segments", s.listSegments)
		})
	})

	// Camera WebSocket endpoint. The upgrade request carries its access token
	// in the token query parameter, which the handler verifies.
	r.Get("/ws/camera/{id}", s.handleCameraWS)

	return r
}

// statusResponse is the response body of the health endpoints.
type statusResponse struct {
	Status string `json:"status"`
}

// handleHealthz handles GET /healthz.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// handleReadyz handles GET /readyz.
func (s *server) handleReadyz(w http.ResponseWriter, r *http.Request) {
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
