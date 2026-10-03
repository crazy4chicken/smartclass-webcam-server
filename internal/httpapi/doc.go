// Package httpapi exposes the webcam server's HTTP API over a chi router built
// by NewRouter.
//
// # Routes
//
// Public endpoints:
//
//	GET /healthz   liveness probe
//	GET /readyz    readiness probe
//
// Management endpoints under /api require a teamusers Bearer token carrying the
// webcam:cameras:any permission (401 without verified claims, 403 when denied):
//
//	GET    /api/cameras                    list cameras
//	POST   /api/cameras                    register a camera
//	GET    /api/cameras/{id}               fetch one camera
//	PUT    /api/cameras/{id}               update camera fields
//	DELETE /api/cameras/{id}               delete a camera and its streams
//	POST   /api/cameras/{id}/configure     push a configuration to a camera
//	POST   /api/cameras/{id}/stream/start  start a recording stream
//	POST   /api/cameras/{id}/stream/stop   stop the active stream
//	GET    /api/cameras/{id}/streams       list a camera's streams
//	GET    /api/streams/{id}               fetch a stream with its segments
//	GET    /api/streams/{id}/segments      list a stream's segments
//
// Camera devices connect over WebSocket, authenticating with an access token in
// the token query parameter:
//
//	GET /ws/camera/{id}?token=<access token>
//
// # Responses
//
// Collection endpoints return {"items": [...]}. Single-resource endpoints
// return the resource object itself. Errors use RFC 9457 problem details
// (application/problem+json) with type "about:blank"; 401 and 403 responses
// emitted by the teamusers middleware use the SDK's own
// {"allow": false, "reason": ...} shape instead.
//
// DocOperations lists every route together with its request and response
// shapes. cmd/genspec walks the router and reflects the table into
// docs/public/openapi.yaml, failing when a route and its metadata drift apart.
package httpapi

import (
	"strings"
	"time"

	apidocs "github.com/crazy4chicken/nsc-teamusers/apidocs/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// DocPermissionDeriver returns the permission that guards method and path for
// the generated reference. Every route under /api is covered by the single
// management permission; health and camera WebSocket routes are public and
// therefore carry no permission line.
func DocPermissionDeriver(_, path string) (anyKey, teamKey string) {
	if strings.HasPrefix(path, "/api/") {
		return camerasManagePermission, ""
	}
	return "", ""
}

// docSegmentWithURL mirrors segmentWithURL with the embedded segment flattened,
// so the reflected schema matches the JSON body the handler writes.
type docSegmentWithURL struct {
	ID          string    `json:"id"`
	StreamID    string    `json:"stream_id"`
	CameraID    string    `json:"camera_id"`
	SegmentSeq  int       `json:"segment_seq"`
	StorageKey  string    `json:"storage_key"`
	SizeBytes   int64     `json:"size_bytes"`
	DurationMs  int       `json:"duration_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	DownloadURL string    `json:"download_url,omitempty"`
}

// docStreamDetail mirrors streamDetail with the embedded stream flattened, so
// the reflected schema matches the JSON body the handler writes.
type docStreamDetail struct {
	ID        string                `json:"id"`
	CameraID  string                `json:"camera_id"`
	Status    domain.StreamStatus   `json:"status"`
	StartedAt time.Time             `json:"started_at"`
	EndedAt   *time.Time            `json:"ended_at,omitempty"`
	Metadata  domain.StreamMetadata `json:"metadata"`
	Segments  []docSegmentWithURL   `json:"segments"`
}

// docError builds one documented problem response. The code is rendered as the
// detail of the response example, matching the problem body handlers emit.
func docError(status int, code, title string) apidocs.ErrorDoc {
	return apidocs.ErrorDoc{Status: status, Code: code, Title: title}
}

var (
	docInvalidBody     = docError(400, "invalid JSON request body", "Invalid Request")
	docBodyTooLarge    = docError(413, "request body too large", "Request Entity Too Large")
	docBadLimit        = docError(400, "limit must be a positive integer", "Invalid Request")
	docUnauthorized    = docError(401, "authentication failed", "Unauthorized")
	docForbidden       = docError(403, "insufficient_permissions", "Forbidden")
	docCameraNotFound  = docError(404, "camera not found", "Not Found")
	docStreamNotFound  = docError(404, "stream not found", "Not Found")
	docCameraOffline   = docError(409, "camera is offline", "Conflict")
	docNotStreaming    = docError(409, "camera is not streaming", "Conflict")
	docCommandFailed   = docError(502, "camera connection is unavailable", "Bad Gateway")
	docInternalFailure = docError(500, "internal server error", "Internal Server Error")
)

var (
	cameraExample = map[string]any{
		"id":       "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"name":     "Room 101 front",
		"location": "Building A / Room 101",
		"status":   "online",
		"config": map[string]any{
			"resolution": "1920x1080",
			"fps":        30,
			"codec":      "h264",
			"bitrate":    4096,
		},
		"last_seen":  "2026-09-30T08:15:04Z",
		"created_at": "2026-09-01T09:00:00Z",
		"updated_at": "2026-09-30T08:15:04Z",
	}
	updatedCameraExample = map[string]any{
		"id":       "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"name":     "Room 101 front (moved)",
		"location": "Building A / Room 102",
		"status":   "online",
		"config": map[string]any{
			"resolution": "1280x720",
			"fps":        25,
			"codec":      "h264",
			"bitrate":    2048,
		},
		"last_seen":  "2026-09-30T08:15:04Z",
		"created_at": "2026-09-01T09:00:00Z",
		"updated_at": "2026-09-30T08:16:40Z",
	}
	streamExample = map[string]any{
		"id":         "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"camera_id":  "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"status":     "active",
		"started_at": "2026-09-30T08:00:00Z",
		"metadata": map[string]any{
			"resolution": "1920x1080",
			"fps":        30,
			"codec":      "h264",
		},
	}
	completedStreamExample = map[string]any{
		"id":         "01J8Z7STRM6E0R4T8V2X6Z0B4D8",
		"camera_id":  "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"status":     "completed",
		"started_at": "2026-09-30T07:15:00Z",
		"ended_at":   "2026-09-30T07:45:00Z",
		"metadata": map[string]any{
			"resolution":   "1920x1080",
			"fps":          30,
			"codec":        "h264",
			"total_frames": 54000,
		},
	}
	segmentExample = map[string]any{
		"id":          "01J8Z6SEGM5D9Q3S7U1W5Y9A3C7",
		"stream_id":   "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"camera_id":   "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"segment_seq": 12,
		"storage_key": "01J8Z4W3K5M7Q9R1T3V5X7Z9B1/01J8Z5STRM4C8N2P6R0T4V8X2Z6/20260930T080112_12.bin",
		"size_bytes":  1048576,
		"duration_ms": 6000,
		"created_at":  "2026-09-30T08:01:12Z",
	}
	segmentWithURLExample = map[string]any{
		"id":           "01J8Z6SEGM5D9Q3S7U1W5Y9A3C7",
		"stream_id":    "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"camera_id":    "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"segment_seq":  12,
		"storage_key":  "01J8Z4W3K5M7Q9R1T3V5X7Z9B1/01J8Z5STRM4C8N2P6R0T4V8X2Z6/20260930T080112_12.bin",
		"size_bytes":   1048576,
		"duration_ms":  6000,
		"created_at":   "2026-09-30T08:01:12Z",
		"download_url": "https://filehouse.example.edu/objects/webcam-segments/01J8Z6SEGM5D9Q3S7U1W5Y9A3C7?X-Amz-Expires=900",
	}
	streamDetailExample = map[string]any{
		"id":         "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"camera_id":  "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"status":     "active",
		"started_at": "2026-09-30T08:00:00Z",
		"metadata": map[string]any{
			"resolution": "1920x1080",
			"fps":        30,
			"codec":      "h264",
		},
		"segments": []any{segmentWithURLExample},
	}
	configureExample = map[string]any{
		"camera_id": "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"config": map[string]any{
			"resolution": "1280x720",
			"fps":        25,
			"codec":      "h264",
			"bitrate":    2048,
		},
	}
)

// DocOperations is the route contract used by the OpenAPI generator. Every
// route served by NewRouter must appear here exactly once, with its chi
// pattern, so cmd/genspec fails instead of publishing a stale reference.
var DocOperations = []apidocs.Operation{
	{
		Method:          "GET",
		Path:            "/healthz",
		Tag:             "Health",
		Summary:         "Check liveness",
		Description:     "Use this unauthenticated probe to determine whether the HTTP process is alive. It returns ok without touching the database, so orchestrators and load balancers can call it frequently.",
		Response:        statusResponse{},
		ResponseExample: map[string]string{"status": "ok"},
		Errors:          []apidocs.ErrorDoc{},
	},
	{
		Method:          "GET",
		Path:            "/readyz",
		Tag:             "Health",
		Summary:         "Check readiness",
		Description:     "Use this unauthenticated probe before routing traffic to the process. It reports ready as soon as the HTTP server serves requests; it does not check the database connection, so a database outage does not change this response.",
		Response:        statusResponse{},
		ResponseExample: map[string]string{"status": "ready"},
		Errors:          []apidocs.ErrorDoc{},
	},

	{
		Method:          "GET",
		Path:            "/api/cameras",
		Tag:             "Cameras",
		Summary:         "List cameras",
		Description:     "Use to enumerate every registered camera. Cameras are returned newest first; the collection is not paginated, so the whole fleet is always returned.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.Camera]{},
		ResponseExample: map[string]any{"items": []any{cameraExample}},
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden, docInternalFailure},
	},
	{
		Method:      "POST",
		Path:        "/api/cameras",
		Tag:         "Cameras",
		Summary:     "Register a camera",
		Description: "Use to add a camera to the fleet before it connects. New cameras start offline and turn online when they complete the WebSocket handshake. The request answers 201 Created with the camera in the body and its URL in the Location header.",
		Security:    "bearerAuth",
		Request:     cameraCreateRequest{},
		RequestExample: map[string]any{
			"name":     "Room 101 front",
			"location": "Building A / Room 101",
			"config": map[string]any{
				"resolution": "1920x1080",
				"fps":        30,
				"codec":      "h264",
				"bitrate":    4096,
			},
		},
		Response:        domain.Camera{},
		ResponseExample: cameraExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "name is required", "Invalid Request"),
			docBodyTooLarge,
			docUnauthorized,
			docForbidden,
			docInternalFailure,
		},
	},
	{
		Method:          "GET",
		Path:            "/api/cameras/{id}/",
		Tag:             "Cameras",
		Summary:         "Get a camera",
		Description:     "Use to fetch one camera's stored state, including its current connection status and configuration.",
		Security:        "bearerAuth",
		Response:        domain.Camera{},
		ResponseExample: cameraExample,
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden, docCameraNotFound, docInternalFailure},
	},
	{
		Method:      "PUT",
		Path:        "/api/cameras/{id}/",
		Tag:         "Cameras",
		Summary:     "Update a camera",
		Description: "Use to change a camera's name, location or configuration. The body is a partial update: present fields overwrite the stored values and absent fields keep theirs, and at least one field is required. The configuration is stored immediately but not delivered to the device; call POST /api/cameras/{id}/configure for that.",
		Security:    "bearerAuth",
		Request:     cameraUpdateRequest{},
		RequestExample: map[string]any{
			"name":     "Room 101 front (moved)",
			"location": "Building A / Room 102",
			"config": map[string]any{
				"resolution": "1280x720",
				"fps":        25,
				"codec":      "h264",
				"bitrate":    2048,
			},
		},
		Response:        domain.Camera{},
		ResponseExample: updatedCameraExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "request body must contain at least one of name, location or config", "Invalid Request"),
			docError(400, "name must not be empty", "Invalid Request"),
			docBodyTooLarge,
			docUnauthorized,
			docForbidden,
			docCameraNotFound,
			docInternalFailure,
		},
	},
	{
		Method:      "DELETE",
		Path:        "/api/cameras/{id}/",
		Tag:         "Cameras",
		Summary:     "Delete a camera",
		Description: "Use to remove a camera together with every stream and segment recorded for it. The response has no body; deleting an unknown camera answers 404 and changes nothing.",
		Security:    "bearerAuth",
		Errors:      []apidocs.ErrorDoc{docUnauthorized, docForbidden, docCameraNotFound, docInternalFailure},
	},
	{
		Method:      "POST",
		Path:        "/api/cameras/{id}/configure",
		Tag:         "Cameras",
		Summary:     "Configure a camera",
		Description: "Use to push new operational settings to a camera over its live WebSocket connection. The camera must be online (409 otherwise). The request answers 200 with the stored configuration; the settings are stored only after the command has been queued successfully, so a 502 leaves the stored configuration unchanged.",
		Security:    "bearerAuth",
		Request:     domain.CameraConfig{},
		RequestExample: map[string]any{
			"resolution": "1280x720",
			"fps":        25,
			"codec":      "h264",
			"bitrate":    2048,
		},
		Response:        configureResponse{},
		ResponseExample: configureExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyTooLarge,
			docUnauthorized,
			docForbidden,
			docCameraNotFound,
			docCameraOffline,
			docCommandFailed,
			docInternalFailure,
		},
	},

	{
		Method:          "POST",
		Path:            "/api/cameras/{id}/stream/start",
		Tag:             "Streams",
		Summary:         "Start a recording stream",
		Description:     "Use to begin recording a camera. The camera must be online. The server creates the stream record first, then tells the device to start sending frames and stores each uploaded chunk as a segment in object storage. The request answers 201 Created with the new stream and its URL in the Location header; a stream whose start command cannot be delivered is marked failed.",
		Security:        "bearerAuth",
		Response:        domain.Stream{},
		ResponseExample: streamExample,
		Errors: []apidocs.ErrorDoc{
			docUnauthorized,
			docForbidden,
			docCameraNotFound,
			docCameraOffline,
			docCommandFailed,
			docInternalFailure,
		},
	},
	{
		Method:          "POST",
		Path:            "/api/cameras/{id}/stream/stop",
		Tag:             "Streams",
		Summary:         "Stop the active stream",
		Description:     "Use to end a camera's active recording. The device is told to stop and buffered frames are flushed to object storage before the response is sent, so every segment of the finished recording is visible once this call returns. The request answers 200 with the stream marked completed; a camera that is offline or has no active stream answers 409.",
		Security:        "bearerAuth",
		Response:        domain.Stream{},
		ResponseExample: completedStreamExample,
		Errors: []apidocs.ErrorDoc{
			docUnauthorized,
			docForbidden,
			docCameraNotFound,
			docCameraOffline,
			docNotStreaming,
			docCommandFailed,
			docInternalFailure,
		},
	},
	{
		Method:          "GET",
		Path:            "/api/cameras/{id}/streams",
		Tag:             "Streams",
		Summary:         "List a camera's streams",
		Description:     "Use to browse the recording history of one camera, newest first. Pass limit to cap the number of streams returned; the default is 100 and the maximum is 1000.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.Stream]{},
		ResponseExample: map[string]any{"items": []any{streamExample, completedStreamExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden, docCameraNotFound, docInternalFailure},
	},
	{
		Method:          "GET",
		Path:            "/api/streams/{id}/",
		Tag:             "Streams",
		Summary:         "Get a stream",
		Description:     "Use to fetch one stream with its segments and a pre-signed download URL for each segment, valid for 15 minutes. Pass limit to cap the number of segments returned; the default is 100 and the maximum is 1000.",
		Security:        "bearerAuth",
		Response:        docStreamDetail{},
		ResponseExample: streamDetailExample,
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden, docStreamNotFound, docInternalFailure},
	},
	{
		Method:          "GET",
		Path:            "/api/streams/{id}/segments",
		Tag:             "Streams",
		Summary:         "List stream segments",
		Description:     "Use to enumerate the stored chunks of one stream in recording order. Pass limit to cap the number returned; the default is 100 and the maximum is 1000. Segments carry storage keys only; use GET /api/streams/{id} when download URLs are needed.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.StreamSegment]{},
		ResponseExample: map[string]any{"items": []any{segmentExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden, docStreamNotFound, docInternalFailure},
	},

	{
		Method:      "GET",
		Path:        "/ws/camera/{id}",
		Tag:         "WebSocket",
		Summary:     "Open the camera WebSocket",
		Description: "Use from a camera device to open its authenticated realtime channel. The teamusers access token travels in the token query parameter because WebSocket clients cannot set an Authorization header; a missing or invalid token answers 401 before any upgrade. A successful handshake answers 101 Switching Protocols and the connection then carries binary frame messages; the response below records the no-body default because the success is a protocol switch rather than a JSON document.",
		Errors: []apidocs.ErrorDoc{
			docError(401, "websocket authentication failed", "Unauthorized"),
			docCameraNotFound,
			docInternalFailure,
		},
	},
}
